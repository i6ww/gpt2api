// Package grok 实现 GROK 风格的视频生成 provider。
//
// GROK 公开 API 仍在演进中，本 provider 采用一个通用的"异步任务 + 轮询"协议，
// 你可以把 base_url 指到任意兼容网关（kleinai-gateway / FAL / Runway 风格）：
//
//	POST {base_url}/v1/videos/generations
//	     Authorization: Bearer {api_key}
//	     Body: {"model","prompt","duration","aspect_ratio","ref_images":[]}
//	     Resp 200 either:
//	       A. {"task_id":"abc","status":"queued"}            // 异步
//	       B. {"data":[{"url":"https://..."}], "duration_ms":... } // 同步直返
//
//	GET {base_url}/v1/videos/tasks/{task_id}
//	     Resp: {"task_id","status":"queued|running|succeeded|failed",
//	            "data":[{"url":"...","duration_ms":...}], "error":""}
//
// 调度器内置超时：默认 12min，单次轮询间隔 3s（指数 backoff 上限 10s）。
package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kleinai/backend/internal/provider"
)

const (
	defaultBaseURL    = webBaseURL
	httpTimeout       = 30 * time.Second
	pollMaxDur        = 12 * time.Minute
	pollInitialPeriod = 3 * time.Second
	pollMaxPeriod     = 10 * time.Second
)

// Provider 实现 provider.Provider。
type Provider struct {
	client     *http.Client
	defaultURL string
	name       string
	web        *WebClient
}

// New 构造。
func New(defaultBase string) *Provider {
	if defaultBase == "" {
		defaultBase = defaultBaseURL
	}
	return &Provider{
		client: &http.Client{
			Timeout: httpTimeout,
		},
		defaultURL: strings.TrimRight(defaultBase, "/"),
		name:       "grok",
		web:        NewWebClient(defaultBase),
	}
}

// Name impl。
func (p *Provider) Name() string { return p.name }

type vidCreateReq struct {
	Model       string   `json:"model"`
	Prompt      string   `json:"prompt"`
	NegPrompt   string   `json:"negative_prompt,omitempty"`
	Duration    int      `json:"duration,omitempty"`
	AspectRatio string   `json:"aspect_ratio,omitempty"`
	N           int      `json:"n,omitempty"`
	RefImages   []string `json:"ref_images,omitempty"`
}

type vidAsset struct {
	URL        string `json:"url"`
	ThumbURL   string `json:"thumb_url,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
	Mime       string `json:"mime,omitempty"`
}

type vidResp struct {
	TaskID string     `json:"task_id,omitempty"`
	Status string     `json:"status,omitempty"`
	Data   []vidAsset `json:"data,omitempty"`
	Error  string     `json:"error,omitempty"`
}

// Generate 视频生成；自动识别同步 / 异步响应。
func (p *Provider) Generate(ctx context.Context, req *provider.Request) (*provider.Result, error) {
	if req.Kind != provider.KindVideo {
		return nil, fmt.Errorf("grok provider only supports video kind, got %s", req.Kind)
	}
	if req.Credential == "" {
		return nil, fmt.Errorf("grok provider missing credential")
	}
	base := req.BaseURL
	if base == "" {
		base = p.defaultURL
	}
	if base == "" || strings.Contains(base, "grok.com") || strings.Contains(base, "api.x.ai") {
		web := p.web
		if req.ProxyURL != "" || req.BaseURL != "" {
			web = NewWebClientWithProxy(req.BaseURL, req.ProxyURL)
		}
		web = web.WithUpstreamLogger(req.UpstreamLog)
		count := req.Count
		if count <= 0 {
			count = 1
		}
		modelCode := NormalizeVideoModel(req.ModelCode)
		usePipeline := IsPipelineVideoModel(modelCode)
		// pipeline 通道必须有 1 张参考图；如果用户明确选了 pipeline 但没传 ref，
		// 应该直接报错给用户，避免静默 fallback 到扣额度通道。
		if usePipeline && len(req.RefAssets) == 0 {
			return nil, fmt.Errorf("grok-imagine-video-6s-free 需要至少一张参考图（i2v 通道）")
		}
		// 允许 fallback 的条件：
		//   - 用户选的是主通道（usePipeline=false）
		//   - 当前任务有至少一张参考图（pipeline 通道是 i2v-only，没有参考图 fallback 也跑不起来）
		//   - 主通道返回明确的额度/限流错误（IsGrokQuotaError 判定）
		allowFallback := !usePipeline && len(req.RefAssets) > 0
		assets := make([]provider.Asset, 0, count)
		// 跟踪是否所有 count 张都因 fallback 改走了 pipeline；只要有一张走的是主通道，
		// 就不能整单按免额度退款（避免少收钱）。effectiveModelCode 默认为空，意味着
		// "按用户原 ModelCode 收"。
		effectiveModelCode := modelCode
		allFallback := true
		for i := 0; i < count; i++ {
			vidReq := VideoRequest{
				ModelCode:   modelCode,
				Prompt:      req.Prompt,
				Refs:        req.RefAssets,
				DurationSec: intParam(req.Params, "duration", 6),
				Size:        strParam(req.Params, "size", ""),
				AspectRatio: strParam(req.Params, "aspect_ratio", ""),
				Quality:     strParam(req.Params, "quality", ""),
				Count:       1,
			}
			var (
				items     []VideoAsset
				err       error
				usedFree  bool // 当前这一张是否最终是 pipeline 出的
			)
			if usePipeline {
				items, err = web.GeneratePipelineVideo(ctx, req.Credential, vidReq)
				usedFree = err == nil
			} else {
				items, err = web.GenerateVideo(ctx, req.Credential, vidReq)
				if err != nil && allowFallback && IsGrokQuotaError(err) {
					// 主通道被限流 → 自动退化到免额度 pipeline 通道。
					// 注意：pipeline 是固定 6s + 服务端定比例，体验和主通道不完全等价，
					// 但任务"能出"远比"出对"重要，所以默认开启。
					if req.UpstreamLog != nil {
						req.UpstreamLog(ctx, provider.UpstreamLogEntry{
							Provider: "grok",
							Stage:    "video.fallback_to_pipeline",
							Error:    err.Error(),
							Meta: map[string]any{
								"reason":   "main_channel_quota_or_rate_limited",
								"original": modelCode,
							},
						})
					}
					items, err = web.GeneratePipelineVideo(ctx, req.Credential, vidReq)
					usedFree = err == nil
				}
			}
			if err != nil {
				return nil, err
			}
			if !usedFree {
				allFallback = false
			}
			for _, it := range items {
				assets = append(assets, provider.Asset{
					URL:        it.URL,
					ThumbURL:   it.ThumbURL,
					Width:      it.Width,
					Height:     it.Height,
					DurationMs: it.DurationMs,
					Mime:       "video/mp4",
				})
			}
		}
		// 全部走的免额度 pipeline → 把 effective 改成 free model code，让上层退款；
		// 否则保持原 model_code（多张里有任何一张走主通道，本次按主通道价收）。
		if allFallback {
			effectiveModelCode = "grok-imagine-video-6s-free"
		}
		return &provider.Result{TaskID: req.TaskID, Assets: assets, EffectiveModelCode: effectiveModelCode}, nil
	}

	base = strings.TrimRight(base, "/")

	count := req.Count
	if count <= 0 {
		count = 1
	}
	dur := normalizeVideoDuration(intParam(req.Params, "duration", 6))
	aspect := strParam(req.Params, "aspect_ratio", "16:9")
	quality := strParam(req.Params, "quality", "")

	body := vidCreateReq{
		Model:       req.ModelCode,
		Prompt:      req.Prompt,
		NegPrompt:   req.NegPrompt,
		Duration:    dur,
		AspectRatio: aspect,
		N:           count,
		RefImages:   req.RefAssets,
	}
	payload, _ := json.Marshal(body)

	start := time.Now()
	createResp, err := p.do(ctx, http.MethodPost, base+"/v1/videos/generations", payload, req.Credential)
	if err != nil {
		return nil, err
	}

	// 同步直返
	if len(createResp.Data) > 0 {
		return &provider.Result{
			TaskID:  req.TaskID,
			Assets:  toAssets(createResp.Data, dur, aspect, quality),
			Latency: time.Since(start),
		}, nil
	}
	if createResp.TaskID == "" {
		return nil, fmt.Errorf("grok empty task_id and empty data")
	}

	// 异步：内部轮询
	period := pollInitialPeriod
	deadline := time.Now().Add(pollMaxDur)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(period):
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("grok task %s timeout", createResp.TaskID)
		}
		st, err := p.do(ctx, http.MethodGet, base+"/v1/videos/tasks/"+createResp.TaskID, nil, req.Credential)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(st.Status) {
		case "succeeded", "success", "completed", "done":
			if len(st.Data) == 0 {
				return nil, fmt.Errorf("grok task %s succeeded but empty data", createResp.TaskID)
			}
			return &provider.Result{
				TaskID:  req.TaskID,
				Assets:  toAssets(st.Data, dur, aspect, quality),
				Latency: time.Since(start),
			}, nil
		case "failed", "error", "cancelled":
			msg := st.Error
			if msg == "" {
				msg = "grok task failed"
			}
			return nil, fmt.Errorf("grok task %s: %s", createResp.TaskID, msg)
		}
		// queued / running / processing → 继续轮询
		period *= 2
		if period > pollMaxPeriod {
			period = pollMaxPeriod
		}
	}
}

func (p *Provider) do(ctx context.Context, method, url string, payload []byte, key string) (*vidResp, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("User-Agent", "kleinai/1.0")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("grok http: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("grok %d: %s", resp.StatusCode, snippet(raw, 240))
	}
	var out vidResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("grok decode: %w (raw=%s)", err, snippet(raw, 240))
	}
	return &out, nil
}

func toAssets(items []vidAsset, durSec int, aspect, quality string) []provider.Asset {
	out := make([]provider.Asset, 0, len(items))
	_, _, defaultWidth, defaultHeight := videoConfig("", aspect)
	for _, it := range items {
		a := provider.Asset{
			URL:        it.URL,
			ThumbURL:   it.ThumbURL,
			Width:      it.Width,
			Height:     it.Height,
			DurationMs: it.DurationMs,
			Mime:       it.Mime,
		}
		if a.DurationMs == 0 && durSec > 0 {
			a.DurationMs = durSec * 1000
		}
		if a.Mime == "" {
			a.Mime = "video/mp4"
		}
		if a.Width == 0 || a.Height == 0 {
			if defaultWidth > 0 && defaultHeight > 0 {
				a.Width, a.Height = defaultWidth, defaultHeight
			} else {
				a.Width, a.Height = 1280, 720
			}
		}
		out = append(out, a)
	}
	return out
}

// === helpers ===

func strParam(p map[string]any, key, def string) string {
	if p == nil {
		return def
	}
	if v, ok := p[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
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
	truncated := false
	if len(b) > n {
		b = b[:n]
		truncated = true
		// 截断点可能落在多字节 UTF-8 字符中间，退回到完整字符边界，
		// 否则结尾是半个字符（如 \xE5），写入 utf8mb4 列会报 Error 1366 Incorrect string value。
		for len(b) > 0 {
			if r, size := utf8.DecodeLastRune(b); r == utf8.RuneError && size <= 1 {
				b = b[:len(b)-1]
				continue
			}
			break
		}
	}
	// 兜底清除任意非法 UTF-8 字节（响应体可能是任意二进制），避免落库失败。
	s := strings.ToValidUTF8(string(b), "")
	if truncated {
		s += "...(truncated)"
	}
	return s
}
