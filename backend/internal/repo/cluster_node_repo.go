package repo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// ClusterNodeRepo 节点仓储。
type ClusterNodeRepo struct{ db *gorm.DB }

// NewClusterNodeRepo 构造。
func NewClusterNodeRepo(db *gorm.DB) *ClusterNodeRepo { return &ClusterNodeRepo{db: db} }

// Get 通过 node_id 查找。
func (r *ClusterNodeRepo) Get(ctx context.Context, nodeID string) (*model.ClusterNode, error) {
	if nodeID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var n model.ClusterNode
	err := r.db.WithContext(ctx).Where("node_id = ?", nodeID).First(&n).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &n, nil
}

// Upsert 节点保存（首次注册）。
func (r *ClusterNodeRepo) Upsert(ctx context.Context, n *model.ClusterNode) error {
	return r.db.WithContext(ctx).Save(n).Error
}

// UpdateStatus 更改状态。
func (r *ClusterNodeRepo) UpdateStatus(ctx context.Context, nodeID string, status int8) error {
	return r.db.WithContext(ctx).Model(&model.ClusterNode{}).
		Where("node_id = ?", nodeID).
		Update("status", status).Error
}

// SetSecret 写入加密后的 HMAC secret，并把节点置为待生效或启用。
func (r *ClusterNodeRepo) SetSecret(ctx context.Context, nodeID string, encSecret []byte, status int8) error {
	return r.db.WithContext(ctx).Model(&model.ClusterNode{}).
		Where("node_id = ?", nodeID).
		Updates(map[string]any{
			"hmac_secret_enc": encSecret,
			"bootstrap_used":  1,
			"status":          status,
		}).Error
}

// Revoke 吊销节点（secret 置空、status=9）。
func (r *ClusterNodeRepo) Revoke(ctx context.Context, nodeID string) error {
	return r.db.WithContext(ctx).Model(&model.ClusterNode{}).
		Where("node_id = ?", nodeID).
		Updates(map[string]any{
			"hmac_secret_enc": nil,
			"status":          model.ClusterNodeRevoked,
		}).Error
}

// Delete 物理删除（同时清掉 download_locator 中相关行由 service 触发）。
func (r *ClusterNodeRepo) Delete(ctx context.Context, nodeID string) error {
	return r.db.WithContext(ctx).Where("node_id = ?", nodeID).
		Delete(&model.ClusterNode{}).Error
}

// Heartbeat 更新心跳时间与 inflight、ip、version。
func (r *ClusterNodeRepo) Heartbeat(ctx context.Context, nodeID, ip, version string, inflight int) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&model.ClusterNode{}).
		Where("node_id = ?", nodeID).
		Updates(map[string]any{
			"last_heartbeat_at": &now,
			"last_inflight":     inflight,
			"last_ip":           ip,
			"version":           version,
		}).Error
}

// ClusterNodeFilter 列表过滤。
type ClusterNodeFilter struct {
	Role     string
	Status   *int8
	Keyword  string
	Page     int
	PageSize int
}

// List 分页列表。
func (r *ClusterNodeRepo) List(ctx context.Context, f ClusterNodeFilter) ([]*model.ClusterNode, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 50
	}
	q := r.db.WithContext(ctx).Model(&model.ClusterNode{})
	if f.Role != "" {
		q = q.Where("role = ?", f.Role)
	}
	if f.Status != nil {
		q = q.Where("status = ?", *f.Status)
	}
	if f.Keyword != "" {
		k := "%" + f.Keyword + "%"
		q = q.Where("(node_id LIKE ? OR display_name LIKE ? OR public_host LIKE ?)", k, k, k)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.ClusterNode
	if err := q.Order("status ASC, node_id ASC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListActiveByProvider 列出当前可下载/可调度的节点：status=1 且心跳在 deadSec 内 + 命中 provider scope。
// providerCode 为空表示不按 provider 过滤（用于纯下载）。
func (r *ClusterNodeRepo) ListActiveByProvider(ctx context.Context, providerCode string, deadSec int) ([]*model.ClusterNode, error) {
	if deadSec <= 0 {
		deadSec = 90
	}
	cutoff := time.Now().Add(-time.Duration(deadSec) * time.Second)

	var items []*model.ClusterNode
	q := r.db.WithContext(ctx).Model(&model.ClusterNode{}).
		Where("status = ?", model.ClusterNodeEnabled).
		Where("last_heartbeat_at IS NULL OR last_heartbeat_at >= ? OR role = ?", cutoff, model.ClusterRoleControl)
	if providerCode != "" {
		q = q.Where("JSON_SEARCH(provider_scope, 'one', ?) IS NOT NULL OR download_only = 1", providerCode)
	}
	if err := q.Order("weight DESC, last_inflight ASC, node_id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListDownloadCapable 列出当前可作为「下载源」的节点（心跳在期 + 启用）。
func (r *ClusterNodeRepo) ListDownloadCapable(ctx context.Context, deadSec int) ([]*model.ClusterNode, error) {
	if deadSec <= 0 {
		deadSec = 90
	}
	cutoff := time.Now().Add(-time.Duration(deadSec) * time.Second)

	var items []*model.ClusterNode
	err := r.db.WithContext(ctx).Model(&model.ClusterNode{}).
		Where("status = ?", model.ClusterNodeEnabled).
		Where("last_heartbeat_at >= ? OR role = ?", cutoff, model.ClusterRoleControl).
		Order("weight DESC, last_inflight ASC, node_id ASC").
		Find(&items).Error
	return items, err
}
