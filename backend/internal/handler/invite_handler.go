// Package handler 用户端邀请中心。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// InviteHandler 邀请中心 HTTP 入口。
type InviteHandler struct {
	svc *service.InviteService
}

// NewInviteHandler 构造。
func NewInviteHandler(s *service.InviteService) *InviteHandler {
	return &InviteHandler{svc: s}
}

// Summary GET /api/v1/invite/summary
func (h *InviteHandler) Summary(c *gin.Context) {
	uid := middleware.MustUID(c)
	resp, err := h.svc.GetSummary(c.Request.Context(), uid)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, resp)
}

// Invitees GET /api/v1/invite/invitees?page=1&page_size=10
func (h *InviteHandler) Invitees(c *gin.Context) {
	var req dto.InviteeListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	uid := middleware.MustUID(c)
	resp, err := h.svc.ListInvitees(c.Request.Context(), uid, req.Page, req.PageSize)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, resp)
}
