package repo

import (
	"context"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// RegisterTaskLogFilter 列表过滤。
type RegisterTaskLogFilter struct {
	TaskID   uint64 // 0=全部
	Provider string // ""=全部
	Level    string // ""=全部
	Limit    int    // 默认 200，最大 1000
}

// RegisterTaskLogRepo 仓储。
type RegisterTaskLogRepo struct {
	db *gorm.DB
}

// NewRegisterTaskLogRepo 构造。
func NewRegisterTaskLogRepo(db *gorm.DB) *RegisterTaskLogRepo {
	return &RegisterTaskLogRepo{db: db}
}

// Insert append 一条日志。失败被吞掉（log 写不进去也不能让主流程挂）。
func (r *RegisterTaskLogRepo) Insert(ctx context.Context, l *model.RegisterTaskLog) error {
	return r.db.WithContext(ctx).Create(l).Error
}

// List 倒序拉取最近若干条。
func (r *RegisterTaskLogRepo) List(ctx context.Context, f RegisterTaskLogFilter) ([]*model.RegisterTaskLog, error) {
	q := r.db.WithContext(ctx).Model(&model.RegisterTaskLog{})
	if f.TaskID > 0 {
		q = q.Where("task_id = ?", f.TaskID)
	}
	if f.Provider != "" {
		q = q.Where("provider = ?", f.Provider)
	}
	if f.Level != "" {
		q = q.Where("level = ?", f.Level)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	var rows []*model.RegisterTaskLog
	if err := q.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// PurgeByTask 清理某个任务的全部日志。
func (r *RegisterTaskLogRepo) PurgeByTask(ctx context.Context, taskID uint64) error {
	return r.db.WithContext(ctx).Where("task_id = ?", taskID).Delete(&model.RegisterTaskLog{}).Error
}

// Purge 按过滤条件批量清理日志。filter 为零值时等价于清空全表。
//
// 注意：GORM v2 默认禁止无 WHERE 的批量 Delete（防止误清表），
// 这里始终带上一个永真 WHERE 显式表达"清空"语义，避免 ErrMissingWhereClause。
func (r *RegisterTaskLogRepo) Purge(ctx context.Context, f RegisterTaskLogFilter) (int64, error) {
	q := r.db.WithContext(ctx).Where("1 = 1")
	if f.TaskID > 0 {
		q = q.Where("task_id = ?", f.TaskID)
	}
	if f.Provider != "" {
		q = q.Where("provider = ?", f.Provider)
	}
	if f.Level != "" {
		q = q.Where("level = ?", f.Level)
	}
	res := q.Delete(&model.RegisterTaskLog{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
