// Package gpt 实现 ChatGPT / OpenAI 平台账号自动注册 dispatcher。
//
// 本实现严格对齐 basketikun/chatgpt2api 的 services/register/openai_register.py
// 注册流程，主要差异：
//
//   - 走真实 platform.openai.com OAuth 客户端（client_id / redirect_uri / audience）
//   - 真 Sentinel：调 /backend-api/sentinel/req 取 seed/difficulty，本地 FNV1a-32 PoW
//   - 注册完成后 *单独* 起一次 password 登录换 access_token / refresh_token，避免
//     依赖 create_account 直接返回 continue_url（OpenAI 在 2025 末把这条短路砍了）
//   - 全程把 W3C/Datadog trace headers + oai-did cookie + sentinel token 都补齐
//
// 落库表：pool_gpt（access_token / refresh_token / id_token / 邮箱 / 密码）。
package gpt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/regkit/browser"
	"github.com/kleinai/backend/internal/regkit/dispatcher"
	"github.com/kleinai/backend/internal/regkit/mailbox"
	"github.com/kleinai/backend/internal/regkit/nameset"
	"github.com/kleinai/backend/internal/regkit/smspool"
	"github.com/kleinai/backend/internal/service"
)

const (
	authBase     = "https://auth.openai.com"
	platformBase = "https://platform.openai.com"

	// === Codex CLI OAuth 客户端（默认走这一条） ===
	// 与 openai/codex（codex-rs/login/src/server.rs）和 Ttungx/codex_auto_register
	// (codex/protocol_keygen.py) 完全一致，产物可直接被 CLIProxyAPI v6 / Codex CLI 使用。
	codexOAuthClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexOAuthRedirectURI = "http://localhost:1455/auth/callback"
	// 注意：Codex 客户端的 scope 比 platform 多了 connectors，少了 audience。
	codexOAuthScope = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	// 与官方 codex-cli 一致的 originator（写死，不参与签名）。
	codexOriginator = "codex_cli_rs"

	// === 备用：platform.openai.com OAuth 客户端 ===
	// 仅用于 platformAuthorize() 这一步：起注册流程时拿 login_session cookie。
	// 注册流程跑完之后我们走 codex 的 /oauth/authorize 拿真正给 CLIProxyAPI 用的 token。
	platformOAuthClientID    = "app_2SKx67EdpoN0G6j64rFvigXD"
	platformOAuthRedirectURI = platformBase + "/auth/callback"
	platformOAuthAudience    = "https://api.openai.com/v1"
	platformAuth0Client      = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9"
	defaultScope             = "openid profile email offline_access"
)

// Dispatcher GPT 注册 dispatcher。
type Dispatcher struct {
	dispatcher.Deps
	Pool *service.PoolGptService
}

// state 注册过程中跨步骤累积的中间值。
type state struct {
	deviceID string
	pkce     PKCEPair
	stateVal string
	nonceVal string
	sentinel *SentinelGenerator

	// email / password 在 /log-in/password 墙触发时复用，重新登录刷会话。
	email    string
	password string

	// smsCountriesOverride 在 task.payload.sms_country 非空时填充，用于
	// AcquirePhoneWithCountries 临时覆盖 system_config 的 sms.country。
	// nil 表示按系统配置走。
	smsCountriesOverride []int

	// codexFlowInit 标识 codex authorize 的 PKCE/state/nonce 是否已经为这条任务
	// 初始化过。第一次进 codexAuthorizeAndExchange 会置 true；后续墙重试不重置，
	// 复用同一组凭据，让 OpenAI 视为"同一 OAuth 会话继续"，不再要求重新登录。
	codexFlowInit bool

	// codexAuthCode 缓存"在 wall 处理过程中顺手抓到的 ?code="：
	// 比如 password/verify 后 follow continue_url 时一路 chase 到 ?code= 的场景，
	// 下一次 codexAuthorizeAndExchange 看到这里有值就直接跳过 chase 走 token 兑换。
	codexAuthCode string

	accessToken  string
	refreshToken string
	idToken      string
	apiKey       string
}

// Run 实现 service.RegisterDispatcher。
func (d *Dispatcher) Run(ctx context.Context, svc *service.RegisterTaskService, task *model.RegisterTask) error {
	_ = svc.UpdateProgress(ctx, task.ID, "preflight", 5)
	if d.Pool == nil {
		return errors.New("PoolGptService 未注入（内部错误）")
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
	birthday := nameset.BirthdayString()

	st := &state{
		deviceID:             newDeviceID(),
		pkce:                 NewPKCE(),
		stateVal:             base64URL(randomBytes(32)),
		nonceVal:             base64URL(randomBytes(32)),
		smsCountriesOverride: parseSMSCountriesPayload(payload.SMSCountry),
	}
	if len(st.smsCountriesOverride) > 0 {
		svc.LogInfo(ctx, task.ID, fmt.Sprintf("payload 指定 sms.country override = %v", st.smsCountriesOverride))
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
	st.sentinel = NewSentinelGenerator(st.deviceID, bc.Profile.UserAgent)
	// oai-did cookie 必须挂在 .auth.openai.com 上，是 sentinel 后端识别"同一会话"的关键。
	setOaiDID(bc.Jar, st.deviceID)

	_ = svc.UpdateProgress(ctx, task.ID, "acquire_mail", 18)
	mailCfg := dispatcher.BuildMailBackendConfig(ctx, d.SysCfg)
	mailCfg.Proxy = resolved.URL
	var acq *mailbox.AcquireResult
	if task.MailID != nil && *task.MailID > 0 {
		acq, err = d.MailMgr.AcquireByID(ctx, *task.MailID, "gpt", mailCfg)
	} else {
		// AcquireFresh：CF Worker 配了就即时签发（不入库），其他模式回退池化。
		acq, err = d.MailMgr.AcquireFresh(ctx, "gpt", mailCfg)
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

	// 1) GET /api/accounts/authorize：种 oai-did + login_session cookie。
	//
	// auth.openai.com 完全在 Cloudflare 后面；某些代理 IP 会被 CF 直接 403。
	// 这里先用初次挑到的代理试，403 就最多换 3 次代理重试 — 只有这一步换，
	// 后面 sentinel / consent 链都已经跟"已选 cookie 会话"绑定。
	_ = svc.UpdateProgress(ctx, task.ID, "authorize", 25)
	if err := platformAuthorize(ctx, bc, st, email); err != nil {
		retryErr := err
		excluded := []uint64{resolved.ID}
		for attempt := 0; attempt < 3; attempt++ {
			if !strings.Contains(retryErr.Error(), "HTTP 403") {
				break
			}
			svc.LogInfo(ctx, task.ID,
				fmt.Sprintf("authorize 被 Cloudflare 403，第 %d 次换代理重试", attempt+1))
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
			st.sentinel = NewSentinelGenerator(st.deviceID, bc.Profile.UserAgent)
			setOaiDID(bc.Jar, st.deviceID)
			svc.LogInfo(ctx, task.ID, fmt.Sprintf("切换到代理 %s", dispatcher.MaskProxy(next.URL)))
			retryErr = platformAuthorize(ctx, bc, st, email)
			if retryErr == nil {
				break
			}
		}
		if retryErr != nil {
			return failMail(fmt.Sprintf("authorize 失败：%v", retryErr))
		}
	}

	// 2) POST /api/accounts/user/register：提交 email + password（带 sentinel）。
	_ = svc.UpdateProgress(ctx, task.ID, "user_register", 40)
	st.email = email
	st.password = payload.Password
	if err := userRegister(ctx, bc, st, email, payload.Password); err != nil {
		return failMail(fmt.Sprintf("user/register 失败：%v", err))
	}

	// 3) GET /api/accounts/email-otp/send。
	_ = svc.UpdateProgress(ctx, task.ID, "email_otp_send", 55)
	otpSent := time.Now()
	if err := sendEmailOTP(ctx, bc, st); err != nil {
		return failMail(fmt.Sprintf("email-otp/send 失败：%v", err))
	}
	otp, err := acq.Mailbox.WaitCode(ctx, mailbox.WaitOptions{
		Provider: mailbox.ProviderGPT,
		SinceTS:  otpSent.Add(-30 * time.Second),
		Timeout:  240 * time.Second,
	})
	if err != nil {
		// 重发兜底：OpenAI 偶尔队列拖延，给一次重发 + 180s 等待机会。
		svc.LogWarn(ctx, task.ID, fmt.Sprintf("第一次等码失败：%v，尝试重发", err))
		retryAt := time.Now()
		if err2 := sendEmailOTP(ctx, bc, st); err2 != nil {
			return failMail(fmt.Sprintf("重发 email-otp 失败：%v", err2))
		}
		otp, err = acq.Mailbox.WaitCode(ctx, mailbox.WaitOptions{
			Provider: mailbox.ProviderGPT,
			SinceTS:  retryAt.Add(-30 * time.Second),
			Timeout:  180 * time.Second,
		})
		if err != nil {
			return failMail(fmt.Sprintf("等待邮箱验证码失败（已重发一次）：%v", err))
		}
	}
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("已收到邮箱验证码 %s", dispatcher.MaskOTP(otp)))

	// 4) POST /api/accounts/email-otp/validate（先不带 sentinel；如失败再带 sentinel 重试）。
	_ = svc.UpdateProgress(ctx, task.ID, "email_otp_validate", 65)
	if err := validateEmailOTP(ctx, bc, st, otp); err != nil {
		return failMail(fmt.Sprintf("email-otp/validate 失败：%v", err))
	}

	// 5) POST /api/accounts/create_account（提交 name + birthdate）。
	_ = svc.UpdateProgress(ctx, task.ID, "create_account", 75)
	if err := createAccount(ctx, bc, st,
		strings.TrimSpace(payload.FirstName+" "+payload.LastName), birthday); err != nil {
		return failMail(fmt.Sprintf("create_account 失败：%v", err))
	}

	// 6) Platform OAuth 取 token：参考 basketikun/chatgpt2api 的 _login_and_exchange_tokens。
	//
	//    用 platform.openai.com 的 OAuth client (app_2SKx67Edpo...) 做"独立登录"：
	//    新 PKCE / 清旧 OAuth session cookies / 新 sentinel / authorize/continue → password/verify
	//    → 可能 email_otp → chase consent → workspace/select → /oauth/token
	//
	//    与 codex client (app_EMoamEEZ73f0CkXaXp7hrann) 流程的关键区别：
	//      * platform client **不会**触发 add-phone 墙（OpenAI 对 platform OAuth client
	//        不强制要求绑定手机号），所以可以彻底绕开 hero-sms / phone challenge。
	//      * 拿到的 access_token 是 api.openai.com/v1 audience，可以直接当 OpenAI Bearer token
	//        调 /v1/models /v1/chat/completions 等 API（chatgpt2api 的目标产物）。
	//      * refresh_token 也是 platform 的，跟 codex 隔离（同一账户可以共存）。
	//
	//    暂时不做 token-exchange 拿 sk- api_key（CLIProxyAPI 用的是 codex token，跟 platform 隔离）。
	_ = svc.UpdateProgress(ctx, task.ID, "exchange_token", 88)
	tokens, terr := d.platformLoginAndExchange(ctx, bc, st, acq.Mailbox, svc, task.ID, otp)
	if terr != nil {
		// 没拿到 token 一律视为失败：不入 pool_gpt，不让任务标 success。
		// 邮箱按软失败回收（账号在 OpenAI 那边已经注册成功了，下次可由人工或离线
		// "登录拿 token" 工具补救；此处先 Release 邮箱避免被误标 broken）。
		_ = d.MailMgr.Release(context.Background(), acq.Row.ID)
		mailReleased = true
		_ = acq.Mailbox.Close()
		return fmt.Errorf("platform token 换取失败（账号 %s 已在 OpenAI 注册，但未拿到 AT，已不入号池）：%w", email, terr)
	}
	if tokens == nil || strings.TrimSpace(tokens.access) == "" {
		_ = d.MailMgr.Release(context.Background(), acq.Row.ID)
		mailReleased = true
		_ = acq.Mailbox.Close()
		return fmt.Errorf("platform /oauth/token 返回缺少 access_token，账号 %s 不入号池", email)
	}
	st.accessToken = tokens.access
	st.refreshToken = tokens.refresh
	st.idToken = tokens.idToken
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("platform /oauth/token 成功（at=%d rt=%d id=%d）",
		len(tokens.access), len(tokens.refresh), len(tokens.idToken)))

	// 7) 写号池（access_token 是必须的，refresh/id 可选）。
	_ = svc.UpdateProgress(ctx, task.ID, "persist", 96)
	created, err := d.Pool.Create(ctx, &dto.GptPoolCreateReq{
		Email:         email,
		Password:      payload.Password,
		AccessToken:   st.accessToken,
		RefreshToken:  st.refreshToken,
		IDToken:       st.idToken,
		APIKey:        st.apiKey,
		OAuthIssuer:   authBase,
		OAuthClientID: platformOAuthClientID,
		Status:        model.GPTStatusValid,
		Notes:         payload.Notes,
	})
	if err != nil {
		return fmt.Errorf("写入 pool_gpt 失败：%w", err)
	}

	_ = d.MailMgr.MarkRegistered(ctx, acq.Row.ID, created.ID)
	mailReleased = true
	_ = acq.Mailbox.Close()

	return svc.FinishSuccess(ctx, task.ID, created.ID, map[string]any{
		"pool_account_id":   created.ID,
		"email":             email,
		"has_access_token":  st.accessToken != "",
		"has_refresh_token": st.refreshToken != "",
		"has_id_token":      st.idToken != "",
		"has_api_key":       st.apiKey != "",
	})
}

// === HTTP 步骤 ===

// makeTraceHeaders 构造 W3C traceparent + Datadog x-datadog-* headers。
//
// OpenAI 的 auth API 从前端 SDK 调用时一定带这些头；缺失后 sentinel 会把
// 请求标为"非来自浏览器"，下游会更严格地校验 token。
func makeTraceHeaders() map[string]string {
	traceID := fmt.Sprintf("%016x", uint64Random()&0xffffffffffffffff)
	parentID := fmt.Sprintf("%016x", uint64Random()&0xffffffffffffffff)
	return map[string]string{
		"traceparent":                 fmt.Sprintf("00-%s-%s-01", strings.ReplaceAll(newDeviceID(), "-", ""), parentID),
		"tracestate":                  "dd=s:1;o:rum",
		"x-datadog-origin":            "rum",
		"x-datadog-parent-id":         parentID,
		"x-datadog-sampling-priority": "1",
		"x-datadog-trace-id":          traceID,
	}
}

// jsonHeaders 构造 application/json POST 用的标准头集合（不含 sentinel）。
func jsonHeaders(bc *browser.Client, st *state, referer string) map[string]string {
	h := map[string]string{
		"accept":          "application/json",
		"content-type":    "application/json",
		"accept-language": bc.Profile.Locale,
		"origin":          authBase,
		"priority":        "u=1, i",
		"referer":         referer,
		"sec-fetch-dest":  "empty",
		"sec-fetch-mode":  "cors",
		"sec-fetch-site":  "same-origin",
		"oai-device-id":   st.deviceID,
	}
	for k, v := range makeTraceHeaders() {
		h[k] = v
	}
	return h
}

// navHeaders 用于 "整页跳转" 类 GET（authorize / email-otp/send / consent 链）。
//
// site 由调用方传入，避免"声称 same-origin 但 referer 跨子域"导致 CF 直接 403：
//
//   - same-origin：referer / 当前 URL 同 host
//   - same-site  ：跨子域同主域（platform.openai.com → auth.openai.com）
//   - cross-site ：跨主域
//   - none       ：直接打开（无 referer）
func navHeaders(bc *browser.Client, st *state, site string) map[string]string {
	if site == "" {
		site = "same-origin"
	}
	h := map[string]string{
		"accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"accept-language":           bc.Profile.Locale,
		"sec-fetch-dest":            "document",
		"sec-fetch-mode":            "navigate",
		"sec-fetch-site":            site,
		"sec-fetch-user":            "?1",
		"upgrade-insecure-requests": "1",
		"oai-device-id":             st.deviceID,
	}
	return h
}

// platformAuthorize 启动 OAuth 注册流（用 platform.openai.com 的 client_id）：
//
//	GET https://auth.openai.com/api/accounts/authorize?client_id=app_2SKx67Edpo...
//
// 注册必须走 platform 的 client_id：codex CLI 客户端不允许通过 /api/accounts/user/register
// 创建新用户（OpenAI 后端把 codex client 的 sign-up 限制为 chatgpt.com 重定向）。
//
// 完成 create_account 之后会保留 login_session / oai-client-auth-session cookie，
// 这一份 cookie 是"用户已登录"的全局凭证（OpenAI 在多个 client 之间共享识别），
// 因此后续 codexAuthorizeAndExchange 用 codex client_id 起第二次 authorize 时，
// 会按 cookie 把请求当作"已登录用户"，跳过密码验证直接到 consent → 拿 code。
func platformAuthorize(ctx context.Context, bc *browser.Client, st *state, email string) error {
	v := url.Values{}
	v.Set("issuer", authBase)
	v.Set("client_id", platformOAuthClientID)
	v.Set("audience", platformOAuthAudience)
	v.Set("redirect_uri", platformOAuthRedirectURI)
	v.Set("device_id", st.deviceID)
	v.Set("screen_hint", "login_or_signup")
	v.Set("max_age", "0")
	v.Set("login_hint", email)
	v.Set("scope", defaultScope)
	v.Set("response_type", "code")
	v.Set("response_mode", "query")
	v.Set("state", st.stateVal)
	v.Set("nonce", st.nonceVal)
	v.Set("code_challenge", st.pkce.Challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("auth0Client", platformAuth0Client)
	u := authBase + "/api/accounts/authorize?" + v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	// ref 用 sec-fetch-site=same-origin（即便严格意义上 platform→auth 是 same-site）；
	// 实测 same-site 会被 Sentinel 视为可疑，导致 OpenAI 把 session 推到 log-in 而非
	// /create-account/password，进而 user/register 直接 invalid_auth_step。
	for k, v := range navHeaders(bc, st, "same-origin") {
		req.Header.Set(k, v)
	}
	req.Header.Set("Referer", platformBase+"/")
	resp, err := bc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256*1024))
	// ref 严格只接受 200 —— 任何 redirect-final 状态都意味着 OpenAI 没把 session 推到
	// /create-account/password。我们这里也对齐：>=400 直接报错。
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// userRegister 提交 email + password 创建草稿账号。
//
// 必须带 sentinel(flow=username_password_create)，否则 OpenAI 直接 403。
//
// 常见错误：
//   - invalid_auth_step：邮箱在 OpenAI 端已开过 OAuth 草稿且 step != register，
//     比如上一次注册任务跑到 email-otp/send 但没完成验证（OTP 没拉到、任务挂了等）。
//     这种邮箱在 OpenAI 端已经被锁定为 "等 email_otp"，再调 /user/register 必然
//     被拒。dispatcher 应把这种邮箱标 failed 隔离，不再领取，避免一直撞同样的错。
//   - "Failed to create account"：邮箱域名疑似被 OpenAI 标记为滥用域。
func userRegister(ctx context.Context, bc *browser.Client, st *state, email, password string) error {
	tok, err := st.sentinel.SentinelToken(ctx, bc.HTTP, "username_password_create")
	if err != nil {
		return fmt.Errorf("sentinel(username_password_create): %w", err)
	}
	body, _ := json.Marshal(map[string]any{
		"username": email,
		"password": password,
	})
	headers := jsonHeaders(bc, st, authBase+"/create-account/password")
	headers["openai-sentinel-token"] = tok
	resp, err := postJSON(ctx, bc, authBase+"/api/accounts/user/register", body, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 || resp.StatusCode == 201 {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if bytes.Contains(raw, []byte(`"invalid_auth_step"`)) {
		return fmt.Errorf("此邮箱已被 OpenAI 锁定在 email_otp 阶段（上一次注册未完成 OTP 验证）。"+
			"邮箱已自动隔离，不会再次领取。HTTP %d", resp.StatusCode)
	}
	if bytes.Contains(raw, []byte("Failed to create account")) {
		return fmt.Errorf("HTTP %d (邮箱域名疑被 OpenAI 标记为滥用): %s", resp.StatusCode, snippet(raw))
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
}

// sendEmailOTP 触发 OpenAI 给当前 session 的邮箱发一封 6 位验证码。
//
// 重要：这是 GET 请求；用 navigate 头集合而不是 cors，否则会被 sentinel 砍掉。
func sendEmailOTP(ctx context.Context, bc *browser.Client, st *state) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		authBase+"/api/accounts/email-otp/send", nil)
	for k, v := range navHeaders(bc, st, "same-origin") {
		req.Header.Set(k, v)
	}
	req.Header.Set("Referer", authBase+"/create-account/password")
	resp, err := bc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// validateEmailOTP 提交 6 位邮箱验证码。
//
// 与 Python 参考一致：先不带 sentinel 试一次（部分情况下 OpenAI 不强制要求），
// 失败再带 sentinel(flow=authorize_continue) 重试。
func validateEmailOTP(ctx context.Context, bc *browser.Client, st *state, otp string) error {
	body, _ := json.Marshal(map[string]any{"code": otp})
	headers := jsonHeaders(bc, st, authBase+"/email-verification")
	resp, err := postJSON(ctx, bc, authBase+"/api/accounts/email-otp/validate", body, headers)
	if err == nil && resp.StatusCode == 200 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*1024))
		_ = resp.Body.Close()
		return nil
	}
	if resp != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*1024))
		_ = resp.Body.Close()
	}
	tok, err := st.sentinel.SentinelToken(ctx, bc.HTTP, "authorize_continue")
	if err != nil {
		return fmt.Errorf("sentinel(authorize_continue): %w", err)
	}
	headers["openai-sentinel-token"] = tok
	resp2, err := postJSON(ctx, bc, authBase+"/api/accounts/email-otp/validate", body, headers)
	if err != nil {
		return err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp2.Body, 32*1024))
		return fmt.Errorf("HTTP %d: %s", resp2.StatusCode, snippet(raw))
	}
	return nil
}

// createAccount 提交资料创建账号（name + birthdate）。
//
// 带 sentinel(flow=oauth_create_account)。
func createAccount(ctx context.Context, bc *browser.Client, st *state, fullName, birthday string) error {
	tok, err := st.sentinel.SentinelToken(ctx, bc.HTTP, "oauth_create_account")
	if err != nil {
		return fmt.Errorf("sentinel(oauth_create_account): %w", err)
	}
	body, _ := json.Marshal(map[string]any{
		"name":      fullName,
		"birthdate": birthday,
	})
	headers := jsonHeaders(bc, st, authBase+"/about-you")
	headers["openai-sentinel-token"] = tok
	resp, err := postJSON(ctx, bc, authBase+"/api/accounts/create_account", body, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 || resp.StatusCode == 302 {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
}

// === 第二段：Codex CLI 换 token ===

type tokenSet struct {
	access  string
	refresh string
	idToken string
}

// codexAuthorizeAndExchange 用 Codex CLI 的 client_id 起一次新的 OAuth 授权：
//
// 注册阶段已经种下 login_session 这种"用户已登录"cookie，OpenAI 在所有 client
// 之间共享识别 —— 第二次用 codex client 起 authorize 时不需要再输密码，但因为
// client_id 切换了，oai-client-auth-session 会被重新种成 codex 专属的状态，
// PKCE / state / nonce 都要换成给 codex 这次流程的新值。
//
// 流程：
//
//	1. GET /oauth/authorize?client_id=app_EMoamEEZ...        → 302 到 redirect_uri?code=
//	   或 → 302 到 sign-in-with-chatgpt/codex/consent  → 我们 POST consent 后再拿 code
//	2. POST /oauth/token (grant_type=authorization_code)     → access/refresh/id_token
func codexAuthorizeAndExchange(ctx context.Context, bc *browser.Client, st *state, email string) (*tokenSet, error) {
	// codex 流程独立的 PKCE / state / nonce —— token 端点会用这份 verifier。
	//
	// 但第一次失败后（撞 /log-in/password、/add-phone 等墙）的重试，必须复用
	// 同一组 PKCE/state/nonce，否则 OpenAI 视为"全新 OAuth flow"，会强行再要求一遍
	// 密码验证（短时间内多次 authorize/continue 还会撞 HTTP 429 rate limit）。
	if !st.codexFlowInit {
		st.pkce = NewPKCE()
		st.stateVal = base64URL(randomBytes(32))
		st.nonceVal = base64URL(randomBytes(32))
		st.codexFlowInit = true
	}

	// 如果 wall 处理（如 password/verify continue_url chase）已经预先拿到 code，
	// 直接换 token，避免重复 authorize（短时间内多次 authorize 会撞 OpenAI 风控）。
	if code := strings.TrimSpace(st.codexAuthCode); code != "" {
		st.codexAuthCode = ""
		tokens, terr := doTokenExchangeCodex(ctx, bc, st, code)
		if terr != nil {
			return nil, fmt.Errorf("codex /oauth/token (cached code): %w", terr)
		}
		return tokens, nil
	}

	authURL := buildCodexAuthorizeURL(st, email)

	code, diag, err := codexFollowAndConsent(ctx, bc, st, authURL)
	if err != nil {
		return nil, fmt.Errorf("codex chase code: %w; trace=%s", err, strings.Join(diag, " | "))
	}
	tokens, terr := doTokenExchangeCodex(ctx, bc, st, code)
	if terr != nil {
		return nil, fmt.Errorf("codex /oauth/token: %w", terr)
	}
	return tokens, nil
}

// errAddPhoneRequired 由 codexFollowAndConsent 在检测到 /add-phone 页面时返回。
//
// Run() 拿到它会调用 SMS 流程（hero-sms 接码 → /add-phone/send → /phone-otp/validate），
// 完成后再发起一次 codexAuthorizeAndExchange，第二次 chase 链路就不会再撞到墙。
var errAddPhoneRequired = errors.New("add_phone_required")

// errLoginPasswordRequired 由 codexFollowAndConsent 在检测到 /log-in/password
// 页面时返回。Run() 拿到它会调用 d.solveLoginWall（authorize/continue → password/verify
// → mailbox WaitCode → email-otp/validate），完成后重新发起 codexAuthorizeAndExchange。
var errLoginPasswordRequired = errors.New("log_in_password_required")

// codexFollowAndConsent 启动 Codex authorize 链路，跟跳到 ?code=。
//
// - 每跳禁止自动 follow，方便逐步读 Location 头与状态码；
// - 中间如果遇到需要 consent 的页面（path 含 sign-in-with-chatgpt 或 consent 二字），
//   主动 POST /api/accounts/consent 提交同意；
// - 检测到 /add-phone 类 URL 时立即返回 errAddPhoneRequired，让上层走 SMS 兜底；
// - 同时在每跳记录 trace（status + url 前 96 字节），失败时返回给上游打日志。
func codexFollowAndConsent(ctx context.Context, bc *browser.Client, st *state, startURL string) (string, []string, error) {
	cur := startURL
	trace := make([]string, 0, 16)
	consentTried := false
	for hop := 0; hop < 12 && cur != ""; hop++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cur, nil)
		// 起跳：referer 是 platform.openai.com（同主域跨子域 = same-site）；
		// 后续跳：referer 自动用上一跳 URL；浏览器在 same-origin 链上是 same-origin。
		site := "same-origin"
		if hop == 0 {
			site = "same-site"
		}
		for k, v := range navHeaders(bc, st, site) {
			req.Header.Set(k, v)
		}
		if hop == 0 {
			req.Header.Set("Referer", platformBase+"/")
		}
		origCheck := bc.HTTP.CheckRedirect
		bc.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := bc.Do(req)
		bc.HTTP.CheckRedirect = origCheck
		if err != nil {
			return "", trace, fmt.Errorf("hop %d GET %s 失败: %w", hop, snippet([]byte(cur)), err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		_ = resp.Body.Close()
		trace = append(trace, fmt.Sprintf("[%d] %d %s", hop, resp.StatusCode, snippet([]byte(cur))))

		if c := pickCode(cur); c != "" {
			return c, trace, nil
		}
		if resp.Request != nil && resp.Request.URL != nil {
			if c := pickCode(resp.Request.URL.String()); c != "" {
				return c, trace, nil
			}
		}
		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if c := pickCode(loc); c != "" {
			return c, trace, nil
		}

		// /add-phone 墙：OpenAI 在 codex 客户端下强制要求绑定手机号才放过 OAuth。
		// 这里立刻返回 sentinel，让上层调 SMS 流程。
		if isAddPhoneURL(cur) || isAddPhoneURL(loc) {
			trace = append(trace, "[hit] add-phone wall")
			return "", trace, errAddPhoneRequired
		}

		// /log-in/password 墙：OpenAI 在 codex authorize 时常常要求重新走一遍
		// "邮箱 → 密码 → 邮件 OTP" 三段式登录。这里立即返回 sentinel，让上层用
		// d.solveLoginWall（需要 mailbox 等依赖）兜底，再重新发起 codex authorize。
		if isLoginPasswordURL(cur) || isLoginPasswordURL(loc) {
			trace = append(trace, "[hit] log-in/password wall")
			return "", trace, errLoginPasswordRequired
		}

		// 200 OK 上的 consent 页：OpenAI 用 React 渲染一个表单，
		// "Authorize" 按钮其实就是 POST /api/accounts/consent，载荷只看 cookie 上下文。
		if resp.StatusCode == 200 && !consentTried &&
			(strings.Contains(strings.ToLower(cur), "consent") ||
				strings.Contains(strings.ToLower(cur), "sign-in-with-chatgpt") ||
				strings.Contains(strings.ToLower(string(body)), "consent")) {
			consentTried = true
			c, ctrace, cerr := postCodexConsent(ctx, bc, st, cur)
			trace = append(trace, ctrace...)
			if cerr == nil && c != "" {
				return c, trace, nil
			}
			if cerr != nil {
				trace = append(trace, fmt.Sprintf("[consent err] %v", cerr))
			}
			// consent 之后链路可能继续 302，把 cur 重置回 authorize 入口让 OpenAI 重新发码。
			// 这里直接 break，让上层兜底走 workspace/organization 的旧路径。
			break
		}

		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			break
		}
		if strings.HasPrefix(loc, "/") {
			cur = authBase + loc
		} else {
			cur = loc
		}
	}

	// 兜底：尝试 workspace/select + organization/select。
	if c, err := tryConsentSelect(ctx, bc, st, startURL); err == nil && c != "" {
		trace = append(trace, "[ok] workspace/org select 兜底命中 code")
		return c, trace, nil
	} else if err != nil {
		trace = append(trace, fmt.Sprintf("[fallback] workspace select 失败: %v", err))
	}
	return "", trace, errors.New("整条 continue_url 链路里都没找到 ?code=")
}

// isAddPhoneURL 路径含 add-phone / phone-verification 类 segment 即视为命中手机墙。
func isAddPhoneURL(u string) bool {
	if u == "" {
		return false
	}
	low := strings.ToLower(u)
	return strings.Contains(low, "/add-phone") ||
		strings.Contains(low, "/phone-verification") ||
		strings.Contains(low, "/phone-otp")
}

// isLoginPasswordURL 检测 OpenAI 在 codex authorize 中弹出的二次密码登录页。
//
// 命中典型路径：
//   - /log-in/password
//   - /log-in/password?login_challenge=...
//   - /log-in（未带子页时也归到这里走密码流程）
//
// 不包含 /log-in/code（邮箱验证码登录）—— 我们刚注册的密码登录走 password 那条。
func isLoginPasswordURL(u string) bool {
	if u == "" {
		return false
	}
	low := strings.ToLower(u)
	parsed, err := url.Parse(low)
	if err != nil {
		return strings.Contains(low, "/log-in/password") ||
			strings.HasSuffix(low, "/log-in")
	}
	host := parsed.Host
	if host != "" && !strings.HasSuffix(host, "auth.openai.com") {
		return false
	}
	p := parsed.Path
	switch {
	case p == "/log-in", p == "/log-in/", p == "/log-in/password":
		return true
	case strings.HasPrefix(p, "/log-in/password/"):
		return true
	}
	return false
}

// postCodexConsent 模拟 consent 页"Authorize"按钮：
//
//	POST https://auth.openai.com/api/accounts/consent
//
// 与 oai-client-auth-session cookie 中携带的 client_id / scope / state 自动绑定，
// 不需要在请求体里再传一遍。提交成功后会 302 到 redirect_uri?code=...
func postCodexConsent(ctx context.Context, bc *browser.Client, st *state, consentURL string) (string, []string, error) {
	trace := make([]string, 0, 4)
	headers := jsonHeaders(bc, st, consentURL)
	body, _ := json.Marshal(map[string]any{"action": "authorize"})

	origCheck := bc.HTTP.CheckRedirect
	bc.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := postJSON(ctx, bc, authBase+"/api/accounts/consent", body, headers)
	bc.HTTP.CheckRedirect = origCheck
	if err != nil {
		return "", trace, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	trace = append(trace, fmt.Sprintf("[consent] POST %d body=%s", resp.StatusCode, snippet(raw)))

	loc := strings.TrimSpace(resp.Header.Get("Location"))
	if c := pickCode(loc); c != "" {
		return c, trace, nil
	}
	// 一些路径下 OpenAI 把 redirect 放在 JSON body 的 continue_url 里。
	if resp.StatusCode == 200 || resp.StatusCode == 201 {
		var out struct {
			ContinueURL string `json:"continue_url"`
		}
		if json.Unmarshal(raw, &out) == nil && out.ContinueURL != "" {
			next := out.ContinueURL
			if strings.HasPrefix(next, "/") {
				next = authBase + next
			}
			if c := pickCode(next); c != "" {
				return c, trace, nil
			}
			// 再 GET 一跳。
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
			for k, v := range navHeaders(bc, st, "same-origin") {
				req.Header.Set(k, v)
			}
			origCheck = bc.HTTP.CheckRedirect
			bc.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			r2, e2 := bc.Do(req)
			bc.HTTP.CheckRedirect = origCheck
			if e2 == nil {
				_ = r2.Body.Close()
				if c := pickCode(strings.TrimSpace(r2.Header.Get("Location"))); c != "" {
					return c, trace, nil
				}
			}
		}
	}
	return "", trace, fmt.Errorf("consent 后未拿到 code (HTTP %d)", resp.StatusCode)
}

// buildCodexAuthorizeURL 严格按 openai/codex (codex-rs/login/src/server.rs) 的查询参数构造。
func buildCodexAuthorizeURL(st *state, email string) string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", codexOAuthClientID)
	v.Set("redirect_uri", codexOAuthRedirectURI)
	v.Set("scope", codexOAuthScope)
	v.Set("code_challenge", st.pkce.Challenge)
	v.Set("code_challenge_method", "S256")
	// codex 特有的两个开关：
	v.Set("id_token_add_organizations", "true")
	v.Set("codex_cli_simplified_flow", "true")
	v.Set("state", st.stateVal)
	v.Set("originator", codexOriginator)
	if email != "" {
		v.Set("login_hint", email)
	}
	return authBase + "/oauth/authorize?" + v.Encode()
}

// obtainAPIKey 把已经拿到的 id_token 通过 token-exchange 换成 OpenAI api_key。
//
// 这是 Codex CLI 给 CLIProxyAPI / openai-rs SDK 用的关键产物。
//
//	POST https://auth.openai.com/oauth/token
//	Content-Type: application/x-www-form-urlencoded
//	grant_type=urn:ietf:params:oauth:grant-type:token-exchange
//	&client_id=app_EMoamEEZ73f0CkXaXp7hrann
//	&requested_token=openai-api-key
//	&subject_token=<id_token>
//	&subject_token_type=urn:ietf:params:oauth:token-type:id_token
func obtainAPIKey(ctx context.Context, bc *browser.Client, clientID, idToken string) (string, error) {
	if idToken == "" {
		return "", errors.New("id_token 为空，跳过 token-exchange")
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:token-exchange")
	form.Set("client_id", clientID)
	form.Set("requested_token", "openai-api-key")
	form.Set("subject_token", idToken)
	form.Set("subject_token_type", "urn:ietf:params:oauth:token-type:id_token")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		authBase+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := bc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("token-exchange HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var data struct {
		AccessToken string `json:"access_token"` // 注意 codex 这里把 api_key 放在 access_token 字段
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", fmt.Errorf("token-exchange 响应非 JSON: %s", snippet(raw))
	}
	if strings.TrimSpace(data.AccessToken) == "" {
		return "", fmt.Errorf("token-exchange 缺少 access_token: %s", snippet(raw))
	}
	return data.AccessToken, nil
}

// chaseAuthCode 跟随 continue_url → 找到 query 里带 ?code= 的 URL。
//
// 多数账号在 1~2 跳即可到达 redirect_uri；遇到组织/项目选择页时
// 走 workspace/organization/select 兜底（参考实现）。
func chaseAuthCode(ctx context.Context, bc *browser.Client, st *state, consentURL string) (string, error) {
	if strings.HasPrefix(consentURL, "/") {
		consentURL = authBase + consentURL
	}
	cur := consentURL
	for hop := 0; hop < 10 && cur != ""; hop++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cur, nil)
		// consent 链路在 auth.openai.com 内，绝大多数 hop 是 same-origin。
		for k, v := range navHeaders(bc, st, "same-origin") {
			req.Header.Set(k, v)
		}
		// 临时禁止自动跟随，方便每跳读 Location。
		origCheck := bc.HTTP.CheckRedirect
		bc.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := bc.Do(req)
		bc.HTTP.CheckRedirect = origCheck
		if err != nil {
			return "", err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
		_ = resp.Body.Close()

		// 当前 URL 自身可能就是 redirect_uri?code=
		if c := pickCode(cur); c != "" {
			return c, nil
		}
		if resp.Request != nil && resp.Request.URL != nil {
			if c := pickCode(resp.Request.URL.String()); c != "" {
				return c, nil
			}
		}
		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if c := pickCode(loc); c != "" {
			return c, nil
		}
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			break
		}
		if strings.HasPrefix(loc, "/") {
			cur = authBase + loc
		} else {
			cur = loc
		}
	}

	// 兜底：尝试 workspace/select + organization/select（基础租户场景）。
	if c, err := tryConsentSelect(ctx, bc, st, consentURL); err == nil && c != "" {
		return c, nil
	}
	return "", errors.New("整条 continue_url 链路里都没找到 ?code=")
}

// tryConsentSelect 尝试走 workspace/select → organization/select 拿 ?code=。
//
// 参考实现：当 consent 页面要求选择 workspace 时，从 oai-client-auth-session
// cookie 里解出第一个 workspace_id，提交 /api/accounts/workspace/select；
// 如果还要选 org，则继续 /api/accounts/organization/select。
func tryConsentSelect(ctx context.Context, bc *browser.Client, st *state, consentURL string) (string, error) {
	// 找 oai-client-auth-session cookie。
	u, _ := url.Parse(authBase)
	var raw string
	for _, c := range bc.Jar.Cookies(u) {
		if c.Name == "oai-client-auth-session" {
			raw = c.Value
			break
		}
	}
	if raw == "" {
		return "", errors.New("缺少 oai-client-auth-session cookie")
	}
	parts := strings.Split(raw, ".")
	if len(parts) < 1 {
		return "", errors.New("cookie 格式错误")
	}
	first := parts[0]
	if pad := len(first) % 4; pad > 0 {
		first += strings.Repeat("=", 4-pad)
	}
	decoded, err := base64URLDecode(first)
	if err != nil {
		return "", fmt.Errorf("cookie base64: %w", err)
	}
	var payload struct {
		Workspaces []struct {
			ID string `json:"id"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return "", fmt.Errorf("cookie json: %w", err)
	}
	if len(payload.Workspaces) == 0 {
		return "", errors.New("cookie 没有 workspaces")
	}
	wsID := payload.Workspaces[0].ID

	// POST /api/accounts/workspace/select。
	// 参考实现 (zc-zhangchen/any-auto-register oauth_pkce_client.py:445)：
	// 这里 OpenAI 返回 200 + JSON `{continue_url: "..."}`，必须 follow 那个 URL
	// (8 跳)才能在 Location 里捕获 ?code=。早期实现只看 response.Header.Get("Location")
	// 永远拿不到（200 OK 没有 Location），随后 fallback 到 org/select 时也撞 400。
	headers := jsonHeaders(bc, st, consentURL)
	body, _ := json.Marshal(map[string]any{"workspace_id": wsID})
	resp, err := postJSON(ctx, bc, authBase+"/api/accounts/workspace/select", body, headers)
	if err != nil {
		return "", err
	}
	rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return "", fmt.Errorf("workspace/select HTTP %d: %s", resp.StatusCode, snippet(rawResp))
	}
	// 优先读 body.continue_url —— OpenAI 在 SPA 路径下用 200+JSON 而非 302。
	var wsData struct {
		ContinueURL string `json:"continue_url"`
		Method      string `json:"method"`
		Data        struct {
			Orgs []struct {
				ID       string `json:"id"`
				Projects []struct {
					ID string `json:"id"`
				} `json:"projects"`
			} `json:"orgs"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rawResp, &wsData)
	if cu := strings.TrimSpace(wsData.ContinueURL); cu != "" {
		if c, err := chaseToCode(ctx, bc, st, cu, 8); err == nil && c != "" {
			return c, nil
		} else if err != nil && (errors.Is(err, errAddPhoneRequired) || errors.Is(err, errLoginPasswordRequired)) {
			// 把墙 sentinel 透传给上层 walls 循环。
			return "", err
		}
	}
	// 头里也试一下（极少数账号 OpenAI 仍返回 302）。
	if loc := strings.TrimSpace(resp.Header.Get("Location")); loc != "" {
		if c := pickCode(loc); c != "" {
			return c, nil
		}
	}
	if len(wsData.Data.Orgs) == 0 {
		return "", errors.New("workspace/select 之后没有 orgs / continue_url 无 code")
	}
	// 极少账号还需要选 org（继承自旧 SDK），按需走第二步。
	orgID := wsData.Data.Orgs[0].ID
	projectID := ""
	if len(wsData.Data.Orgs[0].Projects) > 0 {
		projectID = wsData.Data.Orgs[0].Projects[0].ID
	}
	orgBody := map[string]any{"org_id": orgID}
	if projectID != "" {
		orgBody["project_id"] = projectID
	}
	orgRaw, _ := json.Marshal(orgBody)
	orgHeaders := jsonHeaders(bc, st, wsData.ContinueURL)
	orgResp, err := postJSON(ctx, bc, authBase+"/api/accounts/organization/select", orgRaw, orgHeaders)
	if err != nil {
		return "", err
	}
	orgRawResp, _ := io.ReadAll(io.LimitReader(orgResp.Body, 64*1024))
	_ = orgResp.Body.Close()
	if orgResp.StatusCode != 200 && orgResp.StatusCode != 201 {
		return "", fmt.Errorf("organization/select HTTP %d: %s", orgResp.StatusCode, snippet(orgRawResp))
	}
	var orgData struct {
		ContinueURL string `json:"continue_url"`
	}
	_ = json.Unmarshal(orgRawResp, &orgData)
	if cu := strings.TrimSpace(orgData.ContinueURL); cu != "" {
		if c, err := chaseToCode(ctx, bc, st, cu, 8); err == nil && c != "" {
			return c, nil
		}
	}
	if loc2 := strings.TrimSpace(orgResp.Header.Get("Location")); loc2 != "" {
		if c := pickCode(loc2); c != "" {
			return c, nil
		}
	}
	return "", errors.New("organization/select 后仍未拿到 code")
}

// chaseToCode 是一个仅 GET + 读 Location/?code= 的轻量 chase：
//
// 区别于 codexFollowAndConsent —— 它不主动 POST consent / workspace/select，
// 仅做 N 跳 redirect follow，捕获 ?code=&state= 即停。
// 用于 workspace/select 等场景的 continue_url follow，避免和上层 chase 形成递归。
func chaseToCode(ctx context.Context, bc *browser.Client, st *state, startURL string, maxHops int) (string, error) {
	if startURL == "" {
		return "", errors.New("chaseToCode: 空 URL")
	}
	cur := startURL
	for hop := 0; hop < maxHops && cur != ""; hop++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cur, nil)
		for k, v := range navHeaders(bc, st, "same-origin") {
			req.Header.Set(k, v)
		}
		origCheck := bc.HTTP.CheckRedirect
		bc.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := bc.Do(req)
		bc.HTTP.CheckRedirect = origCheck
		if err != nil {
			return "", fmt.Errorf("hop %d GET 失败: %w", hop, err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		if c := pickCode(cur); c != "" {
			return c, nil
		}
		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if c := pickCode(loc); c != "" {
			return c, nil
		}
		// 撞墙 sentinel 透传给上层 walls 循环。
		if isAddPhoneURL(cur) || isAddPhoneURL(loc) {
			return "", errAddPhoneRequired
		}
		if isLoginPasswordURL(cur) || isLoginPasswordURL(loc) {
			return "", errLoginPasswordRequired
		}
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			break
		}
		if strings.HasPrefix(loc, "/") {
			cur = authBase + loc
		} else {
			cur = loc
		}
	}
	return "", errors.New("chaseToCode: 链路里没找到 ?code=")
}

// buildPlatformLoginAuthorizeURL 用 platform.openai.com OAuth client 构造 /api/accounts/authorize URL。
//
// 与 platformAuthorize 不同：
//   - 在 freshLogin 阶段拿"已注册 + 重登"用，所以会带新 PKCE/state/nonce。
//   - audience=api.openai.com/v1 → 拿到的 access_token 可直接调 api.openai.com/v1/* (chatgpt2api 用得到)。
//
// 与 codex 流程不同：
//   - 不会触发 phone wall（OpenAI 对 platform OAuth client 不强制要求 phone）。
//   - 不需要 codex 那一套 originator / id_token_add_organizations / simplified_flow 参数。
func buildPlatformLoginAuthorizeURL(st *state, email string) string {
	v := url.Values{}
	v.Set("issuer", authBase)
	v.Set("client_id", platformOAuthClientID)
	v.Set("audience", platformOAuthAudience)
	v.Set("redirect_uri", platformOAuthRedirectURI)
	v.Set("device_id", st.deviceID)
	v.Set("screen_hint", "login_or_signup")
	v.Set("max_age", "0")
	if email != "" {
		v.Set("login_hint", email)
	}
	v.Set("scope", defaultScope)
	v.Set("response_type", "code")
	v.Set("response_mode", "query")
	v.Set("state", st.stateVal)
	v.Set("nonce", st.nonceVal)
	v.Set("code_challenge", st.pkce.Challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("auth0Client", platformAuth0Client)
	return authBase + "/api/accounts/authorize?" + v.Encode()
}

// doTokenExchangePlatform 把 ?code= 用 PKCE verifier 换成 platform OAuth 的 access/refresh/id_token。
//
// platform client 的 access_token 是 api.openai.com/v1 的 audience，可直接当 OpenAI Bearer 用。
// 与 codex 不同：scope 里没 connectors，也不会带 codex_cli_simplified_flow 这类专用参数。
func doTokenExchangePlatform(ctx context.Context, bc *browser.Client, st *state, code string) (*tokenSet, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", platformOAuthRedirectURI)
	form.Set("client_id", platformOAuthClientID)
	form.Set("code_verifier", st.pkce.Verifier)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		authBase+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := bc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("/oauth/token HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("/oauth/token 响应非 JSON: %s", snippet(raw))
	}
	if data.AccessToken == "" {
		return nil, fmt.Errorf("/oauth/token 缺少 access_token: %s", snippet(raw))
	}
	return &tokenSet{
		access:  data.AccessToken,
		refresh: data.RefreshToken,
		idToken: data.IDToken,
	}, nil
}

// doTokenExchangeCodex 把 ?code= 用 PKCE verifier 换成 access/refresh/id_token。
//
// 注意 client_id / redirect_uri 都是 Codex CLI 那一套，不能拿来给 platform.openai.com 用，
// 反之亦然 —— OpenAI 的 OAuth client 是隔离校验的。
func doTokenExchangeCodex(ctx context.Context, bc *browser.Client, st *state, code string) (*tokenSet, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", codexOAuthRedirectURI)
	form.Set("client_id", codexOAuthClientID)
	form.Set("code_verifier", st.pkce.Verifier)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		authBase+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := bc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("/oauth/token HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("/oauth/token 响应非 JSON: %s", snippet(raw))
	}
	if data.AccessToken == "" || data.RefreshToken == "" {
		return nil, fmt.Errorf("/oauth/token 缺少 access_token/refresh_token: %s", snippet(raw))
	}
	return &tokenSet{
		access:  data.AccessToken,
		refresh: data.RefreshToken,
		idToken: data.IDToken,
	}, nil
}

// === SMS（hero-sms）接码处理 add-phone 墙 ===

// solvePhoneChallenge 整体接管"OpenAI 要求绑定手机号"这一步。
//
//	1. SMS Manager 申请号码（复用 phone_pool 中 used_count<max_uses 的号 → 否则向 hero-sms 拿新号）
//	2. POST /api/accounts/add-phone/send 提交手机号
//	3. setStatus=1 提示 hero-sms 准备接码
//	4. 轮询 getStatusV2 等 OTP
//	5. POST /api/accounts/phone-otp/validate 提交 OTP
//	6. 成功 → MarkVerified 并 setStatus=6
//	   失败 → MarkFailed 并 setStatus=8（取消，可能太早被 hero-sms 拒，软错）
//
// accountID 在调用时通常还是 0（号池行还没创建），可在注册成功后通过 MarkVerified
// 异步回填 last_account_id（这里我们简化，直接在拿到 code 后 MarkVerified）。
func (d *Dispatcher) solvePhoneChallenge(ctx context.Context, bc *browser.Client, st *state,
	svc *service.RegisterTaskService, taskID, accountID uint64) error {
	if d.SMSMgr == nil || !d.SMSMgr.IsConfigured(ctx) {
		return errors.New("SMS 未配置（system_config sms.api_key 为空）")
	}

	// SMS 用 OpenAI 同一份 cookie/UA 也行，但 hero-sms 调用走纯网络（不必加 OpenAI cookie），
	// 所以传 nil 让 manager 给 hero-sms 用普通 30s 超时 client。
	//
	// hero-sms NO_NUMBERS 是临时缺号 — 配置里的多国家轮询走完一遍后还是拿不到，
	// 这里再做 maxAttempts 次外层重试，每次间隔逐渐拉长，覆盖瞬时库存空洞。
	const maxAttempts = 4
	var (
		acq         *smspool.AcquireResult
		err         error
		acquireWait = []time.Duration{0, 8 * time.Second, 20 * time.Second, 45 * time.Second}
	)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if w := acquireWait[attempt]; w > 0 {
			svc.LogInfo(ctx, taskID, fmt.Sprintf("hero-sms 缺号，%s 后重试 (%d/%d)", w, attempt+1, maxAttempts))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w):
			}
		}
		acq, err = d.SMSMgr.AcquirePhoneWithCountries(ctx, nil, st.smsCountriesOverride)
		if err == nil {
			break
		}
		// NO_NUMBERS / NO_FREE_PHONES 才值得重试，BAD_KEY / NO_BALANCE 直接放弃。
		msg := err.Error()
		if !(strings.Contains(msg, "NO_NUMBERS") ||
			strings.Contains(msg, "NO_FREE_PHONES") ||
			strings.Contains(msg, "NO_ACTIVATION")) {
			return fmt.Errorf("acquire phone: %w", err)
		}
	}
	if err != nil {
		return fmt.Errorf("acquire phone（已重试 %d 次）: %w", maxAttempts, err)
	}
	if acq == nil || acq.Row == nil || acq.Row.ActivationID == nil {
		return errors.New("acquire phone: SMS Manager 返回空")
	}

	// 整个 phone challenge（领号 → /add-phone/send → setStatus=1 → WaitOTP → validate）
	// 包在一个外层 retry 循环里。任何一步如果是"号本身问题"（OpenAI 拒该号 / 没向该号发 SMS / OTP 超时 等）
	// 就 MarkFailed 该号并换新号继续，最多 maxPhoneTries 次。这样可以自动绕开
	// hero-sms 池里的虚号 + OpenAI 风控段。
	const (
		maxPhoneTries = 6
		otpTimeout    = 120 * time.Second // 单号等 OTP 超时；缩短便于快速换号
	)
	var (
		triedActivations = make(map[string]struct{})
		lastErrSummary   string
	)
	for try := 0; try < maxPhoneTries; try++ {
		if try > 0 {
			// retry 之间强制 sleep 5s，避免连续 burst 调用 OpenAI add-phone/send 触发
			// Cloudflare "Just a moment..." 风控（HTTP 403）。3 次以上的 retry 间隔
			// 加倍到 10s。
			wait := 5 * time.Second
			if try >= 3 {
				wait = 10 * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			acq, err = d.SMSMgr.AcquirePhoneWithCountries(ctx, nil, st.smsCountriesOverride)
			if err != nil {
				return fmt.Errorf("acquire phone (重试 %d): %w", try, err)
			}
			if acq == nil || acq.Row == nil || acq.Row.ActivationID == nil {
				return errors.New("acquire phone: SMS Manager 返回空")
			}
		}
		if _, dup := triedActivations[*acq.Row.ActivationID]; dup {
			_ = acq.Client.SetStatus(ctx, *acq.Row.ActivationID, 8)
			_ = d.SMSMgr.MarkFailed(ctx, acq.Row.ID, "duplicate activation", 1)
			svc.LogWarn(ctx, taskID, fmt.Sprintf("hero-sms 派回了重复 activation=%s，等待并重试", *acq.Row.ActivationID))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(6 * time.Second):
			}
			continue
		}
		triedActivations[*acq.Row.ActivationID] = struct{}{}

		row := acq.Row
		cli := acq.Client
		phoneE164 := smspool.PhoneEntry{Phone: row.Phone}.E164()
		svc.LogInfo(ctx, taskID, fmt.Sprintf("已领取手机号 %s (activation=%s used=%d/%d try=%d/%d)",
			maskPhone(phoneE164), *row.ActivationID, row.UsedCount, row.MaxUses, try+1, maxPhoneTries))

		// 1) /api/accounts/add-phone/send
		if err := sendPhoneNumber(ctx, bc, st, phoneE164); err != nil {
			msg := err.Error()
			// 账号级 rate limit（"too many phone verification requests"等）是死锁状态：
			// 同一账号继续 retry 只会越限越严重，OpenAI 也会冷却 24h。Fatal 直接放弃。
			// 这个号本身不背锅 — Release 而不是 MarkFailed/MarkSoftFailure。
			if isOpenAIPhoneFatal(msg) {
				_ = d.SMSMgr.Release(ctx, row.ID)
				_ = cli.SetStatus(ctx, *row.ActivationID, 8)
				svc.LogWarn(ctx, taskID, fmt.Sprintf("OpenAI 账号级 add-phone/send 限流（%s），放弃 SMS 兜底", shortenErr(msg)))
				return fmt.Errorf("add-phone/send 账号级限流: %s", shortenErr(msg))
			}
			if isOpenAIPhoneRetryable(msg) {
				svc.LogWarn(ctx, taskID, fmt.Sprintf("OpenAI 拒绝该号 %s（add-phone/send: %s），换新号", maskPhone(phoneE164), shortenErr(msg)))
				// MarkSoftFailure：从未成功过的号一次拒就 broken；已经成功过的号容忍 3 次拒
				// （suspicious 偶发，不能让宝贵的"已收过 SMS 的热号"被一次软错吞掉）。
				_ = d.SMSMgr.MarkSoftFailure(ctx, row.ID, msg)
				_ = cli.SetStatus(ctx, *row.ActivationID, 8)
				lastErrSummary = "add-phone/send rejected: " + shortenErr(msg)
				continue
			}
			// 网络 / 5xx 之类不是号本身问题 — 直接抛错。
			_ = d.SMSMgr.MarkFailed(ctx, row.ID, msg, 2)
			_ = cli.SetStatus(ctx, *row.ActivationID, 8)
			return fmt.Errorf("add-phone/send: %w", err)
		}
		svc.LogInfo(ctx, taskID, fmt.Sprintf("已提交手机号 %s 到 OpenAI（/add-phone/send 成功），等 SMS", maskPhone(phoneE164)))

		// 2) setStatus=1：标记号码准备接码（软错，部分号 hero-sms 已经自动 ready）。
		if err := cli.SetStatus(ctx, *row.ActivationID, 1); err != nil {
			svc.LogWarn(ctx, taskID, fmt.Sprintf("hero-sms setStatus=1 异常（已忽略）：%v", err))
		}

		// 3) WaitOTP — 超时也算"号坏"，换新号继续。
		otp, err := d.SMSMgr.WaitOTP(ctx, cli, *row.ActivationID, otpTimeout)
		if err != nil {
			svc.LogWarn(ctx, taskID, fmt.Sprintf("号 %s 未在 %s 内收到 OpenAI SMS（%v），换新号", maskPhone(phoneE164), otpTimeout, err))
			_ = d.SMSMgr.MarkSoftFailure(ctx, row.ID, "wait otp: "+err.Error())
			_ = cli.SetStatus(ctx, *row.ActivationID, 8)
			lastErrSummary = "no SMS received in " + otpTimeout.String()
			continue
		}
		svc.LogInfo(ctx, taskID, fmt.Sprintf("号 %s 收到 SMS OTP", maskPhone(phoneE164)))

		// 4) /api/accounts/phone-otp/validate
		if err := validatePhoneOTP(ctx, bc, st, otp); err != nil {
			msg := err.Error()
			if isOpenAIPhoneRetryable(msg) || strings.Contains(msg, "incorrect_otp") {
				svc.LogWarn(ctx, taskID, fmt.Sprintf("OpenAI 拒绝 OTP 校验（%s）：%s，换新号", maskPhone(phoneE164), shortenErr(msg)))
				_ = d.SMSMgr.MarkSoftFailure(ctx, row.ID, msg)
				_ = cli.SetStatus(ctx, *row.ActivationID, 8)
				lastErrSummary = "phone-otp/validate: " + shortenErr(msg)
				continue
			}
			_ = d.SMSMgr.MarkFailed(ctx, row.ID, msg, 2)
			_ = cli.SetStatus(ctx, *row.ActivationID, 8)
			return fmt.Errorf("phone-otp/validate: %w", err)
		}

		// 5) 成功：setStatus=6 + phone_pool MarkVerified。
		_ = cli.SetStatus(ctx, *row.ActivationID, 6)
		_ = d.SMSMgr.MarkVerified(ctx, row.ID, accountID)
		svc.LogInfo(ctx, taskID, fmt.Sprintf("OpenAI 手机号验证通过（%s），已记 phone_pool 一次成功使用", maskPhone(phoneE164)))
		return nil
	}

	if lastErrSummary == "" {
		lastErrSummary = "all tries exhausted"
	}
	return fmt.Errorf("phone challenge: 连续 %d 个号都未通过验证（最后错误: %s）", maxPhoneTries, lastErrSummary)
}

// isOpenAIPhoneFatal 判断 OpenAI add-phone/send 错误是否是"账号级 rate limit"
// （"You've made too many phone verification requests" 等）。这种错误是账号粒度的死锁
// —— 继续换号 retry 只会撞同一道墙，OpenAI 后端也会因连续触发把账号冷却 24h。
// 一旦撞到必须立刻 abort，让账号 graceful drop（账号还在号池里，之后人工或定时
// 自动登录都可能 ok）而不是把所有号烧光。
func isOpenAIPhoneFatal(msg string) bool {
	// 注意：不要把 "please try again later or contact us" 加进来 ——
	// OpenAI 的 suspicious behavior 软错文案恰好含这句，会把每次"号被风控"
	// 都误判为账号级 fatal，导致 maxPhoneTries 循环一次就退出，丧失所有换号机会。
	keywords := []string{
		"too many phone verification",
		"too many verification",
		"rate_limit_exceeded",
		"phone_verification_rate_limit",
		"phone_verification_limit",
		"verification_limit_exceeded",
		"too many requests",
	}
	low := strings.ToLower(msg)
	for _, k := range keywords {
		if strings.Contains(low, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

// isOpenAIPhoneRetryable 判断 OpenAI add-phone / phone-otp 接口的错误体是否值得换新号重试 —
// 即"号本身问题"（已被人占用 / 无效号 / VOIP / 风控段标黑 / blocked / 不支持运营商 等）；
// 或者"短时间频次太高被 Cloudflare/OpenAI 节流"，sleep 一下再换号也可恢复。
//
// 网络 5xx / TLS 错误等不算 — 应直接 fail 让上层 task 重试整个流程。
func isOpenAIPhoneRetryable(msg string) bool {
	keywords := []string{
		"phone_number_in_use",
		"invalid_phone_number",
		"unsupported_phone_number",
		"voip_phone_number",
		"phone_number_blocked",
		"phone_blocked",
		"suspicious behavior", // OpenAI 风控话术
		"unable to send",
		"could not send",
		"sms_failed",
		"too many attempts",
		"HTTP 403", // Cloudflare 'Just a moment...' challenge
		"HTTP 429", // 频次限制
		"Just a moment",
		"cloudflare",
	}
	for _, k := range keywords {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

// shortenErr 截短长 HTTP body 便于日志展示。
func shortenErr(msg string) string {
	const max = 160
	msg = strings.ReplaceAll(strings.ReplaceAll(msg, "\n", " "), "  ", " ")
	if len(msg) <= max {
		return msg
	}
	return msg[:max] + "…"
}

// sendPhoneNumber POST /api/accounts/add-phone/send。
func sendPhoneNumber(ctx context.Context, bc *browser.Client, st *state, phone string) error {
	body, _ := json.Marshal(map[string]any{"phone_number": phone})
	headers := jsonHeaders(bc, st, authBase+"/add-phone")
	resp, err := postJSON(ctx, bc, authBase+"/api/accounts/add-phone/send", body, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	return nil
}

// validatePhoneOTP POST /api/accounts/phone-otp/validate。
func validatePhoneOTP(ctx context.Context, bc *browser.Client, st *state, otp string) error {
	body, _ := json.Marshal(map[string]any{"code": otp})
	headers := jsonHeaders(bc, st, authBase+"/phone-verification")
	resp, err := postJSON(ctx, bc, authBase+"/api/accounts/phone-otp/validate", body, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if resp.StatusCode == 401 {
		return errors.New("hero-sms 给的 OTP 被 OpenAI 拒绝（可能号码已被滥用）")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	return nil
}

// maskPhone 把 + 86 1234567 -> +86 12***67 这样，方便日志安全。
// maskPhone 把手机号脱敏后保留首尾各 6 位 / 2 位用于日志统计前缀分布（观察哪段号能收 OpenAI SMS）。
func maskPhone(p string) string {
	if len(p) <= 8 {
		return p
	}
	headLen := 6
	tailLen := 2
	if len(p) < headLen+tailLen+1 {
		return p
	}
	return p[:headLen] + strings.Repeat("*", len(p)-headLen-tailLen) + p[len(p)-tailLen:]
}

// parseSMSCountriesPayload 把 payload.sms_country 字符串拆成 []int。
//
// 支持 "16" / "16,73,4,6" / "16; 4 73" 等分隔形式；非法元素静默忽略。
// 返回 nil 表示未指定，沿用 system_config 全局 sms.country。
func parseSMSCountriesPayload(raw string) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]int, 0, len(parts))
	seen := make(map[int]struct{}, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// === 登录墙（/log-in/password）处理 ===

// solveLoginWall 完整模拟"邮箱 → 密码 → 邮件 OTP"三段式登录，
// 用以攻克 codex authorize 时偶发的 /log-in/password 二次验证墙。
//
// 调用方：Run() 在 codexAuthorizeAndExchange 抛 errLoginPasswordRequired 时
// 会调本方法。完成后再次发起 codexAuthorizeAndExchange，新的 oai-sc cookie
// 已是"已通过 MFA"状态，直接拿到 ?code=。
//
// OpenAI 安全策略：注册成功 → 切换 client_id 时，oai-client-auth-session
// 会被新 client 的 PKCE 重置成"未登录"，且必须再走一遍邮箱 OTP MFA，
// 即使账号刚刚才在同一会话注册。
func (d *Dispatcher) solveLoginWall(ctx context.Context, bc *browser.Client, st *state,
	mb mailbox.Mailbox, svc *service.RegisterTaskService, taskID uint64) error {
	if st.email == "" || st.password == "" {
		return errors.New("solveLoginWall: state.email / state.password 为空")
	}
	if mb == nil {
		return errors.New("solveLoginWall: mailbox 为空")
	}

	// 1) authorize/continue（screen_hint=login，提交注册时的 email）。
	if err := postLoginContinue(ctx, bc, st, st.email); err != nil {
		return fmt.Errorf("authorize/continue(login): %w", err)
	}
	svc.LogInfo(ctx, taskID, fmt.Sprintf("login wall：已提交邮箱 %s（screen_hint=login）", st.email))

	// 2) password/verify（提交注册时的密码）。
	//    OpenAI 在密码通过后给三种页面：邮箱 OTP / 手机绑定 / 异常。
	//    手机墙路径下直接把 errAddPhoneRequired 透传给上层 Run() 循环。
	otpSent := time.Now()
	needsEmailOTP, continueURL, err := postPasswordVerify(ctx, bc, st, st.password)
	if err != nil {
		if errors.Is(err, errAddPhoneRequired) {
			svc.LogInfo(ctx, taskID, "login wall：密码已通过，但 OpenAI 要求 /add-phone，转 SMS 流程")
			return errAddPhoneRequired
		}
		return fmt.Errorf("password/verify: %w", err)
	}
	if !needsEmailOTP {
		svc.LogInfo(ctx, taskID, "login wall：密码已通过，无需邮箱 OTP")
		// continue_url 必须 follow 一次才能让 cookie 升级到"已登录"。
		// 失败不是致命的——上层重新发起 codex authorize 时如果还是被送回墙会再次循环。
		if continueURL != "" {
			if err := followContinueURL(ctx, bc, st, continueURL); err != nil {
				svc.LogWarn(ctx, taskID, fmt.Sprintf("login wall：follow continue_url 失败：%v（继续 codex authorize）", err))
			} else {
				svc.LogInfo(ctx, taskID, "login wall：已 follow password/verify continue_url，准备重新发起 codex authorize")
			}
		}
		return nil
	}
	svc.LogInfo(ctx, taskID, "login wall：密码已通过，等待邮箱 OTP")

	// 3) 等邮箱 OTP（最多 240s；超时尝试一次手动重发）。
	otp, err := mb.WaitCode(ctx, mailbox.WaitOptions{
		Provider: mailbox.ProviderGPT,
		SinceTS:  otpSent.Add(-30 * time.Second),
		Timeout:  240 * time.Second,
	})
	if err != nil {
		svc.LogWarn(ctx, taskID, fmt.Sprintf("login wall：第一次等码失败：%v，触发手动重发", err))
		retryAt := time.Now()
		if rerr := sendEmailOTP(ctx, bc, st); rerr != nil {
			return fmt.Errorf("login wall 重发 OTP: %w", rerr)
		}
		otp, err = mb.WaitCode(ctx, mailbox.WaitOptions{
			Provider: mailbox.ProviderGPT,
			SinceTS:  retryAt.Add(-30 * time.Second),
			Timeout:  180 * time.Second,
		})
		if err != nil {
			return fmt.Errorf("login wall 等码失败（已重发）: %w", err)
		}
	}
	svc.LogInfo(ctx, taskID, fmt.Sprintf("login wall：已收到二次 OTP %s", dispatcher.MaskOTP(otp)))

	// 4) email-otp/validate（与注册阶段同一 endpoint，复用既有实现）。
	if err := validateEmailOTP(ctx, bc, st, otp); err != nil {
		return fmt.Errorf("login wall email-otp/validate: %w", err)
	}
	svc.LogInfo(ctx, taskID, "login wall：MFA 完成，准备重新发起 codex authorize")
	return nil
}

// followContinueURL 在 password/verify 直接返回 continue_url 的场景里把这条
// "登录完成的尾巴" follow 完，把抓到的 ?code= 缓存到 st.codexAuthCode。
//
// 流程参考 zc-zhangchen/any-auto-register oauth_pkce_client.py：
//  1. continue_url 通常是 https://auth.openai.com/sign-in-with-chatgpt/codex/consent —
//     这是个 SPA HTML 页，GET 它不会自动 302 到 ?code=。
//  2. 必须先 POST /api/accounts/workspace/select（用 oai-client-auth-session cookie 里
//     解出来的 workspace_id）—— OpenAI 才会返回真正能拿 code 的 continue_url。
//  3. 拿新 continue_url 跟跳 8 次，Location 里就有 ?code=&state=。
//
// 行为：
//   - 优先 tryConsentSelect(consentURL) —— 走 workspace/select → continue_url 链路。
//   - 如果链路中又撞墙（add-phone / log-in），把 sentinel error 透传给上层 walls 循环。
//   - 兜底 chaseToCode(continueURL, 8)：直接 GET continue_url 跟跳。
//   - 全部失败时返回 nil（不致命，让上层重发 codex authorize）。
func followContinueURL(ctx context.Context, bc *browser.Client, st *state, continueURL string) error {
	if continueURL == "" {
		return nil
	}
	if c, err := tryConsentSelect(ctx, bc, st, continueURL); err == nil && c != "" {
		st.codexAuthCode = c
		return nil
	} else if err != nil && (errors.Is(err, errAddPhoneRequired) || errors.Is(err, errLoginPasswordRequired)) {
		return err
	}
	// 兜底：直接 follow continue_url 跟跳。
	if c, err := chaseToCode(ctx, bc, st, continueURL, 8); err == nil && c != "" {
		st.codexAuthCode = c
		return nil
	} else if err != nil && (errors.Is(err, errAddPhoneRequired) || errors.Is(err, errLoginPasswordRequired)) {
		return err
	}
	return nil
}

// freshCodexLogin 在注册流程完成后，按 OpenAI Hydra OAuth 协议重新走一次完整登录，
// 拿到含 workspaces[] 的 oai-client-auth-session cookie，然后 workspace/select →
// chase ?code= → /oauth/token 拿 access/refresh/id_token。
//
// 这是参考实现 zc-zhangchen/any-auto-register oauth_pkce_client.py step 9
// (login_after_register) 的 Go 移植。OpenAI 在 2025 末把"注册阶段 cookie 直接拿
// codex token"的短路砍了 —— 必须新建一组 PKCE/state 并重新走 login OAuth flow，
// OpenAI 才会发新的含 workspaces[] 的 cookie。
//
// 步骤：
//  1. 新 PKCE / state / nonce  —— token exchange 必须用新 verifier
//  2. 清 OAuth session cookies（保留 oai-did，避免触发"换设备"风控）
//  3. 重建 sentinel（用同一 oai-did）
//  4. GET codex authorize URL —— 让 OpenAI 建立新 login session
//  5. POST authorize/continue (screen_hint=login)
//  6. POST password/verify  —— 循环最多 maxWalls 次：
//     - 撞 add_phone 墙   → 调 d.solvePhoneChallenge → 重 POST password/verify
//     - 撞 email_otp 墙   → 复用 regOTP 失败再 mailbox WaitCode → email-otp/validate
//     - page.type=consent / sign_in_with_* / success → 拿 continue_url 跳出循环
//  7. 拿到 continue_url 后：tryConsentSelect (workspace/select) → ?code=
//  8. 兜底 chaseToCode(continue_url) → ?code=
//  9. /oauth/token (用 step 1 的新 PKCE verifier)
//
// regOTP 是注册阶段已经成功验证过的 email OTP；OpenAI 在短期内允许同一 OTP 二次使用，
// 优先用它跳过等待。空字符串就跳过复用直接 wait 新码。

// platformLoginAndExchange —— 严格对齐 basketikun/chatgpt2api 的 _login_and_exchange_tokens：
//
//	1. 新 PKCE / state / nonce
//	2. GET /api/accounts/authorize?client_id=platform&screen_hint=login_or_signup&max_age=0&login_hint=email
//	   (allow_redirects=True，复用注册阶段的 oai-did + login_session cookies；
//	    OpenAI 看到"已 cookie 登录但 max_age=0"，会引导到 password 验证页)
//	3. POST /api/accounts/password/verify (sentinel flow=password_verify, allow_redirects=False)
//	   → response.continue_url + page.type
//	4. 如果 page.type=email_otp_verification 或 continue_url 含 email-verification/email-otp
//	   → wait_for_code(mailbox) → POST /api/accounts/email-otp/validate
//	   → response.continue_url 覆盖
//	5. extract_oauth_callback_params_from_consent_session(continue_url) →
//	   GET continue_url 一路 hop，看 ?code=；没拿到从 cookie 里解 workspaces[0].id
//	   → POST /api/accounts/workspace/select 看 Location ?code=；
//	   还没拿到从 body orgs[0].id + projects[0].id → POST /api/accounts/organization/select
//	6. POST /oauth/token (platform client_id + new code_verifier)
//
// 关键差异 vs codex 流程：
//   - **不清** OAuth session cookies（注册阶段的 oai-client-auth-session 必须保留，
//     否则 OpenAI 不认账，会要求重新走 username/password create_account）；
//   - **不重建** sentinel（用同一 generator，oai-did 不变）；
//   - **不** POST authorize/continue（platform client 没这一步，直接 GET authorize 让 OpenAI
//     根据 cookie 自动判断状态）；
//   - 不会触发 add_phone 墙（OpenAI 对 platform client 不强制要求 phone）；
//   - 拿到的 access_token 直接是 api.openai.com/v1 的 Bearer，可调 /v1/models 等 API。
//
// regOTP 是注册阶段成功的 email OTP；OpenAI 短期内允许复用，可省一次 mailbox.WaitCode。
func (d *Dispatcher) platformLoginAndExchange(ctx context.Context, bc *browser.Client, st *state,
	mb mailbox.Mailbox, svc *service.RegisterTaskService, taskID uint64,
	regOTP string) (*tokenSet, error) {

	if mb == nil {
		return nil, errors.New("platformLogin: mailbox 为空")
	}
	if st.email == "" || st.password == "" {
		return nil, errors.New("platformLogin: 缺少 email/password（state 未初始化）")
	}

	// step 1: 新 PKCE / state / nonce。注意不清 cookies、不重建 sentinel。
	st.pkce = NewPKCE()
	st.stateVal = base64URL(randomBytes(32))
	st.nonceVal = base64URL(randomBytes(32))

	// step 2: GET /api/accounts/authorize 让 OpenAI 把会话推进到 /log-in/password。
	authURL := buildPlatformLoginAuthorizeURL(st, st.email)
	if err := primeAuthorize(ctx, bc, st, authURL); err != nil {
		return nil, fmt.Errorf("platform authorize: %w", err)
	}
	svc.LogInfo(ctx, taskID, "platformLogin: GET authorize 完成（复用注册阶段 cookie）")

	// step 3: POST /api/accounts/password/verify。
	// invalid_state（HTTP 409）是 OpenAI 偶发的 session race —— 通常是 oai-state cookie 还没
	// commit 就被 POST 了。重试 1 次：重新 GET authorize（让 OpenAI 重新 set 新 state cookie）
	// 然后 POST password/verify。
	const maxStateRetry = 2
	var needsEmailOTP bool
	var continueURL string
	var err error
	for attempt := 1; attempt <= maxStateRetry; attempt++ {
		needsEmailOTP, continueURL, err = postPasswordVerify(ctx, bc, st, st.password)
		if err == nil {
			break
		}
		// platform client 不应触发 add_phone；如果真撞了，说明 OpenAI 临时升级了风控。
		// 直接返回错误，不调 SMS（避免拉 hero-sms 配额 + 引入新失败模式）。
		if errors.Is(err, errAddPhoneRequired) {
			return nil, fmt.Errorf("platform 撞 add_phone 墙（OpenAI 风控升级，账号可能已被标记）: %w", err)
		}
		retryable := strings.Contains(err.Error(), "invalid_state") ||
			strings.Contains(err.Error(), "Invalid session") ||
			strings.Contains(err.Error(), "HTTP 409")
		if !retryable || attempt == maxStateRetry {
			return nil, fmt.Errorf("password/verify: %w", err)
		}
		svc.LogWarn(ctx, taskID, fmt.Sprintf("platformLogin: password/verify HTTP 409 invalid_state（第 %d 次），清 OAuth session cookies 重 GET authorize", attempt))
		time.Sleep(800 * time.Millisecond)
		// invalid_state 是 OpenAI 端有 stale OAuth session cookies（注册阶段的 oai-state /
		// login_session）跟新 PKCE/state 不匹配。**必须清掉**这些 cookies，让 OpenAI
		// 走"全新 OAuth session"分支，重新 set 干净的 oai-state。
		clearOAuthSessionCookies(bc.Jar)
		st.pkce = NewPKCE()
		st.stateVal = base64URL(randomBytes(32))
		st.nonceVal = base64URL(randomBytes(32))
		retryURL := buildPlatformLoginAuthorizeURL(st, st.email)
		if perr := primeAuthorize(ctx, bc, st, retryURL); perr != nil {
			return nil, fmt.Errorf("retry GET authorize: %w", perr)
		}
	}
	svc.LogInfo(ctx, taskID, fmt.Sprintf("platformLogin: password/verify 通过（needsOTP=%v continue_url_len=%d）", needsEmailOTP, len(continueURL)))

	// step 4: 处理 email OTP 二次校验（如果 OpenAI 要求）。
	if needsEmailOTP {
		svc.LogInfo(ctx, taskID, "platformLogin: 撞 email_otp，获取 OTP")
		otp, err := obtainLoginOTP(ctx, bc, st, mb, regOTP, true)
		if err != nil {
			return nil, fmt.Errorf("email OTP: %w", err)
		}
		if err := validateEmailOTP(ctx, bc, st, otp); err != nil {
			// regOTP 复用失败时拿新码再试一次。
			if regOTP != "" && (strings.Contains(err.Error(), "incorrect") || strings.Contains(err.Error(), "expired")) {
				svc.LogWarn(ctx, taskID, "platformLogin: regOTP 复用被拒，等待新 OTP")
				retryOTP, rerr := obtainLoginOTP(ctx, bc, st, mb, "", true)
				if rerr != nil {
					return nil, fmt.Errorf("email OTP retry: %w", rerr)
				}
				if err2 := validateEmailOTP(ctx, bc, st, retryOTP); err2 != nil {
					return nil, fmt.Errorf("email-otp/validate (retry): %w", err2)
				}
			} else {
				return nil, fmt.Errorf("email-otp/validate: %w", err)
			}
		}
		svc.LogInfo(ctx, taskID, "platformLogin: 二次邮箱 OTP 验证通过")
	}

	// step 5: 兜底 continue_url。ref 在拿不到 continue_url 时硬编 codex/consent 路径，
	// 这里跟着写——OpenAI 内部对所有 OAuth client 的 consent 都通过这个 SPA。
	if continueURL == "" {
		continueURL = authBase + "/sign-in-with-chatgpt/codex/consent"
	}

	// step 6: extract_oauth_callback_params_from_consent_session
	// → workspace/select / organization/select / chaseToCode 拿 ?code=。
	code, err := extractCodeFromConsentSession(ctx, bc, st, continueURL)
	if err != nil {
		return nil, fmt.Errorf("consent session 拿 ?code=: %w", err)
	}
	if code == "" {
		return nil, errors.New("platformLogin: 整条链路都没拿到 ?code=")
	}

	// step 7: /oauth/token 兑换 access/refresh/id_token。
	return doTokenExchangePlatform(ctx, bc, st, code)
}

// extractCodeFromConsentSession 严格对齐 ref `extract_oauth_callback_params_from_consent_session`：
//
//	1. GET continue_url，allow_redirects=False，最多 hop 10 次找 ?code=
//	2. 没找到 → 从 cookie oai-client-auth-session 的 base64.payload 里解 workspaces[0].id
//	3. POST /api/accounts/workspace/select {"workspace_id":...} → response.headers.Location 找 ?code=
//	4. 没找到 → 从 body orgs[0].id + orgs[0].projects[0].id
//	5. POST /api/accounts/organization/select {"org_id":..., "project_id":...} → Location 找 ?code=
func extractCodeFromConsentSession(ctx context.Context, bc *browser.Client, st *state, consentURL string) (string, error) {
	// step 1: GET consent_url 一路跟 redirect（最多 10 hop）。
	if c, _ := chaseToCode(ctx, bc, st, consentURL, 10); c != "" {
		return c, nil
	}

	// step 2: 从 oai-client-auth-session cookie 解 workspace_id。
	wsID := pickWorkspaceID(bc.Jar)
	if wsID == "" {
		// 如果 cookie 里没 workspaces[]（可能是 codex/consent 兜底情况），
		// 尝试 organization/select 也没意义 —— 直接报错。
		return "", errors.New("oai-client-auth-session cookie 里没找到 workspaces[]")
	}

	// step 3: POST /api/accounts/workspace/select。
	wsBody, _ := json.Marshal(map[string]any{"workspace_id": wsID})
	wsHeaders := jsonHeaders(bc, st, consentURL)
	wsResp, err := postNoFollow(ctx, bc, authBase+"/api/accounts/workspace/select", wsBody, wsHeaders)
	if err != nil {
		return "", fmt.Errorf("workspace/select: %w", err)
	}
	defer wsResp.Body.Close()
	wsRaw, _ := io.ReadAll(io.LimitReader(wsResp.Body, 64*1024))
	if c := pickCode(strings.TrimSpace(wsResp.Header.Get("Location"))); c != "" {
		return c, nil
	}
	// step 4: 从 body 里取 orgs[0].id + projects[0].id。
	var wsOut struct {
		ContinueURL string `json:"continue_url"`
		Data        struct {
			Orgs []struct {
				ID       string `json:"id"`
				Projects []struct {
					ID string `json:"id"`
				} `json:"projects"`
			} `json:"orgs"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wsRaw, &wsOut)
	// 如果 body 里有 continue_url，先尝试 chase 一下。
	if wsOut.ContinueURL != "" {
		if c, _ := chaseToCode(ctx, bc, st, wsOut.ContinueURL, 10); c != "" {
			return c, nil
		}
	}
	if len(wsOut.Data.Orgs) == 0 {
		return "", fmt.Errorf("workspace/select 后既没 ?code= 也没 orgs[]，body=%s", snippet(wsRaw))
	}
	orgID := strings.TrimSpace(wsOut.Data.Orgs[0].ID)
	if orgID == "" {
		return "", errors.New("workspace/select.orgs[0].id 为空")
	}
	projID := ""
	if len(wsOut.Data.Orgs[0].Projects) > 0 {
		projID = strings.TrimSpace(wsOut.Data.Orgs[0].Projects[0].ID)
	}

	// step 5: POST /api/accounts/organization/select。
	orgBody := map[string]any{"org_id": orgID}
	if projID != "" {
		orgBody["project_id"] = projID
	}
	orgRaw, _ := json.Marshal(orgBody)
	orgHeaders := jsonHeaders(bc, st, consentURL)
	if wsOut.ContinueURL != "" {
		orgHeaders["referer"] = wsOut.ContinueURL
	}
	orgResp, err := postNoFollow(ctx, bc, authBase+"/api/accounts/organization/select", orgRaw, orgHeaders)
	if err != nil {
		return "", fmt.Errorf("organization/select: %w", err)
	}
	defer orgResp.Body.Close()
	if c := pickCode(strings.TrimSpace(orgResp.Header.Get("Location"))); c != "" {
		return c, nil
	}
	orgBodyRaw, _ := io.ReadAll(io.LimitReader(orgResp.Body, 64*1024))
	var orgOut struct {
		ContinueURL string `json:"continue_url"`
	}
	_ = json.Unmarshal(orgBodyRaw, &orgOut)
	if orgOut.ContinueURL != "" {
		if c, _ := chaseToCode(ctx, bc, st, orgOut.ContinueURL, 10); c != "" {
			return c, nil
		}
	}
	return "", fmt.Errorf("organization/select 也没 ?code=, status=%d body=%s", orgResp.StatusCode, snippet(orgBodyRaw))
}

// postNoFollow 是 postJSON 的"不跟 redirect"版，专门用来抓 302 Location 里的 ?code=。
func postNoFollow(ctx context.Context, bc *browser.Client, urlStr string, body []byte, headers map[string]string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	origCheck := bc.HTTP.CheckRedirect
	bc.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { bc.HTTP.CheckRedirect = origCheck }()
	return bc.Do(req)
}

// pickWorkspaceID 从 cookie jar 里找 oai-client-auth-session，
// 取它第一段 base64URL 解码后的 JSON.workspaces[0].id。
//
// ref 实现：raw.split(".")[0] → urlsafe_b64decode → json.workspaces[0].id
func pickWorkspaceID(jar *cookiejar.Jar) string {
	for _, host := range []string{"https://auth.openai.com", "https://.auth.openai.com"} {
		u, _ := url.Parse(host)
		for _, c := range jar.Cookies(u) {
			if c.Name != "oai-client-auth-session" {
				continue
			}
			parts := strings.SplitN(c.Value, ".", 2)
			if len(parts) == 0 || parts[0] == "" {
				continue
			}
			decoded, err := base64URLDecode(parts[0])
			if err != nil {
				continue
			}
			var payload struct {
				Workspaces []struct {
					ID string `json:"id"`
				} `json:"workspaces"`
			}
			if err := json.Unmarshal(decoded, &payload); err != nil {
				continue
			}
			if len(payload.Workspaces) > 0 && payload.Workspaces[0].ID != "" {
				return payload.Workspaces[0].ID
			}
		}
	}
	return ""
}

func (d *Dispatcher) freshCodexLogin(ctx context.Context, bc *browser.Client, st *state,
	mb mailbox.Mailbox, svc *service.RegisterTaskService, taskID, accountID uint64,
	regOTP string) (*tokenSet, error) {

	if mb == nil {
		return nil, errors.New("freshCodexLogin: mailbox 为空")
	}
	if st.email == "" || st.password == "" {
		return nil, errors.New("freshCodexLogin: 缺少 email/password（state 未初始化）")
	}

	// step 1: 新 PKCE / state / nonce —— token exchange 必须用新 verifier。
	st.pkce = NewPKCE()
	st.stateVal = base64URL(randomBytes(32))
	st.nonceVal = base64URL(randomBytes(32))
	st.codexAuthCode = ""
	st.codexFlowInit = true

	// step 2: 清 OAuth session cookies（保留 oai-did）。
	clearOAuthSessionCookies(bc.Jar)

	// step 3: 重建 sentinel（用同一 oai-did，避免 OpenAI 视为换设备）。
	st.sentinel = NewSentinelGenerator(st.deviceID, bc.Profile.UserAgent)
	setOaiDID(bc.Jar, st.deviceID)

	// step 4: GET codex authorize URL，让 OpenAI 建立新 login session。
	authURL := buildCodexAuthorizeURL(st, st.email)
	if err := primeAuthorize(ctx, bc, st, authURL); err != nil {
		return nil, fmt.Errorf("prime authorize: %w", err)
	}
	svc.LogInfo(ctx, taskID, "freshLogin: 新 OAuth session 已建立（新 PKCE/sentinel）")

	// step 5: POST authorize/continue (screen_hint=login)。
	if err := postLoginContinue(ctx, bc, st, st.email); err != nil {
		return nil, fmt.Errorf("authorize/continue(login): %w", err)
	}

	// step 6: POST password/verify 循环。
	const maxWalls = 5
	var continueURL string
	otpUsed := false // regOTP 只复用一次
	for wall := 0; wall < maxWalls; wall++ {
		needsEmailOTP, contURL, err := postPasswordVerify(ctx, bc, st, st.password)
		if err != nil {
			if errors.Is(err, errAddPhoneRequired) {
				svc.LogInfo(ctx, taskID, fmt.Sprintf("freshLogin: 撞 add_phone 墙（第 %d 次），启动 SMS", wall+1))
				if perr := d.solvePhoneChallenge(ctx, bc, st, svc, taskID, accountID); perr != nil {
					return nil, fmt.Errorf("login wall SMS: %w", perr)
				}
				continue
			}
			return nil, fmt.Errorf("password/verify: %w", err)
		}
		if needsEmailOTP {
			svc.LogInfo(ctx, taskID, fmt.Sprintf("freshLogin: 撞 email_otp 墙（第 %d 次），获取 OTP 中", wall+1))
			otp, err := obtainLoginOTP(ctx, bc, st, mb, regOTP, !otpUsed)
			otpUsed = true
			if err != nil {
				return nil, fmt.Errorf("email OTP: %w", err)
			}
			if err := validateEmailOTP(ctx, bc, st, otp); err != nil {
				// 复用 regOTP 失败时的常见错误是 incorrect_otp，给一次 wait 新码的机会
				if regOTP != "" && strings.Contains(err.Error(), "incorrect") {
					svc.LogWarn(ctx, taskID, "freshLogin: regOTP 复用被拒，等待新 OTP")
					retryOTP, rerr := obtainLoginOTP(ctx, bc, st, mb, "", true)
					if rerr != nil {
						return nil, fmt.Errorf("email OTP retry: %w", rerr)
					}
					if err2 := validateEmailOTP(ctx, bc, st, retryOTP); err2 != nil {
						return nil, fmt.Errorf("email-otp/validate (retry): %w", err2)
					}
				} else {
					return nil, fmt.Errorf("email-otp/validate: %w", err)
				}
			}
			svc.LogInfo(ctx, taskID, "freshLogin: 二次邮箱 OTP 验证通过")
			// OTP 验证后通常会 redirect 到 consent 链 —— 重新 GET authorize 跟跳到 ?code=。
			c, _, cerr := codexFollowAndConsent(ctx, bc, st, buildCodexAuthorizeURL(st, st.email))
			if cerr != nil {
				if errors.Is(cerr, errAddPhoneRequired) || errors.Is(cerr, errLoginPasswordRequired) {
					// 还有墙，继续下一轮 password/verify
					if errors.Is(cerr, errAddPhoneRequired) {
						svc.LogInfo(ctx, taskID, "freshLogin: OTP 后还撞 add_phone，转 SMS")
						if perr := d.solvePhoneChallenge(ctx, bc, st, svc, taskID, accountID); perr != nil {
							return nil, fmt.Errorf("post-OTP SMS: %w", perr)
						}
					}
					continue
				}
				return nil, fmt.Errorf("post-OTP chase: %w", cerr)
			}
			return doTokenExchangeCodex(ctx, bc, st, c)
		}
		// page.type=consent / sign_in_with_* / success_redirect / success — 拿 continue_url
		continueURL = contURL
		svc.LogInfo(ctx, taskID, "freshLogin: 密码已通过且无需 MFA，准备 workspace/select")
		break
	}

	// step 7-8: 拿 ?code=
	var code string
	if continueURL != "" {
		c, err := tryConsentSelect(ctx, bc, st, continueURL)
		if err == nil && c != "" {
			code = c
		} else if err != nil && (errors.Is(err, errAddPhoneRequired) || errors.Is(err, errLoginPasswordRequired)) {
			return nil, fmt.Errorf("workspace/select 撞墙: %w", err)
		}
		if code == "" {
			c2, err2 := chaseToCode(ctx, bc, st, continueURL, 8)
			if err2 != nil {
				return nil, fmt.Errorf("workspace/select 与 chase continue_url 都失败 (ws=%v chase=%v)", err, err2)
			}
			code = c2
		}
	} else {
		// password 直接通过、无 continue_url（少见）—— re-authorize 兜底。
		c, _, err := codexFollowAndConsent(ctx, bc, st, buildCodexAuthorizeURL(st, st.email))
		if err != nil {
			return nil, fmt.Errorf("re-authorize chase: %w", err)
		}
		code = c
	}
	if code == "" {
		return nil, errors.New("freshLogin: 整条链路都没拿到 ?code=")
	}

	// step 9: /oauth/token —— 用新 PKCE verifier 兑换 access/refresh/id_token。
	return doTokenExchangeCodex(ctx, bc, st, code)
}

// obtainLoginOTP 在 freshCodexLogin 二次登录的 email OTP 阶段获取验证码。
//
// 策略：
//   - reuseOTP != "" 且 allowReuse=true → 直接返回它（复用注册阶段的 OTP）
//   - 否则：发一次 send_otp（失败可忽略），mailbox.WaitCode 240s
//
// allowReuse=false 时强制等待新 OTP（用于 reuse 失败后的 retry）。
func obtainLoginOTP(ctx context.Context, bc *browser.Client, st *state, mb mailbox.Mailbox,
	reuseOTP string, allowReuse bool) (string, error) {
	if allowReuse && reuseOTP != "" {
		return reuseOTP, nil
	}
	otpSent := time.Now()
	if err := sendEmailOTP(ctx, bc, st); err != nil {
		// send_otp 失败不致命（OpenAI 偶尔已自动发码），继续等待。
		_ = err
	}
	otp, err := mb.WaitCode(ctx, mailbox.WaitOptions{
		Provider: mailbox.ProviderGPT,
		SinceTS:  otpSent.Add(-30 * time.Second),
		Timeout:  240 * time.Second,
	})
	if err != nil {
		// 重发兜底
		retryAt := time.Now()
		if err2 := sendEmailOTP(ctx, bc, st); err2 != nil {
			return "", fmt.Errorf("re-send OTP: %w", err2)
		}
		otp, err = mb.WaitCode(ctx, mailbox.WaitOptions{
			Provider: mailbox.ProviderGPT,
			SinceTS:  retryAt.Add(-30 * time.Second),
			Timeout:  180 * time.Second,
		})
		if err != nil {
			return "", err
		}
	}
	return otp, nil
}

// postLoginContinue 提交登录场景下的邮箱：
//
//	POST https://auth.openai.com/api/accounts/authorize/continue
//	Content-Type: application/json
//	openai-sentinel-token: <flow=authorize_continue>
//	{"username":{"value":"<email>","kind":"email"},"screen_hint":"login"}
//
// 与注册阶段唯一区别是 screen_hint 从 "login_or_signup" 改成 "login"。
// 成功响应 page.type 为 "login_password"。
func postLoginContinue(ctx context.Context, bc *browser.Client, st *state, email string) error {
	tok, err := st.sentinel.SentinelToken(ctx, bc.HTTP, "authorize_continue")
	if err != nil {
		return fmt.Errorf("sentinel(authorize_continue): %w", err)
	}
	body, _ := json.Marshal(map[string]any{
		"username": map[string]any{
			"value": email,
			"kind":  "email",
		},
		"screen_hint": "login",
	})
	headers := jsonHeaders(bc, st, authBase+"/log-in")
	headers["openai-sentinel-token"] = tok

	resp, err := postJSON(ctx, bc, authBase+"/api/accounts/authorize/continue", body, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	// 健壮性校验：page.type 应为 login_password。失败时把 OpenAI 实际返回带回。
	var out struct {
		Page struct {
			Type string `json:"type"`
		} `json:"page"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Page.Type != "" && out.Page.Type != "login_password" {
		return fmt.Errorf("unexpected page.type=%s body=%s", out.Page.Type, snippet(raw))
	}
	return nil
}

// postPasswordVerify 提交登录密码：
//
//	POST https://auth.openai.com/api/accounts/password/verify
//	Content-Type: application/json
//	{"password":"<password>"}
//
// OpenAI 根据风控 / 账号状态会回多种 page.type：
//   - email_otp_verification / otp_verification        → 需要走邮箱 OTP MFA
//   - add_phone / phone_verification / phone_otp       → 强制要求绑定手机号 ⇒ 抛 errAddPhoneRequired
//   - sign_in_with_chatgpt_*_consent / consent / success / success_redirect
//                                                      → 密码通过、无需 OTP，直接走 OAuth consent 链
//   - 其他                                              → 当成异常返回让上层看
//
// 返回的 (needsEmailOTP, continueURL, error)：
//   - needsEmailOTP=true              → 密码通过、需要 mailbox.WaitCode + email-otp/validate
//   - needsEmailOTP=false + continueURL!="" → 密码通过、且 OpenAI 已把会话推进到一个 continue_url
//     (例如 codex consent 页)；调用方必须 GET 那个 URL 才能让 cookie 升级为"已登录"
//   - 抛 errAddPhoneRequired         → 密码通过、但 OpenAI 要求 SMS 兜底
//   - 其他 error                      → 密码失败或 OpenAI 抛了未知页面
func postPasswordVerify(ctx context.Context, bc *browser.Client, st *state, password string) (bool, string, error) {
	tok, err := st.sentinel.SentinelToken(ctx, bc.HTTP, "password_verify")
	if err != nil {
		return false, "", fmt.Errorf("sentinel(password_verify): %w", err)
	}
	body, _ := json.Marshal(map[string]any{"password": password})
	headers := jsonHeaders(bc, st, authBase+"/log-in/password")
	headers["openai-sentinel-token"] = tok

	resp, err := postJSON(ctx, bc, authBase+"/api/accounts/password/verify", body, headers)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return false, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var out struct {
		ContinueURL string `json:"continue_url"`
		Method      string `json:"method"`
		Page        struct {
			Type string `json:"type"`
		} `json:"page"`
	}
	_ = json.Unmarshal(raw, &out)
	pt := out.Page.Type
	cont := strings.TrimSpace(out.ContinueURL)
	switch {
	case pt == "add_phone" || pt == "phone_verification" || pt == "phone_otp":
		return false, "", errAddPhoneRequired
	case pt == "email_otp_verification" || pt == "otp_verification":
		// 二次邮箱验证。OpenAI 也可能在响应里回 continue_url，我们一并带回去。
		return true, cont, nil
	default:
		// ref 实现：除 phone 外的所有情况都直接用 continue_url（external_url / redirect /
		// success / consent / sign_in_with_* 等）。OpenAI 把"下一步去哪"全塞 continue_url 里，
		// 不靠 page.type 决策。如果连 continue_url 都没有才是真出错。
		if cont == "" {
			return false, "", fmt.Errorf("password/verify 没 continue_url，page.type=%s body=%s", pt, snippet(raw))
		}
		return false, cont, nil
	}
}

// === 工具 ===

func postJSON(ctx context.Context, bc *browser.Client, urlStr string, body []byte, headers map[string]string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return bc.Do(req)
}

// setOaiDID 把 oai-did cookie 写到 .auth.openai.com 与 auth.openai.com（覆盖两种作用域）。
//
// sentinel 后端会校验该 cookie 与 oai-device-id 头一致，否则 token 不被信任。
func setOaiDID(jar *cookiejar.Jar, deviceID string) {
	for _, host := range []string{"https://auth.openai.com", "https://.auth.openai.com"} {
		u, _ := url.Parse(host)
		jar.SetCookies(u, []*http.Cookie{
			{Name: "oai-did", Value: deviceID, Path: "/", Domain: "auth.openai.com"},
		})
	}
}

// clearOAuthSessionCookies 清掉注册阶段建立的 OAuth session cookies，但保留 oai-did
// 和其他 device 级 cookie（避免 OpenAI 视为"换设备"触发风控）。
//
// 注册阶段 OpenAI 给我们的 oai-client-auth-session / login_session 里没有 workspaces[]
// 字段，直接拿来 codex authorize 会撞 hydra AuthApiFailure。必须清掉这些 session cookies，
// 重新走一遍完整 OAuth login，OpenAI 才会发新的含 workspaces[] 的 cookie。
//
// 通过 cookiejar.Jar 的"过期清除"机制：写一个 Expires 在过去的同名 cookie 即可让 jar
// 删掉它（http/cookiejar 会在下次 Cookies(u) 时排除已过期的项）。
func clearOAuthSessionCookies(jar *cookiejar.Jar) {
	if jar == nil {
		return
	}
	expiredAt := time.Unix(0, 0)
	// 这些 cookie 都是 OAuth login flow 的 session 凭证，在新 login 启动前必须清掉。
	names := []string{
		"oai-client-auth-session",
		"login_session",
		"oai-sc",
		"_cfuvid",            // CF 临时 challenge 缓存（不清也行，但偶尔旧值会卡 hydra）
		"oai-csrf-cookie",    // CSRF token 也按 session 重发
		"_oai_workspace",     // 有时 hydra 用它做 workspace fast-path
		"oai-allow-organic",  // 老 cookie
	}
	for _, host := range []string{"https://auth.openai.com", "https://.auth.openai.com"} {
		u, _ := url.Parse(host)
		expired := make([]*http.Cookie, 0, len(names))
		for _, n := range names {
			expired = append(expired, &http.Cookie{
				Name:    n,
				Value:   "",
				Path:    "/",
				Domain:  "auth.openai.com",
				Expires: expiredAt,
				MaxAge:  -1,
			})
		}
		jar.SetCookies(u, expired)
	}
}

// primeAuthorize GET 一次 codex authorize URL，让 OpenAI 建立新的 OAuth login session
// (写新 oai-client-auth-session、login_session 等 cookies)。响应内容直接丢弃。
//
// 这是参考实现 zc-zhangchen/any-auto-register oauth_pkce_client.py step 9-1 的等价：
// `self.session.get(login_oauth.auth_url, timeout=15)`。
//
// 不需要禁 redirect — 让 net/http 自动跟跳，最终落到 /create-account 或 /log-in 都行，
// 我们只在意 cookie。
func primeAuthorize(ctx context.Context, bc *browser.Client, st *state, authURL string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	// 与 platformAuthorize 对齐 ref：sec-fetch-site=same-origin。
	for k, v := range navHeaders(bc, st, "same-origin") {
		req.Header.Set(k, v)
	}
	req.Header.Set("Referer", platformBase+"/")
	resp, err := bc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// pickCode 从 URL（包含 query string）里抽 ?code=xxx；如未匹配返回 ""。
func pickCode(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if v := u.Query().Get("code"); v != "" {
		return v
	}
	// 兼容片段里：#code=xxx（极少见，但兜底）。
	if u.Fragment != "" {
		if vals, err := url.ParseQuery(u.Fragment); err == nil {
			if v := vals.Get("code"); v != "" {
				return v
			}
		}
	}
	return ""
}

func base64URLDecode(s string) ([]byte, error) {
	s = strings.NewReplacer("-", "+", "_", "/").Replace(s)
	if pad := len(s) % 4; pad > 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return base64.StdEncoding.DecodeString(s)
}

func snippet(b []byte) string {
	const max = 240
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

// uint64Random crypto/rand → uint64（用于 trace id）。
func uint64Random() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint64(b[:])
}
