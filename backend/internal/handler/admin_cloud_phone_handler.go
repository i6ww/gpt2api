package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// AdminCloudPhoneHandler /admin/api/v1/cloud-phones
type AdminCloudPhoneHandler struct {
	svc    *service.CloudPhoneService
	sysCfg *service.SystemConfigService
}

// NewAdminCloudPhoneHandler 构造。
func NewAdminCloudPhoneHandler(svc *service.CloudPhoneService, sys *service.SystemConfigService) *AdminCloudPhoneHandler {
	return &AdminCloudPhoneHandler{svc: svc, sysCfg: sys}
}

// List GET /admin/api/v1/cloud-phones
func (h *AdminCloudPhoneHandler) List(c *gin.Context) {
	var req dto.CloudPhoneListReq
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

// Stats GET /admin/api/v1/cloud-phones/stats
func (h *AdminCloudPhoneHandler) Stats(c *gin.Context) {
	out, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, out)
}

// Create POST /admin/api/v1/cloud-phones
func (h *AdminCloudPhoneHandler) Create(c *gin.Context) {
	var req dto.CloudPhoneCreateReq
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

// Update PUT /admin/api/v1/cloud-phones/:id
func (h *AdminCloudPhoneHandler) Update(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.CloudPhoneUpdateReq
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

// Delete DELETE /admin/api/v1/cloud-phones/:id
func (h *AdminCloudPhoneHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, nil)
}

// BatchDelete POST /admin/api/v1/cloud-phones/batch-delete
func (h *AdminCloudPhoneHandler) BatchDelete(c *gin.Context) {
	var req dto.CloudPhoneBatchDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.CloudPhoneBulkOpResult{Affected: n})
}

// Import POST /admin/api/v1/cloud-phones/import
func (h *AdminCloudPhoneHandler) Import(c *gin.Context) {
	var req dto.CloudPhoneImportReq
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

// GopayUnlinkOpenAI POST /admin/api/v1/cloud-phones/:id/gopay-unlink-openai
//
// 通过 GeeLark shell 在云手机内跑 uiautomator + input tap，自动进入
// GoPay「已连接应用」并尝试移除 OpenAI（与人工点「Hapus」同路径）。
func (h *AdminCloudPhoneHandler) GopayUnlinkOpenAI(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.CloudPhoneGopayUnlinkReq
	_ = c.ShouldBindJSON(&req)
	base := ""
	if h.sysCfg != nil {
		base = h.sysCfg.GeeLarkAPIBase(c.Request.Context())
	}
	if err := h.svc.GopayUnlinkOpenAI(c.Request.Context(), id, base, req.AppPackage); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}
