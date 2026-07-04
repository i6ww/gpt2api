package geelark

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// 取 WhatsApp OTP 的 shell 命令模板（按优先级从高到低）。
//
// **重要**：以前用 `grep -oE '[0-9]{6}' | head -20` 直接抓所有 6 位数字，会
// 误把 dumpsys 输出里的 groupKey / 通知内部 ID / channelId / timestamp 后缀
// 等当成 OTP。例如 WhatsApp 收到的真 OTP 是 `920401`，但 dump 里第一个 6 位
// 数字可能是 `177856`（某个 NotificationRecord uid），结果 GoPay 验证失败。
//
// 现在改为：**先 grep 含关键词的行 + 周边 3 行上下文**，把"GoPay 920401 adalah
// kode verifikasi Anda" 这类有语义的句子保留下来；再在 Go 里用"关键词附近的
// 数字"做精确匹配。这样既不漏（保留多种命令兜底），又避免误识别。
//
// 默认链：
//  1. dumpsys notification --noredact + grep 关键词
//  2. dumpsys notification 不带 --noredact（某些 ROM 用 --noredact 反报权限错）
//  3. cmd notification list-recent（Android 12+）
//  4. /sdcard/wa_otp.log（NotificationListener 兜底 app）
//  5. content://sms/inbox（极少数发送商同时走 SMS）
var defaultOTPCommands = []string{
	`dumpsys notification --noredact 2>/dev/null | grep -iE 'whatsapp|gopay|otp|kode|verif|code|adalah' -B1 -A2 | head -120`,
	`dumpsys notification 2>/dev/null | grep -iE 'whatsapp|gopay|otp|kode|verif|code|adalah' -B1 -A2 | head -120`,
	`cmd notification list-recent 2>/dev/null | head -100`,
	`tail -n 30 /sdcard/wa_otp.log 2>/dev/null`,
	`content query --uri content://sms/inbox --projection body --sort '_id DESC LIMIT 3' 2>/dev/null | head -10`,
}

// diagDumpCmd 在 fetch 失败时执行一次，把云手机里的通知原貌完整 dump 到任务
// 日志（限制 ~3000 字符避免刷屏）。专门用来诊断"OTP 没被识别"问题。
const diagDumpCmd = `dumpsys notification --noredact 2>/dev/null | grep -iE 'whatsapp|otp|verif|code|gopay|kode|adalah' -B1 -A4 | head -120`

// OTP 提取的多模式匹配（按优先级从高到低）。
//
// 对齐 Python 参考实现 `payment-adapter/CTF-pay/gopay.py`，但比 Python 更严格：
//
//  1. 关键词只用"两词正文短语"（adalah kode / kode verifikasi / one-time / OTP code），
//     故意**不用** "verifikasi" / "code" / "kode" / "whatsapp" 这种容易在 dumpsys
//     元数据里频繁出现的单词（NotificationRecord 字段、channelId、groupKey 等）。
//  2. 数字和关键词之间最多隔 **30 个非数字字符**（不是 80），避免跨段误命中。
//     真实 OTP 模板 "078964 adalah kode" 只隔 7 字符，"Kode verifikasi Anda: 123456"
//     只隔 21 字符，30 字符窗口足够覆盖所有真实模板，又能拒绝跨字段误命中。
//
//   - p1: 关键词在前，数字紧随其后（≤30 字符）：
//     "Your OTP code is 123456" / "Kode verifikasi Anda: 123456"
//   - p2: 数字在前，关键词紧随其后（≤30 字符）：
//     "123456 adalah kode verifikasi Anda"  ← GoPay 印尼语真实模板
//   - p3: 仅 6 位数字兜底（防止 dumpsys 输出格式完全反常时不至于完全识别不出）。
//
// Go regexp（RE2）不支持 lookbehind，用 `(?:^|\D)` `(?:\D|$)` 替代 `(?<!\d)` `(?!\d)`。
var otpRegexesOrdered = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:otp\s+code|one[-\s]*time\s*(?:code|password)|kode\s+verifikasi|kode\s+otp|verification\s+code|verifikasi\s+anda)\D{0,30}(?:^|\D)(\d{6})(?:\D|$)`),
	regexp.MustCompile(`(?i)(?:^|\D)(\d{6})(?:\D|$)\D{0,30}(?:adalah\s+kode|is\s+your\s+(?:otp|verif|one[-\s]*time)|kode\s+verifikasi|verification\s+code)`),
	regexp.MustCompile(`(?:^|\D)(\d{6})(?:\D|$)`),
}

// defaultOTPRegex 仍保留，供 SnapshotExistingOTPs / 自定义 Options.Regex 使用。
var defaultOTPRegex = otpRegexesOrdered[2]

// OTPOptions 提取 OTP 的可调参数。
type OTPOptions struct {
	// Commands 自定义 shell 命令链；空则用 defaultOTPCommands
	Commands []string
	// Regex 提取数字的正则；nil 则用 defaultOTPRegex（6 位）
	Regex *regexp.Regexp
	// PollInterval 轮询间隔；默认 2s
	PollInterval time.Duration
	// Timeout 总超时；默认 180s
	Timeout time.Duration
	// IssuedAfter 只接受这个时间点之后的 OTP（防止读到旧通知）
	// 业务上：dispatcher 在调 GoPay user-consent **之前** 记一个时间戳，
	// 然后用这个时间戳作为 IssuedAfter 调本函数，确保拿到的是新触发的 OTP。
	// 注意 dumpsys/content 输出本身没带时间戳，所以做法是：
	//   - 调用前先一次性 `clear` （读一遍当前残留 OTP，记录到 seen 集合）
	//   - 之后每轮拉到的若 ∈ seen 就跳过
	IssuedAfter time.Time
	// OnLog 调试日志回调；nil 表示不输出
	OnLog func(format string, args ...any)
}

// FetchWhatsAppOTP 在指定云手机里轮询提取 6 位 WhatsApp OTP。
//
// 用法：
//
//	// 1. 触发 GoPay user-consent 之前先 clear
//	seen, _ := geelark.SnapshotExistingOTPs(ctx, gl, token, phoneID)
//	// 2. 触发 user-consent
//	gopay.UserConsent(...)
//	// 3. 等 OTP
//	otp, err := gl.FetchWhatsAppOTP(ctx, token, phoneID, geelark.OTPOptions{
//	    Timeout: 3*time.Minute,
//	    OnLog: func(f string, a ...any) { logger.Info(fmt.Sprintf(f, a...)) },
//	}, seen)
//
// seen 是一个 map[string]struct{}，函数在轮询时跳过其中已存在的 OTP。
// 传入 nil 等价空集合（拉到啥就用啥，可能撞到旧 OTP）。
func (c *Client) FetchWhatsAppOTP(ctx context.Context, token, phoneID string, opt OTPOptions, seen map[string]struct{}) (string, error) {
	cmds := opt.Commands
	if len(cmds) == 0 {
		cmds = defaultOTPCommands
	}
	// 用户显式传 Regex 时只用那一条；否则用按优先级排序的三档默认 regex（关键词
	// 在前、关键词在后、纯 6 位兜底）。这样能把 "123456 adalah kode verifikasi"
	// 这种印尼语模板里的真 OTP 跟 dumpsys 输出里的 groupKey/uid 等内部 6 位数字
	// 区分开来 —— 优先取"伴随关键词的数字"，没有再退化到纯 6 位匹配。
	regexes := otpRegexesOrdered
	if opt.Regex != nil {
		regexes = []*regexp.Regexp{opt.Regex}
	}
	pollInterval := opt.PollInterval
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	if seen == nil {
		seen = map[string]struct{}{}
	}
	logf := opt.OnLog
	if logf == nil {
		logf = func(string, ...any) {}
	}

	logf("[geelark/otp] start polling phone=%s timeout=%.0fs cmds=%d seen_count=%d",
		phoneID, timeout.Seconds(), len(cmds), len(seen))

	deadline := time.Now().Add(timeout)
	pollIdx := 0
	// 累计每条命令的"是否成功 stdout 有内容"，方便最终诊断
	cmdHits := make([]int, len(cmds))
	for time.Now().Before(deadline) {
		pollIdx++
		// 每轮：按优先级走 regex，每条 regex 都试遍所有命令，找到第一个匹配
		// 就返回。这样能保证如果某条命令拿到了"含关键词的真 OTP 上下文"，会
		// 优先于纯 6 位数字命中。
		for ri, re := range regexes {
			for ci, cmd := range cmds {
				out, err := c.ShellExecute(ctx, token, phoneID, cmd)
				if err != nil {
					if ri == 0 && (pollIdx == 1 || pollIdx%10 == 0) {
						logf("[geelark/otp] shell err cmd#%d (retry): %v", ci, err)
					}
					continue
				}
				if out == nil || strings.TrimSpace(out.Output) == "" {
					continue
				}
				if ri == 0 {
					cmdHits[ci]++
				}
				candidates := re.FindAllStringSubmatchIndex(out.Output, -1)
				for _, idx := range candidates {
					if len(idx) < 4 {
						continue
					}
					code := out.Output[idx[2]:idx[3]]
					if _, dup := seen[code]; dup {
						continue
					}
					// 脱敏 OTP（"957060" → "95***0"），不再打印通知原文上下文。
					masked := code
					if len(masked) > 2 {
						masked = masked[:2] + strings.Repeat("*", len(masked)-3) + masked[len(masked)-1:]
					}
					logf("已识别 OTP %s（匹配规则 #%d，第 %d 轮）", masked, ri, pollIdx)
					_ = ci
					return code, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	// 超时前打一次完整诊断：所有命令的命中数 + dumpsys 真实片段。
	logf("[geelark/otp] timeout phone=%s polls=%d cmd_hits=%v seen=%d — 下面 dump dumpsys notification 实际内容用于诊断",
		phoneID, pollIdx, cmdHits, len(seen))
	if out, err := c.ShellExecute(ctx, token, phoneID, diagDumpCmd); err == nil && out != nil {
		raw := strings.TrimSpace(out.Output)
		if raw == "" {
			logf("[geelark/otp] DIAG dumpsys (filter whatsapp/otp/code) 为空 — WhatsApp 通知未进入系统通知中心")
		} else {
			snippet := raw
			if len(snippet) > 3000 {
				snippet = snippet[:3000] + "...[truncated]"
			}
			logf("[geelark/otp] DIAG dumpsys raw (3000 chars):\n%s", snippet)
		}
	} else if err != nil {
		logf("[geelark/otp] DIAG dumpsys 执行失败：%v", err)
	}
	return "", &Error{Phase: "timeout", Msg: "WhatsApp OTP timeout"}
}

// SnapshotExistingOTPs 在等 OTP 之前调用，记录当前手机里已有的所有 6 位数字。
// 返回的 set 直接传给 FetchWhatsAppOTP 当作"屏蔽列表"。
func (c *Client) SnapshotExistingOTPs(ctx context.Context, token, phoneID string) (map[string]struct{}, error) {
	seen := map[string]struct{}{}
	for _, cmd := range defaultOTPCommands {
		out, err := c.ShellExecute(ctx, token, phoneID, cmd)
		if err != nil {
			// 单条失败不致命：可能某些 cmd 在这台手机上没权限，继续下一条
			continue
		}
		if out == nil || strings.TrimSpace(out.Output) == "" {
			continue
		}
		for _, m := range defaultOTPRegex.FindAllStringSubmatch(out.Output, -1) {
			if len(m) >= 2 {
				seen[m[1]] = struct{}{}
			}
		}
	}
	return seen, nil
}
