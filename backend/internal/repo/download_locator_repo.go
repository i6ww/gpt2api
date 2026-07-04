package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/kleinai/backend/internal/model"
)

// DownloadLocatorRepo 资源定位表仓储。
type DownloadLocatorRepo struct{ db *gorm.DB }

// NewDownloadLocatorRepo 构造。
func NewDownloadLocatorRepo(db *gorm.DB) *DownloadLocatorRepo {
	return &DownloadLocatorRepo{db: db}
}

// Upsert 同 (asset_kind, asset_key, node_id) 已存在则更新 rel_path / size / sha；否则插入。
func (r *DownloadLocatorRepo) Upsert(ctx context.Context, loc *model.DownloadLocator) error {
	if loc.AssetKind == "" {
		loc.AssetKind = model.AssetKindGen
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "asset_kind"}, {Name: "asset_key"}, {Name: "node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"rel_path", "size_bytes", "sha256", "mime", "status", "expires_at",
		}),
	}).Create(loc).Error
}

// ListByAsset 列出某 asset_key 在哪些节点上有可用拷贝。
func (r *DownloadLocatorRepo) ListByAsset(ctx context.Context, kind, key string) ([]*model.DownloadLocator, error) {
	if key == "" {
		return nil, nil
	}
	if kind == "" {
		kind = model.AssetKindGen
	}
	var items []*model.DownloadLocator
	err := r.db.WithContext(ctx).
		Where("asset_kind = ? AND asset_key = ? AND status = ?", kind, key, model.LocatorActive).
		Find(&items).Error
	return items, err
}

// MarkTainted 把某节点持有的某资源标记为不可达（用户拉到 5xx/404 后调用）。
func (r *DownloadLocatorRepo) MarkTainted(ctx context.Context, kind, key, nodeID string) error {
	if key == "" || nodeID == "" {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.DownloadLocator{}).
		Where("asset_kind = ? AND asset_key = ? AND node_id = ?", kind, key, nodeID).
		Update("status", model.LocatorTainted).Error
}

// TouchServed 用户成功下载后，bump served_count + last_served_at（异步调用，失败可忽略）。
func (r *DownloadLocatorRepo) TouchServed(ctx context.Context, id uint64) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&model.DownloadLocator{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_served_at": &now,
			"served_count":   gorm.Expr("served_count + 1"),
		}).Error
}

// DeleteByNode 删除某节点的所有定位（节点下线时调用）。
func (r *DownloadLocatorRepo) DeleteByNode(ctx context.Context, nodeID string) error {
	return r.db.WithContext(ctx).Where("node_id = ?", nodeID).
		Delete(&model.DownloadLocator{}).Error
}

// GC 物理删除 expires_at 已过期的行。
func (r *DownloadLocatorRepo) GC(ctx context.Context, batch int) (int64, error) {
	if batch <= 0 {
		batch = 500
	}
	res := r.db.WithContext(ctx).Where("expires_at IS NOT NULL AND expires_at < ?", time.Now()).
		Limit(batch).Delete(&model.DownloadLocator{})
	return res.RowsAffected, res.Error
}
