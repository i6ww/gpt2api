package model

import "time"

// PoolAdobe 状态。
const (
	AdobeStatusValid    = "valid"    // 可用
	AdobeStatusInvalid  = "invalid"  // token 失效
	AdobeStatusDisabled = "disabled" // 人工禁用
	AdobeStatusCooldown = "cooldown" // 短期冷却（失败后退避）
)

// PoolAdobe 来源。
const (
	AdobeSourceRegister = "register"
	AdobeSourceImport   = "import"
)

// PoolAdobe ADOBE Firefly 号池实体。表 `pool_adobe`。
type PoolAdobe struct {
	ID                 uint64     `gorm:"primaryKey;column:id" json:"id"`
	Email              string     `gorm:"column:email;size:255;not null" json:"email"`
	DisplayName        *string    `gorm:"column:display_name;size:128" json:"display_name,omitempty"`
	AdobeUserID        *string    `gorm:"column:adobe_user_id;size:64" json:"adobe_user_id,omitempty"`
	PasswordEnc        []byte     `gorm:"column:password_enc;type:blob" json:"-"`
	AccessTokenEnc     []byte     `gorm:"column:access_token_enc;type:blob" json:"-"`
	CookieEnc          []byte     `gorm:"column:cookie_enc;type:blob" json:"-"`
	DeviceTokenEnc     []byte     `gorm:"column:device_token_enc;type:blob" json:"-"`
	DeviceID           string     `gorm:"column:device_id;size:64" json:"device_id,omitempty"`
	Status             string     `gorm:"column:status;size:32;not null;default:valid" json:"status"`
	Source             string     `gorm:"column:source;size:32;not null;default:register" json:"source"`
	Credits            float64    `gorm:"column:credits;type:decimal(12,2);not null;default:0" json:"credits"`
	ExpiresAt          *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"`
	LastCheckedAt      *time.Time `gorm:"column:last_checked_at" json:"last_checked_at,omitempty"`
	LastCreditsCheckAt *time.Time `gorm:"column:last_credits_check_at" json:"last_credits_check_at,omitempty"`
	LastRefreshAt      *time.Time `gorm:"column:last_refresh_at" json:"last_refresh_at,omitempty"`
	LastUsedAt         *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	RefreshEnabled     int8       `gorm:"column:refresh_enabled;not null;default:1" json:"refresh_enabled"`
	FailureCount       int        `gorm:"column:failure_count;not null;default:0" json:"failure_count"`
	ErrorMessage       *string    `gorm:"column:error_message;size:500" json:"error_message,omitempty"`
	CooldownUntil      *time.Time `gorm:"column:cooldown_until" json:"cooldown_until,omitempty"`
	// EntitlementsJSON 存储 generation_service 通过 NotEntitledError 学到的档位
	// 权益状态：例如 {"no_4k": true, "no_4k_checked_at": 1731331200}。NULL =
	// 该号从未撞过 not_entitled，按"全档位都能跑"对待。详见 migration
	// 20260513030000_pool_adobe_entitlements.sql。
	EntitlementsJSON *string    `gorm:"column:entitlements_json;type:json" json:"entitlements_json,omitempty"`
	Notes            *string    `gorm:"column:notes;size:500" json:"notes,omitempty"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt        *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (PoolAdobe) TableName() string { return "pool_adobe" }

// ToAccount 把 pool_adobe 行装配成 gateway 调度用的 *Account DTO。
//
// 调用方（AccountRepo facade / AccountPool）从 pool_adobe 取出来后调一下，
// 让 GenerationService / Provider 拿到统一形态的 *Account。
//
// 字段映射：
//   - CredentialEnc = AccessTokenEnc（chat/provider 直接当 Bearer token 用）
//   - AccessTokenExpiresAt = ExpiresAt
//   - LastError = ErrorMessage
//   - ErrorCount = FailureCount
//   - SuccessCount 没有对应字段，留 0
//
// Status 语义映射：
//   - valid    -> AccountStatusEnabled
//   - invalid  -> AccountStatusBroken
//   - disabled -> AccountStatusDisabled
//   - cooldown -> AccountStatusBroken + CooldownUntil
func (p *PoolAdobe) ToAccount() *Account {
	a := &Account{
		ID:                   p.ID,
		Provider:             ProviderADOBE,
		Name:                 p.Email,
		AuthType:             AuthTypeOAuth,
		CredentialEnc:        p.AccessTokenEnc,
		AccessTokenEnc:       p.AccessTokenEnc,
		AccessTokenExpiresAt: p.ExpiresAt,
		LastRefreshAt:        p.LastRefreshAt,
		CooldownUntil:        p.CooldownUntil,
		LastUsedAt:           p.LastUsedAt,
		LastError:            p.ErrorMessage,
		ErrorCount:           p.FailureCount,
		Remark:               p.Notes,
		CreatedAt:            p.CreatedAt,
		UpdatedAt:            p.UpdatedAt,
		// 把 pool_adobe.entitlements_json 透传给 Account.OAuthMeta，
		// 让 generation_service 用 accountOAuthMeta() 统一读权益标记。
		OAuthMeta: p.EntitlementsJSON,
	}
	switch p.Status {
	case AdobeStatusValid:
		a.Status = AccountStatusEnabled
	case AdobeStatusInvalid:
		a.Status = AccountStatusBroken
	case AdobeStatusDisabled:
		a.Status = AccountStatusDisabled
	case AdobeStatusCooldown:
		a.Status = AccountStatusBroken
	default:
		a.Status = AccountStatusEnabled
	}
	return a
}
