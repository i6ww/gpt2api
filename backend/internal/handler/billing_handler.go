// Package handler 用户端计费 handler。
package handler

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// BillingHandler 用户端计费 handler。
type BillingHandler struct {
	billing *service.BillingService
	cdk     *service.CDKService
	sysCfg  *service.SystemConfigService
}

// NewBillingHandler 构造。
// sysCfg 用于读取 `recharge.packages` / `recharge.contact_*` / `payment.enabled` 等运营配置；
// 允许传 nil，对应接口会退化成"无套餐 / 无客服信息"。
func NewBillingHandler(b *service.BillingService, cdk *service.CDKService, sysCfg *service.SystemConfigService) *BillingHandler {
	return &BillingHandler{billing: b, cdk: cdk, sysCfg: sysCfg}
}

// Logs GET /api/v1/billing/logs?page=&page_size=
func (h *BillingHandler) Logs(c *gin.Context) {
	uid := middleware.MustUID(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	logs, total, err := h.billing.ListWalletLogs(c.Request.Context(), uid, page, pageSize)
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]*dto.WalletLogResp, 0, len(logs))
	for _, l := range logs {
		r := &dto.WalletLogResp{
			ID:           l.ID,
			Direction:    l.Direction,
			BizType:      l.BizType,
			BizID:        l.BizID,
			Points:       l.Points,
			PointsBefore: l.PointsBefore,
			PointsAfter:  l.PointsAfter,
			CreatedAt:    l.CreatedAt.Unix(),
		}
		if l.Remark != nil {
			r.Remark = *l.Remark
		}
		out = append(out, r)
	}
	response.Page(c, out, total, page, pageSize)
}

// RechargeProducts GET /api/v1/recharge/products
//
// 返回当前对外上架的充值套餐列表 + 客服联系方式（在线支付未接通时使用）。
// 数据源：admin 系统配置 system_config 表里的：
//   - recharge.packages       JSON 数组（套餐主体）
//   - recharge.contact_email  string 客服邮箱
//   - recharge.contact_notice string 给用户的购买说明（如"邮件附 UID + 套餐 ID"）
//   - payment.enabled         bool（预留，true 时前端可走在线支付下单，当前永远 false）
//
// 接口对**所有访客开放**（未登录也能看），所以必须严格过滤 internal 字段：
//   - 不返回 remark
//   - 不返回 admin only 的 contact_remark / 内部备注
//   - 单价 amount 直接用 admin 录入的元值；点数 / 赠点保持 ×100 整数与钱包一致
func (h *BillingHandler) RechargeProducts(c *gin.Context) {
	resp := dto.RechargeProductsResp{Products: []dto.RechargeProductItem{}}
	if h.sysCfg == nil {
		response.OK(c, resp)
		return
	}
	ctx := c.Request.Context()

	all, err := h.sysCfg.GetAll(ctx)
	if err != nil {
		// 配置读不到时返回空列表 + 不报错，避免支付配置异常打挂整个 billing 页面。
		response.OK(c, resp)
		return
	}

	if raw, ok := all["recharge.packages"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, item := range arr {
				obj, ok := item.(map[string]any)
				if !ok {
					continue
				}
				enabled := true
				if v, ok := obj["enabled"]; ok {
					enabled = toBool(v)
				}
				if !enabled {
					continue
				}
				resp.Products = append(resp.Products, dto.RechargeProductItem{
					ID:          strings.TrimSpace(toString(obj["id"])),
					Name:        strings.TrimSpace(toString(obj["name"])),
					Amount:      toFloat(obj["amount"]),
					Points:      toInt64(obj["points"]),
					BonusPoints: toInt64(obj["bonus_points"]),
					Badge:       strings.TrimSpace(toString(obj["badge"])),
					SortOrder:   int(toInt64(obj["sort_order"])),
				})
			}
			sort.SliceStable(resp.Products, func(i, j int) bool {
				return resp.Products[i].SortOrder < resp.Products[j].SortOrder
			})
		}
	}

	resp.Contact = dto.RechargeContactInfo{
		Email:  strings.TrimSpace(h.sysCfg.GetString(ctx, "recharge.contact_email", "")),
		Notice: strings.TrimSpace(h.sysCfg.GetString(ctx, "recharge.contact_notice", "")),
	}
	resp.OnlinePaymentEnabled = h.sysCfg.GetBool(ctx, "payment.enabled", false)

	response.OK(c, resp)
}

// === 局部 helpers：把 system_config GetAll 返回的 any 安全转成基础类型 ===

func toString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	default:
		return 0
	}
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	default:
		return 0
	}
}

func toBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(x)
		return b
	default:
		return false
	}
}

// RedeemCDK POST /api/v1/billing/cdk/redeem
func (h *BillingHandler) RedeemCDK(c *gin.Context) {
	var req dto.CDKRedeemReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	uid := middleware.MustUID(c)
	pts, err := h.cdk.Redeem(c.Request.Context(), uid, req.Code)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{
		"points":  pts,
		"biz":     model.BizCDK,
		"message": "兑换成功",
	})
}
