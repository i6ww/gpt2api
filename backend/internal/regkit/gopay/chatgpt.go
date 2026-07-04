package gopay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// runStep1CreateCheckout POST chatgpt.com/backend-api/payments/checkout
// 拿 cs_live_xxx 的 checkout session id + hosted checkout_url。
//
// **hosted 模式**：`checkout_ui_mode="hosted"` 让 OpenAI 后端**代为**创建
// Stripe payment_method、setup_intent，并已经"内化"完成 chatgpt approve。
// 响应 `checkout_url` 是一个已 approve 状态的 hosted URL，跟随 redirect 链
// 直接落到 `pm-redirects.stripe.com`，提取 snap_token 进入 Midtrans 流程。
//
// 对比旧 custom 模式：
//   - custom 需要客户端自己跑 createPM / stripeConfirm / chatgptApprove，
//     而 approve 对 OAuth-only Bearer 调用会被 OpenAI 风控判 `result=blocked`，
//     必须有 `__Secure-next-auth.session-token` cookie 才能过。
//   - hosted 直接得到"approve 已完成"的 URL，**绕开 approve 风控完全不需要
//     web session cookie**。这是 OpenAI 给非浏览器客户端的官方支付路径。
//
// 代理通道：c.cs（Phase A = 账号注册国 IP，如 JP），保证 promo 资格判定一致。
func (c *Charger) runStep1CreateCheckout(ctx context.Context) error {
	const step = "chatgpt_create_checkout"
	body := map[string]any{
		"entry_point": "all_plans_pricing_modal",
		"plan_name":   "chatgptplusplan",
		"billing_details": map[string]any{
			"country":  "ID",
			"currency": "IDR",
		},
		"promo_campaign": map[string]any{
			"promo_campaign_id":          "plus-1-month-free",
			"is_coupon_from_query_param": false,
		},
		"checkout_ui_mode": "hosted",
		"cancel_url":       "https://chatgpt.com/#pricing",
	}
	headers := http.Header{}
	headers.Set("Origin", "https://chatgpt.com")
	headers.Set("Referer", "https://chatgpt.com/")

	status, raw, err := c.doJSON(ctx, c.cs, http.MethodPost,
		"https://chatgpt.com/backend-api/payments/checkout", body, headers, nil)
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "POST checkout")
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return newErr(step, ErrCodeUnrecoverable, status, "checkout create unexpected status, body=%s",
			shortBody(raw, 400))
	}
	var data genericJSON
	if err := json.Unmarshal(raw, &data); err != nil {
		return wrapErr(step, ErrCodeUnrecoverable, status, err, "decode checkout response: %s", shortBody(raw, 200))
	}
	csID := pickFirstNonEmpty(
		jsonString(data, "checkout_session_id"),
		jsonString(data, "session_id"),
		jsonString(data, "id"),
	)
	if csID == "" || (len(csID) < 3 || csID[:3] != "cs_") {
		return newErr(step, ErrCodeUnrecoverable, status, "bad checkout response cs_id=%q body=%s",
			csID, shortBody(raw, 300))
	}
	checkoutURL := pickFirstNonEmpty(
		jsonString(data, "checkout_url"),
		jsonString(data, "url"),
		jsonString(data, "openai_checkout_url"),
	)
	processorEntity := jsonString(data, "processor_entity")
	if processorEntity == "" {
		// 兜底：billing.country=ID 时 OpenAI 用 openai_ie 这一组 entity。
		processorEntity = "openai_ie"
	}
	if checkoutURL == "" && csID != "" && processorEntity != "" {
		// 部分 hosted 响应里只有 cs_id，URL 可通过 canonical 模板拼装。
		checkoutURL = "https://chatgpt.com/checkout/" + processorEntity + "/" + csID
	}
	if checkoutURL == "" {
		return newErr(step, ErrCodeUnrecoverable, status, "hosted checkout 缺 checkout_url，body=%s", shortBody(raw, 400))
	}
	c.csID = csID
	c.checkoutURL = checkoutURL
	c.processorEntity = processorEntity
	c.log("info", "[Plus 升级] 已生成 OpenAI 支付会话（hosted 模式）")
	return nil
}

// runStep4ChatGPTApprove POST chatgpt.com/backend-api/payments/checkout/approve。
// approve 之前先刷 sentinel/ping，否则部分账号 approve 过但 setup_intent 不创。
//
// 代理通道：c.cs（Phase A = ChatGPT 会话所用 IP，与 createCheckout 必须一致）。
//
// 历史踩坑：早期实现把这步切到 c.ext（印尼 IP）想"让 OpenAI 把 ID IP 当成支付
// 国家"，结果稳定返回 `result="blocked"`。对比 Python 原版 (CTF-pay/gopay.py
// `_chatgpt_approve`) 发现：原版整个 chatgpt.com 链路（createCheckout / sentinel
// ping / approve / verify）全部用 `self.cs` 同一个 session、同一个 IP；只有
// stripe / midtrans / gopay 域才切到 `self.ext`。OpenAI 后端 sentinel 在 Step 1
// 把 checkout session 绑定到 cs IP，Step 4 IP 跳变即 blocked。
//
// 结论：approve 必须和 Step 1 走同一通道（c.cs）。账号 region 由 createCheckout
// 时的 IP 决定（c.cs 国家），不是 approve 这一步能改的。
func (c *Charger) runStep4ChatGPTApprove(ctx context.Context) error {
	const step = "chatgpt_approve"

	// sentinel/ping 失败不阻塞，记 warn 即可（原 py 也是 best-effort）。
	c.chatGPTSentinelPing(ctx)

	body := map[string]any{
		"checkout_session_id": c.csID,
		"processor_entity":    "openai_llc",
	}
	// 对齐 Python 原版 _build_chatgpt_session 的 chatgpt.com 默认头：
	// oai-language / sec-fetch-* / Origin / Referer。这些 header browser.Client
	// 默认不会注入（只注 UA / sec-ch-ua-*），有些 OpenAI 后端风控规则会校验。
	headers := http.Header{}
	headers.Set("Origin", "https://chatgpt.com")
	headers.Set("Referer", "https://chatgpt.com/")
	headers.Set("oai-language", "en-US")
	headers.Set("sec-fetch-dest", "empty")
	headers.Set("sec-fetch-mode", "cors")
	headers.Set("sec-fetch-site", "same-origin")

	status, respHdr, raw, err := c.doJSONFull(ctx, c.cs, http.MethodPost,
		"https://chatgpt.com/backend-api/payments/checkout/approve", body, headers, nil)
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "POST approve")
	}
	if status != http.StatusOK && status != http.StatusCreated {
		c.logBlockedHeaders(step, respHdr)
		return newErr(step, ErrCodeChatGPTApprove, status, "approve unexpected status, body=%s",
			shortBody(raw, 400))
	}
	var data genericJSON
	_ = json.Unmarshal(raw, &data)
	if jsonString(data, "result") != "approved" {
		c.logBlockedHeaders(step, respHdr)
		return newErr(step, ErrCodeChatGPTApprove, status, "approve result=%q body=%s",
			jsonString(data, "result"), shortBody(raw, 300))
	}
	c.log("info", "[Plus 升级] OpenAI 已批准支付")
	return nil
}

// logBlockedHeaders dump 所有 response header 到任务日志，方便诊断"为什么 OpenAI
// 在没有任何 X-OpenAI-* 风控头的情况下返回 result=blocked"。
//
// 把 white-list 改成 black-list：除了那些纯 HTTP 标准头（Content-Type / Date /
// Vary 等）以外全部打印，这样任何 openai-* / x-* / cf-* 隐藏头都不会漏。
func (c *Charger) logBlockedHeaders(step string, h http.Header) {
	if h == nil {
		return
	}
	skip := map[string]struct{}{
		"Content-Type": {}, "Content-Length": {}, "Date": {},
		"Vary": {}, "Connection": {}, "Transfer-Encoding": {},
		"Accept-Ranges": {}, "Strict-Transport-Security": {},
		"Access-Control-Allow-Origin": {}, "Access-Control-Allow-Credentials": {},
		"Access-Control-Expose-Headers": {}, "Access-Control-Allow-Methods": {},
		"Access-Control-Allow-Headers": {}, "Access-Control-Max-Age": {},
	}
	pairs := make([]string, 0, 16)
	for k, vs := range h {
		if _, sk := skip[k]; sk {
			continue
		}
		for _, v := range vs {
			pairs = append(pairs, k+"="+v)
		}
	}
	if len(pairs) > 0 {
		c.log("error", "[gopay/%s] all response headers: %s", step, strings.Join(pairs, " | "))
	} else {
		c.log("error", "[gopay/%s] all response headers: <全空>", step)
	}
}

// chatGPTSentinelPing best-effort 触发 sentinel 刷新；任何错误都吞掉。
//
// 代理通道：c.cs（与 createCheckout / approve 一致）。chatgpt.com 域全部走
// 同一 session，避免 sentinel mismatch → result=blocked。
func (c *Charger) chatGPTSentinelPing(ctx context.Context) {
	headers := http.Header{}
	headers.Set("Origin", "https://chatgpt.com")
	headers.Set("Referer", "https://chatgpt.com/")
	if _, _, err := c.doJSON(ctx, c.cs, http.MethodPost,
		"https://chatgpt.com/backend-api/sentinel/ping", map[string]any{}, headers, nil); err != nil {
		c.log("warn", "[Plus 升级] OpenAI 风控预检跳过: %v", err)
	}
}

// runStep15VerifyChatGPT 轮询 chatgpt.com/checkout/verify 直到 200 或超时。
// 即使 verify 超时，账号通常也已经升级（GoPay 已扣款），dispatcher 仍可写库。
func (c *Charger) runStep15VerifyChatGPT(ctx context.Context) (bool, error) {
	if c.csID == "" {
		return false, newErr("chatgpt_verify", ErrCodeVerifyTimeout, 0, "missing cs_id")
	}
	deadline := nowFunc().Add(VerifyTimeout)
	for nowFunc().Before(deadline) {
		ok := c.chatGPTVerifyOnce(ctx)
		if ok {
			c.log("info", "[Plus 升级] OpenAI 已确认订阅生效")
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timeAfter(VerifyPollInterval):
		}
	}
	c.log("warn", "[Plus 升级] OpenAI 订阅状态确认超时（订阅大概率已生效，建议人工复核）")
	return false, nil
}

// chatGPTVerifyOnce 单次探测 verify endpoint。
func (c *Charger) chatGPTVerifyOnce(ctx context.Context) bool {
	q := url.Values{}
	q.Set("stripe_session_id", c.csID)
	q.Set("processor_entity", "openai_llc")
	q.Set("plan_type", "plus")
	urlStr := "https://chatgpt.com/checkout/verify?" + q.Encode()

	headers := http.Header{}
	headers.Set("Origin", "https://chatgpt.com")
	headers.Set("Referer", "https://chatgpt.com/")
	headers.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	status, _, _, err := c.doGET(ctx, c.cs, urlStr, headers, nil)
	if err != nil {
		c.log("warn", "[Plus 升级] OpenAI 订阅状态轮询异常: %v", err)
		return false
	}
	return status == http.StatusOK
}

// pickFirstNonEmpty 选第一个非空字符串。
func pickFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
