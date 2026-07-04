// Package repo - 公告仓储。
package repo

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// AnnouncementRepo 公告 CRUD。
type AnnouncementRepo struct{ db *gorm.DB }

// NewAnnouncementRepo 构造。
func NewAnnouncementRepo(db *gorm.DB) *AnnouncementRepo { return &AnnouncementRepo{db: db} }

// AnnouncementFilter admin 列表过滤。
type AnnouncementFilter struct {
	Keyword  string
	Level    string
	Enabled  *bool
	Page     int
	PageSize int
}

// List admin 端分页列表（含禁用 + 过期）。排序：置顶 > sort_order ASC > id DESC。
func (r *AnnouncementRepo) List(ctx context.Context, f AnnouncementFilter) ([]*model.Announcement, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.Announcement{}).Where("deleted_at IS NULL")
	if f.Keyword != "" {
		like := "%" + f.Keyword + "%"
		q = q.Where("title LIKE ? OR content LIKE ?", like, like)
	}
	if f.Level != "" {
		q = q.Where("level = ?", f.Level)
	}
	if f.Enabled != nil {
		v := 0
		if *f.Enabled {
			v = 1
		}
		q = q.Where("enabled = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.Announcement
	if err := q.Order("pinned DESC, sort_order ASC, id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ActiveAt 用户端拉取「当前时刻」可展示的公告列表。
//
// 过滤条件：
//   - deleted_at IS NULL
//   - enabled = 1
//   - (start_at IS NULL OR start_at <= now)
//   - (end_at   IS NULL OR end_at   >= now)
//
// 排序：pinned DESC, sort_order ASC, id DESC（最新创建的同档位排前面）。
func (r *AnnouncementRepo) ActiveAt(ctx context.Context, now time.Time, limit int) ([]*model.Announcement, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var items []*model.Announcement
	err := r.db.WithContext(ctx).
		Model(&model.Announcement{}).
		Where("deleted_at IS NULL AND enabled = 1").
		Where("(start_at IS NULL OR start_at <= ?)", now).
		Where("(end_at IS NULL OR end_at >= ?)", now).
		Order("pinned DESC, sort_order ASC, id DESC").
		Limit(limit).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

// GetByID 主键查询（含软删过滤）。
func (r *AnnouncementRepo) GetByID(ctx context.Context, id uint64) (*model.Announcement, error) {
	var m model.Announcement
	err := r.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", id).First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// Create 插入新公告。
func (r *AnnouncementRepo) Create(ctx context.Context, m *model.Announcement) error {
	return r.db.WithContext(ctx).Create(m).Error
}

// Update 字段级更新（fields 为 column→新值 map）。
func (r *AnnouncementRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	res := r.db.WithContext(ctx).Model(&model.Announcement{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SoftDelete 软删（写 deleted_at=now）。
func (r *AnnouncementRepo) SoftDelete(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Model(&model.Announcement{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Update("deleted_at", time.Now().UTC())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
