package model

import "time"

// MailPool 状态。
const (
	MailStatusAvailable  = "available"  // 可领用
	MailStatusInUse      = "in_use"     // 已被某号池领走，未结束
	MailStatusRegistered = "registered" // 已被成功用于注册
	MailStatusFailed     = "failed"     // 失败上限触达，永久失败
	MailStatusDisabled   = "disabled"   // 人工停用
)

// MailPool 收件后端。
const (
	MailModeOutlookIMAP  = "outlook_imap"
	MailModeOutlookGraph = "outlook_graph"
	MailModeTempmail     = "tempmail"
	MailModeCF           = "cf"
)

// MailPool 共享邮箱池实体。表 `mail_pool`。
type MailPool struct {
	ID               uint64     `gorm:"primaryKey;column:id" json:"id"`
	Email            string     `gorm:"column:email;size:255;not null" json:"email"`
	PasswordEnc      []byte     `gorm:"column:password_enc;type:blob;not null" json:"-"`
	ClientID         string     `gorm:"column:client_id;size:128;not null" json:"client_id"`
	RefreshTokenEnc  []byte     `gorm:"column:refresh_token_enc;type:blob;not null" json:"-"`
	Mode             string     `gorm:"column:mode;size:32;not null;default:outlook_graph" json:"mode"`
	Status           string     `gorm:"column:status;size:32;not null;default:available" json:"status"`
	FailureCount     int        `gorm:"column:failure_count;not null;default:0" json:"failure_count"`
	LastError        *string    `gorm:"column:last_error;size:500" json:"last_error,omitempty"`
	UsedByProvider   *string    `gorm:"column:used_by_provider;size:32" json:"used_by_provider,omitempty"`
	UsedByAccountID  *uint64    `gorm:"column:used_by_account_id" json:"used_by_account_id,omitempty"`
	ImportedAt       time.Time  `gorm:"column:imported_at;autoCreateTime" json:"imported_at"`
	UsedAt           *time.Time `gorm:"column:used_at" json:"used_at,omitempty"`
	RegisteredAt     *time.Time `gorm:"column:registered_at" json:"registered_at,omitempty"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt        *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (MailPool) TableName() string { return "mail_pool" }
