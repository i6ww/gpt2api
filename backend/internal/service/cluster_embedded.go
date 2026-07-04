package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/pkg/logger"
)

// EmbeddedAgent 主控进程内自带的「本地 agent」。
//
// 作用：让单机 / 主控+少量边缘 部署在 cluster.enabled=true 模式下，
// 主控进程也能从任务队列里 lease 任务并复用现成的 runTask 流水线。
//
// 与远端 agent 共用同一张 generation_task 表 + ClaimBatch(SKIP LOCKED)：
//   - 远端 agent 用各自 node_id 抢锁；
//   - embedded 用固定 node_id = "control-main" 抢锁；
//   - lease 过期后 ClusterMaintenance 会把 status 回到 pending，让另一头补跑。
//
// 实例化在 cmd/admin（and 可选 cmd/worker）启动期；多个进程同时跑 embedded
// 会因为共用 node_id 出现 ClaimBatch 的「重入」分支 → 可能造成同任务双跑，
// 因此必须保证主控侧只有一个进程持有 EmbeddedAgent。
type EmbeddedAgent struct {
	gen      *GenerationService
	cluster  *ClusterService
	nodeID   string
	interval time.Duration

	maxConc  int // 0 表示从 cluster_node.max_concurrency 读取；启动期兜底 8
	inflight atomic.Int64

	once   sync.Once
	stopCh chan struct{}
}

// NewEmbeddedAgent 构造；gen / cluster 任意为 nil 都会得到一个永远 no-op 的 agent。
func NewEmbeddedAgent(gen *GenerationService, cluster *ClusterService) *EmbeddedAgent {
	return &EmbeddedAgent{
		gen:      gen,
		cluster:  cluster,
		nodeID:   model.ClusterEmbeddedNodeID,
		interval: 500 * time.Millisecond,
		maxConc:  0,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动 lease loop + heartbeat loop；幂等，多次调用只生效一次。
// 取消 ctx 或调用 Stop() 都会优雅退出，等待 inflight 任务自然结束。
//
// heartbeat：每 30s 把 cluster_node[control-main].last_heartbeat_at 刷新，
// 让 api / openai / worker 等其他进程通过 ClusterService.EmbeddedAlive 知道
// 主控的 lease 通道还活着，从而放心走 dispatch-skip 路径。
func (e *EmbeddedAgent) Start(ctx context.Context) {
	if e == nil || e.gen == nil || e.cluster == nil {
		return
	}
	e.once.Do(func() {
		go e.run(ctx)
		go e.heartbeat(ctx)
	})
}

// heartbeat 周期性写 last_heartbeat_at + last_inflight 到 cluster_node[control-main]。
func (e *EmbeddedAgent) heartbeat(ctx context.Context) {
	if e == nil || e.cluster == nil || e.cluster.nodes == nil {
		return
	}
	e.beat(ctx) // 立刻打一次，避免冷启动期 api 进程把任务当 inline 跑
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-t.C:
			e.beat(ctx)
		}
	}
}

func (e *EmbeddedAgent) beat(ctx context.Context) {
	if err := e.cluster.nodes.Heartbeat(ctx, e.nodeID, "", "embedded", int(e.inflight.Load())); err != nil {
		logger.L().Debug("embedded heartbeat", zap.Error(err))
	}
}

// Stop 触发优雅停止。
func (e *EmbeddedAgent) Stop() {
	if e == nil {
		return
	}
	select {
	case <-e.stopCh:
	default:
		close(e.stopCh)
	}
}

// Inflight 当前持有的任务数（仅用于 admin 心跳 / metrics）。
func (e *EmbeddedAgent) Inflight() int64 {
	if e == nil {
		return 0
	}
	return e.inflight.Load()
}

func (e *EmbeddedAgent) run(ctx context.Context) {
	log := logger.L().Named("cluster.embedded")
	log.Info("embedded agent started", zap.String("node_id", e.nodeID))

	t := time.NewTicker(e.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("embedded agent stopped (ctx)", zap.Int64("inflight", e.inflight.Load()))
			return
		case <-e.stopCh:
			log.Info("embedded agent stopped", zap.Int64("inflight", e.inflight.Load()))
			return
		case <-t.C:
			e.tick(ctx)
		}
	}
}

// tick 单次扫描：根据空闲槽位拉取一批任务并起 goroutine 跑掉。
func (e *EmbeddedAgent) tick(ctx context.Context) {
	if !e.cluster.Enabled(ctx) {
		return
	}
	maxConc, scope := e.loadCapacity(ctx)
	if maxConc <= 0 {
		return
	}
	free := maxConc - int(e.inflight.Load())
	if free <= 0 {
		return
	}
	// 单轮批量抓取上限：高并发部署（maxConc 可达数百）时，32 的旧上限配合
	// 1.5s tick 会让满载 ramp 慢到 ~20s。放大到 256 + 500ms tick 后，
	// 500 槽位可在 ~2s 内填满，几乎消除「明明有容量却排队」的窗口。
	if free > 256 {
		free = 256
	}
	if len(scope) == 0 {
		return
	}

	leaseTTL := e.cluster.LeaseTTL(ctx)
	tasks, err := e.gen.repo.ClaimBatch(ctx, e.nodeID, scope, leaseTTL, free)
	if err != nil {
		logger.L().Warn("embedded claim failed", zap.Error(err))
		return
	}
	if len(tasks) == 0 {
		return
	}
	for _, task := range tasks {
		e.inflight.Add(1)
		go e.runOne(task)
	}
}

func (e *EmbeddedAgent) runOne(task *model.GenerationTask) {
	defer e.inflight.Add(-1)
	defer func() {
		if r := recover(); r != nil {
			logger.L().Error("embedded runTask panic",
				zap.String("task", task.TaskID), zap.Any("recover", r))
			// panic 后兜底：标记任务失败 + 退款，避免任务卡在 Running 等 lease 过期。
			e.ensureTerminal(task.TaskID, fmt.Sprintf("worker panic: %v", r))
		}
	}()
	// runTask 自己控制超时；用 Background 避免被 tick 的 ctx 提前砍掉。
	e.gen.runTask(context.Background(), task)
	// runTask 正常返回后兜底：如果任务仍非终态（Succeeded/Failed/Refunded），
	// 说明 runTask 走了静默 return 路径（典型：SetRunning rows=0 让步），强制收尾。
	// 不这么做的话任务会停在 Running，5min lease 过期被 ReclaimExpired 重置回 Pending，
	// 下一轮 ClaimBatch 再抢，attempt 持续 +1，用户看到「待处理」越拖越久。
	e.ensureTerminal(task.TaskID, "worker exited without setting terminal state")
}

// ensureTerminal 兜底：runTask 返回后看任务是否仍在 Pending/Running，是的话强制 SetFailed
// + 退款。正常路径下任务已经在 runTask 内 SetSucceeded/SetFailed，本函数是 no-op。
func (e *EmbeddedAgent) ensureTerminal(taskID, reason string) {
	if e == nil || e.gen == nil || e.gen.repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t, err := e.gen.repo.GetByTaskID(ctx, taskID)
	if err != nil || t == nil {
		return
	}
	if t.Status != model.GenStatusPending && t.Status != model.GenStatusRunning {
		return // 已是终态，不动
	}
	logger.L().Warn("embedded.ensure_terminal: task not terminal after runTask, forcing fail",
		zap.String("task", taskID),
		zap.Int8("status", t.Status),
		zap.Int8("attempt", t.Attempt),
		zap.String("reason", reason),
	)
	e.gen.failTask(ctx, t, reason)
}

// loadCapacity 读 cluster_node[control-main] 的 max_concurrency / provider_scope；
// 失败兜底为 (8, [gpt, grok, adobe, pic2api])。
func (e *EmbeddedAgent) loadCapacity(ctx context.Context) (int, []string) {
	defaultScope := []string{
		model.ProviderGPT, model.ProviderGROK, model.ProviderADOBE, model.ProviderPIC2API,
		model.ProviderFLOWMUSIC,
	}
	defaultConc := 8

	if e.maxConc > 0 {
		return e.maxConc, defaultScope
	}
	if e.cluster == nil || e.cluster.nodes == nil {
		return defaultConc, defaultScope
	}
	node, err := e.cluster.nodes.Get(ctx, e.nodeID)
	if err != nil || node == nil {
		return defaultConc, defaultScope
	}

	scope := defaultScope
	if node.ProviderScope != "" {
		var parsed []string
		if err := json.Unmarshal([]byte(node.ProviderScope), &parsed); err == nil && len(parsed) > 0 {
			scope = parsed
		}
	}
	conc := node.MaxConcurrency
	if conc <= 0 {
		conc = defaultConc
	}
	return conc, scope
}
