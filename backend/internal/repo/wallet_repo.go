// Package repo 钱包流水仓储。
package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// WalletRepo 钱包 / 流水仓储。
type WalletRepo struct{ db *gorm.DB }

// NewWalletRepo 构造。
func NewWalletRepo(db *gorm.DB) *WalletRepo { return &WalletRepo{db: db} }

// ErrInsufficient 余额不足。
var ErrInsufficient = errors.New("repo: insufficient points")

// Income 在事务中给用户加点 + 写入流水。
func (r *WalletRepo) Income(ctx context.Context, userID uint64, biz, bizID string, points int64, remark string) (*model.WalletLog, error) {
	if points <= 0 {
		return nil, errors.New("income points must >0")
	}
	var log *model.WalletLog
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		u, err := lockUser(tx, userID)
		if err != nil {
			return err
		}
		before := u.Points
		after := before + points
		if err := tx.Model(&model.User{}).Where("id = ?", userID).
			UpdateColumn("points", after).Error; err != nil {
			return err
		}
		log = &model.WalletLog{
			UserID:       userID,
			Direction:    1,
			BizType:      biz,
			BizID:        bizID,
			Points:       points,
			PointsBefore: before,
			PointsAfter:  after,
		}
		if remark != "" {
			log.Remark = &remark
		}
		return tx.Create(log).Error
	})
	return log, err
}

// Freeze 预冻结：从 points 扣，写入 frozen_points + wallet_log（dir=-1 / status=frozen 由 service 控制）。
//
// 失败原因：
//   - ErrInsufficient: 余额不足
//
// Adjust changes available points and writes a wallet log. Positive points add balance,
// negative points deduct balance. When addTotalRecharge is true, positive points are
// also accumulated into user.total_recharge.
func (r *WalletRepo) Adjust(ctx context.Context, userID uint64, biz, bizID string, points int64, remark string, addTotalRecharge bool) (*model.WalletLog, error) {
	if points == 0 {
		return nil, errors.New("adjust points must not be 0")
	}
	var log *model.WalletLog
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		u, err := lockUser(tx, userID)
		if err != nil {
			return err
		}
		before := u.Points
		after := before + points
		if after < 0 {
			return ErrInsufficient
		}
		updates := map[string]any{"points": after}
		if addTotalRecharge && points > 0 {
			updates["total_recharge"] = gorm.Expr("total_recharge + ?", points)
		}
		if err := tx.Model(&model.User{}).Where("id = ?", userID).UpdateColumns(updates).Error; err != nil {
			return err
		}
		direction := int8(1)
		if points < 0 {
			direction = -1
		}
		log = &model.WalletLog{
			UserID:       userID,
			Direction:    direction,
			BizType:      biz,
			BizID:        bizID,
			Points:       points,
			PointsBefore: before,
			PointsAfter:  after,
		}
		if remark != "" {
			log.Remark = &remark
		}
		return tx.Create(log).Error
	})
	return log, err
}

func (r *WalletRepo) Freeze(ctx context.Context, userID uint64, biz, bizID string, points int64, remark string) (*model.WalletLog, error) {
	if points <= 0 {
		return nil, errors.New("freeze points must >0")
	}
	var log *model.WalletLog
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		u, err := lockUser(tx, userID)
		if err != nil {
			return err
		}
		if u.Points < points {
			return ErrInsufficient
		}
		before := u.Points
		after := before - points
		if err := tx.Model(&model.User{}).Where("id = ?", userID).
			UpdateColumns(map[string]any{
				"points":        after,
				"frozen_points": gorm.Expr("frozen_points + ?", points),
			}).Error; err != nil {
			return err
		}
		log = &model.WalletLog{
			UserID:       userID,
			Direction:    -1,
			BizType:      biz,
			BizID:        bizID,
			Points:       -points,
			PointsBefore: before,
			PointsAfter:  after,
		}
		if remark != "" {
			log.Remark = &remark
		}
		return tx.Create(log).Error
	})
	return log, err
}

// Settle 结算：将之前 freeze 的 points 从 frozen_points 中清掉（落地为消费）。
func (r *WalletRepo) Settle(ctx context.Context, userID uint64, points int64) error {
	if points <= 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		u, err := lockUser(tx, userID)
		if err != nil {
			return err
		}
		newFrozen := u.FrozenPoints - points
		if newFrozen < 0 {
			newFrozen = 0
		}
		return tx.Model(&model.User{}).Where("id = ?", userID).
			Update("frozen_points", newFrozen).Error
	})
}

// RefundFrozenPart returns part of frozen points to available balance.
func (r *WalletRepo) RefundFrozenPart(ctx context.Context, userID uint64, taskID, reason string, points int64) error {
	if points <= 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		u, err := lockUser(tx, userID)
		if err != nil {
			return err
		}
		if points > u.FrozenPoints {
			points = u.FrozenPoints
		}
		before := u.Points
		after := before + points
		newFrozen := u.FrozenPoints - points
		if err := tx.Model(&model.User{}).Where("id = ?", userID).
			UpdateColumns(map[string]any{
				"points":        after,
				"frozen_points": newFrozen,
			}).Error; err != nil {
			return err
		}
		remarkCopy := reason
		log := &model.WalletLog{
			UserID:       userID,
			Direction:    1,
			BizType:      model.BizRefund,
			BizID:        taskID,
			Points:       points,
			PointsBefore: before,
			PointsAfter:  after,
			Remark:       &remarkCopy,
		}
		if err := tx.Create(log).Error; err != nil {
			return err
		}
		return tx.Create(&model.RefundRecord{
			TaskID:    taskID,
			UserID:    userID,
			Points:    points,
			Reason:    reason,
			Operator:  "system",
			CreatedAt: time.Now().UTC(),
		}).Error
	})
}

// Refund 退款：把 freeze 的 points 还回 points + 写入 wallet_log + 写 refund_record。
func (r *WalletRepo) Refund(ctx context.Context, userID uint64, taskID, reason string, points int64) error {
	if points <= 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		u, err := lockUser(tx, userID)
		if err != nil {
			return err
		}
		newFrozen := u.FrozenPoints - points
		if newFrozen < 0 {
			newFrozen = 0
		}
		before := u.Points
		after := before + points
		if err := tx.Model(&model.User{}).Where("id = ?", userID).
			UpdateColumns(map[string]any{
				"points":        after,
				"frozen_points": newFrozen,
			}).Error; err != nil {
			return err
		}
		remarkCopy := reason
		log := &model.WalletLog{
			UserID:       userID,
			Direction:    1,
			BizType:      model.BizRefund,
			BizID:        taskID,
			Points:       points,
			PointsBefore: before,
			PointsAfter:  after,
			Remark:       &remarkCopy,
		}
		if err := tx.Create(log).Error; err != nil {
			return err
		}
		return tx.Create(&model.RefundRecord{
			TaskID:    taskID,
			UserID:    userID,
			Points:    points,
			Reason:    reason,
			Operator:  "system",
			CreatedAt: time.Now().UTC(),
		}).Error
	})
}

// ListUserLogs 钱包流水分页。
func (r *WalletRepo) ListUserLogs(ctx context.Context, userID uint64, page, pageSize int) ([]*model.WalletLog, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.WalletLog{}).Where("user_id = ?", userID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.WalletLog
	err := q.Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&items).Error
	return items, total, err
}

type AdminWalletLogFilter struct {
	Keyword   string
	UserID    uint64
	BizType   string
	Direction *int
	Page      int
	PageSize  int
}

type AdminWalletLogRow struct {
	model.WalletLog
	UserLabel string `gorm:"column:user_label"`
}

func (r *WalletRepo) ListAdminLogs(ctx context.Context, f AdminWalletLogFilter) ([]*AdminWalletLogRow, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}
	q := r.db.WithContext(ctx).Table("wallet_log wl").
		Joins("LEFT JOIN user u ON u.id = wl.user_id")
	if f.UserID > 0 {
		q = q.Where("wl.user_id = ?", f.UserID)
	}
	if f.Direction != nil {
		q = q.Where("wl.direction = ?", *f.Direction)
	}
	if bt := strings.TrimSpace(f.BizType); bt != "" {
		q = q.Where("wl.biz_type = ?", bt)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("CAST(wl.id AS CHAR) = ? OR CAST(wl.user_id AS CHAR) = ? OR wl.biz_id LIKE ? OR wl.remark LIKE ? OR u.email LIKE ? OR u.phone LIKE ? OR u.username LIKE ?",
			kw, kw, like, like, like, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*AdminWalletLogRow
	err := q.Select("wl.*, COALESCE(NULLIF(u.email, ''), NULLIF(u.phone, ''), NULLIF(u.username, ''), u.uuid, CAST(wl.user_id AS CHAR)) AS user_label").
		Order("wl.id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Scan(&rows).Error
	return rows, total, err
}

// AdminWalletSummary 「充值消费记录」全局汇总。
// 注意：recharge 实际包含管理员手动加点 + cdk 等所有正向积分；这里口径与 wallet_log.biz_type 对齐：
//   - recharge      正向，管理员充值（含创建时赠送）
//   - cdk           正向，兑换码
//   - promo         正向，优惠码兑换/邀请奖励等
//   - invite_reward 正向，邀请奖励
//   - gift          正向，运营赠送
//   - refund        正向，调用失败退款
//   - consume       负向，生成扣费
//   - admin_deduct  负向，管理员扣减
//
// 汇总卡片把这些归到 3 个口径：recharge（除 refund 外的所有正向）/ consume（所有负向）/ refund。
type AdminWalletSummary struct {
	RechargeToday int64
	RechargeTotal int64
	ConsumeToday  int64
	ConsumeTotal  int64
	RefundToday   int64
	RefundTotal   int64
	RecordsToday  int64
	RecordsTotal  int64
	UsersTouched  int64
}

func (r *WalletRepo) AdminSummary(ctx context.Context) (*AdminWalletSummary, error) {
	now := time.Now()
	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endToday := startToday.Add(24 * time.Hour)

	// 一条 SQL 算完，避免 7 个 round-trip。CASE 内用 ABS(points)，方向已经在 biz_type
	// 之外用 direction 做兜底（极端情况：某 biz_type 也写过负向值）。
	const sumSQL = `SELECT
  COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? AND direction > 0 AND biz_type <> 'refund' THEN ABS(points) ELSE 0 END), 0) AS recharge_today,
  COALESCE(SUM(CASE WHEN direction > 0 AND biz_type <> 'refund' THEN ABS(points) ELSE 0 END), 0) AS recharge_total,
  COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? AND direction < 0 THEN ABS(points) ELSE 0 END), 0) AS consume_today,
  COALESCE(SUM(CASE WHEN direction < 0 THEN ABS(points) ELSE 0 END), 0) AS consume_total,
  COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? AND biz_type = 'refund' THEN ABS(points) ELSE 0 END), 0) AS refund_today,
  COALESCE(SUM(CASE WHEN biz_type = 'refund' THEN ABS(points) ELSE 0 END), 0) AS refund_total,
  COUNT(CASE WHEN created_at >= ? AND created_at < ? THEN 1 END) AS records_today,
  COUNT(1) AS records_total,
  COUNT(DISTINCT user_id) AS users_touched
FROM wallet_log`

	var s AdminWalletSummary
	if err := r.db.WithContext(ctx).Raw(
		sumSQL,
		startToday, endToday,
		startToday, endToday,
		startToday, endToday,
		startToday, endToday,
	).Scan(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// === helpers ===

// lockUser SELECT ... FOR UPDATE。
func lockUser(tx *gorm.DB, userID uint64) (*model.User, error) {
	var u model.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", userID).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
