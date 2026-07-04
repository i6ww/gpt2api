package xai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/provider"
)

const (
	httpTimeout       = 60 * time.Second
	pollMaxDur        = 12 * time.Minute
	pollInitialPeriod = 3 * time.Second
	pollMaxPeriod     = 10 * time.Second

	// xAI 官方 API 对 grok-imagine-video 是「每团队每模型 1 RPS」的提交速率限制
	// （额度档位随历史消费自动上调）。多个在途任务并发提交时会撞 429，但只要
	// 错峰 ~1s 重试即可成功。submitMaxRetries × 退避保证单号 N 并发也能把提交
	// 自然串行到 1/s，而不是直接判失败。
	submitMaxRetries  = 6
	submitRetryBase   = 1100 * time.Millisecond
	submitRetryJitter = 900 * time.Millisecond

	// xAI 视频提交对 prompt 有字节长度上限（约 4096 字节，按 UTF-8 字节算，不是字符数）。
	// 超出会被上游 400 拒绝；历史故障里这个 400 还被错误当成账号故障把唯一的 xAI 号
	// 打进冷却，导致后续任务全部「暂无可用账号」。这里提交前主动截断，从根上不触发 400。
	maxXAIPromptBytes = 4096
	// xAI grok-imagine-video 单次最长 15s，超出会 400/422。提交前钳制。
	maxXAIDurationSec = 15
)

// Client xAI 官方 API HTTP 客户端（chat + video 共用）。
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient 构造一个 xAI API 客户端。baseURL 空走 DefaultBaseURL；proxyURL 空走直连。
func NewClient(baseURL, proxyURL string) *Client {
	tr := &http.Transport{}
	if p := strings.TrimSpace(proxyURL); p != "" {
		if u, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &Client{
		http:    &http.Client{Timeout: httpTimeout, Transport: tr},
		baseURL: strings.TrimRight(firstNonEmpty(baseURL, DefaultBaseURL), "/"),
	}
}

// Provider 实现 provider.Provider（视频生成）。chat 走 Client.ChatComplete。
type Provider struct {
	defaultURL string
	name       string
}

// New 构造 video provider。
func New(defaultBase string) *Provider {
	if strings.TrimSpace(defaultBase) == "" {
		defaultBase = DefaultBaseURL
	}
	return &Provider{defaultURL: strings.TrimRight(defaultBase, "/"), name: "xai"}
}

// Name impl。
func (p *Provider) Name() string { return p.name }

type vidAsset struct {
	URL        string `json:"url"`
	VideoURL   string `json:"video_url"`
	ThumbURL   string `json:"thumb_url"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	DurationMs int    `json:"duration_ms"`
	Mime       string `json:"mime"`
}

func (a vidAsset) bestURL() string { return firstNonEmpty(a.URL, a.VideoURL) }

// vidObj 对应 xAI 轮询返回里的嵌套 video 对象：{"url":"...mp4","duration":8}。duration 单位为秒。
type vidObj struct {
	URL      string `json:"url"`
	VideoURL string `json:"video_url"`
	ThumbURL string `json:"thumb_url"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Duration int    `json:"duration"`
	Mime     string `json:"mime"`
}

type vidResp struct {
	ID        string     `json:"id"`
	RequestID string     `json:"request_id"`
	Status    string     `json:"status"`
	URL       string     `json:"url"`
	VideoURL  string     `json:"video_url"`
	Video     *vidObj    `json:"video"`
	Data      []vidAsset `json:"data"`
	Error     string     `json:"error"`
	Message   string     `json:"message"`
}

// Generate 官方 xAI 视频生成；自动识别同步直返 / 异步轮询。
func (p *Provider) Generate(ctx context.Context, req *provider.Request) (*provider.Result, error) {
	if req.Kind != provider.KindVideo {
		return nil, fmt.Errorf("xai provider only supports video kind, got %s", req.Kind)
	}
	if strings.TrimSpace(req.Credential) == "" {
		return nil, fmt.Errorf("xai provider missing credential")
	}
	base := strings.TrimRight(firstNonEmpty(req.BaseURL, p.defaultURL, DefaultBaseURL), "/")
	c := NewClient(base, req.ProxyURL)

	// 有输入图 → 自动走 grok-imagine-video-1.5（图生视频）；无图 → grok-imagine-video（文生视频）。
	// 对外仍是同一个模型、同一个价格。
	//
	// 关键：RefAssets[0] 往往是站内相对路径（/api/v1/gen/cached/...），xAI 无法回源拉取，
	// 会被当成纯文字请求 → 1.5 报 "Text-to-video is not supported"。必须先解析成
	// data: base64 或可公网访问的 http(s) URL 再塞 image_url。
	var imageURL string
	if len(req.RefAssets) > 0 {
		if u := resolveImageRef(req.RefAssets[0]); u != "" {
			imageURL = u
		} else {
			return nil, fmt.Errorf("xai video: unresolvable input image ref %q", snippet([]byte(req.RefAssets[0]), 80))
		}
	}
	hasImage := imageURL != ""
	body := map[string]any{
		"model":  ResolveVideoModel(req.ModelCode, hasImage),
		"prompt": truncateUTF8Bytes(req.Prompt, maxXAIPromptBytes),
	}
	if d := intParam(req.Params, "duration", 0); d > 0 {
		if d > maxXAIDurationSec {
			d = maxXAIDurationSec
		}
		body["duration"] = d
	}
	// 方向用 aspect_ratio（"16:9"/"9:16"/...）。xAI 没有自由 size 字段（传 1080x1920 会 422），
	// 前端若塞了 size 仅用来兜底推断方向。
	ar := strParam(req.Params, "aspect_ratio", "")
	sz := strParam(req.Params, "size", "")
	if ar == "" && sz != "" {
		ar = aspectFromSize(sz)
	}
	if ar != "" {
		body["aspect_ratio"] = ar
	}
	// 分辨率走独立的 resolution 字段（480p/720p/1080p）。不传 xAI 默认 480p，
	// 这里默认拉到 720p，支持 params.resolution / params.quality 覆盖。
	if res := normalizeXAIResolution(strParam(req.Params, "resolution", ""), strParam(req.Params, "quality", "")); res != "" {
		body["resolution"] = res
	}
	if hasImage {
		// xAI 视频图生视频必须用嵌套 image:{url}。直接传 image_url=string 会被
		// grok-imagine-video-1.5 忽略 → 当成纯文字 → 报 "Text-to-video is not supported"。
		// 实测 image:{url:<data: 或 https>} 在 base 与 1.5 上均 200。
		body["image"] = map[string]any{"url": imageURL}
	}
	payload, _ := json.Marshal(body)
	submitURL := base + "/videos/generations"
	reqExcerpt := redactPayloadForLog(body)

	start := time.Now()
	var createResp *vidResp
	var status int
	var raw []byte
	for attempt := 0; ; attempt++ {
		var err error
		attStart := time.Now()
		createResp, status, raw, err = c.doVideo(ctx, http.MethodPost, submitURL, payload, req.Credential)
		if err != nil {
			logUpstream(ctx, req, provider.UpstreamLogEntry{
				Stage: "video.submit", Method: http.MethodPost, URL: submitURL,
				DurationMs: time.Since(attStart).Milliseconds(), RequestExcerpt: reqExcerpt,
				Error: err.Error(), Meta: map[string]any{"attempt": attempt, "model": body["model"], "has_image": hasImage},
			})
			return nil, err
		}
		logUpstream(ctx, req, provider.UpstreamLogEntry{
			Stage: "video.submit", Method: http.MethodPost, URL: submitURL, StatusCode: status,
			DurationMs: time.Since(attStart).Milliseconds(), RequestExcerpt: reqExcerpt,
			ResponseExcerpt: snippet(raw, 400),
			Meta:            map[string]any{"attempt": attempt, "model": body["model"], "has_image": hasImage},
		})
		if status == http.StatusTooManyRequests && attempt < submitMaxRetries {
			// 撞 1 RPS 限速：错峰退避后重试，让并发提交自然串行到 1/s。
			wait := submitRetryBase + time.Duration(attempt)*300*time.Millisecond +
				time.Duration(rand.Int63n(int64(submitRetryJitter)))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		break
	}
	if status >= 400 {
		return nil, fmt.Errorf("xai video %d: %s", status, snippet(raw, 240))
	}

	if assets := collectAssets(createResp); len(assets) > 0 {
		return &provider.Result{TaskID: req.TaskID, Assets: assets, Latency: time.Since(start)}, nil
	}

	taskID := firstNonEmpty(createResp.RequestID, createResp.ID)
	if taskID == "" {
		return nil, fmt.Errorf("xai video empty request_id and empty data")
	}

	period := pollInitialPeriod
	deadline := time.Now().Add(pollMaxDur)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(period):
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("xai video task %s timeout", taskID)
		}
		pollURL := base + "/videos/" + url.PathEscape(taskID)
		st, sCode, sRaw, sErr := c.doVideo(ctx, http.MethodGet, pollURL, nil, req.Credential)
		if sErr != nil {
			logUpstream(ctx, req, provider.UpstreamLogEntry{
				Stage: "video.poll", Method: http.MethodGet, URL: pollURL,
				Error: sErr.Error(), Meta: map[string]any{"request_id": taskID},
			})
			return nil, sErr
		}
		if sCode == http.StatusTooManyRequests {
			// 轮询撞限速：不算失败，下一周期再查。
			continue
		}
		if sCode >= 400 {
			logUpstream(ctx, req, provider.UpstreamLogEntry{
				Stage: "video.poll", Method: http.MethodGet, URL: pollURL, StatusCode: sCode,
				ResponseExcerpt: snippet(sRaw, 400), Meta: map[string]any{"request_id": taskID},
			})
			return nil, fmt.Errorf("xai video poll %d: %s", sCode, snippet(sRaw, 240))
		}
		switch strings.ToLower(strings.TrimSpace(st.Status)) {
		case "succeeded", "success", "completed", "done", "":
			if assets := collectAssets(st); len(assets) > 0 {
				logUpstream(ctx, req, provider.UpstreamLogEntry{
					Stage: "video.done", Method: http.MethodGet, URL: pollURL, StatusCode: sCode,
					DurationMs: time.Since(start).Milliseconds(),
					Meta:       map[string]any{"request_id": taskID, "assets": len(assets)},
				})
				return &provider.Result{TaskID: req.TaskID, Assets: assets, Latency: time.Since(start)}, nil
			}
			if strings.TrimSpace(st.Status) != "" {
				// 明确终态却没数据 → 失败
				logUpstream(ctx, req, provider.UpstreamLogEntry{
					Stage: "video.done", Method: http.MethodGet, URL: pollURL, StatusCode: sCode,
					ResponseExcerpt: snippet(sRaw, 400), Error: "succeeded but empty data",
					Meta:            map[string]any{"request_id": taskID},
				})
				return nil, fmt.Errorf("xai video task %s succeeded but empty data", taskID)
			}
		case "failed", "error", "cancelled", "canceled":
			msg := firstNonEmpty(st.Error, st.Message, "xai video task failed")
			logUpstream(ctx, req, provider.UpstreamLogEntry{
				Stage: "video.failed", Method: http.MethodGet, URL: pollURL, StatusCode: sCode,
				ResponseExcerpt: snippet(sRaw, 400), Error: msg,
				Meta:            map[string]any{"request_id": taskID},
			})
			return nil, fmt.Errorf("xai video task %s: %s", taskID, msg)
		}
		period *= 2
		if period > pollMaxPeriod {
			period = pollMaxPeriod
		}
	}
}

func (c *Client) doVideo(ctx context.Context, method, fullURL string, payload []byte, token string) (*vidResp, int, []byte, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, fullURL, rdr)
	if err != nil {
		return nil, 0, nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/json")
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("xai video http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, raw, nil
	}
	var out vidResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, resp.StatusCode, raw, fmt.Errorf("xai video decode: %w (raw=%s)", err, snippet(raw, 240))
	}
	return &out, resp.StatusCode, raw, nil
}

func collectAssets(r *vidResp) []provider.Asset {
	if r == nil {
		return nil
	}
	out := make([]provider.Asset, 0, len(r.Data)+1)
	for _, it := range r.Data {
		if u := it.bestURL(); u != "" {
			out = append(out, provider.Asset{
				URL:        u,
				ThumbURL:   it.ThumbURL,
				Width:      it.Width,
				Height:     it.Height,
				DurationMs: it.DurationMs,
				Mime:       firstNonEmpty(it.Mime, "video/mp4"),
			})
		}
	}
	if len(out) == 0 && r.Video != nil {
		if u := firstNonEmpty(r.Video.URL, r.Video.VideoURL); u != "" {
			out = append(out, provider.Asset{
				URL:        u,
				ThumbURL:   r.Video.ThumbURL,
				Width:      r.Video.Width,
				Height:     r.Video.Height,
				DurationMs: r.Video.Duration * 1000, // xAI 返回秒，转毫秒
				Mime:       firstNonEmpty(r.Video.Mime, "video/mp4"),
			})
		}
	}
	if len(out) == 0 {
		if u := firstNonEmpty(r.URL, r.VideoURL); u != "" {
			out = append(out, provider.Asset{URL: u, Mime: "video/mp4"})
		}
	}
	return out
}

// logUpstream 把一次上游交互写入 generation_upstream_log（经 req.UpstreamLog 钩子）。
func logUpstream(ctx context.Context, req *provider.Request, entry provider.UpstreamLogEntry) {
	if req == nil || req.UpstreamLog == nil {
		return
	}
	if entry.Provider == "" {
		entry.Provider = "xai"
	}
	req.UpstreamLog(ctx, entry)
}

// redactPayloadForLog 生成请求体摘要：把 image.url 里的超长 base64 data URI 换成短标记，
// 避免把几 MB 的 base64 灌进日志表。
func redactPayloadForLog(body map[string]any) string {
	clone := make(map[string]any, len(body))
	for k, v := range body {
		clone[k] = v
	}
	if img, ok := clone["image"].(map[string]any); ok {
		u, _ := img["url"].(string)
		marker := u
		if strings.HasPrefix(u, "data:") {
			marker = fmt.Sprintf("<data-uri %d bytes>", len(u))
		} else if len(u) > 120 {
			marker = u[:120] + "...(truncated)"
		}
		clone["image"] = map[string]any{"url": marker}
	}
	b, err := json.Marshal(clone)
	if err != nil {
		return ""
	}
	return snippet(b, 600)
}

// resolveImageRef 把输入图引用转成 xAI image_url 能直接消费的形态：
//   - data:...           → 原样
//   - http(s)://...      → 原样（xAI 服务端回源拉取）
//   - /api/v1/gen/cached → 读本地 storage，转 data:<mime>;base64,...
//
// 解析失败返回 ""，调用方据此判定为"无可用输入图"。
func resolveImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "data:") {
		return ref
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if strings.HasPrefix(ref, "/api/v1/gen/cached/") {
		rel := strings.TrimPrefix(ref, "/api/v1/gen/cached/")
		if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
			return ""
		}
		root := strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT"))
		if root == "" {
			root = "/app/storage/public"
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || len(raw) == 0 {
			return ""
		}
		mime := http.DetectContentType(raw)
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	}
	return ""
}

// normalizeXAIResolution 归一化分辨率到 xAI 接受的 480p/720p/1080p。
// 优先用显式 resolution；否则映射 quality；都没有则默认 720p。
func normalizeXAIResolution(res, quality string) string {
	switch strings.ToLower(strings.TrimSpace(res)) {
	case "480p", "480":
		return "480p"
	case "720p", "720":
		return "720p"
	case "1080p", "1080":
		return "1080p"
	}
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "sd", "low", "480", "480p":
		return "480p"
	case "fhd", "fullhd", "uhd", "1080", "1080p":
		return "1080p"
	}
	// hd / 未指定 → 默认 720p
	return "720p"
}

// aspectFromSize 从 "WxH" 粗略推断 aspect_ratio，供 size 不合法时降级使用。
func aspectFromSize(s string) string {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(s)), "x", 2)
	if len(parts) != 2 {
		return ""
	}
	w := atoiSafe(parts[0])
	h := atoiSafe(parts[1])
	if w <= 0 || h <= 0 {
		return ""
	}
	r := float64(w) / float64(h)
	switch {
	case r >= 1.45:
		return "16:9"
	case r <= 0.7:
		return "9:16"
	default:
		return "1:1"
	}
}

func atoiSafe(s string) int {
	n := 0
	for _, ch := range strings.TrimSpace(s) {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func strParam(p map[string]any, key, def string) string {
	if p == nil {
		return def
	}
	if v, ok := p[key]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return def
}

func intParam(p map[string]any, key string, def int) int {
	if p == nil {
		return def
	}
	if v, ok := p[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return def
}

func snippet(b []byte, n int) string {
	s := strings.ToValidUTF8(string(b), "")
	if len(s) > n {
		return s[:n] + "...(truncated)"
	}
	return s
}

// truncateUTF8Bytes 将 s 截断到不超过 maxBytes 字节，且不切断多字节 UTF-8 字符。
// maxBytes <= 0 时原样返回。
func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	// 回退到一个合法的 rune 边界（首字节不是 0x80~0xBF 续字节）。
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}
