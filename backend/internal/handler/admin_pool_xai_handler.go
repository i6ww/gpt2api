// Package handler 管理后台 - 官方 xAI API 号池 handler。
package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// AdminPoolXAIHandler /admin/api/v1/pools/xai 资源 handler。
type AdminPoolXAIHandler struct {
	svc       *service.PoolXAIService
	pickProxy func() string
}

// NewAdminPoolXAIHandler 构造。pickProxy 可空（刷新走直连）。
func NewAdminPoolXAIHandler(svc *service.PoolXAIService, pickProxy func() string) *AdminPoolXAIHandler {
	return &AdminPoolXAIHandler{svc: svc, pickProxy: pickProxy}
}

// List GET /admin/api/v1/pools/xai
func (h *AdminPoolXAIHandler) List(c *gin.Context) {
	var req dto.XAIPoolListReq
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

// Stats GET /admin/api/v1/pools/xai/stats
func (h *AdminPoolXAIHandler) Stats(c *gin.Context) {
	res, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /admin/api/v1/pools/xai
func (h *AdminPoolXAIHandler) Create(c *gin.Context) {
	var req dto.XAIPoolCreateReq
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

// Update PUT /admin/api/v1/pools/xai/:id
func (h *AdminPoolXAIHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.XAIPoolUpdateReq
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

// Import POST /admin/api/v1/pools/xai/import
func (h *AdminPoolXAIHandler) Import(c *gin.Context) {
	var req dto.XAIPoolImportReq
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

// Delete DELETE /admin/api/v1/pools/xai/:id
func (h *AdminPoolXAIHandler) Delete(c *gin.Context) {
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

// BatchDelete POST /admin/api/v1/pools/xai/batch-delete
func (h *AdminPoolXAIHandler) BatchDelete(c *gin.Context) {
	var req dto.XAIPoolBatchIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.XAIPoolBulkOpResult{Affected: n})
}

// Purge POST /admin/api/v1/pools/xai/purge
func (h *AdminPoolXAIHandler) Purge(c *gin.Context) {
	var req dto.XAIPoolPurgeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.Purge(c.Request.Context(), repo.PoolXAIPurgeFilter{
		All:      req.All,
		Status:   req.Status,
		Abnormal: req.Abnormal,
	})
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	response.OK(c, dto.XAIPoolBulkOpResult{Affected: n})
}

// Refresh POST /admin/api/v1/pools/xai/:id/refresh
func (h *AdminPoolXAIHandler) Refresh(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	proxy := ""
	if h.pickProxy != nil {
		proxy = h.pickProxy()
	}
	row, err := h.svc.RefreshOne(c.Request.Context(), id, service.XAIRefreshOptions{
		ProxyURL: proxy,
		Caller:   "manual",
	})
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	resp := gin.H{
		"id":            row.ID,
		"status":        row.Status,
		"account_type":  row.AccountType,
		"failure_count": row.FailureCount,
	}
	if row.ExpiresAt != nil {
		resp["expires_at"] = row.ExpiresAt.UnixMilli()
	}
	response.OK(c, resp)
}

// RefreshBilling POST /admin/api/v1/pools/xai/:id/billing
// 查询单账号额度（cli-chat-proxy.grok.com/v1/billing，用 access_token，无需 Management Key）。
func (h *AdminPoolXAIHandler) RefreshBilling(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	proxy := ""
	if h.pickProxy != nil {
		proxy = h.pickProxy()
	}
	b, err := h.svc.RefreshBilling(c.Request.Context(), id, proxy)
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	response.OK(c, gin.H{
		"limit_usd":     float64(b.MonthlyLimitCents) / 100.0,
		"used_usd":      float64(b.UsedCents) / 100.0,
		"remaining_usd": float64(b.MonthlyLimitCents-b.UsedCents) / 100.0,
		"cap_usd":       float64(b.OnDemandCapCents) / 100.0,
		"period_end":    b.PeriodEnd,
	})
}

// RefreshBillingAll POST /admin/api/v1/pools/xai/billing/refresh-all
func (h *AdminPoolXAIHandler) RefreshBillingAll(c *gin.Context) {
	ok, fail := h.svc.RefreshBillingAll(c.Request.Context(), 6, h.pickProxy)
	response.OK(c, gin.H{"ok": ok, "fail": fail})
}

// BatchRefresh POST /admin/api/v1/pools/xai/batch-refresh
func (h *AdminPoolXAIHandler) BatchRefresh(c *gin.Context) {
	var req dto.XAIPoolBatchRefreshReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	scope := repo.PoolXAIRefreshScope(req.Scope)
	if scope == "" {
		scope = repo.XAIRefreshScopeAll
	}
	ok, fail := h.svc.RefreshByScope(c.Request.Context(), scope, 6, 500, h.pickProxy)
	response.OK(c, gin.H{"ok": ok, "fail": fail, "scope": string(scope)})
}
