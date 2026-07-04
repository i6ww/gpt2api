// Package handler 管理后台 - FlowMusic（歌曲）Google 号池 handler。
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

// AdminPoolGoogleHandler /admin/api/v1/pools/google 资源 handler。
type AdminPoolGoogleHandler struct {
	svc *service.PoolGoogleService
}

// NewAdminPoolGoogleHandler 构造。
func NewAdminPoolGoogleHandler(svc *service.PoolGoogleService) *AdminPoolGoogleHandler {
	return &AdminPoolGoogleHandler{svc: svc}
}

// List GET /admin/api/v1/pools/google
func (h *AdminPoolGoogleHandler) List(c *gin.Context) {
	var req dto.GooglePoolListReq
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

// Stats GET /admin/api/v1/pools/google/stats
func (h *AdminPoolGoogleHandler) Stats(c *gin.Context) {
	res, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /admin/api/v1/pools/google
func (h *AdminPoolGoogleHandler) Create(c *gin.Context) {
	var req dto.GooglePoolCreateReq
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

// Update PUT /admin/api/v1/pools/google/:id
func (h *AdminPoolGoogleHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.GooglePoolUpdateReq
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

// Import POST /admin/api/v1/pools/google/import
func (h *AdminPoolGoogleHandler) Import(c *gin.Context) {
	var req dto.GooglePoolImportReq
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

// Delete DELETE /admin/api/v1/pools/google/:id
func (h *AdminPoolGoogleHandler) Delete(c *gin.Context) {
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

// BatchDelete POST /admin/api/v1/pools/google/batch-delete
func (h *AdminPoolGoogleHandler) BatchDelete(c *gin.Context) {
	var req dto.GooglePoolBatchIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.GooglePoolBulkOpResult{Affected: n})
}

// Refresh POST /admin/api/v1/pools/google/:id/refresh
//
// query：only_credits=1 仅查积分，不换 token。
func (h *AdminPoolGoogleHandler) Refresh(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	row, err := h.svc.RefreshOne(c.Request.Context(), id, service.GoogleRefreshOptions{
		OnlyCredits: c.Query("only_credits") == "1",
		Caller:      "manual",
	})
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	resp := gin.H{"id": row.ID, "credits": row.Credits}
	if row.ExpiresAt != nil {
		resp["expires_at"] = row.ExpiresAt.UnixMilli()
	}
	response.OK(c, resp)
}

// RefreshAll POST /admin/api/v1/pools/google/refresh-all
func (h *AdminPoolGoogleHandler) RefreshAll(c *gin.Context) {
	ok, fail := h.svc.RefreshExpiring(c.Request.Context(), 12*time.Hour, 2)
	response.OK(c, gin.H{"ok": ok, "fail": fail})
}

// BatchRefresh POST /admin/api/v1/pools/google/batch-refresh
func (h *AdminPoolGoogleHandler) BatchRefresh(c *gin.Context) {
	var req BatchRefreshReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	scope := repo.PoolGoogleRefreshScope(req.Scope)
	if scope == "" {
		scope = repo.GoogleRefreshScopeAll
	}
	ok, fail, total := h.svc.RefreshByScope(c.Request.Context(), scope, req.OnlyCredits)
	response.OK(c, gin.H{"ok": ok, "fail": fail, "total": total})
}
