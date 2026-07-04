package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/regkit/browser"
	"github.com/kleinai/backend/internal/regkit/captcha"
	"github.com/kleinai/backend/internal/regkit/dispatcher"
	"github.com/kleinai/backend/internal/regkit/mailbox"
	"github.com/kleinai/backend/internal/regkit/nameset"
	"github.com/kleinai/backend/internal/service"
)

const (
	baseURL    = "https://accounts.x.ai"
	signupURL  = baseURL + "/sign-up"
	defaultKey = "0x4AAAAAAAhr9JGVDZbrZOo0"

	defaultStateTree = "%5B%22%22%2C%7B%22children%22%3A%5B%22(app)%22%2C%7B%22children%22%3A%5B%22(auth)%22%2C%7B%22children%22%3A%5B%22sign-up%22%2C%7B%22children%22%3A%5B%22__PAGE__%22%2C%7B%7D%2C%22%2Fsign-up%3Fredirect%3Dgrok-com%22%2C%22refresh%22%5D%7D%5D%7D%5D%7D%5D%7D%2Cnull%2Cnull%2Ctrue%5D"
)

// Dispatcher GROK 注册 dispatcher。
type Dispatcher struct {
	dispatcher.Deps
	Pool *service.PoolGrokService
}

// Run 实现 service.RegisterDispatcher。
func (d *Dispatcher) Run(ctx context.Context, svc *service.RegisterTaskService, task *model.RegisterTask) error {
	_ = svc.UpdateProgress(ctx, task.ID, "preflight", 5)
	solver, err := dispatcher.BuildCaptchaTurnstile(ctx, d.SysCfg)
	if err != nil {
		return err
	}
	if d.Pool == nil {
		return errors.New("PoolGrokService 未注入（内部错误）")
	}

	payload, _ := dispatcher.ParsePayload(task.Payload)
	if payload.FirstName == "" {
		payload.FirstName = nameset.FirstName()
	}
	if payload.LastName == "" {
		payload.LastName = nameset.LastName()
	}
	if payload.Password == "" {
		payload.Password = nameset.Password(16)
	}

	_ = svc.UpdateProgress(ctx, task.ID, "pick_proxy", 10)
	resolved, err := d.ProxyPicker.Pick(ctx, payload.ProxyID)
	if err != nil {
		return fmt.Errorf("代理选择失败：%w", err)
	}

	bc, err := browser.New(browser.Options{ProxyURL: resolved.URL, Timeout: 60 * time.Second})
	if err != nil {
		return fmt.Errorf("初始化 HTTP client 失败：%w", err)
	}

	_ = svc.UpdateProgress(ctx, task.ID, "acquire_mail", 18)
	mailCfg := dispatcher.BuildMailBackendConfig(ctx, d.SysCfg)
	mailCfg.Proxy = resolved.URL

	var acq *mailbox.AcquireResult
	if task.MailID != nil && *task.MailID > 0 {
		acq, err = d.MailMgr.AcquireByID(ctx, *task.MailID, "grok", mailCfg)
	} else {
		// AcquireFresh：CF Worker 配了就即时签发（不入库），其他模式回退池化。
		acq, err = d.MailMgr.AcquireFresh(ctx, "grok", mailCfg)
	}
	if err != nil {
		return fmt.Errorf("领取邮箱失败：%w", err)
	}
	mailReleased := false
	defer func() {
		if !mailReleased {
			_ = d.MailMgr.Release(context.Background(), acq.Row.ID)
			_ = acq.Mailbox.Close()
		}
	}()
	_ = svc.AttachMail(ctx, task.ID, acq.Row.ID, acq.Row.Email)
	email := acq.Row.Email
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("已领取邮箱 %s（mode=%s）", email, acq.Row.Mode))
	if resolved.URL != "" {
		svc.LogInfo(ctx, task.ID, fmt.Sprintf("使用代理 %s", dispatcher.MaskProxy(resolved.URL)))
	}

	failMail := func(reason string) error {
		_, _ = d.MailMgr.MarkFailed(context.Background(), acq.Row.ID, reason, dispatcher.PoolMaxFailure)
		mailReleased = true
		_ = acq.Mailbox.Close()
		return errors.New(reason)
	}

	// 1) Bootstrap：拉登录页，提取 site_key。
	_ = svc.UpdateProgress(ctx, task.ID, "bootstrap", 25)
	siteKey, stateTree, err := bootstrap(ctx, bc)
	if err != nil {
		return failMail(fmt.Sprintf("bootstrap 失败：%v", err))
	}

	// 2) 发邮箱验证码（gRPC-web）。
	//
	// accounts.x.ai 的 POST 端点经常被 Cloudflare 单独风控（GET 网页放行 / POST 直接 403）。
	// 先按当前代理打一次，如果是 CF 403 就自动换代理 + 重做 bootstrap，最多 3 次。
	_ = svc.UpdateProgress(ctx, task.ID, "send_email_code", 35)
	mfaSentAt := time.Now()
	sendErr := sendEmailCode(ctx, bc, email)
	if sendErr != nil {
		excluded := []uint64{resolved.ID}
		for attempt := 0; attempt < 3 && sendErr != nil; attempt++ {
			if !strings.Contains(sendErr.Error(), "HTTP 403") {
				break
			}
			svc.LogInfo(ctx, task.ID,
				fmt.Sprintf("send_email_code 被 Cloudflare 403，第 %d 次换代理重试", attempt+1))
			next, perr := d.ProxyPicker.PickExcluding(ctx, excluded)
			if perr != nil || next == nil || next.URL == "" {
				break
			}
			excluded = append(excluded, next.ID)
			nbc, berr := browser.New(browser.Options{ProxyURL: next.URL, Timeout: 60 * time.Second})
			if berr != nil {
				continue
			}
			bc = nbc
			resolved = next
			svc.LogInfo(ctx, task.ID, fmt.Sprintf("切换到代理 %s", dispatcher.MaskProxy(next.URL)))
			// 必须重做 bootstrap（拿到新代理对应的 cookie / state_tree）
			if sk, st2, berr := bootstrap(ctx, bc); berr == nil {
				siteKey = sk
				stateTree = st2
			}
			mfaSentAt = time.Now()
			sendErr = sendEmailCode(ctx, bc, email)
		}
		if sendErr != nil {
			return failMail(fmt.Sprintf("发送邮箱验证码失败：%v", sendErr))
		}
	}

	// 3) 并行：解 Turnstile + 等 OTP。
	_ = svc.UpdateProgress(ctx, task.ID, "captcha+otp", 50)
	// 链式 solver 钩子：把 fallback 切换写进任务日志，方便排查"yescaptcha 超时 → 本地接手"。
	if chain, ok := solver.(*captcha.ChainSolver); ok {
		chain.OnAttempt = func(idx int, name string, total int) {
			svc.LogInfo(ctx, task.ID, fmt.Sprintf("turnstile attempt %d/%d via %s", idx, total, name))
		}
		chain.OnFailover = func(idx int, name string, err error) {
			svc.LogWarn(ctx, task.ID, fmt.Sprintf("turnstile attempt %d via %s failed: %v — failing over", idx, name, err))
		}
	}
	type tsRes struct {
		token string
		err   error
	}
	type otpRes struct {
		code string
		err  error
	}
	tsCh := make(chan tsRes, 1)
	otpCh := make(chan otpRes, 1)

	go func() {
		token, err := solver.SolveTurnstile(ctx, &captcha.TurnstileTask{
			WebsiteURL: signupURL,
			WebsiteKey: siteKey,
			UserAgent:  bc.Profile.UserAgent,
			Proxy:      resolved.URL,
		})
		tsCh <- tsRes{token, err}
	}()

	go func() {
		code, err := acq.Mailbox.WaitCode(ctx, mailbox.WaitOptions{
			Provider: mailbox.ProviderGrok,
			SinceTS:  mfaSentAt.Add(-30 * time.Second),
			Timeout:  240 * time.Second,
		})
		otpCh <- otpRes{code, err}
	}()

	var turnstileToken, otp string
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-tsCh:
			if r.err != nil {
				return failMail(fmt.Sprintf("Turnstile 求解失败：%v", r.err))
			}
			turnstileToken = r.token
			svc.LogInfo(ctx, task.ID, "Turnstile 验证码已通过")
		case r := <-otpCh:
			if r.err != nil {
				return failMail(fmt.Sprintf("等待邮箱验证码失败：%v", r.err))
			}
			otp = r.code
			svc.LogInfo(ctx, task.ID, fmt.Sprintf("已收到邮箱验证码 %s", dispatcher.MaskOTP(otp)))
		}
	}

	// 4) 校验邮箱验证码（gRPC-web）。
	_ = svc.UpdateProgress(ctx, task.ID, "verify_email_code", 65)
	if err := verifyEmailCode(ctx, bc, email, otp); err != nil {
		return failMail(fmt.Sprintf("邮箱验证码校验失败：%v", err))
	}

	// 5) 提交注册（Next.js Server Action）。
	_ = svc.UpdateProgress(ctx, task.ID, "submit_signup", 78)
	verifyURL, err := submitSignup(ctx, bc, signupArgs{
		email:          email,
		givenName:      payload.FirstName,
		familyName:     payload.LastName,
		password:       payload.Password,
		emailValCode:   otp,
		turnstileToken: turnstileToken,
		actionID:       loadActionID(ctx, bc),
		stateTree:      stateTree,
	})
	if err != nil {
		return failMail(fmt.Sprintf("提交注册失败：%v", err))
	}

	// 6) 跟随 verify_url 收 sso/sso-rw cookie。
	_ = svc.UpdateProgress(ctx, task.ID, "follow_verify_url", 86)
	sso, ssoRW := followVerifyURL(ctx, bc, verifyURL)
	if sso == "" {
		// fallback：直接调 createSession RPC 拿 cookie。
		//
		// 关键：Turnstile token 是一次性的，submit_signup 用掉之后再用同一个就会被
		// Grok 拒（"Failed to verify Cloudflare turnstile token"）。所以这里必须
		// 重新解一遍 Turnstile，再走 createSession。
		svc.LogInfo(ctx, task.ID, "follow_verify_url 未拿到 sso，重新解 Turnstile 并走 createSession 兜底")
		newTSToken, tsErr := solver.SolveTurnstile(ctx, &captcha.TurnstileTask{
			WebsiteURL: signupURL,
			WebsiteKey: siteKey,
			UserAgent:  bc.Profile.UserAgent,
			Proxy:      resolved.URL,
		})
		if tsErr != nil {
			return failMail(fmt.Sprintf("收集 sso cookie 失败：fallback 解 Turnstile 失败 %v", tsErr))
		}
		var err2 error
		sso, ssoRW, err2 = createSessionFallback(ctx, bc, email, payload.Password, newTSToken)
		if err2 != nil {
			return failMail(fmt.Sprintf("收集 sso cookie 失败：%v", err2))
		}
	}

	// 7) 接受 ToS（gRPC-web，需 cookie）。
	_ = svc.UpdateProgress(ctx, task.ID, "accept_tos", 92)
	if err := acceptToS(ctx, bc, sso, ssoRW); err != nil {
		// ToS 失败不致命，记录但继续
		_ = svc.UpdateProgress(ctx, task.ID, "accept_tos_warn", 92)
	}

	// 8) 写号池。
	_ = svc.UpdateProgress(ctx, task.ID, "persist", 96)
	created, err := d.Pool.Create(ctx, &dto.GrokPoolCreateReq{
		Email:       email,
		Password:    payload.Password,
		GivenName:   payload.FirstName,
		FamilyName:  payload.LastName,
		UserAgent:   bc.Profile.UserAgent,
		SSO:         sso,
		SSORW:       ssoRW,
		TrialStatus: model.GrokTrialPending,
		Notes:       payload.Notes,
	})
	if err != nil {
		return fmt.Errorf("写入 pool_grok 失败：%w", err)
	}

	// 9) 标邮箱已注册。
	_ = d.MailMgr.MarkRegistered(ctx, acq.Row.ID, created.ID)
	mailReleased = true
	_ = acq.Mailbox.Close()

	return svc.FinishSuccess(ctx, task.ID, created.ID, map[string]any{
		"pool_account_id": created.ID,
		"email":           email,
		"has_sso":         sso != "",
		"has_sso_rw":      ssoRW != "",
		"trial_status":    "pending",
	})
}

// === HTTP 步骤 ===

// reSiteKey 从注册页 HTML 抽取 Turnstile sitekey。
var reSiteKey = regexp.MustCompile(`sitekey":"(0x[0-9A-Za-z]+)"`)

// reStateTree 从注册页 HTML 抽取 next-router-state-tree（页面渲染期间 Next 会
// 把当前路由状态写在 inline script 里，提交 server action 时必须用真值）。
var reStateTree = regexp.MustCompile(`next-router-state-tree(?:&quot;|")\s*:\s*(?:&quot;|")([^"&]+)(?:&quot;|")`)

// reNextScriptSrc 从 sign-up HTML 里收集 _next/static 下所有 script src。
var reNextScriptSrc = regexp.MustCompile(`<script[^>]+src="(/_next/static/[^"]+\.js)"`)

// reActionID Grok 的 server action ID 是 40 位 hex 且固定 7f 开头（与 Next 14
// 的 hashed-action-name 算法一致）。
var reActionID = regexp.MustCompile(`7f[a-fA-F0-9]{40}`)

// 全局 action_id 缓存：同一台机器、同一份 Grok 部署，action_id 在每次发版前
// 都不变，缓存 1h 既减开销又规避网络抖动。
var (
	bootstrapCacheMu  sync.Mutex
	bootstrapCacheVal grokBootstrap
	bootstrapCacheAt  time.Time
)

const bootstrapCacheTTL = time.Hour

type grokBootstrap struct {
	SiteKey   string
	StateTree string
	ActionID  string
}

func bootstrap(ctx context.Context, bc *browser.Client) (siteKey, stateTree string, err error) {
	info, err := bootstrapWithAction(ctx, bc)
	if err != nil {
		return "", "", err
	}
	return info.SiteKey, info.StateTree, nil
}

func bootstrapWithAction(ctx context.Context, bc *browser.Client) (grokBootstrap, error) {
	// 命中 cache 直接返回（site_key / state_tree / action_id 都几乎不变）。
	bootstrapCacheMu.Lock()
	if bootstrapCacheVal.ActionID != "" && time.Since(bootstrapCacheAt) < bootstrapCacheTTL {
		v := bootstrapCacheVal
		bootstrapCacheMu.Unlock()
		return v, nil
	}
	bootstrapCacheMu.Unlock()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, signupURL+"?redirect=grok-com", nil)
	setNavigationHeaders(req)
	resp, err := bc.Do(req)
	if err != nil {
		return grokBootstrap{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	html := string(body)

	info := grokBootstrap{
		SiteKey:   defaultKey,
		StateTree: defaultStateTree,
	}
	if m := reSiteKey.FindStringSubmatch(html); len(m) > 1 {
		info.SiteKey = m[1]
	}
	if m := reStateTree.FindStringSubmatch(html); len(m) > 1 {
		info.StateTree = m[1]
	}

	// 从 HTML 里收集 _next/static script，逐个 GET 找 action_id。
	seen := make(map[string]bool)
	scripts := reNextScriptSrc.FindAllStringSubmatch(html, -1)
	for _, s := range scripts {
		if len(s) < 2 || seen[s[1]] {
			continue
		}
		seen[s[1]] = true
		jsURL := baseURL + s[1]
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, jsURL, nil)
		req2.Header.Set("Accept", "*/*")
		req2.Header.Set("Sec-Fetch-Dest", "script")
		req2.Header.Set("Sec-Fetch-Mode", "no-cors")
		req2.Header.Set("Sec-Fetch-Site", "same-origin")
		req2.Header.Set("Referer", signupURL+"?redirect=grok-com")
		r2, err2 := bc.Do(req2)
		if err2 != nil {
			continue
		}
		jsBody, _ := io.ReadAll(io.LimitReader(r2.Body, 4*1024*1024))
		_ = r2.Body.Close()
		if m := reActionID.FindString(string(jsBody)); m != "" {
			info.ActionID = m
			break
		}
	}

	if info.ActionID == "" {
		return grokBootstrap{}, errors.New("bootstrap: 未能从 _next/static 中提取 action_id（Grok 可能更新了构建产物）")
	}

	bootstrapCacheMu.Lock()
	bootstrapCacheVal = info
	bootstrapCacheAt = time.Now()
	bootstrapCacheMu.Unlock()
	return info, nil
}

// setNavigationHeaders 真实 Chrome 主文档导航的最小头集合（除 UA / sec-ch-ua 外，
// 其余几个由浏览器自动追加，缺失会被 Cloudflare 当 bot 流量降权）。
//
// 注：刻意不设 Accept-Encoding；Go http.Transport 看到手动 Accept-Encoding 就会
// 关闭自动 gzip 解压，反倒拿到二进制 body。让 Transport 默认加 "gzip" 并自动
// 解压；这点小差异 CF 不会因此判 bot。
func setNavigationHeaders(req *http.Request) {
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Priority", "u=0, i")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
}

// setFetchCORSHeaders 给页面内发起的 fetch/XHR（含 gRPC-web）的 POST/GET 注入
// 真实 Chrome 必带的 fetch metadata。
//
// 缺这套头时 Cloudflare 会把请求当 bot 直接 403（accounts.x.ai 的 gRPC 端点对
// 这点格外敏感，本地可以 GET 注册页 200 但 POST 直接 403）。
//
// 同样不设 Accept-Encoding，理由同 setNavigationHeaders。
func setFetchCORSHeaders(req *http.Request) {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Priority", "u=1, i")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
}

// loadActionID 拿当前 cache 里的 action_id；空值会强制 bootstrap 重新抓。
func loadActionID(ctx context.Context, bc *browser.Client) string {
	bootstrapCacheMu.Lock()
	if bootstrapCacheVal.ActionID != "" && time.Since(bootstrapCacheAt) < bootstrapCacheTTL {
		v := bootstrapCacheVal.ActionID
		bootstrapCacheMu.Unlock()
		return v
	}
	bootstrapCacheMu.Unlock()
	if info, err := bootstrapWithAction(ctx, bc); err == nil {
		return info.ActionID
	}
	return ""
}

func sendEmailCode(ctx context.Context, bc *browser.Client, email string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/auth_mgmt.AuthManagement/CreateEmailValidationCode",
		bytes.NewReader(encodeCreateEmailValidationCode(email)))
	req.Header.Set("content-type", "application/grpc-web+proto")
	req.Header.Set("x-grpc-web", "1")
	req.Header.Set("x-user-agent", "connect-es/2.1.1")
	req.Header.Set("origin", baseURL)
	req.Header.Set("referer", signupURL+"?redirect=grok-com")
	setFetchCORSHeaders(req)
	resp, err := bc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// gRPC-web 偶尔会返回 200 但 trailers 里带错误（e.g. 邮箱域名被列入黑名单）。
	// 这里把状态码 + 响应长度 + grpc-status header 都拼进 error，方便日志诊断。
	gStatus := resp.Header.Get("grpc-status")
	gMsg := resp.Header.Get("grpc-message")
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		// 失败时把完整 body 同时落到容器内 /tmp/grok_cf_403_<ts>.html，便于人工分析。
		_ = dumpCFBody("send_email_code", raw)
		return fmt.Errorf("HTTP %d %s%s",
			resp.StatusCode, cfDiagSnippet(resp, raw),
			grpcSuffix(gStatus, gMsg))
	}
	if gStatus != "" && gStatus != "0" {
		return fmt.Errorf("grpc-status=%s msg=%s", gStatus, gMsg)
	}
	return nil
}

// cfDiagSnippet 把响应里跟 Cloudflare 拦截相关的关键标识合并成一段可读字符串：
//
//	cf-ray=... server=cloudflare cf-mitigated=challenge body-ray=...
//
// 大幅缩小日志噪声、又保留诊断价值（cf-ray 可去 CF 控制台查具体规则）。
func cfDiagSnippet(resp *http.Response, body []byte) string {
	parts := []string{}
	if v := resp.Header.Get("cf-ray"); v != "" {
		parts = append(parts, "cf-ray="+v)
	}
	if v := resp.Header.Get("cf-mitigated"); v != "" {
		parts = append(parts, "cf-mitigated="+v)
	}
	if v := resp.Header.Get("server"); v != "" {
		parts = append(parts, "server="+v)
	}
	if v := resp.Header.Get("cf-cache-status"); v != "" {
		parts = append(parts, "cf-cache="+v)
	}
	// 从 body 抓 Ray ID（CF 错误页一般会埋 "Cloudflare Ray ID: <id>"）。
	if rayInBody := reCFRayInBody.FindSubmatch(body); len(rayInBody) > 1 {
		parts = append(parts, "body-ray="+string(rayInBody[1]))
	}
	// CF 错误页 1020 / 1010 / 1006 这种数字也很有用。
	if errCode := reCFErrCode.FindSubmatch(body); len(errCode) > 1 {
		parts = append(parts, "cf-err="+string(errCode[1]))
	}
	if len(parts) == 0 {
		return "body=" + snippetLong(body)
	}
	return strings.Join(parts, " ") + " body=" + snippetLong(body)
}

func grpcSuffix(status, msg string) string {
	if status == "" && msg == "" {
		return ""
	}
	return fmt.Sprintf(" grpc-status=%s msg=%s", status, msg)
}

var (
	reCFRayInBody = regexp.MustCompile(`Cloudflare Ray ID:\s*<strong[^>]*>([0-9a-f]+)`)
	reCFErrCode   = regexp.MustCompile(`Error code\s*<span[^>]*>(\d{3,5})`)
)

// dumpCFBody 把 Cloudflare 拦截的完整 HTML 落到 /tmp，方便容器内 cat 出来肉眼分析。
// 失败时静默忽略，不影响主流程。
func dumpCFBody(label string, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	name := fmt.Sprintf("/tmp/grok_cf_%s_%d.html", label, time.Now().UnixNano())
	return os.WriteFile(name, body, 0o644)
}

func verifyEmailCode(ctx context.Context, bc *browser.Client, email, code string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/auth_mgmt.AuthManagement/VerifyEmailValidationCode",
		bytes.NewReader(encodeVerifyEmailValidationCode(email, code)))
	req.Header.Set("content-type", "application/grpc-web+proto")
	req.Header.Set("x-grpc-web", "1")
	req.Header.Set("x-user-agent", "connect-es/2.1.1")
	req.Header.Set("origin", baseURL)
	req.Header.Set("referer", signupURL+"?redirect=grok-com")
	setFetchCORSHeaders(req)
	resp, err := bc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("VerifyEmailValidationCode HTTP %d", resp.StatusCode)
	}
	return nil
}

type signupArgs struct {
	email, givenName, familyName, password string
	emailValCode, turnstileToken           string
	actionID, stateTree                    string
}

func submitSignup(ctx context.Context, bc *browser.Client, a signupArgs) (verifyURL string, err error) {
	body := []map[string]any{{
		"emailValidationCode": a.emailValCode,
		"createUserAndSessionRequest": map[string]any{
			"email":              a.email,
			"givenName":          a.givenName,
			"familyName":         a.familyName,
			"clearTextPassword":  a.password,
			"tosAcceptedVersion": "$undefined",
		},
		"turnstileToken":          a.turnstileToken,
		"promptOnDuplicateEmail":  true,
	}}
	bodyBytes, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, signupURL, bytes.NewReader(bodyBytes))
	req.Header.Set("accept", "text/x-component")
	req.Header.Set("content-type", "text/plain;charset=UTF-8")
	req.Header.Set("origin", baseURL)
	req.Header.Set("referer", signupURL)
	req.Header.Set("next-router-state-tree", a.stateTree)
	if a.actionID != "" {
		req.Header.Set("next-action", a.actionID)
	}
	setFetchCORSHeaders(req)
	resp, err := bc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("/sign-up HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	// 响应是 RSC 多行；逐行解析含 url 字段的 JSON。
	for _, line := range bytes.Split(raw, []byte("\n")) {
		idx := bytes.IndexByte(line, '{')
		if idx < 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line[idx:], &m); err != nil {
			continue
		}
		if u, _ := m["url"].(string); strings.HasPrefix(u, "http") {
			return u, nil
		}
		if e, _ := m["error"].(string); e != "" {
			return "", fmt.Errorf("Grok 返回 error=%s", e)
		}
	}
	// 兜底：直接在 body 里搜 set-cookie?q=... URL（参考 Python 实现）。
	if m := reSetCookieURL.FindString(string(raw)); m != "" {
		return strings.ReplaceAll(m, `\/`, "/"), nil
	}
	// 这里把完整 body 都塞进 error（register_task_log.message 已升级到 TEXT 能放下）。
	return "", fmt.Errorf("响应未包含 verify_url: %s", string(raw))
}

// reSetCookieURL 匹配 RSC 响应里嵌入的 verify URL（最终 SSO cookie 由这里下发）。
// 形如 https://auth.x.ai/...set-cookie?q=...
var reSetCookieURL = regexp.MustCompile(`https:[\\\/]*[a-zA-Z0-9_.-]+[\\\/]+(?:[^"\s]*?)set-cookie\?q=[^"\s]+`)

// allowedSSOHosts 兼容 Python 实现：sso 跨域跳转白名单。
var allowedSSOHosts = map[string]bool{
	"auth.x.ai":              true,
	"auth.grok.com":          true,
	"auth.grokipedia.com":    true,
	"auth.grokusercontent.com": true,
	"accounts.x.ai":          true,
	"grok.com":               true,
	"x.ai":                   true,
}

// followVerifyURL 跟随 verify_url 跨域跳转链，沿途从 cookie jar 收集 sso/sso-rw。
//
// 关键：必须带上真实 Chrome 跨域导航的 fetch metadata 头（sec-fetch-site=cross-site
// 等），否则 Grok 会把这跳判为可疑 → 不下发 sso cookie。先走自动 redirect，
// 拿不到就改用 Python 同款手动逐跳。
func followVerifyURL(ctx context.Context, bc *browser.Client, vURL string) (sso, ssoRW string) {
	if vURL == "" {
		return
	}
	// 1) 自动重定向：用 navigate 风格的头
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, vURL, nil)
	setCrossSiteNavHeaders(req)
	if resp, err := bc.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
	}
	sso, ssoRW = collectSSOFromJar(bc)
	if sso != "" {
		return
	}

	// 2) 兜底：手动逐跳（关闭自动重定向），从每个响应的 Set-Cookie 拿
	hop := vURL
	noRedirectClient := *bc.HTTP
	noRedirectClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	for i := 0; i < 8; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hop, nil)
		setCrossSiteNavHeaders(req)
		// 透传 UA 等头（http.Client.Do 会因 cookie jar 自动注入 cookie；
		// 但跨 host 时 net/http 不会自动带上之前的 sso，这正是手动逐跳的意义）。
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", bc.Profile.UserAgent)
		}
		resp, err := noRedirectClient.Do(req)
		if err != nil {
			break
		}
		// 收集 Set-Cookie 中的 sso/sso-rw
		for _, c := range resp.Cookies() {
			switch c.Name {
			case "sso":
				if sso == "" {
					sso = c.Value
				}
			case "sso-rw":
				if ssoRW == "" {
					ssoRW = c.Value
				}
			}
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()

		switch resp.StatusCode {
		case 301, 302, 303, 307, 308:
			loc := resp.Header.Get("Location")
			if loc == "" {
				return
			}
			next, err := url.Parse(loc)
			if err != nil {
				return
			}
			if !next.IsAbs() {
				base, _ := url.Parse(hop)
				next = base.ResolveReference(next)
			}
			if !allowedSSOHosts[next.Hostname()] {
				return
			}
			hop = next.String()
		default:
			return
		}
	}
	return
}

func collectSSOFromJar(bc *browser.Client) (sso, ssoRW string) {
	for _, host := range []string{
		"https://accounts.x.ai", "https://grok.com", "https://x.ai",
		"https://auth.x.ai", "https://auth.grok.com",
	} {
		u, _ := url.Parse(host)
		for _, c := range bc.Jar.Cookies(u) {
			switch c.Name {
			case "sso":
				if sso == "" {
					sso = c.Value
				}
			case "sso-rw":
				if ssoRW == "" {
					ssoRW = c.Value
				}
			}
		}
	}
	return
}

// setCrossSiteNavHeaders 给 verify_url 跨域跳转的 GET 请求注入真实 Chrome 头。
func setCrossSiteNavHeaders(req *http.Request) {
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Priority", "u=0, i")
	req.Header.Set("Referer", baseURL+"/")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
}

func createSessionFallback(ctx context.Context, bc *browser.Client, email, password, turnstileToken string) (sso, ssoRW string, err error) {
	body := map[string]any{
		"rpc": "createSession",
		"req": map[string]any{
			"createSessionRequest": map[string]any{
				"credentials": map[string]any{
					"case": "emailAndPassword",
					"value": map[string]any{
						"email":             email,
						"clearTextPassword": password,
					},
				},
			},
			"turnstileToken": turnstileToken,
		},
	}
	bb, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/rpc", bytes.NewReader(bb))
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", baseURL)
	req.Header.Set("referer", signupURL+"?redirect=grok-com")
	setFetchCORSHeaders(req)
	resp, err := bc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("createSession HTTP %d body=%s", resp.StatusCode, snippetLong(raw))
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", "", fmt.Errorf("createSession 响应非 JSON: %s", snippetLong(raw))
	}
	// 把后端真正给的 error / message 一并暴露出来，便于排错。
	if e, _ := data["error"].(string); e != "" {
		return "", "", fmt.Errorf("createSession error=%s body=%s", e, snippetLong(raw))
	}
	cookieURL, _ := data["cookieSetterUrl"].(string)
	if cookieURL == "" {
		return "", "", fmt.Errorf("createSession 缺 cookieSetterUrl，响应=%s", snippetLong(raw))
	}
	sso, ssoRW = followVerifyURL(ctx, bc, cookieURL)
	if sso == "" {
		return "", "", errors.New("createSession 后仍未拿到 sso")
	}
	return
}

func acceptToS(ctx context.Context, bc *browser.Client, sso, ssoRW string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/auth_mgmt.AuthManagement/SetTosAcceptedVersion",
		bytes.NewReader(encodeSetTosAcceptedVersion()))
	req.Header.Set("content-type", "application/grpc-web+proto")
	req.Header.Set("x-grpc-web", "1")
	req.Header.Set("x-user-agent", "connect-es/2.1.1")
	req.Header.Set("origin", baseURL)
	req.Header.Set("referer", baseURL+"/accept-tos")
	req.Header.Set("Cookie", fmt.Sprintf("sso=%s; sso-rw=%s", sso, ssoRW))
	setFetchCORSHeaders(req)
	resp, err := bc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("SetTosAcceptedVersion HTTP %d", resp.StatusCode)
	}
	return nil
}

func snippet(b []byte) string {
	const max = 240
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

// snippetLong 为诊断 Cloudflare 拦截而设的较大上限版本：把响应正文里的关键
// 文案（如 "Sorry, you have been blocked"、"Just a moment"、Error code 1020 之类）
// 完整保留下来，便于在 register_task_log 表里直接看到根因。
func snippetLong(b []byte) string {
	const max = 1800
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
