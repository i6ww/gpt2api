// Package repo: task_cost_log（利润事实表）。
package repo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// TaskCostLogRepo 任务成本日志仓储。
type TaskCostLogRepo struct{ db *gorm.DB }

// NewTaskCostLogRepo 构造。
func NewTaskCostLogRepo(db *gorm.DB) *TaskCostLogRepo {
	return &TaskCostLogRepo{db: db}
}

// Create 单条写入。CostRecorder 一次任务调一次。
func (r *TaskCostLogRepo) Create(ctx context.Context, row *model.TaskCostLog) error {
	if row == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(row).Error
}

// CreateMany 批量写入；目前 chat/generation 都是单条，但 register Phase E 会一次写
// 「短信 + 验证码 + 代理 + 邮箱」多条。
func (r *TaskCostLogRepo) CreateMany(ctx context.Context, rows []*model.TaskCostLog) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&rows).Error
}

// ListByRef 拿某个 ref 的全部成本日志（admin 详情页用）。
func (r *TaskCostLogRepo) ListByRef(ctx context.Context, refType, refID string) ([]*model.TaskCostLog, error) {
	var items []*model.TaskCostLog
	err := r.db.WithContext(ctx).
		Where("ref_type = ? AND ref_id = ?", refType, refID).
		Order("recorded_at ASC, id ASC").Find(&items).Error
	return items, err
}

// CostListFilter 列表过滤（用于 admin 报表展开）。
type CostListFilter struct {
	RefType    string
	ModelCode  string
	ChannelID  uint64
	UserID     uint64
	StartedAt  *time.Time
	EndedAt    *time.Time
	Page       int
	PageSize   int
}

// List 分页列表。默认按 recorded_at DESC。
func (r *TaskCostLogRepo) List(ctx context.Context, f CostListFilter) ([]*model.TaskCostLog, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 500 {
		f.PageSize = 100
	}
	q := r.db.WithContext(ctx).Model(&model.TaskCostLog{})
	if f.RefType != "" {
		q = q.Where("ref_type = ?", f.RefType)
	}
	if f.ModelCode != "" {
		q = q.Where("model_code = ?", f.ModelCode)
	}
	if f.ChannelID > 0 {
		q = q.Where("upstream_channel_id = ?", f.ChannelID)
	}
	if f.UserID > 0 {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.StartedAt != nil {
		q = q.Where("recorded_at >= ?", *f.StartedAt)
	}
	if f.EndedAt != nil {
		q = q.Where("recorded_at < ?", *f.EndedAt)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.TaskCostLog
	if err := q.Order("recorded_at DESC, id DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ProfitDailyRow 利润报表里的一行（按天聚合）。
type ProfitDailyRow struct {
	Day             time.Time `gorm:"column:day" json:"day"`
	TaskCount       int64     `gorm:"column:task_count" json:"task_count"`
	CostMicroUSD    int64     `gorm:"column:cost_micro_usd" json:"cost_micro_usd"`
	SaleMicroCNY    int64     `gorm:"column:sale_micro_cny" json:"sale_micro_cny"`
	SalePoints      int64     `gorm:"column:sale_points" json:"sale_points"`
	AvgFXUSDToCNY   float64   `gorm:"column:avg_fx" json:"avg_fx_usd_to_cny"`
}

// ProfitDaily 按天聚合的利润报表。dim = 维度，影响 GROUP BY:
//   - "day" 仅按天
//   - "day,model" 按天 × model_code
//   - "day,channel" 按天 × upstream_channel_id
func (r *TaskCostLogRepo) ProfitDaily(ctx context.Context, dim string, from, to time.Time) ([]map[string]any, error) {
	groupCols := "DATE(recorded_at) AS day"
	selectCols := "DATE(recorded_at) AS day, COUNT(*) AS task_count, " +
		"COALESCE(SUM(cost_micro_usd),0) AS cost_micro_usd, " +
		"COALESCE(SUM(sale_micro_cny),0) AS sale_micro_cny, " +
		"COALESCE(SUM(sale_points),0) AS sale_points, " +
		"COALESCE(AVG(NULLIF(fx_usd_to_cny,0)),0) AS avg_fx"
	groupBy := "DATE(recorded_at)"
	orderBy := "day DESC"
	switch dim {
	case "day,model":
		groupCols += ", model_code"
		selectCols += ", model_code"
		groupBy += ", model_code"
	case "day,channel":
		groupCols += ", upstream_channel_id"
		selectCols += ", upstream_channel_id"
		groupBy += ", upstream_channel_id"
	case "day,provider":
		// 通道 join 一下，按 provider 聚合
		selectCols += ", c.provider AS provider"
		groupBy += ", c.provider"
	}

	q := r.db.WithContext(ctx).Table("task_cost_log AS l").
		Select(selectCols).
		Where("recorded_at >= ? AND recorded_at < ?", from, to)
	if dim == "day,provider" {
		q = q.Joins("JOIN upstream_channel AS c ON c.id = l.upstream_channel_id")
	}
	q = q.Group(groupBy).Order(orderBy)

	var rows []map[string]any
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// AggregateBucket 聚合区间内的整体总数（dashboard 用，单值不分组）。
func (r *TaskCostLogRepo) AggregateBucket(ctx context.Context, from, to time.Time) (taskCount, costMicroUSD, saleMicroCNY, salePoints int64, err error) {
	type res struct {
		TaskCount    int64
		CostMicroUSD int64
		SaleMicroCNY int64
		SalePoints   int64
	}
	var r0 res
	q := r.db.WithContext(ctx).Table("task_cost_log").
		Select("COUNT(*) AS task_count, COALESCE(SUM(cost_micro_usd),0) AS cost_micro_usd, COALESCE(SUM(sale_micro_cny),0) AS sale_micro_cny, COALESCE(SUM(sale_points),0) AS sale_points").
		Where("recorded_at >= ? AND recorded_at < ?", from, to)
	if err = q.Scan(&r0).Error; err != nil {
		return
	}
	return r0.TaskCount, r0.CostMicroUSD, r0.SaleMicroCNY, r0.SalePoints, nil
}

// PurgeBefore 清理 cutoff 之前的日志（保留期到了后清旧数据）。
func (r *TaskCostLogRepo) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tx := r.db.WithContext(ctx).Where("recorded_at < ?", cutoff).Delete(&model.TaskCostLog{})
	return tx.RowsAffected, tx.Error
}
