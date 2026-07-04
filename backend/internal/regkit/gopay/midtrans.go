package gopay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// runStep6LoadTransaction GET app.midtrans.com/snap/v1/transactions/{snap_token}。
// 顺便记录 enabled_payments / 金额。
func (c *Charger) runStep6LoadTransaction(ctx context.Context) error {
	const step = "midtrans_load_transaction"
	urlStr := fmt.Sprintf("https://app.midtrans.com/snap/v1/transactions/%s", c.snapToken)
	headers := http.Header{}
	headers.Set("x-source", "snap")
	headers.Set("x-source-app-type", "redirection")
	headers.Set("x-source-version", "2.3.0")
	headers.Set("Origin", "https://app.midtrans.com")
	headers.Set("Referer", fmt.Sprintf(MidtransRefererTpl, c.snapToken))

	status, _, raw, err := c.doGET(ctx, c.ext, urlStr, headers, nil)
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "GET load_transaction")
	}
	if status != http.StatusOK {
		return newErr(step, ErrCodeMidtransLink, status, "load_tx body=%s", shortBody(raw, 300))
	}
	var data midtransLoadTxData
	if err := json.Unmarshal(raw, &data); err != nil {
		return wrapErr(step, ErrCodeMidtransLink, status, err, "decode load_tx")
	}
	enabled := make([]string, 0, len(data.EnabledPayments))
	for _, p := range data.EnabledPayments {
		enabled = append(enabled, p.Type)
	}
	c.log("info", "[Plus 升级] Midtrans 已加载（支持 %v）", enabled)

	// 记下 grossAmount → amount cents。Midtrans 返回 "129000.00" 这种字符串。
	if data.GrossAmount != "" {
		// 简单 atoi（去掉小数）。IDR 没有小数。
		amount := data.GrossAmount
		if dot := lastIndex(amount, '.'); dot >= 0 {
			amount = amount[:dot]
		}
		if v, err := strconv.ParseInt(amount, 10, 64); err == nil {
			c.amountCents = v
			c.currency = data.Currency
		}
	}
	return nil
}

// midtransLinkingRefRE 从 activation_link_url 提取 reference UUID。
var midtransLinkingRefRE = regexp.MustCompile(`reference=([a-f0-9-]{36})`)

// linkingBypassBodyHints 命中即可触发「剥 Authorization 头重发」bypass，
// 经验值来自 DanOps-1/Gpt-Agreement-Payment 的 _midtrans_init_linking 实现：
// 部分 IP / 高频场景下，带 Authorization: Basic … 的请求会进入 SDK 风控分支，
// 而同 body / 同 endpoint 不带 Auth 的请求直接返回 201 + activation_link_url。
var linkingBypassBodyHints = []string{
	"technical error",
	"too many",
	"rate limit",
	"rate_limit",
}

// shouldBypassMidtransAuth 判定是否命中 Auth 头风控（429 或 body 关键字）。
func shouldBypassMidtransAuth(status int, raw []byte) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if len(raw) == 0 {
		return false
	}
	body := strings.ToLower(string(raw))
	for _, h := range linkingBypassBodyHints {
		if strings.Contains(body, h) {
			return true
		}
	}
	return false
}

// parseLinkingRef 从 Midtrans linking 响应里抽出 reference UUID；
// 200 / 201 才尝试解析，失败返回 ("", false)。201 + 无 ref 视为协议错误，
// 由调用方再处理（这里只负责"能不能拿到 ref"）。
func parseLinkingRef(raw []byte) (string, bool) {
	var data genericJSON
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", false
	}
	link := jsonString(data, "activation_link_url")
	m := midtransLinkingRefRE.FindStringSubmatch(link)
	if len(m) != 2 {
		return "", false
	}
	return m[1], true
}

// runStep7InitLinking POST snap/v3/accounts/{snap}/linking。
//
// 三种结果分开计数重试：
//   - 201：成功，返回 reference UUID。
//   - 406 "account already linked"：第一次几乎必 406；冷却 12s 后重试通常变 201。
//     最多 LinkRetryLimit (2) 次。
//   - 429 / "technical error" 风控：尝试 1 次「剥 Authorization 头」bypass；
//     bypass 还失败再走代理切换 + 退避（最多 RateLimitMaxRetries 次）。
//
// 把 406 / 429 / bypass 三条路径拆开后：池里只有 1 个 ID 代理时也能等到 Midtrans
// 限流窗口结束；并且对 Auth 头专属风控有快速绕过路径。
func (c *Charger) runStep7InitLinking(ctx context.Context) error {
	const step = "midtrans_init_linking"
	urlStr := fmt.Sprintf("https://app.midtrans.com/snap/v3/accounts/%s/linking", c.snapToken)
	body := map[string]any{
		"type":         "gopay",
		"country_code": c.cfg.Wallet.CountryCode,
		"phone_number": c.cfg.Wallet.PhoneNumber,
	}
	headers := http.Header{}
	headers.Set("Authorization", c.midtransBasicAuth())
	headers.Set("Origin", "https://app.midtrans.com")
	headers.Set("Referer", fmt.Sprintf(MidtransRefererTpl, c.snapToken))
	// bypassHeaders：同 endpoint / 同 body 但**不带 Authorization 头**，专门用于
	// 风控 bypass 一次性重发。
	bypassHeaders := http.Header{}
	bypassHeaders.Set("Origin", "https://app.midtrans.com")
	bypassHeaders.Set("Referer", fmt.Sprintf(MidtransRefererTpl, c.snapToken))

	var lastErr string
	link406Tries := 0
	rate429Tries := 0
	netRetries := 0
	bypassTried := false
	const maxNetRetries = 3 // TLS EOF / handshake failure 也允许有限次内部重试
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		status, raw, err := c.doJSON(ctx, c.ext, http.MethodPost, urlStr, body, headers, nil)
		if err != nil {
			// 网络/TLS 抖动（"tls handshake to app.midtrans.com failed: EOF"、connection
			// reset 等）—— 这其实是 Midtrans 限频的早期表现：服务端在收到 TLS Client
			// Hello 后直接 RST，而不是返回 429。**当成 429 重试**而不是立刻 fail，
			// 同样会触发 swap proxy + wait。这样能从"30s 一次失败立刻 abort"变成
			// 跟 429 一样的指数退避循环，显著提升成功率。
			isTLSish := isProbableRateLimitNetwork(err)
			if isTLSish && netRetries < maxNetRetries {
				netRetries++
				if c.cfg.RefreshExtProxy != nil {
					if newURL, refreshErr := c.cfg.RefreshExtProxy(); refreshErr != nil {
						c.log("warn", "[Plus 升级] 切换支付代理失败: %v", refreshErr)
					} else if newURL != "" {
						if swapErr := c.swapExtProxy(newURL); swapErr != nil {
							c.log("warn", "[Plus 升级] 替换支付代理通道异常: %v", swapErr)
						}
					}
				}
				wait := c.rateLimitWait("")
				c.log("warn", "[Plus 升级] GoPay 绑定网络异常（%d/%d）：%v；视为限流，%.0f 秒后重试",
					netRetries, maxNetRetries, err, wait.Seconds())
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timeAfter(wait):
				}
				continue
			}
			return wrapErr(step, ErrCodeNetwork, 0, err, "POST linking (406_tries=%d, 429_tries=%d, net_retries=%d)",
				link406Tries, rate429Tries, netRetries)
		}
		if status == http.StatusCreated {
			if ref, ok := parseLinkingRef(raw); ok {
				c.linkingReference = ref
				c.log("info", "[Plus 升级] GoPay 账号绑定请求已建立")
				return nil
			}
			return newErr(step, ErrCodeMidtransLink, status, "201 but no reference (body=%s)",
				shortBody(raw, 300))
		}
		// Auth 头风控 bypass：每个任务最多触发一次，命中即"剥 Authorization 头同 body 重发"。
		// 经验上 200 + body 含 "technical error" / 429 都属于这一类风控（不是 IP 限流），
		// 换代理无效；不带 Auth 的同 endpoint 请求会直接 201 + activation_link_url。
		if !bypassTried && shouldBypassMidtransAuth(status, raw) {
			bypassTried = true
			c.log("warn", "[Plus 升级] Midtrans linking 命中 Auth 头风控（status=%d），剥 Authorization 头重发一次", status)
			bstatus, braw, berr := c.doJSON(ctx, c.ext, http.MethodPost, urlStr, body, bypassHeaders, nil)
			if berr == nil && (bstatus == http.StatusCreated || bstatus == http.StatusOK) {
				if ref, ok := parseLinkingRef(braw); ok {
					c.linkingReference = ref
					c.log("info", "[Plus 升级] GoPay 账号绑定请求已建立（bypass）")
					return nil
				}
			}
			if berr != nil {
				c.log("warn", "[Plus 升级] Midtrans linking bypass 网络异常：%v；回退到换代理 + 退避重试", berr)
			} else {
				c.log("warn", "[Plus 升级] Midtrans linking bypass 未成功（status=%d body=%s）；回退到换代理 + 退避重试",
					bstatus, shortBody(braw, 200))
			}
			// 落到下面的 429 / default 分支继续处理（保留代理切换 + 等待退避兜底）。
		}
		if status == 406 {
			if link406Tries >= LinkRetryLimit {
				return newErr(step, ErrCodeMidtransLink, 406, "linking 406 exhausted (%d): %s",
					LinkRetryLimit, lastErr)
			}
			link406Tries++
			lastErr = parseMidtransErr(raw)
			c.log("warn", "[Plus 升级] GoPay 绑定冲突（%s），冷却 %.0f 秒后重试 %d/%d",
				lastErr, LinkRetrySleep.Seconds(), link406Tries, LinkRetryLimit)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timeAfter(LinkRetrySleep):
			}
			continue
		}
		if status == http.StatusTooManyRequests {
			if c.cfg.RateLimitStrategy == "fail" {
				return newErr(step, ErrCodeRateLimited, status, "GoPay linking rate limited")
			}
			if rate429Tries >= RateLimitMaxRetries {
				return newErr(step, ErrCodeMidtransLink, 429,
					"linking 429 exhausted (%d): rate_limited (代理出口 IP 被 Midtrans 限流，加新代理或换 IP 池)",
					RateLimitMaxRetries)
			}
			rate429Tries++
			// 默认 retry：尝试 refresh proxy（换不到也继续在原代理上等限流窗口结束）。
			if c.cfg.RefreshExtProxy != nil {
				if newURL, err := c.cfg.RefreshExtProxy(); err != nil {
					c.log("warn", "[Plus 升级] 切换支付代理失败: %v", err)
				} else if newURL != "" {
					if err := c.swapExtProxy(newURL); err != nil {
						c.log("warn", "[Plus 升级] 替换支付代理通道异常: %v", err)
					}
				}
			}
			wait := c.rateLimitWait("")
			lastErr = "rate_limited"
			c.log("warn", "[Plus 升级] GoPay 绑定被限流（%d/%d），%.0f 秒后重试",
				rate429Tries, RateLimitMaxRetries, wait.Seconds())
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timeAfter(wait):
			}
			continue
		}
		return newErr(step, ErrCodeMidtransLink, status, "unexpected status body=%s", shortBody(raw, 300))
	}
}

// runStep13MidtransCharge POST snap/v2/transactions/{snap}/charge → charge_ref。
//
// 偶发场景：钱包刚 linking 完，Midtrans 内部状态还没同步到 charge 通路，
// 接口虽然 200 OK 但 gopay_verification_link_url 为空字符串。短暂重试 2 次即可。
func (c *Charger) runStep13MidtransCharge(ctx context.Context) error {
	const step = "midtrans_charge"
	urlStr := fmt.Sprintf("https://app.midtrans.com/snap/v2/transactions/%s/charge", c.snapToken)
	body := map[string]any{
		"payment_type":  "gopay",
		"tokenization":  "true",
		"promo_details": nil,
	}
	headers := http.Header{}
	headers.Set("Authorization", c.midtransBasicAuth())
	headers.Set("Origin", "https://app.midtrans.com")
	headers.Set("Referer", fmt.Sprintf(MidtransRefererTpl, c.snapToken))

	chargeRefRE := regexp.MustCompile(`reference=([A-Za-z0-9]+)`)
	const maxTries = 3
	for attempt := 1; attempt <= maxTries; attempt++ {
		status, raw, err := c.doJSON(ctx, c.ext, http.MethodPost, urlStr, body, headers, nil)
		if err != nil {
			return wrapErr(step, ErrCodeNetwork, 0, err, "POST charge")
		}
		if status != http.StatusOK && status != http.StatusCreated {
			return newErr(step, ErrCodeMidtransLink, status, "charge body=%s", shortBody(raw, 400))
		}
		var data genericJSON
		if err := json.Unmarshal(raw, &data); err != nil {
			return wrapErr(step, ErrCodeMidtransLink, status, err, "decode charge")
		}
		// Midtrans 在 200 包装下用 body.status_code 表达逻辑结果：
		//   "200"/"201" = 成功；其它（404、406、412 …）= 风控/余额/限频拒绝。
		//   典型 status_message="Transaksi Anda ditolak. Silakan ulangi
		//   lagi atau coba dengan metode pembayaran lain." → 当前
		//   钱包+IP 组合被 Midtrans/GoPay 反欺诈拒绝，**当次**重试无意义。
		//   dispatcher 端把代理标 failed（出口 IP 大概率被打标），钱包/手机
		//   原样放回；用户单钱包池场景下不冷却钱包，避免 1 票全锁死。
		if bodyCode := jsonString(data, "status_code"); bodyCode != "" && bodyCode != "200" && bodyCode != "201" {
			bodyMsg := jsonString(data, "status_message")
			return newErr(step, ErrCodeChargeRejected, status,
				"charge rejected by Midtrans body_status=%s msg=%q（印尼语原义：交易被拒绝；常见原因：① GoPay 钱包余额不足 / ② 钱包被反欺诈打标 / ③ 支付代理出口 IP 被 Midtrans 风控）",
				bodyCode, bodyMsg)
		}
		// 优先按 gopay_verification_link_url 取（旧字段）；为空再回退到
		// deeplink_url / redirect_url / qr_code_url 等已知备选字段。
		link := jsonString(data, "gopay_verification_link_url")
		if link == "" {
			for _, k := range []string{"deeplink_url", "redirect_url", "qr_code_url"} {
				if v := jsonString(data, k); v != "" {
					link = v
					break
				}
			}
		}
		if m := chargeRefRE.FindStringSubmatch(link); len(m) == 2 {
			c.chargeRef = m[1]
			c.chargedAt = nowFunc()
			c.log("info", "[Plus 升级] GoPay 扣款已发起")
			return nil
		}
		if attempt < maxTries {
			c.log("warn", "[Plus 升级] Midtrans 扣款响应暂无 reference（第 %d/%d 次），1.5s 后重试", attempt, maxTries)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timeAfter(1500 * time.Millisecond):
			}
			continue
		}
		return newErr(step, ErrCodeMidtransLink, status,
			"charge: no reference (link=%q body=%s)", link, shortBody(raw, 400))
	}
	return newErr(step, ErrCodeMidtransLink, 0, "charge: unreachable")
}

// swapExtProxy 在 429 触发 RefreshExtProxy 后换掉 ext browser。返回 error 时
// 调用方应放弃（保留旧的）。
//
// 注意：换掉 client 不会自动让 Midtrans 解锁——Midtrans 限的是出口 IP，新
// proxy 必须有不同的出口 IP 才有效。调用方应当配合 ExcludeIDs 机制（在
// dispatcher 层）避免又换回同一条 banned 代理。
func (c *Charger) swapExtProxy(proxyURL string) error {
	browserTimeout := c.cfg.BrowserTimeout
	if browserTimeout <= 0 {
		browserTimeout = DefaultBrowserTimeout
	}
	cli, err := newExtBrowser(proxyURL, browserTimeout)
	if err != nil {
		return err
	}
	c.ext = cli
	c.cfg.ExtProxy = proxyURL
	c.log("info", "[Plus 升级] 已切换支付代理通道（%s）", maskedProxy(proxyURL))
	return nil
}

// parseMidtransErr 从 406 响应里取人读 error_messages[0]，否则截断 body。
func parseMidtransErr(raw []byte) string {
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr[0]
	}
	var obj genericJSON
	if err := json.Unmarshal(raw, &obj); err == nil {
		if v, ok := obj["error_messages"]; ok {
			if list, ok := v.([]any); ok && len(list) > 0 {
				if s, ok := list[0].(string); ok {
					return s
				}
			}
		}
	}
	return shortBody(raw, 120)
}

// lastIndex 内嵌一份避免引 strings 仅为找一个字符。
func lastIndex(s string, ch byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ch {
			return i
		}
	}
	return -1
}

