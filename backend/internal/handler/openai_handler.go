// Package handler OpenAI / NewAPI compatible downstream protocol handlers.
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/provider/adobe/firefly"
	gptprovider "github.com/kleinai/backend/internal/provider/gpt"
	grokweb "github.com/kleinai/backend/internal/provider/grok"
	xaiprovider "github.com/kleinai/backend/internal/provider/xai"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
)

// OpenAIHandler serves /v1 compatible downstream APIs.
type OpenAIHandler struct {
	svc     *service.GenerationService
	chatSvc *service.ChatService
	repo    *repo.GenerationRepo
	cfg     *service.SystemConfigService
}

var openAISyncWaitInflight int64

const openAISyncWaitHeldKey = "openai_sync_wait_held"

func openAIRateLimit(c *gin.Context, msg string) {
	if strings.TrimSpace(msg) == "" {
		msg = "Too many concurrent generation requests, please retry later"
	}
	c.Header("Retry-After", "5")
	jsonError(c, http.StatusTooManyRequests, "rate_limit_exceeded", msg)
}

func (h *OpenAIHandler) acquireSyncWaitSlot(c *gin.Context) bool {
	limit := int64(20)
	if h != nil && h.cfg != nil {
		limit = h.cfg.OpenAISyncWaitMaxConcurrent(c.Request.Context())
	}
	if limit <= 0 {
		return true
	}
	next := atomic.AddInt64(&openAISyncWaitInflight, 1)
	if next > limit {
		atomic.AddInt64(&openAISyncWaitInflight, -1)
		openAIRateLimit(c, "")
		return false
	}
	c.Set(openAISyncWaitHeldKey, true)
	return true
}

func (h *OpenAIHandler) releaseSyncWaitSlot(c *gin.Context) {
	if v, ok := c.Get(openAISyncWaitHeldKey); ok {
		if held, _ := v.(bool); held {
			atomic.AddInt64(&openAISyncWaitInflight, -1)
		}
	}
}

func (h *OpenAIHandler) mapCreateError(c *gin.Context, err error) {
	if e, ok := errcode.As(err); ok && e.HTTPStatus() == http.StatusTooManyRequests {
		openAIRateLimit(c, e.Msg)
		return
	}
	jsonError(c, http.StatusBadRequest, "billing_or_pool_error", err.Error())
}

// NewOpenAIHandler constructs OpenAIHandler.
func NewOpenAIHandler(svc *service.GenerationService, chatSvc *service.ChatService, r *repo.GenerationRepo, cfg *service.SystemConfigService) *OpenAIHandler {
	return &OpenAIHandler{svc: svc, chatSvc: chatSvc, repo: r, cfg: cfg}
}

type modelItem struct {
	ID       string         `json:"id"`
	Object   string         `json:"object"`
	OwnedBy  string         `json:"owned_by"`
	Kind     string         `json:"kind,omitempty"`
	Endpoint string         `json:"endpoint,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text          string      `json:"text,omitempty"`
	InlineData    *geminiBlob `json:"inline_data,omitempty"`
	InlineDataAlt *geminiBlob `json:"inlineData,omitempty"`
}

type geminiBlob struct {
	MimeType    string `json:"mime_type,omitempty"`
	MimeTypeAlt string `json:"mimeType,omitempty"`
	Data        string `json:"data"`
}

func (b *geminiBlob) mimeType() string {
	if b == nil {
		return ""
	}
	return firstNonEmpty(b.MimeType, b.MimeTypeAlt, "image/png")
}

type geminiGenerationConfig struct {
	ResponseModalities []string           `json:"responseModalities,omitempty"`
	ResponseMimeType   string             `json:"responseMimeType,omitempty"`
	ImageConfig        *geminiImageConfig `json:"imageConfig,omitempty"`
	AspectRatio        string             `json:"aspectRatio,omitempty"`
	AspectRatioSnake   string             `json:"aspect_ratio,omitempty"`
	Resolution         string             `json:"resolution,omitempty"`
	Size               string             `json:"size,omitempty"`
	Extra              map[string]any     `json:"-"`
}

type geminiImageConfig struct {
	ImageSize        string `json:"imageSize,omitempty"`
	ImageSizeSnake   string `json:"image_size,omitempty"`
	AspectRatio      string `json:"aspectRatio,omitempty"`
	AspectRatioSnake string `json:"aspect_ratio,omitempty"`
}

// Models GET /v1/models.
func (h *OpenAIHandler) Models(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.publicOpenAIModels(c.Request.Context()),
	})
}

// GeminiModels GET /v1beta/models.
func (h *OpenAIHandler) GeminiModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"models": []gin.H{
			geminiModelItem("gemini-3.1-flash-image-preview", "Nano Banana V2"),
			geminiModelItem("gemini-3-pro-image-preview", "Nano Banana Pro"),
			geminiModelItem("gemini-3.0-pro-image", "Nano Banana Pro"),
			geminiModelItem("gemini-2.5-flash-image-preview", "Nano Banana"),
		},
	})
}

func geminiModelItem(name, display string) gin.H {
	return gin.H{
		"name":                       "models/" + name,
		"version":                    "001",
		"displayName":                display,
		"description":                "KleinAI image generation compatibility model",
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"},
		"inputTokenLimit":            32768,
		"outputTokenLimit":           8192,
	}
}

// GeminiRouteByAction handles Google GenAI compatible
// POST /v1beta/models/{model}:generateContent and :streamGenerateContent.
func (h *OpenAIHandler) GeminiRouteByAction(c *gin.Context) {
	modelParam := c.Param("model")
	if strings.HasSuffix(modelParam, ":streamGenerateContent") {
		h.GeminiGenerateContent(c, true)
		return
	}
	if strings.HasSuffix(modelParam, ":generateContent") {
		h.GeminiGenerateContent(c, false)
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": 400, "message": "unknown Gemini action"}})
}

func (h *OpenAIHandler) GeminiGenerateContent(c *gin.Context, stream bool) {
	if !middleware.APIKeyScopeAllow(c, "image") {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": 403, "message": "current api key does not allow image generation"}})
		return
	}
	var req geminiRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": 400, "message": "invalid request: " + err.Error()}})
		return
	}
	prompt, refs := extractGeminiPromptAndRefs(req)
	if strings.TrimSpace(prompt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": 400, "message": "no text prompt provided"}})
		return
	}

	modelParam := strings.TrimPrefix(c.Param("model"), "models/")
	modelParam = strings.TrimSuffix(modelParam, ":generateContent")
	modelParam = strings.TrimSuffix(modelParam, ":streamGenerateContent")
	modelCode := resolveGeminiCompatModel(modelParam)
	size, quality, aspect := geminiImageOptions(req.GenerationConfig, len(refs) > 0)
	params := gin.H{
		"size":            size,
		"quality":         quality,
		"resolution":      quality,
		"aspect_ratio":    aspect,
		"response_format": "gemini",
	}
	if len(refs) > 0 {
		params["operation"] = "edit"
	}
	mode := provider.ModeT2I
	if len(refs) > 0 {
		mode = provider.ModeI2I
	}

	if !h.acquireSyncWaitSlot(c) {
		return
	}
	defer h.releaseSyncWaitSlot(c)
	t, ok := h.createTask(c, service.CreateRequest{
		Kind:      provider.KindImage,
		Mode:      mode,
		ModelCode: modelCode,
		Provider:  h.svc.ImageProviderForModelWithParams(modelCode, params),
		Prompt:    prompt,
		Params:    params,
		RefAssets: refs,
		Count:     1,
	})
	if !ok {
		return
	}
	fresh, results := h.waitTask(c, t.TaskID, 10*time.Minute)
	if fresh == nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": gin.H{"code": 504, "message": "image generation timeout"}})
		return
	}
	if fresh.Status == model.GenStatusFailed || fresh.Status == model.GenStatusRefunded {
		msg := "generation failed"
		if fresh.Error != nil && *fresh.Error != "" {
			msg = *fresh.Error
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": 400, "message": msg}})
		return
	}

	resp := h.geminiResponse(c, modelParam, prompt, results)
	if stream {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		raw, _ := json.Marshal(resp)
		fmt.Fprintf(c.Writer, "data: %s\n\n", raw)
		fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *OpenAIHandler) publicOpenAIModels(ctx context.Context) []modelItem {
	rows := make([]modelItem, 0, 16)
	seen := map[string]bool{}
	if h != nil && h.cfg != nil {
		raw := h.cfg.GetString(ctx, "billing.model_prices", "")
		if raw != "" {
			var stored []struct {
				Kind      string `json:"kind"`
				Provider  string `json:"provider"`
				ModelCode string `json:"model_code"`
				Enabled   *bool  `json:"enabled"`
			}
			if err := json.Unmarshal([]byte(raw), &stored); err == nil {
				for _, row := range stored {
					modelCode := strings.TrimSpace(row.ModelCode)
					kind := strings.TrimSpace(row.Kind)
					if modelCode == "" || kind == "" || seen[modelCode] {
						continue
					}
					enabled := true
					if row.Enabled != nil {
						enabled = *row.Enabled
					}
					seen[modelCode] = true
					if !enabled {
						continue
					}
					rows = append(rows, openAIModelItem(modelCode, kind, row.Provider))
				}
			}
		}
	}
	if len(rows) == 0 {
		rows = defaultOpenAIModelItems()
	}
	return rows
}

func openAIModelItem(modelCode, kind, providerName string) modelItem {
	item := modelItem{
		ID:      modelCode,
		Object:  "model",
		OwnedBy: fallbackOwnedBy(providerName, kind),
		Kind:    kind,
	}
	switch kind {
	case "image":
		item.Endpoint = "/v1/images/generations"
		item.Meta = gin.H{"edits": true}
		if strings.EqualFold(modelCode, "gpt-image-2") {
			item.Meta["mode"] = "responses_image_generation"
		}
		if strings.EqualFold(providerName, "adobe") && firefly.IsKnownAlias(modelCode) {
			item.Meta["mode"] = "firefly_3p_generate"
		}
	case "video":
		item.Endpoint = "/v1/video/generations"
		if strings.EqualFold(modelCode, "grok-imagine-video") {
			item.Meta = gin.H{"modes": []string{"text_to_video", "image_to_video", "multi_image_to_video"}}
		}
		if strings.EqualFold(modelCode, "vid-v1") || strings.EqualFold(modelCode, "vid-i2v") {
			item.Meta = gin.H{"alias_of": "grok-imagine-video"}
		}
		if strings.EqualFold(providerName, "adobe") && firefly.IsKnownAlias(modelCode) {
			if item.Meta == nil {
				item.Meta = gin.H{}
			}
			item.Meta["mode"] = "firefly_3p_video"
		}
	default:
		item.Endpoint = "/v1/chat/completions"
	}
	return item
}

func defaultOpenAIModelItems() []modelItem {
	// 默认对外模型清单（billing.model_prices 没配置时的 fallback）：
	//   - 图像：gpt-image-2 + Nano Banana 三件套（由下方 firefly.ListPublicModels 注入）
	//   - 视频：grok-imagine-video 一个（文生视频 / 图生视频 统一模型；
	//     vid-v1 / vid-i2v 作为旧客户端别名仍可使用，但不再单独出现在 /v1/models）
	//   - 文字：GROK + GPT Codex（Plus OAuth 号池）
	data := []modelItem{
		openAIModelItem("gpt-image-2", "image", "gpt"),
		openAIModelItem("grok-imagine-video", "video", "grok"),
	}
	for _, id := range gptprovider.ChatModelIDs() {
		data = append(data, openAIModelItem(id, "text", "gpt"))
	}
	for _, id := range grokweb.ChatModelIDs() {
		data = append(data, openAIModelItem(id, "text", "grok"))
	}
	for _, id := range xaiprovider.ChatModelIDs() {
		data = append(data, openAIModelItem("xai/"+id, "text", "xai"))
	}
	for _, pm := range firefly.ListPublicModels() {
		kind := pm.Type
		if kind == "" {
			kind = "image"
		}
		item := openAIModelItem(pm.ID, kind, "adobe")
		meta := gin.H{
			"display_name": pm.DisplayName,
			"description":  pm.Description,
		}
		if len(pm.Sizes) > 0 {
			meta["sizes"] = pm.Sizes
		}
		if len(pm.Qualities) > 0 {
			meta["qualities"] = pm.Qualities
		}
		if len(pm.Durations) > 0 {
			meta["durations"] = pm.Durations
		}
		if len(pm.Resolutions) > 0 {
			meta["resolutions"] = pm.Resolutions
		}
		if item.Meta != nil {
			for k, v := range item.Meta {
				meta[k] = v
			}
		}
		item.Meta = meta
		data = append(data, item)
	}
	return data
}

func fallbackOwnedBy(providerName, kind string) string {
	switch strings.ToLower(strings.TrimSpace(providerName)) {
	case "gpt", "grok", "pic2api", "adobe":
		return strings.ToLower(strings.TrimSpace(providerName))
	}
	if kind == "video" {
		return "grok"
	}
	return "kleinai"
}

// ChatCompletions POST /v1/chat/completions.
func (h *OpenAIHandler) ChatCompletions(c *gin.Context) {
	if !middleware.APIKeyScopeAllow(c, "chat") {
		jsonError(c, http.StatusForbidden, "scope_not_allowed", "current api key does not allow chat completions")
		return
	}
	k := middleware.APIKeyFromCtx(c)
	if k == nil {
		jsonError(c, http.StatusUnauthorized, "invalid_api_key", "api key required")
		return
	}
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if _, ok := body["messages"]; !ok {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "messages is required")
		return
	}
	modelCode := strings.TrimSpace(anyToString(body["model"]))
	// Adobe Firefly 图像模型走 chat/completions 时桥接到 /v1/images/generations。
	// 这样使用 OpenAI 兼容客户端（LibreChat / Coze / NewAPI 等）的用户
	// 直接在 chat 入口用 model=nano-banana-pro 就能拿到图，不需要切端点。
	if isAdobeImageAlias(modelCode) {
		if stream, _ := body["stream"].(bool); stream {
			h.adobeChatImageBridgeStream(c, k.UserID, &k.ID, modelCode, body)
			return
		}
		h.adobeChatImageBridge(c, k.UserID, &k.ID, modelCode, body)
		return
	}
	req := service.ChatCallRequest{
		UserID:   k.UserID,
		APIKeyID: &k.ID,
		ClientIP: c.ClientIP(),
		IdemKey:  c.GetHeader("Idempotency-Key"),
		Body:     body,
	}
	if stream, _ := body["stream"].(bool); stream {
		if err := h.chatSvc.Stream(c.Request.Context(), req, c.Writer); err != nil {
			jsonError(c, http.StatusBadGateway, "upstream_error", err.Error())
		}
		return
	}
	raw, status, err := h.chatSvc.Complete(c.Request.Context(), req)
	if err != nil {
		jsonError(c, status, "chat_completion_failed", err.Error())
		return
	}
	c.Data(status, "application/json; charset=utf-8", raw)
}

// isAdobeImageAlias 判断 modelCode 是否是 Adobe Firefly 的「图像」alias，
// 这些模型走 chat/completions 时要桥接到 image generation；视频模型不在此列表，
// 因为视频生成耗时通常超过 chat 同步等待窗口。
func isAdobeImageAlias(modelCode string) bool {
	if modelCode == "" {
		return false
	}
	if !firefly.IsKnownAlias(modelCode) {
		return false
	}
	def, ok := firefly.Catalog[firefly.ResolvePublicAlias(modelCode, "", "")]
	if !ok {
		return false
	}
	return def.Type == firefly.ModelTypeImage
}

// adobeChatImageBridge 把一次 chat/completions 翻译成一次 image generation。
//
// 行为：
//   - 把最后一条 user message 当 prompt
//   - 把 message.content 里 type=image_url 的项抽出来当 ref（兼容 OpenAI vision 格式）
//   - 同步等待 image task 完成（默认 60s 上限）
//   - 用 chat.completion 标准 envelope 回复，content 用 Markdown 嵌入图
//
// stream=true 时走 adobeChatImageBridgeStream，定时发送 SSE keep-alive，避免
// 下游网关/客户端因 60s 内无数据而主动断开。
func (h *OpenAIHandler) adobeChatImageBridge(c *gin.Context, userID uint64, apiKeyID *uint64, modelCode string, body map[string]any) {
	prompt, refs := extractChatPromptAndRefs(body)
	if strings.TrimSpace(prompt) == "" {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "prompt is required in messages[]")
		return
	}
	mode := provider.ModeT2I
	if len(refs) > 0 {
		mode = provider.ModeI2I
	}
	params := map[string]any{}
	if sz := anyToString(body["size"]); sz != "" {
		params["size"] = sz
	}
	if q := firstNonEmpty(anyToString(body["quality"]), anyToString(body["resolution"])); q != "" {
		params["quality"] = q
	}
	if aspect := firstNonEmpty(anyToString(body["aspect_ratio"]), anyToString(body["ratio"])); aspect != "" {
		params["ratio"] = aspect
		params["aspect_ratio"] = aspect
		if _, ok := params["size"]; !ok {
			params["size"] = aspect
		}
	}
	if d := anyToString(body["detail"]); d != "" {
		params["detail"] = d
	}
	if len(refs) > 0 {
		params["operation"] = "edit"
	}

	createReq := service.CreateRequest{
		Kind:      provider.KindImage,
		Mode:      mode,
		ModelCode: modelCode,
		Provider:  h.svc.ImageProviderForModelWithParams(modelCode, params),
		Prompt:    prompt,
		Params:    params,
		RefAssets: refs,
		Count:     1,
		UserID:    userID,
		APIKeyID:  apiKeyID,
		IdemKey:   c.GetHeader("Idempotency-Key"),
		ClientIP:  c.ClientIP(),
	}
	if !h.acquireSyncWaitSlot(c) {
		return
	}
	defer h.releaseSyncWaitSlot(c)
	t, err := h.svc.Create(c.Request.Context(), createReq)
	if err != nil {
		if e, ok := errcode.As(err); ok && e.HTTPStatus() == http.StatusTooManyRequests {
			openAIRateLimit(c, e.Msg)
			return
		}
		jsonError(c, http.StatusBadRequest, "generation_failed", err.Error())
		return
	}

	fresh, results := h.waitTask(c, t.TaskID, 120*time.Second)
	if fresh == nil {
		jsonError(c, http.StatusGatewayTimeout, "generation_timeout", "image generation timeout, retrieve with /v1/images/generations/"+t.TaskID)
		return
	}
	if fresh.Status == model.GenStatusFailed || fresh.Status == model.GenStatusRefunded {
		msg := "generation failed"
		if fresh.Error != nil && *fresh.Error != "" {
			msg = *fresh.Error
		}
		jsonError(c, http.StatusBadRequest, "generation_failed", msg)
		return
	}

	content := h.buildAdobeChatContent(c, prompt, results)
	c.JSON(http.StatusOK, gin.H{
		"id":      "chatcmpl-" + t.TaskID,
		"object":  "chat.completion",
		"created": fresh.CreatedAt.Unix(),
		"model":   modelCode,
		"choices": []gin.H{{
			"index": 0,
			"message": gin.H{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": gin.H{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
			"total_cost":        fresh.CostPoints,
		},
	})
}

func (h *OpenAIHandler) adobeChatImageBridgeStream(c *gin.Context, userID uint64, apiKeyID *uint64, modelCode string, body map[string]any) {
	prompt, refs := extractChatPromptAndRefs(body)
	if strings.TrimSpace(prompt) == "" {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "prompt is required in messages[]")
		return
	}
	mode := provider.ModeT2I
	if len(refs) > 0 {
		mode = provider.ModeI2I
	}
	params := map[string]any{}
	if sz := anyToString(body["size"]); sz != "" {
		params["size"] = sz
	}
	if q := firstNonEmpty(anyToString(body["quality"]), anyToString(body["resolution"])); q != "" {
		params["quality"] = q
	}
	if aspect := firstNonEmpty(anyToString(body["aspect_ratio"]), anyToString(body["ratio"])); aspect != "" {
		params["ratio"] = aspect
		params["aspect_ratio"] = aspect
		if _, ok := params["size"]; !ok {
			params["size"] = aspect
		}
	}
	if d := anyToString(body["detail"]); d != "" {
		params["detail"] = d
	}
	if len(refs) > 0 {
		params["operation"] = "edit"
	}

	t, err := h.svc.Create(c.Request.Context(), service.CreateRequest{
		Kind:      provider.KindImage,
		Mode:      mode,
		ModelCode: modelCode,
		Provider:  h.svc.ImageProviderForModelWithParams(modelCode, params),
		Prompt:    prompt,
		Params:    params,
		RefAssets: refs,
		Count:     1,
		UserID:    userID,
		APIKeyID:  apiKeyID,
		IdemKey:   c.GetHeader("Idempotency-Key"),
		ClientIP:  c.ClientIP(),
	})
	if err != nil {
		jsonError(c, http.StatusBadRequest, "generation_failed", err.Error())
		return
	}

	w := c.Writer
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	writeChatBridgeChunk := func(delta gin.H, finish any) {
		payload := gin.H{
			"id":      "chatcmpl-" + t.TaskID,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   modelCode,
			"choices": []gin.H{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		raw, _ := json.Marshal(payload)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(raw)
		_, _ = w.Write([]byte("\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	writeKeepAlive := func() {
		_, _ = w.Write([]byte(": keep-alive\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}

	writeChatBridgeChunk(gin.H{"role": "assistant"}, nil)
	deadline := time.Now().Add(10 * time.Minute)
	poll := time.NewTicker(1 * time.Second)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			writeKeepAlive()
		case <-poll.C:
			fresh, err := h.repo.GetByTaskID(c.Request.Context(), t.TaskID)
			if err == nil && fresh != nil {
				if fresh.Status == model.GenStatusSucceeded {
					results, _ := h.repo.ListResultsByTask(c.Request.Context(), t.TaskID)
					writeChatBridgeChunk(gin.H{"content": h.buildAdobeChatContent(c, prompt, results)}, nil)
					writeChatBridgeChunk(gin.H{}, "stop")
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					if flusher != nil {
						flusher.Flush()
					}
					return
				}
				if fresh.Status == model.GenStatusFailed || fresh.Status == model.GenStatusRefunded {
					msg := "generation failed"
					if fresh.Error != nil && *fresh.Error != "" {
						msg = *fresh.Error
					}
					writeChatBridgeChunk(gin.H{"content": msg}, nil)
					writeChatBridgeChunk(gin.H{}, "stop")
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					if flusher != nil {
						flusher.Flush()
					}
					return
				}
			}
			if time.Now().After(deadline) {
				content := "Image generation is still running. Retrieve it with /v1/images/generations/" + t.TaskID
				writeChatBridgeChunk(gin.H{"content": content}, nil)
				writeChatBridgeChunk(gin.H{}, "stop")
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
				return
			}
		}
	}
}

// extractChatPromptAndRefs 解析 OpenAI 兼容的 messages[] 结构，返回最后一条
// user 消息里的文本 prompt 以及所有 image_url。兼容三种 content 形状：
//   - string                  → 直接当 prompt
//   - [{type:"text",text}]    → 拼接所有 text
//   - [{type:"image_url",image_url:{url}}] → 抽 URL 进 refs
func extractChatPromptAndRefs(body map[string]any) (prompt string, refs []string) {
	defer func() {
		refs = append(refs, collectAnyStringList(body["image_urls"])...)
		if u := anyToString(body["image"]); u != "" {
			refs = append(refs, u)
		}
	}()
	msgs, ok := body["messages"].([]any)
	if !ok {
		return "", nil
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		role := anyToString(msg["role"])
		if role != "" && role != "user" {
			continue
		}
		text, urls := parseChatMessageContent(msg["content"])
		if text == "" && len(urls) == 0 {
			continue
		}
		return text, urls
	}
	return "", nil
}

func parseChatMessageContent(content any) (text string, refs []string) {
	if content == nil {
		return "", nil
	}
	if s, ok := content.(string); ok {
		return strings.TrimSpace(s), nil
	}
	parts, ok := content.([]any)
	if !ok {
		return "", nil
	}
	var sb strings.Builder
	for _, p := range parts {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		switch anyToString(part["type"]) {
		case "text":
			if t := anyToString(part["text"]); t != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(t)
			}
		case "image_url":
			if iu, ok := part["image_url"].(map[string]any); ok {
				if u := anyToString(iu["url"]); u != "" {
					refs = append(refs, u)
				}
			} else if u := anyToString(part["image_url"]); u != "" {
				refs = append(refs, u)
			}
		case "image":
			if u := anyToString(part["url"]); u != "" {
				refs = append(refs, u)
			}
		}
	}
	return strings.TrimSpace(sb.String()), refs
}

func anyToString(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func collectAnyStringList(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := anyToString(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(x))
		for _, s := range x {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(x); s != "" {
			return []string{s}
		}
	}
	return nil
}

func extractGeminiPromptAndRefs(req geminiRequest) (string, []string) {
	var texts []string
	var refs []string
	for _, content := range req.Contents {
		for _, part := range content.Parts {
			if s := strings.TrimSpace(part.Text); s != "" {
				texts = append(texts, s)
			}
			blob := part.InlineData
			if blob == nil {
				blob = part.InlineDataAlt
			}
			if blob != nil && strings.TrimSpace(blob.Data) != "" {
				mimeType := blob.mimeType()
				refs = append(refs, "data:"+mimeType+";base64,"+strings.TrimSpace(blob.Data))
			}
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n")), refs
}

func resolveGeminiCompatModel(model string) string {
	model = strings.TrimPrefix(strings.TrimSpace(model), "models/")
	switch strings.ToLower(model) {
	case "gemini-3.1-flash-image-preview", "gemini-3.1-flash-image":
		return "nano-banana-v2"
	case "gemini-3-pro-image-preview", "gemini-3-pro-image", "gemini-3.0-pro-image":
		return "nano-banana-pro"
	case "gemini-2.5-flash-image-preview", "gemini-2.5-flash-image":
		return "nano-banana"
	default:
		if firefly.IsKnownAlias(model) {
			return model
		}
		return "nano-banana-pro"
	}
}

func geminiImageOptions(cfg *geminiGenerationConfig, hasRef bool) (size, quality, aspect string) {
	quality = "2k"
	aspect = "1:1"
	if cfg != nil {
		if cfg.ImageConfig != nil {
			quality = firstNonEmpty(cfg.ImageConfig.ImageSize, cfg.ImageConfig.ImageSizeSnake, quality)
			aspect = firstNonEmpty(cfg.ImageConfig.AspectRatio, cfg.ImageConfig.AspectRatioSnake, aspect)
		}
		quality = firstNonEmpty(cfg.Resolution, quality)
		aspect = firstNonEmpty(cfg.AspectRatio, cfg.AspectRatioSnake, aspect)
		size = strings.TrimSpace(cfg.Size)
	}
	quality = normalizeGeminiQuality(quality)
	if size == "" {
		if hasRef && (aspect == "" || strings.EqualFold(aspect, "auto")) {
			size = "auto"
			aspect = "auto"
		} else {
			size = aspectRatioToCompatSize(aspect)
		}
	}
	if size == "" {
		size = "1024x1024"
	}
	return size, quality, aspect
}

func normalizeGeminiQuality(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "4k", "ultra", "high", "fine", "max":
		return "4k"
	case "1k", "standard", "std", "low", "fast", "quick", "preview":
		return "1k"
	default:
		return "2k"
	}
}

func aspectRatioToCompatSize(ar string) string {
	switch strings.TrimSpace(ar) {
	case "1:1", "":
		return "1024x1024"
	case "16:9":
		return "2048x1152"
	case "9:16":
		return "1152x2048"
	case "4:3":
		return "2048x1536"
	case "3:4":
		return "1536x2048"
	case "3:2":
		return "2048x1365"
	case "2:3":
		return "1365x2048"
	case "5:4":
		return "2048x1638"
	case "4:5":
		return "1638x2048"
	case "21:9":
		return "2048x878"
	case "auto":
		return "auto"
	default:
		return ar
	}
}

func (h *OpenAIHandler) buildAdobeChatContent(c *gin.Context, prompt string, results []*model.GenerationResult) string {
	if len(results) == 0 {
		return prompt
	}
	var sb strings.Builder
	for i, r := range results {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		url := h.publicResultURL(c, r.URL)
		sb.WriteString("![generated image " + strconv.Itoa(i+1) + "](" + url + ")")
	}
	return sb.String()
}

type imageReq struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	N              int            `json:"n"`
	Count          int            `json:"count"`
	Size           string         `json:"size"`
	Ratio          string         `json:"ratio"`
	AspectRatio    string         `json:"aspect_ratio"`
	Quality        string         `json:"quality"`
	Resolution     string         `json:"resolution"`
	Detail         string         `json:"detail"`
	Style          string         `json:"style"`
	ResponseFormat string         `json:"response_format"`
	Image          string         `json:"image"`
	ImageURLs      []string       `json:"image_urls"`
	Images         []string       `json:"images"`
	RefAssets      []string       `json:"ref_assets"`
	Async          *bool          `json:"async"`
	CallbackURL    string         `json:"callback_url"`
	Params         map[string]any `json:"params"`
	RequestAudit   string         `json:"-"`
}

// ImageGenerations POST /v1/images/generations.
func (h *OpenAIHandler) ImageGenerations(c *gin.Context) {
	h.createImage(c, false)
}

// ImageEdits POST /v1/images/edits.
func (h *OpenAIHandler) ImageEdits(c *gin.Context) {
	h.createImage(c, true)
}

func (h *OpenAIHandler) createImage(c *gin.Context, edit bool) {
	if !middleware.APIKeyScopeAllow(c, "image") {
		jsonError(c, http.StatusForbidden, "scope_not_allowed", "current api key does not allow image generation")
		return
	}
	req, err := bindImageReq(c)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "prompt is required")
		return
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	count := req.N
	if count <= 0 {
		count = req.Count
	}
	if count <= 0 {
		count = 1
	}
	if count > 4 {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "n/count must be less than or equal to 4")
		return
	}

	refs := collectRefs(req.Image, append(req.ImageURLs, req.Images...), req.RefAssets)
	if edit && len(refs) == 0 {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "image is required for image edits")
		return
	}
	mode := provider.ModeT2I
	if len(refs) > 0 {
		mode = provider.ModeI2I
	}
	params := mergeParams(req.Params, gin.H{
		"size":            req.Size,
		"quality":         firstNonEmpty(req.Quality, req.Resolution),
		"detail":          req.Detail,
		"style":           req.Style,
		"response_format": req.ResponseFormat,
		"callback_url":    req.CallbackURL,
	})
	if edit {
		params["operation"] = "edit"
	}
	if aspect := firstNonEmpty(req.Ratio, req.AspectRatio); aspect != "" {
		params["ratio"] = aspect
		params["aspect_ratio"] = aspect
		if _, ok := params["size"]; !ok {
			params["size"] = aspect
		}
	}

	async := shouldAsyncImageRequest(req)
	if !async {
		if !h.acquireSyncWaitSlot(c) {
			return
		}
		defer h.releaseSyncWaitSlot(c)
	}
	t, ok := h.createTask(c, service.CreateRequest{
		Kind:      provider.KindImage,
		Mode:      mode,
		ModelCode: req.Model,
		Provider:  h.svc.ImageProviderForModelWithParams(req.Model, params),
		Prompt:    req.Prompt,
		Params:    params,
		RefAssets: refs,
		Count:     count,
	})
	if !ok {
		return
	}
	h.logDownstreamImageRequest(c, t, req.RequestAudit)
	if async {
		c.JSON(http.StatusOK, h.taskEnvelope(c, t, nil))
		return
	}
	h.respondTaskResult(c, t, 10*time.Minute)
}

func shouldAsyncImageRequest(req *imageReq) bool {
	if req != nil && req.Async != nil {
		return *req.Async
	}
	if req != nil && isOpenAICompatibleImageAlias(req.Model) {
		// OpenAI image clients expect /v1/images/generations to return image data
		// synchronously. Keep task mode for non-OpenAI media endpoints, but make
		// image aliases compatible unless callers explicitly request async=true.
		return false
	}
	return true
}

func isOpenAICompatibleImageAlias(modelCode string) bool {
	modelCode = strings.ToLower(strings.TrimSpace(modelCode))
	return modelCode == "gpt-image-2" || strings.HasPrefix(modelCode, "nano-banana")
}

type videoReq struct {
	Model        string         `json:"model"`
	Prompt       string         `json:"prompt"`
	N            int            `json:"n"`
	Duration     int            `json:"duration"`
	Size         string         `json:"size"`
	Ratio        string         `json:"ratio"`
	AspectRatio  string         `json:"aspect_ratio"`
	Resolution   string         `json:"resolution"`
	Quality      string         `json:"quality"`
	ReferenceFit string         `json:"reference_fit"`
	FPS          int            `json:"fps"`
	Image        string         `json:"image"`
	Images       []string       `json:"images"`
	RefAssets    []string       `json:"ref_assets"`
	Async        *bool          `json:"async"`
	CallbackURL  string         `json:"callback_url"`
	Params       map[string]any `json:"params"`
}

// VideoGenerations POST /v1/video/generations.
func (h *OpenAIHandler) VideoGenerations(c *gin.Context) {
	if !middleware.APIKeyScopeAllow(c, "video") {
		jsonError(c, http.StatusForbidden, "scope_not_allowed", "current api key does not allow video generation")
		return
	}
	var req videoReq
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "prompt is required")
		return
	}
	if req.Model == "" {
		req.Model = "grok-imagine-video"
	}
	if req.N <= 0 {
		req.N = 1
	}
	if req.N > 4 {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "n must be less than or equal to 4")
		return
	}
	refs := collectRefs(req.Image, req.Images, req.RefAssets)
	mode := provider.ModeT2V
	if len(refs) > 0 {
		mode = provider.ModeI2V
	}
	aspect := req.AspectRatio
	if aspect == "" {
		aspect = req.Ratio
	}
	params := mergeParams(req.Params, gin.H{
		"duration":      float64(req.Duration),
		"size":          req.Size,
		"aspect_ratio":  aspect,
		"resolution":    req.Resolution,
		"quality":       req.Quality,
		"reference_fit": req.ReferenceFit,
		"fps":           req.FPS,
		"callback_url":  req.CallbackURL,
	})

	videoProvider := h.svc.VideoProviderForModel(req.Model)
	modelCode := req.Model
	if videoProvider == model.ProviderGROK {
		modelCode = grokweb.NormalizeVideoModel(req.Model)
	}
	if req.Duration > 0 {
		params["duration"] = float64(service.NormalizeVideoDurationForModel(modelCode, req.Duration))
	}
	async := true
	if req.Async != nil {
		async = *req.Async
	}
	if !async {
		if !h.acquireSyncWaitSlot(c) {
			return
		}
		defer h.releaseSyncWaitSlot(c)
	}
	t, ok := h.createTask(c, service.CreateRequest{
		Kind:      provider.KindVideo,
		Mode:      mode,
		ModelCode: modelCode,
		Provider:  videoProvider,
		Prompt:    req.Prompt,
		Params:    params,
		RefAssets: refs,
		Count:     req.N,
	})
	if !ok {
		return
	}
	if async {
		c.JSON(http.StatusOK, h.taskEnvelope(c, t, nil))
		return
	}
	h.respondTaskResult(c, t, 10*time.Minute)
}

type musicReq struct {
	Model       string         `json:"model"`
	Prompt      string         `json:"prompt"`
	Async       *bool          `json:"async"`
	CallbackURL string         `json:"callback_url"`
	Params      map[string]any `json:"params"`
}

// MusicGenerations POST /v1/music/generations.
func (h *OpenAIHandler) MusicGenerations(c *gin.Context) {
	if !middleware.APIKeyScopeAllow(c, "music") {
		jsonError(c, http.StatusForbidden, "scope_not_allowed", "current api key does not allow music generation")
		return
	}
	var req musicReq
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "prompt is required")
		return
	}
	modelCode := strings.TrimSpace(req.Model)
	if modelCode == "" {
		modelCode = "lyria"
	}
	params := mergeParams(req.Params, gin.H{
		"callback_url": req.CallbackURL,
	})
	async := true
	if req.Async != nil {
		async = *req.Async
	}
	if !async {
		if !h.acquireSyncWaitSlot(c) {
			return
		}
		defer h.releaseSyncWaitSlot(c)
	}
	t, ok := h.createTask(c, service.CreateRequest{
		Kind:      provider.KindMusic,
		Mode:      provider.ModeT2A,
		ModelCode: modelCode,
		Provider:  h.svc.MusicProviderForModel(modelCode),
		Prompt:    req.Prompt,
		Params:    params,
		Count:     1,
	})
	if !ok {
		return
	}
	if async {
		c.JSON(http.StatusOK, h.taskEnvelope(c, t, nil))
		return
	}
	h.respondTaskResult(c, t, 10*time.Minute)
}

// GetMusicTask GET /v1/music/generations/:task_id.
func (h *OpenAIHandler) GetMusicTask(c *gin.Context) {
	h.getTask(c, provider.KindMusic)
}

// GetImageTask GET /v1/images/generations/:task_id.
func (h *OpenAIHandler) GetImageTask(c *gin.Context) {
	h.getTask(c, provider.KindImage)
}

// GetImageTaskQuery supports clients that poll the collection URL with
// ?task_id=... / ?id=... instead of /v1/images/generations/:task_id.
func (h *OpenAIHandler) GetImageTaskQuery(c *gin.Context) {
	taskID := strings.TrimSpace(c.Query("task_id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Query("id"))
	}
	if taskID == "" {
		jsonError(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "task_id", Value: taskID})
	h.getTask(c, provider.KindImage)
}

// GetVideoTask GET /v1/video/generations/:task_id.
func (h *OpenAIHandler) GetVideoTask(c *gin.Context) {
	h.getTask(c, provider.KindVideo)
}

func (h *OpenAIHandler) getTask(c *gin.Context, kind provider.Kind) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	t, err := h.repo.GetByTaskID(c.Request.Context(), taskID)
	if err != nil {
		jsonError(c, http.StatusNotFound, "not_found", "task not found")
		return
	}
	k := middleware.APIKeyFromCtx(c)
	if k == nil || t.UserID != k.UserID || t.Kind != string(kind) {
		jsonError(c, http.StatusNotFound, "not_found", "task not found")
		return
	}
	results, _ := h.repo.ListResultsByTask(c.Request.Context(), t.TaskID)
	writeTaskPollHeaders(c, t)
	c.JSON(http.StatusOK, h.taskEnvelope(c, t, results))
}

func writeTaskPollHeaders(c *gin.Context, t *model.GenerationTask) {
	if sec := service.TaskPollRetryAfter(t); sec > 0 {
		c.Header("Retry-After", strconv.Itoa(sec))
	}
}

func (h *OpenAIHandler) createTask(c *gin.Context, req service.CreateRequest) (*model.GenerationTask, bool) {
	k := middleware.APIKeyFromCtx(c)
	if k == nil {
		jsonError(c, http.StatusUnauthorized, "invalid_api_key", "api key required")
		return nil, false
	}
	if h != nil && h.repo != nil {
		pending, running, err := h.repo.CountActiveByAPIKey(c.Request.Context(), k.ID)
		if err == nil {
			maxPending, maxRunning := int64(0), int64(0)
			if h.cfg != nil {
				maxPending = h.cfg.OpenAIMaxPendingPerKey(c.Request.Context())
				maxRunning = h.cfg.OpenAIMaxRunningPerKey(c.Request.Context())
			}
			if maxPending > 0 && pending >= maxPending {
				jsonError(c, http.StatusTooManyRequests, "queue_limit_exceeded", "too many pending generation tasks for this api key")
				return nil, false
			}
			if maxRunning > 0 && running >= maxRunning {
				jsonError(c, http.StatusTooManyRequests, "concurrency_limit_exceeded", "too many running generation tasks for this api key")
				return nil, false
			}
		}
	}
	req.UserID = k.UserID
	req.APIKeyID = &k.ID
	req.IdemKey = c.GetHeader("Idempotency-Key")
	req.ClientIP = c.ClientIP()
	t, err := h.svc.Create(c.Request.Context(), req)
	if err != nil {
		h.mapCreateError(c, err)
		return nil, false
	}
	return t, true
}

func (h *OpenAIHandler) respondTaskResult(c *gin.Context, t *model.GenerationTask, timeout time.Duration) {
	fresh, results := h.waitTask(c, t.TaskID, timeout)
	if fresh == nil {
		c.JSON(http.StatusAccepted, h.taskEnvelope(c, t, nil))
		return
	}
	if fresh.Status == model.GenStatusFailed || fresh.Status == model.GenStatusRefunded {
		msg := "generation failed"
		if fresh.Error != nil && *fresh.Error != "" {
			msg = *fresh.Error
		}
		jsonError(c, http.StatusBadRequest, "generation_failed", msg)
		return
	}
	if fresh.Kind == string(provider.KindVideo) {
		c.JSON(http.StatusOK, h.videoResultEnvelope(c, fresh, results))
		return
	}
	c.JSON(http.StatusOK, h.imageResultEnvelope(c, fresh, results))
}

func (h *OpenAIHandler) waitTask(c *gin.Context, taskID string, timeout time.Duration) (*model.GenerationTask, []*model.GenerationResult) {
	deadline := time.Now().Add(timeout)
	sleep := 500 * time.Millisecond
	for time.Now().Before(deadline) {
		t, err := h.repo.GetByTaskID(c.Request.Context(), taskID)
		if err == nil && (t.Status == model.GenStatusSucceeded || t.Status == model.GenStatusFailed || t.Status == model.GenStatusRefunded) {
			items, _ := h.repo.ListResultsByTask(c.Request.Context(), taskID)
			return t, items
		}
		if err == nil && t != nil {
			if sec := service.TaskPollRetryAfter(t); sec > 0 {
				sleep = time.Duration(sec) * time.Second
			}
		}
		select {
		case <-c.Request.Context().Done():
			return nil, nil
		case <-time.After(sleep):
		}
	}
	return nil, nil
}

func bindImageReq(c *gin.Context) (*imageReq, error) {
	ct := c.GetHeader("Content-Type")
	if strings.Contains(strings.ToLower(ct), "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
			return nil, err
		}
		req := &imageReq{
			Model:          c.PostForm("model"),
			Prompt:         c.PostForm("prompt"),
			Size:           c.PostForm("size"),
			Ratio:          c.PostForm("ratio"),
			AspectRatio:    c.PostForm("aspect_ratio"),
			Quality:        c.PostForm("quality"),
			Resolution:     c.PostForm("resolution"),
			Style:          c.PostForm("style"),
			ResponseFormat: c.PostForm("response_format"),
			Image:          c.PostForm("image"),
			CallbackURL:    c.PostForm("callback_url"),
		}
		req.N, _ = strconv.Atoi(c.DefaultPostForm("n", "1"))
		req.Count, _ = strconv.Atoi(c.DefaultPostForm("count", "0"))
		req.Async = parseOptionalBool(c.PostForm("async"))
		req.ImageURLs = c.PostFormArray("image_urls")
		req.Images = c.PostFormArray("images")
		req.RefAssets = c.PostFormArray("ref_assets")
		uploadRefs, err := collectMultipartImageRefs(c)
		if err != nil {
			return nil, err
		}
		if len(uploadRefs) > 0 {
			req.Images = append(req.Images, uploadRefs...)
			if req.Image == "" {
				req.Image = uploadRefs[0]
			}
		}
		req.RequestAudit = downstreamMultipartAudit(c)
		return req, nil
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	var req imageReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	req.RequestAudit = downstreamJSONAudit(raw)
	return &req, nil
}

func (h *OpenAIHandler) logDownstreamImageRequest(c *gin.Context, t *model.GenerationTask, audit string) {
	if h == nil || h.repo == nil || t == nil || strings.TrimSpace(audit) == "" {
		return
	}
	method := c.Request.Method
	path := c.Request.URL.Path
	requestExcerpt := audit
	metaRaw, _ := json.Marshal(map[string]any{
		"content_type": c.GetHeader("Content-Type"),
		"user_agent":   c.GetHeader("User-Agent"),
	})
	meta := string(metaRaw)
	_ = h.repo.CreateUpstreamLog(c.Request.Context(), &model.GenerationUpstreamLog{
		TaskID:         t.TaskID,
		Provider:       "downstream",
		Stage:          "image_request",
		Method:         method,
		URL:            path,
		RequestExcerpt: &requestExcerpt,
		Meta:           &meta,
	})
}

func downstreamJSONAudit(raw []byte) string {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	return auditMarshal(map[string]any{
		"body_type": "json",
		"fields":    sanitizeAuditValue(obj),
	})
}

func downstreamMultipartAudit(c *gin.Context) string {
	fields := map[string]any{}
	if c.Request.MultipartForm != nil {
		for key, vals := range c.Request.MultipartForm.Value {
			if len(vals) == 1 {
				fields[key] = sanitizeAuditString(key, vals[0])
			} else if len(vals) > 1 {
				items := make([]any, 0, len(vals))
				for _, v := range vals {
					items = append(items, sanitizeAuditString(key, v))
				}
				fields[key] = items
			}
		}
		for key, files := range c.Request.MultipartForm.File {
			fields[key] = fmt.Sprintf("[file count=%d]", len(files))
		}
	}
	return auditMarshal(map[string]any{
		"body_type": "multipart",
		"fields":    fields,
	})
}

func auditMarshal(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

func sanitizeAuditValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			out[k] = sanitizeAuditValueForKey(k, v)
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, sanitizeAuditValue(item))
		}
		return out
	case string:
		return sanitizeAuditString("", x)
	default:
		return x
	}
}

func sanitizeAuditValueForKey(key string, v any) any {
	if s, ok := v.(string); ok {
		return sanitizeAuditString(key, s)
	}
	return sanitizeAuditValue(v)
}

func sanitizeAuditString(key, v string) string {
	s := strings.TrimSpace(v)
	lowerKey := strings.ToLower(key)
	if strings.Contains(lowerKey, "image") || strings.Contains(lowerKey, "asset") || strings.Contains(lowerKey, "url") {
		if len(s) > 96 {
			return fmt.Sprintf("[redacted %s len=%d]", key, len(s))
		}
	}
	if strings.HasPrefix(s, "data:") || len(s) > 160 {
		return fmt.Sprintf("[redacted len=%d]", len(s))
	}
	return s
}

func (h *OpenAIHandler) imageResultEnvelope(c *gin.Context, t *model.GenerationTask, results []*model.GenerationResult) gin.H {
	data := make([]gin.H, 0, len(results))
	for _, r := range results {
		row := gin.H{"url": h.publicResultURL(c, r.URL)}
		if r.Width != nil {
			row["width"] = *r.Width
		}
		if r.Height != nil {
			row["height"] = *r.Height
		}
		data = append(data, row)
	}
	return gin.H{
		"created": t.CreatedAt.Unix(),
		"data":    data,
		"task_id": t.TaskID,
		"usage":   usageEnvelope(t),
	}
}

func (h *OpenAIHandler) geminiResponse(c *gin.Context, modelName, prompt string, results []*model.GenerationResult) gin.H {
	parts := make([]gin.H, 0, len(results)+1)
	for _, r := range results {
		u := h.publicResultURL(c, r.URL)
		if data, mimeType, err := fetchSmallImageAsBase64(c.Request.Context(), u); err == nil && data != "" {
			parts = append(parts, gin.H{
				"inlineData": gin.H{
					"mimeType": mimeType,
					"data":     data,
				},
			})
			continue
		}
		parts = append(parts, gin.H{"text": u})
	}
	if len(parts) == 0 {
		parts = append(parts, gin.H{"text": "No image output was returned."})
	}
	return gin.H{
		"candidates": []gin.H{{
			"content": gin.H{
				"role":  "model",
				"parts": parts,
			},
			"finishReason": "STOP",
			"index":        0,
		}},
		"modelVersion": modelName,
		"usageMetadata": gin.H{
			"promptTokenCount":     len(prompt) / 4,
			"candidatesTokenCount": 256,
			"totalTokenCount":      len(prompt)/4 + 256,
		},
	}
}

func fetchSmallImageAsBase64(ctx context.Context, rawURL string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("download status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", "", err
	}
	if len(body) == 0 {
		return "", "", fmt.Errorf("empty image body")
	}
	mimeType := resp.Header.Get("Content-Type")
	if i := strings.Index(mimeType, ";"); i >= 0 {
		mimeType = strings.TrimSpace(mimeType[:i])
	}
	if mimeType == "" || !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		mimeType = http.DetectContentType(body)
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		mimeType = "image/png"
	}
	return base64.StdEncoding.EncodeToString(body), mimeType, nil
}

func (h *OpenAIHandler) videoResultEnvelope(c *gin.Context, t *model.GenerationTask, results []*model.GenerationResult) gin.H {
	data := make([]gin.H, 0, len(results))
	for _, r := range results {
		row := gin.H{"url": h.publicResultURL(c, r.URL)}
		if r.ThumbURL != nil {
			row["cover_url"] = h.publicResultURL(c, *r.ThumbURL)
		}
		if r.DurationMs != nil {
			row["duration_ms"] = *r.DurationMs
		}
		if r.Width != nil {
			row["width"] = *r.Width
		}
		if r.Height != nil {
			row["height"] = *r.Height
		}
		data = append(data, row)
	}
	return gin.H{
		"id":      t.TaskID,
		"object":  "video.generation",
		"created": t.CreatedAt.Unix(),
		"model":   t.ModelCode,
		"data":    data,
		"usage":   usageEnvelope(t),
	}
}

func (h *OpenAIHandler) musicResultEnvelope(c *gin.Context, t *model.GenerationTask, results []*model.GenerationResult) gin.H {
	data := make([]gin.H, 0, len(results))
	for _, r := range results {
		row := gin.H{"url": h.publicResultURL(c, r.URL)}
		if r.ThumbURL != nil {
			row["cover_url"] = h.publicResultURL(c, *r.ThumbURL)
		}
		if r.DurationMs != nil {
			row["duration_ms"] = *r.DurationMs
		}
		// meta 里带歌名 / 歌词 / wav 直链等富信息，平铺到结果行里方便客户端取用。
		if r.Meta != nil && strings.TrimSpace(*r.Meta) != "" {
			m := map[string]any{}
			if json.Unmarshal([]byte(*r.Meta), &m) == nil {
				for _, k := range []string{"title", "lyrics", "wav_url", "duration"} {
					if v, ok := m[k]; ok {
						row[k] = v
					}
				}
			}
		}
		data = append(data, row)
	}
	return gin.H{
		"id":      t.TaskID,
		"object":  "music.generation",
		"created": t.CreatedAt.Unix(),
		"model":   t.ModelCode,
		"data":    data,
		"usage":   usageEnvelope(t),
	}
}

func (h *OpenAIHandler) taskEnvelope(c *gin.Context, t *model.GenerationTask, results []*model.GenerationResult) gin.H {
	out := gin.H{
		"id":       t.TaskID,
		"task_id":  t.TaskID,
		"object":   t.Kind + ".generation.task",
		"status":   statusName(t.Status),
		"progress": t.Progress,
		"created":  t.CreatedAt.Unix(),
		"model":    t.ModelCode,
		"kind":     t.Kind,
		"mode":     t.Mode,
		"usage":    usageEnvelope(t),
		"error":    nil,
	}
	if sec := service.TaskPollRetryAfter(t); sec > 0 {
		out["retry_after"] = sec
	}
	if t.Error != nil && *t.Error != "" {
		out["error"] = gin.H{"message": *t.Error}
	}
	if len(results) > 0 {
		switch t.Kind {
		case string(provider.KindVideo):
			out["result"] = h.videoResultEnvelope(c, t, results)
		case string(provider.KindMusic):
			out["result"] = h.musicResultEnvelope(c, t, results)
		default:
			out["result"] = h.imageResultEnvelope(c, t, results)
		}
	}
	return out
}

func normalizeOpenAIResultURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "data:") || strings.HasPrefix(v, "/api/") {
		return v
	}
	return "https://assets.grok.com/" + strings.TrimLeft(v, "/")
}

func (h *OpenAIHandler) publicResultURL(c *gin.Context, raw string) string {
	if h == nil {
		return normalizeOpenAIResultURL(raw)
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	v := normalizeOpenAIResultURL(raw)
	return service.AbsolutizeMediaURL(ctx, h.cfg, publicOriginFromGin(c), v)
}

func usageEnvelope(t *model.GenerationTask) gin.H {
	return gin.H{
		"total_cost":   t.CostPoints,
		"total_points": float64(t.CostPoints) / 100,
	}
}

func statusName(status int8) string {
	switch status {
	case model.GenStatusPending:
		return "queued"
	case model.GenStatusRunning:
		return "running"
	case model.GenStatusSucceeded:
		return "succeeded"
	case model.GenStatusFailed:
		return "failed"
	case model.GenStatusRefunded:
		return "refunded"
	default:
		return "unknown"
	}
}

const maxMultipartImageBytes = 32 << 20

func collectMultipartImageRefs(c *gin.Context) ([]string, error) {
	if c == nil || c.Request == nil || c.Request.MultipartForm == nil {
		return nil, nil
	}
	out := make([]string, 0, 2)
	for _, field := range []string{"image", "images", "mask"} {
		for _, fh := range c.Request.MultipartForm.File[field] {
			dataURL, err := multipartFileToDataURL(fh)
			if err != nil {
				return nil, fmt.Errorf("%s upload: %w", field, err)
			}
			out = append(out, dataURL)
		}
	}
	return out, nil
}

func multipartFileToDataURL(fh *multipart.FileHeader) (string, error) {
	if fh == nil {
		return "", fmt.Errorf("missing file")
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxMultipartImageBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty file")
	}
	if len(data) > maxMultipartImageBytes {
		return "", fmt.Errorf("file too large (max %d bytes)", maxMultipartImageBytes)
	}
	mimeType := strings.TrimSpace(fh.Header.Get("Content-Type"))
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = mime.TypeByExtension(filepath.Ext(fh.Filename))
	}
	if !strings.HasPrefix(mimeType, "image/") {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return "", fmt.Errorf("not an image (content-type=%s)", mimeType)
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func collectRefs(one string, many []string, refs []string) []string {
	out := make([]string, 0, 1+len(many)+len(refs))
	if strings.TrimSpace(one) != "" {
		out = append(out, strings.TrimSpace(one))
	}
	for _, v := range many {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	for _, v := range refs {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func mergeParams(base map[string]any, vals map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range vals {
		switch x := v.(type) {
		case string:
			if strings.TrimSpace(x) != "" {
				out[k] = x
			}
		case int:
			if x > 0 {
				out[k] = x
			}
		case float64:
			if x > 0 {
				out[k] = x
			}
		default:
			if v != nil {
				out[k] = v
			}
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseOptionalBool(v string) *bool {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	b := parseBool(v)
	return &b
}

func jsonError(c *gin.Context, status int, kind, msg string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{
			"type":    kind,
			"code":    kind,
			"message": msg,
		},
	})
}
