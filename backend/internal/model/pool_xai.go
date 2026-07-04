package model

import "time"

// PoolXAI 状态（gateway 调度态，与 pool_gpt / pool_google 对齐）。
const (
	XAIStatusValid    = "valid"    // 可用
	XAIStatusInvalid  = "invalid"  // token 失效（终态，不自动复活）
	XAIStatusDisabled = "disabled" // 人工禁用
	XAIStatusCooldown = "cooldown" // 短期冷却
)

// PoolXAI 来源。
const (
	XAISourceImport   = "import"
	XAISourceRegister = "register"
)

// PoolXAI 账号订阅类型。空串 = 未识别。
const (
	XAIAccountTypeUnknown = "unknown"
)

// DefaultXAIBaseURL xAI 官方 Responses / Videos API base。
const DefaultXAIBaseURL = "https://api.x.ai/v1"

// PoolXAI 官方 xAI API（OAuth）账号池实体。表 `pool_xai`。
//
// 与 pool_grok（grok.com Web SSO cookie）不同，本表走 xAI 官方 OAuth：
//
//	CredentialEnc      = access_token（业务接口真正校验的 Bearer）
//	RefreshTokenEnc    = refresh_token（offline_access，续期用）
//	IDTokenEnc         = id_token（OIDC，仅解析身份）
//	TokenEndpoint      = OAuth token 端点（grant_type=refresh_token POST 它）
//	BaseURL            = API base，默认 https://api.x.ai/v1
//
// access_token 寿命短，由 xairefresh 调度器在过期前 silent refresh，原地回写
// CredentialEnc + ExpiresAt。provider 调用时只读解密后的 access_token 当 Bearer。
type PoolXAI struct {
	ID              uint64  `gorm:"primaryKey;column:id" json:"id"`
	Email           string  `gorm:"column:email;size:255;not null" json:"email"`
	Subject         *string `gorm:"column:subject;size:128" json:"subject,omitempty"`
	CredentialEnc   []byte  `gorm:"column:credential_enc;type:blob;not null" json:"-"`
	RefreshTokenEnc []byte  `gorm:"column:refresh_token_enc;type:blob" json:"-"`
	IDTokenEnc      []byte  `gorm:"column:id_token_enc;type:blob" json:"-"`
	TokenEndpoint   *string `gorm:"column:token_endpoint;size:255" json:"token_endpoint,omitempty"`
	BaseURL         *string `gorm:"column:base_url;size:255" json:"base_url,omitempty"`
	AccountType     string  `gorm:"column:account_type;size:32;not null;default:''" json:"account_type"`

	Status            string     `gorm:"column:status;size:32;not null;default:valid" json:"status"`
	Source            string     `gorm:"column:source;size:32;not null;default:import" json:"source"`
	RefreshEnabled    int8       `gorm:"column:refresh_enabled;not null;default:1" json:"refresh_enabled"`
	ExpiresAt         *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"`
	LastRefreshAt     *time.Time `gorm:"column:last_refresh_at" json:"last_refresh_at,omitempty"`
	LastRefreshResult *string    `gorm:"column:last_refresh_result;size:255" json:"last_refresh_result,omitempty"`
	LastUsedAt        *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	LastCheckedAt     *time.Time `gorm:"column:last_checked_at" json:"last_checked_at,omitempty"`
	ProxyID           *uint64    `gorm:"column:proxy_id" json:"proxy_id,omitempty"`
	ModelWhitelist    *string    `gorm:"column:model_whitelist;type:json" json:"model_whitelist,omitempty"`
	Weight            int        `gorm:"column:weight;not null;default:10" json:"weight"`
	RPMLimit          int        `gorm:"column:rpm_limit;not null;default:0" json:"rpm_limit"`
	TPMLimit          int        `gorm:"column:tpm_limit;not null;default:0" json:"tpm_limit"`
	DailyQuota        int        `gorm:"column:daily_quota;not null;default:0" json:"daily_quota"`
	MonthlyQuota      int        `gorm:"column:monthly_quota;not null;default:0" json:"monthly_quota"`
	CooldownUntil     *time.Time `gorm:"column:cooldown_until" json:"cooldown_until,omitempty"`
	LastTestAt        *time.Time `gorm:"column:last_test_at" json:"last_test_at,omitempty"`
	LastTestStatus    int8       `gorm:"column:last_test_status;not null;default:0" json:"last_test_status"`
	LastTestLatencyMs int        `gorm:"column:last_test_latency_ms;not null;default:0" json:"last_test_latency_ms"`
	LastTestError     *string    `gorm:"column:last_test_error;size:255" json:"last_test_error,omitempty"`
	SuccessCount      uint64     `gorm:"column:success_count;not null;default:0" json:"success_count"`
	FailureCount      int        `gorm:"column:failure_count;not null;default:0" json:"failure_count"`
	ErrorMessage      *string    `gorm:"column:error_message;size:500" json:"error_message,omitempty"`
	Remark            *string    `gorm:"column:remark;size:255" json:"remark,omitempty"`
	Notes             *string    `gorm:"column:notes;size:500" json:"notes,omitempty"`
	CreatedAt         time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt         *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (PoolXAI) TableName() string { return "pool_xai" }

// ToAccount 把 pool_xai 行装配成 gateway 调度用的 *Account DTO。
//
// xAI 官方 API 用 OAuth access_token 当 Bearer，所以 CredentialEnc 直接装载
// access_token 密文，AuthType = oauth。注意：generation_service.decryptCredential
// 仅对 (AuthTypeOAuth && Provider==GPT) 走 codex 换 token 流程；xAI 走通用解密分支，
// 拿到的就是 access_token 明文。
func (p *PoolXAI) ToAccount() *Account {
	a := &Account{
		ID:                   p.ID,
		Provider:             ProviderXAI,
		Name:                 p.Email,
		AuthType:             AuthTypeOAuth,
		CredentialEnc:        p.CredentialEnc,
		RefreshTokenEnc:      p.RefreshTokenEnc,
		AccessTokenExpiresAt: p.ExpiresAt,
		LastRefreshAt:        p.LastRefreshAt,
		BaseURL:              p.BaseURL,
		ProxyID:              p.ProxyID,
		ModelWhitelist:       p.ModelWhitelist,
		Weight:               p.Weight,
		RPMLimit:             p.RPMLimit,
		TPMLimit:             p.TPMLimit,
		DailyQuota:           p.DailyQuota,
		MonthlyQuota:         p.MonthlyQuota,
		CooldownUntil:        p.CooldownUntil,
		LastUsedAt:           p.LastUsedAt,
		LastError:            p.ErrorMessage,
		LastTestAt:           p.LastTestAt,
		LastTestStatus:       p.LastTestStatus,
		LastTestLatencyMs:    p.LastTestLatencyMs,
		LastTestError:        p.LastTestError,
		ErrorCount:           p.FailureCount,
		SuccessCount:         p.SuccessCount,
		Remark:               p.Remark,
		CreatedAt:            p.CreatedAt,
		UpdatedAt:            p.UpdatedAt,
	}
	switch p.Status {
	case XAIStatusValid:
		a.Status = AccountStatusEnabled
	case XAIStatusInvalid:
		a.Status = AccountStatusBanned
	case XAIStatusDisabled:
		a.Status = AccountStatusDisabled
	case XAIStatusCooldown:
		a.Status = AccountStatusBroken
	default:
		a.Status = AccountStatusEnabled
	}
	return a
}
