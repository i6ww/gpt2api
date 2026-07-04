package repo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// PaymentProxyPoolRepo 印尼支付代理池仓储。
type PaymentProxyPoolRepo struct{ db *gorm.DB }

// NewPaymentProxyPoolRepo 构造。
func NewPaymentProxyPoolRepo(db *gorm.DB) *PaymentProxyPoolRepo {
	return &PaymentProxyPoolRepo{db: db}
}

// PaymentProxyFilter 列表过滤。
type PaymentProxyFilter struct {
	Status   string
	Country  string
	Keyword  string
	Page     int
	PageSize int
}

// List 分页列表。
func (r *PaymentProxyPoolRepo) List(ctx context.Context, f PaymentProxyFilter) ([]*model.PaymentProxyPool, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 10000 {
		f.PageSize = 50
	}
	q := r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).Where("deleted_at IS NULL")
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Country != "" {
		q = q.Where("country = ?", f.Country)
	}
	if f.Keyword != "" {
		k := "%" + f.Keyword + "%"
		q = q.Where("(name LIKE ? OR host LIKE ? OR remark LIKE ?)", k, k, k)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.PaymentProxyPool
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetByID 主键查询（未软删）。
func (r *PaymentProxyPoolRepo) GetByID(ctx context.Context, id uint64) (*model.PaymentProxyPool, error) {
	var p model.PaymentProxyPool
	err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&p).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

// Create 新建。
func (r *PaymentProxyPoolRepo) Create(ctx context.Context, p *model.PaymentProxyPool) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// Update 部分字段更新。
func (r *PaymentProxyPoolRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete 软删除。
func (r *PaymentProxyPoolRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).
		Where("id = ?", id).Update("deleted_at", time.Now().UTC()).Error
}

// SoftDeleteByIDs 批量软删除。
func (r *PaymentProxyPoolRepo) SoftDeleteByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).
		Where("id IN ? AND deleted_at IS NULL", ids).
		Update("deleted_at", time.Now().UTC())
	return tx.RowsAffected, tx.Error
}

// Stats 状态统计（不含软删）。
func (r *PaymentProxyPoolRepo) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).
		Where("deleted_at IS NULL").
		Select("status, COUNT(*) AS n").
		Group("status").
		Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{
		"total":    0,
		"active":   0,
		"disabled": 0,
		"banned":   0,
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

// PickRandomActive 从 active 池随机抽一条（用于 dispatcher）。
//
// 不像 wallet 池那样需要 FOR UPDATE 锁——同 IP 多次并发使用是允许的（住宅 IP 池本身就是
// 多账号共享设计），轻轻地负载均衡用 last_used_at 升序就够了。
func (r *PaymentProxyPoolRepo) PickRandomActive(ctx context.Context, country string) (*model.PaymentProxyPool, error) {
	return r.PickRandomActiveExcluding(ctx, country, nil)
}

// PickRandomActiveExcluding 与 PickRandomActive 行为一致，但额外支持排除一组 proxy ID。
// 用于 rate-limited swap 场景：被刚标失败的代理不应被立刻重新抽到。
func (r *PaymentProxyPoolRepo) PickRandomActiveExcluding(ctx context.Context, country string, excludeIDs []uint64) (*model.PaymentProxyPool, error) {
	q := r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).
		Where("status = ? AND deleted_at IS NULL", model.PaymentProxyStatusActive)
	if country != "" {
		q = q.Where("country = ?", country)
	}
	if len(excludeIDs) > 0 {
		q = q.Where("id NOT IN ?", excludeIDs)
	}
	var p model.PaymentProxyPool
	if err := q.Order("COALESCE(last_used_at, '1970-01-01') ASC, id ASC").First(&p).Error; err != nil {
		return nil, mapErr(err)
	}
	// 抢到后立刻更新 last_used_at，让下次轮转换 IP
	_ = r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).
		Where("id = ?", p.ID).Update("last_used_at", time.Now().UTC()).Error
	p.LastUsedAt = ptrTime(time.Now().UTC())
	return &p, nil
}

// MarkUsed 成功使用一次：total_used++。
func (r *PaymentProxyPoolRepo) MarkUsed(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).
		Where("id = ?", id).
		UpdateColumn("total_used", gorm.Expr("total_used + 1")).Error
}

// MarkFailed 失败一次：total_failed++。
func (r *PaymentProxyPoolRepo) MarkFailed(ctx context.Context, id uint64, errMsg string) error {
	return r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"total_failed": gorm.Expr("total_failed + 1"),
			"last_error":   truncate(errMsg, 240),
		}).Error
}

// MarkCheck 记录测试结果。
func (r *PaymentProxyPoolRepo) MarkCheck(ctx context.Context, id uint64, ok bool, latencyMs int, errMsg string) error {
	now := time.Now().UTC()
	st := model.ProxyCheckOK
	if !ok {
		st = model.ProxyCheckFail
	}
	return r.db.WithContext(ctx).Model(&model.PaymentProxyPool{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_check_at": now,
			"last_check_ok": st,
			"last_check_ms": latencyMs,
			"last_error":    errMsg,
		}).Error
}

func ptrTime(t time.Time) *time.Time { return &t }
