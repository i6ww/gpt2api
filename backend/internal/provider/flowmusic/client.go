package flowmusic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kleinai/backend/pkg/outbound"
)

// Config FlowMusic 上游配置。可由 env / 配置中心覆盖。
type Config struct {
	BaseURL                string        // https://www.flowmusic.app
	SupabaseBaseURL        string        // https://sb.flowmusic.app
	SupabaseAnonKey        string        // ⭐ refresh_token 刷新必需
	GoogleOAuthTokenURL    string        // https://oauth2.googleapis.com/token
	GoogleOAuthClientID    string        // 默认 1032626174130-...apps.googleusercontent.com
	GoogleOAuthClientSecret string       // 仅当 Google 报缺 secret 时配
	UpstreamTimeout        time.Duration // 普通请求超时
	StreamIdleTimeout      time.Duration // 流式空闲超时
}

const (
	defaultBaseURL             = "https://www.flowmusic.app"
	defaultSupabaseBaseURL     = "https://sb.flowmusic.app"
	defaultGoogleOAuthTokenURL = "https://oauth2.googleapis.com/token"
	defaultGoogleOAuthClientID = "1032626174130-533micbc9tgsei76mqhtguq07lpoe4je.apps.googleusercontent.com"
	// defaultSupabaseAnonKey 是 FlowMusic 前端公开发布的 Supabase anon key（非机密），
	// 作为 refresh_token 续期的兜底；可由 KLEIN_FLOWMUSIC_SUPABASE_ANON_KEY 覆盖。
	defaultSupabaseAnonKey = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6ImVkbmpjY3FjbWJ4ZWF4YmlkaW5yIiwicm9sZSI6ImFub24iLCJpYXQiOjE3NzE1NjEwNjQsImV4cCI6MjA4NzEzNzA2NH0.XCXSuL7Th1xHecfRrP0vAOFmKwJxwBqVFLu06SxtVzg"
	defaultUserAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome Safari"
)

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = defaultBaseURL
	}
	if strings.TrimSpace(c.SupabaseBaseURL) == "" {
		c.SupabaseBaseURL = defaultSupabaseBaseURL
	}
	if strings.TrimSpace(c.SupabaseAnonKey) == "" {
		c.SupabaseAnonKey = defaultSupabaseAnonKey
	}
	if strings.TrimSpace(c.GoogleOAuthTokenURL) == "" {
		c.GoogleOAuthTokenURL = defaultGoogleOAuthTokenURL
	}
	if strings.TrimSpace(c.GoogleOAuthClientID) == "" {
		c.GoogleOAuthClientID = defaultGoogleOAuthClientID
	}
	if c.UpstreamTimeout <= 0 {
		c.UpstreamTimeout = 120 * time.Second
	}
	if c.StreamIdleTimeout <= 0 {
		c.StreamIdleTimeout = 180 * time.Second
	}
	return c
}

// Credentials 业务请求与刷新所需的凭证集合。
type Credentials struct {
	RefreshToken         string // Supabase refresh token（刷新链起点，滚动更新）
	AccessToken          string // ⭐ Supabase JWT，业务接口真正用的 Bearer
	ProviderToken        string // Google OAuth access_token
	ProviderRefreshToken string // Google OAuth refresh token
	FlowBearer           string // FlowMusic 业务 bearer，兜底
	Cookies              string // sb-sb-auth-token.N 分片 cookie
	ProxyURL             string
	ExpiresAt            *time.Time
	LastRefreshResult    string
	Email                string
	Name                 string
}

// Client FlowMusic HTTP 客户端。
type Client struct {
	cfg         Config
	mu          sync.Mutex
	httpClients map[string]*http.Client
}

// NewClient 构造客户端。
func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg.withDefaults(), httpClients: make(map[string]*http.Client)}
}

// ---- 业务/流式类型 ----

type CreditInfo struct {
	CreditsRemaining float64
	TokensRemaining  float64
	SubscriptionTier string
}

type ConversationResult struct {
	JobID        string
	RawEvents    []string
	ClipIDs      []string
	OperationIDs []string
}

type ConversationStreamEvent struct {
	Event        string
	Data         string
	Status       string
	PartKind     string
	ToolName     string
	TextDelta    string
	TextContent  string
	ToolTitle    string
	SoundPrompt  string
	OperationIDs []string
	ClipIDs      []string
}

type ClipPollStatus struct {
	OperationID string
	Status      string
	Progress    any
	ClipIDs     []string
	Error       string
}

type ClipResult struct {
	ID              string
	Title           string
	AudioURL        string
	WavURL          string
	ImageURL        string
	VideoURL        string
	Lyrics          string
	LyricsID        string
	SoundPrompt     string
	OperationID     string
	OperationType   string
	DurationSeconds float64
	CreatedAt       string
}

type ConversationPart struct {
	Content  string `json:"content"`
	PartKind string `json:"part_kind"`
}

type ConversationClientContext struct {
	CurrentSongID      string         `json:"current_song_id,omitempty"`
	SongQueue          []any          `json:"song_queue"`
	SelectedModel      any            `json:"selected_model"`
	LyricsIDMap        map[string]any `json:"lyrics_id_map"`
	GhostwriterVersion string         `json:"ghostwriter_version"`
}

type ConversationRequest struct {
	Parts         []ConversationPart        `json:"parts"`
	ClientContext ConversationClientContext `json:"client_context"`
	ModelName     string                    `json:"model_name"`
	Mode          string                    `json:"mode"`
}

// ---- HTTP client ----

func (c *Client) getHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%s|%d", proxyURL, int64(timeout))
	if client, ok := c.httpClients[key]; ok {
		return client
	}
	client := c.buildHTTPClient(proxyURL, timeout)
	c.httpClients[key] = client
	return client
}

func (c *Client) buildHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return &http.Client{Timeout: timeout}
	}
	client, err := outbound.NewClient(outbound.Options{
		ProxyURL: proxyURL,
		Timeout:  timeout,
		Mode:     outbound.ModeUTLS,
		Profile:  outbound.ProfileChrome,
	})
	if err == nil {
		return client
	}
	return &http.Client{Timeout: timeout}
}

// buildStreamHTTPClient 构造流式专用 HTTP 客户端。
// 拨号 / TLS 握手仍用有限超时（避免坏代理长时间挂起），但整体
// http.Client.Timeout 必须置 0：SSE 读 body 可能持续数分钟，绝不能被
// outbound 默认的 30s 整体超时砍断（否则恒报 "Client.Timeout while reading body"）。
// 流式的取消改由空闲计时器（StreamIdleTimeout）与 genCtx 负责。
func (c *Client) buildStreamHTTPClient(proxyURL string) *http.Client {
	client := c.buildHTTPClient(proxyURL, 30*time.Second)
	client.Timeout = 0
	return client
}

func (c *Client) doJSON(ctx context.Context, creds *Credentials, method, reqURL string, body any, out any, mutate func(*http.Request)) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, reader)
	if err != nil {
		return err
	}
	if creds != nil {
		c.applyFlowHeaders(req, creds)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(req)
	}
	proxyURL := ""
	if creds != nil {
		proxyURL = creds.ProxyURL
	}
	resp, err := c.getHTTPClient(proxyURL, c.cfg.UpstreamTimeout).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &UpstreamHTTPError{Operation: method + " " + reqURL, StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

func (c *Client) applyFlowHeaders(req *http.Request, creds *Credentials) {
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", c.cfg.BaseURL)
	req.Header.Set("Referer", c.cfg.BaseURL+"/")
	req.Header.Set("User-Agent", defaultUserAgent)
	if creds == nil {
		return
	}
	// FlowMusic API 校验 Supabase JWT（cookie 里的 access_token）。
	// 优先用 cookie 解出的 JWT，再退回 AccessToken/FlowBearer。
	bearer := extractSupabaseJWT(creds.Cookies)
	if bearer == "" {
		bearer = normalizeBearerToken(firstNonEmpty(creds.AccessToken, creds.FlowBearer))
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie := strings.TrimSpace(creds.Cookies); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
}

// ---- 生成 4 步 ----

func (c *Client) StartConversation(ctx context.Context, creds Credentials, prompt, model string) (ConversationResult, error) {
	body := buildConversationRequest(prompt, model)
	var payload map[string]any
	if err := c.doJSON(ctx, &creds, http.MethodPost, c.cfg.BaseURL+"/__api/conversation", body, &payload, nil); err != nil {
		return ConversationResult{}, classifyHTTPError(err)
	}
	result := ConversationResult{}
	jobID := conversationJobID(payload)
	if jobID == "" {
		return ConversationResult{}, fmt.Errorf("upstream response missing job_id")
	}
	result.JobID = jobID
	result.OperationIDs = findOperationIDs(payload)
	result.ClipIDs = findClipIDs(payload)
	return result, nil
}

func (c *Client) StreamMessagesWithEvents(ctx context.Context, creds Credentials, jobID string, onEvent func(ConversationStreamEvent)) (ConversationResult, error) {
	result := ConversationResult{JobID: jobID}
	var cancel context.CancelFunc
	var idleTimedOut atomic.Bool
	idleTimeout := c.cfg.StreamIdleTimeout
	if idleTimeout > 0 {
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
	}
	reqURL := c.cfg.BaseURL + "/__api/messages/" + jobID + "/stream?last_id=0"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return result, err
	}
	c.applyFlowHeaders(req, &creds)
	client := c.buildStreamHTTPClient(creds.ProxyURL) // 流式：拨号有限超时、整体不超时
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return result, classifyHTTPError(&UpstreamHTTPError{Operation: "stream messages", StatusCode: resp.StatusCode, Body: string(body)})
	}
	var idleTimer *time.Timer
	if idleTimeout > 0 {
		idleTimer = time.AfterFunc(idleTimeout, func() {
			idleTimedOut.Store(true)
			cancel()
		})
		defer idleTimer.Stop()
	}
	resetIdleTimer := func() {
		if idleTimer == nil {
			return
		}
		if !idleTimer.Stop() && idleTimedOut.Load() {
			return
		}
		idleTimer.Reset(idleTimeout)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()
		resetIdleTimer()
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if data == "" {
			continue
		}
		result.RawEvents = append(result.RawEvents, data)
		beforeOps := len(result.OperationIDs)
		beforeClips := len(result.ClipIDs)
		collectIDs(data, &result)
		if onEvent != nil {
			event := parseConversationStreamEvent(currentEvent, data)
			for _, id := range event.OperationIDs {
				result.OperationIDs = appendUnique(result.OperationIDs, id)
			}
			for _, id := range event.ClipIDs {
				result.ClipIDs = appendUnique(result.ClipIDs, id)
			}
			event.OperationIDs = append([]string(nil), result.OperationIDs[beforeOps:]...)
			event.ClipIDs = append([]string(nil), result.ClipIDs[beforeClips:]...)
			onEvent(event)
		}
		if currentEvent == "final" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		if idleTimedOut.Load() {
			return result, fmt.Errorf("stream messages idle timeout after %s", idleTimeout)
		}
		return result, err
	}
	return result, nil
}

func (c *Client) PollClipsWithProgress(ctx context.Context, creds Credentials, ids []string, deadline time.Time, onStatus func(ClipPollStatus)) ([]string, error) {
	seen := map[string]struct{}{}
	var lastErr error
	reportedErrors := map[string]struct{}{}
	lastHeartbeat := time.Time{}
	for time.Now().Before(deadline) {
		for _, id := range ids {
			if id == "" {
				continue
			}
			var payload map[string]any
			err := c.doJSON(ctx, &creds, http.MethodGet, c.cfg.BaseURL+"/__api/audio-create-song-status/"+id, nil, &payload, nil)
			if err != nil {
				if IsAuthFailure(err) {
					return mapKeys(seen), classifyHTTPError(err)
				}
				_, reported := reportedErrors[id]
				if onStatus != nil && !reported {
					onStatus(ClipPollStatus{OperationID: id, Error: err.Error(), ClipIDs: mapKeys(seen)})
				}
				reportedErrors[id] = struct{}{}
				lastErr = err
				continue
			}
			status := ClipPollStatus{
				OperationID: id,
				Status:      firstNonEmpty(findString(payload, "status"), findString(payload, "state")),
				Progress:    findValue(payload, "progress"),
			}
			for _, clipID := range findClipIDs(payload) {
				seen[clipID] = struct{}{}
			}
			status.ClipIDs = mapKeys(seen)
			if onStatus != nil {
				onStatus(status)
			}
		}
		if len(seen) > 0 {
			return mapKeys(seen), nil
		}
		if onStatus != nil && time.Since(lastHeartbeat) >= 15*time.Second {
			onStatus(ClipPollStatus{Status: "waiting", ClipIDs: mapKeys(seen)})
			lastHeartbeat = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	if lastErr != nil {
		return mapKeys(seen), fmt.Errorf("poll clips timed out while waiting for upstream song status")
	}
	return mapKeys(seen), fmt.Errorf("poll clips timed out without clip ids")
}

func (c *Client) GetClips(ctx context.Context, creds Credentials, clipIDs []string) ([]ClipResult, error) {
	if len(clipIDs) == 0 {
		return nil, nil
	}
	body := map[string]any{"clip_ids": clipIDs}
	var payload map[string]any
	if err := c.doJSON(ctx, &creds, http.MethodPost, c.cfg.BaseURL+"/__api/clips", body, &payload, nil); err != nil {
		return nil, classifyHTTPError(err)
	}
	out := clipsFromPayload(payload)
	return orderClipsByIDs(out, clipIDs), nil
}

func (c *Client) GetCredits(ctx context.Context, creds Credentials) (CreditInfo, error) {
	var info CreditInfo
	var credits map[string]any
	if err := c.doJSON(ctx, &creds, http.MethodGet, c.cfg.BaseURL+"/__api/billing/credits", nil, &credits, nil); err != nil {
		return info, classifyHTTPError(err)
	}
	if data, ok := credits["data"].(map[string]any); ok {
		info.CreditsRemaining = getFloatMap(data, "credits_remaining")
		info.TokensRemaining = getFloatMap(data, "tokens_remaining")
	}
	var sub map[string]any
	if err := c.doJSON(ctx, &creds, http.MethodGet, c.cfg.BaseURL+"/__api/billing/subscription", nil, &sub, nil); err == nil {
		if data, ok := sub["data"].(map[string]any); ok {
			info.SubscriptionTier = getString(data, "subscription_tier")
		}
	}
	return info, nil
}

func getFloatMap(m map[string]any, key string) float64 {
	if v, ok := directValue(m, key); ok {
		if n, ok := numericValue(v); ok {
			return n
		}
	}
	return 0
}

// ---- Token 刷新链（供续期调度器调用）----

// RefreshSupabase 用 refresh_token 换新的 Supabase JWT（主路径）。
func (c *Client) RefreshSupabase(ctx context.Context, creds Credentials) (Credentials, error) {
	refreshToken := strings.TrimSpace(creds.RefreshToken)
	if refreshToken == "" {
		return creds, fmt.Errorf("refresh_token is empty")
	}
	if strings.TrimSpace(c.cfg.SupabaseAnonKey) == "" {
		return creds, fmt.Errorf("SUPABASE_ANON_KEY 未配置，无法刷新 Supabase token")
	}
	body := map[string]string{"refresh_token": refreshToken}
	var payload map[string]any
	if err := c.doJSON(ctx, &Credentials{ProxyURL: creds.ProxyURL}, http.MethodPost, c.cfg.SupabaseBaseURL+"/auth/v1/token?grant_type=refresh_token", body, &payload, func(req *http.Request) {
		req.Header.Set("apikey", c.cfg.SupabaseAnonKey)
		req.Header.Set("Authorization", "Bearer "+c.cfg.SupabaseAnonKey)
		req.Header.Set("X-Client-Info", "supabase-ssr/0.5.2")
		req.Header.Set("X-Supabase-Api-Version", "2024-01-01")
	}); err != nil {
		return creds, err
	}
	if rt := getString(payload, "refresh_token"); rt != "" {
		creds.RefreshToken = rt
	}
	supabasePT := getString(payload, "provider_token")
	supabasePRT := getString(payload, "provider_refresh_token")
	creds.ProviderToken = firstNonEmpty(supabasePT, creds.ProviderToken)
	creds.ProviderRefreshToken = firstNonEmpty(supabasePRT, creds.ProviderRefreshToken)
	if expiresAt := parseExpires(payload); expiresAt != nil {
		creds.ExpiresAt = expiresAt
	}
	if user, ok := payload["user"].(map[string]any); ok {
		if email := getString(user, "email"); email != "" {
			creds.Email = email
		}
		if metadata, ok := user["user_metadata"].(map[string]any); ok {
			if name := getString(metadata, "name"); name != "" {
				creds.Name = name
			}
		}
	}
	supabaseJWT := getString(payload, "access_token")
	if supabaseJWT != "" {
		creds.AccessToken = supabaseJWT
	}
	if creds.ProviderToken == "" {
		// 没 provider_token：JWT 仍可直接用于业务接口鉴权（doc 2.3），不强制失败。
		creds.LastRefreshResult = "supabase_refresh_jwt_only"
		creds.FlowBearer = supabaseJWT
		if cookieValue := buildSupabaseAuthCookie(payload, &creds); cookieValue != "" {
			creds.Cookies = cookieValue
		}
		return creds, nil
	}
	if supabasePT == "" && strings.TrimSpace(creds.ProviderRefreshToken) != "" {
		if secret := strings.TrimSpace(c.cfg.GoogleOAuthClientSecret); secret != "" {
			if refreshed, oauthErr := c.RefreshGoogleProviderToken(ctx, creds); oauthErr == nil {
				creds = refreshed
				supabasePT = creds.ProviderToken
			}
		}
	}
	flowBearer, saveErr := c.saveAndResolveFlowBearer(ctx, &creds, payload)
	if saveErr != nil {
		flowBearer = strings.TrimSpace(creds.ProviderToken)
		if supabasePT == "" {
			creds.LastRefreshResult = "supabase_refresh_no_provider_token"
		} else {
			creds.LastRefreshResult = "supabase_refresh_use_provider_token_directly"
		}
	} else if strings.TrimSpace(flowBearer) == "" {
		flowBearer = strings.TrimSpace(creds.ProviderToken)
		creds.LastRefreshResult = "supabase_refresh_use_provider_token_directly"
	} else {
		creds.LastRefreshResult = "supabase_refresh_and_flow_bearer_success"
	}
	creds.FlowBearer = flowBearer
	// AccessToken 存 Supabase JWT（业务接口校验它），而不是 provider token。
	if supabaseJWT != "" {
		creds.AccessToken = supabaseJWT
	} else {
		creds.AccessToken = flowBearer
	}
	return creds, nil
}

func (c *Client) saveAndResolveFlowBearer(ctx context.Context, creds *Credentials, supabasePayload map[string]any) (string, error) {
	cookieValue := buildSupabaseAuthCookie(supabasePayload, creds)
	if cookieValue != "" {
		creds.Cookies = cookieValue
	}
	// SaveGoogle 不带任何鉴权头
	headerCreds := &Credentials{ProxyURL: creds.ProxyURL}
	var savePayload map[string]any
	saveBody := map[string]string{
		"access_token":  strings.TrimSpace(creds.ProviderToken),
		"platform":      "web",
		"refresh_token": strings.TrimSpace(creds.ProviderRefreshToken),
	}
	if err := c.doJSON(ctx, headerCreds, http.MethodPost, c.cfg.BaseURL+"/__api/auth/google/save", saveBody, &savePayload, nil); err != nil {
		return "", err
	}
	if data, ok := savePayload["data"].(map[string]any); ok {
		return firstNonEmpty(getString(data, "access_token"), getString(data, "flow_bearer"), getString(data, "flow_access_token"), getString(data, "token")), nil
	}
	return firstNonEmpty(getString(savePayload, "access_token"), getString(savePayload, "flow_bearer"), getString(savePayload, "flow_access_token"), getString(savePayload, "token")), nil
}

// RefreshGoogleProviderToken 用 provider_refresh_token 刷 Google provider_token。
func (c *Client) RefreshGoogleProviderToken(ctx context.Context, creds Credentials) (Credentials, error) {
	refreshToken := strings.TrimSpace(creds.ProviderRefreshToken)
	if refreshToken == "" {
		return creds, fmt.Errorf("provider_refresh_token is empty")
	}
	clientID := strings.TrimSpace(c.cfg.GoogleOAuthClientID)
	if clientID == "" {
		return creds, fmt.Errorf("GOOGLE_OAUTH_CLIENT_ID is required")
	}
	tokenURL := strings.TrimSpace(c.cfg.GoogleOAuthTokenURL)
	if tokenURL == "" {
		tokenURL = defaultGoogleOAuthTokenURL
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	if secret := strings.TrimSpace(c.cfg.GoogleOAuthClientSecret); secret != "" {
		form.Set("client_secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return creds, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := c.buildHTTPClient(creds.ProxyURL, c.cfg.UpstreamTimeout).Do(req)
	if err != nil {
		return creds, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return creds, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return creds, &UpstreamHTTPError{Operation: http.MethodPost + " " + tokenURL, StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	var payload map[string]any
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &payload); err != nil {
			return creds, err
		}
	}
	providerToken := firstNonEmpty(getString(payload, "access_token"), getString(payload, "provider_token"))
	if providerToken == "" {
		return creds, fmt.Errorf("Google OAuth refresh returned empty access_token")
	}
	creds.ProviderToken = providerToken
	if nextRefreshToken := getString(payload, "refresh_token"); nextRefreshToken != "" {
		creds.ProviderRefreshToken = nextRefreshToken
	}
	creds.LastRefreshResult = "provider_refresh_token_google_refresh_success"
	return creds, nil
}

// RefreshFromCookies cookie 协议兜底刷新。
func (c *Client) RefreshFromCookies(ctx context.Context, creds Credentials) (Credentials, error) {
	if strings.TrimSpace(creds.Cookies) == "" {
		return creds, fmt.Errorf("cookies is empty")
	}
	var payload map[string]any
	headerCreds := creds
	headerCreds.FlowBearer = ""
	headerCreds.AccessToken = ""
	err := c.doJSON(ctx, &headerCreds, http.MethodGet, c.cfg.BaseURL+"/__api/auth/session", nil, &payload, nil)
	if err != nil {
		if supabaseJWT := extractSupabaseJWT(creds.Cookies); supabaseJWT != "" {
			creds.FlowBearer = supabaseJWT
			creds.AccessToken = supabaseJWT
			if _, testErr := c.GetCredits(ctx, creds); testErr == nil {
				creds.LastRefreshResult = "cookie_supabase_jwt_success"
				return creds, nil
			}
			if rt := extractRefreshTokenFromJWT(supabaseJWT); rt != "" {
				creds.RefreshToken = rt
				if refreshed, fallbackErr := c.RefreshSupabase(ctx, creds); fallbackErr == nil {
					return refreshed, nil
				}
			}
		}
		return creds, err
	}
	creds.RefreshToken = firstNonEmpty(findString(payload, "refresh_token"), creds.RefreshToken)
	sessionProviderToken := findString(payload, "provider_token")
	creds.ProviderToken = firstNonEmpty(sessionProviderToken, creds.ProviderToken)
	creds.ProviderRefreshToken = firstNonEmpty(findString(payload, "provider_refresh_token"), creds.ProviderRefreshToken)
	refreshedFlowBearer := firstNonEmpty(findString(payload, "flow_bearer"), findString(payload, "flow_access_token"))
	if refreshedFlowBearer != "" {
		creds.FlowBearer = refreshedFlowBearer
		creds.AccessToken = refreshedFlowBearer
	} else if strings.TrimSpace(sessionProviderToken) != "" {
		flowBearer, err := c.saveAndResolveFlowBearer(ctx, &creds, payload)
		if err != nil {
			return creds, fmt.Errorf("cookie session found provider_token but bearer update failed: %w", err)
		}
		refreshedFlowBearer = flowBearer
		creds.FlowBearer = refreshedFlowBearer
		creds.AccessToken = refreshedFlowBearer
	}
	if strings.TrimSpace(refreshedFlowBearer) == "" {
		return creds, fmt.Errorf("cookie session did not contain a bearer token")
	}
	creds.LastRefreshResult = "cookie_protocol_flow_bearer_success"
	if expires := parseExpires(payload); expires != nil {
		creds.ExpiresAt = expires
	}
	if email := findString(payload, "email"); email != "" {
		creds.Email = email
	}
	if name := findString(payload, "name"); name != "" {
		creds.Name = name
	}
	return creds, nil
}

// ---- cookie 编解码 ----

func buildSupabaseAuthCookie(payload map[string]any, creds *Credentials) string {
	sessionToken := getString(payload, "access_token")
	refreshToken := getString(payload, "refresh_token")
	if sessionToken == "" || refreshToken == "" {
		return ""
	}
	providerToken := firstNonEmpty(getString(payload, "provider_token"), creds.ProviderToken)
	providerRefreshToken := firstNonEmpty(getString(payload, "provider_refresh_token"), creds.ProviderRefreshToken)
	user, _ := payload["user"].(map[string]any)
	if user == nil {
		user = map[string]any{}
	}
	session := map[string]any{
		"access_token":           sessionToken,
		"token_type":             "bearer",
		"expires_in":             payload["expires_in"],
		"expires_at":             payload["expires_at"],
		"refresh_token":          refreshToken,
		"user":                   user,
		"provider_token":         providerToken,
		"provider_refresh_token": providerRefreshToken,
	}
	data, err := json.Marshal(session)
	if err != nil {
		return ""
	}
	const chunkSize = 4096
	fullValue := "base64-" + base64.StdEncoding.EncodeToString(data)
	var cookieParts []string
	idx := 0
	for i := 0; i < len(fullValue); i += chunkSize {
		end := i + chunkSize
		if end > len(fullValue) {
			end = len(fullValue)
		}
		cookieParts = append(cookieParts, fmt.Sprintf("sb-sb-auth-token.%d=%s", idx, fullValue[i:end]))
		idx++
	}
	return strings.Join(cookieParts, "; ")
}

func extractSupabaseJWT(cookieValue string) string {
	if cookieValue == "" {
		return ""
	}
	parts := map[string]string{}
	for _, part := range strings.Split(cookieValue, "; ") {
		if i := strings.IndexByte(part, '='); i > 0 {
			parts[part[:i]] = part[i+1:]
		}
	}
	var full string
	for i := 0; ; i++ {
		key := fmt.Sprintf("sb-sb-auth-token.%d", i)
		chunk, ok := parts[key]
		if !ok {
			break
		}
		full += chunk
	}
	if full == "" {
		return ""
	}
	full = strings.TrimPrefix(full, "base64-")
	pad := (4 - len(full)%4) % 4
	if pad > 0 {
		full += strings.Repeat("=", pad)
	}
	data, err := base64.StdEncoding.DecodeString(full)
	if err != nil {
		return ""
	}
	var session map[string]any
	if err := json.Unmarshal(data, &session); err != nil {
		return ""
	}
	token, _ := session["access_token"].(string)
	return token
}

func extractRefreshTokenFromJWT(jwt string) string {
	parts := strings.SplitN(jwt, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	pad := (4 - len(parts[1])%4) % 4
	raw := parts[1] + strings.Repeat("=", pad)
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// try URL encoding
		data, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var payload struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.RefreshToken)
}

func normalizeBearerToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Fields(value)
	if len(parts) >= 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(strings.Join(parts[1:], " "))
	}
	return value
}
