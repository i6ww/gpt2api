package firefly

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"go.uber.org/zap"
)

const (
	imageSubmitURL = "https://firefly-3p.ff.adobe.io/v2/3p-images/generate-async"
	videoSubmitURL = "https://firefly-3p.ff.adobe.io/v2/3p-videos/generate-async"
	imageUploadURL = "https://firefly-3p.ff.adobe.io/v2/storage/image"
)

// LogEntry 单次上游 HTTP 调用的诊断记录。
type LogEntry struct {
	Time         time.Time         `json:"time"`
	Phase        string            `json:"phase"`
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	StatusCode   int               `json:"status_code"`
	RequestSize  int               `json:"req_size,omitempty"`
	RequestBody  string            `json:"req_body,omitempty"`
	ResponseBody string            `json:"resp_body,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	DurationMs   int64             `json:"duration_ms"`
	TaskStatus   string            `json:"task_status,omitempty"`
	Progress     int               `json:"progress,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// LogHook 可选的日志收集器（admin 调试用）。
type LogHook func(entry LogEntry)

type logHookKeyType struct{}

var logHookCtxKey = logHookKeyType{}

// WithLogHook ctx 注入 LogHook，client 在每次 HTTP 调用时回调。
func WithLogHook(ctx context.Context, hook LogHook) context.Context {
	return context.WithValue(ctx, logHookCtxKey, hook)
}

func getLogHook(ctx context.Context) LogHook {
	if hook, ok := ctx.Value(logHookCtxKey).(LogHook); ok {
		return hook
	}
	return nil
}

func truncBody(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}

// ProgressUpdate 轮询过程中的进度回调。
type ProgressUpdate struct {
	TaskStatus    string `json:"task_status"`
	TaskProgress  int    `json:"task_progress"`
	UpstreamJobID string `json:"upstream_job_id"`
	RetryAfter    int    `json:"retry_after"`
	Error         string `json:"error,omitempty"`
}

// ProgressCallback 进度回调函数。
type ProgressCallback func(update ProgressUpdate)

// ClientConfig 配置 Firefly HTTP 客户端。
type ClientConfig struct {
	XAPIKey    string // Adobe x-api-key（默认 "clio-playground-web"）
	HTTPClient *http.Client
	ProxyURL   string
	Logger     *zap.Logger
	// SubmitMode "clio"（默认）| "psweb"。psweb 时 x-api-key 改 PSWebApp1、
	// Origin/Referer 改 photoshop.adobe.com，并去掉 x-nonce / x-arp-session-id
	// （调用方需确保 token 已是 PSWebApp1 身份）。
	SubmitMode string
}

// pswebAPIKey Photoshop Web 入口的 x-api-key（与 client_id 同名）。
const pswebAPIKey = "PSWebApp1"

func (c *Client) isPSWeb() bool { return strings.EqualFold(c.cfg.SubmitMode, "psweb") }

func (c *Client) apiKey() string {
	if c.isPSWeb() {
		return pswebAPIKey
	}
	return c.cfg.XAPIKey
}

func (c *Client) originSite() string {
	if c.isPSWeb() {
		return "https://photoshop.adobe.com"
	}
	return "https://firefly.adobe.com"
}

type headerGetter interface {
	Get(string) string
}

// Client Adobe firefly-3p HTTP 客户端。
type Client struct {
	cfg        ClientConfig
	httpClient *http.Client
	logger     *zap.Logger
}

// NewClient 构造一个 Firefly client。HTTPClient 可由 caller 用 outbound.NewClient
// 等带 proxy / utls 的工厂提供。
func NewClient(cfg ClientConfig) *Client {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.XAPIKey == "" {
		cfg.XAPIKey = "clio-playground-web"
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 6 * time.Minute}
	}
	return &Client{cfg: cfg, httpClient: hc, logger: logger}
}

func (c *Client) newTLSClient(timeout time.Duration) (tlsclient.HttpClient, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	options := []tlsclient.HttpClientOption{
		tlsclient.WithTimeoutSeconds(int(timeout.Seconds())),
		tlsclient.WithClientProfile(profiles.Chrome_133),
		tlsclient.WithNotFollowRedirects(),
		tlsclient.WithRandomTLSExtensionOrder(),
	}
	if proxy := strings.TrimSpace(c.cfg.ProxyURL); proxy != "" {
		options = append(options, tlsclient.WithProxyUrl(proxy))
	}
	return tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
}

func (c *Client) tlsDo(ctx context.Context, method, urlStr string, headers map[string]string, body []byte, timeout time.Duration) (int, headerGetter, []byte, error) {
	client, err := c.newTLSClient(timeout)
	if err != nil {
		return 0, nil, nil, err
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := fhttp.NewRequest(method, urlStr, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	req = req.WithContext(ctx)
	req.Header = fhttp.Header{}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header[fhttp.HeaderOrderKey] = orderedHeaderKeys(headers)

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, resp.Header, nil, readErr
	}
	return resp.StatusCode, resp.Header, respBody, nil
}

func orderedHeaderKeys(headers map[string]string) []string {
	actual := make(map[string]string, len(headers))
	for k := range headers {
		actual[strings.ToLower(k)] = k
	}
	preferred := []string{
		"authorization",
		"x-api-key",
		"content-type",
		"accept",
		"origin",
		"referer",
		"accept-language",
		"cache-control",
		"pragma",
		"priority",
		"sec-ch-ua",
		"sec-ch-ua-mobile",
		"sec-ch-ua-platform",
		"sec-fetch-site",
		"sec-fetch-mode",
		"sec-fetch-dest",
		"user-agent",
		"x-nonce",
		"x-arp-session-id",
	}
	out := make([]string, 0, len(headers))
	used := make(map[string]struct{}, len(headers))
	for _, lower := range preferred {
		if key, ok := actual[lower]; ok {
			out = append(out, key)
			used[strings.ToLower(key)] = struct{}{}
		}
	}
	rest := make([]string, 0)
	for lower, key := range actual {
		if _, ok := used[lower]; !ok {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func buildSubmitRequestDebug(headers map[string]string, body []byte) string {
	type payloadDebug struct {
		ModelID      string      `json:"modelId,omitempty"`
		ModelVersion string      `json:"modelVersion,omitempty"`
		Size         interface{} `json:"size,omitempty"`
		HasRefs      bool        `json:"hasReferenceBlobs"`
		RefCount     int         `json:"referenceBlobCount"`
	}
	type headerDebug struct {
		Order     []string `json:"order"`
		Auth      bool     `json:"auth"`
		XAPIKey   string   `json:"x_api_key,omitempty"`
		Nonce     bool     `json:"nonce"`
		NonceLen  int      `json:"nonce_len,omitempty"`
		ARP       bool     `json:"arp"`
		ARPLen    int      `json:"arp_len,omitempty"`
		ARPHasArk bool     `json:"arp_has_ark"`
		UserAgent string   `json:"user_agent,omitempty"`
		SecFetch  string   `json:"sec_fetch_site,omitempty"`
		SecCHUA   string   `json:"sec_ch_ua,omitempty"`
	}
	out := map[string]interface{}{
		"headers": headerDebug{
			Order:     orderedHeaderKeys(headers),
			Auth:      strings.TrimSpace(headerValue(headers, "authorization")) != "",
			XAPIKey:   headerValue(headers, "x-api-key"),
			Nonce:     strings.TrimSpace(headerValue(headers, "x-nonce")) != "",
			NonceLen:  len(headerValue(headers, "x-nonce")),
			ARP:       strings.TrimSpace(headerValue(headers, "x-arp-session-id")) != "",
			ARPLen:    len(headerValue(headers, "x-arp-session-id")),
			ARPHasArk: arpSessionHasArk(headerValue(headers, "x-arp-session-id")),
			UserAgent: headerValue(headers, "user-agent"),
			SecFetch:  headerValue(headers, "sec-fetch-site"),
			SecCHUA:   headerValue(headers, "sec-ch-ua"),
		},
		"body_len": len(body),
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err == nil {
		dbg := payloadDebug{}
		if v, _ := payload["modelId"].(string); v != "" {
			dbg.ModelID = v
		}
		if v, _ := payload["modelVersion"].(string); v != "" {
			dbg.ModelVersion = v
		}
		if v, ok := payload["size"]; ok {
			dbg.Size = v
		}
		if refs, ok := payload["referenceBlobs"].([]interface{}); ok {
			dbg.HasRefs = true
			dbg.RefCount = len(refs)
		}
		out["payload"] = dbg
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func headerValue(headers map[string]string, key string) string {
	key = strings.ToLower(key)
	for k, v := range headers {
		if strings.ToLower(k) == key {
			return v
		}
	}
	return ""
}

func arpSessionHasArk(v string) bool {
	if v == "" {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(v)
	}
	if err != nil {
		return false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	ark, _ := payload["ark"].(string)
	return strings.Contains(ark, "BBCC314C-4937-4CCD-B0A3-FDF0F0F7603C")
}

// 浏览器指纹：对齐当前可稳定出图的 Firefly Web 客户端组合。
//
// 关键点：请求头与 TLS ClientHello 必须自洽。之前 iOS Safari UA 搭配 Chrome
// sec-ch-ua 容易被 Adobe 伪装成 408 "system under load"；这里统一回桌面
// Chrome 形态（TLS 见 adobe.httpClient → outbound.ProfileChrome）。
const (
	userAgentHeader      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
	secChUaHeader        = `"Google Chrome";v="149", "Chromium";v="149", "Not)A;Brand";v="24"`
	secChUaMobileHeader  = "?0"
	secChUaPlatformValue = `"Windows"`
	acceptLanguageHeader = "en-GB,en-US;q=0.9,en;q=0.8"
)

func (c *Client) browserHeaders() map[string]string {
	origin := c.originSite()
	return map[string]string{
		"user-agent":         userAgentHeader,
		"sec-ch-ua":          secChUaHeader,
		"sec-ch-ua-mobile":   secChUaMobileHeader,
		"sec-ch-ua-platform": secChUaPlatformValue,
		"origin":             origin,
		"referer":            origin + "/",
		"accept-language":    acceptLanguageHeader,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "cross-site",
		"accept":             "*/*",
		"content-type":       "application/json",
	}
}

func (c *Client) submitHeaders(token string) map[string]string {
	h := c.browserHeaders()
	h["authorization"] = "Bearer " + token
	h["x-api-key"] = c.apiKey()
	// PSWeb 入口不带 x-arp-session-id / x-nonce（与 adobe2api 的 photoshop 通道一致）。
	// clio 入口缺 x-arp-session-id 会被 Adobe 反爬伪装成 408 "system under load" 拒绝，
	// 合成一个结构合法值即可（上游不验签名/指纹）。
	if !c.isPSWeb() {
		h["x-arp-session-id"] = generateARPSessionID()
	}
	return h
}

func (c *Client) pollHeaders(token string) map[string]string {
	origin := c.originSite()
	return map[string]string{
		"authorization":      "Bearer " + token,
		"x-api-key":          c.apiKey(),
		"accept":             "*/*",
		"accept-language":    acceptLanguageHeader,
		"cache-control":      "no-cache",
		"pragma":             "no-cache",
		"priority":           "u=1, i",
		"user-agent":         userAgentHeader,
		"origin":             origin,
		"referer":            origin + "/",
		"sec-ch-ua":          secChUaHeader,
		"sec-ch-ua-mobile":   secChUaMobileHeader,
		"sec-ch-ua-platform": secChUaPlatformValue,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "cross-site",
	}
}

// GenerateResult 生成完成后的产物。Data 仅在 outPath="" 且调用方需要 bytes 时填。
type GenerateResult struct {
	PresignedURL string // upstream Adobe 直链
	Data         []byte // 下载后的字节（outPath="" 且想要 bytes 时填）
}

// GenerateImage 图片生成：submit → poll → 可选下载。
// outPath="" 时只返回 PresignedURL；否则把图片写到 outPath 然后返回。
func (c *Client) GenerateImage(ctx context.Context, token string, payload interface{}, timeout time.Duration, outPath string, progressCb ProgressCallback) (*GenerateResult, error) {
	jobURL, err := c.submit(ctx, imageSubmitURL, token, payload)
	if err != nil {
		return nil, err
	}

	presignedURL, err := c.poll(ctx, jobURL, token, timeout, progressCb)
	if err != nil {
		return nil, err
	}

	if outPath == "" {
		return &GenerateResult{PresignedURL: presignedURL}, nil
	}
	data, err := c.Download(ctx, presignedURL, outPath)
	return &GenerateResult{PresignedURL: presignedURL, Data: data}, err
}

// GenerateVideo 视频生成：submit → poll → 可选下载。
func (c *Client) GenerateVideo(ctx context.Context, token string, payload interface{}, timeout time.Duration, outPath string, progressCb ProgressCallback) (*GenerateResult, error) {
	jobURL, err := c.submit(ctx, videoSubmitURL, token, payload)
	if err != nil {
		return nil, err
	}

	presignedURL, err := c.poll(ctx, jobURL, token, timeout, progressCb)
	if err != nil {
		return nil, err
	}

	if outPath == "" {
		return &GenerateResult{PresignedURL: presignedURL}, nil
	}
	data, err := c.Download(ctx, presignedURL, outPath)
	return &GenerateResult{PresignedURL: presignedURL, Data: data}, err
}

func (c *Client) submit(ctx context.Context, endpoint, token string, payload interface{}) (string, error) {
	noncePrompt := extractPromptFromPayload(payload)
	if m, ok := payload.(ImagePayload); ok {
		delete(m, "_noncePrompt")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	c.logger.Info("firefly submit", zap.String("endpoint", endpoint), zap.Int("payload_size", len(body)))

	reqCtx, reqCancel := context.WithTimeout(ctx, 60*time.Second)
	defer reqCancel()
	headers := c.submitHeaders(token)

	// x-nonce: sha256(userId + "-" + prompt[:256])，缺失会被上游伪装 422 "Invalid Usage"。
	// PSWeb 通道不需要 x-nonce（photoshop 入口不校验）。
	if !c.isPSWeb() {
		if userID := extractUserIDFromJWT(token); userID != "" {
			if noncePrompt != "" {
				headers["x-nonce"] = computeNonce(userID, noncePrompt)
			}
		}
	}
	requestDebug := buildSubmitRequestDebug(headers, body)

	start := time.Now()
	statusCode, respHeader, respBody, err := c.tlsDo(reqCtx, http.MethodPost, endpoint, headers, body, 60*time.Second)
	elapsed := time.Since(start)
	if err != nil {
		c.logger.Error("firefly submit failed", zap.Error(err))
		if hook := getLogHook(ctx); hook != nil {
			hook(LogEntry{Time: start, Phase: "submit", Method: "POST", URL: endpoint,
				RequestSize: len(body), RequestBody: requestDebug,
				DurationMs: elapsed.Milliseconds(), Error: err.Error()})
		}
		return "", &UpstreamTemporaryError{Message: err.Error(), Retryable: true}
	}
	respHeaders := extractHeadersFromHeader(respHeader)

	c.logger.Info("firefly submit resp",
		zap.Int("status", statusCode),
		zap.String("x-override-status-link", respHeader.Get("x-override-status-link")),
		zap.Int("body_size", len(respBody)),
	)

	if hook := getLogHook(ctx); hook != nil {
		hook(LogEntry{Time: start, Phase: "submit", Method: "POST", URL: endpoint,
			StatusCode: statusCode, RequestSize: len(body), RequestBody: requestDebug,
			ResponseBody: truncBody(string(respBody), 4096), Headers: respHeaders,
			DurationMs: elapsed.Milliseconds()})
	}

	if statusCode != http.StatusOK && statusCode != http.StatusAccepted {
		return "", ClassifyError(statusCode, respHeaders, string(respBody))
	}

	// 优先 x-override-status-link header，否则 body.links.result.href / statusUrl / _links.self / jobId。
	statusURL := respHeader.Get("x-override-status-link")

	if statusURL == "" {
		var result map[string]interface{}
		if json.Unmarshal(respBody, &result) == nil {
			if links, ok := result["links"].(map[string]interface{}); ok {
				if resultLink, ok := links["result"].(map[string]interface{}); ok {
					if href, ok := resultLink["href"].(string); ok {
						statusURL = href
					}
				}
			}
			if statusURL == "" {
				if v, ok := result["statusUrl"].(string); ok && v != "" {
					statusURL = v
				}
			}
			if statusURL == "" {
				if links2, ok := result["_links"].(map[string]interface{}); ok {
					if self, ok := links2["self"].(string); ok && self != "" {
						statusURL = self
					}
				}
			}
			if statusURL == "" {
				if jobID, ok := result["jobId"].(string); ok && jobID != "" {
					statusURL = endpoint + "/" + jobID
				}
			}
		}
	}

	if statusURL == "" {
		c.logger.Error("firefly submit no poll url",
			zap.String("body", string(respBody[:minInt(len(respBody), 500)])),
		)
		return "", fmt.Errorf("no status URL in submit response")
	}

	return statusURL, nil
}

func (c *Client) poll(ctx context.Context, statusURL, token string, timeout time.Duration, progressCb ProgressCallback) (string, error) {
	deadline := time.Now().Add(timeout)
	sleepTime := 3 * time.Second
	pollCount := 0
	providerBlockedPolls := 0

	for {
		if time.Now().After(deadline) {
			return "", &UpstreamTemporaryError{Message: "poll timeout", Retryable: false}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		pollCtx, pollCancel := context.WithTimeout(ctx, 30*time.Second)
		pollStart := time.Now()
		statusCode, respHeader, respBody, err := c.tlsDo(pollCtx, http.MethodGet, statusURL, c.pollHeaders(token), nil, 30*time.Second)
		pollElapsed := time.Since(pollStart)
		pollCancel()
		pollCount++

		if err != nil {
			c.logger.Warn("firefly poll error", zap.Error(err))
			if hook := getLogHook(ctx); hook != nil {
				hook(LogEntry{Time: pollStart, Phase: "poll", Method: "GET", URL: statusURL,
					DurationMs: pollElapsed.Milliseconds(), Error: err.Error()})
			}
			sleepWithContext(ctx, sleepTime)
			continue
		}

		if statusCode != http.StatusOK {
			headers := extractHeadersFromHeader(respHeader)
			classified := ClassifyError(statusCode, headers, string(respBody))
			if hook := getLogHook(ctx); hook != nil {
				hook(LogEntry{Time: pollStart, Phase: "poll", Method: "GET", URL: statusURL,
					StatusCode: statusCode, ResponseBody: truncBody(string(respBody), 2048),
					Headers: headers, DurationMs: pollElapsed.Milliseconds(),
					Error: classified.Error()})
			}
			if _, ok := classified.(*ProviderBlockedError); ok {
				providerBlockedPolls++
				if providerBlockedPolls <= 3 {
					c.logger.Warn("firefly provider blocked, retry",
						zap.Int("attempt", providerBlockedPolls))
					if retryAfter := parseRetryAfter(respHeader.Get("retry-after")); retryAfter > 0 {
						sleepWithContext(ctx, retryAfter)
					} else {
						sleepWithContext(ctx, sleepTime)
					}
					continue
				}
			} else {
				providerBlockedPolls = 0
			}
			// 451 / Retryable=false 的临时错误（内容安全、永久上游拒绝）必须立刻返回，
			// 不能继续 poll 直到 timeout——否则用户会白等 300s/600s 只看到「生成超时」。
			if tempErr, ok := classified.(*UpstreamTemporaryError); ok {
				if tempErr.Retryable {
					sleepWithContext(ctx, sleepTime)
					continue
				}
				return "", tempErr
			}
			return "", classified
		}

		var latest map[string]interface{}
		if err := json.Unmarshal(respBody, &latest); err != nil {
			c.logger.Warn("firefly poll parse", zap.Error(err))
			sleepWithContext(ctx, sleepTime)
			continue
		}

		statusHeader := strings.ToUpper(respHeader.Get("x-task-status"))
		statusBody := ""
		if s, ok := latest["status"].(string); ok {
			statusBody = strings.ToUpper(s)
		}
		status := statusBody
		if status == "" {
			status = statusHeader
		}

		progress := extractProgress(latest, extractHeadersFromHeader(respHeader))

		shouldLog := pollCount <= 2 || pollCount%10 == 0
		if hook := getLogHook(ctx); hook != nil && shouldLog {
			hook(LogEntry{Time: pollStart, Phase: "poll", Method: "GET", URL: statusURL,
				StatusCode: statusCode, ResponseBody: truncBody(string(respBody), 2048),
				Headers: extractHeadersFromHeader(respHeader), DurationMs: pollElapsed.Milliseconds(),
				TaskStatus: status, Progress: progress})
		}

		retryAfterSec := parseRetryAfterHeader(respHeader.Get("retry-after"))

		if progressCb != nil && isInProgressStatus(status) {
			progressCb(ProgressUpdate{
				TaskStatus:   "IN_PROGRESS",
				TaskProgress: progress,
				RetryAfter:   retryAfterSec,
			})
		}

		if reason := detectContentPolicyError(latest); reason != "" {
			if hook := getLogHook(ctx); hook != nil {
				hook(LogEntry{Time: pollStart, Phase: "poll_final", Method: "GET", URL: statusURL,
					StatusCode: statusCode, ResponseBody: truncBody(string(respBody), 4096),
					TaskStatus: "CONTENT_POLICY", Error: reason})
			}
			return "", &ContentPolicyError{Message: reason}
		}

		if status == "FAILED" || status == "ERROR" || status == "CANCELLED" || status == "REJECTED" {
			errMsg := extractErrorMessage(latest)
			if hook := getLogHook(ctx); hook != nil {
				hook(LogEntry{Time: pollStart, Phase: "poll_final", Method: "GET", URL: statusURL,
					StatusCode: statusCode, ResponseBody: truncBody(string(respBody), 4096),
					TaskStatus: status, Error: errMsg})
			}
			return "", &AdobeRequestError{Message: "generation failed: " + errMsg}
		}

		if outputs, ok := latest["outputs"].([]interface{}); ok && len(outputs) > 0 {
			presigned := extractPresignedURL(latest)
			if presigned != "" {
				if hook := getLogHook(ctx); hook != nil {
					hook(LogEntry{Time: pollStart, Phase: "poll_done", Method: "GET", URL: statusURL,
						StatusCode: statusCode, TaskStatus: "COMPLETED", Progress: 100,
						ResponseBody: truncBody(string(respBody), 2048)})
				}
				if progressCb != nil {
					progressCb(ProgressUpdate{TaskStatus: "COMPLETED", TaskProgress: 100})
				}
				return presigned, nil
			}
		}

		if status == "COMPLETED" || status == "SUCCEEDED" {
			presigned := extractPresignedURL(latest)
			if presigned != "" {
				return presigned, nil
			}
			return "", fmt.Errorf("job completed but no output URL found")
		}

		retryAfter := retryAfterSec
		if retryAfter <= 0 {
			retryAfter = 3
		}
		sleepWithContext(ctx, time.Duration(retryAfter)*time.Second)
	}
}

func parseRetryAfterHeader(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// Download 拉取 Adobe 上游 presigned URL 到内存或 outPath。
func (c *Client) Download(ctx context.Context, fileURL string, outPath string) ([]byte, error) {
	statusCode, _, body, err := c.tlsDo(ctx, http.MethodGet, fileURL, map[string]string{
		"Accept":     "*/*",
		"User-Agent": userAgentHeader,
	}, nil, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: status %d", statusCode)
	}

	// 调用方负责创建目录/文件；这里返回 bytes 并要求 caller 写盘。
	return body, nil
}

// UploadImage 上传一张参考图到 Adobe storage，返回 image id（可放进 referenceBlobs）。
func (c *Client) UploadImage(ctx context.Context, token string, data []byte, mimeType string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("image data is empty")
	}
	uploadMime, err := normalizeUploadImageMime(data, mimeType)
	if err != nil {
		return "", err
	}

	// 上传端点同样被 Adobe 指纹反爬，补上桌面 Chrome 头（Content-Type 保持图片 mime 不覆盖）。
	origin := c.originSite()
	headers := map[string]string{
		"Authorization":      "Bearer " + token,
		"x-api-key":          c.apiKey(),
		"Content-Type":       uploadMime,
		"Accept":             "application/json",
		"User-Agent":         userAgentHeader,
		"sec-ch-ua":          secChUaHeader,
		"sec-ch-ua-mobile":   secChUaMobileHeader,
		"sec-ch-ua-platform": secChUaPlatformValue,
		"Origin":             origin,
		"Referer":            origin + "/",
		"accept-language":    acceptLanguageHeader,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "same-site",
	}

	statusCode, respHeader, respBody, err := c.tlsDo(ctx, http.MethodPost, imageUploadURL, headers, data, 60*time.Second)
	if err != nil {
		return "", &UpstreamTemporaryError{Message: err.Error(), Retryable: true}
	}

	if statusCode != http.StatusOK && statusCode != http.StatusCreated {
		headers := extractHeadersFromHeader(respHeader)
		return "", ClassifyError(statusCode, headers, string(respBody))
	}

	var result struct {
		Images []struct {
			ID string `json:"id"`
		} `json:"images"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse upload response: %w", err)
	}
	if len(result.Images) > 0 && result.Images[0].ID != "" {
		return result.Images[0].ID, nil
	}

	var flat struct {
		ID      string `json:"id"`
		ImageID string `json:"imageId"`
	}
	_ = json.Unmarshal(respBody, &flat)
	id := flat.ID
	if id == "" {
		id = flat.ImageID
	}
	if id != "" {
		return id, nil
	}
	return "", fmt.Errorf("upload succeeded but no image id in response: %s", string(respBody[:minInt(len(respBody), 200)]))
}

func normalizeUploadImageMime(data []byte, mimeType string) (string, error) {
	mt := strings.TrimSpace(strings.ToLower(mimeType))
	if mt != "" {
		if parsed, _, err := mime.ParseMediaType(mt); err == nil {
			mt = parsed
		} else if i := strings.Index(mt, ";"); i >= 0 {
			mt = strings.TrimSpace(mt[:i])
		}
	}
	mt = canonicalImageMime(mt)

	if !isSupportedUploadImageMime(mt) {
		detected := canonicalImageMime(http.DetectContentType(data))
		if isSupportedUploadImageMime(detected) {
			mt = detected
		}
	}
	if !isSupportedUploadImageMime(mt) {
		if mt == "" {
			mt = "unknown"
		}
		return "", fmt.Errorf("unsupported reference image content type: %s", mt)
	}
	return mt, nil
}

func canonicalImageMime(mt string) string {
	mt = strings.TrimSpace(strings.ToLower(mt))
	switch mt {
	case "image/jpg", "image/pjpeg":
		return "image/jpeg"
	case "image/x-png":
		return "image/png"
	default:
		return mt
	}
}

func isSupportedUploadImageMime(mt string) bool {
	switch mt {
	case "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

func sleepWithContext(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func detectContentPolicyError(body map[string]interface{}) string {
	policyKeys := []string{
		"moderation_status", "moderationStatus", "content_policy",
		"contentPolicy", "safety", "safetyResult",
	}
	for _, key := range policyKeys {
		if v, ok := body[key]; ok {
			if s, ok := v.(string); ok {
				upper := strings.ToUpper(s)
				if upper == "BLOCKED" || upper == "REJECTED" || upper == "UNSAFE" || upper == "VIOLATED" || upper == "FAILED" {
					return fmt.Sprintf("内容安全审核不通过 (%s: %s)", key, s)
				}
			}
			if m, ok := v.(map[string]interface{}); ok {
				if st, ok := m["status"].(string); ok {
					upper := strings.ToUpper(st)
					if upper == "BLOCKED" || upper == "REJECTED" || upper == "UNSAFE" {
						return fmt.Sprintf("内容安全审核不通过 (%s.status: %s)", key, st)
					}
				}
			}
		}
	}

	errMsg := ""
	if e, ok := body["error"].(string); ok {
		errMsg = strings.ToLower(e)
	} else if e, ok := body["error"].(map[string]interface{}); ok {
		if m, ok := e["message"].(string); ok {
			errMsg = strings.ToLower(m)
		}
	}
	if errMsg != "" {
		violationTerms := []string{"policy", "unsafe", "moderation", "content_policy", "nsfw", "prohibited", "violat"}
		for _, term := range violationTerms {
			if strings.Contains(errMsg, term) {
				return fmt.Sprintf("提示词违规: %s", errMsg)
			}
		}
	}
	return ""
}

func isInProgressStatus(s string) bool {
	switch s {
	case "IN_PROGRESS", "PENDING", "QUEUED", "STARTED", "RUNNING", "PROCESSING", "":
		return true
	}
	return false
}

func extractHeaders(resp *http.Response) map[string]string {
	return extractHeadersFromHeader(resp.Header)
}

func extractHeadersFromHeader(header headerGetter) map[string]string {
	h := make(map[string]string)
	for _, key := range []string{"x-access-error", "x-task-progress", "x-progress", "progress", "retry-after", "x-override-status-link"} {
		if v := header.Get(key); v != "" {
			h[key] = v
		}
	}
	return h
}

func extractProgress(body map[string]interface{}, headers map[string]string) int {
	for _, key := range []string{"x-task-progress", "x-progress", "progress"} {
		if v, ok := headers[key]; ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				return int(f)
			}
		}
	}
	if task, ok := body["task"].(map[string]interface{}); ok {
		for _, key := range []string{"progress", "percentage"} {
			if v, ok := task[key]; ok {
				if n := toInt(v); n > 0 {
					return n
				}
			}
		}
	}
	if result, ok := body["result"].(map[string]interface{}); ok {
		if v, ok := result["progress"]; ok {
			if n := toInt(v); n > 0 {
				return n
			}
		}
	}
	for _, key := range []string{"progress", "percentage", "percent", "task_progress", "taskProgress"} {
		if v, ok := body[key]; ok {
			if n := toInt(v); n > 0 {
				return n
			}
		}
	}
	return 0
}

func extractPresignedURL(body map[string]interface{}) string {
	if outputs, ok := body["outputs"].([]interface{}); ok && len(outputs) > 0 {
		if out, ok := outputs[0].(map[string]interface{}); ok {
			for _, mediaKey := range []string{"image", "video"} {
				if media, ok := out[mediaKey].(map[string]interface{}); ok {
					if u, ok := media["presignedUrl"].(string); ok {
						return u
					}
				}
			}
			if u, ok := out["presignedUrl"].(string); ok {
				return u
			}
			if u, ok := out["url"].(string); ok {
				return u
			}
		}
	}
	if u, ok := body["presignedUrl"].(string); ok {
		return u
	}
	if result, ok := body["result"].(map[string]interface{}); ok {
		if u, ok := result["url"].(string); ok {
			return u
		}
	}
	return ""
}

func extractErrorMessage(body map[string]interface{}) string {
	for _, key := range []string{"error", "message", "errorMessage", "error_message"} {
		if v, ok := body[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
			if m, ok := v.(map[string]interface{}); ok {
				if msg, ok := m["message"].(string); ok {
					return msg
				}
			}
		}
	}
	return "unknown error"
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 静默引用，避免 import 误判（url 在未来 inline 测试中可能用到）。
var _ = url.QueryEscape
