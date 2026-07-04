package dto

type AdminWalletLogListReq struct {
	Keyword string `form:"keyword" binding:"omitempty,max=128"`
	UserID  uint64 `form:"user_id" binding:"omitempty,min=1"`
	BizType string `form:"biz_type" binding:"omitempty,max=32"`
	// 注意：用 string + 自定义解析，不要用 *int + oneof=-1 1。Gin 的 form binding
	// 对 *int 来说 `direction=` 仍然会走绑定到 0，再被 oneof 拒掉 400；前端
	// 「全部方向」下拉框 selected="" 时，axios 序列化出来就是 ?direction=
	// 一旦整页列表就全空了。这里只接受 "" / "1" / "-1"，Direction() 还原成 *int。
	DirectionRaw string `form:"direction" binding:"omitempty,oneof=-1 1"`
	Page         int    `form:"page" binding:"omitempty,min=1"`
	PageSize     int    `form:"page_size" binding:"omitempty,min=1,max=10000"`
}

// Direction 把 form 里的字符串还原成 *int；空串返回 nil 表示「不过滤」。
func (r *AdminWalletLogListReq) Direction() *int {
	switch r.DirectionRaw {
	case "1":
		v := 1
		return &v
	case "-1":
		v := -1
		return &v
	default:
		return nil
	}
}

type AdminWalletLogResp struct {
	ID           uint64 `json:"id"`
	CreatedAt    int64  `json:"created_at"`
	UserID       uint64 `json:"user_id"`
	UserLabel    string `json:"user_label"`
	Direction    int8   `json:"direction"`
	BizType      string `json:"biz_type"`
	BizID        string `json:"biz_id"`
	Points       int64  `json:"points"`
	PointsBefore int64  `json:"points_before"`
	PointsAfter  int64  `json:"points_after"`
	Remark       string `json:"remark,omitempty"`
}

// AdminWalletLogSummaryResp 钱包流水的全局汇总（不分页），给「充值消费记录」
// 顶部 stat 卡片用：今日 vs 累计的充值 / 消费 / 退款 + 用户数。
type AdminWalletLogSummaryResp struct {
	RechargeToday int64 `json:"recharge_today"`
	RechargeTotal int64 `json:"recharge_total"`
	ConsumeToday  int64 `json:"consume_today"`
	ConsumeTotal  int64 `json:"consume_total"`
	RefundToday   int64 `json:"refund_today"`
	RefundTotal   int64 `json:"refund_total"`
	NetToday      int64 `json:"net_today"`  // 收入 - 支出（今天）
	NetTotal      int64 `json:"net_total"`  // 收入 - 支出（全部）
	RecordsToday  int64 `json:"records_today"`
	RecordsTotal  int64 `json:"records_total"`
	UsersTouched  int64 `json:"users_touched"` // 有流水的去重用户数
}
