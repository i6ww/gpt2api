package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

type AdminLogHandler struct {
	gen     *repo.GenerationRepo
	acc     *repo.AccountRepo
	aes     *crypto.AESGCM
	cluster *service.ClusterService // 可选；管理端预览也走集群 302
	genSvc  *service.GenerationService
}

func NewAdminLogHandler(gen *repo.GenerationRepo, acc *repo.AccountRepo, aes *crypto.AESGCM) *AdminLogHandler {
	return &AdminLogHandler{gen: gen, acc: acc, aes: aes}
}

// SetClusterService 注入集群服务（可选）。
func (h *AdminLogHandler) SetClusterService(c *service.ClusterService) {
	if h == nil {
		return
	}
	h.cluster = c
}

// SetGenerationService 注入生成服务，用于管理端手动回收卡住任务。
func (h *AdminLogHandler) SetGenerationService(s *service.GenerationService) {
	if h == nil {
		return
	}
	h.genSvc = s
}

func (h *AdminLogHandler) GenerationLogs(c *gin.Context) {
	var req dto.AdminGenerationLogListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	rows, total, err := h.gen.ListAdminLogs(c.Request.Context(), repo.AdminGenerationLogFilter{
		Keyword:  req.Keyword,
		Kind:     req.Kind,
		Status:   req.Status,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	out := make([]*dto.AdminGenerationLogResp, 0, len(rows))
	for _, r := range rows {
		item := &dto.AdminGenerationLogResp{
			TaskID:     r.TaskID,
			CreatedAt:  r.CreatedAt.Unix(),
			UserID:     r.UserID,
			UserLabel:  r.UserLabel,
			Kind:       r.Kind,
			ModelCode:  r.ModelCode,
			Prompt:     r.Prompt,
			Status:     r.Status,
			CostPoints: r.CostPoints,
		}
		item.Resolution, item.AspectRatio = generationDisplayAttrs(r.Kind, r.Params, r.ResultMeta, r.Width, r.Height)
		if r.APIKeyID != nil {
			item.APIKeyID = *r.APIKeyID
		}
		if r.KeyLabel != nil {
			item.KeyLabel = *r.KeyLabel
		}
		if r.DurationMs != nil {
			item.DurationMs = *r.DurationMs
		}
		if r.PreviewURL != nil && *r.PreviewURL != "" {
			item.PreviewURL = adminPreviewURLFromRaw(r.TaskID, r.Kind, *r.PreviewURL)
		}
		if r.AssetURL != nil && *r.AssetURL != "" {
			item.AssetURL = adminPreviewURLFromRaw(r.TaskID, r.Kind, *r.AssetURL)
		}
		if r.Error != nil {
			item.Error = *r.Error
		}
		out = append(out, item)
	}
	page, pageSize := req.Page, req.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	response.Page(c, out, total, page, pageSize)
}

func (h *AdminLogHandler) CleanupStuckGenerations(c *gin.Context) {
	if h.genSvc == nil {
		response.Fail(c, errcode.ResourceMissing.WithMsg("生成任务清理服务未启用"))
		return
	}
	var req dto.AdminGenerationStuckCleanupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	if req.MinAgeMinutes <= 0 {
		req.MinAgeMinutes = 10
	}
	cleaned, err := h.genSvc.ReapStuckRunningTasks(c.Request.Context(), time.Duration(req.MinAgeMinutes)*time.Minute, 200)
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	response.OK(c, dto.AdminGenerationStuckCleanupResp{Cleaned: cleaned})
}

// adminPreviewURLFromRaw 把数据库里 generation_result 表的原始 URL（可能是 /api/v1/gen/cached/...、
// 也可能是 assets.grok.com 之类的远程 URL）翻译成 admin 侧能直接 <img src=> 加载的相对路径。
// 与用户端 generationResultURL 的规则保持一致，只是前缀换成 /admin/api/v1/gen/。
func adminPreviewURLFromRaw(taskID, kind, raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "data:") {
		return v
	}
	if strings.HasPrefix(v, "/api/v1/gen/cached/") {
		return "/admin" + v
	}
	thumb := kind != "video"
	u := fmt.Sprintf("/admin/api/v1/gen/assets/%s/0", taskID)
	if thumb {
		u += "?thumb=1"
	}
	return u
}

func (h *AdminLogHandler) GenerationUpstreamLogs(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		response.Fail(c, errcode.InvalidParam.WithMsg("empty task_id"))
		return
	}
	rows, err := h.gen.ListUpstreamLogs(c.Request.Context(), taskID)
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	out := make([]*dto.AdminGenerationUpstreamLogResp, 0, len(rows))
	for _, r := range rows {
		item := &dto.AdminGenerationUpstreamLogResp{
			ID:         r.ID,
			TaskID:     r.TaskID,
			Provider:   r.Provider,
			AccountID:  r.AccountID,
			Stage:      r.Stage,
			StatusCode: r.StatusCode,
			DurationMs: r.DurationMs,
			CreatedAt:  r.CreatedAt.Unix(),
		}
		if r.Method != nil {
			item.Method = *r.Method
		}
		if r.URL != nil {
			item.URL = *r.URL
		}
		if r.RequestExcerpt != nil {
			item.RequestExcerpt = *r.RequestExcerpt
		}
		if r.ResponseExcerpt != nil {
			item.ResponseExcerpt = *r.ResponseExcerpt
		}
		if r.Error != nil {
			item.Error = *r.Error
		}
		if r.Meta != nil {
			item.Meta = *r.Meta
		}
		out = append(out, item)
	}
	response.OK(c, out)
}

// GenCachedAsset 提供 /admin/api/v1/gen/cached/*path —— 把 KLEIN_STORAGE_ROOT 下的
// 本地图片/视频对管理后台开放。与 /api/v1/gen/cached/* 行为一致，URL 形态对齐。
func (h *AdminLogHandler) GenCachedAsset(c *gin.Context) {
	rel := strings.TrimLeft(c.Param("path"), "/")
	if rel == "" || strings.Contains(rel, "..") {
		response.Fail(c, errcode.InvalidParam.WithMsg("invalid asset path"))
		return
	}
	if h.cluster != nil {
		kind := model.AssetKindGen
		if strings.Contains(rel, "_thumb") {
			kind = model.AssetKindThumb
		}
		if u, _, err := h.cluster.ResolveDownload(c.Request.Context(), kind, rel); err == nil && u != "" {
			c.Redirect(http.StatusFound, u)
			return
		}
	}
	serveAdminCachedAsset(c, rel)
}

// GenAsset 提供 /admin/api/v1/gen/assets/:task_id/:seq —— 从 generation_result 取出
// 第 seq 条记录的原始 URL，再按其类型分流（cached 文件 / 远程代理）。和用户端 Asset
// 接口对齐，差异仅在前缀。
func (h *AdminLogHandler) GenAsset(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	seq, _ := strconv.Atoi(c.Param("seq"))
	t, err := h.gen.GetByTaskID(c.Request.Context(), taskID)
	if err != nil {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	result, err := h.gen.GetResultByTaskSeq(c.Request.Context(), taskID, seq)
	if err != nil {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	rawURL := strings.TrimSpace(result.URL)
	if c.Query("thumb") == "1" {
		if result.ThumbURL != nil && strings.TrimSpace(*result.ThumbURL) != "" {
			rawURL = strings.TrimSpace(*result.ThumbURL)
		} else if derived := deriveGrokPreviewImageURL(result.URL); derived != "" {
			rawURL = derived
		}
	}
	if rawURL == "" {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	if strings.HasPrefix(rawURL, "/api/v1/gen/cached/") {
		serveAdminCachedAsset(c, strings.TrimPrefix(rawURL, "/api/v1/gen/cached/"))
		return
	}
	if strings.HasPrefix(rawURL, "/admin/api/v1/gen/cached/") {
		serveAdminCachedAsset(c, strings.TrimPrefix(rawURL, "/admin/api/v1/gen/cached/"))
		return
	}
	if strings.HasPrefix(rawURL, "data:") {
		c.Redirect(http.StatusFound, rawURL)
		return
	}
	// 签名媒体短链（redirect / proxy 存储模式）：result.URL 是站内 /api/v1/m/<token>，
	// 真实上游直链记录在 meta。已按 task_id+seq 定位到 result，无需验签，
	// 按 meta.storage_mode 统一出口（proxy 流式转发 / redirect 302）。
	if isInternalMediaPath(rawURL) {
		serveSignedMedia(c, result.Meta, c.Query("thumb") == "1")
		return
	}
	target := adminNormalizeGrokAssetURL(rawURL)
	if !(strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://")) {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	if !strings.Contains(target, "assets.grok.com") {
		c.Redirect(http.StatusFound, target)
		return
	}
	cookie, err := h.grokCookieForTask(c.Request.Context(), t)
	if err != nil {
		response.Fail(c, errcode.GPTUnavailable.WithMsg("资源下载凭证不可用"))
		return
	}
	proxyRemoteAsset(c, target, cookie, rawURL)
}

func serveAdminCachedAsset(c *gin.Context, rel string) {
	rel = strings.TrimLeft(rel, "/")
	if rel == "" || strings.Contains(rel, "..") {
		response.Fail(c, errcode.InvalidParam.WithMsg("invalid asset path"))
		return
	}
	root := strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT"))
	if root == "" {
		root = "/app/storage/public"
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(filepath.Join(root, filepath.FromSlash(rel)))
}

func proxyRemoteAsset(c *gin.Context, target, cookie, rawURL string) {
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, target, nil)
	if err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://grok.com/")
	req.Header.Set("Accept", "*/*")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
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
	c.Header("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, adminAssetName(rawURL)))
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, resp.Body)
}

func adminNormalizeGrokAssetURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "data:") {
		return v
	}
	return "https://assets.grok.com/" + strings.TrimLeft(v, "/")
}

func (h *AdminLogHandler) grokCookieForTask(ctx context.Context, t *model.GenerationTask) (string, error) {
	if t.AccountID == nil || h.acc == nil || h.aes == nil {
		return "", fmt.Errorf("missing account")
	}
	acc, err := h.acc.GetByID(ctx, *t.AccountID)
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
			if token := adminCookieValue(cred, "sso"); token != "" {
				cred = strings.TrimRight(cred, "; ") + "; sso-rw=" + token
			}
		}
		return cred, nil
	}
	return "sso=" + cred + "; sso-rw=" + cred, nil
}

func adminCookieValue(cookie, name string) string {
	prefix := name + "="
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, prefix) {
			return strings.TrimPrefix(part, prefix)
		}
	}
	return ""
}

func adminAssetName(rawURL string) string {
	name := "asset"
	if i := strings.LastIndex(rawURL, "/"); i >= 0 && i+1 < len(rawURL) {
		name = rawURL[i+1:]
	}
	name = strings.TrimSpace(strings.Split(name, "?")[0])
	if name == "" {
		return "asset"
	}
	return name
}

func (h *AdminLogHandler) PurgeGenerationLogs(c *gin.Context) {
	var req dto.AdminGenerationLogPurgeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	// days=0 表示「不限时间」，但必须指定 status（典型：一键删所有失败），
	// 拒绝 days=0 && status=nil 的请求避免把成功记录一起带走。
	if req.Days == 0 && req.Status == nil {
		response.Fail(c, errcode.InvalidParam.Wrap(fmt.Errorf("days=0 必须指定 status（典型 status=3 一键删失败）")))
		return
	}
	// days=0 → before=now，相当于"截止现在的所有"
	before := time.Now().UTC()
	if req.Days > 0 {
		before = before.AddDate(0, 0, -req.Days)
	}
	var statusFilter *int8
	if req.Status != nil {
		s := int8(*req.Status)
		statusFilter = &s
	}
	deleted, err := h.gen.SoftDeleteAdminLogsBefore(c.Request.Context(), before, statusFilter)
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	response.OK(c, &dto.AdminGenerationLogPurgeResp{Deleted: deleted})
}
