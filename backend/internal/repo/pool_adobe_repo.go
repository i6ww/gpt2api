package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// PoolAdobeRepo ADOBE 号池仓储。
type PoolAdobeRepo struct{ db *gorm.DB }

// NewPoolAdobeRepo 构造。
func NewPoolAdobeRepo(db *gorm.DB) *PoolAdobeRepo { return &PoolAdobeRepo{db: db} }

// PoolAdobeFilter 列表过滤。
type PoolAdobeFilter struct {
	Status   string
	Source   string
	Keyword  string
	Page     int
	PageSize int
}

// List 分页列表。
func (r *PoolAdobeRepo) List(ctx context.Context, f PoolAdobeFilter) ([]*model.PoolAdobe, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.PoolAdobe{}).Where("deleted_at IS NULL")
	if f.Status != "" {
		if f.Status == "quota_recovery" {
			q = q.Where("status = ?", model.AdobeStatusCooldown).
				Where("(credits <= 0 OR LOWER(error_message) LIKE ? OR LOWER(error_message) LIKE ? OR LOWER(error_message) LIKE ?)",
					"%taste_exhausted%", "%quota_exhausted%", "%quota exhausted%")
		} else {
			q = q.Where("status = ?", f.Status)
		}
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
	var items []*model.PoolAdobe
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Stats 状态分布。
func (r *PoolAdobeRepo) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.WithContext(ctx).Model(&model.PoolAdobe{}).
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
func (r *PoolAdobeRepo) GetByID(ctx context.Context, id uint64) (*model.PoolAdobe, error) {
	var m model.PoolAdobe
	if err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&m).Error; err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

// Create 新增。
func (r *PoolAdobeRepo) Create(ctx context.Context, p *model.PoolAdobe) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// UpsertMany 按 email upsert。
func (r *PoolAdobeRepo) UpsertMany(ctx context.Context, items []*model.PoolAdobe) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	const batchSize = 50
	var affected int64
	for start := 0; start < len(items); start += batchSize {
		end := start + batchSize
		if end > len(items) {
			end = len(items)
		}
		chunk := items[start:end]
		tx := r.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "email"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"display_name", "adobe_user_id", "password_enc", "access_token_enc",
				"cookie_enc", "device_token_enc", "device_id", "status", "credits", "expires_at", "deleted_at", "updated_at",
			}),
		}).Create(&chunk)
		if tx.Error != nil {
			return affected, tx.Error
		}
		affected += tx.RowsAffected
	}
	return affected, nil
}

// Update 部分字段更新。
func (r *PoolAdobeRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolAdobe{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete deletes the account row permanently.
func (r *PoolAdobeRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Unscoped().
		Where("id = ?", id).Delete(&model.PoolAdobe{}).Error
}

// SoftDeleteByIDs permanently deletes account rows.
func (r *PoolAdobeRepo) SoftDeleteByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Unscoped().
		Where("id IN ?", ids).
		Delete(&model.PoolAdobe{})
	return tx.RowsAffected, tx.Error
}

// ListExpiringSoon 列出 expires_at < now + within 的账号（用于后台续期调度器）。
//
// 过滤条件：
//   - status IN (valid, cooldown)：disabled / invalid 不动
//   - refresh_enabled = 1：人工关闭续期的不动
//   - cookie 或 device_token 非空：可静默续期
//   - cooldown_until IS NULL OR cooldown_until <= now：避开退避中的账号
//   - expires_at IS NULL OR expires_at < now + within：未知过期时间也包含
//     （可能是新导入还没解析过 JWT 的账号）
//
// limit 限制每次扫描的最大返回条数，防止账号巨多时一次性把 worker 池打爆。
func (r *PoolAdobeRepo) ListExpiringSoon(ctx context.Context, within time.Duration, limit int) ([]*model.PoolAdobe, error) {
	if limit <= 0 {
		limit = 50
	}
	now := time.Now().UTC()
	threshold := now.Add(within)
	var items []*model.PoolAdobe
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status IN ?", []string{model.AdobeStatusValid, model.AdobeStatusCooldown}).
		Where("refresh_enabled = 1").
		Where("(LENGTH(cookie_enc) > 0 OR LENGTH(device_token_enc) > 0)").
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

// PoolAdobeExportScope 批量导出的过滤范围。
//
//   - all     ：全部未删除
//   - valid   ：status=valid
//   - invalid ：status=invalid
type PoolAdobeExportScope string

const (
	AdobeExportScopeAll     PoolAdobeExportScope = "all"
	AdobeExportScopeValid   PoolAdobeExportScope = "valid"
	AdobeExportScopeInvalid PoolAdobeExportScope = "invalid"
)

// ListForExport 批量导出。无分页（限制 max=20000，避免一次拉到爆）。
func (r *PoolAdobeRepo) ListForExport(ctx context.Context, scope PoolAdobeExportScope, max int) ([]*model.PoolAdobe, error) {
	if max <= 0 || max > 20000 {
		max = 20000
	}
	q := r.db.WithContext(ctx).Where("deleted_at IS NULL")
	switch scope {
	case AdobeExportScopeValid:
		q = q.Where("status = ?", model.AdobeStatusValid)
	case AdobeExportScopeInvalid:
		q = q.Where("status = ?", model.AdobeStatusInvalid)
	default:
		// all：不加过滤
	}
	var items []*model.PoolAdobe
	if err := q.Order("id ASC").Limit(max).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// PoolAdobePurgeFilter 批量软删过滤条件。任一字段非空即生效；多字段 AND。
//
// 用例：
//
//   - 全部       ：{All: true}
//   - 失效        ：{Status: "invalid"}
//   - 0 积分      ：{ZeroCredits: true}
//   - Token 失效  ：{TokenExpired: true}（expires_at IS NULL 或 < now）
type PoolAdobePurgeFilter struct {
	All               bool
	Status            string
	ZeroCredits       bool
	TokenExpired      bool
	QuotaRecoveryDays int
}

// Purge 按过滤条件批量软删。返回受影响行数。
//
// 没有任何条件 + All=false 时拒绝执行（safety），返回 0 + nil。
func (r *PoolAdobeRepo) Purge(ctx context.Context, f PoolAdobePurgeFilter) (int64, error) {
	q := r.db.WithContext(ctx).Model(&model.PoolAdobe{}).Where("deleted_at IS NULL")
	hasFilter := false
	if !f.All {
		if f.Status != "" {
			q = q.Where("status = ?", f.Status)
			hasFilter = true
		}
		if f.ZeroCredits {
			q = q.Where("credits <= 0")
			hasFilter = true
		}
		if f.TokenExpired {
			q = q.Where("expires_at IS NULL OR expires_at < ?", time.Now().UTC())
			hasFilter = true
		}
		if f.QuotaRecoveryDays > 0 {
			cutoff := time.Now().UTC().AddDate(0, 0, -f.QuotaRecoveryDays)
			q = q.Where("status = ?", model.AdobeStatusCooldown).
				Where("(credits <= 0 OR LOWER(error_message) LIKE ? OR LOWER(error_message) LIKE ? OR LOWER(error_message) LIKE ?)",
					"%taste_exhausted%", "%quota_exhausted%", "%quota exhausted%").
				Where("updated_at < ?", cutoff)
			hasFilter = true
		}
		if !hasFilter {
			return 0, nil
		}
	}
	tx := q.Unscoped().Delete(&model.PoolAdobe{})
	return tx.RowsAffected, tx.Error
}

// PoolAdobeRefreshScope 后台批量刷新过滤条件枚举。
//
//   - all          ：所有 valid + cooldown + invalid 的账号
//   - zero_credits ：积分 = 0 的账号（status=valid，多用于"只刷 credits"低成本巡检）
//   - abnormal     ：status IN (invalid, cooldown) 或 failure_count > 0 — 出过问题的账号
//   - expiring     ：12h 内即将过期（与调度器内部行为一致）
type PoolAdobeRefreshScope string

const (
	AdobeRefreshScopeAll      PoolAdobeRefreshScope = "all"
	AdobeRefreshScopeZeroCred PoolAdobeRefreshScope = "zero_credits"
	AdobeRefreshScopeAbnormal PoolAdobeRefreshScope = "abnormal"
	AdobeRefreshScopeExpiring PoolAdobeRefreshScope = "expiring"
	AdobeRefreshScopeRecovery PoolAdobeRefreshScope = "quota_recovery"
)

// ListForRefresh 按 scope 列出需要刷新的账号。
//
// 只返回 cookie 或 access_token 至少有一个非空的行（否则 silent refresh / fetchOnly 都跑不动）。
//
// limit <= 0 → 默认 500。批量刷新最多 500 条 / 次，避免一把把 firefly 限流打满。
func (r *PoolAdobeRepo) ListForRefresh(ctx context.Context, scope PoolAdobeRefreshScope, limit int) ([]*model.PoolAdobe, error) {
	if limit <= 0 {
		limit = 500
	}
	q := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("LENGTH(access_token_enc) > 0 OR LENGTH(cookie_enc) > 0")

	switch scope {
	case AdobeRefreshScopeZeroCred:
		q = q.Where("credits <= 0").
			Where("status IN ?", []string{model.AdobeStatusValid, model.AdobeStatusCooldown})
	case AdobeRefreshScopeAbnormal:
		q = q.Where("(status IN ? OR failure_count > 0)",
			[]string{model.AdobeStatusInvalid, model.AdobeStatusCooldown})
	case AdobeRefreshScopeExpiring:
	case AdobeRefreshScopeRecovery:
		q = q.Where("status = ?", model.AdobeStatusCooldown).
			Where("(credits <= 0 OR LOWER(error_message) LIKE ? OR LOWER(error_message) LIKE ? OR LOWER(error_message) LIKE ?)",
				"%taste_exhausted%", "%quota_exhausted%", "%quota exhausted%")
		threshold := time.Now().UTC().Add(12 * time.Hour)
		q = q.Where("expires_at IS NULL OR expires_at < ?", threshold).
			Where("status IN ?", []string{model.AdobeStatusValid, model.AdobeStatusCooldown})
	case AdobeRefreshScopeAll:
		// 不再加过滤；连 invalid 也一起重试一次（让人工"摁一下"全量强刷）
	default:
		// 未知 scope 视作 all
	}
	var items []*model.PoolAdobe
	if err := q.Order("id ASC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// AvailableForGateway 拿当前可用于 gateway 调度的 Adobe 号。
//
// 条件：未软删 + status=valid + credits>0 + (cooldown_until 为空或已过期) + (expires_at 为空或还在有效期内)。
// 由 AccountRepo facade 调用，结果转 *model.Account 返回给 AccountPool。
func (r *PoolAdobeRepo) AvailableForGateway(ctx context.Context) ([]*model.PoolAdobe, error) {
	var items []*model.PoolAdobe
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status = ?", model.AdobeStatusValid).
		Where("credits > 0").
		Where("cooldown_until IS NULL OR cooldown_until <= ?", now).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Where("LENGTH(access_token_enc) > 0").
		Order("last_used_at IS NULL DESC, last_used_at ASC, id ASC").
		Find(&items).Error
	return items, err
}

// MarkGatewayUsed gateway 调度成功回写。复用 last_used_at 字段。
func (r *PoolAdobeRepo) MarkGatewayUsed(ctx context.Context, id uint64) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&model.PoolAdobe{}).
		Where("id = ?", id).Updates(map[string]any{
		"last_used_at":   now,
		"failure_count":  0,
		"status":         model.AdobeStatusValid,
		"cooldown_until": nil,
		"error_message":  nil,
	}).Error
}

// MarkGatewayFailed gateway 调度失败 / 熔断回写。
func (r *PoolAdobeRepo) MarkGatewayFailed(ctx context.Context, id uint64, reason string, cooldown time.Duration) error {
	now := time.Now().UTC()
	fields := map[string]any{
		"failure_count": gorm.Expr("failure_count + 1"),
		"error_message": reason,
	}
	if cooldown > 0 {
		until := now.Add(cooldown)
		fields["cooldown_until"] = until
		fields["status"] = model.AdobeStatusCooldown
	} else {
		fields["cooldown_until"] = nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolAdobe{}).
		Where("id = ?", id).Updates(fields).Error
}

// MarkGatewayInvalid 生成时遭遇 firefly token 鉴权失败（干净的 401/403）→ 直接置 invalid 终态。
//
// 与 MarkGatewayFailed(cooldown) 的本质区别：
//   - status=invalid：AvailableForGateway / ListExpiringSoon / RefreshStaleCredits /
//     RefreshQuotaRecovery 全部跳过 invalid，自动续期不会再把它拉回 valid →
//     杜绝"IMS 能续 token、但 firefly 端拒绝"的僵尸号在 cooldown↔valid 之间反复入选反复失败；
//   - 仍保留 error_message + failure_count，行不软删，后台「失效」筛选可查、
//     人工"刷新异常账号"（scope=abnormal/all 含 invalid）仍可尝试恢复。
func (r *PoolAdobeRepo) MarkGatewayInvalid(ctx context.Context, id uint64, reason string) error {
	return r.db.WithContext(ctx).Model(&model.PoolAdobe{}).
		Where("id = ?", id).Updates(map[string]any{
		"status":         model.AdobeStatusInvalid,
		"error_message":  reason,
		"failure_count":  gorm.Expr("failure_count + 1"),
		"cooldown_until": nil,
	}).Error
}

// ListStaleCredits 列出 last_credits_check_at 太旧（或为空）的账号，便于"只刷新积分"低成本扫描。
//
// 不要求 cookie 非空——即便没 cookie 也能用现有 access_token 拿 credits。
func (r *PoolAdobeRepo) ListStaleCredits(ctx context.Context, staleAfter time.Duration, limit int) ([]*model.PoolAdobe, error) {
	if limit <= 0 {
		limit = 50
	}
	cutoff := time.Now().UTC().Add(-staleAfter)
	var items []*model.PoolAdobe
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status IN ?", []string{model.AdobeStatusValid}).
		Where("LENGTH(access_token_enc) > 0").
		Where("last_credits_check_at IS NULL OR last_credits_check_at < ?", cutoff).
		Order("last_credits_check_at ASC").
		Limit(limit).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ListQuotaRecovery 列出「额度回收中」账号：quota/taste exhausted 冷却号，或 credits <= 0。
// 用于后台定时轻量刷新积分；若 credits 恢复则自动拉回 valid。
func (r *PoolAdobeRepo) ListQuotaRecovery(ctx context.Context, staleAfter time.Duration, limit int) ([]*model.PoolAdobe, error) {
	if limit <= 0 {
		limit = 200
	}
	now := time.Now().UTC()
	cutoff := now.Add(-staleAfter)
	var items []*model.PoolAdobe
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("refresh_enabled = 1").
		Where("LENGTH(access_token_enc) > 0").
		Where("(last_credits_check_at IS NULL OR last_credits_check_at < ?)", cutoff).
		// 自动回收尊重 cooldown_until：taste/quota exhausted 后至少冷却到期再试。
		// 手动批量刷新 quota_recovery 不走本函数，可以由运营强制提前探测。
		Where("cooldown_until IS NULL OR cooldown_until <= ?", now).
		Where("(credits <= 0 OR (status = ? AND (LOWER(error_message) LIKE ? OR LOWER(error_message) LIKE ? OR LOWER(error_message) LIKE ?)))",
			model.AdobeStatusCooldown, "%taste_exhausted%", "%quota_exhausted%", "%quota exhausted%").
		Order("last_credits_check_at ASC, updated_at ASC").
		Limit(limit).
		Find(&items).Error
	return items, err
}
