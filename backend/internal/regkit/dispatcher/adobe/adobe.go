// Package adobe ?? Adobe Creative Cloud / Firefly ?????? dispatcher?
//
// ?????newwork/competition_interface_structure.py?
//
// ?????????????
//
//   - HTTP/1.1 + utls Chrome131 TLS ???regkit/browser?
//   - BFP ?????idg.adobe.com/v1/api/bfp_capture?? x-ims-genuine-token
//   - Arkose FunCaptcha?regkit/captcha 2Captcha / CapSolver?site_key=436DD567-...?
//   - ??????x-ims-genuine-token / x-ims-authentication-state-encrypted / x-ims-captcha-encrypted
//   - ?? 6 ? OTP + ?????????regkit/mailbox?
//   - SUSI redirect chain ? access_token????? body ? meta refresh URL??????
//
// ????pool_adobe?access_token / cookie / ?? / ?? / display_name / source=register??
package adobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/regkit/adoberefresh"
	"github.com/kleinai/backend/internal/regkit/browser"
	"github.com/kleinai/backend/internal/regkit/captcha"
	"github.com/kleinai/backend/internal/regkit/dispatcher"
	"github.com/kleinai/backend/internal/regkit/mailbox"
	"github.com/kleinai/backend/internal/regkit/nameset"
	"github.com/kleinai/backend/internal/service"
)

const (
	adobeAuthBase = "https://auth.services.adobe.com"
	adobeIMSBase  = "https://adobeid-na1.services.adobe.com"
	adobeIDGBase  = "https://idg.adobe.com"

	imsClientID        = "clio-playground-web"
	arkosePublicKey    = "436DD567-5435-4B14-89A6-2F1188E11334"
	arkoseAPISubdomain = "arks-client.adobe.com"

	redirectURI = "https://auth-light.identity.adobe.com/wrapper-popup-helper/index.html"
)

// Dispatcher Adobe ?? dispatcher?
type Dispatcher struct {
	dispatcher.Deps
	Pool *service.PoolAdobeService
}

// state ??????????????
type state struct {
	authStateEnc string // x-ims-authentication-state-encrypted
	genuineToken string // x-ims-genuine-token?BFP 200 ?? idg.adobe.com ???
	captchaBlob  string // x-ims-captcha-encrypted??? 400 ????
	sessionJWT   string // tokens?credential=password ??
	imsToken     string // /signin/v1/ims/tokens ??
	accessToken  string // fromSusi ?????? fragment ?? access_token
	idgTokenLs   string // BFP ??? token??????????
	debugID      string // x-debug-id??????? + fromSusi relay
	locale       string
	countryCode  string
	birthYear    int
	birthMonth   int
	birthDay     int
}

// Run ?? service.RegisterDispatcher?
func (d *Dispatcher) Run(ctx context.Context, svc *service.RegisterTaskService, task *model.RegisterTask) error {
	_ = svc.UpdateProgress(ctx, task.ID, "preflight", 5)
	solver, err := dispatcher.BuildCaptchaArkose(ctx, d.SysCfg)
	if err != nil {
		return err
	}
	if d.Pool == nil {
		return errors.New("PoolAdobeService not injected")
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
	st := &state{locale: "en_US", countryCode: "US"}
	if payload.Country != "" {
		st.countryCode = strings.ToUpper(payload.Country)
	}
	st.birthYear, st.birthMonth, st.birthDay = nameset.Birthday()
	// ?????????? debugID ? idgTokenLs?? BFP / x-debug-id /
	// fromSusi relay ????????Adobe ????"????"????
	st.debugID = randUUID()
	st.idgTokenLs = randUUID()

	_ = svc.UpdateProgress(ctx, task.ID, "pick_proxy", 10)
	resolved, err := d.ProxyPicker.Pick(ctx, payload.ProxyID)
	if err != nil {
		return fmt.Errorf("pick proxy: %w", err)
	}

	bc, err := browser.New(browser.Options{ProxyURL: resolved.URL, Timeout: 60 * time.Second})
	if err != nil {
		return fmt.Errorf("init browser client: %w", err)
	}

	_ = svc.UpdateProgress(ctx, task.ID, "acquire_mail", 18)
	mailCfg := dispatcher.BuildMailBackendConfig(ctx, d.SysCfg)
	mailCfg.Proxy = resolved.URL
	var acq *mailbox.AcquireResult
	if task.MailID != nil && *task.MailID > 0 {
		acq, err = d.MailMgr.AcquireByID(ctx, *task.MailID, "adobe", mailCfg)
	} else {
		// AcquireFresh：CF Worker 配了就即时签发（不入库），其他模式回退池化。
		acq, err = d.MailMgr.AcquireFresh(ctx, "adobe", mailCfg)
	}
	if err != nil {
		return fmt.Errorf("acquire mail: %w", err)
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
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("acquired mailbox %s (mode=%s)", email, acq.Row.Mode))
	if resolved.URL != "" {
		svc.LogInfo(ctx, task.ID, fmt.Sprintf("using proxy %s", dispatcher.MaskProxy(resolved.URL)))
	}

	// mailConsumed ??? true????????????? available?
	// ????? registered?? account_id??? ????????? Adobe ???
	// ???????? create_account ?? account_exists?
	//
	// ????create_account_with_captcha 200 ???
	mailConsumed := false

	failMail := func(reason string) error {
		if mailConsumed {
			// ?? registered??? account_id=0????? Acquire ?????
			_ = d.MailMgr.MarkRegistered(context.Background(), acq.Row.ID, 0)
			svc.LogWarn(ctx, task.ID, fmt.Sprintf("post-create failure, mailbox %s pinned as registered (no token): %s", acq.Row.Email, reason))
		} else {
			_, _ = d.MailMgr.MarkFailed(context.Background(), acq.Row.ID, reason, dispatcher.PoolMaxFailure)
		}
		mailReleased = true
		_ = acq.Mailbox.Close()
		return errors.New(reason)
	}

	// 1) ?? + precheck + ????/?? + ????????? 200/429 ??????
	_ = svc.UpdateProgress(ctx, task.ID, "prewarm", 22)
	_ = preWarm(ctx, bc, st)
	if err := precheck(ctx, bc, st, email); err != nil {
		return failMail(fmt.Sprintf("precheck: %v", err))
	}
	_ = passwordValidity(ctx, bc, st, payload.Password)
	_ = passwordLeak(ctx, bc, st, email, payload.Password)
	_ = domainInfo(ctx, bc, st, email)

	// 1.5) BFP ?????captcha_required ????????? BFP ?? ? ???
	// genuine_token ? ?? captcha ?????????BFP ????????????
	// ????????? captcha?
	_ = svc.UpdateProgress(ctx, task.ID, "bfp", 28)
	if err := bfpCapture(ctx, bc, st); err != nil {
		svc.LogWarn(ctx, task.ID, fmt.Sprintf("BFP fingerprint failed (continue but captcha will be harder): %v", err))
	} else {
		svc.LogInfo(ctx, task.ID, "BFP fingerprint passed, genuine_token acquired")
	}

	// 2) ????????? 400 + captcha_required??
	_ = svc.UpdateProgress(ctx, task.ID, "create_account_init", 35)
	body := buildAccountBody(email, payload.Password, payload.FirstName, payload.LastName, st)
	if err := createAccountInitial(ctx, bc, st, body); err != nil {
		return failMail(fmt.Sprintf("create_account first call: %v", err))
	}
	if st.captchaBlob == "" {
		return failMail("create_account first call did not return captcha blob")
	}

	// 3) Arkose 验证码求解：通过 ChainSolver 自动按主配置 → fallback 列表
	// 依次尝试。任一家返回非空 token 即视为成功；全部失败时聚合错误抛出，
	// 邮箱因 mailConsumed=false 仍可复用。
	_ = svc.UpdateProgress(ctx, task.ID, "captcha", 50)
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("submitting Arkose via %s (blob %d bytes, UA=%s)",
		solver.Name(), len(st.captchaBlob), trunc(bc.Profile.UserAgent, 32)))
	// 给 ChainSolver 装上 per-attempt 回调，把链路切换实时写进 task log。
	// 单家 solver 没有 hook 触发，行为退回旧版。
	var lastSolver string
	if chain, ok := solver.(*captcha.ChainSolver); ok {
		chain.OnAttempt = func(idx int, name string, total int) {
			lastSolver = name
			svc.LogInfo(ctx, task.ID, fmt.Sprintf("captcha attempt %d/%d via %s", idx, total, name))
		}
		chain.OnFailover = func(idx int, name string, err error) {
			svc.LogWarn(ctx, task.ID, fmt.Sprintf("captcha attempt %d via %s failed: %v — failing over", idx, name, err))
		}
	}
	// 链失败策略：
	//   - 单家 solver 超时 / UNSOLVABLE → ChainSolver 内部立刻 fail-over 到下一家
	//     （不消耗邮箱、不重复请求 Adobe）。
	//   - 整条链跑完仍失败 → fast-fail 跳号，避免把已知不可解的 blob 反复送进队列
	//     烧 captcha 预算。
	//   - 单家场景（未配 fallback）行为与旧版完全一致：60s 不出立刻跳号。
	captchaToken, solveErr := solver.SolveArkose(ctx, &captcha.ArkoseTask{
		WebsiteURL:   buildReferer(st),
		WebsiteKey:   arkosePublicKey,
		APISubdomain: arkoseAPISubdomain,
		Blob:         st.captchaBlob,
		UserAgent:    bc.Profile.UserAgent,
		// Adobe Arkose 不绑定客户端 IP，Python 参考实现走 Proxyless：
		// 强行带代理只会增加 BAD_PROXY 概率，worker pool 也会被 2Captcha 拒绝。
	})
	if solveErr != nil {
		return failMail(fmt.Sprintf("Arkose solve 全链失败（chain fast-fail）：%v", solveErr))
	}
	winnerName := lastSolver
	if winnerName == "" {
		winnerName = solver.Name()
	}
	svc.LogInfo(ctx, task.ID, fmt.Sprintf("Arkose token acquired via %s (%d bytes)", winnerName, len(captchaToken)))

	// 4) 用拿到的 captcha token 提交账号创建。
	// Adobe 偶尔会回 captcha_required（token 假阳性），原本会再解 1-2 轮，
	// 实测同 token 内 worker 解出来的 token 大概率都被 Adobe 判同源失效，
	// 重试基本是浪费 captcha 钱。这里也改成单次提交、失败立刻跳号。
	_ = svc.UpdateProgress(ctx, task.ID, "create_account_with_captcha", 60)
	if err := createAccountWithCaptcha(ctx, bc, st, body, captchaToken); err != nil {
		// account_exists 说明 Adobe 后端其实已经建过账号，但我们没拿到 fromSusi
		// 或 MFA。此时邮箱必须 pin 为已消耗，避免下次 acquire 又派同一个号。
		// 保留原 registered 路径里写 account_id 的兜底逻辑。
		if strings.Contains(err.Error(), "account_exists") {
			mailConsumed = true
			svc.LogWarn(ctx, task.ID, "Adobe reports account_exists for this mailbox; pinning as consumed")
		}
		return failMail(fmt.Sprintf("create_account with captcha (fast-fail, no retry): %v", err))
	}
	// ?? Adobe ???????????????? fromSusi/MFA ????????????
	mailConsumed = true
	svc.LogInfo(ctx, task.ID, "Adobe account created on backend, mailbox pinned to consumed state")

	// 5) ???? session_jwt?
	_ = svc.UpdateProgress(ctx, task.ID, "exchange_password", 68)
	if err := exchangePassword(ctx, bc, st, email, payload.Password); err != nil {
		return failMail(fmt.Sprintf("exchange_password: %v", err))
	}

	// 6) ?? MFA??? ? ? OTP ? ???
	_ = svc.UpdateProgress(ctx, task.ID, "send_mfa", 75)
	mfaVerified := false
	mfaStart := time.Now()
	if err := sendMFA(ctx, bc, st); err != nil {
		svc.LogWarn(ctx, task.ID, fmt.Sprintf("send MFA email failed: %v", err))
	} else {
		otp, err := acq.Mailbox.WaitCode(ctx, mailbox.WaitOptions{
			Provider: mailbox.ProviderAdobe,
			SinceTS:  mfaStart.Add(-30 * time.Second),
			Timeout:  240 * time.Second,
		})
		if err == nil {
			svc.LogInfo(ctx, task.ID, fmt.Sprintf("MFA OTP received %s", dispatcher.MaskOTP(otp)))
			if vErr := verifyMFA(ctx, bc, st, otp); vErr == nil {
				mfaVerified = true
			} else {
				svc.LogWarn(ctx, task.ID, fmt.Sprintf("MFA verify failed: %v", vErr))
			}
		} else {
			svc.LogWarn(ctx, task.ID, fmt.Sprintf("first MFA wait failed: %v, retry once", err))
			retryStart := time.Now()
			if err := sendMFA(ctx, bc, st); err == nil {
				otp2, err2 := acq.Mailbox.WaitCode(ctx, mailbox.WaitOptions{
					Provider: mailbox.ProviderAdobe,
					SinceTS:  retryStart.Add(-30 * time.Second),
					Timeout:  180 * time.Second,
				})
				if err2 == nil {
					svc.LogInfo(ctx, task.ID, fmt.Sprintf("MFA OTP after retry %s", dispatcher.MaskOTP(otp2)))
					if vErr := verifyMFA(ctx, bc, st, otp2); vErr == nil {
						mfaVerified = true
					}
				} else {
					svc.LogWarn(ctx, task.ID, fmt.Sprintf("MFA retry still failed: %v (continuing without MFA)", err2))
				}
			} else {
				svc.LogWarn(ctx, task.ID, fmt.Sprintf("MFA resend failed: %v", err))
			}
		}
	}
	if !mfaVerified {
		_ = svc.UpdateProgress(ctx, task.ID, "send_mfa_warn", 75)
	}

	// 7) ims/tokens ? fromSusi ? access_token?
	// ?????? Adobe ????????????????????? available
	_ = svc.UpdateProgress(ctx, task.ID, "ims_tokens", 85)
	if err := imsTokens(ctx, bc, st); err != nil {
		return failMail(fmt.Sprintf("ims/tokens: %v", err))
	}
	_ = svc.UpdateProgress(ctx, task.ID, "from_susi", 90)
	// fromSusi 偶尔会"假成功"——返回 200 + meta refresh，但 fragment 里的 state JSON
	// access_token 字段是空字符串。常见原因：
	//   1. PKCE/state 时序问题（imsToken 和 SUSI form 时间差太大）
	//   2. 同 IP 短时间注册过多触发软风控
	//   3. Adobe IMS 后端瞬时抖动
	//
	// 快速失败策略：单次失败立即走 cooldown 入库，不做 1.5s × 3 重试。
	// 实测同会话内重试基本都拿不到（state/PKCE 已与 imsToken 绑死），多等 4.5s
	// 也是浪费；账号 + cookie 已完整入库，事后 RefreshAll 用 cookie 走
	// ims/check/v6/token 重激活率 > 90%，比同会话硬撑 retry 划算得多。
	if err := fromSusi(ctx, bc, st); err != nil {
		// fromSusi 一次拿不到 token，但账号已经在 Adobe 那边创建好了。
		// 不直接 fail —— 把账号 + cookie 入库标 cooldown，让 RefreshAll 用
		// cookie session 走 refresh 流程拿回 access_token，避免浪费 captcha。
		svc.LogWarn(ctx, task.ID, fmt.Sprintf("fromSusi 失败（fast-fail, no retry），账号已建（cookie 完整），写入号池待 RefreshAll 重激活：%v", err))
		st.accessToken = "" // 显式标空，让后面入库走 cooldown 分支
	}

	// 8) ??????? JWT + ? profile/credits??? newwork/token_refresh.py?
	//
	// access_token ????????? firefly ?????profile 401?credits 0??
	// ???? ExtractJWTExpiry ???????? user_id ??????
	// FetchOnly ??????401/??????????? credits=-1?
	_ = svc.UpdateProgress(ctx, task.ID, "persist", 96)
	displayName := strings.TrimSpace(payload.FirstName + " " + payload.LastName)
	cookies := serializeCookies(bc)

	// 没拿到 access_token 但账号已建（fromSusi 反复 200 + 空 token 这种）：
	// 入库标 cooldown，让后续 RefreshAll 用 cookie 走 ims/check/v6/token 重试拿 token。
	// 一旦刷到 token 自动转 valid，相当于用 1 元钱 captcha 换一个 "延迟激活" 的号。
	poolStatus := model.AdobeStatusValid
	notes := payload.Notes
	if st.accessToken == "" {
		poolStatus = model.AdobeStatusCooldown
		extraNote := "fromSusi 未拿到 token，已入池待 refresh 重激活"
		if notes != "" {
			notes = notes + " | " + extraNote
		} else {
			notes = extraNote
		}
	}
	createReq := &dto.AdobePoolCreateReq{
		Email:       email,
		DisplayName: displayName,
		Password:    payload.Password,
		AccessToken: st.accessToken,
		Cookie:      cookies,
		Source:      model.AdobeSourceRegister,
		Status:      poolStatus,
		Notes:       notes,
	}
	if expAt := adoberefresh.ExtractJWTExpiry(st.accessToken); expAt > 0 {
		createReq.ExpiresAt = expAt * 1000 // dto ???
	}
	if uid := adoberefresh.ExtractAccountIDFromJWT(st.accessToken); uid != "" {
		createReq.AdobeUserID = uid
	}
	// ????? credits???????????? 0?
	if st.accessToken != "" {
		fc := adoberefresh.FetchOnly(ctx, st.accessToken, adoberefresh.RefreshOptions{
			ProxyURL:  resolved.URL,
			Timeout:   15 * time.Second,
		})
		if fc != nil {
			if fc.DisplayName != "" {
				createReq.DisplayName = fc.DisplayName
			}
			if fc.UserID != "" {
				createReq.AdobeUserID = fc.UserID
			}
			if fc.Credits >= 0 {
				createReq.Credits = fc.Credits
			}
		}
	}

	created, err := d.Pool.Create(ctx, createReq)
	if err != nil {
		return fmt.Errorf("persist pool_adobe: %w", err)
	}

	_ = d.MailMgr.MarkRegistered(ctx, acq.Row.ID, created.ID)
	mailReleased = true
	_ = acq.Mailbox.Close()

	return svc.FinishSuccess(ctx, task.ID, created.ID, map[string]any{
		"pool_account_id":  created.ID,
		"email":            email,
		"has_access_token": st.accessToken != "",
		"display_name":     displayName,
		"credits":          createReq.Credits,
		"expires_at":       createReq.ExpiresAt,
	})
}

// === HTTP ?? ===

func commonHeaders(bc *browser.Client, st *state) map[string]string {
	return map[string]string{
		"accept":          "application/json, text/plain, */*",
		"accept-language": bc.Profile.Locale,
		"content-type":    "application/json",
		"origin":          adobeAuthBase,
		"sec-fetch-site":  "same-origin",
		"sec-fetch-mode":  "cors",
		"sec-fetch-dest":  "empty",
		"x-ims-clientid":  imsClientID,
		"x-debug-id":      st.debugID,
	}
}

func buildReferer(st *state) string {
	v := url.Values{}
	v.Set("client_id", imsClientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("response_type", "token")
	v.Set("scope", "AdobeID,firefly_api,openid,offline_access")
	v.Set("locale", st.locale)
	v.Set("flow_type", "token")
	v.Set("idp_flow_type", "create_account")
	return adobeAuthBase + "/" + st.locale + "/deeplink.html?" + v.Encode()
}

func preWarm(ctx context.Context, bc *browser.Client, st *state) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, buildReferer(st), nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, err := bc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256*1024))
	return nil
}

// bfpCapture ???????? idg.adobe.com??? genuine_token ?? create_account?
//
// ?????????????? err ??????
// bfpCapture ???????? idg.adobe.com???? request_id ?? x-ims-genuine-token
// ???? create_account ??? captcha ???
//
// ?????? cn.bing.com_2026_05_07 HAR ? 200 ?????
//
//   - ? x-client-id??? x-ims-clientid?
//   - ?? x-debug-id
//   - x-api-key: genuine-bfp-ims??????
//
// ?? body?{"reason_code":200,"request_id":"<uuid>"}?request_id ? genuine_token?
// ????? set-cookie idg_token?? cookie jar ?????????????
func bfpCapture(ctx context.Context, bc *browser.Client, st *state) error {
	body, _ := json.Marshal(buildBFPPayload(st.idgTokenLs, bc.Profile))
	headers := map[string]string{
		"accept":          "*/*",
		"accept-language": bc.Profile.Locale,
		"content-type":    "application/json",
		"origin":          adobeAuthBase,
		"sec-fetch-site":  "same-site",
		"sec-fetch-mode":  "cors",
		"sec-fetch-dest":  "empty",
		"x-api-key":       "genuine-bfp-ims",
		"x-client-id":     imsClientID,
	}
	resp, err := jsonReq(ctx, bc, http.MethodPost,
		adobeIDGBase+"/v1/api/bfp_capture", body, headers, buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	raw, _ := io.ReadAll(resp.Body)
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err == nil {
		if v, _ := data["request_id"].(string); v != "" && st.genuineToken == "" {
			st.genuineToken = v
		}
	}
	return nil
}

func precheck(ctx context.Context, bc *browser.Client, st *state, email string) error {
	body, _ := json.Marshal(map[string]any{"username": email, "usernameType": "EMAIL"})
	resp, err := jsonReq(ctx, bc, http.MethodPost, adobeAuthBase+"/signin/v2/users/accounts", body, commonHeaders(bc, st), buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	if resp.StatusCode == 200 || resp.StatusCode == 201 || resp.StatusCode == 204 {
		return nil
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

func passwordValidity(ctx context.Context, bc *browser.Client, st *state, password string) error {
	body, _ := json.Marshal(map[string]any{"password": password})
	resp, err := jsonReq(ctx, bc, http.MethodPost,
		adobeAuthBase+"/signin/v1/passwords/validity?existingUser=false", body, commonHeaders(bc, st), buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	return nil
}

func passwordLeak(ctx context.Context, bc *browser.Client, st *state, email, password string) error {
	body, _ := json.Marshal(map[string]any{
		"username":     email,
		"usernameType": "EMAIL",
		"password":     password,
	})
	resp, err := jsonReq(ctx, bc, http.MethodPost,
		adobeAuthBase+"/signin/v1/passwords/leak_verification", body, commonHeaders(bc, st), buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	return nil
}

func domainInfo(ctx context.Context, bc *browser.Client, st *state, email string) error {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return errors.New("email missing @")
	}
	domain := email[at+1:]
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		adobeAuthBase+"/signin/v3/domains/"+domain+"/info", nil)
	req.Header.Set("Referer", buildReferer(st))
	for k, v := range commonHeaders(bc, st) {
		req.Header.Set(k, v)
	}
	resp, err := bc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	return nil
}

func buildAccountBody(email, password, first, last string, st *state) []byte {
	m := map[string]any{
		"account": map[string]any{
			"email":       email,
			"phoneNumber": nil,
			"firstName":   first,
			"lastName":    last,
			"password":    password,
			"countryCode": st.countryCode,
			"termsOfUseAcceptances": []map[string]any{{
				"accepted": true,
				"name":     "ADOBE_MASTER",
				"language": st.locale,
			}},
			"marketingConsent": map[string]any{"text": "", "accepted": false},
			"dateOfBirth": map[string]any{
				"day":   st.birthDay,
				"month": st.birthMonth,
				"year":  st.birthYear,
			},
		},
		"clientRedirect":     nil,
		"redirectUri":        redirectURI,
		"regionalOptInKorea": nil,
		"regionalOptInChina": nil,
		"locale":             st.locale,
		"idpFlow":            "create_account",
		"inviteCode":         nil,
	}
	b, _ := json.Marshal(m)
	return b
}

func createAccountInitial(ctx context.Context, bc *browser.Client, st *state, body []byte) error {
	headers := commonHeaders(bc, st)
	if st.genuineToken != "" {
		headers["x-ims-genuine-token"] = st.genuineToken
	}
	resp, err := jsonReq(ctx, bc, http.MethodPost, adobeAuthBase+"/signin/v2/accounts", body, headers, buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	if resp.StatusCode == 200 {
		return nil
	}
	if resp.StatusCode == 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if !bytes.Contains(raw, []byte("captcha_required")) {
			return fmt.Errorf("400 not captcha_required: %s", snippet(raw))
		}
		return nil
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

func createAccountWithCaptcha(ctx context.Context, bc *browser.Client, st *state, body []byte, captchaToken string) error {
	headers := commonHeaders(bc, st)
	// captcha token + ?? blob ???????????? captcha_required?
	headers["x-ims-entcaptcha-response"] = captchaToken
	if st.captchaBlob != "" {
		headers["x-ims-captcha-encrypted"] = st.captchaBlob
	}
	if st.genuineToken != "" {
		headers["x-ims-genuine-token"] = st.genuineToken
	}
	if st.authStateEnc != "" {
		headers["x-ims-authentication-state-encrypted"] = st.authStateEnc
	}
	resp, err := jsonReq(ctx, bc, http.MethodPost, adobeAuthBase+"/signin/v2/accounts", body, headers, buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	return nil
}

func exchangePassword(ctx context.Context, bc *browser.Client, st *state, email, password string) error {
	body, _ := json.Marshal(map[string]any{
		"username":     email,
		"usernameType": "EMAIL",
		"password":     password,
		"accountType":  "individual",
		"rememberMe":   true,
	})
	headers := commonHeaders(bc, st)
	if st.authStateEnc != "" {
		headers["x-ims-authentication-state-encrypted"] = st.authStateEnc
	}
	resp, err := jsonReq(ctx, bc, http.MethodPost,
		adobeAuthBase+"/signin/v2/tokens?credential=password", body, headers, buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	raw, _ := io.ReadAll(resp.Body)
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err == nil {
		if t, _ := data["token"].(string); t != "" {
			st.sessionJWT = t
		}
	}
	if st.sessionJWT == "" {
		return errors.New("response missing token field")
	}
	return nil
}

func sendMFA(ctx context.Context, bc *browser.Client, st *state) error {
	headers := commonHeaders(bc, st)
	headers["authorization"] = "Bearer " + st.sessionJWT
	if st.authStateEnc != "" {
		headers["x-ims-authentication-state-encrypted"] = st.authStateEnc
	}
	body := []byte("{}")
	resp, err := jsonReq(ctx, bc, http.MethodPost,
		adobeAuthBase+"/signin/v3/challenges?purpose=multiFactorAuthentication&factor=email&extendedAuthState=false",
		body, headers, buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func verifyMFA(ctx context.Context, bc *browser.Client, st *state, code string) error {
	headers := commonHeaders(bc, st)
	headers["authorization"] = "Bearer " + st.sessionJWT
	// HAR ?? PUT /challenges?purpose=multiFactorAuthentication ????
	// x-ims-authentication-state-encrypted??? sendMFA ??????? 401
	if st.authStateEnc != "" {
		headers["x-ims-authentication-state-encrypted"] = st.authStateEnc
	}
	body, _ := json.Marshal(map[string]any{"code": code})
	resp, err := jsonReq(ctx, bc, http.MethodPut,
		adobeAuthBase+"/signin/v3/challenges?purpose=multiFactorAuthentication", body, headers, buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	return nil
}

func imsTokens(ctx context.Context, bc *browser.Client, st *state) error {
	headers := commonHeaders(bc, st)
	headers["authorization"] = "Bearer " + st.sessionJWT
	body, _ := json.Marshal(map[string]any{"rememberMe": true, "reauthenticate": nil})
	resp, err := jsonReq(ctx, bc, http.MethodPost,
		adobeAuthBase+"/signin/v1/ims/tokens", body, headers, buildReferer(st))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err == nil {
		if t, _ := data["token"].(string); t != "" {
			st.imsToken = t
		}
	}
	if st.imsToken == "" {
		return errors.New("ims/tokens response missing token field")
	}
	return nil
}

// fromSusi ?? SUSI ??? access_token????? Python newwork/competition_interface_structure.py?
//
//  1. POST /ims/fromSusi ?? 200/302?body ? HTML?meta refresh?
//  2. ???? body ? url=xxx ? meta refresh URL
//  3. ?????? CheckRedirect ???????
//  4. ????? query / fragment ? access_token=xxx ???
// fromSusi ?? token ??? redirect_uri ? fragment?
//
// ????? cn.bing.com_2026_05_07 HAR?????????????????
// ???? Adobe ???? ~200 ????????? meta refresh??
// ??????? access_token????
//
//   - callback ??? ims-na1.adobelogin.com ? /ims/adobeid/<client>/AdobeID/token
//     ??? URL??? redirect_uri / state / code_challenge_method ? query
//     ?????
//   - scope ???? AdobeID,firefly_api,openid,pps.read,pps.write,
//     additional_info.projectedProductContext,additional_info.ownerOrg,
//     uds_read,uds_write,ab.manage,read_organizations,additional_info.roles,
//     account_cluster.read,creative_production,profile?
//   - state ????? JSON?{"name":"AccessTokenFlow","side":"popup","data":{...,"relay":"<uuid>",...}}?
//   - relay ??? state.data.relay ??? UUID?
//   - s_p ??????????
//   - flow=signUp?idp_flow_type=create_account?flow_type=token?response_type=token?
func fromSusi(ctx context.Context, bc *browser.Client, st *state) error {
	relay := st.debugID
	scope := "AdobeID,firefly_api,openid,pps.read,pps.write," +
		"additional_info.projectedProductContext,additional_info.ownerOrg," +
		"uds_read,uds_write,ab.manage,read_organizations," +
		"additional_info.roles,account_cluster.read,creative_production,profile"

	stateJSON, _ := json.Marshal(map[string]any{
		"name": "AccessTokenFlow",
		"side": "popup",
		"data": map[string]any{
			"access_token":      "",
			"returnOrigin":      "https://auth-light.identity.adobe.com",
			"client_id":         imsClientID,
			"clientId":          imsClientID,
			"relay":             relay,
			"useMessageChannel": true,
		},
	})

	// HAR ? callback ??? redirect_uri / state ?? URL ?????
	// ???? callback ????????????"????"??
	// ??????? HAR ????????redirect_uri & state & code_challenge_method & use_ms_for_expiry
	callback := "https://ims-na1.adobelogin.com/ims/adobeid/" + imsClientID + "/AdobeID/token?" +
		"redirect_uri=" + url.QueryEscape(redirectURI) +
		"&state=" + url.QueryEscape(string(stateJSON)) +
		"&code_challenge_method=plain" +
		"&use_ms_for_expiry=false"

	// HAR ?????????????????Adobe IMS ????????
	// ?????? KV ?????????? url.Values.Encode()
	type kv struct{ k, v string }
	pairs := []kv{
		{"remember_me", "true"},
		{"deeplink", "signup"},
		{"callback", callback},
		{"client_id", imsClientID},
		{"scope", scope},
		{"state", string(stateJSON)},
		{"relay", relay},
		{"locale", st.locale},
		{"flow_type", "token"},
		{"idp_flow_type", "create_account"},
		{"dl", "true"},
		{"s_p", "google,facebook,apple,microsoft,line,kakao"},
		{"response_type", "token"},
		{"code_challenge_method", "plain"},
		{"redirect_uri", redirectURI},
		{"use_ms_for_expiry", "false"},
		{"flow", "signUp"},
		{"token", st.imsToken},
		{"probing_results", "[0]"},
	}
	var formBuf strings.Builder
	for i, p := range pairs {
		if i > 0 {
			formBuf.WriteByte('&')
		}
		formBuf.WriteString(url.QueryEscape(p.k))
		formBuf.WriteByte('=')
		formBuf.WriteString(url.QueryEscape(p.v))
	}
	formBody := formBuf.String()

	// ? 1 ??fromSusi ???????????? body / Location?
	origCheck := bc.HTTP.CheckRedirect
	bc.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { bc.HTTP.CheckRedirect = origCheck }()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		adobeIMSBase+"/ims/fromSusi", strings.NewReader(formBody))
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	req.Header.Set("accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("accept-language", bc.Profile.Locale)
	req.Header.Set("origin", adobeAuthBase)
	req.Header.Set("sec-fetch-mode", "navigate")
	req.Header.Set("sec-fetch-dest", "document")
	req.Header.Set("sec-fetch-site", "same-site")
	req.Header.Set("Referer", buildReferer(st))
	resp, err := bc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	consumeResp(resp, st)

	// access_token ????????????
	//   a) Location ??????
	//   b) ?? body ?? <meta http-equiv="refresh" content="0;url=https://...#access_token=...">
	//   c) Body ????? access_token=xxx
	if loc := resp.Header.Get("Location"); loc != "" {
		if t := extractAccessToken(loc); t != "" {
			st.accessToken = t
			return nil
		}
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if t := extractAccessToken(string(raw)); t != "" {
		st.accessToken = t
		return nil
	}

	// ?? body ? meta refresh URL?
	m := reMetaURL.FindStringSubmatch(string(raw))
	if len(m) < 2 {
		// ??????? Location???????????
		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if loc == "" {
			return fmt.Errorf("fromSusi response has no meta refresh URL or Location (status=%d, body %d bytes)", resp.StatusCode, len(raw))
		}
		m = []string{"", loc}
	}
	next := strings.TrimSpace(m[1])
	next = strings.ReplaceAll(next, "&amp;", "&")

	// ?????? 8 ?????? URL / Location / fragment ? access_token ????
	for hop := 0; hop < 8 && next != ""; hop++ {
		if t := extractAccessToken(next); t != "" {
			st.accessToken = t
			return nil
		}
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		req2.Header.Set("Accept", "text/html,*/*")
		req2.Header.Set("Referer", buildReferer(st))
		r2, err := bc.Do(req2)
		if err != nil {
			return fmt.Errorf("redirect hop %d: %w", hop, err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r2.Body, 64*1024))
		_ = r2.Body.Close()
		// ???? final URL?? fragment?? access_token ??????
		if r2.Request != nil && r2.Request.URL != nil {
			if t := extractAccessToken(r2.Request.URL.String()); t != "" {
				st.accessToken = t
				return nil
			}
		}
		loc := strings.TrimSpace(r2.Header.Get("Location"))
		if t := extractAccessToken(loc); t != "" {
			st.accessToken = t
			return nil
		}
		if loc == "" {
			break
		}
		next = loc
	}
	return fmt.Errorf("access_token not found in fromSusi redirect chain (status=%d body=%d bytes): %s",
		resp.StatusCode, len(raw), snippet(raw))
}

// === ?? ===

var (
	reMetaURL = regexp.MustCompile(`(?is)url=([^"'>]+)`)
	reAccess  = regexp.MustCompile(`access_token=([^&#"\s]+)`)
)

func extractAccessToken(rawURL string) string {
	if m := reAccess.FindStringSubmatch(rawURL); len(m) > 1 {
		return m[1]
	}
	return ""
}

func consumeResp(resp *http.Response, st *state) {
	if v := resp.Header.Get("x-ims-genuine-token"); v != "" {
		st.genuineToken = v
	}
	if v := resp.Header.Get("x-ims-authentication-state-encrypted"); v != "" {
		st.authStateEnc = v
	}
	if v := resp.Header.Get("x-ims-captcha-encrypted"); v != "" {
		st.captchaBlob = v
	}
}

func jsonReq(ctx context.Context, bc *browser.Client, method, urlStr string, body []byte, headers map[string]string, referer string) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(ctx, method, urlStr, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	return bc.Do(req)
}

func serializeCookies(bc *browser.Client) string {
	var parts []string
	for _, host := range []string{
		"https://auth.services.adobe.com",
		"https://adobeid-na1.services.adobe.com",
		"https://ims-na1.adobelogin.com",
		"https://firefly.adobe.com",
		"https://idg.adobe.com",
	} {
		u, _ := url.Parse(host)
		for _, c := range bc.Jar.Cookies(u) {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	seen := map[string]struct{}{}
	uniq := parts[:0]
	for _, p := range parts {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			uniq = append(uniq, p)
		}
	}
	return strings.Join(uniq, "; ")
}

func randUUID() string {
	const hex = "0123456789abcdef"
	b := make([]byte, 36)
	for i := range b {
		b[i] = hex[nameset.Random(16)]
	}
	b[8] = '-'
	b[13] = '-'
	b[14] = '4'
	b[18] = '-'
	b[19] = hex[nameset.Random(4)+8]
	b[23] = '-'
	return string(b)
}

func snippet(b []byte) string {
	const max = 240
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "?"
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "?"
}
