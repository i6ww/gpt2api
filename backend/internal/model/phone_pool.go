package model

import "time"

// PhonePool 状态。
const (
	PhoneStatusAvailable = "available" // 可领用 / 还没用满 max_uses
	PhoneStatusInUse     = "in_use"    // 正在被某次注册占用
	PhoneStatusExhausted = "exhausted" // 用满 max_uses（一般是 3 次），不再分配
	PhoneStatusBroken    = "broken"    // 失败次数超阈值或被接码商作废
	PhoneStatusDisabled  = "disabled"  // 人工停用
)

// PhonePool 接码手机号池实体。表 `phone_pool`。
type PhonePool struct {
	ID            uint64     `gorm:"primaryKey;column:id" json:"id"`
	Provider      string     `gorm:"column:provider;size:32;not null;default:herosms" json:"provider"`
	Service       string     `gorm:"column:service;size:32;not null;default:dr" json:"service"`
	Phone         string     `gorm:"column:phone;size:32;not null;uniqueIndex:uk_phone" json:"phone"`
	Country       int        `gorm:"column:country;not null;default:0" json:"country"`
	ActivationID  *string    `gorm:"column:activation_id;size:64" json:"activation_id,omitempty"`
	MaxUses       int        `gorm:"column:max_uses;not null;default:3" json:"max_uses"`
	UsedCount     int        `gorm:"column:used_count;not null;default:0" json:"used_count"`
	FailureCount  int        `gorm:"column:failure_count;not null;default:0" json:"failure_count"`
	Status        string     `gorm:"column:status;size:32;not null;default:available" json:"status"`
	LastAccountID *uint64    `gorm:"column:last_account_id" json:"last_account_id,omitempty"`
	LastUsedAt    *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	LastError     *string    `gorm:"column:last_error;size:255" json:"last_error,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt     *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (PhonePool) TableName() string { return "phone_pool" }
