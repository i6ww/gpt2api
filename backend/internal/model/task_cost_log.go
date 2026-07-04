package model

import "time"

// CostLogRefType ref_type 字段枚举。
const (
	CostRefGeneration  = "generation"   // 图/视频/语音等异步生成
	CostRefChat        = "chat"         // OpenAI 兼容 /v1/chat/completions
	CostRefRegister    = "register"     // 号池注册（一次注册的总成本）
	CostRefAcquisition = "acquisition"  // 单项获客成本（验证码/SMS 等），Phase E
	CostRefRefund      = "refund"       // 取消/失败退款，写一条负数 cost
)

// TaskCostLog 每次成功调用的成本快照。
//
// 字段语义：
//   - cost_micro_usd: 这次调用花了多少 USD（× 1e6）。subscription/per_credit 通道里这是摊销价。
//   - sale_points / sale_micro_cny: 同步记录销售点数和按当时汇率折算的人民币价。
//   - fx_usd_to_cny: 写入时刻 USD→CNY 汇率快照；后续报表用同一行的 fx 求毛利，避免
//     未来汇率变化污染历史数据。
type TaskCostLog struct {
	ID                uint64    `gorm:"primaryKey;column:id" json:"id"`
	RefType           string    `gorm:"column:ref_type;size:16;not null;index:idx_task_cost_ref,priority:1" json:"ref_type"`
	RefID             string    `gorm:"column:ref_id;size:64;not null;index:idx_task_cost_ref,priority:2" json:"ref_id"`
	UserID            *uint64   `gorm:"column:user_id;index:idx_task_cost_user_recorded,priority:1" json:"user_id,omitempty"`
	UpstreamChannelID uint64    `gorm:"column:upstream_channel_id;not null;index:idx_task_cost_channel_recorded,priority:1" json:"upstream_channel_id"`
	AccountID         *uint64   `gorm:"column:account_id" json:"account_id,omitempty"`
	ModelCode         *string   `gorm:"column:model_code;size:64;index:idx_task_cost_model_recorded,priority:1" json:"model_code,omitempty"`
	VariantKey        *string   `gorm:"column:variant_key;size:32" json:"variant_key,omitempty"`
	UnitLabel         *string   `gorm:"column:unit_label;size:32" json:"unit_label,omitempty"`
	UnitQty           float64   `gorm:"column:unit_qty;type:decimal(14,4);not null;default:1" json:"unit_qty"`
	CostMicroUSD      int64     `gorm:"column:cost_micro_usd;not null;default:0" json:"cost_micro_usd"`
	SalePoints        int64     `gorm:"column:sale_points;not null;default:0" json:"sale_points"`
	SaleMicroCNY      int64     `gorm:"column:sale_micro_cny;not null;default:0" json:"sale_micro_cny"`
	FXUSDToCNY        float64   `gorm:"column:fx_usd_to_cny;type:decimal(10,4);not null;default:0" json:"fx_usd_to_cny"`
	RecordedAt        time.Time `gorm:"column:recorded_at;autoCreateTime(3);index:idx_task_cost_recorded;index:idx_task_cost_channel_recorded,priority:2;index:idx_task_cost_model_recorded,priority:2;index:idx_task_cost_user_recorded,priority:2" json:"recorded_at"`
}

// TableName 表名。
func (TaskCostLog) TableName() string { return "task_cost_log" }
