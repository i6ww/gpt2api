// Package service 账号池调度。
//
// 职责：
//  1. 周期性从 DB 装载可用账号到内存（带 TTL 缓存）；
//  2. 提供 Pick(provider) 返回当前应调度的账号（RoundRobin / WeightedRR）；
//  3. 调度结果回写：MarkUsed / MarkFailed（含熔断冷却）。
//
// 不在本组件内做：HTTP 调用 / 计费 / 任务编排。
package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/logger"
)

// AccountPool 多 provider 共用一个池实例，内部按 provider 分桶。
type AccountPool struct {
	repo     *repo.AccountRepo
	leases   *repo.AccountLeaseRepo
	cacheTTL time.Duration
	mu       sync.RWMutex
	buckets  map[string]*providerBucket // key: provider
	busyMu   sync.Mutex
	busy     map[uint64]struct{}
}

type providerBucket struct {
	loadedAt time.Time
	items    []*model.Account
	cursor   uint64 // RR 游标
	weights  []int  // 权重展开缓存
	wIdx     []int  // weights -> items 索引
}

// NewAccountPool 构造。
func NewAccountPool(r *repo.AccountRepo, cacheTTL time.Duration) *AccountPool {
	if cacheTTL <= 0 {
		cacheTTL = 30 * time.Second
	}
	return &AccountPool{
		repo:     r,
		cacheTTL: cacheTTL,
		buckets:  make(map[string]*providerBucket),
		busy:     make(map[uint64]struct{}),
	}
}

// SetLeaseRepo enables cross-process account leases.
func (p *AccountPool) SetLeaseRepo(r *repo.AccountLeaseRepo) {
	if p == nil {
		return
	}
	p.leases = r
}

// Pick 取一个可用账号。strategy: round_robin / weighted_rr / random（默认 round_robin）。
func (p *AccountPool) Pick(ctx context.Context, provider, strategy string) (*model.Account, error) {
	return p.PickWhere(ctx, provider, strategy, nil)
}

// PickWhere 按 provider 取一个满足 predicate 的可用账号。
func (p *AccountPool) PickWhere(ctx context.Context, provider, strategy string, predicate func(*model.Account) bool) (*model.Account, error) {
	bucket, err := p.getBucket(ctx, provider)
	if err != nil {
		return nil, err
	}
	if len(bucket.items) == 0 {
		return nil, errcode.NoAvailableAcc
	}
	attempts := len(bucket.items)
	if strategy == "weighted_rr" && len(bucket.wIdx) > attempts {
		attempts = len(bucket.wIdx)
	}
	for i := 0; i < attempts; i++ {
		var acc *model.Account
		switch strategy {
		case "weighted_rr":
			acc = p.pickWeighted(bucket)
		default:
			acc = p.pickRR(bucket)
		}
		if acc == nil {
			break
		}
		if predicate == nil || predicate(acc) {
			return acc, nil
		}
	}
	return nil, errcode.NoAvailableAcc
}

// ReserveWhere 选取并占用一个账号，确保同一个账号不会被并发复用。
func (p *AccountPool) ReserveWhere(ctx context.Context, provider, strategy string, predicate func(*model.Account) bool) (*model.Account, error) {
	return p.reserveWhere(ctx, provider, strategy, "", "", predicate)
}

// ReserveForTaskWhere reserves an account for a concrete task and writes a
// distributed lease when lease repo is configured.
func (p *AccountPool) ReserveForTaskWhere(ctx context.Context, provider, strategy, taskID, holder string, predicate func(*model.Account) bool) (*model.Account, error) {
	return p.reserveWhere(ctx, provider, strategy, taskID, holder, predicate)
}

func (p *AccountPool) reserveWhere(ctx context.Context, provider, strategy, taskID, holder string, predicate func(*model.Account) bool) (*model.Account, error) {
	bucket, err := p.getBucket(ctx, provider)
	if err != nil {
		return nil, err
	}
	if len(bucket.items) == 0 {
		return nil, errcode.NoAvailableAcc
	}
	attempts := len(bucket.items)
	if strategy == "weighted_rr" && len(bucket.wIdx) > attempts {
		attempts = len(bucket.wIdx)
	}
	for i := 0; i < attempts; i++ {
		var acc *model.Account
		switch strategy {
		case "weighted_rr":
			acc = p.pickWeighted(bucket)
		default:
			acc = p.pickRR(bucket)
		}
		if acc == nil {
			break
		}
		if predicate != nil && !predicate(acc) {
			continue
		}
		if !shouldReserveAccount(acc) {
			return acc, nil
		}
		if p.leases != nil && taskID != "" {
			ok, err := p.leases.TryAcquireWithLimit(ctx, provider, acc.ID, taskID, holder, 30*time.Minute, accountLeaseConcurrency(provider))
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			return acc, nil
		}
		if p.tryReserve(acc.ID) {
			return acc, nil
		}
	}
	return nil, errcode.NoAvailableAcc
}

// MarkUsed 调度成功回写。provider 由内部 accountIDProvider() 反查，调用方
// 不需要关心。
func (p *AccountPool) MarkUsed(ctx context.Context, accountID uint64) {
	provider := accountIDProvider(p, accountID)
	if err := p.repo.MarkUsed(ctx, accountID, provider); err != nil {
		logger.FromCtx(ctx).Warn("account.mark_used", zap.Uint64("id", accountID), zap.Error(err))
	}
}

// MarkFailed 调度失败回写：reason 写入 last_error；cooldown>0 时进入熔断。
func (p *AccountPool) MarkFailed(ctx context.Context, accountID uint64, reason string, cooldown time.Duration) {
	provider := accountIDProvider(p, accountID)
	if err := p.repo.MarkFailed(ctx, accountID, truncate(reason, 240), cooldown, provider); err != nil {
		logger.FromCtx(ctx).Warn("account.mark_failed", zap.Uint64("id", accountID), zap.Error(err))
	}
	if cooldown > 0 {
		p.invalidate(provider)
	}
}

// MarkInvalid 把账号标记为「token 永久失效」终态：从可用池踢出且不进自动续期复活，
// 但保留行记录（status=invalid + error_message + failure_count++）便于后台审计与人工恢复。
//
// 专用于生成时遭遇干净的 token 鉴权失败（firefly 401/403）——避免该号在
// cooldown↔valid 之间被自动续期反复拉回、反复入选、反复失败（僵尸号）。
func (p *AccountPool) MarkInvalid(ctx context.Context, accountID uint64, reason string) {
	if accountID == 0 {
		return
	}
	provider := accountIDProvider(p, accountID)
	if err := p.repo.MarkInvalidForProvider(ctx, accountID, truncate(reason, 240), provider); err != nil {
		logger.FromCtx(ctx).Warn("account.mark_invalid", zap.Uint64("id", accountID), zap.Error(err))
	}
	p.invalidate(provider)
}

// MarkTransientFailed records an upstream path failure without increasing
// error_count or placing the account into cooldown.
func (p *AccountPool) MarkTransientFailed(ctx context.Context, accountID uint64, reason string) {
	if accountID == 0 {
		return
	}
	provider := accountIDProvider(p, accountID)
	if err := p.repo.UpdateForProvider(ctx, accountID, provider, map[string]any{
		"last_error": truncate(reason, 240),
	}); err != nil {
		logger.FromCtx(ctx).Warn("account.mark_transient_failed", zap.Uint64("id", accountID), zap.Error(err))
	}
}

// Release 释放账号占用。
func (p *AccountPool) Release(accountID uint64) {
	if accountID == 0 {
		return
	}
	p.busyMu.Lock()
	delete(p.busy, accountID)
	p.busyMu.Unlock()
}

// ReleaseForTask releases both in-process reservation and distributed lease.
func (p *AccountPool) ReleaseForTask(ctx context.Context, provider string, accountID uint64, taskID string) {
	if p == nil {
		return
	}
	if p.leases != nil && taskID != "" {
		_ = p.leases.Release(ctx, provider, accountID, taskID)
	}
	p.Release(accountID)
}

// Reload 强制重新装载某 provider（管理后台 CRUD 后调用）。
func (p *AccountPool) Reload(provider string) { p.invalidate(provider) }

// Stats 返回各 provider 当前池中可用数量（用于仪表盘）。
func (p *AccountPool) Stats() map[string]int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]int, len(p.buckets))
	for k, b := range p.buckets {
		out[k] = len(b.items)
	}
	return out
}

// === internal ===

func (p *AccountPool) getBucket(ctx context.Context, provider string) (*providerBucket, error) {
	p.mu.RLock()
	b, ok := p.buckets[provider]
	p.mu.RUnlock()
	if ok && time.Since(b.loadedAt) < p.cacheTTL {
		return b, nil
	}
	return p.loadBucket(ctx, provider)
}

func (p *AccountPool) loadBucket(ctx context.Context, provider string) (*providerBucket, error) {
	items, err := p.repo.AvailableByProvider(ctx, provider)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	b := &providerBucket{
		loadedAt: time.Now(),
		items:    items,
	}
	for i, it := range items {
		w := it.Weight
		if w <= 0 {
			w = 1
		}
		for j := 0; j < w; j++ {
			b.weights = append(b.weights, w)
			b.wIdx = append(b.wIdx, i)
		}
	}
	p.mu.Lock()
	p.buckets[provider] = b
	p.mu.Unlock()
	return b, nil
}

func (p *AccountPool) pickRR(b *providerBucket) *model.Account {
	n := uint64(len(b.items))
	if n == 0 {
		return nil
	}
	idx := atomic.AddUint64(&b.cursor, 1) % n
	return b.items[idx]
}

func (p *AccountPool) pickWeighted(b *providerBucket) *model.Account {
	n := uint64(len(b.wIdx))
	if n == 0 {
		return nil
	}
	idx := atomic.AddUint64(&b.cursor, 1) % n
	return b.items[b.wIdx[idx]]
}

func (p *AccountPool) tryReserve(accountID uint64) bool {
	if accountID == 0 {
		return false
	}
	p.busyMu.Lock()
	defer p.busyMu.Unlock()
	if _, ok := p.busy[accountID]; ok {
		return false
	}
	p.busy[accountID] = struct{}{}
	return true
}

func (p *AccountPool) invalidate(provider string) {
	if provider == "" {
		return
	}
	p.mu.Lock()
	delete(p.buckets, provider)
	p.mu.Unlock()
}

// accountIDProvider 通过 ID 反查 provider（仅供失败回写后的 invalidate 使用）。
// 为避免额外 SQL，从内存桶中查找；找不到则忽略。
func accountIDProvider(p *AccountPool, id uint64) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for prov, b := range p.buckets {
		for _, it := range b.items {
			if it.ID == id {
				return prov
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func shouldReserveAccount(acc *model.Account) bool {
	if shouldDirectConnectCustomUpstream(acc) {
		return false
	}
	return true
}

// accountLeaseConcurrency 单个账号允许的并发任务数（分布式租约上限）。
//
// 生产策略：一号一并发。账号池不够时不复用账号硬顶上游，而是让任务保持 pending，
// 下一轮 lease 再抢，形成自然排队。全站并发上限由 openai.admission_max_inflight /
// cluster_node.max_concurrency 控制。
//
// 注意：调小此值需配合清理存量 account_lease（旧租约 30 分钟才过期），否则旧的
// 多槽租约会影响新规则。
func accountLeaseConcurrency(provider string) int {
	return 1
}
