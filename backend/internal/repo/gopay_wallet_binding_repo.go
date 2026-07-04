package repo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// GopayWalletBindingRepo GoPay 钱包-Plus 账号绑定仓储。
type GopayWalletBindingRepo struct{ db *gorm.DB }

// NewGopayWalletBindingRepo 构造。
func NewGopayWalletBindingRepo(db *gorm.DB) *GopayWalletBindingRepo {
	return &GopayWalletBindingRepo{db: db}
}

// GopayBindingFilter 列表过滤。
type GopayBindingFilter struct {
	WalletID     uint64
	GptAccountID uint64
	Status       string
	Page         int
	PageSize     int
}

// List 分页列表。
func (r *GopayWalletBindingRepo) List(ctx context.Context, f GopayBindingFilter) ([]*model.GopayWalletBinding, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 10000 {
		f.PageSize = 50
	}
	q := r.db.WithContext(ctx).Model(&model.GopayWalletBinding{})
	if f.WalletID > 0 {
		q = q.Where("wallet_id = ?", f.WalletID)
	}
	if f.GptAccountID > 0 {
		q = q.Where("gpt_account_id = ?", f.GptAccountID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.GopayWalletBinding
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetByID 主键查询。
func (r *GopayWalletBindingRepo) GetByID(ctx context.Context, id uint64) (*model.GopayWalletBinding, error) {
	var b model.GopayWalletBinding
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&b).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &b, nil
}

// Create 新建（同时调用方应在外层事务里 wallet.MarkSuccess）。
func (r *GopayWalletBindingRepo) Create(ctx context.Context, b *model.GopayWalletBinding) error {
	return r.db.WithContext(ctx).Create(b).Error
}

// MarkCancelled 取消订阅。
func (r *GopayWalletBindingRepo) MarkCancelled(ctx context.Context, id uint64, note string) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":       model.GopayBindingStatusCancelled,
		"cancelled_at": now,
	}
	if note != "" {
		t := truncate(note, 240)
		updates["note"] = t
	}
	return r.db.WithContext(ctx).Model(&model.GopayWalletBinding{}).
		Where("id = ?", id).Updates(updates).Error
}

// CountActiveByWallet 某个钱包下还有多少 active 绑定（用于校对 wallet.active_plus_count 漂移）。
func (r *GopayWalletBindingRepo) CountActiveByWallet(ctx context.Context, walletID uint64) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.GopayWalletBinding{}).
		Where("wallet_id = ? AND status = ?", walletID, model.GopayBindingStatusActive).
		Count(&n).Error
	return n, err
}

// MarkExpiredOlderThan 把所有 active 且 expires_at < cutoff 的标记为 expired。
// 由后台调度器周期性调用，让 wallet 配额自动回收。
func (r *GopayWalletBindingRepo) MarkExpiredOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tx := r.db.WithContext(ctx).Model(&model.GopayWalletBinding{}).
		Where("status = ? AND expires_at < ?", model.GopayBindingStatusActive, cutoff).
		Update("status", model.GopayBindingStatusExpired)
	return tx.RowsAffected, tx.Error
}

// PickActiveByGptAccount 找某个 GPT 账号当前 active 的 binding（取消订阅 / 续订时反查）。
func (r *GopayWalletBindingRepo) PickActiveByGptAccount(ctx context.Context, accountID uint64) (*model.GopayWalletBinding, error) {
	var b model.GopayWalletBinding
	err := r.db.WithContext(ctx).
		Where("gpt_account_id = ? AND status = ?", accountID, model.GopayBindingStatusActive).
		Order("id DESC").First(&b).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &b, nil
}

// txDB 暴露 db 给 service 自己开事务（service 层需要在同一事务里同时 Create binding +
// MarkSuccess wallet）。
func (r *GopayWalletBindingRepo) txDB() *gorm.DB { return r.db }
