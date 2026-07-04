// Package xairefresh 实现官方 xAI（Grok CLI）OAuth：PKCE 授权 URL 构造、授权码换
// token、refresh_token 续期，以及 id_token 身份解析。
//
// 参考 router-for-me/CLIProxyAPI（internal/auth/xai）。OAuth 端点通过 OIDC discovery
// 动态解析（auth.x.ai/.well-known/openid-configuration）。
//
// 交互式登录（打开浏览器 + 本地 loopback 回调）由 cmd/xailogin 完成一次；服务端
// 只用 RefreshTokens 做非交互续期。
package xairefresh

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultAPIBaseURL xAI 官方 Responses / Videos API base。
	DefaultAPIBaseURL = "https://api.x.ai/v1"
	// Issuer xAI OAuth issuer。
	Issuer = "https://auth.x.ai"
	// DiscoveryURL OIDC discovery 端点。
	DiscoveryURL = Issuer + "/.well-known/openid-configuration"
	// ClientID 公开的 xAI Grok CLI OAuth client_id。
	ClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	// Scope xAI API 访问所需 scope 集。
	Scope = "openid profile email offline_access grok-cli:access api:access"
	// RedirectHost loopback 回调主机。
	RedirectHost = "127.0.0.1"
	// CallbackPort loopback 回调端口。
	CallbackPort = 56121
	// RedirectPath loopback 回调路径。
	RedirectPath = "/callback"
)

// RefreshLead access_token 过期前多久触发续期。
const RefreshLead = 5 * time.Minute

var (
	// ErrEmptyRefreshToken 缺少 refresh_token。
	ErrEmptyRefreshToken = errors.New("xairefresh: 缺少 refresh_token")
	// ErrTokenInvalid refresh_token 已失效（token 端点 400/401）。
	ErrTokenInvalid = errors.New("xairefresh: refresh_token 已失效")
	// ErrTransient 其他可恢复错误（5xx / 超时 / 网络）。
	ErrTransient = errors.New("xairefresh: 临时失败")
)

// PKCECodes PKCE verifier/challenge 对。
type PKCECodes struct {
	CodeVerifier  string
	CodeChallenge string
}

// Discovery OIDC discovery 解析出的 OAuth 端点。
type Discovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// TokenData OAuth token 数据。
type TokenData struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	ExpiresIn    int
	Expire       time.Time
	Email        string
	Subject      string
}

// Client xAI OAuth 客户端。proxyURL 留空走直连。
type Client struct {
	http *http.Client
}

// New 构造。proxyURL 形如 http://user:pass@host:port，留空直连。
func New(proxyURL string, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tr := &http.Transport{}
	if p := strings.TrimSpace(proxyURL); p != "" {
		u, err := url.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("xairefresh: bad proxy url: %w", err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &Client{http: &http.Client{Timeout: timeout, Transport: tr}}, nil
}

// GeneratePKCECodes 生成 PKCE verifier/challenge 对。
func GeneratePKCECodes() (*PKCECodes, error) {
	buf := make([]byte, 96)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("xairefresh pkce: %w", err)
	}
	verifier := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(buf)
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hash[:])
	return &PKCECodes{CodeVerifier: verifier, CodeChallenge: challenge}, nil
}

// RandomString 生成 URL-safe 随机串（state / nonce 用）。
func RandomString(n int) (string, error) {
	if n <= 0 {
		n = 32
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// AuthorizeURLParams 构造授权 URL 的参数。
type AuthorizeURLParams struct {
	AuthorizationEndpoint string
	RedirectURI           string
	CodeChallenge         string
	State                 string
	Nonce                 string
}

// BuildAuthorizeURL 构造浏览器授权 URL。
func BuildAuthorizeURL(p AuthorizeURLParams) (string, error) {
	endpoint, err := validateEndpoint(p.AuthorizationEndpoint, "authorization_endpoint")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(p.RedirectURI) == "" {
		return "", errors.New("xairefresh: redirect URI required")
	}
	if strings.TrimSpace(p.CodeChallenge) == "" {
		return "", errors.New("xairefresh: code challenge required")
	}
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {strings.TrimSpace(p.RedirectURI)},
		"scope":                 {Scope},
		"code_challenge":        {strings.TrimSpace(p.CodeChallenge)},
		"code_challenge_method": {"S256"},
		"state":                 {strings.TrimSpace(p.State)},
		"nonce":                 {strings.TrimSpace(p.Nonce)},
		"plan":                  {"generic"},
		"referrer":              {"kleinai"},
	}
	return endpoint + "?" + values.Encode(), nil
}

// Discover 通过 OIDC discovery 解析 OAuth 端点。
func (c *Client) Discover(ctx context.Context) (*Discovery, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DiscoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: discovery http %d: %s", ErrTransient, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload Discovery
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: parse discovery: %v", ErrTransient, err)
	}
	auth, err := validateEndpoint(payload.AuthorizationEndpoint, "authorization_endpoint")
	if err != nil {
		return nil, err
	}
	tok, err := validateEndpoint(payload.TokenEndpoint, "token_endpoint")
	if err != nil {
		return nil, err
	}
	return &Discovery{AuthorizationEndpoint: auth, TokenEndpoint: tok}, nil
}

// ExchangeCodeForTokens 用授权码换 token。
func (c *Client) ExchangeCodeForTokens(ctx context.Context, code, redirectURI, codeVerifier, tokenEndpoint string) (*TokenData, error) {
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("xairefresh: authorization code required")
	}
	if strings.TrimSpace(codeVerifier) == "" {
		return nil, errors.New("xairefresh: code verifier required")
	}
	if strings.TrimSpace(tokenEndpoint) == "" {
		d, err := c.Discover(ctx)
		if err != nil {
			return nil, err
		}
		tokenEndpoint = d.TokenEndpoint
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {strings.TrimSpace(code)},
		"redirect_uri":  {strings.TrimSpace(redirectURI)},
		"client_id":     {ClientID},
		"code_verifier": {strings.TrimSpace(codeVerifier)},
	}
	return c.postTokenForm(ctx, tokenEndpoint, form)
}

// RefreshTokens 用 refresh_token 换新 access_token（非交互）。
func (c *Client) RefreshTokens(ctx context.Context, refreshToken, tokenEndpoint string) (*TokenData, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, ErrEmptyRefreshToken
	}
	if strings.TrimSpace(tokenEndpoint) == "" {
		d, err := c.Discover(ctx)
		if err != nil {
			return nil, err
		}
		tokenEndpoint = d.TokenEndpoint
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {strings.TrimSpace(refreshToken)},
	}
	return c.postTokenForm(ctx, tokenEndpoint, form)
}

func (c *Client) postTokenForm(ctx context.Context, tokenEndpoint string, form url.Values) (*TokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(tokenEndpoint), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("%w: http %d: %s", ErrTokenInvalid, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: token http %d: %s", ErrTransient, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: parse token: %v", ErrTransient, err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("%w: missing access_token", ErrTransient)
	}
	email, subject := ParseJWTIdentity(payload.IDToken)
	expire := time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second).UTC()
	return &TokenData{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
		IDToken:      strings.TrimSpace(payload.IDToken),
		TokenType:    strings.TrimSpace(payload.TokenType),
		ExpiresIn:    payload.ExpiresIn,
		Expire:       expire,
		Email:        email,
		Subject:      subject,
	}, nil
}

// ParseJWTIdentity 从 JWT（id_token）解析 email / sub。失败返回空串。
func ParseJWTIdentity(token string) (email, subject string) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return "", ""
	}
	payload := parts[1]
	if pad := len(payload) % 4; pad > 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return "", ""
	}
	var claims map[string]any
	if err = json.Unmarshal(raw, &claims); err != nil {
		return "", ""
	}
	if v, ok := claims["email"].(string); ok {
		email = strings.TrimSpace(v)
	}
	if v, ok := claims["sub"].(string); ok {
		subject = strings.TrimSpace(v)
	}
	return email, subject
}

// ParseJWTTier 从 JWT（access_token）解析 xAI 限速档位 tier claim。
// 返回 -1 表示 token 里没有 tier（解析失败/缺字段）。tier 越高 RPS 越高，
// 由 xAI 按历史消费自动上调，是 API 侧唯一能拿到的「账号等级」信号。
func ParseJWTTier(token string) int {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return -1
	}
	payload := parts[1]
	if pad := len(payload) % 4; pad > 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return -1
	}
	var claims map[string]any
	if err = json.Unmarshal(raw, &claims); err != nil {
		return -1
	}
	if v, ok := claims["tier"].(float64); ok {
		return int(v)
	}
	return -1
}

func validateEndpoint(rawURL, field string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("xairefresh: discovery %s empty", field)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("xairefresh: discovery %s invalid: %w", field, err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("xairefresh: discovery %s must be https: %q", field, rawURL)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return "", fmt.Errorf("xairefresh: discovery %s host %q not on x.ai", field, host)
	}
	return rawURL, nil
}

// RedirectURI 默认 loopback 回调 URI。
func RedirectURI() string {
	return fmt.Sprintf("http://%s:%d%s", RedirectHost, CallbackPort, RedirectPath)
}
