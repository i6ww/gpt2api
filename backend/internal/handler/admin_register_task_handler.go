// Package handler 管理后台 - 号池注册任务 handler。
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

// AdminRegisterTaskHandler /admin/api/v1/register-tasks 资源 handler。
type AdminRegisterTaskHandler struct {
	svc *service.RegisterTaskService
}

// NewAdminRegisterTaskHandler 构造。
func NewAdminRegisterTaskHandler(svc *service.RegisterTaskService) *AdminRegisterTaskHandler {
	return &AdminRegisterTaskHandler{svc: svc}
}

// List GET /admin/api/v1/register-tasks
func (h *AdminRegisterTaskHandler) List(c *gin.Context) {
	var req dto.RegisterTaskListReq
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

// Stats GET /admin/api/v1/register-tasks/stats
func (h *AdminRegisterTaskHandler) Stats(c *gin.Context) {
	provider := c.Query("provider")
	res, err := h.svc.Stats(c.Request.Context(), provider)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Get GET /admin/api/v1/register-tasks/:id
func (h *AdminRegisterTaskHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	res, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /admin/api/v1/register-tasks
func (h *AdminRegisterTaskHandler) Create(c *gin.Context) {
	var req dto.RegisterTaskCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	uid := middleware.UID(c)
	res, err := h.svc.Create(c.Request.Context(), &req, uid)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Cancel POST /admin/api/v1/register-tasks/:id/cancel
func (h *AdminRegisterTaskHandler) Cancel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	if err := h.svc.Cancel(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, nil)
}

// Delete DELETE /admin/api/v1/register-tasks/:id
func (h *AdminRegisterTaskHandler) Delete(c *gin.Context) {
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

// Purge DELETE /admin/api/v1/register-tasks
//
// 批量清空任务（仅清结束态：success / failed / cancelled），运行中 / 排队中保留。
// 查询参数：
//   - provider  仅清指定 provider 的任务（adobe / grok / gpt）；为空则跨 provider
func (h *AdminRegisterTaskHandler) Purge(c *gin.Context) {
	provider := c.Query("provider")
	n, err := h.svc.Purge(c.Request.Context(), provider)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"deleted": n})
}

// LogsPurge DELETE /admin/api/v1/register-tasks/logs
//
// 按 task_id / provider / level 过滤清理日志，零值即清空全部。
func (h *AdminRegisterTaskHandler) LogsPurge(c *gin.Context) {
	taskID, _ := strconv.ParseUint(c.Query("task_id"), 10, 64)
	provider := c.Query("provider")
	level := c.Query("level")
	n, err := h.svc.LogsPurge(c.Request.Context(), taskID, provider, level)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"deleted": n})
}

// Logs GET /admin/api/v1/register-tasks/logs
//
// 查询参数：
//   - task_id   仅查指定任务
//   - provider  adobe / grok / gpt
//   - level     info / warn / error
//   - limit     默认 200，最大 1000
func (h *AdminRegisterTaskHandler) Logs(c *gin.Context) {
	var req dto.RegisterTaskLogListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	rows, err := h.svc.LogsList(c.Request.Context(), req.TaskID, req.Provider, req.Level, req.Limit)
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]*dto.RegisterTaskLogResp, 0, len(rows))
	for _, r := range rows {
		entry := &dto.RegisterTaskLogResp{
			ID:        r.ID,
			TaskID:    r.TaskID,
			Provider:  r.Provider,
			Level:     r.Level,
			CreatedAt: r.CreatedAt.UnixMilli(),
		}
		if r.Step != nil {
			entry.Step = *r.Step
		}
		if r.Progress != nil {
			entry.Progress = *r.Progress
		}
		if r.Message != nil {
			entry.Message = *r.Message
		}
		out = append(out, entry)
	}
	response.OK(c, gin.H{"list": out})
}
