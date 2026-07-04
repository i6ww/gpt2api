package model

import "time"

// Asset 类型。
const (
	AssetKindGen   = "gen"   // 生成结果主资源
	AssetKindThumb = "thumb" // 生成结果缩略图
	AssetKindUser  = "asset" // 用户上传
)

// Locator 状态。
const (
	LocatorInvalid  int8 = 0 // 已失效（节点删了 / sha 校验失败）
	LocatorActive   int8 = 1
	LocatorTainted  int8 = 2 // 探测到不可达，等待 GC
)

// DownloadLocator 资源在某节点上有本地拷贝（表 `download_locator`）。
type DownloadLocator struct {
	ID            uint64     `gorm:"primaryKey;column:id" json:"id"`
	AssetKind     string     `gorm:"column:asset_kind;size:16;not null;default:'gen'" json:"asset_kind"`
	AssetKey      string     `gorm:"column:asset_key;size:255;not null" json:"asset_key"`
	NodeID        string     `gorm:"column:node_id;size:40;not null" json:"node_id"`
	RelPath       string     `gorm:"column:rel_path;size:512;not null" json:"rel_path"`
	SizeBytes     *int64     `gorm:"column:size_bytes" json:"size_bytes,omitempty"`
	SHA256        *string    `gorm:"column:sha256;size:64" json:"sha256,omitempty"`
	MIME          *string    `gorm:"column:mime;size:120" json:"mime,omitempty"`
	Status        int8       `gorm:"column:status;not null;default:1" json:"status"`
	LastServedAt  *time.Time `gorm:"column:last_served_at" json:"last_served_at,omitempty"`
	ServedCount   int64      `gorm:"column:served_count;not null;default:0" json:"served_count"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	ExpiresAt     *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"`
}

// TableName 表名。
func (DownloadLocator) TableName() string { return "download_locator" }
