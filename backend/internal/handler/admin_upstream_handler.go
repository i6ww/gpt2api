// Package handler: admin 上游 API 管理路由处理器。
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

// AdminUpstreamHandler 上游通道 + 路由 + 利润报表的 admin handler。
type AdminUpstreamHandler struct {
	svc       *service.UpstreamChannelService
	costSvc   *service.CostRecorder
	logsRepo  *repo.TaskCostLogRepo
}

// NewAdminUpstreamHandler 构造。
func NewAdminUpstreamHandler(svc *service.UpstreamChannelService, cost *service.CostRecorder, logs *repo.TaskCostLogRepo) *AdminUpstreamHandler {
	return &AdminUpstreamHandler{svc: svc, costSvc: cost, logsRepo: logs}
}

// === Channels ===

// ListChannels GET /admin/api/v1/upstream/channels
func (h *AdminUpstreamHandler) ListChannels(c *gin.Context) {
	var req dto.ChannelListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	items, total, err := h.svc.ListChannels(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	page := req.Page
	pageSize := req.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	response.Page(c, items, total, page, pageSize)
}

// CreateChannel POST /admin/api/v1/upstream/channels
func (h *AdminUpstreamHandler) CreateChannel(c *gin.Context) {
	var req dto.ChannelSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	ch, err := h.svc.CreateChannel(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"id": ch.ID})
}

// UpdateChannel PUT /admin/api/v1/upstream/channels/:id
func (h *AdminUpstreamHandler) UpdateChannel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.ChannelSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	if err := h.svc.UpdateChannel(c.Request.Context(), id, &req); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// DeleteChannel DELETE /admin/api/v1/upstream/channels/:id
func (h *AdminUpstreamHandler) DeleteChannel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	if err := h.svc.DeleteChannel(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// SeedChannels POST /admin/api/v1/upstream/channels/seed
// 当表为空时一键导入默认 15 行清单。已有数据时不动。
func (h *AdminUpstreamHandler) SeedChannels(c *gin.Context) {
	if err := h.svc.SeedIfEmpty(c.Request.Context()); err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// === Routes ===

// ListRoutes GET /admin/api/v1/upstream/routes
func (h *AdminUpstreamHandler) ListRoutes(c *gin.Context) {
	var req dto.RouteListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	items, total, err := h.svc.ListRoutes(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	page := req.Page
	pageSize := req.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 200
	}
	response.Page(c, items, total, page, pageSize)
}

// CreateRoute POST /admin/api/v1/upstream/routes
func (h *AdminUpstreamHandler) CreateRoute(c *gin.Context) {
	var req dto.RouteSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	rt, err := h.svc.CreateRoute(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"id": rt.ID})
}

// UpdateRoute PUT /admin/api/v1/upstream/routes/:id
func (h *AdminUpstreamHandler) UpdateRoute(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.RouteSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	if err := h.svc.UpdateRoute(c.Request.Context(), id, &req); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// DeleteRoute DELETE /admin/api/v1/upstream/routes/:id
func (h *AdminUpstreamHandler) DeleteRoute(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	if err := h.svc.DeleteRoute(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// === Profit Reports ===

// ProfitOverview GET /admin/api/v1/upstream/profit/overview?from=&to=
// 返回区间内总成本 / 总营收 / 毛利。
func (h *AdminUpstreamHandler) ProfitOverview(c *gin.Context) {
	var req dto.ProfitReportReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	from, to := parseRange(req.From, req.To)
	tc, costUSD, saleCNY, salePts, err := h.logsRepo.AggregateBucket(c.Request.Context(), from, to)
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	fx := h.costSvc.FXUSDToCNY(c.Request.Context())
	costInCNY := int64(float64(costUSD) * fx) // micro_usd × fx = micro_cny
	margin := saleCNY - costInCNY
	rate := 0.0
	if saleCNY > 0 {
		rate = float64(margin) / float64(saleCNY)
	}
	response.OK(c, dto.ProfitOverview{
		TaskCount:       tc,
		CostMicroUSD:    costUSD,
		SaleMicroCNY:    saleCNY,
		SalePoints:      salePts,
		GrossMarginCNY:  margin,
		GrossMarginRate: rate,
		FxUSDToCNY:      fx,
	})
}

// ProfitDaily GET /admin/api/v1/upstream/profit/daily?from=&to=&dim=day,model
func (h *AdminUpstreamHandler) ProfitDaily(c *gin.Context) {
	var req dto.ProfitReportReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	from, to := parseRange(req.From, req.To)
	rows, err := h.logsRepo.ProfitDaily(c.Request.Context(), req.Dim, from, to)
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	fx := h.costSvc.FXUSDToCNY(c.Request.Context())
	response.OK(c, gin.H{"items": rows, "fx_usd_to_cny": fx})
}

// CostLogs GET /admin/api/v1/upstream/logs
// 按各种维度查 task_cost_log 明细。
func (h *AdminUpstreamHandler) CostLogs(c *gin.Context) {
	q := c.Request.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	channelID, _ := strconv.ParseUint(q.Get("channel_id"), 10, 64)
	userID, _ := strconv.ParseUint(q.Get("user_id"), 10, 64)
	f := repo.CostListFilter{
		RefType:   q.Get("ref_type"),
		ModelCode: q.Get("model_code"),
		ChannelID: channelID,
		UserID:    userID,
		Page:      page,
		PageSize:  pageSize,
	}
	from := q.Get("from")
	to := q.Get("to")
	if from != "" || to != "" {
		fromT, toT := parseRange(from, to)
		f.StartedAt = &fromT
		f.EndedAt = &toT
	}
	rows, total, err := h.logsRepo.List(c.Request.Context(), f)
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	response.Page(c, rows, total, page, pageSize)
}

// parseRange 解析 from/to；缺省时默认最近 30 天。
func parseRange(from, to string) (time.Time, time.Time) {
	now := time.Now().UTC()
	defaultFrom := now.AddDate(0, 0, -30)
	defaultTo := now.Add(24 * time.Hour)
	parsedFrom, _ := parseTimeLoose(from)
	parsedTo, _ := parseTimeLoose(to)
	if parsedFrom.IsZero() {
		parsedFrom = defaultFrom
	}
	if parsedTo.IsZero() {
		parsedTo = defaultTo
	}
	return parsedFrom, parsedTo
}

func parseTimeLoose(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, nil
}
