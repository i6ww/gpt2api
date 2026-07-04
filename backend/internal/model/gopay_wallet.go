package model

import "time"

// GopayWalletPool 状态。
const (
	GopayWalletStatusAvailable = "available"
	GopayWalletStatusLeased    = "leased"
	GopayWalletStatusCooldown  = "cooldown"
	GopayWalletStatusBanned    = "banned"
	GopayWalletStatusExhausted = "exhausted"
	GopayWalletStatusDisabled  = "disabled"
)

// GopayWalletBinding 状态。
const (
	GopayBindingStatusActive    = "active"
	GopayBindingStatusCancelled = "cancelled"
	GopayBindingStatusExpired   = "expired"
	GopayBindingStatusRefunded  = "refunded"
)

// GopayWalletPool GoPay 钱包池实体。表 `gopay_wallet_pool`。
//
// "一钱包多 Plus" 模型：
//   - active_plus_count 当前正绑定的活跃 Plus 数；满 quota 后置 exhausted
//   - 任一 binding 取消订阅 → active_plus_count - 1，转回 available
//
// dispatcher 抢锁顺序（FOR UPDATE SKIP LOCKED）：
//   status='available' AND (cooldown_until IS NULL OR cooldown_until <= NOW())
//   AND active_plus_count < per_wallet_quota
//   ORDER BY active_plus_count ASC, last_used_at ASC NULLS FIRST
//   LIMIT 1
type GopayWalletPool struct {
	ID              uint64     `gorm:"primaryKey;column:id" json:"id"`
	PINEnc          []byte     `gorm:"column:pin_enc;type:varbinary(256);not null" json:"-"`
	CloudPhoneID    string     `gorm:"column:cloud_phone_id;size:64;not null" json:"cloud_phone_id"`
	Status          string     `gorm:"column:status;size:16;not null;default:available" json:"status"`
	ActivePlusCount int        `gorm:"column:active_plus_count;not null;default:0" json:"active_plus_count"`
	TotalSuccess    int        `gorm:"column:total_success;not null;default:0" json:"total_success"`
	TotalFailed     int        `gorm:"column:total_failed;not null;default:0" json:"total_failed"`
	LastUsedAt      *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	LastError       *string    `gorm:"column:last_error;size:255" json:"last_error,omitempty"`
	CooldownUntil   *time.Time `gorm:"column:cooldown_until" json:"cooldown_until,omitempty"`
	Remark          *string    `gorm:"column:remark;size:255" json:"remark,omitempty"`
	CreatedBy       *uint64    `gorm:"column:created_by" json:"created_by,omitempty"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt       *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (GopayWalletPool) TableName() string { return "gopay_wallet_pool" }

// GopayWalletBinding 钱包-Plus 账号绑定实体。表 `gopay_wallet_binding`。
//
// 每次成功开 Plus 都写一行；后续可：
//   - 30 天到期前提醒续费
//   - 主动取消订阅以释放钱包配额
//   - 排查"哪个 Plus 是哪个钱包付的"
type GopayWalletBinding struct {
	ID           uint64     `gorm:"primaryKey;column:id" json:"id"`
	WalletID     uint64     `gorm:"column:wallet_id;not null" json:"wallet_id"`
	GptAccountID uint64     `gorm:"column:gpt_account_id;not null" json:"gpt_account_id"`
	CSID         *string    `gorm:"column:cs_id;size:128" json:"cs_id,omitempty"`
	ChargeRef    *string    `gorm:"column:charge_ref;size:64" json:"charge_ref,omitempty"`
	AmountIDR    int64      `gorm:"column:amount_idr;not null;default:0" json:"amount_idr"`
	ChargedAt    time.Time  `gorm:"column:charged_at;not null" json:"charged_at"`
	ExpiresAt    time.Time  `gorm:"column:expires_at;not null" json:"expires_at"`
	Status       string     `gorm:"column:status;size:16;not null;default:active" json:"status"`
	CancelledAt  *time.Time `gorm:"column:cancelled_at" json:"cancelled_at,omitempty"`
	Note         *string    `gorm:"column:note;size:255" json:"note,omitempty"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 表名。
func (GopayWalletBinding) TableName() string { return "gopay_wallet_binding" }
