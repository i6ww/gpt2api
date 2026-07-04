package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// AccountLeaseRepo provides short-lived distributed account leases.
//
// The lease key is (provider, account_id). A new task can take an account only
// when no lease exists or the previous lease has expired. This complements the
// in-process AccountPool.busy map and is required once multiple api/openai/admin
// processes or remote agents execute generation tasks concurrently.
type AccountLeaseRepo struct{ db *gorm.DB }

func NewAccountLeaseRepo(db *gorm.DB) *AccountLeaseRepo { return &AccountLeaseRepo{db: db} }

// TryAcquire atomically acquires a lease if the account is free or the old
// lease has expired. It returns true on success.
func (r *AccountLeaseRepo) TryAcquire(ctx context.Context, provider string, accountID uint64, taskID, holder string, ttl time.Duration) (bool, error) {
	return r.TryAcquireWithLimit(ctx, provider, accountID, taskID, holder, ttl, 1)
}

// TryAcquireWithLimit allows several independent slots per account. It requires
// account_lease primary key to include slot_no; legacy schemas without slot_no
// still work for limit=1.
func (r *AccountLeaseRepo) TryAcquireWithLimit(ctx context.Context, provider string, accountID uint64, taskID, holder string, ttl time.Duration, limit int) (bool, error) {
	if r == nil || r.db == nil || provider == "" || accountID == 0 || taskID == "" {
		return false, nil
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if limit <= 0 {
		limit = 1
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(ttl)
	if limit == 1 {
		var active int64
		if err := r.db.WithContext(ctx).Table("account_lease").
			Where("provider = ? AND account_id = ? AND task_id <> ? AND lease_until > ?", provider, accountID, taskID, now).
			Count(&active).Error; err != nil {
			return false, err
		}
		if active > 0 {
			return false, nil
		}
	}
	for slot := 1; slot <= limit; slot++ {
		returned := false
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Exec(
				`INSERT INTO account_lease (provider, account_id, slot_no, task_id, holder, lease_until, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				 ON DUPLICATE KEY UPDATE
				   task_id = IF(lease_until < VALUES(created_at) OR task_id = VALUES(task_id), VALUES(task_id), task_id),
				   holder = IF(lease_until < VALUES(created_at) OR task_id = VALUES(task_id), VALUES(holder), holder),
				   lease_until = IF(lease_until < VALUES(created_at) OR task_id = VALUES(task_id), VALUES(lease_until), lease_until),
				   updated_at = IF(lease_until < VALUES(created_at) OR task_id = VALUES(task_id), VALUES(updated_at), updated_at)`,
				provider, accountID, slot, taskID, holder, leaseUntil, now, now,
			)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				returned = true
				return nil
			}
			var cnt int64
			if err := tx.Table("account_lease").
				Where("provider = ? AND account_id = ? AND slot_no = ? AND task_id = ? AND lease_until > ?", provider, accountID, slot, taskID, now).
				Count(&cnt).Error; err != nil {
				return err
			}
			returned = cnt > 0
			return nil
		})
		if err != nil {
			return false, err
		}
		if returned {
			return true, nil
		}
	}
	return false, nil
}

func (r *AccountLeaseRepo) Release(ctx context.Context, provider string, accountID uint64, taskID string) error {
	if r == nil || r.db == nil || provider == "" || accountID == 0 || taskID == "" {
		return nil
	}
	return r.db.WithContext(ctx).
		Exec("DELETE FROM account_lease WHERE provider = ? AND account_id = ? AND task_id = ?", provider, accountID, taskID).
		Error
}

func (r *AccountLeaseRepo) Extend(ctx context.Context, provider string, accountID uint64, taskID string, ttl time.Duration) error {
	if r == nil || r.db == nil || provider == "" || accountID == 0 || taskID == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).
		Exec("UPDATE account_lease SET lease_until = ?, updated_at = ? WHERE provider = ? AND account_id = ? AND task_id = ?",
			now.Add(ttl), now, provider, accountID, taskID).
		Error
}

func (r *AccountLeaseRepo) ReapExpired(ctx context.Context) (int64, error) {
	if r == nil || r.db == nil {
		return 0, nil
	}
	res := r.db.WithContext(ctx).Exec("DELETE FROM account_lease WHERE lease_until < ?", time.Now().UTC())
	return res.RowsAffected, res.Error
}
