// Package handler 用户端生成任务 handler。
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// GenerationHandler 生成任务 handler。
type GenerationHandler struct {
	svc     *service.GenerationService
	chatSvc *service.ChatService
	repo    *repo.GenerationRepo
	accRepo *repo.AccountRepo
	cfg     *service.SystemConfigService
	aes     *crypto.AESGCM
	cluster *service.ClusterService // 可选；集群模式 cached 路径会 302 到边缘节点
}

// NewGenerationHandler 构造。
func NewGenerationHandler(svc *service.GenerationService, chatSvc *service.ChatService, r *repo.GenerationRepo, accRepo *repo.AccountRepo, cfg *service.SystemConfigService, aes *crypto.AESGCM) *GenerationHandler {
	return &GenerationHandler{svc: svc, chatSvc: chatSvc, repo: r, accRepo: accRepo, cfg: cfg, aes: aes}
}

// SetClusterService 注入集群服务（可选）。
func (h *GenerationHandler) SetClusterService(c *service.ClusterService) {
	if h == nil {
		return
	}
	h.cluster = c
}

type publicModelResp struct {
	ModelCode        string `json:"model_code"`
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	Provider         string `json:"provider"`
	UpstreamModel    string `json:"upstream_model,omitempty"`
	UnitPoints       int64  `json:"unit_points"`
	InputUnitPoints  int64  `json:"input_unit_points,omitempty"`
	OutputUnitPoints int64  `json:"output_unit_points,omitempty"`
	VideoPricingMode string `json:"video_pricing_mode,omitempty"`
	// ImagePricing: 1k/2k/4k → points*100；前端按当前 resolution 取价
	ImagePricing map[string]int64 `json:"image_pricing,omitempty"`
	// VideoPricing: "6"/"10"/"20"/"30" → points*100；前端按当前 duration 取价
	VideoPricing map[string]int64 `json:"video_pricing,omitempty"`
	Enabled      bool             `json:"enabled"`
}

// Models GET /api/v1/models
func (h *GenerationHandler) Models(c *gin.Context) {
	response.OK(c, gin.H{"list": h.publicModels(c.Request.Context())})
}

// CachedAsset GET /api/v1/gen/cached/*path
//
// 三段路由策略：
//  1. 集群开启 + 该 rel_path 有一个远端 locator → 302 到边缘节点签名 URL
//  2. 主控本地也持有该 rel_path（locator 记录或文件系统直查）→ 直接 c.File()
//  3. 兜底 → 直接 c.File()（兼容旧任务 / 单机模式）
//
// 查询参数：
//   - ?nocluster=1  跳过集群路由直接走本地。客户端检测到边缘节点失败时用这个
//     旁路把用户态恢复到「主控直接服务」，避免单点边缘故障影响整批资源访问。
func (h *GenerationHandler) CachedAsset(c *gin.Context) {
	p := strings.TrimLeft(c.Param("path"), "/")
	if p == "" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
		response.Fail(c, errcode.InvalidParam.WithMsg("invalid asset path"))
		return
	}
	// 资源缩略图（_thumb.jpg）使用 thumb kind 解析，主图使用 gen
	kind := model.AssetKindGen
	if strings.Contains(p, "_thumb") {
		kind = model.AssetKindThumb
	}
	noCluster := c.Query("nocluster") == "1"
	if h.cluster != nil && !noCluster {
		u, _, err := h.cluster.ResolveDownload(c.Request.Context(), kind, p)
		if err == nil && u != "" {
			// 浏览器跟 302；带 Vary 提示 CDN
			c.Header("Vary", "Cookie, Authorization")
			c.Redirect(http.StatusFound, u)
			return
		}
	}
	root := strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT"))
	if root == "" {
		root = "/app/storage/public"
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(filepath.Join(root, filepath.FromSlash(p)))
}

// ReportTaintedAsset POST /api/v1/gen/cached/tainted
//
// 用户态 / 浏览器 / SDK 在 302 跳到边缘节点后下载失败时调用。
// 控制面把对应 download_locator 标 tainted，让后续 ResolveDownload 跳过该节点。
//
// 设计权衡：
//   - 公开匿名接口（与 GET /gen/cached/* 一致），但通过 IP 限流封顶 60/min；
//   - 服务端只标 status=tainted，不做物理删除；后台 GC 周期统一清理；
//   - 不暴露 locator 表的内部字段，参数仅 asset_kind / asset_key / node_id；
//   - rel_path 经规整后才入库，挡掉 ../ 等路径穿越。
func (h *GenerationHandler) ReportTaintedAsset(c *gin.Context) {
	if h == nil || h.cluster == nil {
		response.OK(c, gin.H{"ok": true, "noop": true})
		return
	}
	var req dto.GenTaintedReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	key := strings.TrimSpace(req.AssetKey)
	if key == "" || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		response.Fail(c, errcode.InvalidParam.WithMsg("invalid asset_key"))
		return
	}
	nodeID := strings.TrimSpace(req.NodeID)
	if nodeID == "" {
		response.Fail(c, errcode.InvalidParam.WithMsg("empty node_id"))
		return
	}
	kind := strings.TrimSpace(req.AssetKind)
	switch kind {
	case model.AssetKindGen, model.AssetKindThumb, model.AssetKindUser, "":
		// ok
	default:
		response.Fail(c, errcode.InvalidParam.WithMsg("invalid asset_kind"))
		return
	}
	if kind == "" {
		if strings.Contains(key, "_thumb") {
			kind = model.AssetKindThumb
		} else {
			kind = model.AssetKindGen
		}
	}
	h.cluster.MarkTainted(c.Request.Context(), kind, key, nodeID)
	response.OK(c, gin.H{"ok": true})
}

// CreateImage POST /api/v1/gen/image
func (h *GenerationHandler) CreateImage(c *gin.Context) {
	var req dto.CreateImageReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	uid := middleware.MustUID(c)

	params := req.Params
	if params == nil {
		params = map[string]any{}
	}
	if req.Ratio != "" {
		params["ratio"] = req.Ratio
		params["aspect_ratio"] = req.Ratio
	}
	if req.Quality != "" {
		params["quality"] = req.Quality
	}
	if strings.TrimSpace(req.CallbackURL) != "" {
		params["callback_url"] = strings.TrimSpace(req.CallbackURL)
	}

	mode := req.Mode
	if mode == "" {
		if len(req.RefAssets) > 0 {
			mode = "i2i"
		} else {
			mode = "t2i"
		}
	}
	count := req.Count
	if count <= 0 {
		count = 1
	}

	t, err := h.svc.Create(c.Request.Context(), service.CreateRequest{
		UserID:    uid,
		Kind:      provider.KindImage,
		Mode:      provider.Mode(mode),
		ModelCode: req.ModelCode,
		Provider:  h.svc.ImageProviderForModelWithParams(req.ModelCode, params),
		Prompt:    req.Prompt,
		NegPrompt: req.NegPrompt,
		Params:    params,
		RefAssets: req.RefAssets,
		Count:     count,
		IdemKey:   c.GetHeader("Idempotency-Key"),
		ClientIP:  c.ClientIP(),
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, h.taskToResp(c, t, nil))
}

// CreateVideo POST /api/v1/gen/video
func (h *GenerationHandler) CreateVideo(c *gin.Context) {
	var req dto.CreateVideoReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	uid := middleware.MustUID(c)

	params := req.Params
	if params == nil {
		params = map[string]any{}
	}
	mode := req.Mode
	if mode == "" {
		if len(req.RefAssets) > 0 {
			mode = "i2v"
		} else {
			mode = "t2v"
		}
	}
	// 免额度 Imagine Pipeline 通道（grok-imagine-video-6s-free）的硬约束：
	//   1) 上游 spec 没有时长字段 → 服务端固定 6s
	//   2) 上游没有 aspectRatio 入参 → 输出方向跟参考图走
	//   3) 必须有至少 1 张参考图（i2v-only）
	// 把这三点在 handler 层规范化掉，避免前端传错或老客户端绕过 UI 限制把
	// 30s / 16:9 / t2v 塞进来，导致后端 provider 报错或者跟计费表对不上。
	if strings.EqualFold(strings.TrimSpace(req.ModelCode), "grok-imagine-video-6s-free") {
		if len(req.RefAssets) == 0 {
			response.Fail(c, errcode.InvalidParam.WithMsg("免额度通道仅支持图生视频，请至少上传一张参考图"))
			return
		}
		mode = "i2v"
		req.Duration = 6 // 强制 6s，免得计费表错位
		req.Ratio = ""   // 上游不接受 ratio
	}
	// ratio 一直透传，不区分 t2v / i2v：
	//   - t2v：grok 上游需要它来设置画布尺寸
	//   - i2v：用户显式指定时优先，否则 provider 内部会从参考图推断
	// 旧实现限制 `mode == "t2v"` 会让 i2v 模式下用户的比例选择被静默丢弃，
	// 最终走 inferAspectRatioFromRef → 比例永远跟参考图，与前端不一致。
	if req.Ratio != "" {
		params["ratio"] = req.Ratio
		params["aspect_ratio"] = req.Ratio
	}
	if req.Quality != "" {
		params["quality"] = req.Quality
	}
	if req.Resolution != "" {
		params["resolution"] = req.Resolution
	}
	if req.ReferenceFit != "" {
		params["reference_fit"] = req.ReferenceFit
	}
	if req.Duration > 0 {
		params["duration"] = float64(service.NormalizeVideoDurationForModel(req.ModelCode, req.Duration))
	}
	if strings.TrimSpace(req.CallbackURL) != "" {
		params["callback_url"] = strings.TrimSpace(req.CallbackURL)
	}

	// 视频 provider 不能再硬编码 grok：billing.model_prices 里 sora / veo3.1 / veo3.1-* 都配的是
	// provider=adobe，硬编码会让 pickAccountForTask 跑到 pool_grok 上结果只能返回「暂无可用账号」。
	// 用 VideoProviderForModel(model_code) 查同一份 model_prices；未配置 / unknown 模型 fallback=grok
	// 兼容老的 grok-imagine-video 走 grok 不变。
	t, err := h.svc.Create(c.Request.Context(), service.CreateRequest{
		UserID:    uid,
		Kind:      provider.KindVideo,
		Mode:      provider.Mode(mode),
		ModelCode: req.ModelCode,
		Provider:  h.svc.VideoProviderForModel(req.ModelCode),
		Prompt:    req.Prompt,
		Params:    params,
		RefAssets: req.RefAssets,
		Count:     1,
		IdemKey:   c.GetHeader("Idempotency-Key"),
		ClientIP:  c.ClientIP(),
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, h.taskToResp(c, t, nil))
}

// CreateMusic POST /api/v1/gen/music
func (h *GenerationHandler) CreateMusic(c *gin.Context) {
	var req dto.CreateMusicReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	uid := middleware.MustUID(c)

	params := req.Params
	if params == nil {
		params = map[string]any{}
	}
	if strings.TrimSpace(req.CallbackURL) != "" {
		params["callback_url"] = strings.TrimSpace(req.CallbackURL)
	}
	modelCode := strings.TrimSpace(req.ModelCode)
	if modelCode == "" {
		modelCode = "lyria"
	}

	t, err := h.svc.Create(c.Request.Context(), service.CreateRequest{
		UserID:    uid,
		Kind:      provider.KindMusic,
		Mode:      provider.ModeT2A,
		ModelCode: modelCode,
		Provider:  h.svc.MusicProviderForModel(modelCode),
		Prompt:    req.Prompt,
		Params:    params,
		Count:     1,
		IdemKey:   c.GetHeader("Idempotency-Key"),
		ClientIP:  c.ClientIP(),
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, h.taskToResp(c, t, nil))
}

// CreateText POST /api/v1/gen/text
func (h *GenerationHandler) CreateText(c *gin.Context) {
	var req dto.CreateTextReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	if h.chatSvc == nil {
		response.Fail(c, errcode.ResourceMissing.WithMsg("文字创作服务未启用"))
		return
	}
	if strings.TrimSpace(req.ModelCode) == "" {
		req.ModelCode = "grok-4.20-fast"
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 1200
	}
	content := any(req.Prompt)
	if len(req.Images) > 0 {
		parts := []map[string]any{{"type": "text", "text": req.Prompt}}
		for _, u := range req.Images {
			if strings.TrimSpace(u) != "" {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": strings.TrimSpace(u)}})
			}
		}
		content = parts
	}
	raw, status, err := h.chatSvc.Complete(c.Request.Context(), service.ChatCallRequest{
		UserID:   middleware.MustUID(c),
		ClientIP: c.ClientIP(),
		IdemKey:  c.GetHeader("Idempotency-Key"),
		Body: map[string]any{
			"model":      req.ModelCode,
			"messages":   []map[string]any{{"role": "user", "content": content}},
			"max_tokens": req.MaxTokens,
		},
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	if status >= 400 {
		reason := fmt.Sprintf("codex chat http %d: %s", status, string(raw))
		response.Fail(c, errcode.GPTUnavailable.WithMsg(service.UserFacingGenerationError(reason)))
		return
	}
	response.OK(c, parseTextGenerationResp(raw, req.ModelCode))
}

// Get GET /api/v1/gen/tasks/:task_id
func (h *GenerationHandler) Get(c *gin.Context) {
	id := c.Param("task_id")
	uid := middleware.MustUID(c)
	t, err := h.repo.GetByTaskID(c.Request.Context(), id)
	if err != nil || t.UserID != uid {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	results, _ := h.repo.ListResultsByTask(c.Request.Context(), id)
	if sec := service.TaskPollRetryAfter(t); sec > 0 {
		c.Header("Retry-After", strconv.Itoa(sec))
	}
	response.OK(c, h.taskToResp(c, t, results))
}

// List GET /api/v1/gen/history?kind=image|video&page=&page_size=
func (h *GenerationHandler) List(c *gin.Context) {
	uid := middleware.MustUID(c)
	kind := c.Query("kind")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if h.svc != nil {
		h.svc.ReapStaleTasks(c.Request.Context(), uid)
	}
	items, total, err := h.repo.ListByUser(c.Request.Context(), uid, kind, page, pageSize)
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	out := make([]*dto.GenerationTaskResp, 0, len(items))
	for _, t := range items {
		results, _ := h.repo.ListResultsByTask(c.Request.Context(), t.TaskID)
		out = append(out, h.taskToResp(c, t, results))
	}
	response.Page(c, out, total, page, pageSize)
}

// DeleteHistory DELETE /api/v1/gen/history?scope=all|before_3d|before_7d|failed
func (h *GenerationHandler) DeleteHistory(c *gin.Context) {
	uid := middleware.MustUID(c)
	scope := strings.ToLower(strings.TrimSpace(c.DefaultQuery("scope", "all")))
	var (
		deleted int64
		err     error
	)
	switch scope {
	case "all":
		deleted, err = h.repo.SoftDeleteByUser(c.Request.Context(), uid, false)
	case "before_3d":
		deleted, err = h.repo.SoftDeleteByUserBefore(c.Request.Context(), uid, time.Now().UTC().AddDate(0, 0, -3))
	case "before_7d":
		deleted, err = h.repo.SoftDeleteByUserBefore(c.Request.Context(), uid, time.Now().UTC().AddDate(0, 0, -7))
	case "failed":
		deleted, err = h.repo.SoftDeleteByUser(c.Request.Context(), uid, true)
	default:
		response.Fail(c, errcode.InvalidParam.WithMsg("scope must be all, before_3d, before_7d or failed"))
		return
	}
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	response.OK(c, gin.H{"deleted": deleted})
}

// RedirectMedia GET /api/v1/m/:token（旧路径 /api/v1/gen/media/:token 兼容）
//
// 签名媒体短链统一入口：验证 HMAC token → 按 task_id+seq 查回 meta 里的上游真实直链，
// 再按 meta.storage_mode 决定行为：proxy=服务器流式转发（隐藏地址），redirect=302 跳上游。
func (h *GenerationHandler) RedirectMedia(c *gin.Context) {
	token := stripMediaTokenSuffix(strings.TrimSpace(c.Param("token")))
	secret := service.MediaSigningSecret(c.Request.Context(), h.cfg)
	if len(secret) == 0 {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	p, err := service.VerifyMediaToken(secret, token)
	if err != nil {
		response.Fail(c, errcode.ResourceMissing.WithMsg("媒体链接已失效，请重新生成"))
		return
	}
	result, err := h.repo.GetResultByTaskSeq(c.Request.Context(), p.TaskID, p.Seq)
	if err != nil {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	serveSignedMedia(c, result.Meta, p.Thumb)
}

// stripMediaTokenSuffix 去掉重定向短链 token 末尾可能附带的文件后缀（.png/.mp4 等）。
// 这些后缀只是为了让客户端/浏览器按扩展名识别类型，不参与 HMAC 验签。
func stripMediaTokenSuffix(token string) string {
	if i := strings.LastIndexByte(token, '.'); i > 0 {
		switch strings.ToLower(token[i:]) {
		case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".mp4", ".webm", ".mov",
			".mp3", ".m4a", ".wav", ".ogg", ".flac", ".aac":
			return token[:i]
		}
	}
	return token
}

// extractUpstreamURLFromMeta 从 result.meta 里取出重定向模式记录的上游真实直链。
// thumb=true 时优先取缩略图直链，没有则回退主图直链。
func extractUpstreamURLFromMeta(meta *string, thumb bool) string {
	if meta == nil || strings.TrimSpace(*meta) == "" {
		return ""
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(*meta), &m); err != nil {
		return ""
	}
	if thumb {
		if v, ok := m["upstream_thumb_url"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if v, ok := m["upstream_url"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// Asset GET /api/v1/gen/assets/:task_id/:seq?thumb=1
func (h *GenerationHandler) Asset(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	// seq 可能带扩展名（如 "0.mp4"）——URL 生成端为方便下载附加了后缀，这里剥掉再解析。
	seqParam := c.Param("seq")
	if i := strings.IndexByte(seqParam, '.'); i >= 0 {
		seqParam = seqParam[:i]
	}
	seq, _ := strconv.Atoi(seqParam)
	t, err := h.repo.GetByTaskID(c.Request.Context(), taskID)
	if err != nil {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	result, err := h.repo.GetResultByTaskSeq(c.Request.Context(), taskID, seq)
	if err != nil {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	rawURL := result.URL
	if c.Query("thumb") == "1" {
		if result.ThumbURL != nil && *result.ThumbURL != "" {
			rawURL = *result.ThumbURL
		} else if derived := deriveGrokPreviewImageURL(result.URL); derived != "" {
			rawURL = derived
		}
	}
	// 签名媒体短链（redirect / proxy 存储模式）：按 meta.storage_mode 统一出口处理。
	if isInternalMediaPath(rawURL) {
		serveSignedMedia(c, result.Meta, c.Query("thumb") == "1")
		return
	}
	target := normalizeGrokAssetURL(rawURL)
	if target == "" {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	cookie, err := h.grokCookieForTask(c.Request.Context(), t)
	if err != nil {
		response.Fail(c, errcode.GPTUnavailable.WithMsg("资源下载凭证不可用"))
		return
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, target, nil)
	if err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://grok.com/")
	req.Header.Set("Accept", "*/*")
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		response.Fail(c, errcode.GPTUnavailable.Wrap(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		response.Fail(c, errcode.GPTUnavailable.WithMsg(fmt.Sprintf("资源下载失败 HTTP %d", resp.StatusCode)))
		return
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "private, max-age=300")
	if disp := assetDisposition(rawURL); disp != "" {
		c.Header("Content-Disposition", disp)
	}
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, resp.Body)
}

// === helpers ===

func parseTextGenerationResp(raw []byte, fallbackModel string) *dto.TextGenerationResp {
	var obj struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &obj)
	content := ""
	if len(obj.Choices) > 0 {
		content = obj.Choices[0].Message.Content
	}
	if obj.Model == "" {
		obj.Model = fallbackModel
	}
	return &dto.TextGenerationResp{
		ID:               obj.ID,
		ModelCode:        obj.Model,
		Content:          content,
		PromptTokens:     obj.Usage.PromptTokens,
		CompletionTokens: obj.Usage.CompletionTokens,
		TotalTokens:      obj.Usage.TotalTokens,
	}
}

func (h *GenerationHandler) taskToResp(c *gin.Context, t *model.GenerationTask, results []*model.GenerationResult) *dto.GenerationTaskResp {
	r := &dto.GenerationTaskResp{
		TaskID:     t.TaskID,
		Kind:       t.Kind,
		Status:     t.Status,
		Progress:   t.Progress,
		ModelCode:  t.ModelCode,
		Prompt:     t.Prompt,
		CostPoints: t.CostPoints,
		CreatedAt:  t.CreatedAt.Unix(),
	}
	if sec := service.TaskPollRetryAfter(t); sec > 0 {
		r.RetryAfter = sec
	}
	if t.Error != nil {
		r.Error = *t.Error
	}
	// 视频主资源附带 .mp4 后缀，方便按扩展名识别类型的下载器/第三方消费方。
	mainExt := ""
	if t.Kind == string(provider.KindVideo) {
		mainExt = ".mp4"
	}
	for _, gr := range results {
		row := dto.GenerationResultResp{URL: h.generationResultURL(c, t.TaskID, int(gr.Seq), gr.URL, false, mainExt)}
		if gr.ThumbURL != nil {
			row.ThumbURL = h.generationResultURL(c, t.TaskID, int(gr.Seq), *gr.ThumbURL, true, "")
		} else if t.Kind == string(provider.KindVideo) {
			if derived := deriveGrokPreviewImageURL(gr.URL); derived != "" {
				row.ThumbURL = h.generationResultURL(c, t.TaskID, int(gr.Seq), derived, true, "")
			}
		}
		if gr.Width != nil {
			row.Width = *gr.Width
		}
		if gr.Height != nil {
			row.Height = *gr.Height
		}
		if gr.DurationMs != nil {
			row.DurationMs = *gr.DurationMs
		}
		row.Resolution, row.AspectRatio = generationDisplayAttrs(t.Kind, t.Params, gr.Meta, gr.Width, gr.Height)
		if r.Resolution == "" {
			r.Resolution = row.Resolution
		}
		if r.AspectRatio == "" {
			r.AspectRatio = row.AspectRatio
		}
		r.Results = append(r.Results, row)
	}
	if r.Resolution == "" || r.AspectRatio == "" {
		resolution, aspectRatio := generationDisplayAttrs(t.Kind, t.Params, nil, nil, nil)
		if r.Resolution == "" {
			r.Resolution = resolution
		}
		if r.AspectRatio == "" {
			r.AspectRatio = aspectRatio
		}
	}
	return r
}

// normalizeVideoDuration 把用户/前端传的秒数对齐到 Grok 上游真正支持的离散档。
//
// 早期结论以为只能 6/10s——那是因为只看了单次 conversations/new 的 400。但抓包
// grok.com.har 显示 grok.com web UI 实际上 20s/30s 是用「extend」链拼出来的：
//
//   - 第 1 次：POST /rest/app-chat/conversations/new，videoLength=10，
//     isVideoExtension 不设，得到 postA（10s 成品）。
//   - 第 2 次：再调一次 conversations/new，videoLength=10，
//     isVideoExtension=true, extendPostId=postA, stitchWithExtendPostId=true,
//     videoExtensionStartTime≈10.03，由服务端把两段拼成 postB（20s 成品 mp4）。
//   - 第 3 次：同上，extendPostId=postB，得到 30s 成品。
//
// 拼接逻辑在 grok WebClient.GenerateVideo 内部已实现。这里只负责把入参对齐到
// 6/10/20/30 四档；上游单次依然只接受 ≤10。
func normalizeVideoDuration(sec int) int {
	return service.NormalizeVideoDurationForModel("", sec)
}

func generationAssetURL(taskID string, seq int, thumb bool, ext string) string {
	u := fmt.Sprintf("/api/v1/gen/assets/%s/%d", url.PathEscape(taskID), seq)
	// 给主资源（非缩略图）附带扩展名，方便严格按后缀识别类型的下载工具/第三方消费方。
	// 仅影响 URL 字面值；后端 Asset 解析 seq 时会剥掉扩展名，行为不变。
	if !thumb && ext != "" {
		u += ext
	}
	if thumb {
		u += "?thumb=1"
	}
	return u
}

func (h *GenerationHandler) generationResultURL(c *gin.Context, taskID string, seq int, rawURL string, thumb bool, ext string) string {
	v := strings.TrimSpace(rawURL)
	if v == "" {
		return ""
	}
	var resolved string
	if strings.HasPrefix(v, "/api/v1/gen/cached/") || isInternalMediaPath(v) || strings.HasPrefix(v, "data:") {
		resolved = v
	} else if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		if !strings.Contains(v, "assets.grok.com") {
			resolved = v
		} else {
			resolved = generationAssetURL(taskID, seq, thumb, ext)
		}
	} else {
		resolved = generationAssetURL(taskID, seq, thumb, ext)
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	var cfg *service.SystemConfigService
	if h != nil {
		cfg = h.cfg
	}
	return service.AbsolutizeMediaURL(ctx, cfg, publicOriginFromGin(c), resolved)
}

func deriveGrokPreviewImageURL(videoURL string) string {
	v := strings.TrimSpace(videoURL)
	if v == "" {
		return ""
	}
	lower := strings.ToLower(v)
	if !strings.Contains(lower, "assets.grok.com") && !strings.Contains(lower, "generated_video") && !strings.Contains(lower, "/generated/") {
		return ""
	}
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

func normalizeGrokAssetURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "data:") || strings.HasPrefix(v, "/api/") {
		return v
	}
	if strings.HasPrefix(v, "/") {
		v = strings.TrimLeft(v, "/")
	}
	return "https://assets.grok.com/" + v
}

func (h *GenerationHandler) grokCookieForTask(ctx context.Context, t *model.GenerationTask) (string, error) {
	if t.AccountID == nil || h.accRepo == nil || h.aes == nil {
		return "", fmt.Errorf("missing account")
	}
	acc, err := h.accRepo.GetByID(ctx, *t.AccountID)
	if err != nil {
		return "", err
	}
	plain, err := h.aes.Decrypt(acc.CredentialEnc)
	if err != nil {
		return "", err
	}
	cred := strings.TrimSpace(string(plain))
	if strings.Contains(cred, "=") {
		if !strings.Contains(cred, "sso-rw=") {
			token := extractSSOValue(cred)
			if token != "" {
				cred = strings.TrimRight(cred, "; ") + "; sso-rw=" + token
			}
		}
		return cred, nil
	}
	return "sso=" + cred + "; sso-rw=" + cred, nil
}

func extractSSOValue(cookie string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "sso=") {
			return strings.TrimPrefix(part, "sso=")
		}
	}
	return strings.TrimSpace(cookie)
}

func assetDisposition(rawURL string) string {
	lower := strings.ToLower(rawURL)
	name := "asset"
	if i := strings.LastIndex(rawURL, "/"); i >= 0 && i+1 < len(rawURL) {
		name = rawURL[i+1:]
	}
	if strings.Contains(lower, ".mp4") || strings.Contains(lower, "generated_video") {
		return fmt.Sprintf(`inline; filename="%s"`, name)
	}
	return fmt.Sprintf(`inline; filename="%s"`, name)
}

func (h *GenerationHandler) publicModels(ctx context.Context) []publicModelResp {
	raw := ""
	if h.cfg != nil {
		raw = h.cfg.GetString(ctx, "billing.model_prices", "")
	}
	var rows []publicModelResp
	seen := map[string]bool{}
	if raw != "" {
		var stored []struct {
			ModelCode        string           `json:"model_code"`
			Name             string           `json:"name"`
			Kind             string           `json:"kind"`
			Provider         string           `json:"provider"`
			UpstreamModel    string           `json:"upstream_model"`
			UnitPoints       int64            `json:"unit_points"`
			InputUnitPoints  int64            `json:"input_unit_points"`
			OutputUnitPoints int64            `json:"output_unit_points"`
			VideoPricingMode string           `json:"video_pricing_mode"`
			ImagePricing     map[string]int64 `json:"image_pricing"`
			VideoPricing     map[string]int64 `json:"video_pricing"`
			Enabled          *bool            `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(raw), &stored); err == nil {
			for _, row := range stored {
				if row.ModelCode == "" || row.Kind == "" {
					continue
				}
				seen[row.ModelCode] = true
				enabled := true
				if row.Enabled != nil {
					enabled = *row.Enabled
				}
				if !enabled {
					continue
				}
				rows = append(rows, publicModelResp{
					ModelCode:        row.ModelCode,
					Name:             fallbackString(row.Name, row.ModelCode),
					Kind:             row.Kind,
					Provider:         row.Provider,
					UpstreamModel:    row.UpstreamModel,
					UnitPoints:       row.UnitPoints,
					InputUnitPoints:  row.InputUnitPoints,
					OutputUnitPoints: row.OutputUnitPoints,
					VideoPricingMode: row.VideoPricingMode,
					ImagePricing:     row.ImagePricing,
					VideoPricing:     row.VideoPricing,
					Enabled:          true,
				})
			}
		}
	}
	for _, row := range defaultPublicModels() {
		if !seen[row.ModelCode] {
			rows = append(rows, row)
		}
	}
	return rows
}

func defaultPublicModels() []publicModelResp {
	// 用户端 /api/v1/models 兜底清单（billing.model_prices 未配置时使用）：
	//   - 文字：GROK 4 个常用 plan
	//   - 图像：gpt-image-2 + Nano Banana 三件套（都支持 1K/2K/4K）
	//   - 视频：grok-imagine-video 一个（文生视频 / 图生视频 统一模型；
	//     6 / 10 / 20 / 30 秒四档；20 / 30 秒由后端自动拼接成一条完整视频返回）
	//
	// 图片 / 视频的 image_pricing / video_pricing 与 service.DefaultImageVariantTable /
	// DefaultVideoVariantTable 对齐——admin 后台保存 billing.model_prices 后会覆盖这份兜底。
	imgGPT := copyVariants(service.DefaultImageVariantTable["gpt-image-2"])
	imgNanoBananaPro := copyVariants(service.DefaultImageVariantTable["nano-banana-pro"])
	imgNanoBanana := copyVariants(service.DefaultImageVariantTable["nano-banana"])
	imgNanoBananaV2 := copyVariants(service.DefaultImageVariantTable["nano-banana-v2"])
	vidGrok := copyVariants(service.DefaultVideoVariantTable["grok-imagine-video"])
	return []publicModelResp{
		{ModelCode: "gpt-5.4", Name: "GPT 5.4", Kind: "text", Provider: "gpt", UpstreamModel: "gpt-5.4", InputUnitPoints: 200, OutputUnitPoints: 600, Enabled: true},
		{ModelCode: "gpt-5.4-mini", Name: "GPT 5.4 Mini", Kind: "text", Provider: "gpt", UpstreamModel: "gpt-5.4-mini", InputUnitPoints: 100, OutputUnitPoints: 300, Enabled: true},
		{ModelCode: "gpt-5.3-codex", Name: "GPT 5.3 Codex", Kind: "text", Provider: "gpt", UpstreamModel: "gpt-5.3-codex", InputUnitPoints: 150, OutputUnitPoints: 450, Enabled: true},
		{ModelCode: "grok-4.20-fast", Name: "Grok Fast", Kind: "text", Provider: "grok", UpstreamModel: "grok-4.20-fast", InputUnitPoints: 100, OutputUnitPoints: 300, Enabled: true},
		{ModelCode: "grok-4.20-auto", Name: "Grok Auto", Kind: "text", Provider: "grok", UpstreamModel: "grok-4.20-auto", InputUnitPoints: 150, OutputUnitPoints: 450, Enabled: true},
		{ModelCode: "grok-4.20-expert", Name: "Grok Expert", Kind: "text", Provider: "grok", UpstreamModel: "grok-4.20-expert", InputUnitPoints: 200, OutputUnitPoints: 600, Enabled: true},
		{ModelCode: "grok-4.20-heavy", Name: "Grok Heavy", Kind: "text", Provider: "grok", UpstreamModel: "grok-4.20-heavy", InputUnitPoints: 400, OutputUnitPoints: 1200, Enabled: true},
		{ModelCode: "gpt-image-2", Name: "GPT Image 2", Kind: "image", Provider: "gpt", UpstreamModel: "gpt-image-2", UnitPoints: 400, ImagePricing: imgGPT, Enabled: true},
		{ModelCode: "nano-banana-pro", Name: "Nano Banana Pro", Kind: "image", Provider: "adobe", UpstreamModel: "firefly-nano-banana-pro", UnitPoints: 3000, ImagePricing: imgNanoBananaPro, Enabled: true},
		{ModelCode: "nano-banana-v2", Name: "Nano Banana V2", Kind: "image", Provider: "adobe", UpstreamModel: "firefly-nano-banana2", UnitPoints: 1500, ImagePricing: imgNanoBananaV2, Enabled: true},
		{ModelCode: "nano-banana", Name: "Nano Banana", Kind: "image", Provider: "adobe", UpstreamModel: "firefly-nano-banana", UnitPoints: 1500, ImagePricing: imgNanoBanana, Enabled: true},
		{ModelCode: "grok-imagine-video", Name: "Grok Imagine 视频", Kind: "video", Provider: "grok", UpstreamModel: "grok-imagine-video", UnitPoints: 2000, VideoPricingMode: service.VideoPricingModeVariant, VideoPricing: vidGrok, Enabled: true},
		// 注意：grok-imagine-video-6s-free 是后端「主通道 429 / 配额耗尽时自动 fallback」的内部通道，
		// 不再作为对外公开模型暴露。如运营需要让用户手动选择，可在 admin 后台「模型价格」单独添加并开 enabled。
	}
}

func copyVariants(src map[string]int64) map[string]int64 {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]int64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func fallbackString(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
