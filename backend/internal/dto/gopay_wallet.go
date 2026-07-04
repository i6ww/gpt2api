package dto

// GopayWalletCreateReq 新增 GoPay 钱包
//
// 钱包独有信息只剩 PIN，手机号信息从 cloud_phone_pool 关联拿。
// 同一台云手机可以有多个钱包记录（很少见，比如同一个 SIM 换 GoPay 账号），
// 但绝大多数情况是 1:1。
type GopayWalletCreateReq struct {
	PIN          string `json:"pin" binding:"required,min=4,max=12"` // GoPay PIN 一般 6 位
	CloudPhoneID string `json:"cloud_phone_id" binding:"required,min=1,max=64"`
	Remark       string `json:"remark" binding:"omitempty,max=255"`
}

// GopayWalletUpdateReq 编辑钱包
type GopayWalletUpdateReq struct {
	PIN             *string `json:"pin" binding:"omitempty,min=4,max=12"` // 留空=不动
	CloudPhoneID    *string `json:"cloud_phone_id" binding:"omitempty,min=1,max=64"`
	Status          *string `json:"status" binding:"omitempty,oneof=available leased cooldown banned exhausted disabled"`
	ActivePlusCount *int    `json:"active_plus_count" binding:"omitempty,min=0"`
	Remark          *string `json:"remark" binding:"omitempty,max=255"`
}

// GopayWalletListReq 列表过滤
type GopayWalletListReq struct {
	Status         string `form:"status" binding:"omitempty,oneof=available leased cooldown banned exhausted disabled"`
	CloudPhoneID   string `form:"cloud_phone_id" binding:"omitempty,max=64"`
	Keyword        string `form:"keyword" binding:"omitempty,max=64"`
	Page           int    `form:"page" binding:"omitempty,min=1"`
	PageSize       int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
	HasAvailableOn bool   `form:"has_available_on"` // true 时按 quota 过滤"还能开 Plus"的钱包
}

// GopayWalletBatchDeleteReq 批量删除
type GopayWalletBatchDeleteReq struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=2000,dive,min=1"`
}

// GopayWalletResp 钱包响应（不返 PIN 明文）
//
// CountryCode / PhoneNumber / PhoneMasked 来自关联的 cloud_phone_pool（service 层 join 反查），
// 用于列表展示，钱包表自身不再存这些字段。
type GopayWalletResp struct {
	ID              uint64 `json:"id"`
	CountryCode     string `json:"country_code,omitempty"` // 来自 cloud_phone
	PhoneNumber     string `json:"phone_number,omitempty"` // 来自 cloud_phone
	PhoneMasked     string `json:"phone_masked,omitempty"` // 来自 cloud_phone（中间打码）
	HasPIN          bool   `json:"has_pin"`
	CloudPhoneID    string `json:"cloud_phone_id"`
	CloudPhoneName  string `json:"cloud_phone_name,omitempty"`
	Status          string `json:"status"`
	ActivePlusCount int    `json:"active_plus_count"`
	TotalSuccess    int    `json:"total_success"`
	TotalFailed     int    `json:"total_failed"`
	LastUsedAt      int64  `json:"last_used_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	CooldownUntil   int64  `json:"cooldown_until,omitempty"`
	Remark          string `json:"remark,omitempty"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// GopayWalletSecretResp 编辑时返回明文 PIN
type GopayWalletSecretResp struct {
	PIN string `json:"pin"`
}

// GopayWalletImportReq 批量导入
//
// text 格式（一行一个，分隔符 |）：
//   pin|cloud_phone_id[|remark]
type GopayWalletImportReq struct {
	Text  string                 `json:"text" binding:"omitempty"`
	Items []GopayWalletCreateReq `json:"items" binding:"omitempty,dive"`
}

// GopayWalletImportResult 导入结果
type GopayWalletImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

// GopayWalletBulkOpResult 批量操作结果
type GopayWalletBulkOpResult struct {
	Affected int64 `json:"affected"`
}

// ─── Binding ───

// GopayBindingListReq 绑定列表过滤
type GopayBindingListReq struct {
	WalletID     uint64 `form:"wallet_id" binding:"omitempty,min=1"`
	GptAccountID uint64 `form:"gpt_account_id" binding:"omitempty,min=1"`
	Status       string `form:"status" binding:"omitempty,oneof=active cancelled expired refunded"`
	Page         int    `form:"page" binding:"omitempty,min=1"`
	PageSize     int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// GopayBindingResp 绑定响应
type GopayBindingResp struct {
	ID           uint64 `json:"id"`
	WalletID     uint64 `json:"wallet_id"`
	GptAccountID uint64 `json:"gpt_account_id"`
	CSID         string `json:"cs_id,omitempty"`
	ChargeRef    string `json:"charge_ref,omitempty"`
	AmountIDR    int64  `json:"amount_idr"`
	ChargedAt    int64  `json:"charged_at"`
	ExpiresAt    int64  `json:"expires_at"`
	Status       string `json:"status"`
	CancelledAt  int64  `json:"cancelled_at,omitempty"`
	Note         string `json:"note,omitempty"`
}

// GopayBindingCancelReq 取消订阅请求
type GopayBindingCancelReq struct {
	Note string `json:"note" binding:"omitempty,max=255"`
}
