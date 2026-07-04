package dto

// GrokPoolListReq 列表过滤参数。
//
// AccountType 是来自 /rest/subscriptions 的订阅 tier，目前后端会写入的可能值是
// "" / "free" / "super_grok" / "super_grok_heavy" / "team" / "unknown"，
// 前端筛选时按这些字面值传入即可（空串表示不限）。
type GrokPoolListReq struct {
	TrialStatus string `form:"trial_status" binding:"omitempty,oneof=pending activating active failed expired"`
	AccountType string `form:"account_type" binding:"omitempty,oneof=free super_grok_lite super_grok super_grok_heavy team unknown"`
	// SubscriptionStatus 按订阅生命周期筛选。空 = 不限。
	SubscriptionStatus string `form:"subscription_status" binding:"omitempty,oneof=active trialing past_due canceled inactive"`
	Keyword            string `form:"keyword" binding:"omitempty,max=64"`
	Page               int    `form:"page" binding:"omitempty,min=1"`
	PageSize           int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// GrokPoolCreateReq ????????
type GrokPoolCreateReq struct {
	Email          string  `json:"email" binding:"required,email,max=255"`
	Password       string  `json:"password" binding:"omitempty,max=255"`
	GivenName      string  `json:"given_name" binding:"omitempty,max=64"`
	FamilyName     string  `json:"family_name" binding:"omitempty,max=64"`
	SSO            string  `json:"sso" binding:"omitempty,max=4000"`
	SSORW          string  `json:"sso_rw" binding:"omitempty,max=4000"`
	UserAgent      string  `json:"user_agent" binding:"omitempty,max=255"`
	TrialStatus    string  `json:"trial_status" binding:"omitempty,oneof=pending activating active failed expired"`
	TrialExpiresAt int64   `json:"trial_expires_at" binding:"omitempty,min=0"`
	AccountType    string  `json:"account_type" binding:"omitempty,max=32"`
	Credits        float64 `json:"credits" binding:"omitempty"`
	PaymentURL     string  `json:"payment_url" binding:"omitempty,max=500"`
	Notes          string  `json:"notes" binding:"omitempty,max=500"`
}

// GrokPoolUpdateReq ??
type GrokPoolUpdateReq struct {
	Password       *string  `json:"password" binding:"omitempty,max=255"`
	GivenName      *string  `json:"given_name" binding:"omitempty,max=64"`
	FamilyName     *string  `json:"family_name" binding:"omitempty,max=64"`
	SSO            *string  `json:"sso" binding:"omitempty,max=4000"`
	SSORW          *string  `json:"sso_rw" binding:"omitempty,max=4000"`
	UserAgent      *string  `json:"user_agent" binding:"omitempty,max=255"`
	TrialStatus    *string  `json:"trial_status" binding:"omitempty,oneof=pending activating active failed expired"`
	TrialExpiresAt *int64   `json:"trial_expires_at" binding:"omitempty,min=0"`
	AccountType    *string  `json:"account_type" binding:"omitempty,max=32"`
	Credits        *float64 `json:"credits" binding:"omitempty"`
	PaymentURL     *string  `json:"payment_url" binding:"omitempty,max=500"`
	Notes          *string  `json:"notes" binding:"omitempty,max=500"`
}

// GrokPoolImportReq ??????????? JSON ???
type GrokPoolImportReq struct {
	Text string `json:"text" binding:"required"`
}

// GrokPoolImportResult ??????
type GrokPoolImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

// GrokPoolBatchIDsReq ???? ID ??
type GrokPoolBatchIDsReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// GrokPoolBulkOpResult ??????
type GrokPoolBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// GrokPoolResp ????
type GrokPoolResp struct {
	ID             uint64  `json:"id"`
	Email          string  `json:"email"`
	HasPassword    bool    `json:"has_password"`
	GivenName      string  `json:"given_name,omitempty"`
	FamilyName     string  `json:"family_name,omitempty"`
	HasSSO         bool    `json:"has_sso"`
	HasSSORW       bool    `json:"has_sso_rw"`
	UserAgent      string  `json:"user_agent,omitempty"`
	TrialStatus    string  `json:"trial_status"`
	TrialStartedAt int64   `json:"trial_started_at,omitempty"`
	// TrialExpiresAt 实际承载的是 rate-limits 的"额度刷新窗口"，不是订阅到期。
	// 命名延用历史字段，前端 UI 已改为"额度刷新于"。
	TrialExpiresAt int64 `json:"trial_expires_at,omitempty"`
	// ExpiresAt 真实订阅到期时间（来自 /rest/subscriptions），无信号时为 0。
	//
	// 语义跟着 CancelAtPeriodEnd 走：
	//   - CancelAtPeriodEnd=false → 这是下次"自动续费"扣费日，过了会自动延一周期
	//   - CancelAtPeriodEnd=true  → 这是真正的"到期失效"日，过了订阅就没了
	// 前端按 CancelAtPeriodEnd 区分文案 / 颜色，不要笼统叫"订阅到期"。
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// CancelAtPeriodEnd 用户是否已主动取消、只是仍在当前周期内能用。
	// 来自 /rest/subscriptions 的 cancelAtPeriodEnd 字段。
	CancelAtPeriodEnd bool `json:"cancel_at_period_end,omitempty"`
	// BillingInterval 订阅周期，"monthly" / "yearly" / ""。来自 /rest/subscriptions。
	// 前端可用于显示"月订/年订"标签，或推算下下次扣费日。
	BillingInterval string `json:"billing_interval,omitempty"`
	// SubscriptionStatus 订阅生命周期：active / trialing / past_due / canceled / inactive。
	// 前端用 trialing 显示"试用中"徽章；past_due 显示"欠费"。
	SubscriptionStatus string `json:"subscription_status,omitempty"`
	// ProductID stripe 产品 ID，精确识别 Lite/SuperGrok/Heavy（比 account_type 更细）。
	ProductID  string `json:"product_id,omitempty"`
	TrialError string `json:"trial_error,omitempty"`
	AccountType string `json:"account_type"`
	Credits        float64 `json:"credits"`
	QuotaTotal     float64 `json:"quota_total"`
	FailureCount   int     `json:"failure_count,omitempty"`
	LastCheckedAt  int64   `json:"last_checked_at,omitempty"`
	PaymentURL     string  `json:"payment_url,omitempty"`
	Notes          string  `json:"notes,omitempty"`
	RegisteredAt   int64   `json:"registered_at"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
}

// GrokPoolPurgeReq ???? GROK ?????POST /pools/grok/purge??
//
// ??? AND???? + All=false ??????
type GrokPoolPurgeReq struct {
	All         bool   `json:"all"`          // ????????????
	Status      string `json:"status"`       // ???? trial_status?? failed / expired?
	Abnormal    bool   `json:"abnormal"`     // failed + expired ????
	ZeroCredits bool   `json:"zero_credits"` // credits <= 0
}

// GrokPoolBatchRefreshReq ???? GROK ???
type GrokPoolBatchRefreshReq struct {
	// "all" / "abnormal" / "zero_credits" / "expiring" / "unknown_type"
	Scope string `json:"scope"`
}

// GrokPoolStatsResp ??????
type GrokPoolStatsResp struct {
	Total      int64 `json:"total"`
	Pending    int64 `json:"pending"`
	Activating int64 `json:"activating"`
	Active     int64 `json:"active"`
	Failed     int64 `json:"failed"`
	Expired    int64 `json:"expired"`
}
