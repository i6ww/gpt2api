// Package handler 管理后台 - GPT 号池 handler。
package handler

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// AdminPoolGptHandler /admin/api/v1/pools/gpt 资源 handler。
type AdminPoolGptHandler struct {
	svc       *service.PoolGptService
	pickProxy func() string // 用于 refresh 时挑选代理（轮转）；可空走直连
}

// NewAdminPoolGptHandler 构造。
//
// pickProxy 可空：留空时刷新走直连；否则每次返回一个代理 URL（轮转）。
func NewAdminPoolGptHandler(svc *service.PoolGptService, pickProxy func() string) *AdminPoolGptHandler {
	return &AdminPoolGptHandler{svc: svc, pickProxy: pickProxy}
}

// List GET /admin/api/v1/pools/gpt
func (h *AdminPoolGptHandler) List(c *gin.Context) {
	var req dto.GptPoolListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	items, total, err := h.svc.List(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	page, ps := req.Page, req.PageSize
	if page <= 0 {
		page = 1
	}
	if ps <= 0 {
		ps = 20
	}
	response.Page(c, items, total, page, ps)
}

// Stats GET /admin/api/v1/pools/gpt/stats
func (h *AdminPoolGptHandler) Stats(c *gin.Context) {
	res, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /admin/api/v1/pools/gpt
func (h *AdminPoolGptHandler) Create(c *gin.Context) {
	var req dto.GptPoolCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	p, err := h.svc.Create(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"id": p.ID})
}

// Detail GET /admin/api/v1/pools/gpt/:id
//
// 返回单条详情（含解密后的明文 password / token）。仅管理后台编辑弹窗使用。
func (h *AdminPoolGptHandler) Detail(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	res, err := h.svc.Detail(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Update PUT /admin/api/v1/pools/gpt/:id
func (h *AdminPoolGptHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.GptPoolUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	if err := h.svc.Update(c.Request.Context(), id, &req); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, nil)
}

// Import POST /admin/api/v1/pools/gpt/import
func (h *AdminPoolGptHandler) Import(c *gin.Context) {
	var req dto.GptPoolImportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	res, err := h.svc.Import(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Delete DELETE /admin/api/v1/pools/gpt/:id
func (h *AdminPoolGptHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, nil)
}

// BatchDelete POST /admin/api/v1/pools/gpt/batch-delete
func (h *AdminPoolGptHandler) BatchDelete(c *gin.Context) {
	var req dto.GptPoolBatchIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.GptPoolBulkOpResult{Affected: n})
}

// Refresh POST /admin/api/v1/pools/gpt/:id/refresh
//
// 立即触发一次单账号刷新（refresh AT/RT + wham/usage 拿 plan/quota）。
//
// query：only_quota=1 时跳过换 token，只拉 quota（便宜得多）。
func (h *AdminPoolGptHandler) Refresh(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	onlyQuota := c.Query("only_quota") == "1"
	proxy := ""
	if h.pickProxy != nil {
		proxy = h.pickProxy()
	}
	row, err := h.svc.RefreshOne(c.Request.Context(), id, service.GptRefreshOptions{
		ProxyURL:  proxy,
		OnlyQuota: onlyQuota,
		Caller:    "manual",
	})
	if err != nil {
		// service 已经返回 *errcode.Error（账号失效 / 节流 / 缺凭证 / 上游临时错），
		// 透传即可；只有当返回的不是 errcode 时才兜底成 Internal。
		if e, ok := errcode.As(err); ok {
			response.Fail(c, e)
		} else {
			response.Fail(c, errcode.Internal.Wrap(err))
		}
		return
	}
	// 返回最新 detail（含明文 AT/RT 让前端弹窗 hot-reload）。
	det, derr := h.svc.Detail(c.Request.Context(), row.ID)
	if derr != nil {
		// detail 失败兜底返一个 minimal 结构。
		resp := gin.H{"id": row.ID, "status": row.Status}
		if row.ExpiresAt != nil {
			resp["expires_at"] = row.ExpiresAt.UnixMilli()
		}
		response.OK(c, resp)
		return
	}
	response.OK(c, det)
}

// RefreshAll POST /admin/api/v1/pools/gpt/refresh-all
//
// 触发一次后台扫描（< 12h 过期的账号一并完整刷新 + 全部账号 quota 增量刷新）。
//
// 同步等待结束并返回成功 / 失败计数。
func (h *AdminPoolGptHandler) RefreshAll(c *gin.Context) {
	ok1, fail1 := h.svc.RefreshExpiring(c.Request.Context(), 12*time.Hour, 3, h.pickProxy)
	ok2, fail2 := h.svc.RefreshStaleQuota(c.Request.Context(), 30*time.Minute, 3, h.pickProxy)
	response.OK(c, gin.H{
		"token_ok": ok1, "token_fail": fail1,
		"quota_ok": ok2, "quota_fail": fail2,
	})
}

// BatchRefresh POST /admin/api/v1/pools/gpt/batch-refresh
//
// 同步等待全部刷新结束并返回 ok / fail / total。
//
// 前端常用入口：
//
//   - {"scope":"all"}                          → 刷新全部账号 token+quota
//   - {"scope":"abnormal"}                     → 刷新异常账号
//   - {"scope":"expiring"}                     → 与 refresh-all token 路径等价
//   - {"scope":"all",        "only_quota":true} → 只刷全部账号 quota
//   - {"scope":"quota_stale","only_quota":true} → 只刷过期 quota
func (h *AdminPoolGptHandler) BatchRefresh(c *gin.Context) {
	var req dto.GptPoolBatchRefreshReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	scope := repo.PoolGptRefreshScope(req.Scope)
	if scope == "" {
		scope = repo.PoolGptScopeAll
	}
	maxConc := req.MaxConcurrent
	if maxConc <= 0 {
		maxConc = 3
	}
	ok, fail, total := h.svc.RefreshByScope(
		c.Request.Context(),
		scope,
		req.OnlyQuota,
		maxConc,
		h.pickProxy,
	)
	response.OK(c, dto.GptPoolBatchRefreshResp{
		Total: total, OK: ok, Fail: fail,
	})
}

// Purge POST /admin/api/v1/pools/gpt/purge
//
// Body: {"scope":"all|invalid|token_expired|quota_exceeded|no_refresh"}
//
// 按 scope 软删账号，返回受影响行数。
func (h *AdminPoolGptHandler) Purge(c *gin.Context) {
	var req dto.GptPoolPurgeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.PurgeBy(c.Request.Context(), req.Scope)
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	response.OK(c, dto.GptPoolBulkOpResult{Affected: n})
}

// Export GET /admin/api/v1/pools/gpt/export?scope=all|valid|invalid|selected&format=internal|crs|codex|account_password&ids=1,2,3
//
// 返回流：
//   - format=crs / codex / internal → application/json (.json 文件)
//   - format=account_password       → text/plain      (.txt 文件)
//
// 响应头：
//   - Content-Disposition: attachment; filename=gpt-<scope>-<format>-<unix>.<ext>
//   - X-Klein-Export-Count: <count>
//
// 仅 admin token 可访问。
//
// 4 种导出格式：
//   - internal         : 我们自家扁平 JSON Array（导入完全互通）
//   - crs              : claude-relay-service 风格，{"accounts":[...]}
//   - codex            : token_xxx_xxx_<unix>.json 单 object 格式合并成 Array
//   - account_password : 纯文本 email:password 一行一条
func (h *AdminPoolGptHandler) Export(c *gin.Context) {
	scope := c.DefaultQuery("scope", "all")
	switch scope {
	case "all", "valid", "invalid", "selected":
	default:
		scope = "all"
	}
	format := c.DefaultQuery("format", "internal")
	switch format {
	case "internal", "crs", "codex", "account_password":
	default:
		format = "internal"
	}
	// selected 时：ids 取自 query (?ids=1,2,3) 或 body (POST {"ids":[...]})
	var ids []uint64
	if scope == "selected" {
		if raw := strings.TrimSpace(c.Query("ids")); raw != "" {
			for _, s := range strings.Split(raw, ",") {
				if id, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64); err == nil && id > 0 {
					ids = append(ids, id)
				}
			}
		}
		// 也兼容 POST + JSON body
		if len(ids) == 0 {
			var body struct {
				IDs []uint64 `json:"ids"`
			}
			if err := c.ShouldBindBodyWithJSON(&body); err == nil {
				ids = body.IDs
			}
		}
		if len(ids) == 0 {
			response.Fail(c, errcode.InvalidParam.WithMsg("scope=selected 但 ids 为空"))
			return
		}
	}
	body, count, err := h.svc.ExportJSON(c.Request.Context(), scope, format, ids)
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	ext := service.GptExportFileExt(service.GptExportFormat(format))
	filename := "gpt-" + scope + "-" + format + "-" + strconv.FormatInt(time.Now().Unix(), 10) + "." + ext
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("X-Klein-Export-Count", strconv.Itoa(count))
	c.Data(200, service.GptExportContentType(service.GptExportFormat(format)), body)
}
