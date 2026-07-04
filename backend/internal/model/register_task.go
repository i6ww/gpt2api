package model

import "time"

// RegisterTask 状态机。
const (
	RegisterTaskPending   = "pending"
	RegisterTaskRunning   = "running"
	RegisterTaskSuccess   = "success"
	RegisterTaskFailed    = "failed"
	RegisterTaskCancelled = "cancelled"
)

// RegisterTaskProvider 注册目标号池。
const (
	RegisterTaskProviderAdobe = "adobe"
	RegisterTaskProviderGrok  = "grok"
	RegisterTaskProviderGPT   = "gpt"
)

// RegisterTask 号池注册任务实体。表 `register_task`。
type RegisterTask struct {
	ID               uint64     `gorm:"primaryKey;column:id" json:"id"`
	Provider         string     `gorm:"column:provider;size:16;not null" json:"provider"`
	Status           string     `gorm:"column:status;size:32;not null;default:pending" json:"status"`
	Step             *string    `gorm:"column:step;size:64" json:"step,omitempty"`
	Progress         uint8      `gorm:"column:progress;not null;default:0" json:"progress"`
	MailID           *uint64    `gorm:"column:mail_id" json:"mail_id,omitempty"`
	Email            *string    `gorm:"column:email;size:255" json:"email,omitempty"`
	Payload          []byte     `gorm:"column:payload;type:json" json:"-"`
	Result           []byte     `gorm:"column:result;type:json" json:"-"`
	Error            *string    `gorm:"column:error;size:500" json:"error,omitempty"`
	PoolAccountID    *uint64    `gorm:"column:pool_account_id" json:"pool_account_id,omitempty"`
	CancelRequested  bool       `gorm:"column:cancel_requested;not null;default:0" json:"cancel_requested"`
	CreatedBy        *uint64    `gorm:"column:created_by" json:"created_by,omitempty"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	StartedAt        *time.Time `gorm:"column:started_at" json:"started_at,omitempty"`
	FinishedAt       *time.Time `gorm:"column:finished_at" json:"finished_at,omitempty"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt        *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (RegisterTask) TableName() string { return "register_task" }
