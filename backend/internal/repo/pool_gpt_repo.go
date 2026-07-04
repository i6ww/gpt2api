package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// PoolGptRepo GPT 号池仓储。
type PoolGptRepo struct{ db *gorm.DB }

// NewPoolGptRepo 构造。
func NewPoolGptRepo(db *gorm.DB) *PoolGptRepo { return &PoolGptRepo{db: db} }

// PoolGptFilter 列表过滤。
//
// PlanType 接受官方档位字符串（"free" / "plus" / "pro" / "team" /
// "enterprise" / "unknown"）以及聚合值 "__unsubscribed"（= free 或
// plan_type 为空 / unknown），用于"哪些号还能升 Plus"快查。
type PoolGptFilter struct {
	Status   string
	PlanType string
	Keyword  string
	Page     int
	PageSize int
}

// List 分页列表。
func (r *PoolGptRepo) List(ctx context.Context, f PoolGptFilter) ([]*model.PoolGpt, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	// PageSize 上限与 DTO 保持一致（10000）；超过则回落 20。原本 cap 在 200
	// 跟 dto 的 max=10000 不一致，导致 frontend 选 1000 时被默默切回 20。
	if f.PageSize <= 0 || f.PageSize > 10000 {
		f.PageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.PoolGpt{}).Where("deleted_at IS NULL")
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	switch f.PlanType {
	case "":
		// 不过滤
	case "__unsubscribed":
		// "未订阅" = free 档 或 未探测过（plan_type 为 NULL / 空 / unknown）。
		// 给用户"哪些号还能升 Plus"一键聚合视图。
		q = q.Where("plan_type IS NULL OR plan_type = '' OR plan_type IN ?", []string{"free", "unknown"})
	default:
		q = q.Where("plan_type = ?", f.PlanType)
	}
	if f.Keyword != "" {
		q = q.Where("email LIKE ?", "%"+f.Keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.PoolGpt
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Stats 状态分布。
func (r *PoolGptRepo) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.WithContext(ctx).Model(&model.PoolGpt{}).
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
func (r *PoolGptRepo) GetByID(ctx context.Context, id uint64) (*model.PoolGpt, error) {
	var m model.PoolGpt
	if err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&m).Error; err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

// Create 新增。
func (r *PoolGptRepo) Create(ctx context.Context, p *model.PoolGpt) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// UpsertMany 按 email upsert。
//
// 凡是导入提供的字段都会覆盖；命中软删除行时会清空 deleted_at，让重新导入的账号恢复显示。
func (r *PoolGptRepo) UpsertMany(ctx context.Context, items []*model.PoolGpt) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"password_enc", "access_token_enc", "refresh_token_enc",
			"id_token_enc", "api_key_enc",
			"oauth_issuer", "oauth_client_id", "status", "expires_at",
			"plan_type", "chatgpt_account_id", "deleted_at", "updated_at",
		}),
	}).Create(&items)
	return tx.RowsAffected, tx.Error
}

// Update 部分字段更新。
func (r *PoolGptRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolGpt{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete deletes the account row permanently.
func (r *PoolGptRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Unscoped().
		Where("id = ?", id).Delete(&model.PoolGpt{}).Error
}

// SoftDeleteByIDs permanently deletes account rows.
func (r *PoolGptRepo) SoftDeleteByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Unscoped().
		Where("id IN ?", ids).
		Delete(&model.PoolGpt{})
	return tx.RowsAffected, tx.Error
}

// PoolGptExportScope 批量导出的过滤范围。
//
//   - all      ：全部未删除
//   - valid    ：status=valid
//   - invalid  ：status=invalid
//   - selected ：仅 ids 列表里的（由 service 层过滤；repo 层提供 ListByIDs）
type PoolGptExportScope string

const (
	GPTExportScopeAll      PoolGptExportScope = "all"
	GPTExportScopeValid    PoolGptExportScope = "valid"
	GPTExportScopeInvalid  PoolGptExportScope = "invalid"
	GPTExportScopeSelected PoolGptExportScope = "selected"
)

// ListForExport 批量导出。无分页（默认上限 max=20000，避免一次拉到 OOM）。
//
// scope=selected 时 ids 必须非空；其它 scope 忽略 ids。
func (r *PoolGptRepo) ListForExport(ctx context.Context, scope PoolGptExportScope, ids []uint64, max int) ([]*model.PoolGpt, error) {
	if max <= 0 || max > 20000 {
		max = 20000
	}
	q := r.db.WithContext(ctx).Where("deleted_at IS NULL")
	switch scope {
	case GPTExportScopeValid:
		q = q.Where("status = ?", model.GPTStatusValid)
	case GPTExportScopeInvalid:
		q = q.Where("status = ?", model.GPTStatusInvalid)
	case GPTExportScopeSelected:
		if len(ids) == 0 {
			return nil, nil
		}
		q = q.Where("id IN ?", ids)
	default:
		// all
	}
	var items []*model.PoolGpt
	if err := q.Order("id ASC").Limit(max).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// PoolGptRefreshScope 后台批量刷新的范围 enum（与 Adobe 对齐）。
type PoolGptRefreshScope string

const (
	PoolGptScopeAll        PoolGptRefreshScope = "all"
	PoolGptScopeAbnormal   PoolGptRefreshScope = "abnormal"    // status != valid
	PoolGptScopeExpiring   PoolGptRefreshScope = "expiring"    // < 12h
	PoolGptScopeQuotaStale PoolGptRefreshScope = "quota_stale" // last_quota_check_at 久远 / NULL
)

// ListExpiringSoon 拿 access_token expires_at < now+within 的有效号（用于 silent refresh）。
//
// 只返回 status=valid 且有 refresh_token 的号；返回最多 limit 条。
func (r *PoolGptRepo) ListExpiringSoon(ctx context.Context, within time.Duration, limit int) ([]*model.PoolGpt, error) {
	if limit <= 0 {
		limit = 100
	}
	cutoff := time.Now().UTC().Add(within)
	var items []*model.PoolGpt
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status = ?", model.GPTStatusValid).
		Where("LENGTH(refresh_token_enc) > 0").
		Where("expires_at IS NOT NULL AND expires_at <= ?", cutoff).
		Order("expires_at ASC").
		Limit(limit).
		Find(&items).Error
	return items, err
}

// ListStaleQuota 拿 last_quota_check_at NULL 或 < now-staleAfter 的号。
//
// 用于轻量"只拉 quota 不换 token"的扫描，配合 wham/usage 即可。
func (r *PoolGptRepo) ListStaleQuota(ctx context.Context, staleAfter time.Duration, limit int) ([]*model.PoolGpt, error) {
	if limit <= 0 {
		limit = 100
	}
	cutoff := time.Now().UTC().Add(-staleAfter)
	var items []*model.PoolGpt
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status = ?", model.GPTStatusValid).
		Where("LENGTH(access_token_enc) > 0").
		Where("(last_quota_check_at IS NULL OR last_quota_check_at <= ?)", cutoff).
		Order("last_quota_check_at ASC").
		Limit(limit).
		Find(&items).Error
	return items, err
}

// ListForRefresh 按 scope 列出待刷新账号（手动批量刷新入口）。
func (r *PoolGptRepo) ListForRefresh(ctx context.Context, scope PoolGptRefreshScope, limit int) ([]*model.PoolGpt, error) {
	if limit <= 0 {
		limit = 200
	}
	q := r.db.WithContext(ctx).Where("deleted_at IS NULL")
	switch scope {
	case PoolGptScopeAbnormal:
		q = q.Where("status <> ?", model.GPTStatusValid)
	case PoolGptScopeExpiring:
		cutoff := time.Now().UTC().Add(12 * time.Hour)
		q = q.Where("expires_at IS NOT NULL AND expires_at <= ?", cutoff)
	case PoolGptScopeQuotaStale:
		cutoff := time.Now().UTC().Add(-30 * time.Minute)
		q = q.Where("(last_quota_check_at IS NULL OR last_quota_check_at <= ?)", cutoff)
	case PoolGptScopeAll:
		// no extra filter
	default:
		// no extra filter
	}
	var items []*model.PoolGpt
	err := q.Order("id ASC").Limit(limit).Find(&items).Error
	return items, err
}

// PoolGptPurgeFilter 物理删除（实际是软删）的范围 filter。
type PoolGptPurgeFilter struct {
	All           bool
	Status        string // "invalid" / "cooldown" / "disabled"
	TokenExpired  bool
	QuotaExceeded bool // primary_used_percent >= 100
	NoRefresh     bool // 没有 refresh_token 的号（拿不回来）
}

// AvailableForGateway 拿当前可用于 gateway 调度的号。
//
// 条件：未软删 + status=valid + (cooldown_until 为空或已过期) + (expires_at 为空或还在有效期内)。
// 由 AccountRepo facade 调用，结果转 *model.Account 返回给 AccountPool。
func (r *PoolGptRepo) AvailableForGateway(ctx context.Context) ([]*model.PoolGpt, error) {
	var items []*model.PoolGpt
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status = ?", model.GPTStatusValid).
		Where("cooldown_until IS NULL OR cooldown_until <= ?", now).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Order("last_used_at IS NULL DESC, last_used_at ASC, id ASC").
		Find(&items).Error
	return items, err
}

// MarkGatewayUsed gateway 调度成功回写。复用 success_count / last_used_at；
// 不动 trial_status / plan_type 等池管理字段。
func (r *PoolGptRepo) MarkGatewayUsed(ctx context.Context, id uint64) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&model.PoolGpt{}).
		Where("id = ?", id).Updates(map[string]any{
		"last_used_at":   now,
		"success_count":  gorm.Expr("success_count + 1"),
		"failure_count":  0,
		"status":         model.GPTStatusValid,
		"cooldown_until": nil,
		"error_message":  nil,
	}).Error
}

// MarkGatewayFailed gateway 调度失败 / 熔断回写。
func (r *PoolGptRepo) MarkGatewayFailed(ctx context.Context, id uint64, reason string, cooldown time.Duration) error {
	now := time.Now().UTC()
	fields := map[string]any{
		"failure_count": gorm.Expr("failure_count + 1"),
		"error_message": reason,
	}
	if cooldown > 0 {
		until := now.Add(cooldown)
		fields["cooldown_until"] = until
		fields["status"] = model.GPTStatusCooldown
	} else {
		fields["cooldown_until"] = nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolGpt{}).
		Where("id = ?", id).Updates(fields).Error
}

// PurgeBy 按 filter 软删账号，返回删除条数。
//
// 至少要命中一个条件，避免误删；All=true 时无视其它字段直接清空。
func (r *PoolGptRepo) PurgeBy(ctx context.Context, f PoolGptPurgeFilter) (int64, error) {
	q := r.db.WithContext(ctx).Model(&model.PoolGpt{}).Where("deleted_at IS NULL")
	if f.All {
		tx := q.Unscoped().Delete(&model.PoolGpt{})
		return tx.RowsAffected, tx.Error
	}
	hit := false
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
		hit = true
	}
	if f.TokenExpired {
		q = q.Where("expires_at IS NULL OR expires_at <= ?", time.Now().UTC())
		hit = true
	}
	if f.QuotaExceeded {
		q = q.Where("quota_primary_used_percent IS NOT NULL AND quota_primary_used_percent >= ?", 100)
		hit = true
	}
	if f.NoRefresh {
		q = q.Where("refresh_token_enc IS NULL OR LENGTH(refresh_token_enc) = 0")
		hit = true
	}
	if !hit {
		return 0, nil
	}
	tx := q.Unscoped().Delete(&model.PoolGpt{})
	return tx.RowsAffected, tx.Error
}
