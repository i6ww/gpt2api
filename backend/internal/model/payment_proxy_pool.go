package model

import "time"

// PaymentProxyPool 状态。
const (
	PaymentProxyStatusActive   = "active"
	PaymentProxyStatusDisabled = "disabled"
	PaymentProxyStatusBanned   = "banned"
)

// PaymentProxy 协议常量（与 Proxy 表共用枚举值）。
//
// PaymentProxyPool 印尼支付代理池实体。表 `payment_proxy_pool`。
//
// 与 proxy 表（号注册代理）独立：
//   - Phase B（Midtrans/GoPay）必须用印尼/东南亚 IP，否则 OTP 风控、linking 拒绝
//   - 大部分注册代理是 US / 全球 IP，与本池业务诉求不同
//
// 支持两种模式：
//   - 静态：host:port (+ username/password)
//   - 动态：api_url，每次取代理就 GET 该 URL，返回 host:port:user:pass 或 user:pass@host:port
type PaymentProxyPool struct {
	ID          uint64     `gorm:"primaryKey;column:id" json:"id"`
	Name        string     `gorm:"column:name;size:128;not null;default:''" json:"name"`
	Scheme      string     `gorm:"column:scheme;size:8;not null;default:http" json:"scheme"`
	Host        *string    `gorm:"column:host;size:255" json:"host,omitempty"`
	Port        int        `gorm:"column:port;not null;default:0" json:"port"`
	Username    *string    `gorm:"column:username;size:128" json:"username,omitempty"`
	PasswordEnc []byte     `gorm:"column:password_enc;type:varbinary(256)" json:"-"`
	APIURL      *string    `gorm:"column:api_url;size:512" json:"api_url,omitempty"`
	Country     string     `gorm:"column:country;size:8;not null;default:ID" json:"country"`
	Status      string     `gorm:"column:status;size:16;not null;default:active" json:"status"`
	TotalUsed   int        `gorm:"column:total_used;not null;default:0" json:"total_used"`
	TotalFailed int        `gorm:"column:total_failed;not null;default:0" json:"total_failed"`
	LastUsedAt  *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	LastCheckAt *time.Time `gorm:"column:last_check_at" json:"last_check_at,omitempty"`
	LastCheckOK int8       `gorm:"column:last_check_ok;not null;default:0" json:"last_check_ok"`
	LastCheckMs int        `gorm:"column:last_check_ms;not null;default:0" json:"last_check_ms"`
	LastError   *string    `gorm:"column:last_error;size:255" json:"last_error,omitempty"`
	Remark      *string    `gorm:"column:remark;size:255" json:"remark,omitempty"`
	CreatedBy   *uint64    `gorm:"column:created_by" json:"created_by,omitempty"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt   *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (PaymentProxyPool) TableName() string { return "payment_proxy_pool" }
