package gopay

import (
	"context"
	"net/http"
)

// warmupChatGPTSession 在 Step 1 createCheckout 之前预热 chatgpt.com web session。
//
// 背景：OpenAI 对 `/backend-api/payments/checkout/approve` 端点的"首次绑卡风控"
// 严格识别"调用方是否像真实浏览器 web 会话"。OAuth-only 调用（只有 Bearer
// access_token、没有 web cookies）几乎必然被风控判 `result="blocked"`，无论
// 短间隔 retry 还是跨小时 retry 都救不回来——参考 plus_gopay_gptp-plus-main/
// payment-adapter/CTF-pay/card.py 的 _warmup_chatgpt_checkout_context。
//
// 解法：在 approve 之前主动调用 chatgpt.com 一组"web session bootstrap"端点：
//
//   - GET /api/auth/session          ← NextAuth.js 把 access_token 换成 web session，
//                                      Set-Cookie: __Secure-next-auth.session-token
//   - GET /backend-api/accounts/check/v4-2023-04-27   ← 拉账户状态
//   - GET /backend-api/accounts/domain-density-eligibility  ← 触发 fraud check
//   - GET /backend-api/checkout_pricing_config/countries   ← pricing 上下文
//   - GET /backend-api/checkout_pricing_config/configs/ID
//
// 这些请求的响应里 Cloudflare / OpenAI 会通过 Set-Cookie 累积一组 cookie 到
// browser.Client.Jar（Go stdlib 自动）：session-token / cf_clearance / oai-*
// 等。后续 approve 调用时 stdlib 自动从 jar 拼出完整 Cookie 头，OpenAI 后端
// 看到的就是"真实 web 会话"，fraud-score 大幅下降。
//
// 全部 best-effort：任何一步失败都只记 warn，不阻断主流程（即使 warmup 没拿到
// session-token，approve 仍可尝试；只是成功率会降回 OAuth-only 水平）。
func (c *Charger) warmupChatGPTSession(ctx context.Context) {
	if c.cs == nil {
		return
	}

	const billingCountry = "ID"

	steps := []struct {
		name string
		url  string
	}{
		{"auth_session", "https://chatgpt.com/api/auth/session"},
		{"accounts_check", "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=0"},
		{"domain_density", "https://chatgpt.com/backend-api/accounts/domain-density-eligibility"},
		{"pricing_countries", "https://chatgpt.com/backend-api/checkout_pricing_config/countries"},
		{"pricing_config", "https://chatgpt.com/backend-api/checkout_pricing_config/configs/" + billingCountry},
	}

	// hosted 模式不调 chatgpt approve，session-token cookie 不再是必须；warmup 主要
	// 是为了 verify 步骤的 cf_bm / oai-did 等基础 cookie。所以这里跑成"静默模式"：
	// 全部成功不打日志，只有异常或非 200 状态码才输出 warn，减少正常流程的日志噪音。
	failures := 0
	for _, s := range steps {
		headers := http.Header{}
		headers.Set("Origin", "https://chatgpt.com")
		headers.Set("Referer", "https://chatgpt.com/")
		headers.Set("oai-language", "en-US")
		headers.Set("sec-fetch-dest", "empty")
		headers.Set("sec-fetch-mode", "cors")
		headers.Set("sec-fetch-site", "same-origin")

		status, _, _, err := c.doGET(ctx, c.cs, s.url, headers, nil)
		if err != nil {
			c.log("warn", "[gopay/warmup] %s 异常: %v", s.name, err)
			failures++
			continue
		}
		if status >= 400 {
			c.log("warn", "[gopay/warmup] %s → %d", s.name, status)
			failures++
		}
	}
	_ = failures // hosted 模式 warmup 是 best-effort，失败计数仅供未来扩展使用
}
