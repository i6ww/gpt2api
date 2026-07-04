// Package handler 公告 - admin 后台 CRUD。
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

// AdminAnnouncementHandler admin 后台公告管理。
type AdminAnnouncementHandler struct {
	svc *service.AnnouncementService
}

// NewAdminAnnouncementHandler 构造。
func NewAdminAnnouncementHandler(s *service.AnnouncementService) *AdminAnnouncementHandler {
	return &AdminAnnouncementHandler{svc: s}
}

// List GET /admin/api/v1/announcements
func (h *AdminAnnouncementHandler) List(c *gin.Context) {
	var req dto.AnnouncementListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	items, total, err := h.svc.ListAdmin(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"list": items, "total": total, "page": req.Page, "page_size": req.PageSize})
}

// Create POST /admin/api/v1/announcements
func (h *AdminAnnouncementHandler) Create(c *gin.Context) {
	var req dto.AnnouncementCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	adminID := middleware.MustUID(c)
	resp, err := h.svc.Create(c.Request.Context(), &req, adminID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, resp)
}

// Update PUT /admin/api/v1/announcements/:id
func (h *AdminAnnouncementHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.Fail(c, errcode.InvalidParam.WithMsg("缺少 id"))
		return
	}
	var req dto.AnnouncementUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	resp, err := h.svc.Update(c.Request.Context(), id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, resp)
}

// Delete DELETE /admin/api/v1/announcements/:id
func (h *AdminAnnouncementHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.Fail(c, errcode.InvalidParam.WithMsg("缺少 id"))
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}
