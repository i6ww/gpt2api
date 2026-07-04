// Package repo API Key 仓储。
package repo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// APIKeyRepo API Key 数据访问层。
type APIKeyRepo struct{ db *gorm.DB }

// NewAPIKeyRepo 构造。
func NewAPIKeyRepo(db *gorm.DB) *APIKeyRepo { return &APIKeyRepo{db: db} }

// Create 创建。
func (r *APIKeyRepo) Create(ctx context.Context, k *model.APIKey) error {
	return r.db.WithContext(ctx).Create(k).Error
}

// GetByHash 通过 hash 查 key（用于鉴权）。
func (r *APIKeyRepo) GetByHash(ctx context.Context, hash string) (*model.APIKey, error) {
	var k model.APIKey
	err := r.db.WithContext(ctx).
		Where("hash = ? AND deleted_at IS NULL", hash).First(&k).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &k, nil
}

// ListByUser 用户拥有的 keys。
func (r *APIKeyRepo) ListByUser(ctx context.Context, userID uint64) ([]*model.APIKey, error) {
	var items []*model.APIKey
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND deleted_at IS NULL", userID).
		Order("id DESC").
		Find(&items).Error
	return items, err
}

// GetByID 主键查（含 user_id 校验）。
func (r *APIKeyRepo) GetByID(ctx context.Context, userID, id uint64) (*model.APIKey, error) {
	var k model.APIKey
	err := r.db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).First(&k).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &k, nil
}

// UpdateStatus 启用 / 禁用。
func (r *APIKeyRepo) UpdateStatus(ctx context.Context, userID, id uint64, status int8) error {
	return r.db.WithContext(ctx).Model(&model.APIKey{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("status", status).Error
}

// SoftDelete 软删除。
func (r *APIKeyRepo) SoftDelete(ctx context.Context, userID, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.APIKey{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("deleted_at", time.Now().UTC()).Error
}

// MarkUsed 异步：更新 last_used_at（不阻塞鉴权）。
func (r *APIKeyRepo) MarkUsed(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.APIKey{}).
		Where("id = ?", id).
		Update("last_used_at", time.Now().UTC()).Error
}

// ListByLast4 通过 last4 + status=1 拿候选（鉴权用）。
func (r *APIKeyRepo) ListByLast4(ctx context.Context, last4 string) ([]*model.APIKey, error) {
	var items []*model.APIKey
	err := r.db.WithContext(ctx).
		Where("last4 = ? AND status = 1 AND deleted_at IS NULL", last4).
		Find(&items).Error
	return items, err
}

// APIKeyStatRow 一个 Key 在指定时间窗口里的聚合行；GORM scan 用。
type APIKeyStatRow struct {
	KeyID          uint64
	CallTotal      int64
	CallSucceeded  int64
	CallFailed     int64
	ConsumedPoints int64
	RefundedPoints int64
	LastCalledAt   *time.Time
}

// Stats 按 user_id 聚合 generation_task 里的 from_api_key_id 列。
//
// 设计要点：
//   - 仅统计 from_api_key_id IS NOT NULL 的行（前端 Studio 下单的任务 from_api_key_id 为 NULL，
//     不会污染 sk-xxx 调用统计）。
//   - 时间窗口默认按 created_at 过滤，传 since=0 / until=0 表示该端不限。
//   - status=2 视为「真实消费」，status=3 / 4（失败 / 已退款）的 cost_points 进 refunded 一列，
//     语义上对应钱包已经退还的部分；前端可以分别展示「已扣 / 已退」。
//   - 注意：deleted_at 的过滤这里没有做，因为 generation_task 用软删的场景极少；
//     如果有删旧任务的脚本，可以在外面包一层 deleted_at IS NULL。
func (r *APIKeyRepo) Stats(ctx context.Context, userID uint64, since, until time.Time) ([]APIKeyStatRow, error) {
	q := r.db.WithContext(ctx).
		Table("generation_task").
		Select(`
			from_api_key_id AS key_id,
			COUNT(*) AS call_total,
			SUM(CASE WHEN status = 2 THEN 1 ELSE 0 END) AS call_succeeded,
			SUM(CASE WHEN status IN (3, 4) THEN 1 ELSE 0 END) AS call_failed,
			SUM(CASE WHEN status = 2 THEN cost_points ELSE 0 END) AS consumed_points,
			SUM(CASE WHEN status IN (3, 4) THEN cost_points ELSE 0 END) AS refunded_points,
			MAX(created_at) AS last_called_at
		`).
		Where("user_id = ? AND from_api_key_id IS NOT NULL", userID).
		Group("from_api_key_id")
	if !since.IsZero() {
		q = q.Where("created_at >= ?", since)
	}
	if !until.IsZero() {
		q = q.Where("created_at <= ?", until)
	}
	var rows []APIKeyStatRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
