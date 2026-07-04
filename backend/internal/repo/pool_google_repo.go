package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// PoolGoogleRepo FlowMusic（歌曲）Google 号池仓储。镜像 PoolAdobeRepo。
type PoolGoogleRepo struct{ db *gorm.DB }

// NewPoolGoogleRepo 构造。
func NewPoolGoogleRepo(db *gorm.DB) *PoolGoogleRepo { return &PoolGoogleRepo{db: db} }

// PoolGoogleFilter 列表过滤。
type PoolGoogleFilter struct {
	Status   string
	Source   string
	Keyword  string
	Page     int
	PageSize int
}

// List 分页列表。
func (r *PoolGoogleRepo) List(ctx context.Context, f PoolGoogleFilter) ([]*model.PoolGoogle, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.PoolGoogle{}).Where("deleted_at IS NULL")
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Source != "" {
		q = q.Where("source = ?", f.Source)
	}
	if f.Keyword != "" {
		k := "%" + f.Keyword + "%"
		q = q.Where("(email LIKE ? OR display_name LIKE ?)", k, k)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.PoolGoogle
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Stats 状态分布。
func (r *PoolGoogleRepo) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.WithContext(ctx).Model(&model.PoolGoogle{}).
		Where("deleted_at IS NULL").
		Select("status, COUNT(*) AS n").Group("status").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{"total": 0, "valid": 0, "invalid": 0, "disabled": 0, "cooldown": 0}
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
func (r *PoolGoogleRepo) GetByID(ctx context.Context, id uint64) (*model.PoolGoogle, error) {
	var m model.PoolGoogle
	if err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&m).Error; err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

// Create 新增。
func (r *PoolGoogleRepo) Create(ctx context.Context, p *model.PoolGoogle) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// UpsertMany 按 email upsert。
func (r *PoolGoogleRepo) UpsertMany(ctx context.Context, items []*model.PoolGoogle) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"display_name", "credential_enc", "protocol_mode", "status",
			"expires_at", "deleted_at", "updated_at",
		}),
	}).Create(&items)
	return tx.RowsAffected, tx.Error
}

// Update 部分字段更新。
func (r *PoolGoogleRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolGoogle{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete deletes the account row permanently.
func (r *PoolGoogleRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Unscoped().
		Where("id = ?", id).Delete(&model.PoolGoogle{}).Error
}

// SoftDeleteByIDs permanently deletes account rows.
func (r *PoolGoogleRepo) SoftDeleteByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Unscoped().
		Where("id IN ?", ids).
		Delete(&model.PoolGoogle{})
	return tx.RowsAffected, tx.Error
}

// ListExpiringSoon 列出即将过期、需要续期的账号（后台续期调度器用）。
//
// 过滤：status IN (valid, cooldown) + refresh_enabled=1 + credential 非空 +
// 未在退避中 + (expires_at 为空或 < now+within)。
func (r *PoolGoogleRepo) ListExpiringSoon(ctx context.Context, within time.Duration, limit int) ([]*model.PoolGoogle, error) {
	if limit <= 0 {
		limit = 50
	}
	now := time.Now().UTC()
	threshold := now.Add(within)
	var items []*model.PoolGoogle
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status IN ?", []string{model.GoogleStatusValid, model.GoogleStatusCooldown}).
		Where("refresh_enabled = 1").
		Where("LENGTH(credential_enc) > 0").
		Where("cooldown_until IS NULL OR cooldown_until <= ?", now).
		Where("expires_at IS NULL OR expires_at < ?", threshold).
		Order("expires_at ASC").
		Limit(limit).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// PoolGoogleRefreshScope 后台批量刷新过滤条件。
type PoolGoogleRefreshScope string

const (
	GoogleRefreshScopeAll      PoolGoogleRefreshScope = "all"
	GoogleRefreshScopeAbnormal PoolGoogleRefreshScope = "abnormal"
	GoogleRefreshScopeExpiring PoolGoogleRefreshScope = "expiring"
)

// ListForRefresh 按 scope 列出需要刷新的账号。
func (r *PoolGoogleRepo) ListForRefresh(ctx context.Context, scope PoolGoogleRefreshScope, limit int) ([]*model.PoolGoogle, error) {
	if limit <= 0 {
		limit = 500
	}
	q := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("LENGTH(credential_enc) > 0")
	switch scope {
	case GoogleRefreshScopeAbnormal:
		q = q.Where("(status IN ? OR failure_count > 0)",
			[]string{model.GoogleStatusInvalid, model.GoogleStatusCooldown})
	case GoogleRefreshScopeExpiring:
		threshold := time.Now().UTC().Add(12 * time.Hour)
		q = q.Where("expires_at IS NULL OR expires_at < ?", threshold).
			Where("status IN ?", []string{model.GoogleStatusValid, model.GoogleStatusCooldown})
	case GoogleRefreshScopeAll:
	default:
	}
	var items []*model.PoolGoogle
	if err := q.Order("id ASC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListForExport 批量导出。
func (r *PoolGoogleRepo) ListForExport(ctx context.Context, status string, max int) ([]*model.PoolGoogle, error) {
	if max <= 0 || max > 20000 {
		max = 20000
	}
	q := r.db.WithContext(ctx).Where("deleted_at IS NULL")
	if status != "" && status != "all" {
		q = q.Where("status = ?", status)
	}
	var items []*model.PoolGoogle
	if err := q.Order("id ASC").Limit(max).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// AvailableForGateway 拿当前可用于 gateway 调度的歌曲号。
//
// 条件：未软删 + status=valid + (cooldown_until 空或过期) +
// (expires_at 空或还在有效期内) + credential 非空。
// 不强制 credits>0：部分账号 credits 字段未维护，避免误过滤导致"无可用账号"。
func (r *PoolGoogleRepo) AvailableForGateway(ctx context.Context) ([]*model.PoolGoogle, error) {
	var items []*model.PoolGoogle
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status = ?", model.GoogleStatusValid).
		Where("cooldown_until IS NULL OR cooldown_until <= ?", now).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Where("LENGTH(credential_enc) > 0").
		Order("last_used_at IS NULL DESC, last_used_at ASC, id ASC").
		Find(&items).Error
	return items, err
}

// MarkGatewayUsed gateway 调度成功回写。
func (r *PoolGoogleRepo) MarkGatewayUsed(ctx context.Context, id uint64) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&model.PoolGoogle{}).
		Where("id = ?", id).Updates(map[string]any{
		"last_used_at":   now,
		"failure_count":  0,
		"status":         model.GoogleStatusValid,
		"cooldown_until": nil,
		"error_message":  nil,
	}).Error
}

// MarkGatewayFailed gateway 调度失败 / 熔断回写。
func (r *PoolGoogleRepo) MarkGatewayFailed(ctx context.Context, id uint64, reason string, cooldown time.Duration) error {
	now := time.Now().UTC()
	fields := map[string]any{
		"failure_count": gorm.Expr("failure_count + 1"),
		"error_message": reason,
	}
	if cooldown > 0 {
		until := now.Add(cooldown)
		fields["cooldown_until"] = until
		fields["status"] = model.GoogleStatusCooldown
	} else {
		fields["cooldown_until"] = nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolGoogle{}).
		Where("id = ?", id).Updates(fields).Error
}

// MarkGatewayInvalid 生成时遭遇干净的 401/403 → 直接置 invalid 终态（不自动复活）。
func (r *PoolGoogleRepo) MarkGatewayInvalid(ctx context.Context, id uint64, reason string) error {
	return r.db.WithContext(ctx).Model(&model.PoolGoogle{}).
		Where("id = ?", id).Updates(map[string]any{
		"status":         model.GoogleStatusInvalid,
		"error_message":  reason,
		"failure_count":  gorm.Expr("failure_count + 1"),
		"cooldown_until": nil,
	}).Error
}
