// Package gptauth 提供 GPT (OpenAI / ChatGPT) 账号 token 续期 + 配额查询能力。
//
// 三个外部 endpoint：
//
//  1. POST  https://auth.openai.com/oauth/token  (grant_type=refresh_token)
//     - 用 platform OAuth client (app_2SKx67Edpo...) 拿新 access_token / refresh_token / id_token
//
//  2. GET   https://chatgpt.com/backend-api/wham/usage
//     - Bearer access_token，返回 plan_type + 短/长窗口已用百分比 + 重置倒计时
//     - 注意：必须用 chatgpt.com hostname；api.openai.com 走的是另一套（rate-limit headers）
//
//  3. JWT decode access_token.exp / .https://api.openai.com/profile.email / .user_id
//     - 不用任何外部网络
//
// 设计要点：
//   - 所有外部调用都接受 ProxyURL 参数（号池每个号绑了不同代理）
//   - 不带 utls / 浏览器指纹（这两个 endpoint 不在 Cloudflare 严格保护下，标准 Go HTTP 即可）
//   - 失败信息精确分类（network / 401 / 429 / 5xx），便于 service 层决定是否 cooldown / invalid
//
// 参考：AlexANSO/gpt-codex-pool packages/cli/src/services/token-validator.ts
package gptauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 常量。platform 与 codex 共用 auth.openai.com,但 client_id 各自隔离。
const (
	authBase    = "https://auth.openai.com"
	chatGPTBase = "https://chatgpt.com"

	// PlatformClientID 是 platform.openai.com 的 OAuth client，用来 refresh
	// 注册阶段 platformLoginAndExchange 拿到的 RT。
	PlatformClientID = "app_2SKx67EdpoN0G6j64rFvigXD"

	// CodexClientID 是 Codex CLI 的 OAuth client，未来如果支持 codex token
	// silent refresh 用。
	CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// 标准 UA：用 Mac Chrome，跟 ref 项目一致，避免 wham/usage 把 Linux UA 视作可疑。
	defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// TokenSet refresh 后的新 token 三件套。
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresIn    int       // 秒
	ExpiresAt    time.Time // 绝对过期时间（now + ExpiresIn）
}

// Usage 是 wham/usage endpoint 解析后的结构。
//
// 字段对齐 ref `WhamUsageResponse`：
//
//   - PlanType        : free / plus / pro / team / enterprise / unknown
//   - PrimaryUsedPct  : 短窗口（5h）已用百分比 0~100
//   - PrimaryResetSec : 短窗口下次重置距现在多少秒
//   - SecondaryUsedPct: 长窗口（7d）已用百分比 0~100
//   - SecondaryResetSec: 长窗口下次重置距现在多少秒
//   - CodeReviewUsedPct: code review 短窗口已用百分比
type Usage struct {
	UserID            string  `json:"user_id"`
	AccountID         string  `json:"account_id"`
	Email             string  `json:"email"`
	PlanType          string  `json:"plan_type"`
	PrimaryUsedPct    float64 `json:"primary_used_pct"`
	PrimaryResetSec   int     `json:"primary_reset_sec"`
	SecondaryUsedPct  float64 `json:"secondary_used_pct"`
	SecondaryResetSec int     `json:"secondary_reset_sec"`
	CodeReviewUsedPct float64 `json:"code_review_used_pct"`
}

// JWTClaims 解出来的 access_token claims（按需取，不映射全部字段）。
type JWTClaims struct {
	Exp              int64
	Iat              int64
	UserID           string // https://api.openai.com/auth.user_id
	Email            string // https://api.openai.com/profile.email
	EmailVerified    bool   // https://api.openai.com/profile.email_verified
	ClientID         string // 解出来用来比对当前是 platform / codex
	Scope            []string
	PlanType         string // https://api.openai.com/auth.chatgpt_plan_type (free/plus/pro/team/enterprise)
	ChatGPTAccountID string // https://api.openai.com/auth.chatgpt_account_id
	ChatGPTUserID    string // https://api.openai.com/auth.chatgpt_user_id
}

// ExpiresAt 取 Exp 转 time.Time（UTC）。Exp=0 时返回零值。
func (c JWTClaims) ExpiresAt() time.Time {
	if c.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(c.Exp, 0).UTC()
}

// =================== HTTP client ===================

// httpClient 构造一个带可选代理的 *http.Client。
//
// timeout 为 0 时给个 30s 兜底，避免运维误传出现挂死。
func httpClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}
	if strings.TrimSpace(proxyURL) != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy url %q: %w", proxyURL, err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// =================== 1. RefreshAccessToken ===================

// RefreshAccessToken 用 refresh_token 换一组新的 AT/RT/IDT。
//
//   - clientID 留空时默认用 PlatformClientID（注册流程产物用的就是 platform client）
//   - proxyURL 留空 = 直连
//
// HTTP 200 才认为成功；其他状态原文抛出便于排错。
//
// 返回的 TokenSet 总是 non-nil（即使 ExpiresIn=0 也会带 AT/RT）。
func RefreshAccessToken(ctx context.Context, refreshToken, clientID, proxyURL string, timeout time.Duration) (*TokenSet, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("refresh_token 为空")
	}
	if clientID == "" {
		clientID = PlatformClientID
	}
	cli, err := httpClient(proxyURL, timeout)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, authBase+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", defaultUA)
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth/token 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth/token HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("oauth/token 响应非 JSON: %s", snippet(body))
	}
	if data.AccessToken == "" {
		return nil, fmt.Errorf("oauth/token 响应缺 access_token: %s", snippet(body))
	}
	now := time.Now().UTC()
	out := &TokenSet{
		AccessToken:  data.AccessToken,
		RefreshToken: strings.TrimSpace(data.RefreshToken),
		IDToken:      strings.TrimSpace(data.IDToken),
		ExpiresIn:    data.ExpiresIn,
	}
	if out.RefreshToken == "" {
		// OpenAI 偶尔不下发新 RT（沿用旧的）。
		out.RefreshToken = refreshToken
	}
	if data.ExpiresIn > 0 {
		out.ExpiresAt = now.Add(time.Duration(data.ExpiresIn) * time.Second)
	} else if c, err := DecodeAccessToken(out.AccessToken); err == nil && c.Exp > 0 {
		// expires_in 缺失时从 JWT exp 兜底。
		out.ExpiresAt = c.ExpiresAt()
	}
	return out, nil
}

// =================== 2. FetchUsage ===================

// FetchUsage 调 chatgpt.com/backend-api/wham/usage 拿 plan_type + 配额。
//
// HTTP 200 才认为成功；401 时账号 token 已失效（service 层应置 invalid）。
//
// 返回 *Usage；Plan / 各 percent 字段允许为零值。
func FetchUsage(ctx context.Context, accessToken, proxyURL string, timeout time.Duration) (*Usage, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("access_token 为空")
	}
	cli, err := httpClient(proxyURL, timeout)
	if err != nil {
		return nil, err
	}
	// 加 _t 缓存 buster，跟 ref 一致。
	u := chatGPTBase + "/backend-api/wham/usage?_t=" + fmt.Sprint(time.Now().UnixMilli())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Oai-Client-Build-Number", "5298191")
	req.Header.Set("Oai-Client-Version", "prod")
	req.Header.Set("Oai-Language", "en-US")
	req.Header.Set("Referer", chatGPTBase+"/codex/settings/usage")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", defaultUA)

	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wham/usage 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("wham/usage HTTP 401 (token 失效或被吊销): %s", snippet(body))
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("wham/usage HTTP 429 (限流): %s", snippet(body))
	}
	if resp.StatusCode == http.StatusForbidden {
		low := strings.ToLower(string(body))
		if strings.Contains(low, "<html") || strings.Contains(low, "cloudflare") ||
			strings.Contains(low, "just a moment") || strings.Contains(low, "cf-ray") {
			return nil, fmt.Errorf(
				"wham/usage HTTP 403（Cloudflare/WAF 拦截，返回了网页而非接口 JSON）。" +
					"多为出口代理 IP 被风控或请求像机器人。可换与账号地区一致的干净代理、" +
					"降低刷新频率或隔一段时间再试；账号 token 未必失效")
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wham/usage HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var raw struct {
		UserID    string `json:"user_id"`
		AccountID string `json:"account_id"`
		Email     string `json:"email"`
		PlanType  string `json:"plan_type"`
		RateLimit struct {
			Allowed         bool `json:"allowed"`
			LimitReached    bool `json:"limit_reached"`
			PrimaryWindow   struct {
				UsedPercent       float64 `json:"used_percent"`
				ResetAfterSeconds int     `json:"reset_after_seconds"`
			} `json:"primary_window"`
			SecondaryWindow struct {
				UsedPercent       float64 `json:"used_percent"`
				ResetAfterSeconds int     `json:"reset_after_seconds"`
			} `json:"secondary_window"`
		} `json:"rate_limit"`
		CodeReviewRateLimit struct {
			PrimaryWindow struct {
				UsedPercent       float64 `json:"used_percent"`
				ResetAfterSeconds int     `json:"reset_after_seconds"`
			} `json:"primary_window"`
		} `json:"code_review_rate_limit"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("wham/usage 响应非 JSON: %s", snippet(body))
	}
	plan := strings.TrimSpace(raw.PlanType)
	if plan == "" {
		plan = "unknown"
	}
	return &Usage{
		UserID:            raw.UserID,
		AccountID:         raw.AccountID,
		Email:             raw.Email,
		PlanType:          plan,
		PrimaryUsedPct:    raw.RateLimit.PrimaryWindow.UsedPercent,
		PrimaryResetSec:   raw.RateLimit.PrimaryWindow.ResetAfterSeconds,
		SecondaryUsedPct:  raw.RateLimit.SecondaryWindow.UsedPercent,
		SecondaryResetSec: raw.RateLimit.SecondaryWindow.ResetAfterSeconds,
		CodeReviewUsedPct: raw.CodeReviewRateLimit.PrimaryWindow.UsedPercent,
	}, nil
}

// =================== 3. DecodeAccessToken (本地 JWT 解析) ===================

// DecodeAccessToken 解 access_token JWT（不验签）拿核心 claims。
//
// 主要用途：
//   - 拿 exp 算 token 真实剩余时间（即便 OAuth response 没给 expires_in）
//   - 拿 email / user_id 做账号画像
//
// 任何错误（格式不对 / base64 失败 / JSON 解析失败）都会返回 error。
func DecodeAccessToken(token string) (*JWTClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT (parts=%d)", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// 兼容标准 base64（带 padding）的 token。
		payload, err = base64.StdEncoding.DecodeString(addBase64Padding(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("payload base64: %w", err)
		}
	}
	// 用 map 解析以处理 OpenAI 的 namespaced claims（带 URL 前缀的 key）。
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("payload json: %w", err)
	}
	out := &JWTClaims{}
	if v, ok := raw["exp"].(float64); ok {
		out.Exp = int64(v)
	}
	if v, ok := raw["iat"].(float64); ok {
		out.Iat = int64(v)
	}
	if v, ok := raw["client_id"].(string); ok {
		out.ClientID = v
	}
	if scopes, ok := raw["scp"].([]any); ok {
		for _, s := range scopes {
			if str, ok := s.(string); ok {
				out.Scope = append(out.Scope, str)
			}
		}
	}
	if auth, ok := raw["https://api.openai.com/auth"].(map[string]any); ok {
		if v, ok := auth["user_id"].(string); ok {
			out.UserID = v
		}
		if v, ok := auth["chatgpt_plan_type"].(string); ok {
			out.PlanType = v
		}
		if v, ok := auth["chatgpt_account_id"].(string); ok {
			out.ChatGPTAccountID = v
		}
		if v, ok := auth["chatgpt_user_id"].(string); ok {
			out.ChatGPTUserID = v
		}
	}
	if profile, ok := raw["https://api.openai.com/profile"].(map[string]any); ok {
		if v, ok := profile["email"].(string); ok {
			out.Email = v
		}
		if v, ok := profile["email_verified"].(bool); ok {
			out.EmailVerified = v
		}
	}
	return out, nil
}

func addBase64Padding(s string) string {
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return s
}

func snippet(b []byte) string {
	const max = 300
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
