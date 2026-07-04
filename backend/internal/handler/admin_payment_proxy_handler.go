package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// AdminPaymentProxyHandler /admin/api/v1/payment-proxies
type AdminPaymentProxyHandler struct {
	svc *service.PaymentProxyService
}

// NewAdminPaymentProxyHandler 构造。
func NewAdminPaymentProxyHandler(svc *service.PaymentProxyService) *AdminPaymentProxyHandler {
	return &AdminPaymentProxyHandler{svc: svc}
}

// List GET /admin/api/v1/payment-proxies
func (h *AdminPaymentProxyHandler) List(c *gin.Context) {
	var req dto.PaymentProxyListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	items, total, err := h.svc.List(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	page, pageSize := req.Page, req.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	response.Page(c, items, total, page, pageSize)
}

// Stats GET /admin/api/v1/payment-proxies/stats
func (h *AdminPaymentProxyHandler) Stats(c *gin.Context) {
	out, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, out)
}

// Create POST /admin/api/v1/payment-proxies
func (h *AdminPaymentProxyHandler) Create(c *gin.Context) {
	var req dto.PaymentProxyCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	uid := middleware.UID(c)
	p, err := h.svc.Create(c.Request.Context(), uid, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"id": p.ID})
}

// Update PUT /admin/api/v1/payment-proxies/:id
func (h *AdminPaymentProxyHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.PaymentProxyUpdateReq
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

// Delete DELETE /admin/api/v1/payment-proxies/:id
func (h *AdminPaymentProxyHandler) Delete(c *gin.Context) {
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

// BatchDelete POST /admin/api/v1/payment-proxies/batch-delete
func (h *AdminPaymentProxyHandler) BatchDelete(c *gin.Context) {
	var req dto.PaymentProxyBatchDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.PaymentProxyBulkOpResult{Affected: n})
}

// Import POST /admin/api/v1/payment-proxies/import
func (h *AdminPaymentProxyHandler) Import(c *gin.Context) {
	var req dto.PaymentProxyImportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	uid := middleware.UID(c)
	res, err := h.svc.BatchImport(c.Request.Context(), uid, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Test POST /admin/api/v1/payment-proxies/:id/test
func (h *AdminPaymentProxyHandler) Test(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	p, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	if p == nil {
		response.Fail(c, errcode.ResourceMissing)
		return
	}
	res := h.svc.Test(c.Request.Context(), p)
	if err := h.svc.MarkCheck(c.Request.Context(), id, res.OK, res.LatencyMs, res.Error); err != nil {
		// 记录失败不阻断响应
		_ = err
	}
	response.OK(c, res)
}
