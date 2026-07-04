package xairefresh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// BillingURL Grok Build CLI 的额度查询端点。
//
// 注意：不是 api.x.ai 也不是 management-api.x.ai（那两个对 OAuth token 返回 403/404），
// 而是 grok.com 的 CLI 代理域名。用登录拿到的 access_token 当 Bearer 即可读，
// 无需 Management Key（抓 CLIProxyAPI「刷新额度」流量实测确认）。
const BillingURL = "https://cli-chat-proxy.grok.com/v1/billing"

// Billing 账号额度快照。金额单位为「美分」（val/100 = 美元）。
type Billing struct {
	MonthlyLimitCents int64  `json:"monthly_limit_cents"` // 月度包含额度（如 20000=$200）
	UsedCents         int64  `json:"used_cents"`          // 本计费周期已用
	OnDemandCapCents  int64  `json:"on_demand_cap_cents"` // 按量付费封顶（如 500000=$5000）
	PeriodStart       string `json:"period_start"`
	PeriodEnd         string `json:"period_end"`
}

// FetchBilling 用 access_token 查询账号额度。
func (c *Client) FetchBilling(ctx context.Context, accessToken string) (*Billing, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, ErrEmptyRefreshToken
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BillingURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "Go-http-client/2.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xairefresh billing: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: billing %d", ErrTokenInvalid, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("xairefresh billing %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var body struct {
		Config struct {
			MonthlyLimit struct {
				Val int64 `json:"val"`
			} `json:"monthlyLimit"`
			Used struct {
				Val int64 `json:"val"`
			} `json:"used"`
			OnDemandCap struct {
				Val int64 `json:"val"`
			} `json:"onDemandCap"`
			BillingPeriodStart string `json:"billingPeriodStart"`
			BillingPeriodEnd   string `json:"billingPeriodEnd"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("xairefresh billing decode: %w", err)
	}
	return &Billing{
		MonthlyLimitCents: body.Config.MonthlyLimit.Val,
		UsedCents:         body.Config.Used.Val,
		OnDemandCapCents:  body.Config.OnDemandCap.Val,
		PeriodStart:       body.Config.BillingPeriodStart,
		PeriodEnd:         body.Config.BillingPeriodEnd,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
