// Package workerpool 号池注册任务的全局并发执行器。
//
// 设计要点：
//
//  1. 全局有 1 个 Pool 单例，通过 `Submit(taskID)` 投递任务。
//  2. Pool 内部有固定大小（默认 5）的 goroutine 池消费 channel。
//     超过容量的任务在 channel 中排队（buffer=1024），不会丢。
//  3. 应用启动时调用 Recover() 把上次进程异常退出留下的 running 任务
//     标记为 failed（防止"幽灵任务"卡在 running 永不结束）。
//  4. 任务执行函数 RunFn 由 service 层注入，pool 不感知业务。
package workerpool

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/pkg/logger"
)

// RunFn 执行单个注册任务的回调，由 RegisterTaskService 注入。
type RunFn func(ctx context.Context, taskID uint64)

// RecoverFn 启动时恢复一次性回调（典型实现：把 running 任务标 failed）。
type RecoverFn func(ctx context.Context) error

// Pool 全局并发 worker pool。
type Pool struct {
	mu          sync.Mutex
	concurrency int
	started     int // 已启动 worker 数（用于 Resize 时计算增量）
	queue       chan uint64
	runFn       RunFn
	stopOnce    sync.Once
	stopped     chan struct{}
	wg          sync.WaitGroup
}

const (
	maxConcurrency = 64
	defaultConc    = 5
)

func clampConcurrency(n int) int {
	if n <= 0 {
		return defaultConc
	}
	if n > maxConcurrency {
		return maxConcurrency
	}
	return n
}

// New 创建一个 Pool。concurrency<=0 时回落到 5；超过 64 截断到 64。
func New(concurrency int, runFn RunFn) *Pool {
	return &Pool{
		concurrency: clampConcurrency(concurrency),
		queue:       make(chan uint64, 1024),
		runFn:       runFn,
		stopped:     make(chan struct{}),
	}
}

// Start 启动 worker goroutine。重复调用会按当前 concurrency 补齐缺口。
func (p *Pool) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := p.started; i < p.concurrency; i++ {
		p.wg.Add(1)
		go p.worker(i)
		p.started++
	}
	logger.L().Info("register worker pool started", zap.Int("concurrency", p.concurrency))
}

// Concurrency 当前并发数。
func (p *Pool) Concurrency() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.concurrency
}

// Resize 在线调整并发数：
//   - n > 当前值：立即 spawn 新 worker；
//   - n <= 当前值：仅记录新值，多余 worker 等当前任务结束后下一次重启缩容
//     （Go 不支持安全杀死 goroutine，缩容真正生效需要重启 API 容器）。
//
// 返回最终生效的并发上限（已 clamp 到 [1,64]）。
func (p *Pool) Resize(n int) int {
	n = clampConcurrency(n)
	p.mu.Lock()
	defer p.mu.Unlock()
	prev := p.concurrency
	p.concurrency = n
	if n > p.started {
		for i := p.started; i < n; i++ {
			p.wg.Add(1)
			go p.worker(i)
			p.started++
		}
	}
	if n != prev {
		logger.L().Info("register worker pool resized",
			zap.Int("old", prev), zap.Int("new", n), zap.Int("workers", p.started))
	}
	return n
}

// Submit 投递一个任务 ID。channel 满则阻塞（极端情况下）；
// 调用方通常能保证压入速率远小于消费速率。
func (p *Pool) Submit(taskID uint64) {
	select {
	case <-p.stopped:
		logger.L().Warn("register pool stopped, drop task", zap.Uint64("task_id", taskID))
	default:
	}
	p.queue <- taskID
}

// Stop 等待全部 worker 完成（可在程序退出时调）。
func (p *Pool) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopped)
		close(p.queue)
	})
	p.wg.Wait()
}

func (p *Pool) worker(id int) {
	defer p.wg.Done()
	log := logger.L().With(zap.Int("worker_id", id))
	log.Info("register worker booted")
	for taskID := range p.queue {
		p.safeRun(taskID, log)
	}
	log.Info("register worker exited")
}

func (p *Pool) safeRun(taskID uint64, log *zap.Logger) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("register worker panic",
				zap.Uint64("task_id", taskID),
				zap.Any("panic", r),
				zap.ByteString("stack", debug.Stack()),
			)
		}
	}()
	// 给单个任务一个上限保护，避免某个 dispatcher 异常死循环（30 分钟）。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	p.runFn(ctx, taskID)
}
