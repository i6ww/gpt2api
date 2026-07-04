package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// PoolGrokRepo GROK 号池仓储。
type PoolGrokRepo struct{ db *gorm.DB }

// NewPoolGrokRepo 构造。
func NewPoolGrokRepo(db *gorm.DB) *PoolGrokRepo { return &PoolGrokRepo{db: db} }

// PoolGrokFilter 列表过滤。
type PoolGrokFilter struct {
	TrialStatus string
	// AccountType 精确匹配 pool_grok.account_type。可选值同 dto.GrokPoolListReq.AccountType。
	AccountType string
	// SubscriptionStatus 精确匹配 pool_grok.subscription_status。
	SubscriptionStatus string
	Keyword            string
	Page               int
	PageSize           int
}

// List 分页列表。
func (r *PoolGrokRepo) List(ctx context.Context, f PoolGrokFilter) ([]*model.PoolGrok, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.PoolGrok{}).Where("deleted_at IS NULL")
	if f.TrialStatus != "" {
		q = q.Where("trial_status = ?", f.TrialStatus)
	}
	if f.AccountType != "" {
		q = q.Where("account_type = ?", f.AccountType)
	}
	if f.SubscriptionStatus != "" {
		q = q.Where("subscription_status = ?", f.SubscriptionStatus)
	}
	if f.Keyword != "" {
		q = q.Where("email LIKE ?", "%"+f.Keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.PoolGrok
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Stats 试用状态分布。
func (r *PoolGrokRepo) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.WithContext(ctx).Model(&model.PoolGrok{}).
		Where("deleted_at IS NULL").
		Select("trial_status, COUNT(*) AS n").Group("trial_status").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{
		"total": 0, "pending": 0, "activating": 0,
		"active": 0, "failed": 0, "expired": 0,
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
func (r *PoolGrokRepo) GetByID(ctx context.Context, id uint64) (*model.PoolGrok, error) {
	var m model.PoolGrok
	if err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&m).Error; err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

// Create 新增。
func (r *PoolGrokRepo) Create(ctx context.Context, p *model.PoolGrok) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// UpsertMany 按 email upsert。
func (r *PoolGrokRepo) UpsertMany(ctx context.Context, items []*model.PoolGrok) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"password_enc", "given_name", "family_name", "sso_enc", "sso_rw_enc",
			"user_agent", "trial_status", "trial_expires_at",
			"account_type", "credits",
			"payment_url", "deleted_at", "updated_at",
		}),
	}).Create(&items)
	return tx.RowsAffected, tx.Error
}

// Update 部分字段更新。
func (r *PoolGrokRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolGrok{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete deletes the account row permanently.
func (r *PoolGrokRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Unscoped().
		Where("id = ?", id).Delete(&model.PoolGrok{}).Error
}

// SoftDeleteByIDs permanently deletes account rows.
func (r *PoolGrokRepo) SoftDeleteByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Unscoped().
		Where("id IN ?", ids).
		Delete(&model.PoolGrok{})
	return tx.RowsAffected, tx.Error
}

// AvailableForGateway 拿当前可用于 gateway 调度的号。
//
// 条件：未软删 + gateway status=valid + (cooldown_until 为空或已过期) +
// (expires_at 为空或还在有效期内) + (有 sso_enc，否则没法 call API)。
func (r *PoolGrokRepo) AvailableForGateway(ctx context.Context) ([]*model.PoolGrok, error) {
	var items []*model.PoolGrok
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("status = ?", model.GrokStatusValid).
		Where("cooldown_until IS NULL OR cooldown_until <= ?", now).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Where("LENGTH(sso_enc) > 0").
		Order("last_used_at IS NULL DESC, last_used_at ASC, id ASC").
		Find(&items).Error
	return items, err
}

// MarkGatewayUsed gateway 调度成功回写。
func (r *PoolGrokRepo) MarkGatewayUsed(ctx context.Context, id uint64) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&model.PoolGrok{}).
		Where("id = ?", id).Updates(map[string]any{
		"last_used_at":   now,
		"success_count":  gorm.Expr("success_count + 1"),
		"failure_count":  0,
		"status":         model.GrokStatusValid,
		"cooldown_until": nil,
		"error_message":  nil,
	}).Error
}

// MarkGatewayFailed gateway 调度失败 / 熔断回写。
func (r *PoolGrokRepo) MarkGatewayFailed(ctx context.Context, id uint64, reason string, cooldown time.Duration) error {
	now := time.Now().UTC()
	fields := map[string]any{
		"failure_count": gorm.Expr("failure_count + 1"),
		"error_message": reason,
	}
	if cooldown > 0 {
		until := now.Add(cooldown)
		fields["cooldown_until"] = until
		fields["status"] = model.GrokStatusCooldown
	} else {
		fields["cooldown_until"] = nil
	}
	return r.db.WithContext(ctx).Model(&model.PoolGrok{}).
		Where("id = ?", id).Updates(fields).Error
}

// ExpireOverdueTrials 把 trial_expires_at <= now 的 active 行置为 expired。
func (r *PoolGrokRepo) ExpireOverdueTrials(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	tx := r.db.WithContext(ctx).Model(&model.PoolGrok{}).
		Where("trial_status = ? AND trial_expires_at IS NOT NULL AND trial_expires_at <= ? AND deleted_at IS NULL",
			model.GrokTrialActive, now).
		Update("trial_status", model.GrokTrialExpired)
	return tx.RowsAffected, tx.Error
}

// PoolGrokPurgeFilter 批量软删过滤条件。任一字段非空即生效；多字段 AND。
//
// 用例：
//
//   - 全部       ：{All: true}
//   - 失效        ：{Status: "failed"}
//   - 异常        ：{Abnormal: true}（failed + expired）
//   - 0 额度      ：{ZeroCredits: true}
type PoolGrokPurgeFilter struct {
	All         bool
	Status      string
	Abnormal    bool
	ZeroCredits bool
}

// Purge 按过滤条件批量软删。返回受影响行数。
//
// 没有任何条件 + All=false 时拒绝执行，返回 0 + nil（防止误清空）。
func (r *PoolGrokRepo) Purge(ctx context.Context, f PoolGrokPurgeFilter) (int64, error) {
	q := r.db.WithContext(ctx).Model(&model.PoolGrok{}).Where("deleted_at IS NULL")
	hasFilter := false
	if !f.All {
		if f.Status != "" {
			q = q.Where("trial_status = ?", f.Status)
			hasFilter = true
		}
		if f.Abnormal {
			q = q.Where("trial_status IN ?",
				[]string{model.GrokTrialFailed, model.GrokTrialExpired})
			hasFilter = true
		}
		if f.ZeroCredits {
			q = q.Where("credits <= 0")
			hasFilter = true
		}
		if !hasFilter {
			return 0, nil
		}
	}
	tx := q.Unscoped().Delete(&model.PoolGrok{})
	return tx.RowsAffected, tx.Error
}

// PoolGrokRefreshScope 后台批量刷新过滤条件枚举。
//
//   - all          : 所有有 sso 的账号
//   - abnormal     : trial_status IN (failed, expired) 或 failure_count > 0
//   - zero_credits : credits <= 0 + status 为 active / pending（让 cooling 复活）
//   - expiring     : 12h 内即将到期的 active 账号
//   - unknown_type : account_type 为空的账号（首次入库后人工触发探测）
type PoolGrokRefreshScope string

const (
	GrokRefreshScopeAll         PoolGrokRefreshScope = "all"
	GrokRefreshScopeAbnormal    PoolGrokRefreshScope = "abnormal"
	GrokRefreshScopeZeroCred    PoolGrokRefreshScope = "zero_credits"
	GrokRefreshScopeExpiring    PoolGrokRefreshScope = "expiring"
	GrokRefreshScopeUnknownType PoolGrokRefreshScope = "unknown_type"
)

// ListForRefresh 按 scope 列出需要探测的账号（首批，无分页）。
//
// 旧 API：用于 scheduler 等只关心首批 N 条的调用方。万级账号场景请用
// ListForRefreshChunk 做 cursor 分页。
//
// 仅返回有 sso 的行（rate-limits API 只能用 sso）。
//
// limit <= 0 → 默认 500。
func (r *PoolGrokRepo) ListForRefresh(ctx context.Context, scope PoolGrokRefreshScope, limit int) ([]*model.PoolGrok, error) {
	return r.ListForRefreshChunk(ctx, scope, 0, limit)
}

// ListForRefreshChunk 按 scope + (id > afterID) cursor 分页扫描需要探测的账号。
//
// 返回结果按 id ASC 排序，长度 ≤ limit；调用方拿最后一条的 ID 作为下次 afterID，
// 直到返回空 slice 表示扫完。
//
// 这是万级账号批量刷新的关键：避免一次 `SELECT ... LIMIT 10000` 把内存打穿，
// 也避免 `OFFSET` 翻页在并发 update 时漏行/重复。
//
// afterID = 0 → 从头开始；limit <= 0 → 默认 200。
func (r *PoolGrokRepo) ListForRefreshChunk(
	ctx context.Context,
	scope PoolGrokRefreshScope,
	afterID uint64,
	limit int,
) ([]*model.PoolGrok, error) {
	if limit <= 0 {
		limit = 200
	}
	q := r.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Where("LENGTH(sso_enc) > 0")
	if afterID > 0 {
		q = q.Where("id > ?", afterID)
	}

	switch scope {
	case GrokRefreshScopeAbnormal:
		q = q.Where("(trial_status IN ? OR failure_count > 0)",
			[]string{model.GrokTrialFailed, model.GrokTrialExpired})
	case GrokRefreshScopeZeroCred:
		q = q.Where("credits <= 0").
			Where("trial_status IN ?",
				[]string{model.GrokTrialActive, model.GrokTrialPending})
	case GrokRefreshScopeExpiring:
		threshold := time.Now().UTC().Add(12 * time.Hour)
		q = q.Where("trial_expires_at IS NOT NULL AND trial_expires_at < ?", threshold).
			Where("trial_status = ?", model.GrokTrialActive)
	case GrokRefreshScopeUnknownType:
		q = q.Where("account_type = ''")
	case GrokRefreshScopeAll:
		// no-op
	default:
		// 未知 scope 视作 all
	}
	var items []*model.PoolGrok
	if err := q.Order("id ASC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}
