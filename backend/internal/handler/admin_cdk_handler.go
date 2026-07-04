// Package handler 管理后台 - CDK handler。
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// AdminCDKHandler 管理后台 CDK 批次 handler。
type AdminCDKHandler struct {
	svc *service.CDKService
}

// NewAdminCDKHandler 构造。
func NewAdminCDKHandler(svc *service.CDKService) *AdminCDKHandler {
	return &AdminCDKHandler{svc: svc}
}

// CreateBatch POST /admin/api/v1/cdk/batches
func (h *AdminCDKHandler) CreateBatch(c *gin.Context) {
	var req dto.CDKBatchCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	var expire *time.Time
	if req.ExpireAt > 0 {
		t := time.Unix(req.ExpireAt, 0).UTC()
		expire = &t
	}
	uid := middleware.UID(c)
	batch, err := h.svc.GenerateBatch(c.Request.Context(), uid, req.BatchNo, req.Name, req.Points, req.Qty, req.PerUserLimit, expire)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{
		"id":        batch.ID,
		"batch_no":  batch.BatchNo,
		"total_qty": batch.TotalQty,
	})
}

// ListBatches GET /admin/api/v1/cdk/batches
func (h *AdminCDKHandler) ListBatches(c *gin.Context) {
	var req dto.AdminCDKBatchListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	rows, revoked, total, err := h.svc.ListBatches(c.Request.Context(), service.BatchListFilter{
		Keyword:  req.Keyword,
		Status:   req.Status,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	resp := make([]*dto.AdminCDKBatchResp, 0, len(rows))
	for _, r := range rows {
		resp = append(resp, mapCDKBatchResp(r, revoked[r.ID]))
	}
	response.OK(c, gin.H{
		"list":      resp,
		"total":     total,
		"page":      orDefault(req.Page, 1),
		"page_size": orDefault(req.PageSize, 20),
	})
}

// GetBatch GET /admin/api/v1/cdk/batches/:id
func (h *AdminCDKHandler) GetBatch(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	batch, revoked, err := h.svc.GetBatch(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, mapCDKBatchResp(batch, revoked))
}

// ToggleBatch POST /admin/api/v1/cdk/batches/:id/toggle
func (h *AdminCDKHandler) ToggleBatch(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.AdminCDKBatchToggleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	if err := h.svc.ToggleBatch(c.Request.Context(), id, req.Status); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"id": id, "status": req.Status})
}

// AppendBatch POST /admin/api/v1/cdk/batches/:id/append
func (h *AdminCDKHandler) AppendBatch(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.AdminCDKBatchAppendReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	appended, batch, err := h.svc.AppendBatch(c.Request.Context(), id, req.Qty)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, dto.AdminCDKBatchAppendResp{
		BatchID:  batch.ID,
		Appended: appended,
		TotalQty: batch.TotalQty,
	})
}

// ListCodes GET /admin/api/v1/cdk/batches/:id/codes
func (h *AdminCDKHandler) ListCodes(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	var req dto.AdminCDKCodeListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	rows, total, err := h.svc.ListCodes(c.Request.Context(), service.CodeListFilter{
		BatchID:  id,
		Status:   req.Status,
		Keyword:  req.Keyword,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	resp := make([]*dto.AdminCDKCodeResp, 0, len(rows))
	for _, r := range rows {
		resp = append(resp, mapCDKCodeResp(r))
	}
	response.OK(c, gin.H{
		"list":      resp,
		"total":     total,
		"page":      orDefault(req.Page, 1),
		"page_size": orDefault(req.PageSize, 50),
	})
}

// RevokeCode POST /admin/api/v1/cdk/codes/:id/revoke
func (h *AdminCDKHandler) RevokeCode(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	if err := h.svc.RevokeCode(c.Request.Context(), id); err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{"id": id, "status": model.CDKStatusInvalid})
}

// ExportBatch GET /admin/api/v1/cdk/batches/:id/export → text/csv
func (h *AdminCDKHandler) ExportBatch(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, errcode.InvalidParam)
		return
	}
	body, batch, err := h.svc.ExportBatchCSV(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	filename := fmt.Sprintf("cdk-%s.csv", batch.BatchNo)
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", body)
}

func mapCDKBatchResp(b *model.RedeemCodeBatch, revoked int) *dto.AdminCDKBatchResp {
	if b == nil {
		return nil
	}
	resp := &dto.AdminCDKBatchResp{
		ID:           b.ID,
		BatchNo:      b.BatchNo,
		Name:         b.Name,
		RewardType:   b.RewardType,
		TotalQty:     b.TotalQty,
		UsedQty:      b.UsedQty,
		RevokedQty:   revoked,
		RemainingQty: b.TotalQty - b.UsedQty - revoked,
		PerUserLimit: b.PerUserLimit,
		Status:       b.Status,
		CreatedAt:    b.CreatedAt.Unix(),
	}
	if resp.RemainingQty < 0 {
		resp.RemainingQty = 0
	}
	if b.ExpireAt != nil {
		resp.ExpireAt = b.ExpireAt.Unix()
	}
	if b.CreatedBy != nil {
		resp.CreatedBy = *b.CreatedBy
	}
	if b.RewardType == "points" {
		var v map[string]any
		if err := json.Unmarshal([]byte(b.RewardValue), &v); err == nil {
			switch p := v["points"].(type) {
			case float64:
				resp.RewardPoints = int64(p)
			case int64:
				resp.RewardPoints = p
			}
		}
	}
	return resp
}

func mapCDKCodeResp(c *model.RedeemCode) *dto.AdminCDKCodeResp {
	if c == nil {
		return nil
	}
	resp := &dto.AdminCDKCodeResp{
		ID:        c.ID,
		BatchID:   c.BatchID,
		Code:      c.Code,
		Status:    c.Status,
		CreatedAt: c.CreatedAt.Unix(),
	}
	if c.UsedBy != nil {
		resp.UsedBy = *c.UsedBy
	}
	if c.UsedAt != nil {
		resp.UsedAt = c.UsedAt.Unix()
	}
	return resp
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
