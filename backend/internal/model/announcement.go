package model

import "time"

// Announcement 公告级别。
const (
	AnnouncementLevelInfo    = "info"
	AnnouncementLevelSuccess = "success"
	AnnouncementLevelWarning = "warning"
	AnnouncementLevelDanger  = "danger"
)

// Announcement 系统公告实体。表 `announcement`。
//
// 用户端首页顶部滚动条展示。admin 后台维护，支持时间窗 / 级别 / 置顶 / 排序。
type Announcement struct {
	ID        uint64     `gorm:"primaryKey;column:id" json:"id"`
	Title     string     `gorm:"column:title;size:128;not null" json:"title"`
	Content   string     `gorm:"column:content;type:text;not null" json:"content"`
	Level     string     `gorm:"column:level;size:16;not null;default:info" json:"level"`
	LinkURL   *string    `gorm:"column:link_url;size:500" json:"link_url,omitempty"`
	LinkText  *string    `gorm:"column:link_text;size:64" json:"link_text,omitempty"`
	Pinned    bool       `gorm:"column:pinned;not null;default:0" json:"pinned"`
	Enabled   bool       `gorm:"column:enabled;not null;default:1" json:"enabled"`
	StartAt   *time.Time `gorm:"column:start_at" json:"start_at,omitempty"`
	EndAt     *time.Time `gorm:"column:end_at" json:"end_at,omitempty"`
	SortOrder int        `gorm:"column:sort_order;not null;default:0" json:"sort_order"`
	CreatedBy *uint64    `gorm:"column:created_by" json:"created_by,omitempty"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (Announcement) TableName() string { return "announcement" }
