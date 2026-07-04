package repo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// RegisterTaskRepo 号池注册任务仓储。
type RegisterTaskRepo struct{ db *gorm.DB }

// NewRegisterTaskRepo 构造。
func NewRegisterTaskRepo(db *gorm.DB) *RegisterTaskRepo { return &RegisterTaskRepo{db: db} }

// RegisterTaskFilter 列表过滤。
type RegisterTaskFilter struct {
	Provider string
	Status   string
	Keyword  string
	Page     int
	PageSize int
}

// List 分页列表。
func (r *RegisterTaskRepo) List(ctx context.Context, f RegisterTaskFilter) ([]*model.RegisterTask, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}
	q := r.db.WithContext(ctx).Model(&model.RegisterTask{}).Where("deleted_at IS NULL")
	if f.Provider != "" {
		q = q.Where("provider = ?", f.Provider)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Keyword != "" {
		q = q.Where("email LIKE ?", "%"+f.Keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.RegisterTask
	if err := q.Order("id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Stats 状态分布（按 provider 维度）。
func (r *RegisterTaskRepo) Stats(ctx context.Context, provider string) (map[string]int64, error) {
	q := r.db.WithContext(ctx).Model(&model.RegisterTask{}).Where("deleted_at IS NULL")
	if provider != "" {
		q = q.Where("provider = ?", provider)
	}
	rows, err := q.Select("status, COUNT(*) AS n").Group("status").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{
		"total": 0, "pending": 0, "running": 0,
		"success": 0, "failed": 0, "cancelled": 0,
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

// GetByID 主键查询。
func (r *RegisterTaskRepo) GetByID(ctx context.Context, id uint64) (*model.RegisterTask, error) {
	var m model.RegisterTask
	if err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&m).Error; err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

// Create 新增。
func (r *RegisterTaskRepo) Create(ctx context.Context, t *model.RegisterTask) error {
	return r.db.WithContext(ctx).Create(t).Error
}

// Update 部分字段更新。
func (r *RegisterTaskRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.RegisterTask{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete 软删除。
func (r *RegisterTaskRepo) SoftDelete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.RegisterTask{}).
		Where("id = ?", id).Update("deleted_at", time.Now().UTC()).Error
}

// PurgeFilter 批量清理过滤。零值即"清空全部已结束任务"。
type PurgeFilter struct {
	Provider string   // ""=全部
	Statuses []string // 空 -> [success, failed, cancelled]
}

// Purge 批量软删任务（仅清结束态：success / failed / cancelled），保留 pending / running。
func (r *RegisterTaskRepo) Purge(ctx context.Context, f PurgeFilter) (int64, error) {
	statuses := f.Statuses
	if len(statuses) == 0 {
		statuses = []string{"success", "failed", "cancelled"}
	}
	q := r.db.WithContext(ctx).Model(&model.RegisterTask{}).
		Where("deleted_at IS NULL").
		Where("status IN ?", statuses)
	if f.Provider != "" {
		q = q.Where("provider = ?", f.Provider)
	}
	res := q.Update("deleted_at", time.Now().UTC())
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// MarkCancelRequested 标记取消请求（worker 自检）。
func (r *RegisterTaskRepo) MarkCancelRequested(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.RegisterTask{}).
		Where("id = ?", id).Update("cancel_requested", true).Error
}
