package geelark

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 云手机上 GoPay / Gojek 包名：印尼地区常见为 Gojek 主包内含 GoPay；若使用独立
// GoPay 安装包请通过 UnlinkOpenAIOptions.AppPackage 覆盖。
const (
	DefaultGojekAppPackage = "com.gojek.app"
)

// UnlinkOpenAIOptions 在云手机内自动化：GoPay → 已连接应用 → 移除 OpenAI。
//
// 依赖：GeeLark /shell/execute 能执行 adb shell（与 OTP 抓取相同通路）。
// 实现策略：轮询 uiautomator dump，在 accessibility 树里按印尼语文案匹配可点击
// 节点的 bounds，再 input tap 中心点；找不到则上滑翻页或按返回键。
type UnlinkOpenAIOptions struct {
	// AppPackage 为空则使用 DefaultGojekAppPackage；设为 "-" 表示不自动拉起 App
	//（假定当前已在 GoPay 前台）。
	AppPackage string
	// StepDelay 每步完成后停顿（等待界面动画）。
	StepDelay time.Duration
	// MaxSteps 最大「识别+点击」轮数，避免死循环。
	MaxSteps int
	// Swipe 翻页：`input swipe` 起止坐标（竖屏自下向上滑）。
	SwipeX1, SwipeY1, SwipeX2, SwipeY2 int
	// OnLog 调试输出；nil 则静默。
	OnLog func(format string, args ...any)
}

// UnlinkOpenAIInGopay 尝试在云手机上去掉「Aplikasi yang terhubung」里的 OpenAI
// 连接（用户截图中的「Hapus」流程）。
//
// 成功条件（满足其一即返回 nil）：
//   1) 界面 dump 中已不再出现 OpenAI LLC / OpenAI 相关文案；
//   2) 已连续多轮未找到任何可点击目标且未检测到 OpenAI（认为已清完）。
//
// 失败：超过 MaxSteps、shell 连续失败、ctx 取消。
func (c *Client) UnlinkOpenAIInGopay(ctx context.Context, token, phoneID string, opt UnlinkOpenAIOptions) error {
	if c == nil {
		return &Error{Phase: "param", Msg: "geelark client nil"}
	}
	if token == "" || phoneID == "" {
		return &Error{Phase: "param", Msg: "token or phoneID empty"}
	}
	stepDelay := opt.StepDelay
	if stepDelay <= 0 {
		stepDelay = 900 * time.Millisecond
	}
	maxSteps := opt.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 35
	}
	x1, y1, x2, y2 := opt.SwipeX1, opt.SwipeY1, opt.SwipeX2, opt.SwipeY2
	if x1 == 0 && y1 == 0 && x2 == 0 && y2 == 0 {
		// 默认按常见 1080p 竖屏：从下向上滑一段
		x1, y1, x2, y2 = 540, 1800, 540, 600
	}
	pkg := strings.TrimSpace(opt.AppPackage)
	if pkg == "" {
		pkg = DefaultGojekAppPackage
	}
	logf := opt.OnLog
	if logf == nil {
		logf = func(string, ...any) {}
	}

	launchCmd := ""
	if pkg != "" && pkg != "-" {
		launchCmd = fmt.Sprintf(
			`am start -a android.intent.action.MAIN -c android.intent.category.LAUNCHER -p %s 2>/dev/null; sleep 2`,
			pkg,
		)
	}

	// 每轮优先匹配的文案（越靠前越优先 —— 先处理弹窗再处理导航）。
	// 使用子串匹配：content-desc / text 包含即命中。
	tapTargets := []string{
		"Hapus",
		"Ya,",
		"Ya",
		"Lanjut",
		"OK",
		"OpenAI",
		"Aplikasi yang terhubung",
		"Pengaturan akun & aplikasi",
		"Pengaturan & keamanan",
		"Perlindungan akun",
		"Profil",
	}

	if launchCmd != "" {
		if _, err := c.ShellExecute(ctx, token, phoneID, launchCmd); err != nil {
			return err
		}
		logf("已尝试拉起应用包 %s", pkg)
	}

	staleSwipe := 0
	for step := 0; step < maxSteps; step++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		xml, err := c.dumpWindowHierarchy(ctx, token, phoneID)
		if err != nil {
			return err
		}
		if !strings.Contains(strings.ToLower(xml), "openai") {
			logf("界面中未再检测到 OpenAI，认为解绑已完成（第 %d 步）", step+1)
			return nil
		}

		tapped := false
		for _, label := range tapTargets {
			if x, y, ok := findClickableCenterForText(xml, label); ok {
				cmd := fmt.Sprintf("input tap %d %d", x, y)
				out, err := c.ShellExecute(ctx, token, phoneID, cmd)
				if err != nil {
					return err
				}
				if out != nil && !out.Status {
					return &Error{Phase: "api", Msg: "input tap 执行失败: " + truncate(out.Output, 120)}
				}
				logf("已点击含「%s」的控件（坐标 %d,%d）", label, x, y)
				tapped = true
				break
			}
		}

		if tapped {
			staleSwipe = 0
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timeAfter(stepDelay):
			}
			continue
		}

		// 无匹配：上滑或返回
		if staleSwipe < 3 {
			sw := fmt.Sprintf("input swipe %d %d %d %d 320", x1, y1, x2, y2)
			if _, err := c.ShellExecute(ctx, token, phoneID, sw); err != nil {
				return err
			}
			logf("未命中可点控件，执行上滑翻页")
			staleSwipe++
		} else {
			if _, err := c.ShellExecute(ctx, token, phoneID, "input keyevent 4"); err != nil {
				return err
			}
			logf("多次翻页无进展，按返回键")
			staleSwipe = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeAfter(stepDelay):
		}
	}
	return &Error{Phase: "timeout", Msg: fmt.Sprintf("GoPay 解绑步骤超过上限 %d，请人工核对界面", maxSteps)}
}

// dumpWindowHierarchy uiautomator dump + 读回 XML。
func (c *Client) dumpWindowHierarchy(ctx context.Context, token, phoneID string) (string, error) {
	dumpPath := "/sdcard/gopay_unlink_ui.xml"
	cmd := fmt.Sprintf(`uiautomator dump %s 2>/dev/null; cat %s 2>/dev/null`, dumpPath, dumpPath)
	out, err := c.ShellExecute(ctx, token, phoneID, cmd)
	if err != nil {
		return "", err
	}
	if out == nil || !out.Status {
		return "", &Error{Phase: "api", Msg: "uiautomator dump 失败: " + truncate(safeOut(out), 200)}
	}
	return out.Output, nil
}

func safeOut(out *ShellExecuteData) string {
	if out == nil {
		return ""
	}
	return out.Output
}

var (
	reNodeLine       = regexp.MustCompile(`<node\b[^>]*>`)
	reBoundsAttr     = regexp.MustCompile(`bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"`)
	reTextAttr       = regexp.MustCompile(`\btext="([^"]*)"`)
	reDescAttr       = regexp.MustCompile(`\bcontent-desc="([^"]*)"`)
	reClickableTrue  = regexp.MustCompile(`\bclickable="true"`)
)

// findClickableCenterForText 在 uiautomator dump 的单行 node 中查找 text 或
// content-desc 包含 sub 且 clickable=true 的节点，返回 bounds 中心点。
func findClickableCenterForText(xml, sub string) (x, y int, ok bool) {
	subLower := strings.ToLower(sub)
	for _, line := range reNodeLine.FindAllString(xml, -1) {
		if !reClickableTrue.MatchString(line) {
			continue
		}
		tm := reTextAttr.FindStringSubmatch(line)
		dm := reDescAttr.FindStringSubmatch(line)
		hay := ""
		if len(tm) >= 2 {
			hay += strings.ToLower(tm[1]) + " "
		}
		if len(dm) >= 2 {
			hay += strings.ToLower(dm[1]) + " "
		}
		if hay == "" || !strings.Contains(hay, subLower) {
			continue
		}
		b := reBoundsAttr.FindStringSubmatch(line)
		if len(b) != 5 {
			continue
		}
		x0, _ := strconv.Atoi(b[1])
		y0, _ := strconv.Atoi(b[2])
		x1, _ := strconv.Atoi(b[3])
		y1, _ := strconv.Atoi(b[4])
		return (x0 + x1) / 2, (y0 + y1) / 2, true
	}
	return 0, 0, false
}

// timeAfter 便于单测替换。
var timeAfter = time.After
