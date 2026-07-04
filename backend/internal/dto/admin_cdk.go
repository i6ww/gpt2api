// Package dto 管理后台 - CDK 批次 / 单码 DTO。
//
// 与 CDKBatchCreateReq（dto/billing.go）配套，覆盖运营常用动作：
// 列出批次 / 查批次详情 / 启停批次 / 看单码 / 追加生成 / 吊销单码 / 导出 CSV。
package dto

// AdminCDKBatchListReq 管理后台分页查询 CDK 批次。
type AdminCDKBatchListReq struct {
	Keyword  string `form:"keyword" binding:"omitempty,max=128"`
	Status   *int   `form:"status" binding:"omitempty,oneof=0 1"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=200"`
}

// AdminCDKBatchResp 批次列表 / 详情共用响应。
type AdminCDKBatchResp struct {
	ID            uint64 `json:"id"`
	BatchNo       string `json:"batch_no"`
	Name          string `json:"name"`
	RewardType    string `json:"reward_type"`
	RewardPoints  int64  `json:"reward_points"` // 解出来给前端用，单位与 CDK 一致（×100 储存）
	TotalQty      int    `json:"total_qty"`
	UsedQty       int    `json:"used_qty"`
	RevokedQty    int    `json:"revoked_qty"` // 已吊销数（status=2）
	RemainingQty  int    `json:"remaining_qty"`
	PerUserLimit  int    `json:"per_user_limit"`
	ExpireAt      int64  `json:"expire_at"` // 0 表示永久
	Status        int8   `json:"status"`
	CreatedBy     uint64 `json:"created_by,omitempty"`
	CreatedAt     int64  `json:"created_at"`
}

// AdminCDKBatchToggleReq 启停批次。
type AdminCDKBatchToggleReq struct {
	Status int8 `json:"status" binding:"oneof=0 1"`
}

// AdminCDKBatchAppendReq 给已有批次追加生成 N 张。
type AdminCDKBatchAppendReq struct {
	Qty int `json:"qty" binding:"required,min=1,max=100000"`
}

// AdminCDKBatchAppendResp 追加生成返回。
type AdminCDKBatchAppendResp struct {
	BatchID    uint64 `json:"batch_id"`
	Appended   int    `json:"appended"`
	TotalQty   int    `json:"total_qty"`
}

// AdminCDKCodeListReq 批次内单码分页查询。
type AdminCDKCodeListReq struct {
	Status   *int   `form:"status" binding:"omitempty,oneof=0 1 2"`
	Keyword  string `form:"keyword" binding:"omitempty,max=128"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=500"`
}

// AdminCDKCodeResp 单码响应。
type AdminCDKCodeResp struct {
	ID        uint64 `json:"id"`
	BatchID   uint64 `json:"batch_id"`
	Code      string `json:"code"`
	Status    int8   `json:"status"` // 0=unused 1=used 2=revoked
	UsedBy    uint64 `json:"used_by,omitempty"`
	UsedAt    int64  `json:"used_at,omitempty"`
	CreatedAt int64  `json:"created_at"`
}
