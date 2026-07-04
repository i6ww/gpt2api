package dto

// GooglePoolListReq FlowMusic（歌曲）Google 号池列表查询。
type GooglePoolListReq struct {
	Status   string `form:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	Source   string `form:"source" binding:"omitempty,oneof=register import"`
	Keyword  string `form:"keyword" binding:"omitempty,max=64"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// GooglePoolCreateReq 单条新增。
//
// 三种填法（优先级从上到下）：
//   - Cookies：浏览器导出的 cookie JSON 数组字符串（含 sb-sb-auth-token.N），最省事；
//   - AccessToken/RefreshToken/ProviderToken 等手填 token；
//   - Name 是 Import 用的 email 别名（cookie 文件常用 name 而非 email）。
type GooglePoolCreateReq struct {
	Email                string  `json:"email" binding:"omitempty,email,max=255"`
	Name                 string  `json:"name" binding:"-"`
	DisplayName          string  `json:"display_name" binding:"omitempty,max=128"`
	Cookies              string  `json:"cookies" binding:"omitempty"`
	AccessToken          string  `json:"access_token" binding:"omitempty,max=8000"`
	RefreshToken         string  `json:"refresh_token" binding:"omitempty,max=2000"`
	ProviderToken        string  `json:"provider_token" binding:"omitempty,max=8000"`
	ProviderRefreshToken string  `json:"provider_refresh_token" binding:"omitempty,max=2000"`
	Status               string  `json:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	Source               string  `json:"source" binding:"omitempty,oneof=register import"`
	Credits              float64 `json:"credits" binding:"omitempty,min=0"`
	ExpiresAt            int64   `json:"expires_at" binding:"omitempty,min=0"`
	Notes                string  `json:"notes" binding:"omitempty,max=500"`
}

// GooglePoolUpdateReq 单条更新。
type GooglePoolUpdateReq struct {
	DisplayName    *string  `json:"display_name" binding:"omitempty,max=128"`
	Cookies        *string  `json:"cookies" binding:"omitempty"`
	AccessToken    *string  `json:"access_token" binding:"omitempty,max=8000"`
	RefreshToken   *string  `json:"refresh_token" binding:"omitempty,max=2000"`
	Status         *string  `json:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	Credits        *float64 `json:"credits" binding:"omitempty,min=0"`
	ExpiresAt      *int64   `json:"expires_at" binding:"omitempty,min=0"`
	RefreshEnabled *int8    `json:"refresh_enabled" binding:"omitempty,oneof=0 1"`
	Notes          *string  `json:"notes" binding:"omitempty,max=500"`
}

// GooglePoolImportReq 批量文本导入（cookie JSON 数组 / JSONL / 导出 JSON）。
type GooglePoolImportReq struct {
	Text   string `json:"text" binding:"required"`
	Source string `json:"source" binding:"omitempty,oneof=register import"`
}

// GooglePoolImportResult 导入结果。
type GooglePoolImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

// GooglePoolBatchIDsReq 批量 ID。
type GooglePoolBatchIDsReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// GooglePoolBulkOpResult 批量操作结果。
type GooglePoolBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// GooglePoolResp 列表/详情响应（凭证字段只返回 has_* 布尔，不回明文）。
type GooglePoolResp struct {
	ID                uint64  `json:"id"`
	Email             string  `json:"email"`
	DisplayName       string  `json:"display_name,omitempty"`
	HasCredential     bool    `json:"has_credential"`
	HasCookie         bool    `json:"has_cookie"`
	ProtocolMode      string  `json:"protocol_mode"`
	Status            string  `json:"status"`
	Source            string  `json:"source"`
	Credits           float64 `json:"credits"`
	TokensRemaining   float64 `json:"tokens_remaining"`
	SubscriptionTier  string  `json:"subscription_tier,omitempty"`
	ExpiresAt         int64   `json:"expires_at,omitempty"`
	LastCheckedAt     int64   `json:"last_checked_at,omitempty"`
	LastRefreshAt     int64   `json:"last_refresh_at,omitempty"`
	LastRefreshResult string  `json:"last_refresh_result,omitempty"`
	LastUsedAt        int64   `json:"last_used_at,omitempty"`
	RefreshEnabled    int8    `json:"refresh_enabled"`
	FailureCount      int     `json:"failure_count"`
	ErrorMessage      string  `json:"error_message,omitempty"`
	CooldownUntil     int64   `json:"cooldown_until,omitempty"`
	Notes             string  `json:"notes,omitempty"`
	CreatedAt         int64   `json:"created_at"`
	UpdatedAt         int64   `json:"updated_at"`
}

// GooglePoolStatsResp 状态分布。
type GooglePoolStatsResp struct {
	Total    int64 `json:"total"`
	Valid    int64 `json:"valid"`
	Invalid  int64 `json:"invalid"`
	Disabled int64 `json:"disabled"`
	Cooldown int64 `json:"cooldown"`
}
