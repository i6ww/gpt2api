// Command xailogin 在有浏览器的机器上完成官方 xAI（Grok CLI）OAuth PKCE 登录，
// 半自动地把 access_token / refresh_token 抓出来，输出可直接导入 pool_xai 的 JSON
// （后台「xAI 号池 / 导入」粘贴整段 JSON 数组即可）。
//
// 双击运行（Windows）：会交互式询问代理、登录几个号，逐个弹浏览器，
// 你手动登录过掉验证码 / 2FA，登录成功后自动收 OAuth code 换 token，
// 全部完成后把结果合并写到 exe 同目录的 xai-accounts.json。
//
// 命令行用法（可选，给出 flag 就跳过对应交互）：
//
//	xailogin                                  # 全交互
//	xailogin -n 2                             # 连登 2 个号
//	xailogin -proxy http://user:pass@host:port -n 2
//	xailogin -out d:\xai.json -n 2
//	xailogin -no-open                         # 不自动开浏览器，自己用无痕窗口打开
//
// 流程：discovery → 打开浏览器授权 → 本地 loopback 回调收 code → 换 token → 汇总输出。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/regkit/xairefresh"
)

func main() {
	proxyFlag := flag.String("proxy", "", "出口代理 URL（http://user:pass@host:port），留空直连")
	outFile := flag.String("out", "", "结果 JSON 写到此文件（默认 exe 同目录 xai-accounts.json）")
	noOpen := flag.Bool("no-open", false, "不自动打开浏览器（只打印 URL，自己用无痕窗口打开避免串号）")
	countFlag := flag.Int("n", 0, "要登录的账号数量（0=交互询问）")
	flag.Parse()

	reader := bufio.NewReader(os.Stdin)

	fmt.Println("==== xAI 官方 API 号 半自动登录工具 ====")
	fmt.Println("说明：弹出浏览器后你手动登录（过验证码/2FA），登录成功会自动抓取 token。")
	fmt.Println()

	proxy := strings.TrimSpace(*proxyFlag)
	if proxy == "" && !flagPassed("proxy") {
		proxy = ask(reader, "出口代理 URL（x.ai 被墙时必填，回车=直连）: ", "")
	}

	count := *countFlag
	if count <= 0 {
		count = atoiDefault(ask(reader, "要登录几个 xAI 账号？（回车=1）: ", "1"), 1)
	}
	if count <= 0 {
		count = 1
	}

	cli, err := xairefresh.New(proxy, 30*time.Second)
	if err != nil {
		fatal("构造 OAuth 客户端失败: %v", err)
	}

	discCtx, discCancel := context.WithTimeout(context.Background(), 30*time.Second)
	disc, err := cli.Discover(discCtx)
	discCancel()
	if err != nil {
		fatal("OIDC discovery 失败: %v（x.ai 不通？试着填代理）", err)
	}

	results := make([]map[string]any, 0, count)
	for i := 1; i <= count; i++ {
		fmt.Printf("\n========== 第 %d/%d 个账号 ==========\n", i, count)
		if i > 1 {
			fmt.Println("⚠ 防串号：浏览器里请先【退出当前账号】，或用【无痕/隐私窗口】打开下面地址，")
			fmt.Println("  否则会拿到上一个已登录账号的 token。")
			_ = ask(reader, "准备好后按【回车】继续... ", "")
		}
		out, lerr := loginOnce(cli, disc, *noOpen)
		if lerr != nil {
			fmt.Printf("✗ 第 %d 个账号登录失败: %v\n", i, lerr)
			if i < count && !yes(ask(reader, "继续登下一个吗？(y/N): ", "n")) {
				break
			}
			continue
		}
		results = append(results, out)
		fmt.Printf("✓ 已获取：%s\n", strFromAny(out["email"]))
	}

	if len(results) == 0 {
		fmt.Println("\n没有成功获取任何账号。")
		pause(reader)
		os.Exit(1)
	}

	pretty, _ := json.MarshalIndent(results, "", "  ")

	path := strings.TrimSpace(*outFile)
	if path == "" {
		path = defaultOutPath()
	}
	if werr := os.WriteFile(path, pretty, 0o600); werr != nil {
		fmt.Printf("写文件失败: %v\n", werr)
	} else {
		fmt.Printf("\n已写入 %d 个账号到: %s\n", len(results), path)
	}

	fmt.Println("\n下面整段 JSON 数组，直接粘贴到后台「xAI 号池 / 导入」即可：")
	fmt.Println()
	fmt.Println(string(pretty))

	pause(reader)
}

// loginOnce 完成一次交互式 OAuth：弹浏览器、收 code、换 token，返回可导入字段。
func loginOnce(cli *xairefresh.Client, disc *xairefresh.Discovery, noOpen bool) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pkce, err := xairefresh.GeneratePKCECodes()
	if err != nil {
		return nil, fmt.Errorf("生成 PKCE 失败: %w", err)
	}
	state, _ := xairefresh.RandomString(24)
	nonce, _ := xairefresh.RandomString(24)
	redirectURI := xairefresh.RedirectURI()

	authURL, err := xairefresh.BuildAuthorizeURL(xairefresh.AuthorizeURLParams{
		AuthorizationEndpoint: disc.AuthorizationEndpoint,
		RedirectURI:           redirectURI,
		CodeChallenge:         pkce.CodeChallenge,
		State:                 state,
		Nonce:                 nonce,
	})
	if err != nil {
		return nil, fmt.Errorf("构造授权 URL 失败: %w", err)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv, err := startCallbackServer(state, codeCh, errCh)
	if err != nil {
		return nil, err
	}
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srv.Shutdown(shutCtx)
		shutCancel()
	}()

	fmt.Println("请在浏览器打开以下地址完成 xAI 登录授权：")
	fmt.Println()
	fmt.Println("    " + authURL)
	fmt.Println()
	if !noOpen {
		_ = openBrowser(authURL)
	}
	fmt.Println("等待回调...（本地监听 " + redirectURI + "）")

	var code string
	select {
	case code = <-codeCh:
	case e := <-errCh:
		return nil, fmt.Errorf("回调出错: %w", e)
	case <-ctx.Done():
		return nil, fmt.Errorf("登录超时（10 分钟内未完成）")
	}

	td, err := cli.ExchangeCodeForTokens(ctx, code, redirectURI, pkce.CodeVerifier, disc.TokenEndpoint)
	if err != nil {
		return nil, fmt.Errorf("授权码换 token 失败: %w", err)
	}

	return map[string]any{
		"email":          td.Email,
		"subject":        td.Subject,
		"access_token":   td.AccessToken,
		"refresh_token":  td.RefreshToken,
		"id_token":       td.IDToken,
		"token_endpoint": disc.TokenEndpoint,
		"base_url":       xairefresh.DefaultAPIBaseURL,
		"expires_at":     td.Expire.UnixMilli(),
	}, nil
}

func startCallbackServer(wantState string, codeCh chan<- string, errCh chan<- error) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc(xairefresh.RedirectPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, "授权失败: "+e, http.StatusBadRequest)
			errCh <- fmt.Errorf("oauth error: %s %s", e, q.Get("error_description"))
			return
		}
		if st := q.Get("state"); st != wantState {
			http.Error(w, "state 校验失败", http.StatusBadRequest)
			errCh <- fmt.Errorf("state mismatch")
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "缺少 code", http.StatusBadRequest)
			errCh <- fmt.Errorf("missing code")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><h3>登录成功，可以关闭本页面返回工具。</h3></body></html>"))
		codeCh <- code
	})

	addr := fmt.Sprintf("%s:%d", xairefresh.RedirectHost, xairefresh.CallbackPort)
	// 多账号连登时上一个回调端口可能还在 TIME_WAIT，重试几次再放弃。
	var ln net.Listener
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败: %w（端口被占用？）", addr, err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// ---- 交互/工具 helper ----

func ask(r *bufio.Reader, prompt, def string) string {
	fmt.Print(prompt)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func pause(r *bufio.Reader) {
	fmt.Print("\n按【回车】退出...")
	_, _ = r.ReadString('\n')
}

func yes(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func strFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func flagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func defaultOutPath() string {
	if exe, err := os.Executable(); err == nil {
		dir := exe[:len(exe)-len(baseName(exe))]
		return dir + "xai-accounts.json"
	}
	return "xai-accounts.json"
}

func baseName(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return p
	}
	return p[i+1:]
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[xailogin] "+format+"\n", args...)
	// 给双击运行的用户留个停顿，别让窗口一闪而过。
	fmt.Fprint(os.Stderr, "\n按【回车】退出...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}
