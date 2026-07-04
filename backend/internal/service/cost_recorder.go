// Package service: 上游 API 管理 Phase B —— cost_recorder.go
//
// 调用方：generation_service.runTask 成功路径、chat_service.Complete/Stream 成功路径、
// register_service Phase E 等。
//
// 设计原则：
//   1. 失败容忍。channel 没配 / 价不全 / DB 写不进，都只打 warning 日志，不影响主流程。
//   2. 零外部 IO（除一次 INSERT）。所有 channel/route 数据从 UpstreamChannelService
//      的内存缓存读，避免影响任务延迟。
//   3. 价格全部用 micro_usd（USD * 1e6）整数算，避免浮点误差。
package service

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/logger"
)

// TokenUsage chat 类调用的 token 用量（input/output）。
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// CostRecordReq 主入口入参。
//
// 字段是 chat / generation / register 等共用的「最大并集」。调用方按需填，
// 不需要的字段留零值。
type CostRecordReq struct {
	RefType    string // "generation" / "chat" / "register" / "refund"
	RefID      string // task_id
	UserID     uint64
	ModelCode  string
	VariantKey string
	Provider   string // 用于兜底：route 没配时按 (provider, kind) 找通道
	Kind       string // "image" / "video" / "chat" / "register"
	AccountID  uint64
	UnitQty    float64    // 通常 = count；token 模式忽略此字段，从 TokenUsage 读
	Tokens     TokenUsage // 仅 per_token_io 通道用
	SalePoints int64      // 销售点数（点 *100），来自 task.cost_points
	// 显式 cost：custom 通道或退款记录时直接传入；非 0 则跳过 channel 计算
	OverrideCostMicroUSD *int64
	// 显式时间：默认 time.Now()。退款重放等场景可显式指定。
	RecordedAt time.Time
}

// CostRecorder 上游成本记录器。
//
// 持有：
//   - upstream channel service（解析 model_code → channel + multiplier）
//   - system config service（读 fx.usd_to_cny 等动态参数）
//   - task_cost_log repo（INSERT）
type CostRecorder struct {
	channels *UpstreamChannelService
	cfg      *SystemConfigService
	logs     *repo.TaskCostLogRepo
}

// NewCostRecorder 构造。任何依赖为 nil 都会让 Record 退化为 no-op。
func NewCostRecorder(channels *UpstreamChannelService, cfg *SystemConfigService, logs *repo.TaskCostLogRepo) *CostRecorder {
	return &CostRecorder{channels: channels, cfg: cfg, logs: logs}
}

// enabled 是否开启 cost 记录（system_config.cost_log.enabled 默认 true）。
func (r *CostRecorder) enabled(ctx context.Context) bool {
	if r == nil || r.channels == nil || r.logs == nil {
		return false
	}
	if r.cfg == nil {
		return true
	}
	return r.cfg.GetBool(ctx, SettingCostLogEnabled, true)
}

// FXUSDToCNY 当前生效汇率（fallback 7.2）。
func (r *CostRecorder) FXUSDToCNY(ctx context.Context) float64 {
	if r == nil || r.cfg == nil {
		return 7.2
	}
	raw := r.cfg.GetString(ctx, SettingFXUSDToCNY, "")
	if raw == "" {
		return 7.2
	}
	// 兼容存储格式：JSON number / JSON string / 裸数字
	raw = strings.Trim(raw, "\"")
	var f float64
	if err := json.Unmarshal([]byte(raw), &f); err == nil && f > 0 {
		return f
	}
	return 7.2
}

// PointToCNY 销售点数 → CNY 的换算因子（默认 0.01，即 1 点 ≈ 1 分）。
// cost_log 里 sale_micro_cny = sale_points × PointToCNY × 1e6 / 100。
func (r *CostRecorder) PointToCNY(ctx context.Context) float64 {
	if r == nil || r.cfg == nil {
		return 0.01
	}
	raw := r.cfg.GetString(ctx, SettingCostPointToCNY, "")
	if raw == "" {
		return 0.01
	}
	raw = strings.Trim(raw, "\"")
	var f float64
	if err := json.Unmarshal([]byte(raw), &f); err == nil && f > 0 {
		return f
	}
	return 0.01
}

// Record 主入口。失败只 warn，不影响调用方。
func (r *CostRecorder) Record(ctx context.Context, req CostRecordReq) {
	if !r.enabled(ctx) {
		return
	}
	if strings.TrimSpace(req.RefType) == "" || strings.TrimSpace(req.RefID) == "" {
		return
	}
	if req.UnitQty <= 0 {
		req.UnitQty = 1
	}
	if req.RecordedAt.IsZero() {
		req.RecordedAt = time.Now().UTC()
	}

	ch, rt, ok := r.channels.ResolveChannelForTask(ctx, req.ModelCode, req.VariantKey)
	if !ok {
		logger.FromCtx(ctx).Debug(
			"cost.record.channel_missing",
			zap.String("model", req.ModelCode),
			zap.String("variant", req.VariantKey),
			zap.String("provider", req.Provider),
		)
		return // 暂未配通道：跳过，不写日志，等运营在 admin 里手配
	}

	costMicroUSD := int64(0)
	if req.OverrideCostMicroUSD != nil {
		costMicroUSD = *req.OverrideCostMicroUSD
	} else {
		costMicroUSD = computeCostMicroUSD(ch, rt, req)
	}

	fx := r.FXUSDToCNY(ctx)
	pointToCNY := r.PointToCNY(ctx)
	saleMicroCNY := salePointsToMicroCNY(req.SalePoints, pointToCNY)
	unitLabel := unitLabelFor(ch.BillingMode, req.Tokens)

	row := &model.TaskCostLog{
		RefType:           req.RefType,
		RefID:             req.RefID,
		UpstreamChannelID: ch.ID,
		UnitQty:           req.UnitQty,
		CostMicroUSD:      costMicroUSD,
		SalePoints:        req.SalePoints,
		SaleMicroCNY:      saleMicroCNY,
		FXUSDToCNY:        fx,
		RecordedAt:        req.RecordedAt,
	}
	if req.UserID > 0 {
		uid := req.UserID
		row.UserID = &uid
	}
	if req.AccountID > 0 {
		aid := req.AccountID
		row.AccountID = &aid
	}
	if req.ModelCode != "" {
		mc := req.ModelCode
		row.ModelCode = &mc
	}
	if req.VariantKey != "" {
		vk := req.VariantKey
		row.VariantKey = &vk
	}
	if unitLabel != "" {
		ul := unitLabel
		row.UnitLabel = &ul
	}

	if err := r.logs.Create(ctx, row); err != nil {
		logger.FromCtx(ctx).Warn(
			"cost.record.insert_failed",
			zap.String("ref_type", req.RefType),
			zap.String("ref_id", req.RefID),
			zap.Uint64("channel_id", ch.ID),
			zap.Error(err),
		)
	}
}

// RecordRefund 退款记录：写一行负 cost / 负 sale 的反向日志。
func (r *CostRecorder) RecordRefund(ctx context.Context, refType, refID string, userID uint64, originalCostMicroUSD, originalSalePoints int64) {
	if !r.enabled(ctx) {
		return
	}
	override := -originalCostMicroUSD
	r.Record(ctx, CostRecordReq{
		RefType:              model.CostRefRefund,
		RefID:                refID,
		UserID:               userID,
		UnitQty:              1,
		SalePoints:           -originalSalePoints,
		OverrideCostMicroUSD: &override,
	})
}

// === 计算 ===

// computeCostMicroUSD 按 channel + route 的计费模式算单次成本（micro_usd）。
//
// 公式：
//   per_call         unit_price.micro_usd × unit_qty
//   per_unit         unit_price.micro_usd_per_unit × unit_qty
//   per_token_io     (input × input_per_1k + output × output_per_1k) / 1000
//   per_credit       (credits_per_call × unit_qty) × (monthly_pack_micro_usd / credits_per_month)
//   subscription     monthly_fixed_cost / expected_monthly_calls × unit_qty
//   custom           0（调用方必须用 OverrideCostMicroUSD 自己算）
//
// 最后再乘 route.cost_multiplier。
func computeCostMicroUSD(ch *model.UpstreamChannel, rt *model.UpstreamModelRoute, req CostRecordReq) int64 {
	if ch == nil {
		return 0
	}
	price := unmarshalJSONMap(ch.UnitPrice)
	multiplier := 1.0
	if rt != nil && rt.CostMultiplier > 0 {
		multiplier = rt.CostMultiplier
	}
	qty := req.UnitQty
	if qty <= 0 {
		qty = 1
	}

	switch ch.BillingMode {
	case model.BillingModePerCall:
		v := readFloat(price, "micro_usd")
		return int64(math.Round(v * qty * multiplier))

	case model.BillingModePerUnit:
		v := readFloat(price, "micro_usd_per_unit")
		return int64(math.Round(v * qty * multiplier))

	case model.BillingModePerTokenIO:
		in := readFloat(price, "input_per_1k_micro_usd")
		out := readFloat(price, "output_per_1k_micro_usd")
		cost := (in*float64(req.Tokens.InputTokens) + out*float64(req.Tokens.OutputTokens)) / 1000.0
		return int64(math.Round(cost * multiplier))

	case model.BillingModePerCredit:
		creditsPerCall := readFloat(price, "credits_per_call")
		creditsPerMonth := readFloat(price, "credits_per_month")
		monthlyMicroUSD := readFloat(price, "monthly_pack_micro_usd")
		if creditsPerMonth <= 0 || creditsPerCall <= 0 || monthlyMicroUSD <= 0 {
			// 不齐全：退回 0；CostRecorder 写日志时 cost=0，运营从 admin 后台看到「这通道还没配价」。
			return 0
		}
		credits := creditsPerCall * qty
		perCredit := monthlyMicroUSD / creditsPerMonth
		return int64(math.Round(credits * perCredit * multiplier))

	case model.BillingModeSubscription:
		if ch.ExpectedMonthlyCalls <= 0 {
			return 0
		}
		perCall := float64(ch.MonthlyFixedCost) / float64(ch.ExpectedMonthlyCalls)
		return int64(math.Round(perCall * qty * multiplier))

	case model.BillingModeCustom:
		return 0
	}
	return 0
}

// salePointsToMicroCNY 销售点数（unit = 1 点 ×100，即 100 ≈ 1 点）→ micro_cny。
// pointToCNY 是「1 点 = 多少 CNY」，默认 0.01。
// 公式：sale_points / 100 × pointToCNY × 1_000_000
func salePointsToMicroCNY(salePoints int64, pointToCNY float64) int64 {
	if salePoints == 0 || pointToCNY <= 0 {
		return 0
	}
	v := float64(salePoints) / 100.0 * pointToCNY * 1_000_000
	return int64(math.Round(v))
}

func unitLabelFor(billingMode string, tu TokenUsage) string {
	switch billingMode {
	case model.BillingModePerCall:
		return "call"
	case model.BillingModePerUnit:
		return "unit"
	case model.BillingModePerTokenIO:
		if tu.InputTokens > 0 || tu.OutputTokens > 0 {
			return "token_io"
		}
		return "token"
	case model.BillingModePerCredit:
		return "credit"
	case model.BillingModeSubscription:
		return "amortized"
	default:
		return ""
	}
}

func unmarshalJSONMap(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

func readFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}
