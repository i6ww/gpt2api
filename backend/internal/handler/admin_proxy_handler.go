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

// AdminProxyHandler 管理后台代理接口
type AdminProxyHandler struct {
	svc     *service.ProxyService
	testSvc *service.AccountTestService
}

func NewAdminProxyHandler(svc *service.ProxyService, testSvc *service.AccountTestService) *AdminProxyHandler {
	return &AdminProxyHandler{svc: svc, testSvc: testSvc}
}

// List GET /admin/api/v1/proxies
func (h *AdminProxyHandler) List(c *gin.Context) {
	var req dto.ProxyListReq
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

// Create POST /admin/api/v1/proxies
func (h *AdminProxyHandler) Create(c *gin.Context) {
	var req dto.ProxyCreateReq
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

// Update PUT /admin/api/v1/proxies/:id
func (h *AdminProxyHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}

	var req dto.ProxyUpdateReq
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

// Delete DELETE /admin/api/v1/proxies/:id
func (h *AdminProxyHandler) Delete(c *gin.Context) {
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

// BatchDelete POST /admin/api/v1/proxies/batch-delete
func (h *AdminProxyHandler) BatchDelete(c *gin.Context) {
	var req dto.ProxyBatchDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}

	n, err := h.svc.BatchDeleteByIDs(c.Request.Context(), req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}

	response.OK(c, dto.ProxyBulkOpResult{Deleted: n})
}

// Import POST /admin/api/v1/proxies/import
func (h *AdminProxyHandler) Import(c *gin.Context) {
	var req dto.ProxyImportReq
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

// Test POST /admin/api/v1/proxies/:id/test
func (h *AdminProxyHandler) Test(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}

	if h.testSvc == nil {
		response.Fail(c, errcode.Internal.WithMsg("测试服务未启用"))
		return
	}

	p, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, errcode.ResourceMissing)
		return
	}

	res, err := h.testSvc.TestProxy(c.Request.Context(), p)
	if err != nil {
		response.Fail(c, err)
		return
	}

	response.OK(c, res)
}

// BatchTest POST /admin/api/v1/proxies/batch-test
func (h *AdminProxyHandler) BatchTest(c *gin.Context) {
	var req dto.ProxyBatchTestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}

	if h.testSvc == nil {
		response.Fail(c, errcode.Internal.WithMsg("测试服务未启用"))
		return
	}

	tested := 0
	success := 0
	failed := 0
	failedIDs := make([]uint64, 0)

	for _, id := range req.IDs {
		p, err := h.svc.GetByID(c.Request.Context(), id)
		if err != nil || p == nil {
			failed++
			failedIDs = append(failedIDs, id)
			continue
		}

		res, err := h.testSvc.TestProxy(c.Request.Context(), p)
		tested++
		if err != nil || !res.OK {
			failed++
			failedIDs = append(failedIDs, id)
			continue
		}

		success++
	}

	response.OK(c, dto.ProxyBatchTestResp{
		Tested:    tested,
		Success:   success,
		Failed:    failed,
		FailedIDs: failedIDs,
	})
}
