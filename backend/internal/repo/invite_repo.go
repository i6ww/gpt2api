// Package repo 邀请关系 / 返佣明细仓储。
package repo

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// InviteRepo 邀请数据访问。
type InviteRepo struct{ db *gorm.DB }

// NewInviteRepo 构造。
func NewInviteRepo(db *gorm.DB) *InviteRepo { return &InviteRepo{db: db} }

// InviteeRow 被邀请者列表行：用户主信息 + 累计充值（user.total_recharge） +
// 该邀请人从这个被邀请者身上累计获得的返佣（reward_to_inviter）。
type InviteeRow struct {
	UserID          uint64 `gorm:"column:user_id" json:"user_id"`
	UUID            string `gorm:"column:uuid" json:"uuid"`
	Email           string `gorm:"column:email" json:"email"`
	Phone           string `gorm:"column:phone" json:"phone"`
	Username        string `gorm:"column:username" json:"username"`
	Status          int8   `gorm:"column:status" json:"status"`
	TotalRecharge   int64  `gorm:"column:total_recharge" json:"total_recharge"`
	BoundAt         int64  `gorm:"column:bound_at" json:"bound_at"`
	RewardToInviter int64  `gorm:"column:reward_to_inviter" json:"reward_to_inviter"`
}

// ListInvitees 分页查询某邀请人名下的所有被邀请者。
//
// LEFT JOIN invite_reward_log 是为了在列表里直接显示「我从这个人身上累计赚了多少返佣」，
// 一次查询 ≤ pageSize 条即可，外加 GROUP BY 聚合。
func (r *InviteRepo) ListInvitees(ctx context.Context, inviterID uint64, page, pageSize int) ([]*InviteeRow, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 10
	}

	// 总数：直接走 user_invite_relation（带索引 idx_inviter），不需要 join。
	var total int64
	if err := r.db.WithContext(ctx).
		Table("user_invite_relation").
		Where("inviter_id = ?", inviterID).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}

	const listSQL = `
SELECT
  u.id                                                                AS user_id,
  u.uuid                                                              AS uuid,
  COALESCE(u.email, '')                                               AS email,
  COALESCE(u.phone, '')                                               AS phone,
  COALESCE(u.username, '')                                            AS username,
  u.status                                                            AS status,
  u.total_recharge                                                    AS total_recharge,
  CAST(UNIX_TIMESTAMP(r.created_at) AS SIGNED)                        AS bound_at,
  COALESCE((SELECT SUM(l.reward_points)
              FROM invite_reward_log l
             WHERE l.inviter_id = r.inviter_id
               AND l.invitee_id = r.user_id), 0)                       AS reward_to_inviter
FROM user_invite_relation r
JOIN user u ON u.id = r.user_id AND u.deleted_at IS NULL
WHERE r.inviter_id = ?
ORDER BY r.created_at DESC, r.user_id DESC
LIMIT ? OFFSET ?`

	var rows []*InviteeRow
	if err := r.db.WithContext(ctx).Raw(listSQL,
		inviterID, pageSize, (page-1)*pageSize,
	).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// InviteSummaryRow 邀请人维度的汇总。
type InviteSummaryRow struct {
	InviteeCount       int64 `gorm:"column:invitee_count"`
	TotalRewardPoints  int64 `gorm:"column:total_reward_points"`
	RewardCount        int64 `gorm:"column:reward_count"`
}

// Summary 邀请人维度的总览：被邀请人数 / 累计返佣点数 / 返佣记录条数。
func (r *InviteRepo) Summary(ctx context.Context, inviterID uint64) (*InviteSummaryRow, error) {
	const sqlText = `
SELECT
  (SELECT COUNT(1) FROM user_invite_relation WHERE inviter_id = ?)                            AS invitee_count,
  COALESCE((SELECT SUM(reward_points) FROM invite_reward_log WHERE inviter_id = ?), 0)        AS total_reward_points,
  COALESCE((SELECT COUNT(1)           FROM invite_reward_log WHERE inviter_id = ?), 0)        AS reward_count`
	var s InviteSummaryRow
	if err := r.db.WithContext(ctx).Raw(sqlText, inviterID, inviterID, inviterID).Scan(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// InsertReward 写入一条返佣明细。
// 触发 uk_source(source_log_id) 重复时返回 ErrRewardDuplicated（幂等）。
func (r *InviteRepo) InsertReward(ctx context.Context, row *model.InviteRewardLog) error {
	err := r.db.WithContext(ctx).Create(row).Error
	if err == nil {
		return nil
	}
	if isMySQLDuplicate(err) {
		return ErrRewardDuplicated
	}
	return err
}

func isMySQLDuplicate(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Error 1062") || strings.Contains(msg, "Duplicate entry")
}

// ErrRewardDuplicated 同一条充值流水已经返过佣（uk_source 冲突）。调用方应忽略。
var ErrRewardDuplicated = errors.New("repo: invite reward already exists for this source log")

// GetInviterID 查询某用户的邀请人 id；无邀请人时返回 (0, nil)。
func (r *InviteRepo) GetInviterID(ctx context.Context, userID uint64) (uint64, error) {
	var rel model.UserInviteRelation
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		First(&rel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return rel.InviterID, nil
}
