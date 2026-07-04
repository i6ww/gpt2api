package repo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// CloudPhonePoolRepo GeeLark 云手机池仓储。
type CloudPhonePoolRepo struct{ db *gorm.DB }

// NewCloudPhonePoolRepo 构造。
func NewCloudPhonePoolRepo(db *gorm.DB) *CloudPhonePoolRepo {
	return &CloudPhonePoolRepo{db: db}
}

// CloudPhoneFilter 列表过滤。
type CloudPhoneFilter struct {
	Status   string
	Keyword  string
	Page     int
	PageSize int
}

// List 分页列表。
func (r *CloudPhonePoolRepo) List(ctx context.Context, f CloudPhoneFilter) ([]*model.CloudPhonePool, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 10000 {
		f.PageSize = 50
	}
	q := r.db.WithContext(ctx).Model(&model.CloudPhonePool{}).Where("deleted_at IS NULL")
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Keyword != "" {
		k := "%" + f.Keyword + "%"
		q = q.Where("(id LIKE ? OR name LIKE ? OR phone_number LIKE ? OR remark LIKE ?)", k, k, k, k)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*model.CloudPhonePool
	if err := q.Order("created_at DESC").
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetByID 主键查询（未软删）。
func (r *CloudPhonePoolRepo) GetByID(ctx context.Context, id string) (*model.CloudPhonePool, error) {
	var p model.CloudPhonePool
	err := r.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).First(&p).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

// Create 新建。
func (r *CloudPhonePoolRepo) Create(ctx context.Context, p *model.CloudPhonePool) error {
	return r.db.WithContext(ctx).Create(p).Error
}

// Update 部分字段更新。
func (r *CloudPhonePoolRepo) Update(ctx context.Context, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.CloudPhonePool{}).
		Where("id = ?", id).Updates(fields).Error
}

// SoftDelete 软删除。
func (r *CloudPhonePoolRepo) SoftDelete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Model(&model.CloudPhonePool{}).
		Where("id = ?", id).Update("deleted_at", time.Now().UTC()).Error
}

// SoftDeleteByIDs 批量软删除。
func (r *CloudPhonePoolRepo) SoftDeleteByIDs(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx := r.db.WithContext(ctx).Model(&model.CloudPhonePool{}).
		Where("id IN ? AND deleted_at IS NULL", ids).
		Update("deleted_at", time.Now().UTC())
	return tx.RowsAffected, tx.Error
}

// Stats 状态统计（不含已软删）。
func (r *CloudPhonePoolRepo) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.WithContext(ctx).Model(&model.CloudPhonePool{}).
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
		"online":   0,
		"offline":  0,
		"banned":   0,
		"disabled": 0,
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

// MarkCheck 记录探测结果。
func (r *CloudPhonePoolRepo) MarkCheck(ctx context.Context, id string, ok bool, errMsg string) error {
	now := time.Now().UTC()
	st := model.CloudPhoneCheckOK
	if !ok {
		st = model.CloudPhoneCheckFail
	}
	fields := map[string]any{
		"last_check_at": now,
		"last_check_ok": st,
		"last_error":    errMsg,
	}
	return r.db.WithContext(ctx).Model(&model.CloudPhonePool{}).
		Where("id = ?", id).Updates(fields).Error
}
