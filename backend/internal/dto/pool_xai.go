package dto

// XAIPoolListReq 列表过滤参数。
type XAIPoolListReq struct {
	Status      string `form:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	AccountType string `form:"account_type" binding:"omitempty,max=32"`
	Keyword     string `form:"keyword" binding:"omitempty,max=64"`
	Page        int    `form:"page" binding:"omitempty,min=1"`
	PageSize    int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// XAIPoolCreateReq 新增/导入单条字段。
//
// access_token / refresh_token 来自 cmd/xailogin 的 OAuth 登录产物。
type XAIPoolCreateReq struct {
	Email         string `json:"email" binding:"omitempty,max=255"`
	Subject       string `json:"subject" binding:"omitempty,max=128"`
	AccessToken   string `json:"access_token" binding:"omitempty"`
	RefreshToken  string `json:"refresh_token" binding:"omitempty"`
	IDToken       string `json:"id_token" binding:"omitempty"`
	TokenEndpoint string `json:"token_endpoint" binding:"omitempty,max=255"`
	BaseURL       string `json:"base_url" binding:"omitempty,max=255"`
	AccountType   string `json:"account_type" binding:"omitempty,max=32"`
	ExpiresAt     int64  `json:"expires_at" binding:"omitempty,min=0"`
	Notes         string `json:"notes" binding:"omitempty,max=500"`
}

// XAIPoolUpdateReq 部分更新。
type XAIPoolUpdateReq struct {
	AccessToken    *string `json:"access_token" binding:"omitempty"`
	RefreshToken   *string `json:"refresh_token" binding:"omitempty"`
	IDToken        *string `json:"id_token" binding:"omitempty"`
	TokenEndpoint  *string `json:"token_endpoint" binding:"omitempty,max=255"`
	BaseURL        *string `json:"base_url" binding:"omitempty,max=255"`
	AccountType    *string `json:"account_type" binding:"omitempty,max=32"`
	Status         *string `json:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	RefreshEnabled *bool   `json:"refresh_enabled" binding:"omitempty"`
	ExpiresAt      *int64  `json:"expires_at" binding:"omitempty,min=0"`
	Notes          *string `json:"notes" binding:"omitempty,max=500"`
	// BalanceNote 手填的余额(U)备注（xAI 不开放余额 API，仅人工记录；存 remark 列）。
	BalanceNote *string `json:"balance_note" binding:"omitempty,max=64"`
}

// XAIPoolImportReq 文本批量导入。
type XAIPoolImportReq struct {
	Text string `json:"text" binding:"required"`
}

// XAIPoolImportResult 导入结果。
type XAIPoolImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

// XAIPoolBatchIDsReq 批量 ID 操作。
type XAIPoolBatchIDsReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// XAIPoolBulkOpResult 批量操作结果。
type XAIPoolBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// XAIPoolPurgeReq 批量删除。
type XAIPoolPurgeReq struct {
	All      bool   `json:"all"`
	Status   string `json:"status"`
	Abnormal bool   `json:"abnormal"`
}

// XAIPoolBatchRefreshReq 批量刷新。
type XAIPoolBatchRefreshReq struct {
	// "all" / "expiring" / "abnormal"
	Scope string `json:"scope"`
}

// XAIPoolResp 列表/详情返回（不暴露密文）。
type XAIPoolResp struct {
	ID                uint64 `json:"id"`
	Email             string `json:"email"`
	Subject           string `json:"subject,omitempty"`
	HasAccessToken    bool   `json:"has_access_token"`
	HasRefreshToken   bool   `json:"has_refresh_token"`
	TokenEndpoint     string `json:"token_endpoint,omitempty"`
	BaseURL           string `json:"base_url,omitempty"`
	AccountType       string `json:"account_type"`
	Status            string `json:"status"`
	Source            string `json:"source"`
	RefreshEnabled    bool   `json:"refresh_enabled"`
	ExpiresAt         int64  `json:"expires_at,omitempty"`
	LastRefreshAt     int64  `json:"last_refresh_at,omitempty"`
	LastRefreshResult string `json:"last_refresh_result,omitempty"`
	LastUsedAt        int64  `json:"last_used_at,omitempty"`
	FailureCount      int    `json:"failure_count,omitempty"`
	SuccessCount      uint64 `json:"success_count,omitempty"`
	ErrorMessage      string `json:"error_message,omitempty"`
	Notes             string `json:"notes,omitempty"`
	BalanceNote       string `json:"balance_note,omitempty"`
	// Billing 自动查询到的额度快照（来自 cli-chat-proxy.grok.com/v1/billing）。
	Billing           *XAIBillingResp `json:"billing,omitempty"`
	CreatedAt         int64           `json:"created_at"`
	UpdatedAt         int64           `json:"updated_at"`
}

// XAIBillingResp 账号额度（美元）。
type XAIBillingResp struct {
	LimitUSD     float64 `json:"limit_usd"`     // 月度包含额度
	UsedUSD      float64 `json:"used_usd"`      // 本周期已用
	RemainingUSD float64 `json:"remaining_usd"` // 剩余 = limit-used
	CapUSD       float64 `json:"cap_usd"`       // 按量付费封顶
	UsedPct      int     `json:"used_pct"`      // used/limit 百分比
	PeriodEnd    string  `json:"period_end"`    // 计费周期结束（重置时间）
	UpdatedAt    int64   `json:"updated_at"`    // 查询时间（毫秒）
}

// XAIPoolStatsResp 状态分布。
type XAIPoolStatsResp struct {
	Total    int64 `json:"total"`
	Valid    int64 `json:"valid"`
	Invalid  int64 `json:"invalid"`
	Disabled int64 `json:"disabled"`
	Cooldown int64 `json:"cooldown"`
}
