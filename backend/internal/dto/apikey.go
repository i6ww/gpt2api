// Package dto API Key 入参 / 出参。
package dto

// APIKeyCreateReq 创建 Key。
type APIKeyCreateReq struct {
	Name       string `json:"name"        binding:"required,min=1,max=64"`
	Scope      string `json:"scope"       binding:"omitempty,max=255"`
	RPMLimit   int    `json:"rpm_limit"   binding:"omitempty,min=0,max=10000"`
	DailyQuota int    `json:"daily_quota" binding:"omitempty,min=0"`
	ExpireDays int    `json:"expire_days" binding:"omitempty,min=0,max=3650"`
}

// APIKeyCreateResp 创建返回（含明文，仅一次）。
type APIKeyCreateResp struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	Plain     string `json:"plain"`
	Prefix    string `json:"prefix"`
	Last4     string `json:"last4"`
	Scope     string `json:"scope"`
	CreatedAt int64  `json:"created_at"`
}

// APIKeyResp 列表 / 详情返回（已脱敏）。
type APIKeyResp struct {
	ID         uint64 `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	Last4      string `json:"last4"`
	Mask       string `json:"mask"`
	Scope      string `json:"scope"`
	RPMLimit   int    `json:"rpm_limit"`
	DailyQuota int    `json:"daily_quota"`
	Status     int8   `json:"status"`
	ExpireAt   int64  `json:"expire_at,omitempty"`
	LastUsedAt int64  `json:"last_used_at,omitempty"`
	CreatedAt  int64  `json:"created_at"`
}

// APIKeyStatsReq 查询 Key 用量统计的过滤条件。
//
// 时间窗口为闭区间 [Since, Until]（unix 秒）。两者都可省略：
//   - 都不传 → 全量（截至当前）。
//   - 只传 Since → 从 Since 到现在。
//   - 只传 Until → 从最早到 Until（不限起点）。
type APIKeyStatsReq struct {
	Since int64 `form:"since"`
	Until int64 `form:"until"`
}

// APIKeyStat 单个 Key 在指定窗口内的用量。
type APIKeyStat struct {
	KeyID            uint64 `json:"key_id"`
	CallTotal        int64  `json:"call_total"`       // 全部任务条数（含失败 / 已退款）
	CallSucceeded    int64  `json:"call_succeeded"`   // status=2 成功完成
	CallFailed       int64  `json:"call_failed"`      // status=3 / 4 失败 + 已退款
	ConsumedPoints   int64  `json:"consumed_points"`  // 真实扣费（成功任务的 cost_points 之和）
	RefundedPoints   int64  `json:"refunded_points"`  // 失败 / 退款任务的 cost_points 之和（已还回钱包）
	LastCalledAt     int64  `json:"last_called_at,omitempty"`
}

// APIKeyStatsResp 列出所有 keys 在窗口内的统计 + 顶部汇总。
type APIKeyStatsResp struct {
	Since   int64        `json:"since"`
	Until   int64        `json:"until"`
	Total   APIKeyStat   `json:"total"`
	PerKey  []APIKeyStat `json:"per_key"`
}
