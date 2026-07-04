package repo

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// GopayWalletPoolRepo GoPay 钱包池仓储。
type GopayWalletPoolRepo struct{ db *gorm.DB }

// NewGopayWalletPoolRepo 构造。
func NewGopayWalletPoolRepo(db *gorm.DB) *GopayWalletPoolRepo {
	return &GopayWalletPoolRepo{db: db}
}

// GopayWalletFilter 列表过滤。
type GopayWalletFilter struct {
	Status         string
	CloudPhoneID   string
	Keyword        string
	Page           int
	PageSize       int
	HasAvailableOn bool // true=只取还能再开 Plus 的（active_plus_count < quota）
	Quota          int  // 与 HasAvailableOn 配合
}

// List 分页列表。
func (r *GopayWalletPoolRepo) List(ctx context.Context, f GopayWalletFilter) ([]*model.GopayWalletPool, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 10000 {
		f.PageSize = 50
	}
	q := r.db.WithContext(ctx).Model(&model.GopayWalletPool{}).Where("deleted_at IS NULL")
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.CloudPhoneID != "" {
		q = q.Where("cloud_phone_id = ?", f.CloudPhoneID)
	}
	if f.Keyword != "" {
		// 钱包侧自身没手机号字段了；keyword 命中：备注 / cloud_phone_id 直接匹配，
		// 或 join cloud_phone 拿 phone_number 命中。
		k := "%" + f.Keyword + "%"
		q = q.Where(
			"(remark LIKE ? OR cloud_phone_id LIKE ? OR cloud_phone_id IN (SELECT id FROM cloud_phone_pool WHERE phone_number LIKE ? OR name LIKE ?))",
			k, k, k, k,
		)
	}
	if f.HasAvailableOn && f.Quota > 0 {
		q = q.Where("active_plus_count < ?", f.Quota)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.GopayWalletPool
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetByID 主键查询（未软删）。
func (r *GopayWalletPoolRepo) GetByID(ctx context.Context, id uint64) (*model.GopayWalletPool, error) {
	var p model.GopayWalletPool
	err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&p).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

// Create 新建。
func (r *GopayWalletPoolRepo) Create(ctx context.Context, p *model.GopayWalletPool) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// Update 部分字段更新。
func (r *GopayWalletPoolRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.GopayWalletPool{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete 软删除。
func (r *GopayWalletPoolRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.GopayWalletPool{}).
		Where("id = ?", id).Update("deleted_at", time.Now().UTC()).Error
}

// SoftDeleteByIDs 批量软删除。
func (r *GopayWalletPoolRepo) SoftDeleteByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Model(&model.GopayWalletPool{}).
		Where("id IN ? AND deleted_at IS NULL", ids).
		Update("deleted_at", time.Now().UTC())
	return tx.RowsAffected, tx.Error
}

// Stats 状态统计（不含软删）。
func (r *GopayWalletPoolRepo) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.WithContext(ctx).Model(&model.GopayWalletPool{}).
		Where("deleted_at IS NULL").
		Select("status, COUNT(*) AS n").
		Group("status").
		Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{
		"total":     0,
		"available": 0,
		"leased":    0,
		"cooldown":  0,
		"banned":    0,
		"exhausted": 0,
		"disabled":  0,
	}
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
		out["total"] += n
	}
	return out, nil
}

// LeaseAvailable 抢一个可用钱包（事务 + FOR UPDATE SKIP LOCKED 防撞）。
//
// 优先策略：active_plus_count 升序（让所有钱包流量分散）→ last_used_at 升序（优先冷的）。
// 排除：cooldown 未到期、active_plus_count >= quota、status != available。
//
// 抢到后立即把 status 置为 leased，避免同一钱包被多个 dispatcher 同时占用。
// 调用方拿到后必须最终调 Release / MarkSuccess / MarkFailed 之一。
func (r *GopayWalletPoolRepo) LeaseAvailable(ctx context.Context, perWalletQuota int) (*model.GopayWalletPool, error) {
	if perWalletQuota <= 0 {
		perWalletQuota = 30
	}
	var picked model.GopayWalletPool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND deleted_at IS NULL", model.GopayWalletStatusAvailable).
			Where("active_plus_count < ?", perWalletQuota).
			Where("(cooldown_until IS NULL OR cooldown_until <= ?)", now).
			Order("active_plus_count ASC, COALESCE(last_used_at, '1970-01-01') ASC").
			First(&picked).Error
		if err != nil {
			return err
		}
		return tx.Model(&model.GopayWalletPool{}).
			Where("id = ?", picked.ID).
			Updates(map[string]any{
				"status":       model.GopayWalletStatusLeased,
				"last_used_at": now,
			}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	picked.Status = model.GopayWalletStatusLeased
	return &picked, nil
}

// Release 把租用的钱包放回 available（不动 active_plus_count，用于失败回滚或主动释放）。
func (r *GopayWalletPoolRepo) Release(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.GopayWalletPool{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status": model.GopayWalletStatusAvailable,
		}).Error
}

// MarkSuccess 成功开 Plus：active_plus_count++，total_success++，转回 available；
// 若 active_plus_count 已达 quota → exhausted。
func (r *GopayWalletPoolRepo) MarkSuccess(ctx context.Context, id uint64, perWalletQuota int) error {
	if perWalletQuota <= 0 {
		perWalletQuota = 30
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var w model.GopayWalletPool
		if err := tx.First(&w, id).Error; err != nil {
			return err
		}
		w.ActivePlusCount++
		w.TotalSuccess++
		updates := map[string]any{
			"active_plus_count": w.ActivePlusCount,
			"total_success":     w.TotalSuccess,
			"last_used_at":      time.Now().UTC(),
			"last_error":        nil,
			"cooldown_until":    nil,
		}
		if w.ActivePlusCount >= perWalletQuota {
			updates["status"] = model.GopayWalletStatusExhausted
		} else {
			updates["status"] = model.GopayWalletStatusAvailable
		}
		return tx.Model(&model.GopayWalletPool{}).
			Where("id = ?", id).Updates(updates).Error
	})
}

// MarkFailed 失败：total_failed++，转 cooldown，到 cooldown_until 到期后自动可用。
// 当 reason 包含"banned"/"风控" 等永久错误时调用方应改调 MarkBanned。
func (r *GopayWalletPoolRepo) MarkFailed(ctx context.Context, id uint64, reason string, cooldownMin int) error {
	if cooldownMin <= 0 {
		cooldownMin = 60
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var w model.GopayWalletPool
		if err := tx.First(&w, id).Error; err != nil {
			return err
		}
		w.TotalFailed++
		now := time.Now().UTC()
		until := now.Add(time.Duration(cooldownMin) * time.Minute)
		updates := map[string]any{
			"total_failed":   w.TotalFailed,
			"last_used_at":   now,
			"last_error":     truncate(reason, 240),
			"status":         model.GopayWalletStatusCooldown,
			"cooldown_until": until,
		}
		return tx.Model(&model.GopayWalletPool{}).
			Where("id = ?", id).Updates(updates).Error
	})
}

// MarkBanned 钱包永久禁用（PIN 错 / 账户被 GoPay ban / 风控严重）。
func (r *GopayWalletPoolRepo) MarkBanned(ctx context.Context, id uint64, reason string) error {
	return r.db.WithContext(ctx).Model(&model.GopayWalletPool{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     model.GopayWalletStatusBanned,
			"last_error": truncate(reason, 240),
		}).Error
}

// DecActivePlusCount 取消订阅时 active_plus_count - 1，并把 exhausted 转回 available。
func (r *GopayWalletPoolRepo) DecActivePlusCount(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var w model.GopayWalletPool
		if err := tx.First(&w, id).Error; err != nil {
			return err
		}
		if w.ActivePlusCount > 0 {
			w.ActivePlusCount--
		}
		updates := map[string]any{
			"active_plus_count": w.ActivePlusCount,
		}
		if w.Status == model.GopayWalletStatusExhausted {
			updates["status"] = model.GopayWalletStatusAvailable
		}
		return tx.Model(&model.GopayWalletPool{}).
			Where("id = ?", id).Updates(updates).Error
	})
}
