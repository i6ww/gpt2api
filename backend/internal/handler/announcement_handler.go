// Package handler 公告 - 用户端公开接口（无需登录）。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/response"
)

// AnnouncementHandler 用户端公告 handler。
type AnnouncementHandler struct {
	svc *service.AnnouncementService
}

// NewAnnouncementHandler 构造。
func NewAnnouncementHandler(s *service.AnnouncementService) *AnnouncementHandler {
	return &AnnouncementHandler{svc: s}
}

// ListActive GET /api/v1/announcements
// 返回当前时刻可见的公告（按 pinned + sort_order 排序）。
// 无需登录鉴权（首页未登录时也要看得见）。
func (h *AnnouncementHandler) ListActive(c *gin.Context) {
	items, err := h.svc.ActivePublic(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"list": items})
}
