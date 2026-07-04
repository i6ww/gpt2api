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

// AdminGopayWalletHandler /admin/api/v1/gopay-wallets
type AdminGopayWalletHandler struct {
	svc    *service.GopayWalletService
	sysSvc *service.SystemConfigService
}

// NewAdminGopayWalletHandler 构造。
func NewAdminGopayWalletHandler(svc *service.GopayWalletService, sysSvc *service.SystemConfigService) *AdminGopayWalletHandler {
	return &AdminGopayWalletHandler{svc: svc, sysSvc: sysSvc}
}

// List GET /admin/api/v1/gopay-wallets
func (h *AdminGopayWalletHandler) List(c *gin.Context) {
	var req dto.GopayWalletListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	quota := h.sysSvc.PlusUpgradePerWalletQuota(c.Request.Context())
	items, total, err := h.svc.List(c.Request.Context(), &req, quota)
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

// Stats GET /admin/api/v1/gopay-wallets/stats
func (h *AdminGopayWalletHandler) Stats(c *gin.Context) {
	out, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, out)
}

// Create POST /admin/api/v1/gopay-wallets
func (h *AdminGopayWalletHandler) Create(c *gin.Context) {
	var req dto.GopayWalletCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	uid := middleware.UID(c)
	w, err := h.svc.Create(c.Request.Context(), uid, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"id": w.ID})
}

// Update PUT /admin/api/v1/gopay-wallets/:id
func (h *AdminGopayWalletHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.GopayWalletUpdateReq
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

// Delete DELETE /admin/api/v1/gopay-wallets/:id
func (h *AdminGopayWalletHandler) Delete(c *gin.Context) {
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

// BatchDelete POST /admin/api/v1/gopay-wallets/batch-delete
func (h *AdminGopayWalletHandler) BatchDelete(c *gin.Context) {
	var req dto.GopayWalletBatchDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.GopayWalletBulkOpResult{Affected: n})
}

// Import POST /admin/api/v1/gopay-wallets/import
func (h *AdminGopayWalletHandler) Import(c *gin.Context) {
	var req dto.GopayWalletImportReq
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

// Secrets GET /admin/api/v1/gopay-wallets/:id/secrets
func (h *AdminGopayWalletHandler) Secrets(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	secrets, err := h.svc.SecretsByID(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, secrets)
}

// ListBindings GET /admin/api/v1/gopay-wallets/bindings
func (h *AdminGopayWalletHandler) ListBindings(c *gin.Context) {
	var req dto.GopayBindingListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	items, total, err := h.svc.ListBindings(c.Request.Context(), &req)
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

// CancelBinding POST /admin/api/v1/gopay-wallets/bindings/:id/cancel
func (h *AdminGopayWalletHandler) CancelBinding(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.GopayBindingCancelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// 允许空 body
		req = dto.GopayBindingCancelReq{}
	}
	if err := h.svc.CancelBinding(c.Request.Context(), id, req.Note); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, nil)
}
