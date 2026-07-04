package model

import "time"

// 上游通道计费模式。字段名跟 migrations/20260513040000_upstream_api_management.sql 对齐。
//
// per_call         一次调用一个固定价；unit_price.micro_usd
// per_unit         按单位收费（图片张数 / 视频秒数）；unit_price.micro_usd_per_unit + unit_label
// per_token_io     LLM token 进出分别计价；unit_price.input_per_1k_micro_usd / output_per_1k_micro_usd
// per_credit       Adobe Firefly 这种"用 credit 换算"；
//                  unit_price.credits_per_call + (credits_per_month + monthly_fixed_cost) 用来推单价
// subscription     纯订阅平摊；monthly_fixed_cost / expected_monthly_calls = 单次摊销
// custom           其它无法表达的，admin UI 提示让人填 notes，cost_micro_usd 直接走 manual。
const (
	BillingModePerCall      = "per_call"
	BillingModePerUnit      = "per_unit"
	BillingModePerTokenIO   = "per_token_io"
	BillingModePerCredit    = "per_credit"
	BillingModeSubscription = "subscription"
	BillingModeCustom       = "custom"
)

// 通道类型常量。
//
//	local_pool   仅 1 行；runtime 按请求 model→provider 自动选 pool_gpt/pool_grok/pool_adobe。
//	external_api N 行；runtime 用 api_key + base_url 走 OpenAI-compat 协议直接发起请求。
const (
	ChannelTypeLocalPool   = "local_pool"
	ChannelTypeExternalAPI = "external_api"
)

// UpstreamChannel 上游通道。
//
// 一个通道 = 一种「provider + 路径 + 计费方式」的组合。
// 例如 Adobe Firefly 的 1K / 2K / 4K 分三个通道，因为虽然 provider 都是 adobe，
// 但 credits_per_call 不同；GPT 的 web 路径和 API 路径是两个通道（前者基本免费，后者按 token 计）。
type UpstreamChannel struct {
	ID    uint64 `gorm:"primaryKey;column:id" json:"id"`
	Key   string `gorm:"column:key;size:64;not null;uniqueIndex:uk_upstream_channel_key" json:"key"`
	// ChannelType 通道类型，决定 runtime 调度方式：
	//   - local_pool   : 用本地号池（系统建好的唯一行，不可删）；
	//   - external_api : 用 api_key + base_url 直连第三方付费 API。
	ChannelType          string    `gorm:"column:channel_type;size:16;not null;default:external_api;index:idx_channel_type" json:"channel_type"`
	Provider             string    `gorm:"column:provider;size:32;not null;index:idx_provider_route,priority:1" json:"provider"`
	Route                string    `gorm:"column:route;size:48;not null;default:'';index:idx_provider_route,priority:2" json:"route"`
	BaseURL              string    `gorm:"column:base_url;size:255;not null;default:''" json:"base_url"`
	Label                string    `gorm:"column:label;size:120;not null;default:''" json:"label"`
	Enabled              bool      `gorm:"column:enabled;not null;default:1" json:"enabled"`
	BillingMode          string    `gorm:"column:billing_mode;size:32;not null;default:per_call" json:"billing_mode"`
	UnitPrice            string    `gorm:"column:unit_price;type:json;not null" json:"unit_price"`
	// APIKeyEnc external_api 通道的 API key，AES-GCM 加密；local_pool 留空。
	// 写库前由 UpstreamChannelService 加密；读出来后由调度路径解密。
	APIKeyEnc            []byte    `gorm:"column:api_key_enc;type:blob" json:"-"`
	Currency             string    `gorm:"column:currency;size:3;not null;default:USD" json:"currency"`
	Capabilities         string    `gorm:"column:capabilities;type:json;not null" json:"capabilities"`
	// SupportedModels external_api 通道支持的内部 model_code 列表 JSON（["gpt-4o","grok-4-fast"...]）。
	// local_pool 留空 = 系统自动识别全部。
	SupportedModels      *string   `gorm:"column:supported_models;type:json" json:"supported_models,omitempty"`
	MonthlyFixedCost     int64     `gorm:"column:monthly_fixed_cost;not null;default:0" json:"monthly_fixed_cost"`
	ExpectedMonthlyCalls int64     `gorm:"column:expected_monthly_calls;not null;default:0" json:"expected_monthly_calls"`
	FXToCNY              float64   `gorm:"column:fx_to_cny;type:decimal(10,4);not null;default:0" json:"fx_to_cny"`
	Notes                *string   `gorm:"column:notes;type:text" json:"notes,omitempty"`
	CreatedAt            time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt            time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 表名。
func (UpstreamChannel) TableName() string { return "upstream_channel" }

// UpstreamModelRoute 内部 model_code/variant 路由到一个通道。
//
// 同 (model_code, variant_key) 可以有多条 enabled=true 的路由，按 priority 升序选第一条；
// 命中的路由 cost_multiplier 直接乘到 cost_micro_usd 上，用于"同通道下分档"场景
// （例如 2K 用 1× 4K 用 4× 都映射到 firefly_credit_pack 这一条 per_credit 通道）。
type UpstreamModelRoute struct {
	ID                uint64    `gorm:"primaryKey;column:id" json:"id"`
	ModelCode         string    `gorm:"column:model_code;size:64;not null;index:idx_model_variant_priority,priority:1" json:"model_code"`
	VariantKey        string    `gorm:"column:variant_key;size:32;not null;default:'';index:idx_model_variant_priority,priority:2" json:"variant_key"`
	UpstreamChannelID uint64    `gorm:"column:upstream_channel_id;not null;index:idx_channel" json:"upstream_channel_id"`
	Priority          int16     `gorm:"column:priority;not null;default:1;index:idx_model_variant_priority,priority:3" json:"priority"`
	Enabled           bool      `gorm:"column:enabled;not null;default:1" json:"enabled"`
	CostMultiplier    float64   `gorm:"column:cost_multiplier;type:decimal(6,3);not null;default:1.000" json:"cost_multiplier"`
	Notes             *string   `gorm:"column:notes;size:255" json:"notes,omitempty"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName 表名。
func (UpstreamModelRoute) TableName() string { return "upstream_model_route" }
