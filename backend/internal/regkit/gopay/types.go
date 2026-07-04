// Package gopay 实现 ChatGPT Plus 通过 GoPay 钱包扣款开通的纯 HTTP 流程。
//
// 流程总览（15 步，照着 plus_gopay_gptp-plus-main 项目 1:1 复刻）：
//
//	 1. POST chatgpt.com/backend-api/payments/checkout                     ← cs_live_xxx
//	 2. POST api.stripe.com/v1/payment_methods (type=gopay)                ← pm_xxx
//	 3. POST api.stripe.com/v1/payment_pages/{cs}/init  → init_checksum
//	    POST api.stripe.com/v1/payment_pages/{cs}/confirm                  ← status:open
//	 4. POST chatgpt.com/backend-api/payments/checkout/approve             ← approved
//	 5. GET  api.stripe.com/v1/payment_pages/{cs}  轮询 setup_intent.requires_action
//	    → 拿到 next_action.redirect_to_url.url (pm-redirects.stripe.com)
//	    GET  pm-redirects → 302 Location 提取 snap_token (Midtrans)
//	 6. GET  app.midtrans.com/snap/v1/transactions/{snap_token}            ← merchant info
//	 7. POST app.midtrans.com/snap/v3/accounts/{snap_token}/linking
//	    body: {type:gopay, country_code, phone_number}
//	    （第一次 406 → 冷却 12s → 第二次 201；最多重试 LINK_RETRY_LIMIT 次）
//	 8. POST gwa.gopayapi.com/v1/linking/validate-reference
//	 9. POST gwa.gopayapi.com/v1/linking/user-consent                      ← OTP 触发
//	 10. POST gwa.gopayapi.com/v1/linking/validate-otp                     ← challenge_id, client_id
//	 11. POST customer.gopayapi.com/api/v1/users/pin/tokens/nb             ← pin_token (JWT)
//	 12. POST gwa.gopayapi.com/v1/linking/validate-pin                     ← linking complete
//	 13. POST app.midtrans.com/snap/v2/transactions/{snap}/charge          ← charge_ref (A12...)
//	 14. GET  gwa.gopayapi.com/v1/payment/validate?reference_id=...
//	    POST gwa.gopayapi.com/v1/payment/confirm?reference_id=...          ← challenge2
//	    POST customer.gopayapi.com/.../tokens/nb                           ← pin_token2
//	    POST gwa.gopayapi.com/v1/payment/process?reference_id=...          ← settled
//	 15. GET  chatgpt.com/checkout/verify?stripe_session_id=...            ← Plus active
//
// 双 proxy 设计：
//
//	cs (Phase A: ChatGPT/Stripe)   走 CSProxy   通常用账号注册时的代理
//	ext (Phase B: Midtrans/GoPay)  走 ExtProxy  必须用印尼住宅 IP，否则风控
//
// dispatcher 调用入口：
//
//	charger, err := gopay.New(ctx, cfg)
//	result, err := charger.Run(ctx)
package gopay

import (
	"net/http"
	"time"
)

// 默认常量。所有可调参数都允许通过 Config 覆盖。
const (
	// DefaultMidtransClientID OpenAI 在 Midtrans 的公开 client id（embedded in JS，
	// 本身不是机密；如果未来轮换通过 Config.MidtransClientID 覆盖即可）。
	DefaultMidtransClientID = "Mid-client-3TX8nUa-f_RgNrky"

	// DefaultStripePublishableKey OpenAI 的 Stripe live PK（embedded in checkout JS）。
	DefaultStripePublishableKey = "pk_live_51HOrSwC6h1nxGoI3lTAgRjYVrz4dU3fVOabyCcKR3pbEJguCVAlqCxdxCUvoRh1XWwRacViovU3kLKvpkjh7IqkW00iXQsjo3n"

	// DefaultStripeVersion stripe.js 版本，confirm 时带上避免某些字段被拒。
	DefaultStripeVersion = "fed52f3bc6"

	// DefaultRequestTimeout 单次 HTTP 请求超时。
	DefaultRequestTimeout = 30 * time.Second

	// DefaultBrowserTimeout browser.Client 总超时（含连接）。
	DefaultBrowserTimeout = 60 * time.Second

	// SnapTokenWaitTimeout 轮询 setup_intent 拿 snap_token 的总超时（Stripe approve
	// 后 setup_intent 异步生成，typically 1-3s 但偶尔 10s+）。
	SnapTokenWaitTimeout = 60 * time.Second
	// SnapTokenPollInterval 上述轮询间隔。
	SnapTokenPollInterval = 1 * time.Second

	// LinkRetryLimit Midtrans linking 406 (account already linked) 最多重试次数。
	LinkRetryLimit = 2
	// LinkRetrySleep Midtrans 需要冷却 ~10s 才会让 406 → 201（实测）。
	LinkRetrySleep = 12 * time.Second
	// RateLimitMaxRetries Midtrans 429 (rate_limited) 独立的最大重试次数。
	// 跟 406 "already linked" 分开计数，因为 429 是出口 IP 频率限制（5-10 分钟
	// 窗口），需要更长等待。5 次 × 90s = 7.5 分钟，能覆盖大部分恢复时间。
	RateLimitMaxRetries = 5
	// RateLimitDefaultWait 429 时如果没配 RateLimitRetrySeconds 也没拿到
	// Retry-After 头，使用的默认等待秒数（90s = 1.5 分钟，避开短期限流）。
	RateLimitDefaultWait = 90.0

	// PaymentValidatePollAttempts charge 创建后 GoPay 后端要数秒才能 fetch；轮询次数。
	PaymentValidatePollAttempts = 8
	// PaymentValidatePollInterval 轮询间隔。
	PaymentValidatePollInterval = 1500 * time.Millisecond

	// VerifyTimeout chatgpt verify 总轮询超时（Plus 激活通常 < 10s）。
	VerifyTimeout = 60 * time.Second
	// VerifyPollInterval verify 轮询间隔。
	VerifyPollInterval = 2 * time.Second

	// ApproveMaxAttempts ChatGPT approve 端点对"首次绑卡的账号"会返回
	// result=blocked（OpenAI 反欺诈机制：无 web session cookie 的 API-only 调用
	// 在 fraud-score 还没攒够时会被风控）。实测同一账号 attempt 3-4 次后会放行。
	// 此处包含首次在内，默认最多 4 次：1+3 retries。
	ApproveMaxAttempts = 4
	// ApproveRetryBackoff 每次 retry 之间的固定等待。短时间内连发可能触发更强
	// 风控；间隔 30-60s 给 OpenAI fraud-pipeline 一些"消化"时间。
	ApproveRetryBackoff = 45 * time.Second
	// NetworkRetryBackoff 网络抖动（TLS EOF / connection reset / DNS）的 retry
	// 间隔。代理通道瞬时挂掉通常几秒就恢复，不需要等很久。
	NetworkRetryBackoff = 5 * time.Second

	// MidtransRefererTpl Midtrans linking 必带的 Referer。
	MidtransRefererTpl = "https://app.midtrans.com/snap/v4/redirection/%s"
	// GopayApiOrigin 所有 gwa.gopayapi.com 接口必带的 Origin。
	GopayApiOrigin = "https://merchants-gws-app.gopayapi.com"
	// GopayApiReferer 所有 gwa.gopayapi.com 接口必带的 Referer。
	GopayApiReferer = "https://merchants-gws-app.gopayapi.com/"

	// PinClientIDLink linking 阶段 PIN tokenize 用的 client_id (写死的 GoPay 公开值)。
	PinClientIDLink = "51b5f09a-3813-11ee-be56-0242ac120002-MGUPA"
	// PinClientIDCharge charge 阶段 PIN tokenize 用的 client_id。
	PinClientIDCharge = "47180a8e-f56e-11ed-a05b-0242ac120003-GWC"
)

// Config 创建 Charger 所需的全部参数。dispatcher 从 system_config + 池资源
// 拼装一份 Config 后传入即可。
type Config struct {
	// CSProxy Phase A（ChatGPT/Stripe）出口代理 URL，可空（直连）。
	// 推荐用注册时的代理保证 cookie 跟账号一致，避免触发风控。
	CSProxy string
	// ExtProxy Phase B（Midtrans/GoPay）出口代理 URL，**必须** 印尼/东南亚 IP。
	ExtProxy string

	// Auth ChatGPT 账号凭证。dispatcher 从 pool_gpt 拼装。
	Auth Auth

	// Wallet GoPay 钱包信息。
	Wallet Wallet

	// StripeRuntime 可选的 Stripe runtime fingerprint（HAR 抓的 js_checksum 等）。
	// 不传时只带 version，对大部分账号已够用。
	StripeRuntime StripeRuntime

	// MidtransClientID 默认 DefaultMidtransClientID。
	MidtransClientID string

	// OTPProvider WhatsApp OTP 取数器。生产用 GeeLarkOTPProvider，
	// 调试可用 StaticOTPProvider 或 ChannelOTPProvider。
	OTPProvider OTPProvider

	// Log 关键事件回调。dispatcher 接到 register_task_log 写 DB。
	// level 取 "info" / "warn" / "error"。
	Log func(level, msg string)

	// RequestTimeout 单次 HTTP 请求超时；0 走 DefaultRequestTimeout。
	RequestTimeout time.Duration
	// BrowserTimeout browser.Client 总体超时；0 走 DefaultBrowserTimeout。
	BrowserTimeout time.Duration

	// RateLimitStrategy 收到 429 时的策略：
	//   "retry" (默认): refresh proxy + sleep RateLimitRetrySeconds 重试
	//   "fail":          直接返回 GoPayRateLimited
	RateLimitStrategy string
	// RateLimitRetrySeconds 429 时基础等待秒数（实际取 max(本字段, Retry-After)）。
	RateLimitRetrySeconds float64
	// SuccessWaitSeconds payment process 200 后的"二次保险"等待，给 GoPay 内部
	// settlement 一些时间。0 表示不等。
	SuccessWaitSeconds float64

	// RefreshExtProxy 在 ExtProxy 触发 429/banned 时被回调，让 dispatcher 重新
	// 从 payment_proxy_pool 拿一个新代理塞回 charger.ext。返回新 URL 字符串；
	// 失败返回 error 时 charger 直接 fail。可空。
	RefreshExtProxy func() (string, error)
}

// Auth ChatGPT 账号凭证。
type Auth struct {
	// AccessToken Bearer token，写到 Authorization 头。
	AccessToken string
	// Cookies 直接拼成 Cookie 头（"name1=val1; name2=val2"）。
	// dispatcher 从 pool_gpt.cookies 列表拼接。
	Cookies string
	// DeviceID 写到 OAI-Device-Id 头，跟创建账号时的 sentinel 对齐。
	DeviceID string
	// UserAgent 强制使用的 UA（必须跟拿 cookie 时一致，否则 ChatGPT 风控）。
	// 留空时 browser.Client 用随机 Chrome UA（仅在测试 / 没记 UA 时）。
	UserAgent string
	// LanguageHeader Accept-Language；空走 "en-US,en;q=0.9"。
	LanguageHeader string
}

// Wallet GoPay 钱包信息。
type Wallet struct {
	// CountryCode 国家码不带 +（印尼 = "62"）。
	CountryCode string
	// PhoneNumber 手机号 E.164 不带 + 不带国家码（"838xxxxxxxx"）。
	PhoneNumber string
	// PIN GoPay 6 位 PIN（明文；dispatcher 调 service.ResolvePIN 拿到）。
	PIN string
}

// StripeRuntime Stripe.js 客户端运行时签名。
//
// `js_checksum` / `rv_timestamp` 这俩是 Stripe.js 防 bot 用的，没传也能跑，
// 但部分账号（hCaptcha-protected merchants）会卡在 confirm 400。
// 推荐从浏览器实测时抓 HAR 取一份塞进 system_config。
type StripeRuntime struct {
	PublishableKey string
	Version        string
	JSChecksum     string
	RVTimestamp    string
}

// Result Run() 成功返回。dispatcher 写 gopay_wallet_binding 用。
type Result struct {
	// State 通常 "succeeded"；可能值见 ResultState* 常量。
	State string `json:"state"`
	// CSID Stripe checkout session id，cs_live_xxx。
	CSID string `json:"cs_id"`
	// SnapToken Midtrans snap token。
	SnapToken string `json:"snap_token"`
	// ChargeRef Midtrans charge_ref（A12...），取消订阅时反查。
	ChargeRef string `json:"charge_ref"`
	// AmountIDR 本次扣款金额（IDR cents）。来自 Midtrans loadTransaction。
	AmountIDR int64 `json:"amount_idr"`
	// ChargedAt 扣款成功时间。
	ChargedAt time.Time `json:"charged_at"`
	// VerifyOK ChatGPT verify 是否在超时内成功。
	VerifyOK bool `json:"verify_ok"`
}

// ResultState* 业务状态。
const (
	ResultStateSucceeded     = "succeeded"
	ResultStateVerifyTimeout = "verify_timeout"
)

// midtransLoadTxData Midtrans /snap/v1/transactions/{snap} 的关键字段。
type midtransLoadTxData struct {
	EnabledPayments []struct {
		Type     string `json:"type"`
		Acquirer string `json:"acquirer"`
	} `json:"enabled_payments"`
	GrossAmount string `json:"gross_amount"` // 例如 "129000.00"
	Currency    string `json:"currency"`     // "IDR"
}

// stripePaymentPagesData 轮询 setup_intent 用的部分字段。
type stripePaymentPagesData struct {
	SetupIntent struct {
		Status     string `json:"status"`
		NextAction struct {
			RedirectToURL struct {
				URL string `json:"url"`
			} `json:"redirect_to_url"`
		} `json:"next_action"`
	} `json:"setup_intent"`
	PaymentStatus string `json:"payment_status"`
	Status        string `json:"status"`
}

// gopayChallengeAction 包装 GoPay 各种 challenge 响应里的 action.value 子结构。
//
// validate-otp / payment/confirm 都返回 data.challenge.action.value 里有
// challenge_id + client_id，PIN tokenize 时要用。
type gopayChallengeAction struct {
	ChallengeID string `json:"challenge_id"`
	ClientID    string `json:"client_id"`
}

// gopayChallengeWrap 通用包装。
type gopayChallengeWrap struct {
	Success bool `json:"success"`
	Data    struct {
		Challenge struct {
			Action struct {
				Value gopayChallengeAction `json:"value"`
			} `json:"action"`
		} `json:"challenge"`
		NextAction string `json:"next_action,omitempty"`
	} `json:"data"`
	ErrorMessages []string `json:"error_messages,omitempty"`
}

// 帮助类型：JSON 任意值响应。
type genericJSON map[string]any

// jsonString 安全取 string 字段。
func jsonString(m genericJSON, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key].(string)
	if !ok {
		return ""
	}
	return v
}

// 内部 helper：HTTP response 是否表示重定向。
func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}
