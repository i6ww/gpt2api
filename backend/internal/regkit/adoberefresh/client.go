// Adobe IMS / Firefly HTTP 调用集合。
//
// 每个调用都接受一个 proxyURL（可空），通过共享 *http.Client 发请求；
// 客户端默认 30s 超时 + 标准浏览器 UA，确保对 Adobe 后端看起来"像浏览器"。
package adoberefresh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/regkit/mailbox"
)

// Adobe IMS / Firefly 端点常量。
//
// Python 参考实现 (newwork/token_refresh.py) 用的是同一组 URL：
//
//   - IMS_TOKEN_URL  : silent refresh — 拿 cookie 换新 access_token
//   - IMS_PROFILE_URL: 拿 displayName / userId
//   - CREDITS_URL    : Firefly 积分余额
const (
	IMSTokenURL       = "https://adobeid-na1.services.adobe.com/ims/check/v6/token?jslVersion=v2-v0.48.0-1-g1e322cb"
	IMSDeviceTokenURL = "https://ims-na1.adobelogin.com/ims/token/v4"
	IMSProfileURL     = "https://ims-na1.adobelogin.com/ims/profile/v1"
	CreditsURL        = "https://firefly.adobe.io/v1/credits/balance"

	// DefaultUserAgent 与 Python 实现保持一致。
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"

	// DefaultClientID firefly 网页前端用的 client_id；当账号没指定时回退到它。
	DefaultClientID = "clio-playground-web"

	// DefaultScope newbanana 实测可用的最小完整 scope 集（Python 版同款）。
	DefaultScope = "AdobeID,firefly_api,openid,pps.read,pps.write," +
		"additional_info.projectedProductContext,additional_info.ownerOrg," +
		"uds_read,uds_write,ab.manage,read_organizations," +
		"additional_info.roles,account_cluster.read,creative_production"

	// DefaultCreditsAPIKey x-api-key 头；与 client_id 同名（Adobe 在网页就这么写的）。
	DefaultCreditsAPIKey = "clio-playground-web"

	FFIOSClientID  = "FF-iOS"
	FFIOSUserAgent = "Firefly/26.10.0 (AdobeCreativeSDK 11.0.2434;Apple;iPhone;iOS;26.6)"
)

// PSWebClientID Photoshop Web 入口的 client_id（与 x-api-key 同名）。
const PSWebClientID = "PSWebApp1"

// PSWebScope Photoshop Web 入口铸 token 用的 scope（含 uds_write / tk_platform 等，
// 与 adobe2api 的 psweb_refresh_scope 对齐）。
const PSWebScope = "AdobeID,ab.manage,account_cluster.read,additional_info.ownerOrg," +
	"additional_info.roles,af_byof,creative_sdk,dii_lr_ml_unlimited," +
	"firefly_api,openid,pps.read,read_organizations,tk_platform," +
	"tk_platform_sync,uds_read,uds_write"

// RefreshOptions 单次刷新的覆盖项；可空字段全部用对应 Default*。
type RefreshOptions struct {
	ProxyURL      string        // http://user:pass@host:port，留空走直连
	ClientID      string        // 默认 DefaultClientID
	Scope         string        // 默认 DefaultScope
	CreditsAPIKey string        // 默认 DefaultCreditsAPIKey
	UserAgent     string        // 默认 DefaultUserAgent
	Origin        string        // Origin/Referer 站点（默认 https://firefly.adobe.com）
	Timeout       time.Duration // 单请求超时；默认 30s
}

// RefreshTokenResult silent refresh 结果。
type RefreshTokenResult struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"` // 秒
	ExpiresAt   int64  `json:"expires_at"` // 绝对 Unix 秒（从 JWT 或 ExpiresIn + now 推出）
}

// AccountInfo /ims/profile/v1 投影。
type AccountInfo struct {
	DisplayName string
	Email       string
	UserID      string
}

// httpClient 复用 mailbox 包里的代理感知 client，保证：
//   - http / https / socks5 / socks5h 都能 dial
//   - keepalive + 90s idle 复用连接
//
// 不放 utls 指纹是因为 Adobe IMS 后端只看 cookie + UA，TLS 指纹无所谓。
func httpClient(proxyURL string, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return mailbox.HTTPClientWithProxy(proxyURL, timeout)
}

// pickStr 取首个非空 trim 后的字符串。
func pickStr(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// applyDefaults 填充 opt 的零值字段。
func (o *RefreshOptions) applyDefaults() {
	o.ClientID = pickStr(o.ClientID, DefaultClientID)
	o.Scope = pickStr(o.Scope, DefaultScope)
	o.CreditsAPIKey = pickStr(o.CreditsAPIKey, DefaultCreditsAPIKey)
	o.UserAgent = pickStr(o.UserAgent, DefaultUserAgent)
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
}

// RefreshAccessTokenViaCookie POST /ims/check/v6/token：用 cookie 静默换新 access_token。
//
// cookie 必须是完整 Adobe IMS 会话（注册流程结束后从 jar 序列化出来；导入用户也可以
// 在前端粘贴 'Cookie:' 头里那串）。
//
// 错误：
//
//   - cookie 空 → 直接返回 ErrNoCookie
//   - HTTP 非 200 → "IMS refresh HTTP <code>: <body 摘要>"
//   - 200 但响应里没 access_token → 报错把 body 摘要带回
func RefreshAccessTokenViaCookie(ctx context.Context, cookie string, opt RefreshOptions) (*RefreshTokenResult, error) {
	if strings.TrimSpace(cookie) == "" {
		return nil, ErrNoCookie
	}
	opt.applyDefaults()
	form := url.Values{}
	form.Set("client_id", opt.ClientID)
	form.Set("guest_allowed", "true")
	form.Set("scope", opt.Scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, IMSTokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	origin := pickStr(opt.Origin, "https://firefly.adobe.com")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", opt.UserAgent)

	resp, err := httpClient(opt.ProxyURL, opt.Timeout).Do(req)
	if err != nil {
		return nil, fmt.Errorf("IMS refresh: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IMS refresh HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   any    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("IMS refresh: 响应非 JSON：%s", snippet(body))
	}
	if data.AccessToken == "" {
		return nil, fmt.Errorf("IMS refresh: 响应无 access_token：%s", snippet(body))
	}
	expiresIn := toInt64(data.ExpiresIn)
	// 同样自动归一毫秒
	if expiresIn > 86400*2 {
		expiresIn /= 1000
	}
	expAt := ExtractJWTExpiry(data.AccessToken)
	if expAt == 0 && expiresIn > 0 {
		expAt = time.Now().Unix() + expiresIn
	}
	return &RefreshTokenResult{
		AccessToken: data.AccessToken,
		ExpiresIn:   expiresIn,
		ExpiresAt:   expAt,
	}, nil
}

// MintPSWebToken 用账号 cookie 现铸一个 Photoshop Web 入口（PSWebApp1）的 access_token。
// 与普通 silent refresh 同一 IMS 端点，只是换 client_id / scope / Origin。
// 用于 adobe.submit_mode=psweb 时给 firefly generate-async 提供 PSWeb 身份。
func MintPSWebToken(ctx context.Context, cookie, proxyURL string, timeout time.Duration) (*RefreshTokenResult, error) {
	return RefreshAccessTokenViaCookie(ctx, cookie, RefreshOptions{
		ProxyURL: proxyURL,
		ClientID: PSWebClientID,
		Scope:    PSWebScope,
		Origin:   "https://photoshop.adobe.com",
		Timeout:  timeout,
	})
}

// RefreshAccessTokenViaDeviceToken 用 okad 产出的 FF-iOS device_token 免验证码换
// 新 access_token。Adobe IMS 要求 grant_type=device、device_token 和原始 device_id。
func RefreshAccessTokenViaDeviceToken(ctx context.Context, deviceToken, deviceID string, opt RefreshOptions) (*RefreshTokenResult, error) {
	deviceToken = strings.TrimSpace(deviceToken)
	deviceID = strings.TrimSpace(deviceID)
	if deviceToken == "" || deviceID == "" {
		return nil, errors.New("adoberefresh: device_token / device_id 为空，无法续期")
	}
	opt.applyDefaults()
	form := url.Values{}
	form.Set("client_id", FFIOSClientID)
	form.Set("grant_type", "device")
	form.Set("device_token", deviceToken)
	form.Set("device_id", deviceID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, IMSDeviceTokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("User-Agent", FFIOSUserAgent)
	req.Header.Set("x-ims-clientid", FFIOSClientID)

	resp, err := httpClient(opt.ProxyURL, opt.Timeout).Do(req)
	if err != nil {
		return nil, fmt.Errorf("IMS device refresh: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IMS device refresh HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   any    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("IMS device refresh: 响应非 JSON：%s", snippet(body))
	}
	if data.AccessToken == "" {
		return nil, fmt.Errorf("IMS device refresh: 响应无 access_token：%s", snippet(body))
	}
	expiresIn := toInt64(data.ExpiresIn)
	if expiresIn > 86400*2 {
		expiresIn /= 1000
	}
	expAt := ExtractJWTExpiry(data.AccessToken)
	if expAt == 0 && expiresIn > 0 {
		expAt = time.Now().Unix() + expiresIn
	}
	return &RefreshTokenResult{
		AccessToken: data.AccessToken,
		ExpiresIn:   expiresIn,
		ExpiresAt:   expAt,
	}, nil
}

// FetchAccountInfo GET /ims/profile/v1，从 access_token 拿 displayName + userId。
//
// 返回零值代表查询失败（Adobe 时常因为 token 刚换还没传播给 profile 端点而 401）。
func FetchAccountInfo(ctx context.Context, token string, opt RefreshOptions) AccountInfo {
	if strings.TrimSpace(token) == "" {
		return AccountInfo{}
	}
	opt.applyDefaults()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, IMSProfileURL, nil)
	if err != nil {
		return AccountInfo{}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", opt.UserAgent)
	resp, err := httpClient(opt.ProxyURL, opt.Timeout).Do(req)
	if err != nil {
		return AccountInfo{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AccountInfo{}
	}
	var data struct {
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
		UserID      string `json:"userId"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err := json.Unmarshal(body, &data); err != nil {
		return AccountInfo{}
	}
	return AccountInfo{
		DisplayName: data.DisplayName,
		Email:       data.Email,
		UserID:      data.UserID,
	}
}

// FetchCredits GET /v1/credits/balance：拿 Firefly 积分余额。
//
// 返回 -1 表示"问不到"（HTTP 错 / 解析失败 / token 失效），与 Python 参考实现保持
// 一致。返回 0 表示"问到了，刚好是 0"。
//
// accountID 留空时自动从 token 解 user_id。
func FetchCredits(ctx context.Context, token, accountID string, opt RefreshOptions) float64 {
	if strings.TrimSpace(token) == "" {
		return -1
	}
	opt.applyDefaults()
	if accountID == "" {
		accountID = ExtractAccountIDFromJWT(token)
	}
	if accountID == "" {
		return -1
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CreditsURL, nil)
	if err != nil {
		return -1
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-api-key", opt.CreditsAPIKey)
	req.Header.Set("x-account-id", accountID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", opt.UserAgent)
	resp, err := httpClient(opt.ProxyURL, opt.Timeout).Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	// Adobe shape: {"total": {"quota": {"available": N, ...}, ...}}
	var shape struct {
		Total struct {
			Quota struct {
				Available *float64 `json:"available"`
			} `json:"quota"`
		} `json:"total"`
		Balance *float64 `json:"balance"`
	}
	if err := json.Unmarshal(body, &shape); err != nil {
		return -1
	}
	if shape.Total.Quota.Available != nil {
		return *shape.Total.Quota.Available
	}
	if shape.Balance != nil {
		return *shape.Balance
	}
	return 0
}

// FullResult 一次完整刷新的结果（access_token + 过期 + 个人信息 + 积分）。
type FullResult struct {
	AccessToken string
	ExpiresAt   int64 // Unix 秒
	ExpiresIn   int64 // 秒
	UserID      string
	DisplayName string
	Email       string
	Credits     float64 // -1 = 问不到
}

// RefreshOne 用 cookie 串行跑一次完整刷新：
//
//  1. 静默换 token
//  2. 拿 profile 信息
//  3. 拿 credits 余额
//
// 任何一步出错都会返回非 nil error，但已成功拿到的字段会一并返回（callers 可以
// 选择部分写入数据库）。
func RefreshOne(ctx context.Context, cookie string, opt RefreshOptions) (*FullResult, error) {
	tok, err := RefreshAccessTokenViaCookie(ctx, cookie, opt)
	if err != nil {
		return nil, err
	}
	out := &FullResult{
		AccessToken: tok.AccessToken,
		ExpiresAt:   tok.ExpiresAt,
		ExpiresIn:   tok.ExpiresIn,
		Credits:     -1,
	}
	info := FetchAccountInfo(ctx, tok.AccessToken, opt)
	out.DisplayName = info.DisplayName
	out.Email = info.Email
	out.UserID = pickStr(info.UserID, ExtractAccountIDFromJWT(tok.AccessToken))
	out.Credits = FetchCredits(ctx, tok.AccessToken, out.UserID, opt)
	return out, nil
}

// RefreshOneViaDeviceToken 用 FF-iOS device_token 完整刷新：
//
//  1. device_token + device_id 换 access_token
//  2. 拿 profile 信息
//  3. 拿 credits 余额
func RefreshOneViaDeviceToken(ctx context.Context, deviceToken, deviceID string, opt RefreshOptions) (*FullResult, error) {
	tok, err := RefreshAccessTokenViaDeviceToken(ctx, deviceToken, deviceID, opt)
	if err != nil {
		return nil, err
	}
	out := &FullResult{
		AccessToken: tok.AccessToken,
		ExpiresAt:   tok.ExpiresAt,
		ExpiresIn:   tok.ExpiresIn,
		Credits:     -1,
	}
	info := FetchAccountInfo(ctx, tok.AccessToken, opt)
	out.DisplayName = info.DisplayName
	out.Email = info.Email
	out.UserID = pickStr(info.UserID, ExtractAccountIDFromJWT(tok.AccessToken))
	out.Credits = FetchCredits(ctx, tok.AccessToken, out.UserID, opt)
	return out, nil
}

// FetchOnly 用现有 token 直接拿 profile + credits（跳过换 token，便宜得多）。
//
// 用于"刚拿到 token，不需要再换；只需要补齐 user_id + credits"的场景，比如：
//
//   - 注册流程结束 1s 内
//   - UI 上"只刷新积分"按钮
func FetchOnly(ctx context.Context, token string, opt RefreshOptions) *FullResult {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	out := &FullResult{
		AccessToken: token,
		ExpiresAt:   ExtractJWTExpiry(token),
		Credits:     -1,
	}
	info := FetchAccountInfo(ctx, token, opt)
	out.DisplayName = info.DisplayName
	out.Email = info.Email
	out.UserID = pickStr(info.UserID, ExtractAccountIDFromJWT(token))
	out.Credits = FetchCredits(ctx, token, out.UserID, opt)
	return out
}

// ErrNoCookie cookie 为空时直接返回，不发请求。
var ErrNoCookie = errors.New("adoberefresh: cookie 为空，无法静默续期")

// snippet 截断 body 用于错误消息（避免巨型 HTML 把日志打爆）。
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// 编译期保证 bytes 包被引用（便于未来扩展二进制响应解析）
var _ = bytes.NewReader
