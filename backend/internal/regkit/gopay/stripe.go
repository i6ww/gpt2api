package gopay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// runStep2CreatePM POST api.stripe.com/v1/payment_methods (type=gopay)。
// billing_details 即使是 IDR 计划也接受 US 地址（HAR 验证）。
func (c *Charger) runStep2CreatePM(ctx context.Context) error {
	const step = "stripe_create_pm"
	form := url.Values{}
	form.Set("billing_details[name]", "John Doe")
	form.Set("billing_details[email]", "buyer@example.com")
	form.Set("billing_details[address][country]", "US")
	form.Set("billing_details[address][line1]", "3110 Sunset Boulevard")
	form.Set("billing_details[address][city]", "Los Angeles")
	form.Set("billing_details[address][postal_code]", "90026")
	form.Set("billing_details[address][state]", "CA")
	form.Set("type", "gopay")
	form.Set("client_attribution_metadata[checkout_session_id]", c.csID)
	form.Set("key", c.stripePK)

	headers := http.Header{}
	headers.Set("Origin", "https://js.stripe.com")
	headers.Set("Referer", "https://js.stripe.com/")

	status, raw, err := c.doForm(ctx, c.ext, http.MethodPost,
		"https://api.stripe.com/v1/payment_methods", form.Encode(), headers, nil)
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "POST payment_methods")
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return newErr(step, ErrCodeStripeConfirm, status, "create pm body=%s", shortBody(raw, 400))
	}
	var data genericJSON
	if err := json.Unmarshal(raw, &data); err != nil {
		return wrapErr(step, ErrCodeStripeConfirm, status, err, "decode pm response: %s", shortBody(raw, 200))
	}
	pmID := jsonString(data, "id")
	if len(pmID) < 3 || pmID[:3] != "pm_" {
		return newErr(step, ErrCodeStripeConfirm, status, "bad pm id=%q body=%s", pmID, shortBody(raw, 300))
	}
	c.pmID = pmID
	c.log("info", "[Plus 升级] Stripe 已创建支付方式")
	return nil
}

// runStep3StripeConfirm 调 /init 拿 init_checksum + total_summary.total，
// 校验 total == 0（仅支持 0 元试用 plus-1-month-free promo），然后调 /confirm。
//
// 严格对齐 gopay2codex 的 checkStripeInitTotal：账号没 0 元 promo 资格时直接
// fail-fast，不浪费后续 GoPay linking + WhatsApp OTP 资源。approve 这一步在
// OpenAI 服务端对"0 元 trial"的 region 校验比"正式订阅"宽松得多，是 JP/US
// 账号能跑通 GoPay 升级的关键前提。
func (c *Charger) runStep3StripeConfirm(ctx context.Context) error {
	initChecksum, total, err := c.stripeInit(ctx)
	if err != nil {
		return err
	}
	if total == 0 {
		c.log("info", "[Plus 升级] Stripe 校验通过：0 元试用 promo 可用")
	} else {
		c.log("warn", "[Plus 升级] Stripe 校验：账号无 0 元 promo 资格（总额=%d）", total)
	}
	if total != 0 {
		return newErr("stripe_init", ErrCodeUnrecoverable, 0,
			"该账号没拿到 0 元 promo（stripe total=%d，期望 0）。"+
				"OpenAI 在 createCheckout 时按 account.country 派发 promo —— "+
				"JP/US/EU 等主要市场的账号会派 plus-1-month-free，"+
				"KH/某些东南亚账号则只能看到 IDR 349,000 全价。"+
				"换个 country=JP/US/EU 注册的账号再试（看 probe_account 那条 log）。"+
				"这个不是代理 / 钱包的问题，不会消耗任何资源", total)
	}
	return c.stripeConfirm(ctx, initChecksum)
}

// stripeInit POST /payment_pages/{cs}/init → init_checksum + total_summary.total。
func (c *Charger) stripeInit(ctx context.Context) (string, int64, error) {
	const step = "stripe_init"
	form := url.Values{}
	form.Set("browser_locale", "en-US")
	form.Set("browser_timezone", "Asia/Shanghai")
	form.Set("elements_session_client[client_betas][0]", "custom_checkout_server_updates_1")
	form.Set("elements_session_client[client_betas][1]", "custom_checkout_manual_approval_1")
	form.Set("elements_session_client[elements_init_source]", "custom_checkout")
	form.Set("elements_session_client[referrer_host]", "chatgpt.com")
	form.Set("elements_session_client[stripe_js_id]", uuid.New().String())
	form.Set("elements_session_client[locale]", "en")
	form.Set("elements_session_client[is_aggregation_expected]", "false")
	form.Set("elements_options_client[stripe_js_locale]", "auto")
	form.Set("key", c.stripePK)

	headers := http.Header{}
	headers.Set("Origin", "https://js.stripe.com")
	headers.Set("Referer", "https://js.stripe.com/")

	urlStr := fmt.Sprintf("https://api.stripe.com/v1/payment_pages/%s/init", c.csID)
	status, raw, err := c.doForm(ctx, c.ext, http.MethodPost, urlStr, form.Encode(), headers, nil)
	if err != nil {
		return "", 0, wrapErr(step, ErrCodeNetwork, 0, err, "POST init")
	}
	if status != http.StatusOK {
		return "", 0, newErr(step, ErrCodeStripeConfirm, status, "init body=%s", shortBody(raw, 400))
	}
	var data genericJSON
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", 0, wrapErr(step, ErrCodeStripeConfirm, status, err, "decode init: %s", shortBody(raw, 200))
	}
	ic := jsonString(data, "init_checksum")
	if ic == "" {
		return "", 0, newErr(step, ErrCodeStripeConfirm, status, "no init_checksum body=%s", shortBody(raw, 300))
	}
	total := extractTotalSummary(data)
	return ic, total, nil
}

// extractTotalSummary 从 Stripe init 响应里取 total_summary.total（单位：分）。
// 找不到时返回 -1（区分"未读到字段"与"总额 0"），调用方按需处理。
func extractTotalSummary(data genericJSON) int64 {
	ts, ok := data["total_summary"].(map[string]any)
	if !ok {
		return -1
	}
	switch v := ts["total"].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		// 极少数情况 Stripe 把数字 stringify 了
		var n int64
		_, _ = fmt.Sscanf(v, "%d", &n)
		return n
	}
	return -1
}

// stripeConfirm POST /payment_pages/{cs}/confirm。
//
// Stripe 需要 return_url 才会把 checkout 推进到 requires_action（带 setup_intent）。
// js_checksum / rv_timestamp 这俩是反 bot 用的，没传 hCaptcha 保护的商户会拒。
func (c *Charger) stripeConfirm(ctx context.Context, initChecksum string) error {
	const step = "stripe_confirm"
	chatgptReturn := fmt.Sprintf(
		"https://chatgpt.com/checkout/verify?stripe_session_id=%s&processor_entity=openai_llc&plan_type=plus",
		c.csID,
	)
	returnURL := fmt.Sprintf(
		"https://checkout.stripe.com/c/pay/%s?returned_from_redirect=true&ui_mode=custom&return_url=%s",
		c.csID, url.QueryEscape(chatgptReturn),
	)

	form := url.Values{}
	form.Set("guid", uuidHex())
	form.Set("muid", uuidHex())
	form.Set("sid", uuidHex())
	form.Set("payment_method", c.pmID)
	form.Set("init_checksum", initChecksum)
	form.Set("version", c.stripeRT.Version)
	form.Set("expected_amount", "0")
	form.Set("expected_payment_method_type", "gopay")
	form.Set("return_url", returnURL)
	form.Set("elements_session_client[session_id]", "elements_session_"+uuidHex()[:11])
	form.Set("elements_session_client[locale]", "en")
	form.Set("elements_session_client[referrer_host]", "chatgpt.com")
	form.Set("elements_session_client[is_aggregation_expected]", "false")
	form.Set("client_attribution_metadata[client_session_id]", uuid.New().String())
	form.Set("client_attribution_metadata[merchant_integration_source]", "elements")
	form.Set("client_attribution_metadata[merchant_integration_subtype]", "payment-element")
	form.Set("client_attribution_metadata[payment_intent_creation_flow]", "deferred")
	form.Set("key", c.stripePK)
	if c.stripeRT.JSChecksum != "" {
		form.Set("js_checksum", c.stripeRT.JSChecksum)
	}
	if c.stripeRT.RVTimestamp != "" {
		form.Set("rv_timestamp", c.stripeRT.RVTimestamp)
	}
	// hosted 模式 Stripe 会启用 merchant terms of service 同意框，confirm 必须
	// 带上 consent[terms_of_service]=accepted；custom 模式没启用时 Stripe 会忽略
	// 多余字段，所以无条件带上是安全的。
	form.Set("consent[terms_of_service]", "accepted")

	headers := http.Header{}
	headers.Set("Origin", "https://js.stripe.com")
	headers.Set("Referer", "https://js.stripe.com/")

	urlStr := fmt.Sprintf("https://api.stripe.com/v1/payment_pages/%s/confirm", c.csID)
	status, raw, err := c.doForm(ctx, c.ext, http.MethodPost, urlStr, form.Encode(), headers, nil)
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "POST confirm")
	}
	// 兜底：如果 Stripe 仍然报 ToS 未接受，自动补 form 再 confirm 一次。
	// 理论上前面已经无条件带了 consent，这条分支只是防御性日志。
	if status == http.StatusBadRequest && bytesContainsFold(raw, []byte("terms of service")) {
		c.log("warn", "[Plus 升级] Stripe 商户条款未接受，正在自动补 consent 重试")
		form.Set("consent[terms_of_service]", "accepted")
		status, raw, err = c.doForm(ctx, c.ext, http.MethodPost, urlStr, form.Encode(), headers, nil)
		if err != nil {
			return wrapErr(step, ErrCodeNetwork, 0, err, "POST confirm retry")
		}
	}
	if status != http.StatusOK {
		return newErr(step, ErrCodeStripeConfirm, status, "confirm body=%s", shortBody(raw, 600))
	}
	var data genericJSON
	_ = json.Unmarshal(raw, &data)
	c.log("info", "[Plus 升级] Stripe 支付确认通过，等待 GoPay 跳转")
	return nil
}

// bytesContainsFold 是 strings.Contains + EqualFold 的字节版本，避免拷贝。
func bytesContainsFold(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	hs := strings.ToLower(string(haystack))
	nd := strings.ToLower(string(needle))
	return strings.Contains(hs, nd)
}

// runStep5ResolveSnapToken 轮询 payment_pages/{cs} 直到 setup_intent.requires_action，
// 然后跟 pm-redirects.stripe.com 拿 302 提取 Midtrans snap_token。
func (c *Charger) runStep5ResolveSnapToken(ctx context.Context) error {
	const step = "resolve_snap_token"

	sessID := "elements_session_" + uuidHex()[:11]
	jsID := uuid.New().String()
	q := url.Values{}
	q.Set("elements_session_client[client_betas][0]", "custom_checkout_server_updates_1")
	q.Set("elements_session_client[client_betas][1]", "custom_checkout_manual_approval_1")
	q.Set("elements_session_client[elements_init_source]", "custom_checkout")
	q.Set("elements_session_client[referrer_host]", "chatgpt.com")
	q.Set("elements_session_client[session_id]", sessID)
	q.Set("elements_session_client[stripe_js_id]", jsID)
	q.Set("elements_session_client[locale]", "en")
	q.Set("elements_session_client[is_aggregation_expected]", "false")
	q.Set("elements_options_client[stripe_js_locale]", "auto")
	q.Set("elements_options_client[saved_payment_method][enable_save]", "never")
	q.Set("elements_options_client[saved_payment_method][enable_redisplay]", "never")
	q.Set("key", c.stripePK)
	q.Set("_stripe_version", "2025-03-31.basil; checkout_server_update_beta=v1; checkout_manual_approval_preview=v1")
	urlStr := fmt.Sprintf("https://api.stripe.com/v1/payment_pages/%s?%s", c.csID, q.Encode())

	headers := http.Header{}
	headers.Set("Origin", "https://js.stripe.com")
	headers.Set("Referer", "https://js.stripe.com/")

	deadline := nowFunc().Add(SnapTokenWaitTimeout)
	var lastErr string

	for nowFunc().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		status, _, raw, err := c.doGET(ctx, c.ext, urlStr, headers, nil)
		if err != nil {
			lastErr = err.Error()
		} else if status == http.StatusOK {
			var payload stripePaymentPagesData
			if err := json.Unmarshal(raw, &payload); err == nil {
				if payload.SetupIntent.Status == "requires_action" {
					pmURL := payload.SetupIntent.NextAction.RedirectToURL.URL
					if pmURL != "" {
						snap, err := c.fetchPmRedirectSnapToken(ctx, pmURL)
						if err != nil {
							return err
						}
						c.snapToken = snap
						c.log("info", "[Plus 升级] 已获取 Midtrans 凭据，准备跳转 GoPay")
						return nil
					}
				}
				lastErr = fmt.Sprintf("setup_intent.status=%q payment_status=%q stripe.status=%q",
					payload.SetupIntent.Status, payload.PaymentStatus, payload.Status)
			} else {
				lastErr = fmt.Sprintf("decode payment_pages: %v", err)
			}
		} else {
			lastErr = fmt.Sprintf("http %d body=%s", status, shortBody(raw, 200))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeAfter(SnapTokenPollInterval):
		}
	}
	return newErr(step, ErrCodeStripeConfirm, 0, "snap_token resolution timeout: %s", lastErr)
}

var midtransSnapTokenRE = regexp.MustCompile(`app\.midtrans\.com/snap/v[14]/redirection/([a-f0-9-]{36})`)

// fetchPmRedirectSnapToken GET pm-redirects.stripe.com/authorize/...
// → 302 Location 含 app.midtrans.com/snap/v4/redirection/{uuid}。
func (c *Charger) fetchPmRedirectSnapToken(ctx context.Context, pmURL string) (string, error) {
	const step = "pm_redirects"
	headers := http.Header{}
	headers.Set("Origin", "https://js.stripe.com")
	headers.Set("Referer", "https://js.stripe.com/")
	status, location, raw, err := c.doRedirectGET(ctx, c.ext, pmURL, headers)
	if err != nil {
		return "", wrapErr(step, ErrCodeNetwork, 0, err, "GET pm-redirect")
	}
	if !isRedirect(status) {
		return "", newErr(step, ErrCodeUnrecoverable, status, "expected redirect body=%s", shortBody(raw, 300))
	}
	m := midtransSnapTokenRE.FindStringSubmatch(location)
	if len(m) != 2 {
		return "", newErr(step, ErrCodeUnrecoverable, status, "no midtrans snap token in Location=%q", location)
	}
	return m[1], nil
}

// uuidHex 生成 32 字符 hex（去掉 dash），用于 stripe.guid/muid/sid 等需要纯 hex 的地方。
func uuidHex() string {
	u := uuid.New()
	hex := make([]byte, 0, 32)
	for _, b := range u {
		const hexChars = "0123456789abcdef"
		hex = append(hex, hexChars[b>>4], hexChars[b&0xF])
	}
	return string(hex)
}
