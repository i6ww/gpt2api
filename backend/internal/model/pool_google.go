package model

import "time"

// PoolGoogle 状态（与 pool_adobe 对齐）。
const (
	GoogleStatusValid    = "valid"    // 可用
	GoogleStatusInvalid  = "invalid"  // token 失效（终态，不自动复活）
	GoogleStatusDisabled = "disabled" // 人工禁用
	GoogleStatusCooldown = "cooldown" // 短期冷却
)

// PoolGoogle 来源。
const (
	GoogleSourceImport   = "import"
	GoogleSourceRegister = "register"
)

// PoolGoogle FlowMusic（歌曲/音乐）Google 账号池实体。表 `pool_google`。
//
// CredentialEnc 存的是加密后的「凭证 bundle」JSON：
//
//	{"refresh_token":..,"access_token":..,"provider_token":..,
//	 "provider_refresh_token":..,"flow_bearer":..,"cookies":..}
//
// 其中 access_token 是 Supabase JWT —— FlowMusic 业务接口真正校验的 Bearer。
// 续期调度器（flowmusicrefresh）解密整包 → RefreshSupabase → 回写整包；
// provider 调用时只读 access_token + cookies（忽略其余字段）。
type PoolGoogle struct {
	ID                uint64     `gorm:"primaryKey;column:id" json:"id"`
	Email             string     `gorm:"column:email;size:255;not null" json:"email"`
	DisplayName       *string    `gorm:"column:display_name;size:128" json:"display_name,omitempty"`
	CredentialEnc     []byte     `gorm:"column:credential_enc;type:blob;not null" json:"-"`
	ProtocolMode      string     `gorm:"column:protocol_mode;size:32;not null;default:refresh_token" json:"protocol_mode"`
	Status            string     `gorm:"column:status;size:32;not null;default:valid" json:"status"`
	Source            string     `gorm:"column:source;size:32;not null;default:import" json:"source"`
	Credits           float64    `gorm:"column:credits;type:decimal(12,2);not null;default:0" json:"credits"`
	TokensRemaining   float64    `gorm:"column:tokens_remaining;type:decimal(14,2);not null;default:0" json:"tokens_remaining"`
	SubscriptionTier  *string    `gorm:"column:subscription_tier;size:64" json:"subscription_tier,omitempty"`
	ProxyID           *uint64    `gorm:"column:proxy_id" json:"proxy_id,omitempty"`
	ExpiresAt         *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"`
	LastCheckedAt     *time.Time `gorm:"column:last_checked_at" json:"last_checked_at,omitempty"`
	LastRefreshAt     *time.Time `gorm:"column:last_refresh_at" json:"last_refresh_at,omitempty"`
	LastRefreshResult *string    `gorm:"column:last_refresh_result;size:255" json:"last_refresh_result,omitempty"`
	LastUsedAt        *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	RefreshEnabled    int8       `gorm:"column:refresh_enabled;not null;default:1" json:"refresh_enabled"`
	FailureCount      int        `gorm:"column:failure_count;not null;default:0" json:"failure_count"`
	ErrorMessage      *string    `gorm:"column:error_message;size:500" json:"error_message,omitempty"`
	CooldownUntil     *time.Time `gorm:"column:cooldown_until" json:"cooldown_until,omitempty"`
	Notes             *string    `gorm:"column:notes;size:500" json:"notes,omitempty"`
	CreatedAt         time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt         *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (PoolGoogle) TableName() string { return "pool_google" }

// ToAccount 把 pool_google 行装配成 gateway 调度用的 *Account DTO。
//
// 字段映射：
//   - CredentialEnc = CredentialEnc（凭证 bundle，provider 解出 access_token+cookies）
//   - AccessTokenExpiresAt = ExpiresAt
//   - LastError = ErrorMessage / ErrorCount = FailureCount
//
// Status 语义映射：
//   - valid    -> AccountStatusEnabled
//   - invalid  -> AccountStatusBroken
//   - disabled -> AccountStatusDisabled
//   - cooldown -> AccountStatusBroken + CooldownUntil
func (p *PoolGoogle) ToAccount() *Account {
	a := &Account{
		ID:                   p.ID,
		Provider:             ProviderFLOWMUSIC,
		Name:                 p.Email,
		AuthType:             AuthTypeOAuth,
		CredentialEnc:        p.CredentialEnc,
		AccessTokenExpiresAt: p.ExpiresAt,
		LastRefreshAt:        p.LastRefreshAt,
		CooldownUntil:        p.CooldownUntil,
		LastUsedAt:           p.LastUsedAt,
		LastError:            p.ErrorMessage,
		ErrorCount:           p.FailureCount,
		Remark:               p.Notes,
		ProxyID:              p.ProxyID,
		CreatedAt:            p.CreatedAt,
		UpdatedAt:            p.UpdatedAt,
	}
	switch p.Status {
	case GoogleStatusValid:
		a.Status = AccountStatusEnabled
	case GoogleStatusInvalid:
		a.Status = AccountStatusBroken
	case GoogleStatusDisabled:
		a.Status = AccountStatusDisabled
	case GoogleStatusCooldown:
		a.Status = AccountStatusBroken
	default:
		a.Status = AccountStatusEnabled
	}
	return a
}
