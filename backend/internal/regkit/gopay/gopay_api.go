package gopay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// （旧版 gopayPostWithRetry 已下沉到 client.go 的 doWithTransportRetry —— 所有
// transport 函数 doJSON / doForm / doGET / doRedirectGET 现在都自带握手层重试，
// 这里不再需要本 API 文件单独包一层。）

// runLinkingPhase Step 8-12：validate-reference → user-consent → 等 OTP →
// validate-otp → tokenize PIN → validate-pin。
func (c *Charger) runLinkingPhase(ctx context.Context) error {
	ref := c.linkingReference
	if ref == "" {
		return newErr("linking_phase", ErrCodeUnrecoverable, 0, "missing linking reference")
	}

	// Step 8
	if err := c.gopayValidateReference(ctx, ref); err != nil {
		return err
	}
	// Step 9
	otpStartedAt := nowFunc()
	if err := c.gopayUserConsent(ctx, ref); err != nil {
		return err
	}
	// 等 OTP（OTPProvider 阻塞直到拿到，或返回错误：超时 / 取消 / 风控）。
	otp, err := c.cfg.OTPProvider.Wait(ctx, OTPRequest{
		ReferenceID: ref,
		Phone:       c.cfg.Wallet.PhoneNumber,
		CountryCode: c.cfg.Wallet.CountryCode,
		StartedAt:   otpStartedAt,
		Stage:       OTPStageLinking,
	})
	if err != nil {
		return mapOTPProviderError(err, "linking")
	}
	c.log("info", "[Plus 升级] WhatsApp OTP 已收到（%s）", maskOTP(otp))

	// Step 10
	challengeID, clientID, err := c.gopayValidateOTP(ctx, ref, otp)
	if err != nil {
		return err
	}
	// Step 11
	pinToken, err := c.tokenizePIN(ctx, challengeID, clientID, PinClientIDLink)
	if err != nil {
		return err
	}
	// Step 12
	return c.gopayValidatePIN(ctx, ref, pinToken)
}

// runPaymentPhase Step 14：payment/validate (轮询) → payment/confirm → tokenize → process。
func (c *Charger) runPaymentPhase(ctx context.Context) error {
	ref := c.chargeRef
	if ref == "" {
		return newErr("payment_phase", ErrCodeUnrecoverable, 0, "missing charge_ref")
	}
	if err := c.gopayPaymentValidate(ctx, ref); err != nil {
		return err
	}
	challengeID, clientID, err := c.gopayPaymentConfirm(ctx, ref)
	if err != nil {
		return err
	}
	pinToken, err := c.tokenizePIN(ctx, challengeID, clientID, PinClientIDCharge)
	if err != nil {
		return err
	}
	if err := c.gopayPaymentProcess(ctx, ref, pinToken); err != nil {
		return err
	}
	if c.cfg.SuccessWaitSeconds > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeAfter(secondsToDuration(c.cfg.SuccessWaitSeconds)):
		}
	}
	return nil
}

// ───── Step 8-12: linking ──────────────────────────────────────────

// gopayValidateReference Step 8。
func (c *Charger) gopayValidateReference(ctx context.Context, ref string) error {
	const step = "gopay_validate_reference"
	body := map[string]any{"reference_id": ref}
	headers := gopayDefaultHeaders()
	status, raw, err := c.doJSON(ctx, c.ext, http.MethodPost,
		"https://gwa.gopayapi.com/v1/linking/validate-reference", body, headers, nil)
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "POST")
	}
	if status != http.StatusOK {
		return newErr(step, ErrCodeMidtransLink, status, "body=%s", shortBody(raw, 300))
	}
	var data genericJSON
	_ = json.Unmarshal(raw, &data)
	if !boolField(data, "success") {
		return newErr(step, ErrCodeMidtransLink, status, "success=false body=%s", shortBody(raw, 300))
	}
	return nil
}

// gopayUserConsent Step 9：触发 OTP 推送到 WhatsApp。
//
// 注意：副作用端点 —— transport 层重试仅在握手层失败时触发，
// 此时请求没送达 GoPay，不会触发重复 OTP 推送。
func (c *Charger) gopayUserConsent(ctx context.Context, ref string) error {
	const step = "gopay_user_consent"
	body := map[string]any{"reference_id": ref}
	headers := gopayDefaultHeaders()
	headers.Set("x-user-locale", "en-US")

	status, raw, err := c.doJSON(ctx, c.ext, http.MethodPost,
		"https://gwa.gopayapi.com/v1/linking/user-consent", body, headers, nil)
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "POST")
	}
	if status != http.StatusOK {
		return newErr(step, ErrCodeMidtransLink, status, "body=%s", shortBody(raw, 300))
	}
	var data genericJSON
	_ = json.Unmarshal(raw, &data)
	if !boolField(data, "success") {
		return newErr(step, ErrCodeMidtransLink, status, "success=false body=%s", shortBody(raw, 300))
	}
	c.log("info", "[Plus 升级] GoPay 已同意授权，WhatsApp OTP 已下发")
	return nil
}

// gopayValidateOTP Step 10：返回 (challenge_id, client_id) 给 PIN tokenize 用。
func (c *Charger) gopayValidateOTP(ctx context.Context, ref, otp string) (string, string, error) {
	const step = "gopay_validate_otp"
	body := map[string]any{"reference_id": ref, "otp": otp}
	headers := gopayDefaultHeaders()
	status, raw, err := c.doJSON(ctx, c.ext, http.MethodPost,
		"https://gwa.gopayapi.com/v1/linking/validate-otp", body, headers, nil)
	if err != nil {
		return "", "", wrapErr(step, ErrCodeNetwork, 0, err, "POST")
	}
	if status != http.StatusOK {
		return "", "", newErr(step, ErrCodeMidtransLink, status, "body=%s", shortBody(raw, 300))
	}
	var wrap gopayChallengeWrap
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return "", "", wrapErr(step, ErrCodeMidtransLink, status, err, "decode")
	}
	if !wrap.Success {
		return "", "", newErr(step, ErrCodeMidtransLink, status, "success=false body=%s", shortBody(raw, 300))
	}
	v := wrap.Data.Challenge.Action.Value
	if v.ChallengeID == "" || v.ClientID == "" {
		return "", "", newErr(step, ErrCodeMidtransLink, status,
			"missing challenge details body=%s", shortBody(raw, 400))
	}
	c.log("info", "[Plus 升级] OTP 校验通过")
	return v.ChallengeID, v.ClientID, nil
}

// gopayValidatePIN Step 12：linking 完成。
func (c *Charger) gopayValidatePIN(ctx context.Context, ref, pinToken string) error {
	const step = "gopay_validate_pin"
	body := map[string]any{"reference_id": ref, "token": pinToken}
	headers := gopayDefaultHeaders()
	status, raw, err := c.doJSON(ctx, c.ext, http.MethodPost,
		"https://gwa.gopayapi.com/v1/linking/validate-pin", body, headers, nil)
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "POST")
	}
	if status != http.StatusOK {
		return newErr(step, ErrCodeMidtransLink, status, "body=%s", shortBody(raw, 300))
	}
	var data genericJSON
	_ = json.Unmarshal(raw, &data)
	if !boolField(data, "success") {
		return newErr(step, ErrCodeMidtransLink, status, "success=false body=%s", shortBody(raw, 300))
	}
	c.log("info", "[Plus 升级] GoPay 钱包绑定完成")
	return nil
}

// ───── Step 14: payment ────────────────────────────────────────────

// gopayPaymentValidate Step 14a：charge 创建后 GoPay 后端要数秒才能 fetch。
// 轮询 PaymentValidatePollAttempts 次。
func (c *Charger) gopayPaymentValidate(ctx context.Context, ref string) error {
	const step = "gopay_payment_validate"
	headers := gopayDefaultHeaders()
	urlStr := fmt.Sprintf("https://gwa.gopayapi.com/v1/payment/validate?reference_id=%s", ref)

	var lastStatus int
	var lastBody []byte
	for i := 0; i < PaymentValidatePollAttempts; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		status, _, raw, err := c.doGET(ctx, c.ext, urlStr, headers, nil)
		if err != nil {
			return wrapErr(step, ErrCodeNetwork, 0, err, "GET attempt=%d", i+1)
		}
		lastStatus, lastBody = status, raw
		if status == http.StatusOK {
			var data genericJSON
			_ = json.Unmarshal(raw, &data)
			if boolField(data, "success") {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeAfter(PaymentValidatePollInterval):
		}
	}
	return newErr(step, ErrCodeMidtransLink, lastStatus, "exhausted polls body=%s", shortBody(lastBody, 300))
}

// gopayPaymentConfirm Step 14b：返回 (challenge_id, client_id) 用 PIN tokenize。
func (c *Charger) gopayPaymentConfirm(ctx context.Context, ref string) (string, string, error) {
	const step = "gopay_payment_confirm"
	headers := gopayDefaultHeaders()
	urlStr := fmt.Sprintf("https://gwa.gopayapi.com/v1/payment/confirm?reference_id=%s", ref)
	body := map[string]any{"payment_instructions": []any{}}
	status, raw, err := c.doJSON(ctx, c.ext, http.MethodPost, urlStr, body, headers, nil)
	if err != nil {
		return "", "", wrapErr(step, ErrCodeNetwork, 0, err, "POST")
	}
	if status != http.StatusOK {
		return "", "", newErr(step, ErrCodeMidtransLink, status, "body=%s", shortBody(raw, 300))
	}
	var wrap gopayChallengeWrap
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return "", "", wrapErr(step, ErrCodeMidtransLink, status, err, "decode")
	}
	if !wrap.Success {
		return "", "", newErr(step, ErrCodeMidtransLink, status, "success=false body=%s", shortBody(raw, 300))
	}
	v := wrap.Data.Challenge.Action.Value
	if v.ChallengeID == "" || v.ClientID == "" {
		return "", "", newErr(step, ErrCodeMidtransLink, status,
			"missing payment challenge body=%s", shortBody(raw, 400))
	}
	return v.ChallengeID, v.ClientID, nil
}

// gopayPaymentProcess Step 14c：扣款落地，需 next_action=payment-success 才算成功。
//
// 注意：扣款副作用端点 —— transport 层仅在握手层失败时重试（请求没送达服务端），
// 不会产生重复扣款；服务端额外按 reference_id 做幂等校验，重发也安全。
func (c *Charger) gopayPaymentProcess(ctx context.Context, ref, pinToken string) error {
	const step = "gopay_payment_process"
	headers := gopayDefaultHeaders()
	urlStr := fmt.Sprintf("https://gwa.gopayapi.com/v1/payment/process?reference_id=%s", ref)
	body := map[string]any{
		"challenge": map[string]any{
			"type":  "GOPAY_PIN_CHALLENGE",
			"value": map[string]any{"pin_token": pinToken},
		},
	}
	status, raw, err := c.doJSON(ctx, c.ext, http.MethodPost, urlStr, body, headers, nil)
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "POST")
	}
	if status != http.StatusOK {
		return newErr(step, ErrCodeMidtransLink, status, "body=%s", shortBody(raw, 600))
	}
	var wrap gopayChallengeWrap
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return wrapErr(step, ErrCodeMidtransLink, status, err, "decode")
	}
	if !wrap.Success || wrap.Data.NextAction != "payment-success" {
		return newErr(step, ErrCodeMidtransLink, status,
			"unexpected next_action=%q body=%s", wrap.Data.NextAction, shortBody(raw, 300))
	}
	c.log("info", "[Plus 升级] GoPay 扣款完成")
	return nil
}

// ───── helpers ─────────────────────────────────────────────────────

// gopayDefaultHeaders gwa.gopayapi.com 接口必带的 Origin/Referer。
func gopayDefaultHeaders() http.Header {
	h := http.Header{}
	h.Set("Origin", GopayApiOrigin)
	h.Set("Referer", GopayApiReferer)
	return h
}

// boolField 安全取 bool 字段（不存在或非 bool 都返回 false）。
func boolField(m genericJSON, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		return false
	}
	return b
}

// secondsToDuration 浮点秒转 time.Duration。
func secondsToDuration(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}
