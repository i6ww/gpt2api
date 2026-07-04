// Package dto 计费相关 DTO。
package dto

// CDKRedeemReq 兑换 CDK。
type CDKRedeemReq struct {
	Code string `json:"code" binding:"required,min=4,max=32"`
}

// WalletLogResp 钱包流水响应（一行）。
type WalletLogResp struct {
	ID           uint64 `json:"id"`
	Direction    int8   `json:"direction"`
	BizType      string `json:"biz_type"`
	BizID        string `json:"biz_id"`
	Points       int64  `json:"points"`
	PointsBefore int64  `json:"points_before"`
	PointsAfter  int64  `json:"points_after"`
	Remark       string `json:"remark,omitempty"`
	CreatedAt    int64  `json:"created_at"`
}

// RechargeProductItem 用户端可见的充值套餐项。
// 来源：admin 系统配置 `recharge.packages`（JSON 数组）。
// 与后台同名结构相比，去掉了 remark 等内部字段，避免暴露运营备注。
//
// 单位约定：
//   - Amount  ：元（float64，admin 直接以元为单位录入，支持 9.9 这类小数）
//   - Points  / BonusPoints：×100 整数（与 wallet_log.points 同单位，前端用 fmtPoints / 100 还原）
type RechargeProductItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Amount      float64 `json:"amount"`
	Points      int64   `json:"points"`
	BonusPoints int64   `json:"bonus_points"`
	Badge       string  `json:"badge,omitempty"`
	SortOrder   int     `json:"sort_order"`
}

// RechargeContactInfo 在线支付未接通前展示给用户的客服联系方式。
type RechargeContactInfo struct {
	Email  string `json:"email,omitempty"`  // 客服邮箱（运营在 admin 配置）
	Notice string `json:"notice,omitempty"` // 给用户的购买说明（如要求附 UID + 套餐 ID）
}

// RechargeProductsResp `GET /api/v1/recharge/products` 响应。
// products 为空表示运营尚未上架任何套餐。
// online_payment_enabled 预留给后续在线支付接入：true 时前端可以走支付通道下单，
// 当前一律 false（仅展示套餐 + 客服邮箱）。
type RechargeProductsResp struct {
	Products             []RechargeProductItem `json:"products"`
	Contact              RechargeContactInfo   `json:"contact"`
	OnlinePaymentEnabled bool                  `json:"online_payment_enabled"`
}

// CDKBatchCreateReq 管理后台创建 CDK 批次。
type CDKBatchCreateReq struct {
	BatchNo      string `json:"batch_no"       binding:"required,min=4,max=32"`
	Name         string `json:"name"           binding:"required,min=1,max=64"`
	Points       int64  `json:"points"         binding:"required,min=1"`
	Qty          int    `json:"qty"            binding:"required,min=1,max=100000"`
	PerUserLimit int    `json:"per_user_limit" binding:"omitempty,min=0"`
	ExpireAt     int64  `json:"expire_at"      binding:"omitempty,min=0"` // unix
}
