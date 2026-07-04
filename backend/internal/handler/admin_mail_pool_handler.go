// Package handler 管理后台 - 共享邮箱池 handler。
package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// AdminMailPoolHandler /admin/api/v1/mail-pool 资源 handler。
type AdminMailPoolHandler struct {
	svc *service.MailPoolService
}

// NewAdminMailPoolHandler 构造。
func NewAdminMailPoolHandler(svc *service.MailPoolService) *AdminMailPoolHandler {
	return &AdminMailPoolHandler{svc: svc}
}

// List GET /admin/api/v1/mail-pool
func (h *AdminMailPoolHandler) List(c *gin.Context) {
	var req dto.MailPoolListReq
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
		ps = 50
	}
	response.Page(c, items, total, page, ps)
}

// Stats GET /admin/api/v1/mail-pool/stats
func (h *AdminMailPoolHandler) Stats(c *gin.Context) {
	res, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Import POST /admin/api/v1/mail-pool/import
func (h *AdminMailPoolHandler) Import(c *gin.Context) {
	var req dto.MailPoolImportReq
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

// Update PUT /admin/api/v1/mail-pool/:id
func (h *AdminMailPoolHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.MailPoolUpdateReq
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

// Delete DELETE /admin/api/v1/mail-pool/:id
func (h *AdminMailPoolHandler) Delete(c *gin.Context) {
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

// BatchDelete POST /admin/api/v1/mail-pool/batch-delete
func (h *AdminMailPoolHandler) BatchDelete(c *gin.Context) {
	var req dto.MailPoolBatchIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.MailPoolBulkOpResult{Affected: n})
}

// DeleteByStatus POST /admin/api/v1/mail-pool/delete-by-status
func (h *AdminMailPoolHandler) DeleteByStatus(c *gin.Context) {
	var req dto.MailPoolDeleteByStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.DeleteByStatus(c.Request.Context(), req.Status)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.MailPoolBulkOpResult{Affected: n})
}

// Truncate POST /admin/api/v1/mail-pool/truncate
//
// 按当前筛选条件软删全部匹配；filter 全空 = 清空整张表。
// 需要 body 里 confirm == "DELETE" 二次确认。
func (h *AdminMailPoolHandler) Truncate(c *gin.Context) {
	var req dto.MailPoolTruncateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.Truncate(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.MailPoolBulkOpResult{Affected: n})
}

// CFGenerate POST /admin/api/v1/mail-pool/cf-generate
//
// 调用系统配置中的 CF Worker /admin/new_address 一键生成 N 个临时邮箱并入池。
func (h *AdminMailPoolHandler) CFGenerate(c *gin.Context) {
	var req dto.MailPoolCFGenerateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	res, err := h.svc.GenerateCF(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Reset POST /admin/api/v1/mail-pool/reset
func (h *AdminMailPoolHandler) Reset(c *gin.Context) {
	var req dto.MailPoolBatchIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.Reset(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.MailPoolBulkOpResult{Affected: n})
}
