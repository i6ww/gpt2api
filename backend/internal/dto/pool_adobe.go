package dto

// AdobePoolListReq ????
type AdobePoolListReq struct {
	Status   string `form:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	Source   string `form:"source" binding:"omitempty,oneof=register import"`
	Keyword  string `form:"keyword" binding:"omitempty,max=64"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// AdobePoolCreateReq ??????????
//
// Name 是 Import 专用的 email 别名：cookies.json 类导出文件里把账号写成
// `{"cookie":"...", "name":"foo@bar"}`，没有 `email` key。Import 解析时如果 Email 为空
// 就回落到 Name；Create 单条接口仍然按 binding 要求的 `email` 走，Name 字段被忽略。
type AdobePoolCreateReq struct {
	Email       string  `json:"email" binding:"required,email,max=255"`
	Name        string  `json:"name" binding:"-"`
	DisplayName string  `json:"display_name" binding:"omitempty,max=128"`
	AdobeUserID string  `json:"adobe_user_id" binding:"omitempty,max=64"`
	Password    string  `json:"password" binding:"omitempty,max=255"`
	AccessToken string  `json:"access_token" binding:"omitempty,max=8000"`
	Cookie      string  `json:"cookie" binding:"omitempty,max=8000"`
	DeviceToken string  `json:"device_token" binding:"omitempty,max=8000"`
	DeviceID    string  `json:"device_id" binding:"omitempty,max=64"`
	Status      string  `json:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	Source      string  `json:"source" binding:"omitempty,oneof=register import"`
	Credits     float64 `json:"credits" binding:"omitempty,min=0"`
	ExpiresAt   int64   `json:"expires_at" binding:"omitempty,min=0"`
	Notes       string  `json:"notes" binding:"omitempty,max=500"`
}

// AdobePoolUpdateReq ??
type AdobePoolUpdateReq struct {
	DisplayName    *string  `json:"display_name" binding:"omitempty,max=128"`
	AdobeUserID    *string  `json:"adobe_user_id" binding:"omitempty,max=64"`
	Password       *string  `json:"password" binding:"omitempty,max=255"`
	AccessToken    *string  `json:"access_token" binding:"omitempty,max=8000"`
	Cookie         *string  `json:"cookie" binding:"omitempty,max=8000"`
	DeviceToken    *string  `json:"device_token" binding:"omitempty,max=8000"`
	DeviceID       *string  `json:"device_id" binding:"omitempty,max=64"`
	Status         *string  `json:"status" binding:"omitempty,oneof=valid invalid disabled cooldown"`
	Credits        *float64 `json:"credits" binding:"omitempty,min=0"`
	ExpiresAt      *int64   `json:"expires_at" binding:"omitempty,min=0"`
	RefreshEnabled *int8    `json:"refresh_enabled" binding:"omitempty,oneof=0 1"`
	Notes          *string  `json:"notes" binding:"omitempty,max=500"`
}

// AdobePoolImportReq ??????????? JSON ???
type AdobePoolImportReq struct {
	Text   string `json:"text" binding:"required"`
	Source string `json:"source" binding:"omitempty,oneof=register import"`
}

// AdobePoolImportResult ??????
type AdobePoolImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

// AdobePoolBatchIDsReq ???? ID ??
type AdobePoolBatchIDsReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// AdobePoolBulkOpResult ??????
type AdobePoolBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// AdobePoolResp ????
type AdobePoolResp struct {
	ID                 uint64  `json:"id"`
	Email              string  `json:"email"`
	DisplayName        string  `json:"display_name,omitempty"`
	AdobeUserID        string  `json:"adobe_user_id,omitempty"`
	HasPassword        bool    `json:"has_password"`
	HasAccessToken     bool    `json:"has_access_token"`
	HasCookie          bool    `json:"has_cookie"`
	HasDeviceToken     bool    `json:"has_device_token"`
	DeviceID           string  `json:"device_id,omitempty"`
	Status             string  `json:"status"`
	Source             string  `json:"source"`
	Credits            float64 `json:"credits"`
	ExpiresAt          int64   `json:"expires_at,omitempty"`
	LastCheckedAt      int64   `json:"last_checked_at,omitempty"`
	LastCreditsCheckAt int64   `json:"last_credits_check_at,omitempty"`
	LastRefreshAt      int64   `json:"last_refresh_at,omitempty"`
	LastUsedAt         int64   `json:"last_used_at,omitempty"`
	RefreshEnabled     int8    `json:"refresh_enabled"`
	FailureCount       int     `json:"failure_count"`
	ErrorMessage       string  `json:"error_message,omitempty"`
	CooldownUntil      int64   `json:"cooldown_until,omitempty"`
	Notes              string  `json:"notes,omitempty"`
	// Entitlements 给运营看的「该号在各档位上的权益学习状态」。
	// 由 generation_service 在撞到 NotEntitledError 时自动写入，nil = 该号从未撞过。
	// 字段示例：{"image_4k": "unknown" | "ok" | "blocked"}。
	Entitlements *AdobePoolEntitlements `json:"entitlements,omitempty"`
	CreatedAt    int64                  `json:"created_at"`
	UpdatedAt    int64                  `json:"updated_at"`
}

// AdobePoolEntitlements 该号在各档位上的权益状态。
//
// 三态语义：
//   - "ok"      : 该号最近成功跑过此档位（暂未实现 ok 标记，留扩展）；
//   - "blocked" : 该号撞过 NotEntitledError 且在 TTL（7 天）内 → filter 会跳过；
//   - "unknown" : 从未撞过 / 标记已过期 → 默认乐观允许试一次。
type AdobePoolEntitlements struct {
	Image1K          string `json:"image_1k"`
	Image1KCheckedAt int64  `json:"image_1k_checked_at,omitempty"`
	Image2K          string `json:"image_2k"`
	Image2KCheckedAt int64  `json:"image_2k_checked_at,omitempty"`
	Image4K          string `json:"image_4k"`
	Image4KCheckedAt int64  `json:"image_4k_checked_at,omitempty"`
}

// AdobePoolStatsResp ????
type AdobePoolStatsResp struct {
	Total    int64 `json:"total"`
	Valid    int64 `json:"valid"`
	Invalid  int64 `json:"invalid"`
	Disabled int64 `json:"disabled"`
	Cooldown int64 `json:"cooldown"`
}
