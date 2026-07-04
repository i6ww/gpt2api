// Package handler 管理后台 - GROK 号池 handler。
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

// AdminPoolGrokHandler /admin/api/v1/pools/grok 资源 handler。
type AdminPoolGrokHandler struct {
	svc       *service.PoolGrokService
	pickProxy func() string // refresh 时挑选代理（轮转），可空走直连
}

// NewAdminPoolGrokHandler 构造。
//
// pickProxy 可空：留空时所有刷新走直连；否则每次返回一个不同的代理 URL。
func NewAdminPoolGrokHandler(svc *service.PoolGrokService, pickProxy func() string) *AdminPoolGrokHandler {
	return &AdminPoolGrokHandler{svc: svc, pickProxy: pickProxy}
}

// List GET /admin/api/v1/pools/grok
func (h *AdminPoolGrokHandler) List(c *gin.Context) {
	var req dto.GrokPoolListReq
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

// Stats GET /admin/api/v1/pools/grok/stats
func (h *AdminPoolGrokHandler) Stats(c *gin.Context) {
	res, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /admin/api/v1/pools/grok
func (h *AdminPoolGrokHandler) Create(c *gin.Context) {
	var req dto.GrokPoolCreateReq
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

// Update PUT /admin/api/v1/pools/grok/:id
func (h *AdminPoolGrokHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.GrokPoolUpdateReq
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

// Import POST /admin/api/v1/pools/grok/import
func (h *AdminPoolGrokHandler) Import(c *gin.Context) {
	var req dto.GrokPoolImportReq
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

// Delete DELETE /admin/api/v1/pools/grok/:id
func (h *AdminPoolGrokHandler) Delete(c *gin.Context) {
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

// BatchDelete POST /admin/api/v1/pools/grok/batch-delete
func (h *AdminPoolGrokHandler) BatchDelete(c *gin.Context) {
	var req dto.GrokPoolBatchIDsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.GrokPoolBulkOpResult{Affected: n})
}

// ExpireOverdue POST /admin/api/v1/pools/grok/expire-overdue
func (h *AdminPoolGrokHandler) ExpireOverdue(c *gin.Context) {
	n, err := h.svc.ExpireOverdue(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.GrokPoolBulkOpResult{Affected: n})
}

// Refresh POST /admin/api/v1/pools/grok/:id/refresh
//
// 立即触发一次单账号探测（rate-limits → 推断 type / 拉 quota）。
func (h *AdminPoolGrokHandler) Refresh(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	proxy := ""
	if h.pickProxy != nil {
		proxy = h.pickProxy()
	}
	row, err := h.svc.RefreshOne(c.Request.Context(), id, service.GrokRefreshOptions{
		ProxyURL: proxy,
		Caller:   "manual",
	})
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	resp := gin.H{
		"id":            row.ID,
		"account_type":  row.AccountType,
		"credits":       row.Credits,
		"quota_total":   row.QuotaTotal,
		"trial_status":  row.TrialStatus,
		"failure_count": row.FailureCount,
	}
	if row.LastCheckedAt != nil {
		resp["last_checked_at"] = row.LastCheckedAt.UnixMilli()
	}
	response.OK(c, resp)
}

// BatchRefresh POST /admin/api/v1/pools/grok/batch-refresh
//
// **异步**启动批量刷新任务。立即返回 jobID，前端轮询 /batch-refresh/status
// 看进度。一次只允许一个任务在跑（避免万级账号挤占代理池 / DB 连接）。
//
// scope 取值见 dto.GrokPoolBatchRefreshReq；省略 = all。
//
// 返回：
//
//	{
//	  "job_id":  "abcd1234...",
//	  "status":  "running",
//	  "scope":   "all",
//	  "started_at": 1730000000000  // ms
//	}
//
// 409 = 已有任务在跑（返回当前任务快照让前端继续接手轮询）。
func (h *AdminPoolGrokHandler) BatchRefresh(c *gin.Context) {
	var req dto.GrokPoolBatchRefreshReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	scope := repo.PoolGrokRefreshScope(req.Scope)
	if scope == "" {
		scope = repo.GrokRefreshScopeAll
	}
	snap, err := h.svc.StartBatchRefresh(scope, 6, h.pickProxy)
	if err != nil {
		// 已经有任务在跑 — 不算严重错误，回 409 + 当前快照让前端接管轮询。
		c.JSON(409, gin.H{
			"code":     errcode.IdemConflict.Code,
			"msg":      err.Error(),
			"data":     batchRefreshSnapToResp(snap),
			"trace_id": c.GetString("trace_id"),
		})
		return
	}
	response.OK(c, batchRefreshSnapToResp(snap))
}

// BatchRefreshStatus GET /admin/api/v1/pools/grok/batch-refresh/status
//
// 返回当前/最近一次批量刷新任务的进度。无任务时返回 {status:"idle"}。
func (h *AdminPoolGrokHandler) BatchRefreshStatus(c *gin.Context) {
	snap := h.svc.BatchRefreshSnapshot()
	if snap == nil {
		response.OK(c, gin.H{"status": "idle"})
		return
	}
	response.OK(c, batchRefreshSnapToResp(snap))
}

// BatchRefreshCancel POST /admin/api/v1/pools/grok/batch-refresh/cancel
//
// 取消正在跑的任务（已经在 in-flight 的 RefreshOne 会自然走完或撞超时）。
func (h *AdminPoolGrokHandler) BatchRefreshCancel(c *gin.Context) {
	ok := h.svc.CancelBatchRefresh()
	snap := h.svc.BatchRefreshSnapshot()
	resp := gin.H{"cancelled": ok}
	if snap != nil {
		resp["job"] = batchRefreshSnapToResp(snap)
	}
	response.OK(c, resp)
}

// batchRefreshSnapToResp 把 service 层 snapshot 序列化成前端友好结构。
func batchRefreshSnapToResp(s *service.GrokBatchRefreshJobSnapshot) gin.H {
	if s == nil {
		return gin.H{"status": "idle"}
	}
	errs := make([]gin.H, 0, len(s.Errors))
	for _, e := range s.Errors {
		errs = append(errs, gin.H{"message": e.Message, "count": e.Count})
	}
	out := gin.H{
		"job_id":     s.ID,
		"status":     s.Status,
		"scope":      s.Scope,
		"started_at": s.StartedAt.UnixMilli(),
		"scanned":    s.Scanned,
		"ok":         s.OK,
		"fail":       s.Fail,
		"errors":     errs,
	}
	if s.EndedAt != nil {
		out["ended_at"] = s.EndedAt.UnixMilli()
		out["elapsed_ms"] = s.EndedAt.Sub(s.StartedAt).Milliseconds()
	} else {
		out["elapsed_ms"] = time.Since(s.StartedAt).Milliseconds()
	}
	if s.LastError != "" {
		out["last_error"] = s.LastError
	}
	return out
}

// Purge POST /admin/api/v1/pools/grok/purge
//
// 按条件批量软删 GROK 账号，返回受影响行数。
//
// 前端 4 个常用入口：
//
//   - {"all": true}                → 删除全部
//   - {"status": "failed"}         → 删除失效（trial_status=failed）
//   - {"abnormal": true}           → 删除异常（failed + expired）
//   - {"zero_credits": true}       → 删除 0 额度账号
func (h *AdminPoolGrokHandler) Purge(c *gin.Context) {
	var req dto.GrokPoolPurgeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, err := h.svc.Purge(c.Request.Context(), repo.PoolGrokPurgeFilter{
		All:         req.All,
		Status:      req.Status,
		Abnormal:    req.Abnormal,
		ZeroCredits: req.ZeroCredits,
	})
	if err != nil {
		response.Fail(c, errcode.Internal.Wrap(err))
		return
	}
	response.OK(c, dto.GrokPoolBulkOpResult{Affected: n})
}
