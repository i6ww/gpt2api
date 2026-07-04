package model

import (
	"encoding/json"
	"time"
)

// PoolGrok 订阅试用状态机。
const (
	GrokTrialPending    = "pending"    // 还没开通
	GrokTrialActivating = "activating" // 正在开通
	GrokTrialActive     = "active"     // 已开通
	GrokTrialFailed     = "failed"     // 开通失败
	GrokTrialExpired    = "expired"    // 已过期
)

// PoolGrok 账号订阅类型。空串 = 未识别 / 未刷新。
const (
	GrokAccountTypeFree           = "free"
	GrokAccountTypeSuperGrokLite  = "super_grok_lite"
	GrokAccountTypeSuperGrok      = "super_grok"
	GrokAccountTypeSuperGrokHeavy = "super_grok_heavy"
	GrokAccountTypeTeam           = "team"
	GrokAccountTypeUnknown        = "unknown"
)

// 订阅状态常量 — 来自 /rest/subscriptions.status 归一后的值。
const (
	GrokSubStatusActive   = "active"
	GrokSubStatusTrialing = "trialing"
	GrokSubStatusInactive = "inactive"
	GrokSubStatusPastDue  = "past_due"
	GrokSubStatusCanceled = "canceled"
)

// PoolGrok 状态（gateway 调度态，与 pool_gpt 对齐）。
const (
	GrokStatusValid    = "valid"
	GrokStatusInvalid  = "invalid"
	GrokStatusDisabled = "disabled"
	GrokStatusCooldown = "cooldown"
)

// PoolGrok GROK 号池实体。表 `pool_grok`。
type PoolGrok struct {
	ID             uint64     `gorm:"primaryKey;column:id" json:"id"`
	Email          string     `gorm:"column:email;size:255;not null" json:"email"`
	PasswordEnc    []byte     `gorm:"column:password_enc;type:blob" json:"-"`
	GivenName      *string    `gorm:"column:given_name;size:64" json:"given_name,omitempty"`
	FamilyName     *string    `gorm:"column:family_name;size:64" json:"family_name,omitempty"`
	SSOEnc         []byte     `gorm:"column:sso_enc;type:blob" json:"-"`
	SSORWEnc       []byte     `gorm:"column:sso_rw_enc;type:blob" json:"-"`
	UserAgent      *string    `gorm:"column:user_agent;size:255" json:"user_agent,omitempty"`
	TrialStatus    string     `gorm:"column:trial_status;size:32;not null;default:pending" json:"trial_status"`
	TrialStartedAt *time.Time `gorm:"column:trial_started_at" json:"trial_started_at,omitempty"`
	TrialExpiresAt *time.Time `gorm:"column:trial_expires_at" json:"trial_expires_at,omitempty"`
	TrialError     *string    `gorm:"column:trial_error;size:500" json:"trial_error,omitempty"`
	AccountType    string     `gorm:"column:account_type;size:32;not null;default:''" json:"account_type"`

	// Gateway runtime 字段。
	Status string `gorm:"column:status;size:32;not null;default:valid" json:"status"`
	// ExpiresAt subscription 的"下次结算日"。语义跟着 CancelAtPeriodEnd：
	//   - CancelAtPeriodEnd=false → 自动续费扣费日（过了会自动延一周期）
	//   - CancelAtPeriodEnd=true  → 真到期失效日
	// UI 必须读两个字段一起判断，不要单看 ExpiresAt。
	ExpiresAt *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"`
	// CancelAtPeriodEnd 用户是否已退订。来自 /rest/subscriptions.cancelAtPeriodEnd。
	CancelAtPeriodEnd bool `gorm:"column:cancel_at_period_end;not null;default:0" json:"cancel_at_period_end"`
	// BillingInterval "monthly" / "yearly" / ""。来自 /rest/subscriptions.billingInterval。
	BillingInterval string `gorm:"column:billing_interval;size:16;not null;default:''" json:"billing_interval"`
	// SubscriptionStatus 订阅生命周期状态："active" / "trialing" / "past_due" /
	// "canceled" / "inactive" / ""。来自 /rest/subscriptions.status 归一。
	// 区分"三天试用中"和"正式订阅"用 trialing；"欠费"用 past_due。
	SubscriptionStatus string `gorm:"column:subscription_status;size:32;not null;default:''" json:"subscription_status"`
	// ProductID stripe.productId — 比 account_type 字段更精细，能区分 Lite。
	// prod_RilhxLgoA7h5ri = SuperGrok / Sdnv8D1IKHvyAs = Heavy / UB9qtKxWZvyvtM = Lite
	ProductID     string     `gorm:"column:product_id;size:64;not null;default:''" json:"product_id"`
	LastRefreshAt *time.Time `gorm:"column:last_refresh_at" json:"last_refresh_at,omitempty"`
	LastUsedAt        *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	ErrorMessage      *string    `gorm:"column:error_message;size:500" json:"error_message,omitempty"`
	ProxyID           *uint64    `gorm:"column:proxy_id" json:"proxy_id,omitempty"`
	BaseURL           *string    `gorm:"column:base_url;size:255" json:"base_url,omitempty"`
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
	Remark            *string    `gorm:"column:remark;size:255" json:"remark,omitempty"`

	Credits       float64    `gorm:"column:credits;type:decimal(12,2);not null;default:0" json:"credits"`
	FailureCount  int        `gorm:"column:failure_count;not null;default:0" json:"failure_count"`
	LastCheckedAt *time.Time `gorm:"column:last_checked_at" json:"last_checked_at,omitempty"`
	QuotaTotal    float64    `gorm:"column:quota_total;type:decimal(12,2);not null;default:0" json:"quota_total"`
	PaymentURL    *string    `gorm:"column:payment_url;size:500" json:"payment_url,omitempty"`
	Notes         *string    `gorm:"column:notes;size:500" json:"notes,omitempty"`
	RegisteredAt  time.Time  `gorm:"column:registered_at;autoCreateTime" json:"registered_at"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt     *time.Time `gorm:"column:deleted_at;index" json:"-"`
}

// TableName 表名。
func (PoolGrok) TableName() string { return "pool_grok" }

// ToAccount 把 pool_grok 行装配成 gateway 调度用的 *Account DTO。
//
// Grok 用 SSO cookie 作为凭证，所以 CredentialEnc = SSOEnc，AuthType = cookie。
// account_type 写入 oauth_meta.plan_type，给 accountSupportsGrokPlan 用。
func (p *PoolGrok) ToAccount() *Account {
	a := &Account{
		ID:                   p.ID,
		Provider:             ProviderGROK,
		Name:                 p.Email,
		AuthType:             AuthTypeCookie,
		CredentialEnc:        p.SSOEnc,
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
	case GrokStatusValid:
		a.Status = AccountStatusEnabled
	case GrokStatusInvalid:
		a.Status = AccountStatusBroken
	case GrokStatusDisabled:
		a.Status = AccountStatusDisabled
	case GrokStatusCooldown:
		a.Status = AccountStatusBroken
	default:
		a.Status = AccountStatusEnabled
	}
	if p.AccountType != "" {
		meta := map[string]any{"plan_type": p.AccountType}
		if raw, err := json.Marshal(meta); err == nil {
			s := string(raw)
			a.OAuthMeta = &s
		}
	}
	return a
}
