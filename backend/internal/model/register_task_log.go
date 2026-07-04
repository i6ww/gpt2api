package model

import "time"

// RegisterTaskLog 注册任务的逐步事件流。append-only。
type RegisterTaskLog struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	TaskID    uint64    `gorm:"column:task_id;not null;index" json:"task_id"`
	Provider  string    `gorm:"column:provider;size:16;not null" json:"provider"`
	Level     string    `gorm:"column:level;size:16;not null;default:info" json:"level"`
	Step      *string   `gorm:"column:step;size:64" json:"step,omitempty"`
	Progress  *uint8    `gorm:"column:progress" json:"progress,omitempty"`
	Message   *string   `gorm:"column:message;type:text" json:"message,omitempty"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
}

// TableName 表名。
func (RegisterTaskLog) TableName() string { return "register_task_log" }

// 日志级别常量。
const (
	RegisterLogInfo  = "info"
	RegisterLogWarn  = "warn"
	RegisterLogError = "error"
)
