// Package repo 数据访问层。
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// AccountRepo 账号池仓储（Phase 1 façade）。
//
// 历史：本仓储原来直接读/写 account 表，承担「号池存储 + gateway 调度状态」
// 两个职责。号池管理拆分到 pool_gpt / pool_grok / pool_adobe 后，account
// 表事实上只剩 gateway 调度态，且 prod 0 行。
//
// Phase 1 把 account 表的「读 + gateway 状态回写」改为透明走 pool_gpt /
// pool_grok：
//
//   - AvailableByProvider / GetByID：读 pool_*，用 PoolGpt.ToAccount() /
//     PoolGrok.ToAccount() 装配回 *model.Account，下游 chat_service /
//     generation_service / account_test_service 不需要改 struct 引用。
//   - MarkUsed / MarkFailed：根据传入 provider 路由到对应 pool repo。
//   - Update：把 account 列名翻译成 pool_* 列名后路由。
//
// Create / BatchCreate / List / SoftDelete 这一类「管理后台增删改查」方法
// 暂时保留向 account 表写的旧实现；它们的调用方 AccountAdminService 会在
// Phase 2 整体下线（前端 Token 管理菜单一同删除）。
type AccountRepo struct {
	db         *gorm.DB
	poolGpt    *PoolGptRepo
	poolGrok   *PoolGrokRepo
	poolXai    *PoolXAIRepo
	poolAdobe  *PoolAdobeRepo
	poolGoogle *PoolGoogleRepo
}

// NewAccountRepo 构造。
func NewAccountRepo(db *gorm.DB) *AccountRepo {
	return &AccountRepo{
		db:         db,
		poolGpt:    NewPoolGptRepo(db),
		poolGrok:   NewPoolGrokRepo(db),
		poolXai:    NewPoolXAIRepo(db),
		poolAdobe:  NewPoolAdobeRepo(db),
		poolGoogle: NewPoolGoogleRepo(db),
	}
}

// Create 创建。仅向 account 表写（AccountAdminService 用，Phase 2 下线）。
func (r *AccountRepo) Create(ctx context.Context, a *model.Account) error {
	return r.db.WithContext(ctx).Create(a).Error
}

// BatchCreate 批量插入；忽略空切片。
func (r *AccountRepo) BatchCreate(ctx context.Context, items []*model.Account) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).CreateInBatches(items, 200).Error
}

// GetByID 主键查询：先试 pool_gpt → pool_grok → account（legacy）。
//
// 注意：pool_gpt.id 与 pool_grok.id 是独立自增序列，理论上可能冲突；
// 目前业务上没有冲突的行（pool_gpt 79+，pool_grok 1-35），Phase 3 删
// account 时再考虑 namespacing。
func (r *AccountRepo) GetByID(ctx context.Context, id uint64) (*model.Account, error) {
	if p, err := r.poolGpt.GetByID(ctx, id); err == nil && p != nil {
		return p.ToAccount(), nil
	}
	if g, err := r.poolGrok.GetByID(ctx, id); err == nil && g != nil {
		return g.ToAccount(), nil
	}
	if x, err := r.poolXai.GetByID(ctx, id); err == nil && x != nil {
		return x.ToAccount(), nil
	}
	if ad, err := r.poolAdobe.GetByID(ctx, id); err == nil && ad != nil {
		return ad.ToAccount(), nil
	}
	if g, err := r.poolGoogle.GetByID(ctx, id); err == nil && g != nil {
		return g.ToAccount(), nil
	}
	var a model.Account
	err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&a).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &a, nil
}

// AccountListFilter 列表过滤参数（仅 admin 用，作用域仍是 account 表）。
type AccountListFilter struct {
	Provider string
	PlanType string
	AuthType string
	Status   *int8
	Keyword  string
	Page     int
	PageSize int
}

// List 分页列表（仍读 account 表，AccountAdminService 用，Phase 2 下线）。
func (r *AccountRepo) List(ctx context.Context, f AccountListFilter) ([]*model.Account, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 1000 {
		f.PageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.Account{}).Where("deleted_at IS NULL")
	if f.Provider != "" {
		q = q.Where("provider = ?", f.Provider)
	}
	switch f.AuthType {
	case "token":
		q = q.Where("auth_type IN ?", []string{model.AuthTypeCookie, model.AuthTypeOAuth})
	case model.AuthTypeAPIKey, model.AuthTypeCookie, model.AuthTypeOAuth:
		q = q.Where("auth_type = ?", f.AuthType)
	}
	if f.PlanType != "" {
		q = q.Where("LOWER(JSON_UNQUOTE(JSON_EXTRACT(oauth_meta, '$.plan_type'))) = ?", f.PlanType)
	}
	if f.Status != nil {
		q = q.Where("status = ?", *f.Status)
	}
	if f.Keyword != "" {
		k := "%" + f.Keyword + "%"
		q = q.Where("(name LIKE ? OR remark LIKE ?)", k, k)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.Account
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Update 部分字段更新。
//
// 仍保留 (id, fields) 单参签名以兼容 AccountAdminService 老路径。
// 新代码（account_test_service / account_pool）改调 UpdateForProvider，
// 让 fields 走 account → pool_* 的列名翻译并路由到对应 pool repo。
func (r *AccountRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).Updates(fields).Error
}

// UpdateForProvider 按 provider 把 account 字段翻译后写到对应 pool_* 表。
//
// 翻译规则见 translateAccountFieldsToPool：
//   - last_test_* / cooldown_until / model_whitelist / weight / proxy_id 等
//     pool 表里有同名字段的直接透传。
//   - last_error → error_message
//   - error_count → failure_count
//   - access_token_expires_at → expires_at
//   - status (int8) → pool 字符串状态
//   - oauth_meta（JSON）→ 拆出 plan_type / chatgpt_account_id（仅 GPT）
func (r *AccountRepo) UpdateForProvider(ctx context.Context, id uint64, provider string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	translated := translateAccountFieldsToPool(fields, provider)
	if len(translated) == 0 {
		return nil
	}
	switch provider {
	case model.ProviderGPT:
		return r.poolGpt.Update(ctx, id, translated)
	case model.ProviderGROK:
		return r.poolGrok.Update(ctx, id, translated)
	case model.ProviderXAI:
		return r.poolXai.Update(ctx, id, translated)
	case model.ProviderADOBE:
		return r.poolAdobe.Update(ctx, id, translated)
	case model.ProviderFLOWMUSIC:
		return r.poolGoogle.Update(ctx, id, translated)
	}
	return errors.New("account.update: unsupported provider " + provider)
}

// SoftDelete 软删除（仍走 account 表）。
func (r *AccountRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).Update("deleted_at", time.Now().UTC()).Error
}

// SoftDeleteMany 按 ID 列表批量软删（仅未删除行）。
func (r *AccountRepo) SoftDeleteMany(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&model.Account{}).
		Where("id IN ? AND deleted_at IS NULL", ids).
		Update("deleted_at", now)
	return res.RowsAffected, res.Error
}

// SoftDeleteInvalid 软删：已禁用、熔断、或最近连通测试失败。
func (r *AccountRepo) SoftDeleteInvalid(ctx context.Context, provider string) (int64, error) {
	return r.SoftDeleteInvalidByAuthType(ctx, provider, "")
}

func (r *AccountRepo) SoftDeleteInvalidByAuthType(ctx context.Context, provider, authType string) (int64, error) {
	now := time.Now().UTC()
	q := r.db.WithContext(ctx).Model(&model.Account{}).Where("deleted_at IS NULL").
		Where("(last_test_status = ? OR status IN (?, ?))",
			model.AccountTestFail, model.AccountStatusDisabled, model.AccountStatusBroken)
	if provider != "" {
		q = q.Where("provider = ?", provider)
	}
	switch authType {
	case "token":
		q = q.Where("auth_type IN ?", []string{model.AuthTypeCookie, model.AuthTypeOAuth})
	case model.AuthTypeAPIKey, model.AuthTypeCookie, model.AuthTypeOAuth:
		q = q.Where("auth_type = ?", authType)
	}
	res := q.Update("deleted_at", now)
	return res.RowsAffected, res.Error
}

// SoftDeleteZeroQuota soft-deletes accounts that have been quota-probed and have no remaining image quota.
func (r *AccountRepo) SoftDeleteZeroQuota(ctx context.Context, provider string) (int64, error) {
	return r.SoftDeleteZeroQuotaByAuthType(ctx, provider, "")
}

func (r *AccountRepo) SoftDeleteZeroQuotaByAuthType(ctx context.Context, provider, authType string) (int64, error) {
	now := time.Now().UTC()
	q := r.db.WithContext(ctx).Model(&model.Account{}).Where("deleted_at IS NULL").
		Where("oauth_meta IS NOT NULL").
		Where("JSON_EXTRACT(oauth_meta, '$.image_quota_remaining') IS NOT NULL").
		Where("CAST(JSON_UNQUOTE(JSON_EXTRACT(oauth_meta, '$.image_quota_remaining')) AS SIGNED) <= 0")
	if provider != "" {
		q = q.Where("provider = ?", provider)
	}
	switch authType {
	case "token":
		q = q.Where("auth_type IN ?", []string{model.AuthTypeCookie, model.AuthTypeOAuth})
	case model.AuthTypeAPIKey, model.AuthTypeCookie, model.AuthTypeOAuth:
		q = q.Where("auth_type = ?", authType)
	}
	res := q.Update("deleted_at", now)
	return res.RowsAffected, res.Error
}

// SoftDeleteAll 软删当前条件下所有账号（未删行）。provider 空表示两池全量。
func (r *AccountRepo) SoftDeleteAll(ctx context.Context, provider string) (int64, error) {
	return r.SoftDeleteAllByAuthType(ctx, provider, "")
}

func (r *AccountRepo) SoftDeleteAllByAuthType(ctx context.Context, provider, authType string) (int64, error) {
	now := time.Now().UTC()
	q := r.db.WithContext(ctx).Model(&model.Account{}).Where("deleted_at IS NULL")
	if provider != "" {
		q = q.Where("provider = ?", provider)
	}
	switch authType {
	case "token":
		q = q.Where("auth_type IN ?", []string{model.AuthTypeCookie, model.AuthTypeOAuth})
	case model.AuthTypeAPIKey, model.AuthTypeCookie, model.AuthTypeOAuth:
		q = q.Where("auth_type = ?", authType)
	}
	res := q.Update("deleted_at", now)
	return res.RowsAffected, res.Error
}

// AvailableByProvider 拿出给定 provider 下当前可用的账号（gateway 装载用）。
//
// Phase 1 起，gpt 走 pool_gpt、grok 走 pool_grok；其它 provider（pic2api 等
// 仍是 URL-config 型，不入号池）保留向 account 表查询的旧路径。
func (r *AccountRepo) AvailableByProvider(ctx context.Context, provider string) ([]*model.Account, error) {
	switch provider {
	case model.ProviderGPT:
		rows, err := r.poolGpt.AvailableForGateway(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]*model.Account, 0, len(rows))
		for _, p := range rows {
			out = append(out, p.ToAccount())
		}
		return out, nil
	case model.ProviderGROK:
		rows, err := r.poolGrok.AvailableForGateway(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]*model.Account, 0, len(rows))
		for _, p := range rows {
			out = append(out, p.ToAccount())
		}
		return out, nil
	case model.ProviderXAI:
		rows, err := r.poolXai.AvailableForGateway(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]*model.Account, 0, len(rows))
		for _, p := range rows {
			out = append(out, p.ToAccount())
		}
		return out, nil
	case model.ProviderADOBE:
		rows, err := r.poolAdobe.AvailableForGateway(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]*model.Account, 0, len(rows))
		for _, p := range rows {
			out = append(out, p.ToAccount())
		}
		return out, nil
	case model.ProviderFLOWMUSIC:
		rows, err := r.poolGoogle.AvailableForGateway(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]*model.Account, 0, len(rows))
		for _, p := range rows {
			out = append(out, p.ToAccount())
		}
		return out, nil
	}
	var items []*model.Account
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).
		Where("provider = ? AND deleted_at IS NULL", provider).
		Where("status = ?", model.AccountStatusEnabled).
		Where("cooldown_until IS NULL OR cooldown_until <= ?", now).
		Where("access_token_expires_at IS NULL OR access_token_expires_at > ?", now).
		Order("id ASC").
		Find(&items).Error
	return items, err
}

// MarkUsed 标记调度成功。需要传入 provider 路由到 pool_*。
//
// 调用方（AccountPool）通过 accountIDProvider() 拿到桶里的 provider。
func (r *AccountRepo) MarkUsed(ctx context.Context, id uint64, provider string) error {
	switch provider {
	case model.ProviderGPT:
		return r.poolGpt.MarkGatewayUsed(ctx, id)
	case model.ProviderGROK:
		return r.poolGrok.MarkGatewayUsed(ctx, id)
	case model.ProviderXAI:
		return r.poolXai.MarkGatewayUsed(ctx, id)
	case model.ProviderADOBE:
		return r.poolAdobe.MarkGatewayUsed(ctx, id)
	case model.ProviderFLOWMUSIC:
		return r.poolGoogle.MarkGatewayUsed(ctx, id)
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).Updates(map[string]any{
		"last_used_at":   now,
		"success_count":  gorm.Expr("success_count + 1"),
		"error_count":    0,
		"status":         model.AccountStatusEnabled,
		"cooldown_until": nil,
		"last_error":     nil,
	}).Error
}

// MarkFailed 标记调度失败 / 进入熔断。
func (r *AccountRepo) MarkFailed(ctx context.Context, id uint64, reason string, cooldown time.Duration, provider string) error {
	switch provider {
	case model.ProviderGPT:
		return r.poolGpt.MarkGatewayFailed(ctx, id, reason, cooldown)
	case model.ProviderGROK:
		return r.poolGrok.MarkGatewayFailed(ctx, id, reason, cooldown)
	case model.ProviderXAI:
		return r.poolXai.MarkGatewayFailed(ctx, id, reason, cooldown)
	case model.ProviderADOBE:
		return r.poolAdobe.MarkGatewayFailed(ctx, id, reason, cooldown)
	case model.ProviderFLOWMUSIC:
		return r.poolGoogle.MarkGatewayFailed(ctx, id, reason, cooldown)
	}
	now := time.Now().UTC()
	fields := map[string]any{
		"error_count": gorm.Expr("error_count + 1"),
		"last_error":  reason,
	}
	if cooldown > 0 {
		until := now.Add(cooldown)
		fields["cooldown_until"] = until
		fields["status"] = model.AccountStatusBroken
	} else {
		fields["cooldown_until"] = nil
		fields["status"] = model.AccountStatusEnabled
	}
	return r.db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).Updates(fields).Error
}

// MarkInvalidForProvider 把账号标记为「token 永久失效」终态。
//
// 与 MarkFailed 的区别：MarkFailed 进 cooldown，到期后会被自动续期拉回可用（适合
// 限流 / 临时抖动）；MarkInvalidForProvider 是终态，不进自动续期复活循环，专门用于
// "token 真的死了"（生成时干净的 401/403）——避免僵尸号反复入选。仍保留行记录，
// 后台可查、可人工刷新恢复。
//
// 目前仅 ADOBE 走专用 invalid 态；其它 provider 退化为长冷却以保持既有行为不破坏。
func (r *AccountRepo) MarkInvalidForProvider(ctx context.Context, id uint64, reason string, provider string) error {
	switch provider {
	case model.ProviderADOBE:
		return r.poolAdobe.MarkGatewayInvalid(ctx, id, reason)
	case model.ProviderFLOWMUSIC:
		return r.poolGoogle.MarkGatewayInvalid(ctx, id, reason)
	case model.ProviderXAI:
		return r.poolXai.MarkGatewayInvalid(ctx, id, reason)
	}
	return r.MarkFailed(ctx, id, reason, 6*time.Hour, provider)
}

// translateAccountFieldsToPool 把 account 表语义的字段名/值翻成对应 pool_*
// 表能直接 Updates() 进去的形式。未识别的字段透传。
func translateAccountFieldsToPool(fields map[string]any, provider string) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		switch k {
		case "last_error":
			out["error_message"] = v
		case "error_count":
			// gorm.Expr("error_count + 1") -> "failure_count + 1"
			if expr, ok := v.(clause.Expr); ok {
				out["failure_count"] = gorm.Expr(
					strings.ReplaceAll(expr.SQL, "error_count", "failure_count"),
					expr.Vars...)
			} else {
				out["failure_count"] = v
			}
		case "access_token_expires_at":
			out["expires_at"] = v
		case "status":
			out["status"] = accountStatusInt8ToPool(v, provider)
		case "oauth_meta":
			// 仅 GPT 池有 plan_type / chatgpt_account_id；Grok 用 account_type
			s, ok := v.(string)
			if !ok || s == "" {
				continue
			}
			var meta map[string]any
			if err := json.Unmarshal([]byte(s), &meta); err != nil {
				continue
			}
			switch provider {
			case model.ProviderGPT:
				if pt, ok := meta["plan_type"].(string); ok && pt != "" {
					out["plan_type"] = pt
				}
				if cid, ok := meta["chatgpt_account_id"].(string); ok && cid != "" {
					out["chatgpt_account_id"] = cid
				}
			case model.ProviderGROK:
				if pt, ok := meta["plan_type"].(string); ok && pt != "" {
					out["account_type"] = pt
				}
			case model.ProviderADOBE:
				// pool_adobe 把 oauth_meta 整块原样存到 entitlements_json 字段，
				// 供 generation_service 学习 not_entitled 档位 / 7 天后过期。
				out["entitlements_json"] = s
			}
		default:
			out[k] = v
		}
	}
	return out
}

// accountStatusInt8ToPool 把 account 表的 int8 状态码翻成 pool_* 字符串状态。
//
// pool_gpt / pool_grok 用同一套常量字符串（GPTStatus*），pool_adobe 单独一套
// （AdobeStatus*）。除 cooldown 外其余值在两套常量中字面相同。
func accountStatusInt8ToPool(v any, provider string) string {
	var i int8
	switch n := v.(type) {
	case int8:
		i = n
	case int:
		i = int8(n)
	case int32:
		i = int8(n)
	case int64:
		i = int8(n)
	default:
		if provider == model.ProviderADOBE || provider == model.ProviderFLOWMUSIC {
			return model.AdobeStatusValid
		}
		return model.GPTStatusValid
	}
	if provider == model.ProviderADOBE || provider == model.ProviderFLOWMUSIC {
		// pool_adobe / pool_google 共用同一套字符串状态常量。
		switch i {
		case model.AccountStatusEnabled:
			return model.AdobeStatusValid
		case model.AccountStatusDisabled:
			return model.AdobeStatusDisabled
		case model.AccountStatusBroken:
			return model.AdobeStatusCooldown
		case model.AccountStatusBanned:
			return model.AdobeStatusInvalid
		default:
			return model.AdobeStatusValid
		}
	}
	switch i {
	case model.AccountStatusEnabled:
		return model.GPTStatusValid
	case model.AccountStatusDisabled:
		return model.GPTStatusDisabled
	case model.AccountStatusBroken:
		return model.GPTStatusCooldown
	case model.AccountStatusBanned:
		return model.GPTStatusInvalid
	default:
		return model.GPTStatusValid
	}
}
