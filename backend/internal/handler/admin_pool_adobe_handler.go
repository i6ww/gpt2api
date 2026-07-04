// Package handler 管理后台 - ADOBE 号池 handler。
package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// AdminPoolAdobeHandler /admin/api/v1/pools/adobe 资源 handler。
type AdminPoolAdobeHandler struct {
	svc       *service.PoolAdobeService
	pickProxy func() string // 用于 refresh 时挑选代理（轮转）；可空走直连
}

// NewAdminPoolAdobeHandler 构造。
//
// pickProxy 可空：留空时刷新走直连；否则每次返回一个代理 URL（轮转）。
func NewAdminPoolAdobeHandler(svc *service.PoolAdobeService, pickProxy func() string) *AdminPoolAdobeHandler {
	return &AdminPoolAdobeHandler{svc: svc, pickProxy: pickProxy}
}

// List GET /admin/api/v1/pools/adobe
func (h *AdminPoolAdobeHandler) List(c *gin.Context) {
	var req dto.AdobePoolListReq
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

// Stats GET /admin/api/v1/pools/adobe/stats
func (h *AdminPoolAdobeHandler) Stats(c *gin.Context) {
	res, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /admin/api/v1/pools/adobe
func (h *AdminPoolAdobeHandler) Create(c *gin.Context) {
	var req dto.AdobePoolCreateReq
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

// Update PUT /admin/api/v1/pools/adobe/:id
func (h *AdminPoolAdobeHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.AdobePoolUpdateReq
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

// Import POST /admin/api/v1/pools/adobe/import
func (h *AdminPoolAdobeHandler) Import(c *gin.Context) {
	var req dto.AdobePoolImportReq
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

// Delete DELETE /admin/api/v1/pools/adobe/:id
func (h *AdminPoolAdobeHandler) Delete(c *gin.Context) {
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

// BatchDelete POST /admin/api/v1/pools/adobe/batch-delete
func (h *AdminPoolAdobeHandler) BatchDelete(c *gin.Context) {
	var req dto.AdobePoolBatchIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.AdobePoolBulkOpResult{Affected: n})
}

// Refresh POST /admin/api/v1/pools/adobe/:id/refresh
//
// 立即触发一次单账号刷新（silent refresh + profile + credits）。
//
// query：only_credits=1 时跳过换 token，仅刷新积分。
func (h *AdminPoolAdobeHandler) Refresh(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	onlyCredits := c.Query("only_credits") == "1"
	proxy := ""
	if h.pickProxy != nil {
		proxy = h.pickProxy()
	}
	row, err := h.svc.RefreshOne(c.Request.Context(), id, service.AdobeRefreshOptions{
		ProxyURL:    proxy,
		OnlyCredits: onlyCredits,
		Caller:      "manual",
	})
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	resp := gin.H{
		"id":      row.ID,
		"credits": row.Credits,
	}
	if row.ExpiresAt != nil {
		resp["expires_at"] = row.ExpiresAt.UnixMilli()
	}
	response.OK(c, resp)
}

// RefreshAll POST /admin/api/v1/pools/adobe/refresh-all
//
// 触发一次后台扫描（< 12h 过期的账号一并刷新）。同步等待结束并返回成功 / 失败计数。
func (h *AdminPoolAdobeHandler) RefreshAll(c *gin.Context) {
	// 12h 与 newwork python 默认一致；后续可改成读 system_config。
	ok, fail := h.svc.RefreshExpiring(c.Request.Context(), 12*time.Hour, 4, h.pickProxy)
	response.OK(c, gin.H{"ok": ok, "fail": fail})
}

// PurgeReq 批量软删过滤入参（POST /pools/adobe/purge）。
//
// 多字段同时给时按 AND 处理；都不给时拒绝执行（返回 affected=0）。
type PurgeReq struct {
	All               bool   `json:"all"`                 // true → 删除全部（不带任何条件）
	Status            string `json:"status"`              // 等于该 status（"invalid" / "cooldown" / "valid" / "disabled"）
	ZeroCredits       bool   `json:"zero_credits"`        // true → credits <= 0
	TokenExpired      bool   `json:"token_expired"`       // true → expires_at IS NULL OR < now
	QuotaRecoveryDays int    `json:"quota_recovery_days"` // >0 → 删除额度回收中且 updated_at 早于 N 天前
}

// Purge POST /admin/api/v1/pools/adobe/purge
//
// 按条件批量软删 ADOBE 账号。返回受影响行数。
//
// 前端 4 个常用入口：
//
//   - {"all": true}                → 删除全部
//   - {"status": "invalid"}        → 删除失效
//   - {"zero_credits": true}       → 删除 0 积分
//   - {"token_expired": true}      → 删除 Token 失效
func (h *AdminPoolAdobeHandler) Purge(c *gin.Context) {
	var req PurgeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.Purge(c.Request.Context(), repo.PoolAdobePurgeFilter{
		All:               req.All,
		Status:            req.Status,
		ZeroCredits:       req.ZeroCredits,
		TokenExpired:      req.TokenExpired,
		QuotaRecoveryDays: req.QuotaRecoveryDays,
	})
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	response.OK(c, dto.AdobePoolBulkOpResult{Affected: n})
}

// BatchRefreshReq 批量刷新入参。
type BatchRefreshReq struct {
	Scope       string `json:"scope"`        // "all" / "zero_credits" / "abnormal" / "expiring" / "quota_recovery"
	OnlyCredits bool   `json:"only_credits"` // true → 跳过 silent refresh，只拿 profile + credits
}

// Export GET /admin/api/v1/pools/adobe/export?scope=all|valid|invalid
//
// 返回 `application/json` 文件，内容为 JSON Array，每个元素含完整账号信息：
// email / password / access_token / cookie 解密为明文，附带运维元数据
// (status / source / credits / expires_at / refresh_enabled / *_at 等)。
//
// 与 Import 完全互通：导出的 JSON 文件可以直接粘贴到导入对话框完整克隆账号
// （依据 email upsert）。
//
// 仅 admin token 可访问。响应头：
//   - Content-Disposition: attachment; filename=adobe-<scope>-<unix>.json
//   - X-Klein-Export-Count: <count>
func (h *AdminPoolAdobeHandler) Export(c *gin.Context) {
	scope := repo.PoolAdobeExportScope(c.DefaultQuery("scope", "all"))
	switch scope {
	case repo.AdobeExportScopeAll, repo.AdobeExportScopeValid, repo.AdobeExportScopeInvalid:
	default:
		scope = repo.AdobeExportScopeAll
	}
	body, count, err := h.svc.ExportJSON(c.Request.Context(), scope)
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	filename := "adobe-" + string(scope) + "-" + strconv.FormatInt(time.Now().Unix(), 10) + ".json"
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("X-Klein-Export-Count", strconv.Itoa(count))
	c.Data(200, "application/json; charset=utf-8", body)
}

// BatchRefresh POST /admin/api/v1/pools/adobe/batch-refresh
//
// 同步等待全部刷新结束并返回 ok / fail / total。
//
// 前端 5 个常用入口：
//
//   - {"scope":"zero_credits","only_credits":true} → 刷新 0 积分
//   - {"scope":"abnormal"}                          → 刷新异常账号 token+积分
//   - {"scope":"all"}                               → 刷新全部账号 token+积分
//   - {"scope":"abnormal","only_credits":true}      → 刷新异常账号仅积分（少见）
//   - {"scope":"expiring"}                          → 与 refresh-all 等价
func (h *AdminPoolAdobeHandler) BatchRefresh(c *gin.Context) {
	var req BatchRefreshReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	scope := repo.PoolAdobeRefreshScope(req.Scope)
	if scope == "" {
		scope = repo.AdobeRefreshScopeAll
	}
	ok, fail, total := h.svc.RefreshByScope(
		c.Request.Context(),
		scope,
		req.OnlyCredits,
		4,
		h.pickProxy,
	)
	response.OK(c, gin.H{"ok": ok, "fail": fail, "total": total})
}
