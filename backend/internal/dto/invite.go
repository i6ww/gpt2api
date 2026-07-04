// Package dto 邀请中心传输对象。
package dto

// InviteSummaryResp 邀请中心总览。
type InviteSummaryResp struct {
	InviteCode        string  `json:"invite_code"`
	InviteLink        string  `json:"invite_link"`
	InviteeCount      int64   `json:"invitee_count"`
	TotalRewardPoints int64   `json:"total_reward_points"`
	RewardCount       int64   `json:"reward_count"`
	CommissionRateBP  int     `json:"commission_rate_bp"`
	CommissionRate    float64 `json:"commission_rate"` // 已换算成百分比，例如 10.0
}

// InviteeRow 列表里每一条被邀请用户的展示数据。
//   - account 已脱敏（邮箱：t***@x.com / 手机：138****1234 / 用户名截断中段）
//   - total_recharge 与 user.total_recharge 同口径（点 *100）
//   - reward_to_inviter 本邀请人累计从该被邀请者拿到的返佣（点 *100）
type InviteeRow struct {
	UserID          uint64 `json:"user_id"`
	Account         string `json:"account"`
	Status          int8   `json:"status"`
	TotalRecharge   int64  `json:"total_recharge"`
	RewardToInviter int64  `json:"reward_to_inviter"`
	BoundAt         int64  `json:"bound_at"`
}

// InviteeListReq 列表查询参数。
type InviteeListReq struct {
	Page     int `form:"page" binding:"omitempty,min=1"`
	PageSize int `form:"page_size" binding:"omitempty,min=1,max=100"`
}

// InviteeListResp 分页返回。
type InviteeListResp struct {
	List     []*InviteeRow `json:"list"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}
