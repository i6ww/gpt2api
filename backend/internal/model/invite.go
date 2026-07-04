// Package model 邀请关系 / 返佣记录实体。
package model

import "time"

// UserInviteRelation 邀请绑定关系（注册时一次性写入，绑定后不可改）。
// 表 user_invite_relation。
type UserInviteRelation struct {
	UserID     uint64    `gorm:"primaryKey;column:user_id" json:"user_id"`
	InviterID  uint64    `gorm:"column:inviter_id;not null;index:idx_inviter" json:"inviter_id"`
	InviteCode string    `gorm:"column:invite_code;size:16;not null" json:"invite_code"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
}

// TableName 自定义表名。
func (UserInviteRelation) TableName() string { return "user_invite_relation" }

// InviteRewardLog 邀请返佣日志：每个被邀请者的每次充值，最多生成一条。
// uk_source(source_log_id) 提供幂等性保证。
type InviteRewardLog struct {
	ID             uint64    `gorm:"primaryKey;column:id" json:"id"`
	InviterID      uint64    `gorm:"column:inviter_id;not null;index:idx_inviter_created,priority:1" json:"inviter_id"`
	InviteeID      uint64    `gorm:"column:invitee_id;not null;index:idx_invitee" json:"invitee_id"`
	SourceLogID    uint64    `gorm:"column:source_log_id;not null;uniqueIndex:uk_source" json:"source_log_id"`
	RechargePoints int64     `gorm:"column:recharge_points;not null" json:"recharge_points"`
	RewardPoints   int64     `gorm:"column:reward_points;not null" json:"reward_points"`
	RateBP         int       `gorm:"column:rate_bp;not null" json:"rate_bp"`
	WalletLogID    uint64    `gorm:"column:wallet_log_id;not null" json:"wallet_log_id"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime;index:idx_inviter_created,priority:2" json:"created_at"`
}

// TableName 自定义表名。
func (InviteRewardLog) TableName() string { return "invite_reward_log" }
