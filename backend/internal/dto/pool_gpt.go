package dto

// GptPoolListReq 列表过滤参数。
//
// PlanType 用于按 OpenAI 订阅档位筛选；除官方档位外另接受聚合值
// "__unsubscribed" 表示「Free 或 未探测」（用于"哪些号还能升 Plus"快查）。
type GptPoolListReq struct {
	Status   string `form:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	PlanType string `form:"plan_type" binding:"omitempty,oneof=free plus pro team enterprise unknown __unsubscribed"`
	Keyword  string `form:"keyword" binding:"omitempty,max=64"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// GptPoolCreateReq ????????
type GptPoolCreateReq struct {
	Email         string `json:"email" binding:"required,email,max=255"`
	Password      string `json:"password" binding:"omitempty,max=255"`
	AccessToken   string `json:"access_token" binding:"omitempty,max=12000"`
	RefreshToken  string `json:"refresh_token" binding:"omitempty,max=8000"`
	IDToken       string `json:"id_token" binding:"omitempty,max=12000"`
	APIKey        string `json:"api_key" binding:"omitempty,max=4000"`
	OAuthIssuer   string `json:"oauth_issuer" binding:"omitempty,max=255"`
	OAuthClientID string `json:"oauth_client_id" binding:"omitempty,max=128"`
	Status        string `json:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	ExpiresAt     int64  `json:"expires_at" binding:"omitempty,min=0"`
	Notes         string `json:"notes" binding:"omitempty,max=500"`
}

// GptPoolUpdateReq ??
type GptPoolUpdateReq struct {
	Password      *string `json:"password" binding:"omitempty,max=255"`
	AccessToken   *string `json:"access_token" binding:"omitempty,max=12000"`
	RefreshToken  *string `json:"refresh_token" binding:"omitempty,max=8000"`
	IDToken       *string `json:"id_token" binding:"omitempty,max=12000"`
	APIKey        *string `json:"api_key" binding:"omitempty,max=4000"`
	OAuthIssuer   *string `json:"oauth_issuer" binding:"omitempty,max=255"`
	OAuthClientID *string `json:"oauth_client_id" binding:"omitempty,max=128"`
	Status        *string `json:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	ExpiresAt     *int64  `json:"expires_at" binding:"omitempty,min=0"`
	Notes         *string `json:"notes" binding:"omitempty,max=500"`
}

// GptPoolImportReq ??????
//
// ???????
//   1) ?? email:password ??
//   2) ???? JSON ?????????
type GptPoolImportReq struct {
	Text   string `json:"text" binding:"required"`
	Format string `json:"format" binding:"omitempty,oneof=auto colon json"`
}

// GptPoolImportResult ??????
type GptPoolImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

// GptPoolBatchIDsReq ???? ID ??
type GptPoolBatchIDsReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// GptPoolBulkOpResult ??????
type GptPoolBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// GptPoolResp ????
type GptPoolResp struct {
	ID              uint64 `json:"id"`
	Email           string `json:"email"`
	HasPassword     bool   `json:"has_password"`
	HasAccessToken  bool   `json:"has_access_token"`
	HasRefreshToken bool   `json:"has_refresh_token"`
	HasIDToken      bool   `json:"has_id_token"`
	HasAPIKey       bool   `json:"has_api_key"`
	OAuthIssuer     string `json:"oauth_issuer,omitempty"`
	OAuthClientID   string `json:"oauth_client_id,omitempty"`

	// ???? + ????? wham/usage + JWT ???
	PlanType                   string   `json:"plan_type,omitempty"`
	ChatGPTAccountID           string   `json:"chatgpt_account_id,omitempty"`
	QuotaPrimaryUsedPercent    *float64 `json:"quota_primary_used_percent,omitempty"`
	QuotaPrimaryResetAt        int64    `json:"quota_primary_reset_at,omitempty"`
	QuotaSecondaryUsedPercent  *float64 `json:"quota_secondary_used_percent,omitempty"`
	QuotaSecondaryResetAt      int64    `json:"quota_secondary_reset_at,omitempty"`
	QuotaCodeReviewUsedPercent *float64 `json:"quota_code_review_used_percent,omitempty"`
	LastQuotaCheckAt           int64    `json:"last_quota_check_at,omitempty"`

	Status        string `json:"status"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	LastCheckedAt int64  `json:"last_checked_at,omitempty"`
	LastRefreshAt int64  `json:"last_refresh_at,omitempty"`
	LastUsedAt    int64  `json:"last_used_at,omitempty"`
	FailureCount  int    `json:"failure_count"`
	ErrorMessage  string `json:"error_message,omitempty"`
	Notes         string `json:"notes,omitempty"`
	RegisteredAt  int64  `json:"registered_at"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// GptPoolRefreshReq ?????? body
type GptPoolRefreshReq struct {
	OnlyQuota bool `json:"only_quota"` // true = ?? quota??? RT/AT
}

// GptPoolRefreshResp ?????????????? detail?
type GptPoolRefreshResp struct {
	Item    *GptPoolDetailResp `json:"item"`
	Message string             `json:"message,omitempty"`
}

// GptPoolBatchRefreshReq ???????
//
//   - Scope?all / abnormal / expiring / quota_stale
//   - OnlyQuota?true = ?????????? RT??? wham/usage?
//   - MaxConcurrent????<=0 ?? 3
type GptPoolBatchRefreshReq struct {
	Scope         string `json:"scope" binding:"omitempty,oneof=all abnormal expiring quota_stale"`
	OnlyQuota     bool   `json:"only_quota"`
	MaxConcurrent int    `json:"max_concurrent" binding:"omitempty,min=1,max=20"`
}

// GptPoolBatchRefreshResp ??????
type GptPoolBatchRefreshResp struct {
	Total int `json:"total"`
	OK    int `json:"ok"`
	Fail  int `json:"fail"`
}

// GptPoolPurgeReq ????????
type GptPoolPurgeReq struct {
	Scope string `json:"scope" binding:"required,oneof=all invalid token_expired quota_exceeded no_refresh"`
}

// GptPoolStatsResp ????
type GptPoolStatsResp struct {
	Total    int64 `json:"total"`
	Valid    int64 `json:"valid"`
	Invalid  int64 `json:"invalid"`
	Disabled int64 `json:"disabled"`
	Cooldown int64 `json:"cooldown"`
}

// GptPoolDetailResp ????????????????????? / ?????
//
// ???? endpoint ???????admin token????????????
type GptPoolDetailResp struct {
	ID            uint64 `json:"id"`
	Email         string `json:"email"`
	Password      string `json:"password,omitempty"`
	AccessToken   string `json:"access_token,omitempty"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	IDToken       string `json:"id_token,omitempty"`
	APIKey        string `json:"api_key,omitempty"`
	OAuthIssuer   string `json:"oauth_issuer,omitempty"`
	OAuthClientID string `json:"oauth_client_id,omitempty"`

	// ???? + ?????? resp ???????????????
	PlanType                   string   `json:"plan_type,omitempty"`
	ChatGPTAccountID           string   `json:"chatgpt_account_id,omitempty"`
	QuotaPrimaryUsedPercent    *float64 `json:"quota_primary_used_percent,omitempty"`
	QuotaPrimaryResetAt        int64    `json:"quota_primary_reset_at,omitempty"`
	QuotaSecondaryUsedPercent  *float64 `json:"quota_secondary_used_percent,omitempty"`
	QuotaSecondaryResetAt      int64    `json:"quota_secondary_reset_at,omitempty"`
	QuotaCodeReviewUsedPercent *float64 `json:"quota_code_review_used_percent,omitempty"`
	LastQuotaCheckAt           int64    `json:"last_quota_check_at,omitempty"`

	Status        string `json:"status"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	LastCheckedAt int64  `json:"last_checked_at,omitempty"`
	LastRefreshAt int64  `json:"last_refresh_at,omitempty"`
	LastUsedAt    int64  `json:"last_used_at,omitempty"`
	FailureCount  int    `json:"failure_count"`
	ErrorMessage  string `json:"error_message,omitempty"`
	Notes         string `json:"notes,omitempty"`
	RegisteredAt  int64  `json:"registered_at"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}
