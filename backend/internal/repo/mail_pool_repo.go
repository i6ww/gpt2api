package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// MailPoolRepo 共享邮箱池仓储。
type MailPoolRepo struct{ db *gorm.DB }

// NewMailPoolRepo 构造。
func NewMailPoolRepo(db *gorm.DB) *MailPoolRepo { return &MailPoolRepo{db: db} }

// MailPoolFilter 列表过滤。
type MailPoolFilter struct {
	Status   string
	Mode     string
	Keyword  string
	Page     int
	PageSize int
}

// List 分页列表。
func (r *MailPoolRepo) List(ctx context.Context, f MailPoolFilter) ([]*model.MailPool, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 50
	}
	q := r.db.WithContext(ctx).Model(&model.MailPool{}).Where("deleted_at IS NULL")
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Mode != "" {
		q = q.Where("mode = ?", f.Mode)
	}
	if f.Keyword != "" {
		q = q.Where("email LIKE ?", "%"+f.Keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.MailPool
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Stats 邮箱池状态统计。
func (r *MailPoolRepo) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.WithContext(ctx).Model(&model.MailPool{}).
		Where("deleted_at IS NULL").
		Select("status, COUNT(*) AS n").
		Group("status").
		Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{
		"total":      0,
		"available":  0,
		"in_use":     0,
		"registered": 0,
		"failed":     0,
		"disabled":   0,
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

// GetByID 主键查询（未软删）。
func (r *MailPoolRepo) GetByID(ctx context.Context, id uint64) (*model.MailPool, error) {
	var m model.MailPool
	err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&m).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

// UpsertMany 按 email 唯一键 upsert 一批。返回成功 upsert 数。
func (r *MailPoolRepo) UpsertMany(ctx context.Context, items []*model.MailPool) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"password_enc", "client_id", "refresh_token_enc", "mode", "updated_at",
		}),
	}).Create(&items)
	return tx.RowsAffected, tx.Error
}

// Update 部分字段更新。
func (r *MailPoolRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.MailPool{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete 软删除。
func (r *MailPoolRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.MailPool{}).
		Where("id = ?", id).Update("deleted_at", time.Now().UTC()).Error
}

// SoftDeleteByIDs 批量软删除。
func (r *MailPoolRepo) SoftDeleteByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Model(&model.MailPool{}).
		Where("id IN ? AND deleted_at IS NULL", ids).
		Update("deleted_at", time.Now().UTC())
	return tx.RowsAffected, tx.Error
}

// SoftDeleteByStatus 按状态批量软删除（典型用法：清理 failed）。返回行数。
func (r *MailPoolRepo) SoftDeleteByStatus(ctx context.Context, status string) (int64, error) {
	tx := r.db.WithContext(ctx).Model(&model.MailPool{}).
		Where("status = ? AND deleted_at IS NULL", status).
		Update("deleted_at", time.Now().UTC())
	return tx.RowsAffected, tx.Error
}

// SoftDeleteByFilter 按 status / mode / keyword 三元组软删全部匹配的行；filter
// 所有字段都为空时等价于"清空整张表"。返回受影响行数。
func (r *MailPoolRepo) SoftDeleteByFilter(ctx context.Context, f MailPoolFilter) (int64, error) {
	q := r.db.WithContext(ctx).Model(&model.MailPool{}).Where("deleted_at IS NULL")
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Mode != "" {
		q = q.Where("mode = ?", f.Mode)
	}
	if f.Keyword != "" {
		q = q.Where("email LIKE ?", "%"+f.Keyword+"%")
	}
	tx := q.Update("deleted_at", time.Now().UTC())
	return tx.RowsAffected, tx.Error
}

// ResetByIDs 批量重置为 available（清理 failure_count + last_error）。
func (r *MailPoolRepo) ResetByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Model(&model.MailPool{}).
		Where("id IN ? AND deleted_at IS NULL", ids).
		Updates(map[string]any{
			"status":             model.MailStatusAvailable,
			"failure_count":      0,
			"last_error":         nil,
			"used_by_provider":   nil,
			"used_by_account_id": nil,
			"used_at":            nil,
		})
	return tx.RowsAffected, tx.Error
}

// Acquire 原子领取一条 available 邮箱，标记为 in_use。
// 用 SELECT ... FOR UPDATE 加行锁，避免并发 worker 抢同一条。
func (r *MailPoolRepo) Acquire(ctx context.Context, provider string) (*model.MailPool, error) {
	var picked *model.MailPool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m model.MailPool
		// 优先选 failure_count 最低的邮箱：
		//
		// 临时邮箱注册失败一次（OTP 拿错、user_register 撞 invalid_auth_step 等）
		// 之后，OpenAI / Grok 那边会把这个邮箱标为"已经在某个 OAuth 草稿"，
		// 第二次再用同一邮箱必然 400。先按 failure_count ASC 排序就能让"全新邮箱"
		// 始终排在前面，老的失败邮箱自然沉底直至 PoolMaxFailure 触发 broken。
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND deleted_at IS NULL", model.MailStatusAvailable).
			Order("failure_count ASC, imported_at ASC").
			First(&m).Error
		if err != nil {
			return mapErr(err)
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"status":           model.MailStatusInUse,
			"used_at":          now,
			"used_by_provider": provider,
		}
		if err := tx.Model(&model.MailPool{}).
			Where("id = ?", m.ID).Updates(updates).Error; err != nil {
			return err
		}
		m.Status = model.MailStatusInUse
		m.UsedAt = &now
		p := provider
		m.UsedByProvider = &p
		picked = &m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return picked, nil
}

// Release 把 in_use 状态归还 available。
func (r *MailPoolRepo) Release(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.MailPool{}).
		Where("id = ? AND status = ?", id, model.MailStatusInUse).
		Updates(map[string]any{
			"status":             model.MailStatusAvailable,
			"used_by_provider":   nil,
			"used_by_account_id": nil,
		}).Error
}

// MarkRegistered 注册成功后标记。
// MarkRegistered 把邮箱状态置 registered。
//
// accountID == 0 表示账号还没落到对应号池（例如 Adobe 已 create_account 但 fromSusi 失败、
// 没拿到 access_token），此时把 used_by_account_id 写 NULL 而不是 0，方便后续 admin
// UI 区分"成功入池"与"已被消费但未入池"。
func (r *MailPoolRepo) MarkRegistered(ctx context.Context, id, accountID uint64) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":        model.MailStatusRegistered,
		"registered_at": now,
	}
	if accountID > 0 {
		updates["used_by_account_id"] = accountID
	} else {
		updates["used_by_account_id"] = nil
	}
	return r.db.WithContext(ctx).Model(&model.MailPool{}).
		Where("id = ?", id).Updates(updates).Error
}

// MarkFailed 失败 +1，达到上限置 failed。
func (r *MailPoolRepo) MarkFailed(ctx context.Context, id uint64, errMsg string, maxFail int) (terminal bool, err error) {
	if maxFail <= 0 {
		maxFail = 3
	}
	if len(errMsg) > 480 {
		errMsg = errMsg[:480]
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m model.MailPool
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", id).First(&m).Error; e != nil {
			return mapErr(e)
		}
		newCount := m.FailureCount + 1
		updates := map[string]any{
			"failure_count": newCount,
			"last_error":    errMsg,
		}
		if newCount >= maxFail {
			updates["status"] = model.MailStatusFailed
			terminal = true
		} else {
			updates["status"] = model.MailStatusAvailable
		}
		return tx.Model(&model.MailPool{}).
			Where("id = ?", id).Updates(updates).Error
	})
	return terminal, err
}
