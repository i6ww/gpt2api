package gopay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/regkit/browser"
)

// Charger 一次 GoPay 扣款的整个会话状态。一个实例只能跑一次 Run()，
// dispatcher 重试时必须 New() 一个新的（cookie / cs_id 都是一次性的）。
type Charger struct {
	cfg Config

	// cs Phase A 客户端（chatgpt + stripe）。
	cs *browser.Client
	// ext Phase B 客户端（midtrans + gopay）。
	ext *browser.Client

	// 运行时累积的状态。
	csID             string
	checkoutURL      string // hosted 模式下 OpenAI 返回的"已 approve"的 checkout URL
	processorEntity  string // hosted 模式：openai_llc / openai_ie 等
	pmRedirectURL    string // 跟随 hosted URL redirect 后落到的 pm-redirects.stripe.com URL
	pmID             string // 仅 custom 模式保留；hosted 模式由 OpenAI 后端代建，不暴露给客户端
	snapToken        string
	linkingReference string
	chargeRef        string
	amountCents      int64
	currency         string
	chargedAt        time.Time
	stripePK         string
	midtransClID     string
	stripeRT         StripeRuntime

	logFn func(level, msg string)
}

// New 构造一个 Charger。Run 之前会做基础校验。
func New(ctx context.Context, cfg Config) (*Charger, error) {
	if cfg.Wallet.PhoneNumber == "" {
		return nil, errors.New("[gopay] config: wallet.phone_number required")
	}
	if cfg.Wallet.PIN == "" {
		return nil, errors.New("[gopay] config: wallet.pin required")
	}
	if cfg.Wallet.CountryCode == "" {
		cfg.Wallet.CountryCode = "62"
	}
	if cfg.Auth.AccessToken == "" {
		return nil, errors.New("[gopay] config: auth.access_token required")
	}
	if cfg.Auth.Cookies == "" {
		return nil, errors.New("[gopay] config: auth.cookies required")
	}
	if cfg.OTPProvider == nil {
		return nil, errors.New("[gopay] config: otp_provider required")
	}
	stripePK := cfg.StripeRuntime.PublishableKey
	if stripePK == "" {
		stripePK = DefaultStripePublishableKey
	}
	stripeVer := cfg.StripeRuntime.Version
	if stripeVer == "" {
		stripeVer = DefaultStripeVersion
	}
	midtransCID := cfg.MidtransClientID
	if midtransCID == "" {
		midtransCID = DefaultMidtransClientID
	}
	browserTimeout := cfg.BrowserTimeout
	if browserTimeout <= 0 {
		browserTimeout = DefaultBrowserTimeout
	}

	cs, err := browser.New(browser.Options{ProxyURL: cfg.CSProxy, Timeout: browserTimeout})
	if err != nil {
		return nil, fmt.Errorf("[gopay] cs browser init: %w", err)
	}
	ext, err := browser.New(browser.Options{ProxyURL: cfg.ExtProxy, Timeout: browserTimeout})
	if err != nil {
		return nil, fmt.Errorf("[gopay] ext browser init: %w", err)
	}
	if cfg.Auth.UserAgent != "" {
		cs.Profile.UserAgent = cfg.Auth.UserAgent
	}

	c := &Charger{
		cfg:          cfg,
		cs:           cs,
		ext:          ext,
		stripePK:     stripePK,
		midtransClID: midtransCID,
		stripeRT: StripeRuntime{
			PublishableKey: stripePK,
			Version:        stripeVer,
			JSChecksum:     cfg.StripeRuntime.JSChecksum,
			RVTimestamp:    cfg.StripeRuntime.RVTimestamp,
		},
		logFn: cfg.Log,
	}
	// 把 dispatcher 传入的初始 cookies（oai-did 等）灌进 cs 客户端的 cookie jar。
	// 后续 warmup 阶段 chatgpt.com 各 endpoint 通过 Set-Cookie 累加 session-token
	// 等关键 cookie；approve 时 Go stdlib 自动从 jar 把全部 cookie 拼到 Cookie 头。
	if cfg.Auth.Cookies != "" {
		c.seedCookiesIntoJar(cs, "https://chatgpt.com", cfg.Auth.Cookies)
	}
	return c, nil
}

// seedCookiesIntoJar 把"name=val; name2=val2"风格的 cookie 头解析后塞进 jar，
// 让 Go stdlib 后续从 jar 取，避免我们手动 Header.Set("Cookie", ...) 覆盖掉
// jar 自动累积的 cookie（stdlib：req.Header 里有 Cookie 时 jar 会被跳过）。
func (c *Charger) seedCookiesIntoJar(cl *browser.Client, rawURL, cookieHeader string) {
	if cl == nil || cl.Jar == nil {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	parts := strings.Split(cookieHeader, ";")
	var cookies []*http.Cookie
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || !strings.Contains(p, "=") {
			continue
		}
		eq := strings.Index(p, "=")
		name := strings.TrimSpace(p[:eq])
		val := strings.TrimSpace(p[eq+1:])
		if name == "" {
			continue
		}
		// Domain 留空 → jar 视作 host-only cookie；Path 不填 → jar 默认 "/"。
		// 这样最接近浏览器 Set-Cookie 的默认语义，避免 Domain 写死带来的"子域才生效"陷阱。
		cookies = append(cookies, &http.Cookie{Name: name, Value: val})
	}
	if len(cookies) > 0 {
		cl.Jar.SetCookies(u, cookies)
	}
}

// Run 跑完整个 hosted checkout → midtrans → gopay 流程并返回 Result。
//
// **hosted 模式流程**：
//
//	Step 1: POST /backend-api/payments/checkout (checkout_ui_mode="hosted")
//	Step 2: Stripe createPM (type=gopay)
//	Step 3: Stripe /init + /confirm  ← /confirm 触发 OpenAI 后端 webhook 自动 approve
//	(Step 4 chatgptApprove 跳过 — hosted 模式 OpenAI 自动 approve)
//	Step 5: Poll setup_intent.next_action.redirect_to_url → pm-redirects → snap_token
//	Step 6+: midtrans loadTransaction / linking / OTP / PIN / charge
//	Step 15: chatgpt verify
//
// 跟 custom 模式的唯一区别：**省掉 chatgpt approve**。Stripe confirm 后 OpenAI
// 后端通过 webhook 自动 approve（hosted 模式的特性），客户端不需要再发请求。
// 这就是用户说的"长链不要 ST、短链要 ST"—— ST 仅在 chatgpt approve 端点要求，
// hosted 模式根本不调那个端点，自然不需要 session-token cookie。
func (c *Charger) Run(ctx context.Context) (*Result, error) {
	c.warmupChatGPTSession(ctx)
	// Step 1: hosted createCheckout，带 network retry。
	if err := c.createHostedCheckoutWithRetry(ctx); err != nil {
		return nil, err
	}
	// Step 2-3: Stripe createPM + /init + /confirm。
	if err := c.runStep2CreatePM(ctx); err != nil {
		return nil, err
	}
	if err := c.runStep3StripeConfirm(ctx); err != nil {
		return nil, err
	}
	// Step 4 (chatgpt approve) 跳过 — hosted 模式 OpenAI 自动 approve。
	// Step 5: poll setup_intent → pm-redirects → snap_token。
	if err := c.runStep5ResolveSnapToken(ctx); err != nil {
		return nil, err
	}

	// Phase B: 全部走 ext 客户端。
	if err := c.runStep6LoadTransaction(ctx); err != nil {
		return nil, err
	}
	if err := c.runStep7InitLinking(ctx); err != nil {
		return nil, err
	}
	if err := c.runLinkingPhase(ctx); err != nil { // Step 8-12
		return nil, err
	}
	if err := c.runStep13MidtransCharge(ctx); err != nil {
		return nil, err
	}
	if err := c.runPaymentPhase(ctx); err != nil { // Step 14
		return nil, err
	}

	verifyOK, _ := c.runStep15VerifyChatGPT(ctx)
	state := ResultStateSucceeded
	if !verifyOK {
		state = ResultStateVerifyTimeout
	}
	return &Result{
		State:     state,
		CSID:      c.csID,
		SnapToken: c.snapToken,
		ChargeRef: c.chargeRef,
		AmountIDR: c.amountCents,
		ChargedAt: c.chargedAt,
		VerifyOK:  verifyOK,
	}, nil
}

// createHostedCheckoutWithRetry 跑 Step1 hosted createCheckout，只对 network
// 抖动做 retry（TLS EOF / DNS / 代理通道临时挂掉），其他错误一次性返回。
//
// hosted 模式不再有 "approve blocked" 概念——OpenAI 后端代为完成 approve，
// 我们只需要拿到 hosted URL 就行。createCheckout 本身对 OAuth-only Bearer
// 通常 200 OK（实测 custom 和 hosted 模式都过得了），所以这里 retry 频率低。
func (c *Charger) createHostedCheckoutWithRetry(ctx context.Context) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			c.log("warn", "[Plus 升级] 生成支付会话网络抖动，等 %s 后重试（第 %d/%d 次）",
				NetworkRetryBackoff, attempt, maxAttempts)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timeAfter(NetworkRetryBackoff):
			}
			c.csID = ""
			c.checkoutURL = ""
			c.processorEntity = ""
		}
		err := c.runStep1CreateCheckout(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if !IsCode(err, ErrCodeNetwork) {
			return err
		}
	}
	return lastErr
}

// log 内部日志投递。Level 取 "info"/"warn"/"error"。logFn 为空时静默。
func (c *Charger) log(level, format string, args ...any) {
	if c.logFn == nil {
		return
	}
	c.logFn(level, fmt.Sprintf(format, args...))
}

// requestTimeout 取配置或默认。
func (c *Charger) requestTimeout() time.Duration {
	if c.cfg.RequestTimeout > 0 {
		return c.cfg.RequestTimeout
	}
	return DefaultRequestTimeout
}

// rateLimitWait 计算 429 等待秒数（取 max(配置基线, Retry-After, 默认值)）。
func (c *Charger) rateLimitWait(retryAfter string) time.Duration {
	base := c.cfg.RateLimitRetrySeconds
	if base <= 0 {
		base = RateLimitDefaultWait
	}
	if retryAfter != "" {
		if v, err := strconv.ParseFloat(retryAfter, 64); err == nil && v > base {
			base = v
		}
	}
	return time.Duration(base * float64(time.Second))
}

// ===== HTTP helpers =====
//
// 这些 helper 把 browser.Client.Do 包成"带 ctx + 超时 + 自动 JSON 编码 + body
// 读完关闭"的便捷函数。每一步都需要精确控制 headers，所以保留 raw header 注入
// 的入口（perRequestHeaders）。

// doJSON 发一个 application/json POST，自动编码 body 并解码到 out（out=nil 表示
// 不解码）。返回 status + body。
func (c *Charger) doJSON(
	ctx context.Context,
	cl *browser.Client,
	method, url string,
	body any,
	headers http.Header,
	out any,
) (int, []byte, error) {
	status, _, raw, err := c.doJSONFull(ctx, cl, method, url, body, headers, out)
	return status, raw, err
}

// doJSONFull 像 doJSON 但额外返回 response Header，给上层做诊断（approve blocked
// 时打 cf-ray / openai-* / set-cookie，方便定位风控原因）。
//
// 自带握手层抖动重试：cl.Do(req) 返回 TLS handshake EOF / connection reset 等
// 错误时（请求未送达服务端），自动 1.5s 退避重试，最多 3 次。响应头一旦回来，
// 4xx/5xx 不会触发重试 —— 由上层业务自己处理。
func (c *Charger) doJSONFull(
	ctx context.Context,
	cl *browser.Client,
	method, url string,
	body any,
	headers http.Header,
	out any,
) (int, http.Header, []byte, error) {
	var buf []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("marshal body: %w", err)
		}
		buf = b
	}
	build := func(rctx context.Context) (*http.Request, error) {
		var reqBody io.Reader
		if buf != nil {
			reqBody = bytes.NewReader(buf)
		}
		req, err := http.NewRequestWithContext(rctx, method, url, reqBody)
		if err != nil {
			return nil, err
		}
		if buf != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json, text/plain, */*")
		for k, vs := range headers {
			for _, v := range vs {
				req.Header.Set(k, v)
			}
		}
		c.injectAuth(req, cl)
		return req, nil
	}
	status, hdr, raw, err := c.doWithTransportRetry(ctx, cl, build, urlHostShort(url))
	if err != nil {
		return 0, nil, nil, err
	}
	if out != nil && len(raw) > 0 {
		// 容错：非 JSON 内容（HTML 错误页等）不强求解码。
		_ = json.Unmarshal(raw, out)
	}
	return status, hdr, raw, nil
}

// doWithTransportRetry 在 cl.Do 层做握手错误重试。仅当本次 Do 返回 error 时
// 才考虑重试 —— 响应头一旦回来，无论 status 多少都直接返回给调用方。
//
// 调用方必须用 buildReq 闭包重建请求（body Reader 不能复用）。
func (c *Charger) doWithTransportRetry(
	ctx context.Context,
	cl *browser.Client,
	buildReq func(rctx context.Context) (*http.Request, error),
	label string,
) (int, http.Header, []byte, error) {
	const maxAttempts = 3
	const baseDelay = 1500 * time.Millisecond
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		rctx, cancel := context.WithTimeout(ctx, c.requestTimeout())
		req, err := buildReq(rctx)
		if err != nil {
			cancel()
			return 0, nil, nil, err
		}
		resp, err := cl.Do(req)
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			status := resp.StatusCode
			hdr := resp.Header.Clone()
			resp.Body.Close()
			cancel()
			if i > 1 {
				c.log("info", "[Plus 升级] %s 第 %d 次重试成功", label, i)
			}
			return status, hdr, raw, nil
		}
		cancel()
		lastErr = err
		// 父 ctx 已取消（任务被 cancel 或整体超时）：直接抛出
		if pe := ctx.Err(); pe != nil {
			return 0, nil, nil, err
		}
		if !isProbableRateLimitNetwork(err) || i >= maxAttempts {
			return 0, nil, nil, err
		}
		c.log("warn", "[Plus 升级] %s 握手抖动（第 %d/%d 次：%v），%.1fs 后重试",
			label, i, maxAttempts, err, baseDelay.Seconds())
		select {
		case <-ctx.Done():
			return 0, nil, nil, ctx.Err()
		case <-timeAfter(baseDelay):
		}
	}
	return 0, nil, nil, lastErr
}

// urlHostShort 截 URL 的 host 部分用于日志（便于诊断哪一段被抖动）。
func urlHostShort(rawURL string) string {
	const prefix = "://"
	i := strings.Index(rawURL, prefix)
	if i < 0 {
		return rawURL
	}
	rest := rawURL[i+len(prefix):]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// doForm 发一个 application/x-www-form-urlencoded POST。自带握手层抖动重试。
func (c *Charger) doForm(
	ctx context.Context,
	cl *browser.Client,
	method, urlStr string,
	form string,
	headers http.Header,
	out any,
) (int, []byte, error) {
	build := func(rctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(rctx, method, urlStr, strings.NewReader(form))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json, text/plain, */*")
		for k, vs := range headers {
			for _, v := range vs {
				req.Header.Set(k, v)
			}
		}
		c.injectAuth(req, cl)
		return req, nil
	}
	status, _, raw, err := c.doWithTransportRetry(ctx, cl, build, urlHostShort(urlStr))
	if err != nil {
		return 0, nil, err
	}
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return status, raw, nil
}

// doGET 发一个 GET，可选 query 已拼到 url 里。自带握手层抖动重试。
//
// 注意：返回的 *http.Response 主要给上层取 Header 用；Body 已读完关闭。
func (c *Charger) doGET(
	ctx context.Context,
	cl *browser.Client,
	urlStr string,
	headers http.Header,
	out any,
) (int, *http.Response, []byte, error) {
	build := func(rctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json, text/plain, */*")
		for k, vs := range headers {
			for _, v := range vs {
				req.Header.Set(k, v)
			}
		}
		c.injectAuth(req, cl)
		return req, nil
	}
	status, hdr, raw, err := c.doWithTransportRetry(ctx, cl, build, urlHostShort(urlStr))
	if err != nil {
		return 0, nil, nil, err
	}
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	// 构造一个仅含 Header 的 *http.Response 给上层用（兼容旧签名）；Body 已读完。
	resp := &http.Response{
		StatusCode: status,
		Header:     hdr,
		Body:       io.NopCloser(bytes.NewReader(nil)),
	}
	return status, resp, raw, nil
}

// doRedirectGET GET 不跟随重定向（用来抓 Location 头取 snap_token）。
// 自带握手层抖动重试。
func (c *Charger) doRedirectGET(
	ctx context.Context,
	cl *browser.Client,
	urlStr string,
	headers http.Header,
) (int, string, []byte, error) {
	prev := cl.HTTP.CheckRedirect
	cl.HTTP.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	defer func() { cl.HTTP.CheckRedirect = prev }()

	build := func(rctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "*/*")
		for k, vs := range headers {
			for _, v := range vs {
				req.Header.Set(k, v)
			}
		}
		c.injectAuth(req, cl)
		return req, nil
	}
	status, hdr, raw, err := c.doWithTransportRetry(ctx, cl, build, urlHostShort(urlStr))
	if err != nil {
		return 0, "", nil, err
	}
	return status, hdr.Get("Location"), raw, nil
}

// injectAuth 自动给 chatgpt.com 域请求挂上 Authorization / Device 头。
// Stripe / Midtrans / GoPay 域不需要 ChatGPT 的 Auth，故只识别 chatgpt.com host。
//
// 关键：**不再手动设 Cookie 头**。原来塞 `Cookie: oai-did=xxx` 会让 Go stdlib
// 跳过 cookie jar 的自动注入，导致 warmup 阶段累积的 `__Secure-next-auth.session-token`
// 等关键 cookie 全部丢失。现在 cookies 在 Charger.New 阶段灌进 jar，stdlib
// 自己负责拼装完整 Cookie 头。
func (c *Charger) injectAuth(req *http.Request, cl *browser.Client) {
	if !strings.HasSuffix(strings.ToLower(req.URL.Host), "chatgpt.com") {
		return
	}
	if c.cfg.Auth.AccessToken != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Auth.AccessToken)
	}
	if c.cfg.Auth.DeviceID != "" && req.Header.Get("OAI-Device-Id") == "" {
		req.Header.Set("OAI-Device-Id", c.cfg.Auth.DeviceID)
	}
	if c.cfg.Auth.LanguageHeader != "" && req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", c.cfg.Auth.LanguageHeader)
	} else if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	}
}

// midtransBasicAuth 计算 Midtrans 公开 client id 的 Basic auth 头。
func (c *Charger) midtransBasicAuth() string {
	tok := base64.StdEncoding.EncodeToString([]byte(c.midtransClID + ":"))
	return "Basic " + tok
}

// shortBody 截断响应体用于错误展示，避免 100KB HTML 灌进日志。
func shortBody(b []byte, n int) string {
	s := string(b)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// maskURL 把 URL query 部分隐去，便于日志展示而不泄露 token / session_id。
// 形如 "https://chatgpt.com/checkout/openai_ie/cs_live_xxx?param=secret"
// → "https://chatgpt.com/checkout/openai_ie/cs_live_xxx?<redacted>"。
func maskURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	q := strings.Index(rawURL, "?")
	if q < 0 {
		return rawURL
	}
	return rawURL[:q+1] + "<redacted>"
}

// 时间相关变量化以便测试 stub。生产用 time.Now / time.After。
var (
	nowFunc   = time.Now
	timeAfter = time.After
)

// newExtBrowser 创建/切换 ext (Phase B) 客户端，复用 New() 的初始化逻辑。
func newExtBrowser(proxyURL string, timeout time.Duration) (*browser.Client, error) {
	return browser.New(browser.Options{ProxyURL: proxyURL, Timeout: timeout})
}

// maskedProxy 把代理 URL 的密码部分隐去，便于安全打日志。
func maskedProxy(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// http://user:secret@host:port → http://user:***@host:port
	const tail = "@"
	i := strings.Index(rawURL, "://")
	if i < 0 {
		return rawURL
	}
	rest := rawURL[i+3:]
	at := strings.Index(rest, tail)
	if at < 0 {
		return rawURL
	}
	auth := rest[:at]
	colon := strings.Index(auth, ":")
	if colon < 0 {
		return rawURL[:i+3] + auth + tail + rest[at+1:]
	}
	return rawURL[:i+3] + auth[:colon+1] + "***" + tail + rest[at+1:]
}

