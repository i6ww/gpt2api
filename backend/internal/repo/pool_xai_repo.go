package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// PoolXAIRepo 官方 xAI API 号池仓储。
type PoolXAIRepo struct{ db *gorm.DB }

// NewPoolXAIRepo 构造。
func NewPoolXAIRepo(db *gorm.DB) *PoolXAIRepo { return &PoolXAIRepo{db: db} }

// PoolXAIFilter 列表过滤。
type PoolXAIFilter struct {
	Status      string
	AccountType string
	Keyword     string
	Page        int
	PageSize    int
}

// List 分页列表。
func (r *PoolXAIRepo) List(ctx context.Context, f PoolXAIFilter) ([]*model.PoolXAI, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.PoolXAI{}).Where("deleted_at IS NULL")
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.AccountType != "" {
		q = q.Where("account_type = ?", f.AccountType)
	}
	if f.Keyword != "" {
		q = q.Where("email LIKE ?", "%"+f.Keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.PoolXAI
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Stats 状态分布。
func (r *PoolXAIRepo) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.WithContext(ctx).Model(&model.PoolXAI{}).
		Where("deleted_at IS NULL").
		Select("status, COUNT(*) AS n").Group("status").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{
		"total": 0, "valid": 0, "invalid": 0, "disabled": 0, "cooldown": 0,
	}
	for rows.Next() {
		var s string
		var n int64
		if e := rows.Scan(&s, &n); e != nil {
			return nil, e
		}
		out[s] = n
		out["total"] += n
	}
	return out, nil
}

// GetByID 主键查询（未软删）。
func (r *PoolXAIRepo) GetByID(ctx context.Context, id uint64) (*model.PoolXAI, error) {
	var m model.PoolXAI
	if err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&m).Error; err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

// Create 新增。
func (r *PoolXAIRepo) Create(ctx context.Context, p *model.PoolXAI) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// UpsertMany 按 email upsert。
func (r *PoolXAIRepo) UpsertMany(ctx context.Context, items []*model.PoolXAI) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"subject", "credential_enc", "refresh_token_enc", "id_token_enc",
			"token_endpoint", "base_url", "account_type", "status",
			"expires_at", "deleted_at", "updated_at",
		}),
	}).Create(&items)
	return tx.RowsAffected, tx.Error
}

// Update 部分字段更新。
func (r *PoolXAIRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolXAI{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete 永久删除一行。
func (r *PoolXAIRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Unscoped().
		Where("id = ?", id).Delete(&model.PoolXAI{}).Error
}

// SoftDeleteByIDs 批量永久删除。
func (r *PoolXAIRepo) SoftDeleteByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Unscoped().
		Where("id IN ?", ids).
		Delete(&model.PoolXAI{})
	return tx.RowsAffected, tx.Error
}

// AvailableForGateway 拿当前可用于 gateway 调度的号。
//
// 条件：未软删 + status=valid + (cooldown 已过/为空) + (access_token 还在有效期内) +
// 有 credential_enc。
func (r *PoolXAIRepo) AvailableForGateway(ctx context.Context) ([]*model.PoolXAI, error) {
	var items []*model.PoolXAI
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status = ?", model.XAIStatusValid).
		Where("cooldown_until IS NULL OR cooldown_until <= ?", now).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Where("LENGTH(credential_enc) > 0").
		Order("last_used_at IS NULL DESC, last_used_at ASC, id ASC").
		Find(&items).Error
	return items, err
}

// MarkGatewayUsed gateway 调度成功回写。
func (r *PoolXAIRepo) MarkGatewayUsed(ctx context.Context, id uint64) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&model.PoolXAI{}).
		Where("id = ?", id).Updates(map[string]any{
		"last_used_at":   now,
		"success_count":  gorm.Expr("success_count + 1"),
		"failure_count":  0,
		"status":         model.XAIStatusValid,
		"cooldown_until": nil,
		"error_message":  nil,
	}).Error
}

// MarkGatewayFailed gateway 调度失败 / 熔断回写。
func (r *PoolXAIRepo) MarkGatewayFailed(ctx context.Context, id uint64, reason string, cooldown time.Duration) error {
	now := time.Now().UTC()
	fields := map[string]any{
		"failure_count": gorm.Expr("failure_count + 1"),
		"error_message": reason,
	}
	if cooldown > 0 {
		fields["cooldown_until"] = now.Add(cooldown)
		fields["status"] = model.XAIStatusCooldown
	} else {
		fields["cooldown_until"] = nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolXAI{}).
		Where("id = ?", id).Updates(fields).Error
}

// MarkGatewayInvalid 把账号标记为 token 永久失效（终态，不自动复活）。
func (r *PoolXAIRepo) MarkGatewayInvalid(ctx context.Context, id uint64, reason string) error {
	return r.db.WithContext(ctx).Model(&model.PoolXAI{}).
		Where("id = ?", id).Updates(map[string]any{
		"status":         model.XAIStatusInvalid,
		"error_message":  reason,
		"cooldown_until": nil,
	}).Error
}

// PoolXAIPurgeFilter 批量删除过滤条件。
type PoolXAIPurgeFilter struct {
	All      bool
	Status   string
	Abnormal bool // invalid + disabled
}

// Purge 按条件批量删除。无任何条件且 All=false 时拒绝执行。
func (r *PoolXAIRepo) Purge(ctx context.Context, f PoolXAIPurgeFilter) (int64, error) {
	q := r.db.WithContext(ctx).Model(&model.PoolXAI{}).Where("deleted_at IS NULL")
	hasFilter := false
	if !f.All {
		if f.Status != "" {
			q = q.Where("status = ?", f.Status)
			hasFilter = true
		}
		if f.Abnormal {
			q = q.Where("status IN ?", []string{model.XAIStatusInvalid, model.XAIStatusDisabled})
			hasFilter = true
		}
		if !hasFilter {
			return 0, nil
		}
	}
	tx := q.Unscoped().Delete(&model.PoolXAI{})
	return tx.RowsAffected, tx.Error
}

// PoolXAIRefreshScope 批量刷新过滤。
type PoolXAIRefreshScope string

const (
	XAIRefreshScopeAll      PoolXAIRefreshScope = "all"
	XAIRefreshScopeExpiring PoolXAIRefreshScope = "expiring"
	XAIRefreshScopeAbnormal PoolXAIRefreshScope = "abnormal"
)

// ListForRefresh 按 scope 列出需要刷新的账号（有 refresh_token 才能刷）。
func (r *PoolXAIRepo) ListForRefresh(ctx context.Context, scope PoolXAIRefreshScope, limit int) ([]*model.PoolXAI, error) {
	if limit <= 0 {
		limit = 200
	}
	q := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("refresh_enabled = 1").
		Where("LENGTH(refresh_token_enc) > 0")
	switch scope {
	case XAIRefreshScopeExpiring:
		threshold := time.Now().UTC().Add(15 * time.Minute)
		q = q.Where("status = ?", model.XAIStatusValid).
			Where("expires_at IS NOT NULL AND expires_at < ?", threshold)
	case XAIRefreshScopeAbnormal:
		q = q.Where("status IN ? OR failure_count > 0",
			[]string{model.XAIStatusCooldown})
	case XAIRefreshScopeAll:
		// no-op
	}
	var items []*model.PoolXAI
	if err := q.Order("id ASC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}
