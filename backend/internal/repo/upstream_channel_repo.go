// Package repo: upstream API management。
//
// 两张表合到一个 Repo 文件里管：
//   - upstream_channel
//   - upstream_model_route
//
// 设计动机：channel 和 route 在 admin UI / cost recorder 里几乎总是成对查
// （查 route → 拿 channel → 算价），分两个文件反而要互相 import 浪费一次跳转。
package repo

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// UpstreamChannelRepo 上游通道 + 路由仓储。
type UpstreamChannelRepo struct{ db *gorm.DB }

// NewUpstreamChannelRepo 构造。
func NewUpstreamChannelRepo(db *gorm.DB) *UpstreamChannelRepo {
	return &UpstreamChannelRepo{db: db}
}

// === Channel ===

// CreateChannel 新建一个通道。
func (r *UpstreamChannelRepo) CreateChannel(ctx context.Context, ch *model.UpstreamChannel) error {
	return r.db.WithContext(ctx).Create(ch).Error
}

// GetChannelByID 主键查询。
func (r *UpstreamChannelRepo) GetChannelByID(ctx context.Context, id uint64) (*model.UpstreamChannel, error) {
	var ch model.UpstreamChannel
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&ch).Error; err != nil {
		return nil, mapErr(err)
	}
	return &ch, nil
}

// GetChannelByKey 按 key 查询；用于 CostRecorder 把硬编码 key 解析成 channel。
func (r *UpstreamChannelRepo) GetChannelByKey(ctx context.Context, key string) (*model.UpstreamChannel, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var ch model.UpstreamChannel
	if err := r.db.WithContext(ctx).Where("`key` = ?", key).First(&ch).Error; err != nil {
		return nil, mapErr(err)
	}
	return &ch, nil
}

// ChannelListFilter 列表过滤。
type ChannelListFilter struct {
	Provider string
	Enabled  *bool
	Keyword  string
	Page     int
	PageSize int
}

// ListChannels 分页列表。total = 全量条数。
func (r *UpstreamChannelRepo) ListChannels(ctx context.Context, f ChannelListFilter) ([]*model.UpstreamChannel, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 500 {
		f.PageSize = 100
	}
	q := r.db.WithContext(ctx).Model(&model.UpstreamChannel{})
	if strings.TrimSpace(f.Provider) != "" {
		q = q.Where("provider = ?", strings.TrimSpace(f.Provider))
	}
	if f.Enabled != nil {
		q = q.Where("enabled = ?", *f.Enabled)
	}
	if strings.TrimSpace(f.Keyword) != "" {
		k := "%" + strings.TrimSpace(f.Keyword) + "%"
		q = q.Where("(`key` LIKE ? OR label LIKE ? OR provider LIKE ? OR route LIKE ?)", k, k, k, k)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.UpstreamChannel
	if err := q.Order("provider ASC, route ASC, id ASC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// AllEnabledChannels 拿所有 enabled=1 的通道，CostRecorder 启动期预热缓存用。
func (r *UpstreamChannelRepo) AllEnabledChannels(ctx context.Context) ([]*model.UpstreamChannel, error) {
	var items []*model.UpstreamChannel
	err := r.db.WithContext(ctx).Where("enabled = 1").Order("provider, route, id").Find(&items).Error
	return items, err
}

// AllChannels 拿全部通道（含 disabled），admin UI 列表初始化。
func (r *UpstreamChannelRepo) AllChannels(ctx context.Context) ([]*model.UpstreamChannel, error) {
	var items []*model.UpstreamChannel
	err := r.db.WithContext(ctx).Order("provider, route, id").Find(&items).Error
	return items, err
}

// UpdateChannel 部分字段更新。fields 不要传 created_at。
func (r *UpstreamChannelRepo) UpdateChannel(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.UpstreamChannel{}).Where("id = ?", id).Updates(fields).Error
}

// DeleteChannel 硬删除。被引用的 route 一并删（外键无；逻辑上 svc 层兜底）。
func (r *UpstreamChannelRepo) DeleteChannel(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&model.UpstreamChannel{}, id).Error
}

// CountChannels 通道总数（启动期判断是否需要 seed）。
func (r *UpstreamChannelRepo) CountChannels(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.UpstreamChannel{}).Count(&n).Error
	return n, err
}

// === Route ===

// CreateRoute 新建一条路由。
func (r *UpstreamChannelRepo) CreateRoute(ctx context.Context, rt *model.UpstreamModelRoute) error {
	return r.db.WithContext(ctx).Create(rt).Error
}

// GetRouteByID 主键查询。
func (r *UpstreamChannelRepo) GetRouteByID(ctx context.Context, id uint64) (*model.UpstreamModelRoute, error) {
	var rt model.UpstreamModelRoute
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&rt).Error; err != nil {
		return nil, mapErr(err)
	}
	return &rt, nil
}

// RouteListFilter 路由查询过滤。
type RouteListFilter struct {
	ModelCode  string
	VariantKey *string
	ChannelID  uint64
	Enabled    *bool
	Page       int
	PageSize   int
}

// ListRoutes 分页列表（默认按 model_code, variant_key, priority 排）。
func (r *UpstreamChannelRepo) ListRoutes(ctx context.Context, f RouteListFilter) ([]*model.UpstreamModelRoute, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 500 {
		f.PageSize = 200
	}
	q := r.db.WithContext(ctx).Model(&model.UpstreamModelRoute{})
	if strings.TrimSpace(f.ModelCode) != "" {
		q = q.Where("model_code = ?", strings.TrimSpace(f.ModelCode))
	}
	if f.VariantKey != nil {
		q = q.Where("variant_key = ?", strings.TrimSpace(*f.VariantKey))
	}
	if f.ChannelID > 0 {
		q = q.Where("upstream_channel_id = ?", f.ChannelID)
	}
	if f.Enabled != nil {
		q = q.Where("enabled = ?", *f.Enabled)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.UpstreamModelRoute
	if err := q.Order("model_code ASC, variant_key ASC, priority ASC, id ASC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// AllRoutes 拿全部（含 disabled），admin UI + CostRecorder 缓存预热。
func (r *UpstreamChannelRepo) AllRoutes(ctx context.Context) ([]*model.UpstreamModelRoute, error) {
	var items []*model.UpstreamModelRoute
	err := r.db.WithContext(ctx).Order("model_code, variant_key, priority, id").Find(&items).Error
	return items, err
}

// RoutesForModel 查 (model_code, variant_key) 的全部 enabled 路由，按 priority 排序。
// 第一条就是默认走的通道；后续可作为 fallback。
func (r *UpstreamChannelRepo) RoutesForModel(ctx context.Context, modelCode, variantKey string) ([]*model.UpstreamModelRoute, error) {
	var items []*model.UpstreamModelRoute
	q := r.db.WithContext(ctx).
		Where("model_code = ? AND variant_key = ? AND enabled = 1", strings.TrimSpace(modelCode), strings.TrimSpace(variantKey)).
		Order("priority ASC, id ASC")
	if err := q.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// UpdateRoute 部分字段更新。
func (r *UpstreamChannelRepo) UpdateRoute(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.UpstreamModelRoute{}).Where("id = ?", id).Updates(fields).Error
}

// DeleteRoute 硬删除。
func (r *UpstreamChannelRepo) DeleteRoute(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&model.UpstreamModelRoute{}, id).Error
}

// DeleteRoutesByChannel 删除一个通道下所有路由（删通道前先调）。
func (r *UpstreamChannelRepo) DeleteRoutesByChannel(ctx context.Context, channelID uint64) (int64, error) {
	tx := r.db.WithContext(ctx).Where("upstream_channel_id = ?", channelID).Delete(&model.UpstreamModelRoute{})
	return tx.RowsAffected, tx.Error
}

// CountRoutes 路由总数（启动 seed 判定用）。
func (r *UpstreamChannelRepo) CountRoutes(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.UpstreamModelRoute{}).Count(&n).Error
	return n, err
}
