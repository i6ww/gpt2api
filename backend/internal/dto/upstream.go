package dto

// UpstreamChannel admin UI / API 双向使用的 DTO（不直接复用 gorm model 是因为
// unit_price / capabilities 在数据库里是 JSON 字符串，对外要解码成 object）。

// ChannelListReq 列表查询。
type ChannelListReq struct {
	Provider string `form:"provider"`
	Enabled  *bool  `form:"enabled"`
	Keyword  string `form:"keyword"`
	Page     int    `form:"page"`
	PageSize int    `form:"page_size"`
}

// ChannelDTO 通道展示对象。
//
// 注意：APIKeyMasked 是脱敏字符串（前 4 + 后 4 + 中间星号），
// 永远不返回明文 key。运营要看明文请走 /upstream/channels/:id/secret 接口（未实现）。
type ChannelDTO struct {
	ID                   uint64         `json:"id"`
	Key                  string         `json:"key"`
	ChannelType          string         `json:"channel_type"` // local_pool / external_api
	Provider             string         `json:"provider"`
	Route                string         `json:"route"`
	BaseURL              string         `json:"base_url"`
	Label                string         `json:"label"`
	Enabled              bool           `json:"enabled"`
	BillingMode          string         `json:"billing_mode"`
	UnitPrice            map[string]any `json:"unit_price"`
	APIKeyMasked         string         `json:"api_key_masked,omitempty"` // 仅显示，写库走 ChannelSaveReq.APIKey
	HasAPIKey            bool           `json:"has_api_key"`              // 是否已配 key
	Currency             string         `json:"currency"`
	Capabilities         map[string]any `json:"capabilities"`
	SupportedModels      []string       `json:"supported_models,omitempty"`
	MonthlyFixedCost     int64          `json:"monthly_fixed_cost"`
	ExpectedMonthlyCalls int64          `json:"expected_monthly_calls"`
	FXToCNY              float64        `json:"fx_to_cny"`
	Notes                string         `json:"notes"`
	CreatedAt            string         `json:"created_at"`
	UpdatedAt            string         `json:"updated_at"`
}

// ChannelSaveReq 创建/更新统一 DTO（区别：创建必须带 key，更新可省略 key）。
//
// APIKey 字段语义：
//   - 创建：填入即明文 key（加密后存）；留空则该通道无 key（local_pool 用）。
//   - 更新：留空 = 保留现有 key；填新值 = 覆盖；填特殊值 "__CLEAR__" = 清空。
type ChannelSaveReq struct {
	Key                  string         `json:"key"`
	ChannelType          string         `json:"channel_type"` // 仅创建有用；更新忽略（local_pool 行禁止改回 external_api）
	Provider             string         `json:"provider"`
	Route                string         `json:"route"`
	BaseURL              string         `json:"base_url"`
	Label                string         `json:"label"`
	Enabled              *bool          `json:"enabled"`
	BillingMode          string         `json:"billing_mode"`
	UnitPrice            map[string]any `json:"unit_price"`
	APIKey               *string        `json:"api_key,omitempty"` // 见上方注释；nil = 不变
	Currency             string         `json:"currency"`
	Capabilities         map[string]any `json:"capabilities"`
	SupportedModels      []string       `json:"supported_models,omitempty"`
	MonthlyFixedCost     int64          `json:"monthly_fixed_cost"`
	ExpectedMonthlyCalls int64          `json:"expected_monthly_calls"`
	FXToCNY              float64        `json:"fx_to_cny"`
	Notes                string         `json:"notes"`
}

// APIKeyClearSentinel 在 ChannelSaveReq.APIKey 里填这个值代表"清空 API key"。
const APIKeyClearSentinel = "__CLEAR__"

// RouteListReq 路由查询。
type RouteListReq struct {
	ModelCode  string `form:"model_code"`
	VariantKey string `form:"variant_key"`
	ChannelID  uint64 `form:"channel_id"`
	Enabled    *bool  `form:"enabled"`
	Page       int    `form:"page"`
	PageSize   int    `form:"page_size"`
}

// RouteDTO 路由展示对象。
type RouteDTO struct {
	ID                uint64  `json:"id"`
	ModelCode         string  `json:"model_code"`
	VariantKey        string  `json:"variant_key"`
	UpstreamChannelID uint64  `json:"upstream_channel_id"`
	ChannelKey        string  `json:"channel_key"`
	ChannelLabel      string  `json:"channel_label"`
	ChannelProvider   string  `json:"channel_provider"`
	Priority          int16   `json:"priority"`
	Enabled           bool    `json:"enabled"`
	CostMultiplier    float64 `json:"cost_multiplier"`
	Notes             string  `json:"notes"`
}

// RouteSaveReq 创建/更新统一 DTO。
type RouteSaveReq struct {
	ModelCode         string  `json:"model_code"`
	VariantKey        string  `json:"variant_key"`
	UpstreamChannelID uint64  `json:"upstream_channel_id"`
	Priority          int16   `json:"priority"`
	Enabled           *bool   `json:"enabled"`
	CostMultiplier    float64 `json:"cost_multiplier"`
	Notes             string  `json:"notes"`
}

// ProfitReportReq 利润报表查询。
type ProfitReportReq struct {
	From string `form:"from"` // RFC3339 / yyyy-mm-dd
	To   string `form:"to"`
	Dim  string `form:"dim"`  // day / day,model / day,channel / day,provider
}

// ProfitOverview 利润总览（dashboard 用）。
type ProfitOverview struct {
	TaskCount       int64   `json:"task_count"`
	CostMicroUSD    int64   `json:"cost_micro_usd"`
	SaleMicroCNY    int64   `json:"sale_micro_cny"`
	SalePoints      int64   `json:"sale_points"`
	GrossMarginCNY  int64   `json:"gross_margin_micro_cny"`
	GrossMarginRate float64 `json:"gross_margin_rate"`
	FxUSDToCNY      float64 `json:"fx_usd_to_cny"`
}
