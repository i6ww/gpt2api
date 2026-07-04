package repo

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
)

// PhonePoolRepo 接码手机号池仓库。
type PhonePoolRepo struct{ db *gorm.DB }

// NewPhonePoolRepo 构造。
func NewPhonePoolRepo(db *gorm.DB) *PhonePoolRepo { return &PhonePoolRepo{db: db} }

// hero-sms 的 activation 默认 20 分钟后销毁；超期的号 setStatus=1 重激活会失败,
// 也无法再走 getStatusV2 收新 SMS。这里留 18 分钟安全边际,acquire 时跳过更老的号。
const phoneActivationTTL = 18 * time.Minute

// AcquireOrInsert 优先复用一条还能用的号(used_count<max_uses)；
// 没有的话插入参数中传入的新号并返回。
//
// 整个过程在一个事务内串行，避免两条 dispatcher 抢同一个号。
//
// countries 不为空时仅复用号的 country 命中其中之一的池号 —— 调度方按任务粒度
// override country 时还能命中"已经收过 SMS"的池号（同一国家），避免每次都重新
// 跟 hero-sms 申请新号。
//
// 复用排序优先 used_count>0（已经收过码、最稳）→ 然后按 used_count 升序 →
// 最近用过的优先（保持号"热身"状态新鲜，hero-sms 端不容易判失效）。
func (r *PhonePoolRepo) AcquireOrInsert(ctx context.Context, provider, service string, countries []int, fallback *model.PhonePool) (*model.PhonePool, error) {
	var picked model.PhonePool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 同时排除 hero-sms activation 已过期的号（>18min 未用）—— 这些号即使 row 还
		// 在,setStatus=1 也激活不回来,白浪费一次复用尝试。
		// last_used_at 为 NULL 表示号刚 getNumberV2 入池就失败回滚到 available,
		// 这里也允许重试一次。
		ttlCutoff := time.Now().UTC().Add(-phoneActivationTTL)
		q := tx.Where("provider = ? AND service = ? AND status = ? AND used_count < max_uses AND deleted_at IS NULL",
			provider, service, model.PhoneStatusAvailable).
			Where("last_used_at IS NULL OR last_used_at >= ?", ttlCutoff)
		if len(countries) > 0 {
			q = q.Where("country IN ?", countries)
		}
		err := q.Order("CASE WHEN used_count > 0 THEN 0 ELSE 1 END ASC, used_count ASC, COALESCE(last_used_at, '1970-01-01') DESC").
			First(&picked).Error
		if err == nil {
			return tx.Model(&picked).Updates(map[string]any{
				"status":     model.PhoneStatusInUse,
				"updated_at": time.Now().UTC(),
			}).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// 2) 池里没号，把传进来的 fallback 落库（来自 hero-sms getNumberV2 的新号）。
		if fallback == nil {
			return errors.New("没有可用手机号且未提供新号")
		}
		fallback.Status = model.PhoneStatusInUse
		if fallback.MaxUses <= 0 {
			fallback.MaxUses = 3
		}
		if fallback.Provider == "" {
			fallback.Provider = provider
		}
		if fallback.Service == "" {
			fallback.Service = service
		}
		if err := tx.Create(fallback).Error; err != nil {
			return err
		}
		picked = *fallback
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &picked, nil
}

// Release 将一条 in_use 的号码放回 available（不增加 used_count）。
func (r *PhonePoolRepo) Release(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.PhonePool{}).
		Where("id = ?", id).Updates(map[string]any{
		"status":     model.PhoneStatusAvailable,
		"updated_at": time.Now().UTC(),
	}).Error
}

// MarkVerified 注册成功，used_count++。如果用满 max_uses 自动置 exhausted。
func (r *PhonePoolRepo) MarkVerified(ctx context.Context, id, accountID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p model.PhonePool
		if err := tx.First(&p, id).Error; err != nil {
			return err
		}
		p.UsedCount++
		now := time.Now().UTC()
		updates := map[string]any{
			"used_count":      p.UsedCount,
			"failure_count":   0,
			"last_used_at":    now,
			"last_account_id": accountID,
			"updated_at":      now,
		}
		if p.UsedCount >= p.MaxUses {
			updates["status"] = model.PhoneStatusExhausted
		} else {
			updates["status"] = model.PhoneStatusAvailable
		}
		return tx.Model(&model.PhonePool{}).Where("id = ?", id).Updates(updates).Error
	})
}

// MarkFailed 失败计数+1。超过 maxFailure 永久标 broken。
func (r *PhonePoolRepo) MarkFailed(ctx context.Context, id uint64, reason string, maxFailure int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p model.PhonePool
		if err := tx.First(&p, id).Error; err != nil {
			return err
		}
		p.FailureCount++
		updates := map[string]any{
			"failure_count": p.FailureCount,
			"last_error":    truncate(reason, 240),
			"updated_at":    time.Now().UTC(),
		}
		if p.FailureCount >= maxFailure {
			updates["status"] = model.PhoneStatusBroken
		} else {
			updates["status"] = model.PhoneStatusAvailable
		}
		return tx.Model(&model.PhonePool{}).Where("id = ?", id).Updates(updates).Error
	})
}

// GetByID 按主键拿一条；用于上层做"已成功过的号容错更宽松"等动态决策。
func (r *PhonePoolRepo) GetByID(ctx context.Context, id uint64) (*model.PhonePool, error) {
	var p model.PhonePool
	if err := r.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// UpdateActivationID 用于在每次申请新 SMS 时把 activation id 写回。
func (r *PhonePoolRepo) UpdateActivationID(ctx context.Context, id uint64, activationID string) error {
	return r.db.WithContext(ctx).Model(&model.PhonePool{}).
		Where("id = ?", id).Updates(map[string]any{
		"activation_id": activationID,
		"updated_at":    time.Now().UTC(),
	}).Error
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
