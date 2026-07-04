package dto

import "time"

// AdminClusterNodeListReq 后台分页查询。
type AdminClusterNodeListReq struct {
	Role     string `form:"role"`
	Status   *int8  `form:"status"`
	Keyword  string `form:"keyword"`
	Page     int    `form:"page"`
	PageSize int    `form:"page_size"`
}

// AdminClusterNodeItem 后台展示 DTO。
type AdminClusterNodeItem struct {
	NodeID          string     `json:"node_id"`
	DisplayName     string     `json:"display_name"`
	Role            string     `json:"role"`
	PublicHost      string     `json:"public_host"`
	InternalHost    string     `json:"internal_host,omitempty"`
	ProviderScope   []string   `json:"provider_scope"`
	Weight          int        `json:"weight"`
	MaxConcurrency  int        `json:"max_concurrency"`
	DownloadOnly    bool       `json:"download_only"`
	AllowedIPs      string     `json:"allowed_ips,omitempty"`
	Status          int8       `json:"status"`
	StatusLabel     string     `json:"status_label"`
	HasSecret       bool       `json:"has_secret"`
	BootstrapUsed   bool       `json:"bootstrap_used"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	HeartbeatAgeSec *int       `json:"heartbeat_age_sec,omitempty"`
	LastInflight    int        `json:"last_inflight"`
	LastIP          string     `json:"last_ip,omitempty"`
	Version         string     `json:"version,omitempty"`
	// PingFailStreak 反向 /healthz 探活的当前连续失败次数；3 次会被踢到 Maintenance。
	PingFailStreak int `json:"ping_fail_streak"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// AdminClusterOverview 设置面板「集群调度」卡片的实时摘要。
//
// 在 ConfigPage 上方显示，帮运维一眼判断：
//   - 主控自带 lease 通道是否在跑（control-main 心跳）
//   - 当前注册了多少 agent，多少在线，多少在 Maintenance
//   - 最近一次 ReclaimExpired / GC / ping 周期是否健康
type AdminClusterOverview struct {
	Enabled             bool       `json:"enabled"`
	EmbeddedAlive       bool       `json:"embedded_alive"`
	EmbeddedHeartbeatAt *time.Time `json:"embedded_heartbeat_at,omitempty"`
	EmbeddedInflight    int        `json:"embedded_inflight"`
	TotalNodes          int        `json:"total_nodes"`
	OnlineAgents        int        `json:"online_agents"`
	MaintenanceNodes    int        `json:"maintenance_nodes"`
	LeaseTTLSec         int        `json:"lease_ttl_sec"`
	HeartbeatDeadSec    int        `json:"heartbeat_dead_sec"`
	TicketTTLSec        int        `json:"ticket_ttl_sec"`
}

// AdminClusterNodeUpsertReq 创建 / 编辑节点。
type AdminClusterNodeUpsertReq struct {
	NodeID         string   `json:"node_id" binding:"required,min=1,max=40"`
	DisplayName    string   `json:"display_name"`
	Role           string   `json:"role,omitempty"`
	PublicHost     string   `json:"public_host"`
	InternalHost   string   `json:"internal_host,omitempty"`
	ProviderScope  []string `json:"provider_scope"`
	Weight         int      `json:"weight,omitempty"`
	MaxConcurrency int      `json:"max_concurrency,omitempty"`
	DownloadOnly   bool     `json:"download_only,omitempty"`
	AllowedIPs     string   `json:"allowed_ips,omitempty"`
}

// AdminClusterNodeUpsertResp 创建后端返回（bootstrap_token 仅在首次或重发时给）。
type AdminClusterNodeUpsertResp struct {
	Node           AdminClusterNodeItem `json:"node"`
	BootstrapToken string               `json:"bootstrap_token,omitempty"`
	ControlURL     string               `json:"control_url,omitempty"`
}

// AdminClusterNodeStatusReq 启停 / 维护 / 吊销。
type AdminClusterNodeStatusReq struct {
	Status int8 `json:"status" binding:"required"`
}

// ── agent → 主控 内部接口 DTO ───────────────────────────────

type ClusterHandshakeReq struct {
	Token     string `json:"token" binding:"required"`
	PublicURL string `json:"public_url,omitempty"`
	Version   string `json:"version,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
}

type ClusterHandshakeResp struct {
	NodeID         string   `json:"node_id"`
	HMACSecret     string   `json:"hmac_secret"` // base64url；首次握手才返回
	ProviderScope  []string `json:"provider_scope"`
	MaxConcurrency int      `json:"max_concurrency"`
	HeartbeatSec   int      `json:"heartbeat_sec"`
	LeaseSec       int      `json:"lease_sec"`
	StorageRoot    string   `json:"storage_root"`
}

type ClusterHeartbeatReq struct {
	Inflight int    `json:"inflight"`
	Version  string `json:"version,omitempty"`
}

type ClusterLeaseReq struct {
	Max       int      `json:"max"`
	Providers []string `json:"providers,omitempty"`
}

type ClusterResultReq struct {
	TaskID  string                  `json:"task_id" binding:"required"`
	Status  int8                    `json:"status" binding:"required"` // 2 / 3
	Error   string                  `json:"error,omitempty"`
	Cost    int64                   `json:"cost_points,omitempty"`
	Results []ClusterResultRowItem  `json:"results,omitempty"`
}

// ── 运维接口 ───────────────────────────────────────────────

// AdminClusterTaintReq 手动把某节点上某个资源标 tainted —— 后续 ResolveDownload 跳过它。
type AdminClusterTaintReq struct {
	AssetKind string `json:"asset_kind"`         // gen / thumb，缺省 gen
	AssetKey  string `json:"asset_key" binding:"required"`
	NodeID    string `json:"node_id" binding:"required"`
}

// GenTaintedReq 用户态（浏览器 / SDK）下载失败时主动汇报。
//
// 端点：POST /api/v1/gen/cached/tainted
// 公开匿名 + IP 限流；服务端只标 status=tainted，不做物理删除。
type GenTaintedReq struct {
	AssetKind string `json:"asset_kind,omitempty"` // gen / thumb / asset；空则按 asset_key 自动推断
	AssetKey  string `json:"asset_key" binding:"required"`
	NodeID    string `json:"node_id" binding:"required"`
	Reason    string `json:"reason,omitempty"` // 客户端可选解释（仅日志，不入库）
}

type ClusterResultRowItem struct {
	Seq        int                    `json:"seq"`
	URL        string                 `json:"url,omitempty"`
	RelPath    string                 `json:"rel_path,omitempty"`
	ThumbRel   string                 `json:"thumb_rel,omitempty"`
	Width      *int                   `json:"width,omitempty"`
	Height     *int                   `json:"height,omitempty"`
	DurationMs *int                   `json:"duration_ms,omitempty"`
	SizeBytes  *int64                 `json:"size_bytes,omitempty"`
	SHA256     string                 `json:"sha256,omitempty"`
	MIME       string                 `json:"mime,omitempty"`
	Meta       map[string]any         `json:"meta,omitempty"`
}
