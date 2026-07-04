package model

import "time"

// 节点角色。
const (
	ClusterRoleControl = "control"
	ClusterRoleAgent   = "agent"
	ClusterRoleEdge    = "edge"
)

// 节点状态。
const (
	ClusterNodePending     int8 = 0 // 待激活：已注册但未握手
	ClusterNodeEnabled     int8 = 1 // 启用：参与调度 / 下载
	ClusterNodeDisabled    int8 = 2 // 禁用：暂不参与
	ClusterNodeMaintenance int8 = 3 // 维护中：不再分配新任务，inflight 跑完即停
	ClusterNodeRevoked     int8 = 9 // 吊销：secret 失效，不可恢复
)

// ClusterNode 集群节点注册（表 `cluster_node`）。
type ClusterNode struct {
	NodeID          string     `gorm:"primaryKey;column:node_id;size:40" json:"node_id"`
	DisplayName     string     `gorm:"column:display_name;size:120;not null;default:''" json:"display_name"`
	Role            string     `gorm:"column:role;size:16;not null;default:'agent'" json:"role"`
	PublicHost      string     `gorm:"column:public_host;size:255;not null;default:''" json:"public_host"`
	InternalHost    string     `gorm:"column:internal_host;size:255;not null;default:''" json:"internal_host"`
	ProviderScope   string     `gorm:"column:provider_scope;type:json;not null" json:"provider_scope"`
	Weight          int        `gorm:"column:weight;not null;default:100" json:"weight"`
	MaxConcurrency  int        `gorm:"column:max_concurrency;not null;default:16" json:"max_concurrency"`
	DownloadOnly    int8       `gorm:"column:download_only;not null;default:0" json:"download_only"`
	AllowedIPs      string     `gorm:"column:allowed_ips;size:512;not null;default:''" json:"allowed_ips"`
	HMACSecretEnc   []byte     `gorm:"column:hmac_secret_enc;type:varbinary(255)" json:"-"`
	BootstrapUsed   int8       `gorm:"column:bootstrap_used;not null;default:0" json:"bootstrap_used"`
	Status          int8       `gorm:"column:status;not null;default:0" json:"status"`
	LastHeartbeatAt *time.Time `gorm:"column:last_heartbeat_at" json:"last_heartbeat_at,omitempty"`
	LastInflight    int        `gorm:"column:last_inflight;not null;default:0" json:"last_inflight"`
	LastIP          string     `gorm:"column:last_ip;size:45;not null;default:''" json:"last_ip"`
	Version         string     `gorm:"column:version;size:60;not null;default:''" json:"version"`
	Meta            *string    `gorm:"column:meta;type:json" json:"meta,omitempty"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 表名。
func (ClusterNode) TableName() string { return "cluster_node" }

// Embedded 主控自带的本地 agent 用此固定 id。
const ClusterEmbeddedNodeID = "control-main"
