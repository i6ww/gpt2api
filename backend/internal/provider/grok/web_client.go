package grok

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/pkg/outbound"
)

var grokCFCache = struct {
	sync.Mutex
	state  grokRuntimeCFState
	loaded time.Time
}{}

type grokRuntimeCFState struct {
	Cookies     string `json:"cookies"`
	CFClearance string `json:"cf_clearance"`
	UserAgent   string `json:"user_agent"`
	Browser     string `json:"browser"`
	UpdatedAt   int64  `json:"updated_at"`

	// x-statsig-id 反爬签名所需的「按构建变化」的常量。可在 grok_cf.json 里热更新，
	// 无需重新部署。提取方式见 grokStatsigIDFor 注释里的浏览器 console 片段。
	StatsigFingerprintHex string `json:"statsig_fingerprint_hex"` // 48 字节指纹（hex，96 hex 字符）
	StatsigSuffix         string `json:"statsig_suffix"`          // 被 sha256 进去的静态后缀串（构建相关）
	StatsigTrailer        *int   `json:"statsig_trailer"`         // 末尾常量字节，默认 3
	StatsigEpoch          int64  `json:"statsig_epoch"`           // 计数起点（秒），默认 2023-05-01 UTC
}

const (
	webBaseURL             = "https://grok.com"
	chatEndpoint           = "/rest/app-chat/conversations/new"
	uploadEndpoint         = "/rest/app-chat/upload-file"
	mediaEndpoint          = "/rest/media/post/create"
	mediaGetEndpoint       = "/rest/media/post/get"
	pipelineRunEndpoint    = "/rest/media/pipeline/run" // 免额度 6s i2v 通道（Imagine Pipeline）
	videoModelName         = "imagine-video-gen"
	grokUA                 = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
	imageMediaType         = "MEDIA_POST_TYPE_IMAGE"
	videoMediaType         = "MEDIA_POST_TYPE_VIDEO"
	defaultVideoSize       = "1280x720"
	defaultVideoMode       = "custom"
	defaultVideoResolution = "720p"
)

type chatModelParams struct {
	upstream string
	mode     string
}

var chatModels = map[string]chatModelParams{
	"grok-3":          {upstream: "grok-3", mode: "MODEL_MODE_GROK_3"},
	"grok-3-mini":     {upstream: "grok-3", mode: "MODEL_MODE_GROK_3_MINI_THINKING"},
	"grok-3-thinking": {upstream: "grok-3", mode: "MODEL_MODE_GROK_3_THINKING"},
	"grok-4":          {upstream: "grok-4", mode: "MODEL_MODE_GROK_4"},
	"grok-4-thinking": {upstream: "grok-4", mode: "MODEL_MODE_GROK_4_THINKING"},
	"grok-4-heavy":    {upstream: "grok-4", mode: "MODEL_MODE_HEAVY"},
	"grok-4.1-mini":   {upstream: "grok-4-1-thinking-1129", mode: "MODEL_MODE_GROK_4_1_MINI_THINKING"},
	"grok-4.1-fast":   {upstream: "grok-4-1-thinking-1129", mode: "MODEL_MODE_FAST"},
	"grok-4.1-expert": {upstream: "grok-4-1-thinking-1129", mode: "MODEL_MODE_EXPERT"},
	"grok-4.1-thinking": {
		upstream: "grok-4-1-thinking-1129",
		mode:     "MODEL_MODE_GROK_4_1_THINKING",
	},
	"grok-4.20-beta": {upstream: "grok-420", mode: "MODEL_MODE_GROK_420"},

	// Backward-compatible aliases used by the current frontend.
	"grok-4.20-fast":   {upstream: "grok-4-1-thinking-1129", mode: "MODEL_MODE_FAST"},
	"grok-4.20-auto":   {upstream: "grok-4", mode: "MODEL_MODE_GROK_4"},
	"grok-4.20-expert": {upstream: "grok-4-1-thinking-1129", mode: "MODEL_MODE_EXPERT"},
	"grok-4.20-heavy":  {upstream: "grok-4", mode: "MODEL_MODE_HEAVY"},
	"grok-4.3-beta":    {upstream: "grok-420", mode: "MODEL_MODE_GROK_420"},
}

// ChatModelIDs returns downstream chat models backed by Grok Web.
func ChatModelIDs() []string {
	return []string{
		"grok-4.20-fast",
		"grok-4.20-auto",
		"grok-4.20-expert",
		"grok-4.20-heavy",
		"grok-4.3-beta",
		"grok-3",
	}
}

func IsChatModel(modelCode string) bool {
	_, ok := chatModels[strings.ToLower(strings.TrimSpace(modelCode))]
	return ok
}

func ModeForChatModel(modelCode string) string {
	if params, ok := chatModels[strings.ToLower(strings.TrimSpace(modelCode))]; ok {
		return params.mode
	}
	return "MODEL_MODE_FAST"
}

func UpstreamForChatModel(modelCode string) string {
	if params, ok := chatModels[strings.ToLower(strings.TrimSpace(modelCode))]; ok {
		return params.upstream
	}
	return strings.TrimSpace(modelCode)
}

func NormalizeVideoModel(modelCode string) string {
	switch strings.ToLower(strings.TrimSpace(modelCode)) {
	case "", "vid-v1", "vid-i2v", "grok-video", "grok-i2v", "grok-imagine-video":
		return "grok-imagine-video"
	case "grok-imagine-video-6s-free", "grok-imagine-6s", "grok-imagine-free", "grok-pipeline-video":
		return "grok-imagine-video-6s-free"
	default:
		return modelCode
	}
}

// IsPipelineVideoModel 判断当前 video 模型是否要走免额度的 Imagine Pipeline 通道。
//   - true：走 /rest/media/pipeline/run（固定 6s, 不扣 credits, 不可调时长 / 比例）
//   - false：走 /rest/app-chat/conversations/new（6/10/20/30s + custom ratio, 扣 credits）
func IsPipelineVideoModel(modelCode string) bool {
	switch NormalizeVideoModel(modelCode) {
	case "grok-imagine-video-6s-free":
		return true
	default:
		return false
	}
}

// IsGrokQuotaError 把上游错误归类为「额度/限流」错误，主通道遇到这类错误时
// 调度层可以选择 fallback 到免额度的 pipeline 通道（i2v 至少能保住一次出图）。
//
// 命中规则（保守，避免把网络异常也当成限流）：
//   - 包含 "HTTP 429" / "HTTP 402"
//   - 包含 "quota" / "rate limit" / "rate-limited" / "credit" 关键词
//
// 取消 / 超时类错误不算（避免把网络抖动 fallback）。
func IsGrokQuotaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "deadline exceeded") {
		return false
	}
	switch {
	case strings.Contains(msg, "http 429"),
		strings.Contains(msg, "http 402"),
		strings.Contains(msg, "rate limit"),
		strings.Contains(msg, "rate-limited"),
		strings.Contains(msg, "ratelimit"),
		strings.Contains(msg, "quota"),
		strings.Contains(msg, "credit"),
		strings.Contains(msg, "insufficient"):
		return true
	}
	return false
}

type WebClient struct {
	baseURL        string
	proxyURL       string
	upstreamLogger provider.UpstreamLogger
}

func NewWebClient(base string) *WebClient {
	return NewWebClientWithProxy(base, "")
}

func NewWebClientWithProxy(base, proxyURL string) *WebClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || strings.Contains(base, "api.x.ai") {
		base = webBaseURL
	}
	return &WebClient{baseURL: base, proxyURL: strings.TrimSpace(proxyURL)}
}

func (c *WebClient) WithUpstreamLogger(logger provider.UpstreamLogger) *WebClient {
	if c == nil {
		return nil
	}
	clone := *c
	clone.upstreamLogger = logger
	return &clone
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatResult struct {
	Raw    []byte
	Status int
	Usage  *OpenAIUsage
}

func (c *WebClient) logUpstream(ctx context.Context, entry provider.UpstreamLogEntry) {
	if c == nil || c.upstreamLogger == nil {
		return
	}
	if entry.Provider == "" {
		entry.Provider = "grok"
	}
	c.upstreamLogger(ctx, entry)
}

func (c *WebClient) ChatComplete(ctx context.Context, token, modelCode string, body map[string]any) (*ChatResult, error) {
	prompt, files := buildGrokPromptAndFiles(body)
	reqBody := c.chatPayload(prompt, modelCode)
	if len(files) > 0 {
		attachments, err := c.uploadChatFiles(ctx, token, files)
		if err != nil {
			return nil, err
		}
		reqBody["fileAttachments"] = attachments
	}
	resp, err := c.doJSONStream(ctx, token, chatEndpoint, reqBody, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	resp.Body = decodeResponseBody(resp)
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return &ChatResult{Raw: raw, Status: resp.StatusCode}, nil
	}
	rawText, _, _, _, _, err := collectGrokStream(resp.Body, nil)
	if err != nil {
		return nil, err
	}
	rawText = cleanGrokText(rawText)
	usage := estimateOpenAIUsage(prompt, rawText)
	raw, _ := json.Marshal(map[string]any{
		"id":      "chatcmpl_" + shortID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelCode,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": rawText},
			"finish_reason": "stop",
		}},
		"usage": usage,
	})
	return &ChatResult{Raw: raw, Status: http.StatusOK, Usage: usage}, nil
}

func (c *WebClient) ChatStream(ctx context.Context, token, modelCode string, body map[string]any, w io.Writer, flusher http.Flusher) (*OpenAIUsage, error) {
	prompt, files := buildGrokPromptAndFiles(body)
	reqBody := c.chatPayload(prompt, modelCode)
	if len(files) > 0 {
		attachments, err := c.uploadChatFiles(ctx, token, files)
		if err != nil {
			return nil, err
		}
		reqBody["fileAttachments"] = attachments
	}
	resp, err := c.doJSONStream(ctx, token, chatEndpoint, reqBody, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	resp.Body = decodeResponseBody(resp)
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("grok chat HTTP %d: %s", resp.StatusCode, snippet(raw, 240))
	}
	var full strings.Builder
	_, _, _, _, _, err = collectGrokStream(resp.Body, func(delta string) {
		if delta == "" {
			return
		}
		if looksLikeXAIToolMarkup(delta) {
			return
		}
		full.WriteString(delta)
		payload, _ := json.Marshal(map[string]any{
			"id":      "chatcmpl_" + shortID(),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   modelCode,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": delta}}},
		})
		_, _ = io.WriteString(w, "data: "+string(payload)+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	if err != nil {
		return nil, err
	}
	usage := estimateOpenAIUsage(prompt, full.String())
	payload, _ := json.Marshal(map[string]any{
		"id":      "chatcmpl_" + shortID(),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   modelCode,
		"choices": []map[string]any{},
		"usage":   usage,
	})
	_, _ = io.WriteString(w, "data: "+string(payload)+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return usage, nil
}

type VideoRequest struct {
	ModelCode   string
	Prompt      string
	Refs        []string
	DurationSec int
	Size        string
	AspectRatio string
	Quality     string
	Count       int
}

type VideoAsset struct {
	URL        string
	ThumbURL   string
	Width      int
	Height     int
	DurationMs int
}

type uploadedVideoRef struct {
	fileID   string
	assetURL string
}

type grokUploadOptions struct {
	videoLegacyRef bool
}

func (c *WebClient) GenerateVideo(ctx context.Context, token string, req VideoRequest) ([]VideoAsset, error) {
	if req.DurationSec <= 0 {
		req.DurationSec = 6
	}
	// targetDuration: 用户/前端原始想要的总长度（6/10/20/30）。
	// > 10 的情况 web 上游单次调用拒绝（"Video duration must be between 1 and 10 seconds"），
	// 需要走「初始 10s + N×10s extension」串接，由 stitchWithExtendPostId=true 让上游一并合成完整 mp4。
	targetDuration := normalizeVideoDuration(req.DurationSec)
	firstSegment := targetDuration
	if firstSegment > 10 {
		firstSegment = 10
	}
	req.DurationSec = firstSegment
	// 阶段耗时埋点：refsT0 → prepareT0 → streamT0 → endT0，方便定位「慢在哪一段」。
	overallStart := time.Now()
	// aspect 选取规则：
	//   - 无参考图 (t2v)：按 req.AspectRatio / req.Size 算（videoAspectRatio）。
	//   - 单参考图 (i2v)：用户显式 ratio 优先，否则从参考图推断。
	//   - 多参考图：用户显式 ratio 优先，否则从首张参考图推断；都没有就空串走上游默认。
	aspect := ""
	switch {
	case len(req.Refs) == 0:
		aspect = videoAspectRatio(req.Size, req.AspectRatio)
	case len(req.Refs) >= 1:
		aspect = firstNonEmpty(strings.TrimSpace(req.AspectRatio), inferAspectRatioFromRef(req.Refs[0]))
	}
	resolution := defaultVideoResolution
	width, height := 0, 0
	c.logUpstream(ctx, provider.UpstreamLogEntry{
		Provider: "grok",
		Stage:    "video.start",
		Meta: map[string]any{
			"model":           req.ModelCode,
			"duration_sec":    req.DurationSec,
			"target_duration": targetDuration,
			"extension_count": (targetDuration - firstSegment) / 10,
			"aspect_ratio":    aspect,
			"resolution":      resolution,
			"size":            req.Size,
			"refs_count":      len(req.Refs),
			"has_proxy":       c.proxyURL != "",
			"has_ref_prompt":  strings.TrimSpace(req.Prompt) != "",
		},
	})

	parentPostID := ""
	refs := make([]uploadedVideoRef, 0, len(req.Refs))
	refsStart := time.Now()
	for _, ref := range req.Refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		uploaded, err := c.prepareVideoRef(ctx, token, ref)
		if err != nil {
			c.logUpstream(ctx, provider.UpstreamLogEntry{
				Provider: "grok",
				Stage:    "video.ref",
				Error:    err.Error(),
				Meta:     map[string]any{"ref_index": len(refs) + 1, "ref": sanitizeDiagURL(ref)},
			})
			return nil, err
		}
		if uploaded.assetURL != "" {
			refs = append(refs, uploaded)
		}
	}
	if len(refs) == 1 {
		imagePostID, err := c.createMediaPost(ctx, token, imageMediaType, "", refs[0].assetURL)
		if err != nil {
			c.logUpstream(ctx, provider.UpstreamLogEntry{Provider: "grok", Stage: "video.parent_post", Error: err.Error(), Meta: map[string]any{"media_type": imageMediaType, "refs_count": len(refs), "has_media_url": refs[0].assetURL != ""}})
			return nil, err
		}
		parentPostID = imagePostID
	}
	if len(refs) != 1 {
		var err error
		parentPostID, err = c.createMediaPost(ctx, token, videoMediaType, req.Prompt, "")
		if err != nil {
			c.logUpstream(ctx, provider.UpstreamLogEntry{Provider: "grok", Stage: "video.parent_post", Error: err.Error(), Meta: map[string]any{"media_type": videoMediaType, "refs_count": len(refs)}})
			return nil, err
		}
	}

	message := strings.TrimSpace(req.Prompt)
	if message == "" {
		message = "Generate a video"
	}
	fileAttachments := []any{}
	if len(refs) == 1 {
		message = strings.TrimSpace(refs[0].assetURL + "  " + message)
	} else if len(refs) > 1 {
		mentions := make([]string, 0, len(refs))
		for _, ref := range refs {
			if ref.fileID != "" {
				mentions = append(mentions, "@"+ref.fileID)
				fileAttachments = append(fileAttachments, ref.fileID)
			}
		}
		if len(mentions) > 0 {
			message = strings.TrimSpace(strings.Join(mentions, " ") + " " + message)
		}
	}
	message = strings.TrimSpace(message + " --mode=" + defaultVideoMode)
	// 首段视频请求严格对齐 grok.com 抓包：modelName=imagine-video-gen + 极简 body。
	// 之前复用 chatPayload("grok-3") 再挂 toolOverrides.videoGen 的老形态，字段又多又
	// 自相矛盾（视频请求里却带 enableImageGeneration / imageGenerationCount / isAsyncChat
	// 等聊天字段），被 anti-bot 识别为非官方客户端而概率性 403（尤其纯文生视频 t2v）。
	payload := c.buildVideoConversationPayload(videoConversationArgs{
		message:         message,
		parentPostID:    parentPostID,
		videoLength:     req.DurationSec,
		aspectRatio:     aspect,
		resolution:      resolution,
		refs:            refs,
		fileAttachments: fileAttachments,
	})
	refsElapsed := time.Since(refsStart).Milliseconds()
	c.logUpstream(ctx, provider.UpstreamLogEntry{
		Provider:       "grok",
		Stage:          "video.conversation.prepare",
		Method:         "POST",
		URL:            c.baseURL + chatEndpoint,
		RequestExcerpt: jsonSnippet(payload, 12000),
		Meta: map[string]any{
			"refs_count":         len(refs),
			"duration_sec":       req.DurationSec,
			"resolution":         resolution,
			"aspect_ratio":       aspect,
			"parent_post_id":     parentPostID,
			"refs_upload_ms":     refsElapsed,
			"elapsed_so_far_ms":  time.Since(overallStart).Milliseconds(),
		},
	})

	streamStart := time.Now()
	resp, err := c.doJSONStream(ctx, token, chatEndpoint, payload, 15*time.Minute)
	if err != nil {
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider:       "grok",
			Stage:          "video.conversation",
			Method:         "POST",
			URL:            c.baseURL + chatEndpoint,
			RequestExcerpt: jsonSnippet(payload, 600),
			Error:          err.Error(),
			Meta:           map[string]any{"refs_count": len(refs), "duration_sec": req.DurationSec, "resolution": resolution, "aspect_ratio": aspect},
		})
		return nil, err
	}
	resp.Body = decodeResponseBody(resp)
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider:        "grok",
			Stage:           "video.conversation",
			Method:          "POST",
			URL:             c.baseURL + chatEndpoint,
			StatusCode:      resp.StatusCode,
			RequestExcerpt:  jsonSnippet(payload, 600),
			ResponseExcerpt: snippet(raw, 600),
			Meta:            map[string]any{"refs_count": len(refs), "duration_sec": req.DurationSec, "resolution": resolution, "aspect_ratio": aspect},
		})
		return nil, fmt.Errorf("grok video HTTP %d: %s", resp.StatusCode, snippet(raw, 240))
	}
	var assets []VideoAsset
	_, videoURL, thumbURL, videoPostID, streamCandidates, err := collectGrokStream(resp.Body, func(_ string) {})
	if err != nil {
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider: "grok",
			Stage:    "video.stream",
			Error:    err.Error(),
			Meta:     map[string]any{"refs_count": len(refs), "duration_sec": req.DurationSec, "resolution": resolution, "aspect_ratio": aspect},
		})
		return nil, err
	}
	c.logUpstream(ctx, provider.UpstreamLogEntry{
		Provider: "grok",
		Stage:    "video.stream_result",
		Meta: map[string]any{
			"post_id":           videoPostID,
			"selected_video":    sanitizeDiagURL(videoURL),
			"selected_thumb":    sanitizeDiagURL(thumbURL),
			"video_candidates":  summarizeVideoCandidates(streamCandidates),
			"refs_count":        len(refs),
			"duration_sec":      req.DurationSec,
			"resolution":        resolution,
			"aspect_ratio":      aspect,
			"stream_ms":         time.Since(streamStart).Milliseconds(),
			"elapsed_total_ms":  time.Since(overallStart).Milliseconds(),
		},
	})
	if videoURL == "" && videoPostID != "" {
		videoURL, thumbURL, err = c.fetchVideoAssetFromPost(ctx, token, videoPostID, thumbURL)
		if err != nil {
			c.logUpstream(ctx, provider.UpstreamLogEntry{
				Provider: "grok",
				Stage:    "video.post_fetch",
				Error:    err.Error(),
				Meta:     map[string]any{"post_id": videoPostID, "fallback_thumb": sanitizeDiagURL(thumbURL)},
			})
			return nil, err
		}
	}
	if videoURL == "" {
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider: "grok",
			Stage:    "video.failed",
			Error:    "grok video finished without video url",
			Meta:     map[string]any{"refs_count": len(refs), "duration_sec": req.DurationSec, "resolution": resolution, "aspect_ratio": aspect, "parent_post_id": parentPostID},
		})
		return nil, fmt.Errorf("grok video finished without video url")
	}

	// 初始片段完成。若用户请求 >10s，则依 HAR 抓包逐段调用 extension：
	//   POST /rest/app-chat/conversations/new
	//     videoGenModelConfig.isVideoExtension = true
	//     videoGenModelConfig.extendPostId = <上一片段的 videoPostId>
	//     videoGenModelConfig.videoExtensionStartTime = <累计秒 + 一帧 0.031667>
	//     videoGenModelConfig.stitchWithExtendPostId = true（让上游把片段在服务端拼成一条 mp4）
	//     videoGenModelConfig.videoLength = 10
	//     videoGenModelConfig.mode = "normal"
	//     videoGenModelConfig.originalPostId = <最初片段的 videoPostId>（始终指向 root）
	//     videoGenModelConfig.parentPostId   = <上一片段的 videoPostId>
	//     videoGenModelConfig.originalRefType = "ORIGINAL_REF_TYPE_VIDEO_EXTENSION"
	// 每次 extension 的响应里都给出新的 stitched 视频 url / videoPostId，下一段再串。
	originalPostID := videoPostID
	prevPostID := videoPostID
	accumulated := firstSegment
	extensionsRun := 0
	finalVideoURL := videoURL
	finalThumbURL := thumbURL
	for accumulated < targetDuration && prevPostID != "" {
		chunk := targetDuration - accumulated
		if chunk > 10 {
			chunk = 10
		}
		startTime := float64(accumulated) + 0.031667
		extPayload := c.buildVideoExtensionPayload(extensionPayloadArgs{
			extendPostID:   prevPostID,
			originalPostID: originalPostID,
			startTime:      startTime,
			length:         chunk,
			aspectRatio:    aspect,
			resolution:     resolution,
		})
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider:       "grok",
			Stage:          "video.extension.prepare",
			Method:         "POST",
			URL:            c.baseURL + chatEndpoint,
			RequestExcerpt: jsonSnippet(extPayload, 4000),
			Meta: map[string]any{
				"extend_post_id":   prevPostID,
				"original_post_id": originalPostID,
				"start_time":       startTime,
				"chunk_sec":        chunk,
				"accumulated_sec":  accumulated,
				"target_sec":       targetDuration,
				"attempt":          extensionsRun + 1,
			},
		})
		extVideoURL, extThumbURL, extPostID, extErr := c.runVideoExtensionSegment(ctx, token, extPayload, extensionLogMeta{
			extendPostID:   prevPostID,
			originalPostID: originalPostID,
			startTime:      startTime,
			chunkSec:       chunk,
			accumulatedSec: accumulated,
			targetSec:      targetDuration,
			attempt:        extensionsRun + 1,
		})
		if extErr != nil {
			return nil, extErr
		}
		if extPostID == "" {
			return nil, fmt.Errorf("grok video extension finished without post id (attempt=%d)", extensionsRun+1)
		}
		if extVideoURL == "" {
			return nil, fmt.Errorf("grok video extension finished without video url (attempt=%d, post_id=%s)", extensionsRun+1, extPostID)
		}
		prevPostID = extPostID
		finalVideoURL = extVideoURL
		if extThumbURL != "" {
			finalThumbURL = extThumbURL
		}
		accumulated += chunk
		extensionsRun++
	}
	if finalVideoURL == "" {
		return nil, fmt.Errorf("grok video extension chain finished without final url")
	}
	if finalThumbURL == "" {
		finalThumbURL = derivePreviewImageURL(finalVideoURL)
	}
	assets = append(assets, VideoAsset{URL: finalVideoURL, ThumbURL: finalThumbURL, Width: width, Height: height, DurationMs: accumulated * 1000})

	c.logUpstream(ctx, provider.UpstreamLogEntry{
		Provider: "grok",
		Stage:    "video.success",
		Meta: map[string]any{
			"assets":            len(assets),
			"duration_sec":      accumulated,
			"target_duration":   targetDuration,
			"extensions_run":    extensionsRun,
			"resolution":        resolution,
			"aspect_ratio":      aspect,
			"parent_post_id":    parentPostID,
			"root_post_id":      originalPostID,
			"final_post_id":     prevPostID,
			"selected_video":    sanitizeDiagURL(finalVideoURL),
			"selected_thumb":    sanitizeDiagURL(finalThumbURL),
			"video_candidates":  summarizeVideoCandidates(streamCandidates),
			"elapsed_total_ms":  time.Since(overallStart).Milliseconds(),
		},
	})
	return assets, nil
}

type videoConversationArgs struct {
	message         string
	parentPostID    string
	videoLength     int
	aspectRatio     string
	resolution      string
	refs            []uploadedVideoRef
	fileAttachments []any
}

// buildVideoConversationPayload 构造首段视频生成的 conversations/new body，
// 严格对齐 grok.com 抓包（2026-06-14.har 第 46 条）：
//
//	{
//	  "temporary":true,
//	  "modelName":"imagine-video-gen",
//	  "message":"<assetURL>  <prompt> --mode=custom",
//	  "fileAttachments":["<imagePostId>"],
//	  "enableSideBySide":true,
//	  "responseMetadata":{
//	    "experiments":[],
//	    "modelConfigOverride":{"modelMap":{"videoGenModelConfig":{
//	      "parentPostId":"<imagePostId>","aspectRatio":"9:16",
//	      "videoLength":10,"resolutionName":"720p"}}}
//	  }
//	}
//
// 不再携带聊天时代字段（deviceEnvInfo / enableImageGeneration / imageGenerationCount /
// isAsyncChat / modelMode / toolOverrides 等），避免被 anti-bot 判为非官方客户端而 403。
func (c *WebClient) buildVideoConversationPayload(args videoConversationArgs) map[string]any {
	cfg := map[string]any{
		"parentPostId": args.parentPostID,
		"videoLength":  args.videoLength,
	}
	if strings.TrimSpace(args.aspectRatio) != "" {
		cfg["aspectRatio"] = args.aspectRatio
	}
	if strings.TrimSpace(args.resolution) != "" {
		cfg["resolutionName"] = args.resolution
	}
	if len(args.refs) > 1 {
		imageReferences := make([]string, 0, len(args.refs))
		for _, ref := range args.refs {
			if ref.assetURL != "" {
				imageReferences = append(imageReferences, ref.assetURL)
			}
		}
		cfg["isReferenceToVideo"] = true
		cfg["imageReferences"] = imageReferences
	}
	fileAttachments := args.fileAttachments
	if fileAttachments == nil {
		fileAttachments = []any{}
	}
	// 单图 i2v：把图片 post 作为附件，与浏览器一致（fileAttachments 与 parentPostId 同指）。
	if len(args.refs) == 1 && args.parentPostID != "" {
		fileAttachments = []any{args.parentPostID}
	}
	return map[string]any{
		"temporary":        true,
		"modelName":        videoModelName,
		"message":          args.message,
		"fileAttachments":  fileAttachments,
		"enableSideBySide": true,
		"responseMetadata": map[string]any{
			"experiments": []any{},
			"modelConfigOverride": map[string]any{
				"modelMap": map[string]any{
					"videoGenModelConfig": cfg,
				},
			},
		},
	}
}

type extensionPayloadArgs struct {
	extendPostID   string
	originalPostID string
	startTime      float64
	length         int
	aspectRatio    string
	resolution     string
}

type extensionLogMeta struct {
	extendPostID   string
	originalPostID string
	startTime      float64
	chunkSec       int
	accumulatedSec int
	targetSec      int
	attempt        int
}

// buildVideoExtensionPayload 复刻 grok.com 抓包中第二条 conversations/new 的 body。
// 见 grok.com.har 中 line 143948 附近：
//
//	{
//	  "temporary":true,
//	  "modelName":"imagine-video-gen",
//	  "message":"--mode=normal",
//	  "enableSideBySide":true,
//	  "responseMetadata":{
//	    "experiments":[],
//	    "modelConfigOverride":{
//	      "modelMap":{
//	        "videoGenModelConfig":{
//	          "isVideoExtension":true,
//	          "videoExtensionStartTime":10.031667,
//	          "extendPostId":"<prev>",
//	          "stitchWithExtendPostId":true,
//	          "originalPostId":"<root>",
//	          "originalRefType":"ORIGINAL_REF_TYPE_VIDEO_EXTENSION",
//	          "mode":"normal",
//	          "aspectRatio":"9:16",
//	          "videoLength":10,
//	          "resolutionName":"720p",
//	          "parentPostId":"<prev>",
//	          "isVideoEdit":false
//	        }
//	      }
//	    }
//	  }
//	}
func (c *WebClient) buildVideoExtensionPayload(args extensionPayloadArgs) map[string]any {
	cfg := map[string]any{
		"isVideoExtension":        true,
		"videoExtensionStartTime": args.startTime,
		"extendPostId":            args.extendPostID,
		"stitchWithExtendPostId":  true,
		"originalPostId":          args.originalPostID,
		"originalRefType":         "ORIGINAL_REF_TYPE_VIDEO_EXTENSION",
		"mode":                    "normal",
		"videoLength":             args.length,
		"parentPostId":            args.extendPostID,
		"isVideoEdit":             false,
	}
	if strings.TrimSpace(args.aspectRatio) != "" {
		cfg["aspectRatio"] = args.aspectRatio
	}
	if strings.TrimSpace(args.resolution) != "" {
		cfg["resolutionName"] = args.resolution
	}
	return map[string]any{
		"temporary":        true,
		"modelName":        videoModelName,
		"message":          "--mode=normal",
		"enableSideBySide": true,
		"responseMetadata": map[string]any{
			"experiments": []any{},
			"modelConfigOverride": map[string]any{
				"modelMap": map[string]any{
					"videoGenModelConfig": cfg,
				},
			},
		},
	}
}

func (c *WebClient) runVideoExtensionSegment(ctx context.Context, token string, payload map[string]any, meta extensionLogMeta) (string, string, string, error) {
	resp, err := c.doJSONStream(ctx, token, chatEndpoint, payload, 15*time.Minute)
	if err != nil {
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider:       "grok",
			Stage:          "video.extension",
			Method:         "POST",
			URL:            c.baseURL + chatEndpoint,
			RequestExcerpt: jsonSnippet(payload, 600),
			Error:          err.Error(),
			Meta: map[string]any{
				"extend_post_id":   meta.extendPostID,
				"original_post_id": meta.originalPostID,
				"start_time":       meta.startTime,
				"chunk_sec":        meta.chunkSec,
				"accumulated_sec":  meta.accumulatedSec,
				"target_sec":       meta.targetSec,
				"attempt":          meta.attempt,
			},
		})
		return "", "", "", err
	}
	resp.Body = decodeResponseBody(resp)
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider:        "grok",
			Stage:           "video.extension",
			Method:          "POST",
			URL:             c.baseURL + chatEndpoint,
			StatusCode:      resp.StatusCode,
			RequestExcerpt:  jsonSnippet(payload, 600),
			ResponseExcerpt: snippet(raw, 600),
			Meta: map[string]any{
				"extend_post_id":   meta.extendPostID,
				"original_post_id": meta.originalPostID,
				"start_time":       meta.startTime,
				"chunk_sec":        meta.chunkSec,
				"accumulated_sec":  meta.accumulatedSec,
				"target_sec":       meta.targetSec,
				"attempt":          meta.attempt,
			},
		})
		return "", "", "", fmt.Errorf("grok video extension HTTP %d: %s", resp.StatusCode, snippet(raw, 240))
	}
	_, videoURL, thumbURL, videoPostID, streamCandidates, err := collectGrokStream(resp.Body, func(_ string) {})
	if err != nil {
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider: "grok",
			Stage:    "video.extension.stream",
			Error:    err.Error(),
			Meta: map[string]any{
				"extend_post_id":   meta.extendPostID,
				"original_post_id": meta.originalPostID,
				"start_time":       meta.startTime,
				"chunk_sec":        meta.chunkSec,
				"accumulated_sec":  meta.accumulatedSec,
				"target_sec":       meta.targetSec,
				"attempt":          meta.attempt,
			},
		})
		return "", "", "", err
	}
	c.logUpstream(ctx, provider.UpstreamLogEntry{
		Provider: "grok",
		Stage:    "video.extension.stream_result",
		Meta: map[string]any{
			"post_id":          videoPostID,
			"extend_post_id":   meta.extendPostID,
			"original_post_id": meta.originalPostID,
			"start_time":       meta.startTime,
			"chunk_sec":        meta.chunkSec,
			"accumulated_sec":  meta.accumulatedSec,
			"target_sec":       meta.targetSec,
			"attempt":          meta.attempt,
			"selected_video":   sanitizeDiagURL(videoURL),
			"selected_thumb":   sanitizeDiagURL(thumbURL),
			"video_candidates": summarizeVideoCandidates(streamCandidates),
		},
	})
	if videoURL == "" && videoPostID != "" {
		videoURL, thumbURL, err = c.fetchVideoAssetFromPost(ctx, token, videoPostID, thumbURL)
		if err != nil {
			c.logUpstream(ctx, provider.UpstreamLogEntry{
				Provider: "grok",
				Stage:    "video.extension.post_fetch",
				Error:    err.Error(),
				Meta: map[string]any{
					"post_id":          videoPostID,
					"extend_post_id":   meta.extendPostID,
					"original_post_id": meta.originalPostID,
					"chunk_sec":        meta.chunkSec,
					"attempt":          meta.attempt,
				},
			})
			return "", "", "", err
		}
	}
	return videoURL, thumbURL, videoPostID, nil
}

func (c *WebClient) fetchVideoAssetFromPost(ctx context.Context, token, postID, fallbackThumb string) (string, string, error) {
	postID = strings.TrimSpace(postID)
	if postID == "" {
		return "", "", nil
	}
	var lastErr error
	lastStatus := 0
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(mediaPostGetRetryDelay(attempt, lastStatus)):
			}
		}
		resp, err := c.doJSON(ctx, token, mediaGetEndpoint, map[string]any{"id": postID}, 45*time.Second)
		if err != nil {
			lastErr = err
			lastStatus = 0
			continue
		}
		resp.Body = decodeResponseBody(resp)
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		lastStatus = resp.StatusCode
		if resp.StatusCode/100 != 2 {
			lastErr = fmt.Errorf("grok media post get HTTP %d: %s", resp.StatusCode, snippet(raw, 240))
			continue
		}
		var obj any
		if err := json.Unmarshal(raw, &obj); err != nil {
			lastErr = err
			continue
		}
		videoURL, candidates := bestVideoURL(obj)
		if videoURL != "" {
			thumbURL := firstStringByKeys(obj, []string{"thumbnailImageUrl", "thumbnailUrl", "coverUrl"})
			if thumbURL == "" {
				thumbURL = fallbackThumb
			}
			c.logUpstream(ctx, provider.UpstreamLogEntry{
				Provider: "grok",
				Stage:    "video.post_fetch_result",
				Meta: map[string]any{
					"post_id":          postID,
					"selected_video":   sanitizeDiagURL(videoURL),
					"selected_thumb":   sanitizeDiagURL(thumbURL),
					"video_candidates": summarizeVideoCandidates(candidates),
					"response_excerpt": snippet(raw, 600),
				},
			})
			return normalizeAssetURL(videoURL), normalizeAssetURL(thumbURL), nil
		}
		lastErr = fmt.Errorf("grok media post get missing video url")
	}
	return "", fallbackThumb, lastErr
}

func mediaPostGetRetryDelay(attempt int, lastStatus int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	if lastStatus == http.StatusNotFound {
		delays := []time.Duration{4, 6, 8, 10, 12, 15, 18}
		idx := attempt - 1
		if idx >= len(delays) {
			idx = len(delays) - 1
		}
		return delays[idx] * time.Second
	}
	delay := time.Duration(attempt*4) * time.Second
	if delay > 20*time.Second {
		return 20 * time.Second
	}
	return delay
}

func (c *WebClient) prepareVideoRef(ctx context.Context, token, ref string) (uploadedVideoRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return uploadedVideoRef{}, nil
	}
	if strings.HasPrefix(ref, "data:") {
		fileID, assetURL, err := c.uploadDataURLMetaWithOptions(ctx, token, ref, grokUploadOptions{videoLegacyRef: true})
		if err != nil {
			return uploadedVideoRef{}, err
		}
		return uploadedVideoRef{fileID: fileID, assetURL: assetURL}, nil
	}
	if strings.HasPrefix(ref, "/api/v1/gen/cached/") {
		fileID, assetURL, err := c.uploadCachedLocalImageMetaWithOptions(ctx, token, ref, grokUploadOptions{videoLegacyRef: true})
		if err != nil {
			return uploadedVideoRef{}, err
		}
		return uploadedVideoRef{fileID: fileID, assetURL: assetURL}, nil
	}
	if !isGrokAssetURL(ref) {
		fileID, assetURL, err := c.uploadRemoteImageMetaWithOptions(ctx, token, ref, grokUploadOptions{videoLegacyRef: true})
		if err != nil {
			return uploadedVideoRef{}, err
		}
		return uploadedVideoRef{fileID: fileID, assetURL: assetURL}, nil
	}
	return uploadedVideoRef{assetURL: normalizeAssetURL(ref)}, nil
}

// GeneratePipelineVideo 走 grok.com 的免额度 Imagine Pipeline 通道。
//
// 端点：POST /rest/media/pipeline/run
// 输入：单张参考图 + 文本 prompt（参考图必须先上传到 assets.grok.com 拿到 assetUrl）。
// 输出：服务端固定 6 秒视频，分辨率服务端定（HAR 抓包看到 480×640 / 400×736 等竖屏）。
//
// 关键事实（从 HAR 推断 & template 字段 creditCost=0）：
//   - 该通道 不 计 credits / quota，是限流/扣费旁路的最佳备用接口；
//   - specJson 没有 `videoLength` 字段，传也无效，时长固定 6s；
//   - 没有 aspectRatio / resolution 入参，输出方向由参考图决定。
//
// 响应是 NDJSON 流，逐帧 `{result:{pipelineStatus, overallProgressPct, steps:[...], post?: {...}}}`，
// 最后一帧的 post.mediaUrl / thumbnailImageUrl 是最终资产。
func (c *WebClient) GeneratePipelineVideo(ctx context.Context, token string, req VideoRequest) ([]VideoAsset, error) {
	if len(req.Refs) == 0 {
		return nil, fmt.Errorf("grok pipeline video requires at least one reference image")
	}
	overallStart := time.Now()

	c.logUpstream(ctx, provider.UpstreamLogEntry{
		Provider: "grok",
		Stage:    "video.pipeline.start",
		Meta: map[string]any{
			"model":          req.ModelCode,
			"refs_count":     len(req.Refs),
			"has_proxy":      c.proxyURL != "",
			"has_ref_prompt": strings.TrimSpace(req.Prompt) != "",
			"endpoint":       pipelineRunEndpoint,
			"channel":        "imagine_pipeline",
		},
	})

	uploaded, err := c.prepareVideoRef(ctx, token, strings.TrimSpace(req.Refs[0]))
	if err != nil {
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider: "grok",
			Stage:    "video.pipeline.ref",
			Error:    err.Error(),
		})
		return nil, err
	}
	if uploaded.assetURL == "" {
		return nil, fmt.Errorf("grok pipeline video: prepareVideoRef returned empty assetURL")
	}
	refElapsed := time.Since(overallStart).Milliseconds()

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "Animate this photo"
	}
	// HAR 实测的 spec：version=1，photo+video_prompt 两个 input，单个 video_gen 节点。
	spec := map[string]any{
		"version": 1,
		"inputs": map[string]any{
			"photo": map[string]any{
				"type":  "image",
				"label": "Your photo",
			},
			"video_prompt": map[string]any{
				"type": "text",
				"fixed": map[string]any{
					"type":  "text",
					"value": prompt,
				},
			},
		},
		"nodes": map[string]any{
			"gen_video": map[string]any{
				"type": "video_gen",
				"inputs": map[string]any{
					"image":  "$input.photo",
					"prompt": "$input.video_prompt",
				},
			},
		},
		"outputs": map[string]any{
			"result": "$gen_video.video",
		},
	}
	specJSON, _ := json.Marshal(spec)
	body := map[string]any{
		"inputs":   []map[string]any{{"name": "photo", "imageUrl": uploaded.assetURL}},
		"specJson": string(specJSON),
	}

	c.logUpstream(ctx, provider.UpstreamLogEntry{
		Provider:       "grok",
		Stage:          "video.pipeline.run",
		Method:         "POST",
		URL:            c.baseURL + pipelineRunEndpoint,
		RequestExcerpt: jsonSnippet(body, 4096),
		Meta: map[string]any{
			"refs_upload_ms":    refElapsed,
			"elapsed_so_far_ms": time.Since(overallStart).Milliseconds(),
		},
	})

	streamStart := time.Now()
	resp, err := c.doJSONStream(ctx, token, pipelineRunEndpoint, body, 10*time.Minute)
	if err != nil {
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider: "grok", Stage: "video.pipeline.run", Error: err.Error(),
		})
		return nil, err
	}
	resp.Body = decodeResponseBody(resp)
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider:        "grok",
			Stage:           "video.pipeline.run",
			Method:          "POST",
			URL:             c.baseURL + pipelineRunEndpoint,
			StatusCode:      resp.StatusCode,
			ResponseExcerpt: snippet(raw, 600),
		})
		return nil, fmt.Errorf("grok pipeline HTTP %d: %s", resp.StatusCode, snippet(raw, 240))
	}

	asset, err := parsePipelineNDJSON(resp.Body)
	if err != nil {
		c.logUpstream(ctx, provider.UpstreamLogEntry{
			Provider: "grok", Stage: "video.pipeline.parse", Error: err.Error(),
		})
		return nil, err
	}
	if asset.URL == "" {
		return nil, fmt.Errorf("grok pipeline finished without video url")
	}

	c.logUpstream(ctx, provider.UpstreamLogEntry{
		Provider: "grok",
		Stage:    "video.pipeline.success",
		Meta: map[string]any{
			"selected_video":   sanitizeDiagURL(asset.URL),
			"selected_thumb":   sanitizeDiagURL(asset.ThumbURL),
			"resolution":       fmt.Sprintf("%dx%d", asset.Width, asset.Height),
			"channel":          "imagine_pipeline",
			"stream_ms":        time.Since(streamStart).Milliseconds(),
			"elapsed_total_ms": time.Since(overallStart).Milliseconds(),
		},
	})
	return []VideoAsset{asset}, nil
}

// parsePipelineNDJSON 解析 /rest/media/pipeline/run 的 NDJSON 流。
//
// 每行一个 JSON：{"result":{"pipelineStatus":"...", "overallProgressPct":N, "steps":[...], "post"?: {...}}}
// 我们只关心最后一行 post 字段里的 mediaUrl / thumbnailImageUrl / resolution。
func parsePipelineNDJSON(r io.Reader) (VideoAsset, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 8<<20)
	var asset VideoAsset
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var frame struct {
			Result struct {
				Post *struct {
					MediaURL          string `json:"mediaUrl"`
					ThumbnailImageURL string `json:"thumbnailImageUrl"`
					Resolution        struct {
						Width  int `json:"width"`
						Height int `json:"height"`
					} `json:"resolution"`
				} `json:"post,omitempty"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			continue
		}
		if frame.Result.Post == nil {
			continue
		}
		asset = VideoAsset{
			URL:      frame.Result.Post.MediaURL,
			ThumbURL: frame.Result.Post.ThumbnailImageURL,
			Width:    frame.Result.Post.Resolution.Width,
			Height:   frame.Result.Post.Resolution.Height,
		}
	}
	if err := scanner.Err(); err != nil && asset.URL == "" {
		return asset, fmt.Errorf("scan pipeline ndjson: %w", err)
	}
	return asset, nil
}

func (c *WebClient) chatPayload(message, modelCode string) map[string]any {
	upstreamModel := UpstreamForChatModel(modelCode)
	return map[string]any{
		"deviceEnvInfo":               map[string]any{"darkModeEnabled": false, "devicePixelRatio": 2, "screenHeight": 1329, "screenWidth": 2056, "viewportHeight": 1083, "viewportWidth": 2056},
		"disableMemory":               true,
		"disableSearch":               true,
		"disableSelfHarmShortCircuit": false,
		"disableTextFollowUps":        false,
		"enableImageGeneration":       true,
		"enableImageStreaming":        true,
		"enableSideBySide":            true,
		"fileAttachments":             []any{},
		"forceConcise":                false,
		"forceSideBySide":             false,
		"imageAttachments":            []any{},
		"imageGenerationCount":        2,
		"isAsyncChat":                 false,
		"isReasoning":                 false,
		"message":                     message,
		"modelMode":                   ModeForChatModel(modelCode),
		"modelName":                   upstreamModel,
		"responseMetadata":            map[string]any{"requestModelDetails": map[string]any{"modelId": upstreamModel}},
		"returnImageBytes":            false,
		"returnRawGrokInXaiRequest":   false,
		"sendFinalMetadata":           true,
		"temporary":                   true,
		"toolOverrides":               map[string]any{"webSearch": false, "xSearch": false, "x_keyword_search": false},
		"enable420":                   upstreamModel == "grok-420",
	}
}

func (c *WebClient) doJSONStream(ctx context.Context, token, endpoint string, body map[string]any, timeout time.Duration) (*http.Response, error) {
	payload, _ := json.Marshal(body)
	client, err := outbound.NewClient(outbound.Options{Timeout: timeout, ProxyURL: c.proxyURL, Mode: outbound.ModeUTLS, Profile: outbound.ProfileChrome})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.setGrokHeaders(ctx, req, token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json, */*")
	return client.Do(req)
}

func (c *WebClient) createMediaPost(ctx context.Context, token, mediaType, prompt, mediaURL string) (string, error) {
	body := map[string]any{"mediaType": mediaType, "prompt": prompt}
	if mediaURL != "" {
		body["mediaUrl"] = mediaURL
	}
	resp, err := c.doJSON(ctx, token, mediaEndpoint, body, 30*time.Second)
	if err != nil {
		return "", err
	}
	resp.Body = decodeResponseBody(resp)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("grok media post HTTP %d: %s", resp.StatusCode, snippet(raw, 240))
	}
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)
	for _, key := range []string{"postId", "id", "mediaPostId"} {
		if s, _ := obj[key].(string); s != "" {
			return s, nil
		}
	}
	if s := firstStringByKey(obj, "id"); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("grok media post missing id: %s", snippet(raw, 240))
}

func (c *WebClient) doJSON(ctx context.Context, token, endpoint string, body map[string]any, timeout time.Duration) (*http.Response, error) {
	payload, _ := json.Marshal(body)
	client, err := outbound.NewClient(outbound.Options{Timeout: timeout, ProxyURL: c.proxyURL, Mode: outbound.ModeUTLS, Profile: outbound.ProfileChrome})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.setGrokHeaders(ctx, req, token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, */*")
	return client.Do(req)
}

func (c *WebClient) uploadDataURL(ctx context.Context, token, dataURL string) (string, error) {
	_, url, err := c.uploadDataURLMeta(ctx, token, dataURL)
	if err != nil {
		return "", err
	}
	return url, nil
}

// UploadProbeImage exposes the existing Grok upload path for diagnostics.
func (c *WebClient) UploadProbeImage(ctx context.Context, token, dataURL string) (string, string, error) {
	return c.uploadDataURLMeta(ctx, token, dataURL)
}

func (c *WebClient) uploadDataURLMeta(ctx context.Context, token, dataURL string) (string, string, error) {
	return c.uploadDataURLMetaWithOptions(ctx, token, dataURL, grokUploadOptions{})
}

func (c *WebClient) uploadDataURLMetaWithOptions(ctx context.Context, token, dataURL string, opts grokUploadOptions) (string, string, error) {
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return "", "", fmt.Errorf("invalid data url")
	}
	meta, b64 := dataURL[:comma], dataURL[comma+1:]
	mimeType := "image/png"
	if strings.HasPrefix(meta, "data:") {
		if semi := strings.Index(meta, ";"); semi > len("data:") {
			mimeType = meta[len("data:"):semi]
		}
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", "", fmt.Errorf("decode data url: %w", err)
	}
	return c.uploadImageBytesMetaWithOptions(ctx, token, mimeType, data, opts)
}

func (c *WebClient) uploadCachedLocalImageMeta(ctx context.Context, token, cachedURL string) (string, string, error) {
	return c.uploadCachedLocalImageMetaWithOptions(ctx, token, cachedURL, grokUploadOptions{})
}

func (c *WebClient) uploadCachedLocalImageMetaWithOptions(ctx context.Context, token, cachedURL string, opts grokUploadOptions) (string, string, error) {
	rel := strings.TrimPrefix(strings.TrimSpace(cachedURL), "/api/v1/gen/cached/")
	if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", "", fmt.Errorf("invalid cached reference path")
	}
	root := strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT"))
	if root == "" {
		root = "/app/storage/public"
	}
	filePath := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", fmt.Errorf("read cached reference image: %w", err)
	}
	if len(data) == 0 {
		return "", "", fmt.Errorf("empty cached reference image")
	}
	mimeType := mime.TypeByExtension(filepath.Ext(filePath))
	if mimeType == "" {
		mimeType = detectImageMime(data)
	}
	return c.uploadImageBytesMetaWithOptions(ctx, token, mimeType, data, opts)
}

func (c *WebClient) uploadRemoteImageMeta(ctx context.Context, token, imageURL string) (string, string, error) {
	return c.uploadRemoteImageMetaWithOptions(ctx, token, imageURL, grokUploadOptions{})
}

func (c *WebClient) uploadRemoteImageMetaWithOptions(ctx context.Context, token, imageURL string, opts grokUploadOptions) (string, string, error) {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return "", "", fmt.Errorf("empty image url")
	}
	client, err := outbound.NewClient(outbound.Options{Timeout: 90 * time.Second, ProxyURL: c.proxyURL, Mode: outbound.ModeUTLS, Profile: outbound.ProfileChrome})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("download reference image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("download reference image HTTP %d: %s", resp.StatusCode, snippet(raw, 180))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		return "", "", fmt.Errorf("read reference image: %w", err)
	}
	if len(raw) == 0 {
		return "", "", fmt.Errorf("empty reference image")
	}
	mimeType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mimeType == "" || !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		mimeType = detectImageMime(raw)
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return "", "", fmt.Errorf("reference is not image: %s", mimeType)
	}
	return c.uploadImageBytesMetaWithOptions(ctx, token, mimeType, raw, opts)
}

func (c *WebClient) uploadImageBytesMeta(ctx context.Context, token, mimeType string, data []byte) (string, string, error) {
	return c.uploadImageBytesMetaWithOptions(ctx, token, mimeType, data, grokUploadOptions{})
}

func (c *WebClient) uploadImageBytesMetaWithOptions(ctx context.Context, token, mimeType string, data []byte, opts grokUploadOptions) (string, string, error) {
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = detectImageMime(data)
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") || strings.EqualFold(mimeType, "application/octet-stream") {
		if detected := detectImageMime(data); strings.HasPrefix(strings.ToLower(detected), "image/") {
			mimeType = detected
		}
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return "", "", fmt.Errorf("unsupported image mime: %s", mimeType)
	}
	if shouldNormalizeGrokUpload() {
		if normalized, ok := normalizeImageForGrokUploadWithOptions(data, opts); ok {
			data = normalized
			mimeType = "image/jpeg"
		}
	}
	exts, _ := mime.ExtensionsByType(mimeType)
	ext := ".png"
	if len(exts) > 0 {
		ext = exts[0]
	}
	client, err := outbound.NewClient(outbound.Options{Timeout: 2 * time.Minute, ProxyURL: c.proxyURL, Mode: outbound.ModeUTLS, Profile: outbound.ProfileChrome})
	if err != nil {
		return "", "", err
	}
	payload, _ := json.Marshal(map[string]any{
		"fileName":     "image" + filepath.Ext(ext),
		"fileMimeType": mimeType,
		"content":      base64.StdEncoding.EncodeToString(data),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+uploadEndpoint, bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	c.setGrokHeaders(ctx, req, token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	resp.Body = decodeResponseBody(resp)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("grok upload HTTP %d: %s", resp.StatusCode, snippet(raw, 240))
	}
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)
	fileID := firstStringByKeys(obj, []string{"fileMetadataId", "file_id", "fileId", "id"})
	for _, key := range []string{"fileUri", "file_uri", "fileUrl", "url", "mediaUrl"} {
		if s, _ := obj[key].(string); s != "" {
			return fileID, normalizeAssetURL(s), nil
		}
	}
	return fileID, normalizeAssetURL(firstStringByKey(obj, "url")), nil
}

func (c *WebClient) uploadChatFiles(ctx context.Context, token string, files []string) ([]any, error) {
	out := make([]any, 0, len(files))
	for _, item := range files {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "data:") {
			fileID, fileURL, err := c.uploadDataURLMeta(ctx, token, item)
			if err != nil {
				return nil, err
			}
			if fileID != "" {
				out = append(out, fileID)
			} else if fileURL != "" {
				out = append(out, fileURL)
			}
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func jsonSnippet(v any, limit int) string {
	raw, _ := json.Marshal(v)
	return snippet(raw, limit)
}

func sanitizeDiagURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if i := strings.Index(rawURL, "?"); i >= 0 {
		rawURL = rawURL[:i]
	}
	return rawURL
}

func normalizeAssetURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return v
	}
	if strings.HasPrefix(v, "/") {
		return "https://assets.grok.com" + v
	}
	return "https://assets.grok.com/" + v
}

func isGrokAssetURL(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return strings.Contains(v, "://assets.grok.com/") || strings.Contains(v, "://imagine-public.x.ai/")
}

func detectImageMime(data []byte) string {
	if len(data) >= 12 {
		switch {
		case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
			return "image/png"
		case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
			return "image/jpeg"
		case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
			return "image/gif"
		case bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
			return "image/webp"
		case bytes.Equal(data[4:8], []byte("ftyp")) && (bytes.Equal(data[8:12], []byte("avif")) || bytes.Equal(data[8:12], []byte("avis"))):
			return "image/avif"
		case bytes.Equal(data[4:8], []byte("ftyp")) && (bytes.Equal(data[8:12], []byte("heic")) || bytes.Equal(data[8:12], []byte("heix")) || bytes.Equal(data[8:12], []byte("hevc")) || bytes.Equal(data[8:12], []byte("hevx"))):
			return "image/heic"
		}
	}
	if len(data) >= 4 {
		switch {
		case bytes.HasPrefix(data, []byte("BM")):
			return "image/bmp"
		case bytes.HasPrefix(data, []byte("II*\x00")) || bytes.HasPrefix(data, []byte("MM\x00*")):
			return "image/tiff"
		}
	}
	return http.DetectContentType(data)
}

func (c *WebClient) setGrokHeaders(ctx context.Context, req *http.Request, token string) {
	cookie := buildGrokCookie(token)
	ua := grokUserAgent()
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Baggage", "sentry-environment=production,sentry-release=d6add6fb0460641fd482d767a335ef72b9b6abb8,sentry-public_key=b311e0f2690c81f25e2c4cf6d4f7ce1c")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", webBaseURL)
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Priority", "u=1, i")
	req.Header.Set("Referer", webBaseURL+"/")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Sec-Ch-Ua", grokSecCHUA(ua))
	req.Header.Set("Sec-Ch-Ua-Arch", "x86")
	req.Header.Set("Sec-Ch-Ua-Bitness", "64")
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Model", "")
	req.Header.Set("Sec-Ch-Ua-Platform", grokSecPlatform(ua))
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	fp := c.grokFingerprint(ctx, token)
	sid := grokStatsigIDWithFingerprint(req.Method, req.URL.Path, fp)
	zap.L().Info("grok.statsig.set", zap.String("path", req.URL.Path), zap.Int("fp_len", len(fp)), zap.Int("sid_len", len(sid)))
	req.Header.Set("X-Statsig-ID", sid)
	req.Header.Set("X-XAI-Request-ID", uuid.NewString())
}

func normalizeGrokToken(cred string) string {
	cred = strings.TrimSpace(cred)
	if strings.Contains(cred, "sso=") {
		parts := strings.Split(cred, ";")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "sso=") {
				return strings.TrimPrefix(p, "sso=")
			}
		}
	}
	return cred
}

func buildGrokCookie(cred string) string {
	cred = strings.TrimSpace(cred)
	state := readGrokRuntimeCFState()
	cf := strings.TrimSpace(firstNonEmpty(state.CFClearance, os.Getenv("KLEIN_GROK_CF_CLEARANCE")))
	extraCookies := normalizeCookieEnv(firstNonEmpty(state.Cookies, os.Getenv("KLEIN_GROK_CF_COOKIES")))
	if strings.Contains(cred, "=") {
		if !strings.Contains(cred, "sso-rw=") {
			token := normalizeGrokToken(cred)
			if token != "" {
				cred = strings.TrimRight(cred, "; ") + "; sso-rw=" + token
			}
		}
		if cf != "" && !strings.Contains(cred, "cf_clearance=") {
			cred = strings.TrimRight(cred, "; ") + "; cf_clearance=" + cf
		}
		return appendMissingCookies(cred, extraCookies)
	}
	cookie := "sso=" + cred + "; sso-rw=" + cred
	if cf != "" {
		cookie += "; cf_clearance=" + cf
	}
	return appendMissingCookies(cookie, extraCookies)
}

func readGrokRuntimeCFState() grokRuntimeCFState {
	grokCFCache.Lock()
	defer grokCFCache.Unlock()
	if time.Since(grokCFCache.loaded) < 30*time.Second {
		return grokCFCache.state
	}
	grokCFCache.loaded = time.Now()
	grokCFCache.state = grokRuntimeCFState{}
	path := strings.TrimSpace(os.Getenv("KLEIN_GROK_CF_STATE_PATH"))
	if path == "" {
		path = "/app/storage/grok_cf.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return grokCFCache.state
	}
	_ = json.Unmarshal(raw, &grokCFCache.state)
	return grokCFCache.state
}

func grokUserAgent() string {
	state := readGrokRuntimeCFState()
	if ua := strings.TrimSpace(firstNonEmpty(state.UserAgent, os.Getenv("KLEIN_GROK_USER_AGENT"))); ua != "" {
		return ua
	}
	return grokUA
}

func grokSecCHUA(ua string) string {
	v := "136"
	if m := regexp.MustCompile(`(?:Chrome|Chromium)/(\d+)`).FindStringSubmatch(ua); len(m) == 2 {
		v = m[1]
	}
	return fmt.Sprintf(`"Google Chrome";v="%s", "Chromium";v="%s", "Not(A:Brand";v="24"`, v, v)
}

func grokSecPlatform(ua string) string {
	ua = strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "windows"):
		return `"Windows"`
	case strings.Contains(ua, "mac os"):
		return `"macOS"`
	case strings.Contains(ua, "linux"):
		return `"Linux"`
	default:
		return `"Windows"`
	}
}

// grok x-statsig-id 反爬签名常量。算法是从真实浏览器抓包逆出来的（见 grok.com.har + 实测）：
//
//	70 字节 token = [rnd] + XOR_rnd( fingerprint[48] + le32(counter) + sha256(...)[0:16] + trailer[1] )
//
// 关键事实（已实测确认）：
//   - fingerprint[48] == base64decode(<meta name="grok-site-verification">)，由 grok.com 每次页面加载下发，
//     服务端按它下发的 meta 校验。因此我们必须用「实时从 grok.com 抓的 meta」当指纹，而不能写死。
//   - hash16 里混入了 Math.random()/设备信息且不回传，服务端无法重算，故只做结构性校验（指纹 + 计数新鲜度 + trailer）。
//   - trailer 恒为 3，counter = unix_now - epoch(2023-05-01)。
//
// defaultStatsigFingerprintHex 仅作为「抓不到 meta」时的最后兜底（多半已失效）。
const (
	defaultStatsigFingerprintHex = "ec13c7ab0d53fc97cdb63dea78cfd9faf04e7107a671ea2d6e84fd586f27ee335c10c908acf132a32cc193c950823f15"
	defaultStatsigTrailer        = 3
	defaultStatsigEpoch          = int64(1682924400) // 2023-05-01 00:00:00 UTC
)

// grokSiteVerifRe 从 grok.com 首页 HTML 里抓 grok-site-verification meta（容忍属性顺序）。
var (
	grokSiteVerifRe1 = regexp.MustCompile(`(?i)name=["']grok-site-verification["'][^>]*content=["']([^"']+)["']`)
	grokSiteVerifRe2 = regexp.MustCompile(`(?i)content=["']([^"']+)["'][^>]*name=["']grok-site-verification["']`)
)

var grokFpCache = struct {
	sync.Mutex
	m map[string]grokFpEntry
}{m: map[string]grokFpEntry{}}

type grokFpEntry struct {
	fp []byte
	at time.Time
}

func grokStatsigTrailer() byte {
	state := readGrokRuntimeCFState()
	if state.StatsigTrailer != nil {
		return byte(*state.StatsigTrailer)
	}
	if v := strings.TrimSpace(os.Getenv("KLEIN_GROK_STATSIG_TRAILER")); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			return byte(n)
		}
	}
	return byte(defaultStatsigTrailer)
}

func grokStatsigEpoch() int64 {
	state := readGrokRuntimeCFState()
	if state.StatsigEpoch > 0 {
		return state.StatsigEpoch
	}
	if v := strings.TrimSpace(os.Getenv("KLEIN_GROK_STATSIG_EPOCH")); v != "" {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil && n > 0 {
			return n
		}
	}
	return defaultStatsigEpoch
}

// grokFingerprint 解析 x-statsig-id 用的 48 字节指纹。优先级：
//  1. 手动 pin（grok_cf.json: statsig_fingerprint_hex / 环境变量）——应急用；
//  2. 实时从 grok.com 首页 meta 抓取并缓存（默认 TTL 90s，按 proxy 维度缓存）；
//  3. 过期的旧缓存兜底；
//  4. 内置默认值（多半失效）。
func (c *WebClient) grokFingerprint(ctx context.Context, token string) []byte {
	state := readGrokRuntimeCFState()
	if hs := strings.TrimSpace(firstNonEmpty(state.StatsigFingerprintHex, os.Getenv("KLEIN_GROK_STATSIG_FINGERPRINT"))); hs != "" {
		if b, err := hex.DecodeString(hs); err == nil && len(b) == 48 {
			zap.L().Info("grok.statsig.fp", zap.String("source", "pin"), zap.String("fp8", hex.EncodeToString(b[:8])))
			return b
		}
	}

	ttl := time.Duration(envInt("KLEIN_GROK_STATSIG_FP_TTL_SEC", 90)) * time.Second
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	key := c.proxyURL

	grokFpCache.Lock()
	e, ok := grokFpCache.m[key]
	grokFpCache.Unlock()
	if ok && len(e.fp) == 48 && time.Since(e.at) < ttl {
		zap.L().Info("grok.statsig.fp", zap.String("source", "cache"), zap.String("fp8", hex.EncodeToString(e.fp[:8])), zap.String("proxy", key))
		return e.fp
	}

	if fp := c.fetchGrokSiteVerification(ctx, token); len(fp) == 48 {
		grokFpCache.Lock()
		grokFpCache.m[key] = grokFpEntry{fp: fp, at: time.Now()}
		grokFpCache.Unlock()
		zap.L().Info("grok.statsig.fp", zap.String("source", "live"), zap.String("fp8", hex.EncodeToString(fp[:8])), zap.String("proxy", key))
		return fp
	}

	if ok && len(e.fp) == 48 { // 抓取失败时用过期旧值兜底，好过完全没有
		zap.L().Warn("grok.statsig.fp", zap.String("source", "stale_cache"), zap.String("fp8", hex.EncodeToString(e.fp[:8])), zap.String("proxy", key))
		return e.fp
	}
	if b, err := hex.DecodeString(defaultStatsigFingerprintHex); err == nil {
		zap.L().Warn("grok.statsig.fp", zap.String("source", "default_fallback"), zap.String("proxy", key))
		return b
	}
	return nil
}

// fetchGrokSiteVerification 用与业务请求相同的 proxy / cookie / UA / cf_clearance GET grok.com 首页，
// 解析 grok-site-verification meta，base64 解码出 48 字节指纹。
func (c *WebClient) fetchGrokSiteVerification(ctx context.Context, token string) []byte {
	client, err := outbound.NewClient(outbound.Options{Timeout: 20 * time.Second, ProxyURL: c.proxyURL, Mode: outbound.ModeUTLS, Profile: outbound.ProfileChrome})
	if err != nil {
		return nil
	}
	base := strings.TrimRight(firstNonEmpty(c.baseURL, webBaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
	if err != nil {
		return nil
	}
	ua := grokUserAgent()
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Cookie", buildGrokCookie(token))
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", base+"/")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Sec-Ch-Ua", grokSecCHUA(ua))
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", grokSecPlatform(ua))
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := client.Do(req)
	if err != nil {
		zap.L().Warn("grok.statsig.fp.fetch", zap.String("stage", "do"), zap.Error(err), zap.String("proxy", c.proxyURL))
		return nil
	}
	defer resp.Body.Close()
	resp.Body = decodeResponseBody(resp)
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		zap.L().Warn("grok.statsig.fp.fetch", zap.String("stage", "read"), zap.Int("http", resp.StatusCode), zap.Error(err))
		return nil
	}
	html := string(data)
	fp := parseGrokSiteVerification(html)
	hasMeta := strings.Contains(strings.ToLower(html), "grok-site-verification")
	cfChallenge := strings.Contains(strings.ToLower(html), "just a moment") || strings.Contains(html, "challenge-platform") || strings.Contains(html, "cf-chl")
	zap.L().Info("grok.statsig.fp.fetch",
		zap.String("stage", "parsed"),
		zap.Int("http", resp.StatusCode),
		zap.Int("body_len", len(html)),
		zap.Bool("has_meta", hasMeta),
		zap.Bool("cf_challenge", cfChallenge),
		zap.Int("fp_len", len(fp)),
		zap.String("ct", resp.Header.Get("Content-Type")),
	)
	return fp
}

func parseGrokSiteVerification(html string) []byte {
	var v string
	if m := grokSiteVerifRe1.FindStringSubmatch(html); len(m) == 2 {
		v = m[1]
	} else if m := grokSiteVerifRe2.FindStringSubmatch(html); len(m) == 2 {
		v = m[1]
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == 48 {
		return b
	}
	if b, err := base64.RawStdEncoding.DecodeString(v); err == nil && len(b) == 48 {
		return b
	}
	return nil
}

// grokStatsigIDWithFingerprint 用给定指纹复刻 per-request x-statsig-id。
// hash16 里掺入每次请求的随机 nonce（对齐浏览器的 Math.random 行为，且服务端本来也不校验内容）。
func grokStatsigIDWithFingerprint(method, path string, fingerprint []byte) string {
	if !envBool("KLEIN_GROK_STATSIG_SIGNED", true) || len(fingerprint) != 48 {
		return grokStatsigIDLegacy()
	}
	if method == "" {
		method = http.MethodPost
	}
	if path == "" {
		path = chatEndpoint
	}
	counter := time.Now().Unix() - grokStatsigEpoch()
	if counter < 0 {
		counter = 0
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		binary.LittleEndian.PutUint64(nonce, uint64(time.Now().UnixNano()))
	}
	hash16 := grokStatsigHash16(method, path, counter, hex.EncodeToString(nonce))

	rb := make([]byte, 1)
	if _, err := rand.Read(rb); err != nil {
		rb[0] = byte(time.Now().UnixNano())
	}
	return encodeGrokStatsigToken(fingerprint, counter, hash16, grokStatsigTrailer(), rb[0])
}

// grokStatsigHash16 = sha256(method+"!"+path+"!"+itoa(counter)+suffix) 的前 16 字节。
func grokStatsigHash16(method, path string, counter int64, suffix string) []byte {
	sum := sha256.Sum256([]byte(method + "!" + path + "!" + strconv.FormatInt(counter, 10) + suffix))
	return sum[:16]
}

// encodeGrokStatsigToken 把各分量拼成 69 字节 raw，前置随机字节并逐字节 XOR，最后 base64(RawStd)。
// 与浏览器输出逐字节一致（已用 grok.com.har 的真实 token 验证）。
func encodeGrokStatsigToken(fingerprint []byte, counter int64, hash16 []byte, trailer, rnd byte) string {
	raw := make([]byte, 0, 48+4+16+1)
	raw = append(raw, fingerprint...)
	var le [4]byte
	binary.LittleEndian.PutUint32(le[:], uint32(counter))
	raw = append(raw, le[:]...)
	raw = append(raw, hash16...)
	raw = append(raw, trailer)

	out := make([]byte, 0, 1+len(raw))
	out = append(out, rnd)
	for _, b := range raw {
		out = append(out, b^rnd)
	}
	return base64.RawStdEncoding.EncodeToString(out)
}

// grokStatsigIDLegacy 复刻 grok2api(Chenyme) 的简化方案：x-statsig-id = base64("x1:TypeError: …")。
// 关键：前缀必须是 "x1"（早期的 "e" 已被反爬拒绝）。每次随机一条伪造的 JS 报错串，避免重放特征。
func grokStatsigIDLegacy() string {
	if !envBool("KLEIN_GROK_DYNAMIC_STATSIG", true) {
		// 静态兜底：base64("x1:TypeError: Cannot read properties of undefined (reading 'childNodes')")
		return base64.StdEncoding.EncodeToString([]byte("x1:TypeError: Cannot read properties of undefined (reading 'childNodes')"))
	}
	var msg string
	if grokRandByte()%2 == 0 {
		msg = "x1:TypeError: Cannot read properties of null (reading 'children['" + grokRandString(5, true) + "']')"
	} else {
		msg = "x1:TypeError: Cannot read properties of undefined (reading '" + grokRandString(10, false) + "')"
	}
	return base64.StdEncoding.EncodeToString([]byte(msg))
}

// grokRandByte 返回一个随机字节（crypto/rand，失败时退化为时间）。
func grokRandByte() byte {
	b := make([]byte, 1)
	if _, err := rand.Read(b); err != nil {
		return byte(time.Now().UnixNano())
	}
	return b[0]
}

// grokRandString 生成 n 个随机小写字母（withDigits=true 时含数字），对齐 Chenyme 的随机字段。
func grokRandString(n int, withDigits bool) string {
	const lower = "abcdefghijklmnopqrstuvwxyz"
	const lowerDigits = "abcdefghijklmnopqrstuvwxyz0123456789"
	alphabet := lower
	if withDigits {
		alphabet = lowerDigits
	}
	rnd := make([]byte, n)
	if _, err := rand.Read(rnd); err != nil {
		for i := range rnd {
			rnd[i] = byte(time.Now().UnixNano() >> uint(i%8))
		}
	}
	buf := make([]byte, n)
	for i := 0; i < n; i++ {
		buf[i] = alphabet[int(rnd[i])%len(alphabet)]
	}
	return string(buf)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func envBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func shouldNormalizeGrokUpload() bool {
	return envBool("KLEIN_GROK_UPLOAD_NORMALIZE_JPEG", true)
}

func grokUploadScalePercent() int {
	v := envInt("KLEIN_GROK_UPLOAD_SCALE_PERCENT", 100)
	if v < 1 {
		return 100
	}
	if v > 100 {
		return 100
	}
	return v
}

func grokUploadMaxEdge() int {
	return envInt("KLEIN_GROK_UPLOAD_MAX_EDGE", 1280)
}

func grokUploadMinLongEdge() int {
	v := envInt("KLEIN_GROK_UPLOAD_MIN_LONG_EDGE", 720)
	if v < 0 {
		return 0
	}
	return v
}

func normalizeImageForGrokUpload(data []byte) ([]byte, bool) {
	return normalizeImageForGrokUploadWithOptions(data, grokUploadOptions{})
}

func normalizeImageForGrokUploadWithOptions(data []byte, opts grokUploadOptions) ([]byte, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil || img == nil {
		return nil, false
	}
	canvas := flattenImageToRGBA(img)
	if !opts.videoLegacyRef {
		width := canvas.Bounds().Dx()
		height := canvas.Bounds().Dy()
		targetWidth, targetHeight := resolveGrokUploadSize(width, height)
		if targetWidth > 0 && targetHeight > 0 && (targetWidth != width || targetHeight != height) {
			canvas = resizeRGBA(canvas, targetWidth, targetHeight)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 92}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func inferAspectRatioFromRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	var data []byte
	switch {
	case strings.HasPrefix(ref, "data:"):
		comma := strings.Index(ref, ",")
		if comma < 0 || comma+1 >= len(ref) {
			return ""
		}
		decoded, err := base64.StdEncoding.DecodeString(ref[comma+1:])
		if err != nil {
			return ""
		}
		data = decoded
	case strings.HasPrefix(ref, "/api/v1/gen/cached/"):
		rel := strings.TrimPrefix(ref, "/api/v1/gen/cached/")
		if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
			return ""
		}
		root := strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT"))
		if root == "" {
			root = "/app/storage/public"
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return ""
		}
		data = raw
	default:
		return ""
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return ""
	}
	return ratioFromWH(cfg.Width, cfg.Height)
}

func ratioFromWH(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	ratio := float64(width) / float64(height)
	switch {
	case ratio >= 1.45:
		return "16:9"
	case ratio <= 0.8:
		return "9:16"
	default:
		return "1:1"
	}
}

func flattenImageToRGBA(img image.Image) *image.RGBA {
	b := img.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), img, b.Min, draw.Over)
	return canvas
}

func resolveGrokUploadSize(width, height int) (int, int) {
	if width <= 0 || height <= 0 {
		return width, height
	}
	originalLong := width
	if height > originalLong {
		originalLong = height
	}
	targetWidth, targetHeight := scaleByPercent(width, height, grokUploadScalePercent())
	targetWidth, targetHeight = fitWithinMaxEdge(targetWidth, targetHeight, grokUploadMaxEdge())
	minLong := grokUploadMinLongEdge()
	if minLong > 0 {
		targetWidth, targetHeight = ensureMinLongEdge(targetWidth, targetHeight, originalLong, minLong)
	}
	return targetWidth, targetHeight
}

func scaleByPercent(width, height, percent int) (int, int) {
	if width <= 0 || height <= 0 {
		return width, height
	}
	if percent <= 0 || percent >= 100 {
		return width, height
	}
	targetWidth := width * percent / 100
	targetHeight := height * percent / 100
	if targetWidth < 1 {
		targetWidth = 1
	}
	if targetHeight < 1 {
		targetHeight = 1
	}
	return targetWidth, targetHeight
}

func fitWithinMaxEdge(width, height, maxEdge int) (int, int) {
	if width <= 0 || height <= 0 || maxEdge <= 0 {
		return width, height
	}
	longEdge := width
	if height > longEdge {
		longEdge = height
	}
	if longEdge <= maxEdge {
		return width, height
	}
	if width >= height {
		targetWidth := maxEdge
		targetHeight := height * maxEdge / width
		if targetHeight < 1 {
			targetHeight = 1
		}
		return targetWidth, targetHeight
	}
	targetHeight := maxEdge
	targetWidth := width * maxEdge / height
	if targetWidth < 1 {
		targetWidth = 1
	}
	return targetWidth, targetHeight
}

func ensureMinLongEdge(width, height, originalLongEdge, minLongEdge int) (int, int) {
	if width <= 0 || height <= 0 || originalLongEdge <= 0 || minLongEdge <= 0 {
		return width, height
	}
	targetLong := width
	if height > targetLong {
		targetLong = height
	}
	if targetLong >= minLongEdge || originalLongEdge < minLongEdge {
		return width, height
	}
	if width >= height {
		targetWidth := minLongEdge
		if targetWidth > originalLongEdge {
			targetWidth = originalLongEdge
		}
		targetHeight := height * targetWidth / width
		if targetHeight < 1 {
			targetHeight = 1
		}
		return targetWidth, targetHeight
	}
	targetHeight := minLongEdge
	if targetHeight > originalLongEdge {
		targetHeight = originalLongEdge
	}
	targetWidth := width * targetHeight / height
	if targetWidth < 1 {
		targetWidth = 1
	}
	return targetWidth, targetHeight
}

func resizeRGBA(src *image.RGBA, width, height int) *image.RGBA {
	if src == nil || width <= 0 || height <= 0 {
		return src
	}
	srcBounds := src.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()
	if srcWidth == width && srcHeight == height {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		srcY := srcBounds.Min.Y + y*srcHeight/height
		for x := 0; x < width; x++ {
			srcX := srcBounds.Min.X + x*srcWidth/width
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}

func normalizeCookieEnv(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	v = strings.TrimPrefix(v, "Cookie:")
	v = strings.TrimSpace(v)
	return strings.TrimRight(v, "; ")
}

func appendMissingCookies(cookie, extra string) string {
	if extra == "" {
		return cookie
	}
	out := strings.TrimRight(strings.TrimSpace(cookie), "; ")
	for _, part := range strings.Split(extra, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		if i := strings.Index(part, "="); i >= 0 {
			name = strings.TrimSpace(part[:i])
		}
		if name == "" || strings.Contains(out, name+"=") {
			continue
		}
		if out != "" {
			out += "; "
		}
		out += part
	}
	return out
}

func decodeResponseBody(resp *http.Response) io.ReadCloser {
	if resp == nil || resp.Body == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip":
		zr, err := gzip.NewReader(resp.Body)
		if err == nil {
			resp.Header.Del("Content-Encoding")
			return &joinedReadCloser{Reader: zr, closers: []io.Closer{zr, resp.Body}}
		}
	}
	return resp.Body
}

type joinedReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (r *joinedReadCloser) Close() error {
	var first error
	for _, closer := range r.closers {
		if err := closer.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func collectGrokStream(r io.Reader, onText func(string)) (string, string, string, string, []string, error) {
	var out strings.Builder
	videoURL := ""
	thumbURL := ""
	videoPostID := ""
	streamCandidates := make([]string, 0, 4)
	streamSeen := map[string]struct{}{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "" || line == "[DONE]" {
			continue
		}
		var obj any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if errMsg := firstStringByKey(obj, "error"); errMsg != "" {
			return out.String(), videoURL, thumbURL, videoPostID, streamCandidates, fmt.Errorf("grok stream error: %s", errMsg)
		}
		if s := firstStringByKeys(obj, []string{"videoPostId", "video_post_id", "postId", "post_id", "mediaPostId", "media_post_id"}); s != "" {
			videoPostID = s
		}
		if u, candidates := bestVideoURL(obj); u != "" {
			mergeVideoCandidates(&streamCandidates, streamSeen, candidates)
			videoURL = normalizeAssetURL(u)
			if videoPostID == "" {
				videoPostID = extractUUID(videoURL)
			}
		}
		if u := firstStringByKeys(obj, []string{"thumbnailImageUrl", "thumbnailUrl", "coverUrl"}); u != "" {
			thumbURL = normalizeAssetURL(u)
		}
		delta := extractTextDelta(obj)
		if delta != "" {
			out.WriteString(delta)
			if onText != nil {
				onText(delta)
			}
		}
	}
	return out.String(), videoURL, thumbURL, videoPostID, streamCandidates, sc.Err()
}

func firstVideoURL(v any) string {
	best, _ := bestVideoURL(v)
	return best
}

func bestVideoURL(v any) (string, []string) {
	candidates := collectVideoURLs(v)
	if len(candidates) == 0 {
		return "", nil
	}
	best := candidates[0]
	bestScore := scoreVideoURL(best)
	for _, candidate := range candidates[1:] {
		if score := scoreVideoURL(candidate); score > bestScore {
			best = candidate
			bestScore = score
		}
	}
	return best, candidates
}

func collectVideoURLs(v any) []string {
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, key := range []string{"videoUrl", "videoURL", "video_url", "mediaUrl", "result_url"} {
		collectStringsByKey(v, key, &out, seen)
	}
	collectVideoStrings(v, &out, seen)
	return out
}

func collectVideoStrings(v any, out *[]string, seen map[string]struct{}) {
	switch x := v.(type) {
	case map[string]any:
		for _, child := range x {
			collectVideoStrings(child, out, seen)
		}
	case []any:
		for _, child := range x {
			collectVideoStrings(child, out, seen)
		}
	case string:
		if isVideoURLCandidate(x) {
			addUniqueVideoURL(out, seen, x)
		}
	}
}

func collectStringsByKey(v any, key string, out *[]string, seen map[string]struct{}) {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x[key].(string); ok && isVideoURLCandidate(s) {
			addUniqueVideoURL(out, seen, s)
		}
		for _, child := range x {
			collectStringsByKey(child, key, out, seen)
		}
	case []any:
		for _, child := range x {
			collectStringsByKey(child, key, out, seen)
		}
	}
}

func addUniqueVideoURL(out *[]string, seen map[string]struct{}, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if _, ok := seen[raw]; ok {
		return
	}
	seen[raw] = struct{}{}
	*out = append(*out, raw)
}

func mergeVideoCandidates(out *[]string, seen map[string]struct{}, candidates []string) {
	for _, candidate := range candidates {
		addUniqueVideoURL(out, seen, candidate)
	}
}

func summarizeVideoCandidates(candidates []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, fmt.Sprintf("%d|%s", scoreVideoURL(candidate), sanitizeDiagURL(candidate)))
	}
	return out
}

func scoreVideoURL(s string) int {
	lower := strings.ToLower(strings.TrimSpace(s))
	score := 0
	switch {
	case strings.Contains(lower, "master"):
		score += 100
	case strings.Contains(lower, "original"):
		score += 90
	case strings.Contains(lower, "source"):
		score += 80
	case strings.Contains(lower, "download"):
		score += 70
	}
	if strings.Contains(lower, "1080") || strings.Contains(lower, "1920") {
		score += 60
	}
	if strings.Contains(lower, "720") || strings.Contains(lower, "1280") {
		score += 40
	}
	if strings.Contains(lower, "preview") {
		score -= 120
	}
	if strings.Contains(lower, "thumb") || strings.Contains(lower, "thumbnail") {
		score -= 160
	}
	if strings.Contains(lower, "low") || strings.Contains(lower, "small") {
		score -= 40
	}
	if strings.Contains(lower, "400") || strings.Contains(lower, "360") {
		score -= 60
	}
	if strings.Contains(lower, "generated_video") {
		score += 30
	}
	if strings.HasSuffix(lower, ".mp4") {
		score += 10
	}
	return score
}

func isVideoURLCandidate(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "preview_image") || strings.Contains(lower, "thumbnail") ||
		strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".webp") {
		return false
	}
	return strings.Contains(lower, ".mp4") ||
		strings.Contains(lower, ".webm") ||
		strings.Contains(lower, "generated_video") ||
		strings.Contains(lower, "/video/")
}

func derivePreviewImageURL(videoURL string) string {
	v := strings.TrimSpace(videoURL)
	if v == "" {
		return ""
	}
	lower := strings.ToLower(v)
	for _, marker := range []string{"/generated_video.mp4", "/generated_video.webm", "/generated_video"} {
		if idx := strings.LastIndex(lower, marker); idx >= 0 {
			return v[:idx] + "/preview_image.jpg"
		}
	}
	if strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".webm") {
		if idx := strings.LastIndex(v, "/"); idx >= 0 {
			return v[:idx] + "/preview_image.jpg"
		}
	}
	return ""
}

var uuidRe = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func extractUUID(s string) string {
	matches := uuidRe.FindAllString(strings.TrimSpace(s), -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func extractTextDelta(v any) string {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range []string{"token", "responseToken", "text"} {
			if s, ok := x[k].(string); ok && looksLikeTextDelta(s) {
				return s
			}
		}
		for _, child := range x {
			if s := extractTextDelta(child); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range x {
			if s := extractTextDelta(child); s != "" {
				return s
			}
		}
	}
	return ""
}

func looksLikeTextDelta(s string) bool {
	if s == "" || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return false
	}
	return true
}

var xaiToolCardPattern = regexp.MustCompile(`(?s)<xai:tool_usage_card>.*?</xai:tool_usage_card>`)

func cleanGrokText(s string) string {
	if s == "" {
		return ""
	}
	s = xaiToolCardPattern.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if looksLikeXAIToolMarkup(line) {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func looksLikeXAIToolMarkup(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "<xai:") ||
		strings.HasPrefix(s, "</xai:") ||
		strings.Contains(s, "<xai:tool_usage_card") ||
		strings.Contains(s, "<xai:tool_name>") ||
		strings.Contains(s, "<xai:tool_args>")
}

func firstStringByKeys(v any, keys []string) string {
	for _, key := range keys {
		if s := firstStringByKey(v, key); s != "" {
			return s
		}
	}
	return ""
}

func firstStringByKey(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x[key].(string); ok && s != "" {
			return s
		}
		for _, child := range x {
			if s := firstStringByKey(child, key); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range x {
			if s := firstStringByKey(child, key); s != "" {
				return s
			}
		}
	}
	return ""
}

func buildGrokPrompt(body map[string]any) string {
	prompt, _ := buildGrokPromptAndFiles(body)
	return prompt
}

func buildGrokPromptAndFiles(body map[string]any) (string, []string) {
	msgs := normalizeAnySlice(body["messages"])
	if len(msgs) == 0 {
		return "", nil
	}
	var b strings.Builder
	var files []string
	for _, item := range msgs {
		m, _ := item.(map[string]any)
		role, _ := m["role"].(string)
		content, imgs := messageContentAndFiles(m["content"])
		files = append(files, imgs...)
		if strings.TrimSpace(content) == "" {
			continue
		}
		if role == "" {
			role = "user"
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(content)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), files
}

func messageContent(v any) string {
	content, _ := messageContentAndFiles(v)
	return content
}

func messageContentAndFiles(v any) (string, []string) {
	switch c := v.(type) {
	case string:
		return c, nil
	case []map[string]any:
		items := make([]any, 0, len(c))
		for _, item := range c {
			items = append(items, item)
		}
		return messageContentAndFiles(items)
	case []any:
		parts := make([]string, 0, len(c))
		files := make([]string, 0, 4)
		for _, p := range c {
			m, _ := p.(map[string]any)
			if m == nil {
				continue
			}
			typ, _ := m["type"].(string)
			if s, _ := m["text"].(string); s != "" {
				parts = append(parts, s)
			}
			if typ == "image_url" {
				if im, _ := m["image_url"].(map[string]any); im != nil {
					if u, _ := im["url"].(string); strings.TrimSpace(u) != "" {
						files = append(files, strings.TrimSpace(u))
					}
				}
			}
		}
		return strings.Join(parts, "\n"), files
	default:
		b, _ := json.Marshal(c)
		return string(b), nil
	}
}

func normalizeAnySlice(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func estimateOpenAIUsage(prompt, completion string) *OpenAIUsage {
	u := &OpenAIUsage{
		PromptTokens:     len([]rune(prompt))/4 + 1,
		CompletionTokens: len([]rune(completion))/4 + 1,
	}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	return u
}

// normalizeVideoDuration 见 handler/generation_handler.go 同名函数注释。
// 这里和外层 handler 必须保持档位一致，避免二次 normalize 给出不一致的秒数。
//
// 支持的档位：6 / 10 / 20 / 30。10 以上靠 GenerateVideo 内部的 extension 链
// （isVideoExtension + extendPostId + stitchWithExtendPostId）拼接而成，每段
// 单次调用仍然 ≤10s——上游 web 链路对单次 videoLength > 10 直接 400。
func normalizeVideoDuration(sec int) int {
	for _, v := range []int{6, 10, 20, 30} {
		if sec <= v {
			return v
		}
	}
	return 30
}

func videoConfig(size, aspect string) (string, string, int, int) {
	switch strings.TrimSpace(aspect) {
	case "9:16":
		return "9:16", defaultVideoResolution, 720, 1280
	case "1:1":
		return "1:1", defaultVideoResolution, 1024, 1024
	case "16:9":
		return "16:9", defaultVideoResolution, 1280, 720
	}
	switch size {
	case "720x1280", "1024x1792":
		return "9:16", defaultVideoResolution, 720, 1280
	case "1024x1024":
		return "1:1", defaultVideoResolution, 1024, 1024
	case "1280x720", "1792x1024":
		return "16:9", defaultVideoResolution, 1280, 720
	}
	return "16:9", defaultVideoResolution, 1280, 720
}

func videoAspectRatio(size, aspect string) string {
	switch strings.TrimSpace(aspect) {
	case "9:16", "1:1", "16:9":
		return strings.TrimSpace(aspect)
	}
	switch size {
	case "720x1280", "1024x1792":
		return "9:16"
	case "1024x1024":
		return "1:1"
	case "1280x720", "1792x1024":
		return "16:9"
	default:
		return ""
	}
}

func shortID() string {
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	if len(id) > 26 {
		return id[:26]
	}
	return id
}
