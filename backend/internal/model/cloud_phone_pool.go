package model

import "time"

// CloudPhonePool 状态。
const (
	CloudPhoneStatusOnline   = "online"
	CloudPhoneStatusOffline  = "offline"
	CloudPhoneStatusBanned   = "banned"
	CloudPhoneStatusDisabled = "disabled"
)

// CloudPhone 探测结果。
const (
	CloudPhoneCheckUnknown = 0
	CloudPhoneCheckOK      = 1
	CloudPhoneCheckFail    = 2
)

// CloudPhonePool GeeLark 云手机实体。表 `cloud_phone_pool`。
//
// 用于读取 WhatsApp OTP：dispatcher 调 GeeLark `/shell/execute` 跑
// `dumpsys notification --noredact | grep com.whatsapp` 拿到通知文本，
// 提取 6 位数字喂给 GoPay 流程。
//
// 一台云手机 = 一张 SIM = 一个 WhatsApp 号 = 一个 GoPay 钱包，
// 所以国家码 + 手机号字段直接放在云手机上，钱包侧只关联 cloud_phone_id 即可。
type CloudPhonePool struct {
	ID          string     `gorm:"primaryKey;column:id;size:64" json:"id"`
	Name        string     `gorm:"column:name;size:128;not null;default:''" json:"name"`
	GLTokenEnc  []byte     `gorm:"column:gl_token_enc;type:varbinary(512);not null" json:"-"`
	ADBAddr     *string    `gorm:"column:adb_addr;size:128" json:"adb_addr,omitempty"`
	PreferAPI   int8       `gorm:"column:prefer_api;not null;default:1" json:"prefer_api"`
	CountryCode string     `gorm:"column:country_code;size:8;not null;default:'62'" json:"country_code"`
	PhoneNumber string     `gorm:"column:phone_number;size:32;not null;default:''" json:"phone_number"`
	Status      string     `gorm:"column:status;size:16;not null;default:online" json:"status"`
	LastCheckAt *time.Time `gorm:"column:last_check_at" json:"last_check_at,omitempty"`
	LastCheckOK int8       `gorm:"column:last_check_ok;not null;default:0" json:"last_check_ok"`
	LastError   *string    `gorm:"column:last_error;size:255" json:"last_error,omitempty"`
	Remark      *string    `gorm:"column:remark;size:255" json:"remark,omitempty"`
	CreatedBy   *uint64    `gorm:"column:created_by" json:"created_by,omitempty"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt   *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (CloudPhonePool) TableName() string { return "cloud_phone_pool" }
