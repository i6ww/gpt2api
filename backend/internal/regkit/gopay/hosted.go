package gopay

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// runHostedToSnapToken hosted 模式跳过 Stripe createPM / confirm / chatgpt approve
// 三步，直接通过 hosted checkout URL 跟随 redirect chain 拿 pm-redirects.stripe.com
// URL，再 GET pm-redirects 不跟随 redirect，从 Location 头里抽 Midtrans snap_token。
//
// 这是 OpenAI 给"非浏览器客户端"的官方支付路径——OpenAI 后端在生成 hosted URL
// 时已经替我们做完了 chatgpt approve（创建 Stripe pm + setup_intent + 一切
// payment state），所以这条路**完全绕开** approve 的 fraud-score 风控，不需要
// `__Secure-next-auth.session-token` cookie。
//
// 流程：
//
//	GET https://chatgpt.com/checkout/openai_ie/cs_live_xxx
//	  → 302 → https://pm-redirects.stripe.com/authorize/{nonce}/{token}
//	GET https://pm-redirects.stripe.com/authorize/{nonce}/{token}  (no-follow)
//	  → 302 Location: https://app.midtrans.com/snap/v[14]/redirection/{snap_token}
//	提取 snap_token = UUID
//
// 全程走 c.cs（Phase A 代理，跟 hosted URL 生成时的 IP 一致）。pm-redirects
// 这一跳实际上是 Stripe 域，但用 c.cs 还是 c.ext 都可以，这里用 c.cs 保持
// session 一致。
var midtransRedirectRe = regexp.MustCompile(`app\.midtrans\.com/snap/v[14]/redirection/([a-f0-9-]{36})`)

func (c *Charger) runHostedToSnapToken(ctx context.Context) error {
	if c.checkoutURL == "" {
		return newErr("hosted_follow", ErrCodeUnrecoverable, 0, "missing checkout_url，请先跑 Step1")
	}

	// (1) GET hosted checkout URL，跟随 redirect 链直到落到 pm-redirects.stripe.com。
	pmURL, err := c.followHostedToPMRedirect(ctx)
	if err != nil {
		return err
	}
	c.pmRedirectURL = pmURL
	c.log("info", "[Plus 升级] OpenAI 已重定向至 GoPay 支付通道")

	// (2) GET pm-redirects（不跟随），从 Location 头里抽 snap_token。
	const step = "pm_redirect_snap"
	status, location, raw, err := c.doRedirectGET(ctx, c.cs, pmURL, http.Header{
		"Referer": []string{"https://chatgpt.com/"},
	})
	if err != nil {
		return wrapErr(step, ErrCodeNetwork, 0, err, "GET pm-redirects")
	}
	if !isRedirect(status) {
		return newErr(step, ErrCodeUnrecoverable, status, "pm-redirects 预期 3xx，实际 %d body=%s",
			status, shortBody(raw, 200))
	}
	m := midtransRedirectRe.FindStringSubmatch(location)
	if len(m) < 2 {
		return newErr(step, ErrCodeUnrecoverable, status, "pm-redirects Location 里没有 midtrans token: %q", location)
	}
	c.snapToken = m[1]
	c.log("info", "[Plus 升级] 已获取 Midtrans 凭据，准备跳转 GoPay")
	return nil
}

// followHostedToPMRedirect GET hosted checkout URL，**手动**跟随 redirect chain
// 直到落到 pm-redirects.stripe.com（一般 1-3 跳）。跟我们 doGET 默认跟随
// redirect 的行为相反——这里需要观察每一跳的 URL，及时停止避免误踩到 Stripe
// hosted page（那是 HTML 大页面，体积大且耗时）。
func (c *Charger) followHostedToPMRedirect(ctx context.Context) (string, error) {
	const step = "hosted_follow"
	current := c.checkoutURL
	const maxHops = 8
	for i := 0; i < maxHops; i++ {
		status, location, _, err := c.doRedirectGET(ctx, c.cs, current, http.Header{
			"Accept":  []string{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
			"Referer": []string{"https://chatgpt.com/"},
		})
		if err != nil {
			return "", wrapErr(step, ErrCodeNetwork, 0, err, "GET hosted hop=%d", i+1)
		}
		// 命中 pm-redirects.stripe.com 就停 —— current 已经是它，但也接受
		// Location 指过去的情况（部分账号 Stripe 直接把 cs 重定向到 pm-redirects）。
		if strings.Contains(current, "pm-redirects.stripe.com") {
			return current, nil
		}
		if strings.Contains(location, "pm-redirects.stripe.com") {
			return location, nil
		}
		if !isRedirect(status) {
			// 既不是 redirect 也不是 pm-redirects，说明 hosted page 落到 HTML 了
			// （可能是 stripe checkout hosted page）。这种情况下我们没办法从
			// HTML 里抽 pm-redirects URL —— 改用 setup_intent polling fallback。
			return "", newErr(step, ErrCodeUnrecoverable, status,
				"hosted chain 停在 non-redirect 页面，需要 setup_intent fallback: current=%s", maskURL(current))
		}
		if location == "" {
			return "", newErr(step, ErrCodeUnrecoverable, status,
				"hosted chain 拿到 %d 但 Location 空: current=%s", status, maskURL(current))
		}
		// 相对路径补齐。
		if strings.HasPrefix(location, "/") {
			// 取当前 host。
			i := strings.Index(current, "://")
			if i > 0 {
				rest := current[i+3:]
				j := strings.Index(rest, "/")
				if j > 0 {
					location = current[:i+3+j] + location
				} else {
					location = current[:i+3+len(rest)] + location
				}
			}
		}
		c.log("info", "[Plus 升级] hosted 跳转跟踪 %d/%d → 状态码 %d", i+1, maxHops, status)
		current = location
	}
	return "", fmt.Errorf("[gopay/%s] hosted chain 跑了 %d 跳还没落到 pm-redirects", step, maxHops)
}
