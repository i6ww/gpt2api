package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/logger"
)

// ClusterMaintenance 控制面后台守护：
//
//   - 30s 一次 ReclaimExpired：回收 lease 过期的 running 任务（agent 崩溃 / 网络断开）
//   - 5min 一次 locator GC：物理删除 expires_at 已过期的 download_locator 行
//   - 60s 一次 reverse health-ping：对每个 Enabled 节点的 public_host/healthz 发 HEAD，
//     连续 3 次失败把节点踢到 Maintenance 防止再分配 / 再路由；
//     成功一次立刻清零计数（保持 Enabled）。
//
// 仅控制面应该启动一份；agent 不需要跑它。
type ClusterMaintenance struct {
	gen   *GenerationService
	loc   *repo.DownloadLocatorRepo
	nodes *repo.ClusterNodeRepo

	reclaimInterval time.Duration
	gcInterval      time.Duration
	pingInterval    time.Duration
	reapInterval    time.Duration

	pingMaxFail int
	pingTimeout time.Duration
	pingFails   sync.Map // node_id → int 当前连续失败计数

	pingClient *http.Client

	startOnce sync.Once
}

// NewClusterMaintenance 构造。
//   - gen / loc 必填（reclaim & gc 依赖它们）。
//   - nodes 可空（不传则跳过反向 ping）。
func NewClusterMaintenance(gen *GenerationService, loc *repo.DownloadLocatorRepo) *ClusterMaintenance {
	m := &ClusterMaintenance{
		gen:             gen,
		loc:             loc,
		reclaimInterval: 30 * time.Second,
		gcInterval:      5 * time.Minute,
		pingInterval:    60 * time.Second,
		reapInterval:    60 * time.Second,
		pingMaxFail:     3,
		pingTimeout:     5 * time.Second,
		pingClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
	if gen != nil && gen.cluster != nil {
		gen.cluster.setPingProbe(m)
	}
	return m
}

// PingFails 返回某节点当前连续失败次数；外部展示用。
func (m *ClusterMaintenance) PingFails(nodeID string) int {
	if m == nil {
		return 0
	}
	if v, ok := m.pingFails.Load(nodeID); ok {
		if n, ok2 := v.(int); ok2 {
			return n
		}
	}
	return 0
}

// WithNodes 注入 ClusterNodeRepo 开启反向 health-ping。
// nil 静默禁用，保留旧测试用例的兼容性。
func (m *ClusterMaintenance) WithNodes(nodes *repo.ClusterNodeRepo) *ClusterMaintenance {
	if m == nil {
		return m
	}
	m.nodes = nodes
	return m
}

// WithIntervals 仅测试 / debug 用。
func (m *ClusterMaintenance) WithIntervals(reclaim, gc time.Duration) *ClusterMaintenance {
	if reclaim > 0 {
		m.reclaimInterval = reclaim
	}
	if gc > 0 {
		m.gcInterval = gc
	}
	return m
}

// Start 在后台起 ticker；同进程内多次调用只生效一次。
func (m *ClusterMaintenance) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.startOnce.Do(func() {
		go m.reclaimLoop(ctx)
		go m.gcLoop(ctx)
		go m.reapLoop(ctx)
		if m.nodes != nil {
			go m.pingLoop(ctx)
		}
		logger.L().Info("cluster.maintenance.started",
			zap.Duration("reclaim", m.reclaimInterval),
			zap.Duration("gc", m.gcInterval),
			zap.Duration("reap", m.reapInterval),
			zap.Bool("reverse_ping", m.nodes != nil))
	})
}

func (m *ClusterMaintenance) reclaimLoop(ctx context.Context) {
	t := time.NewTicker(m.reclaimInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.runReclaimOnce(ctx)
		}
	}
}

func (m *ClusterMaintenance) runReclaimOnce(ctx context.Context) {
	if m.gen == nil || m.gen.repo == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger.FromCtx(ctx).Error("cluster.reclaim.panic", zap.Any("err", r))
		}
	}()
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := m.gen.repo.ReclaimExpired(c)
	if err != nil {
		logger.FromCtx(ctx).Warn("cluster.reclaim.failed", zap.Error(err))
		return
	}
	if n > 0 {
		logger.FromCtx(ctx).Info("cluster.reclaim.recovered", zap.Int64("count", n))
	}
}

// reapLoop 周期性收尸卡死任务（attempt 撞 ClaimAttemptHardCap 或年龄超阈值），
// SetFailed 并退款。此前 ReapStaleTasks 只在用户翻看历史列表时被动触发，导致
// 上游卡死的僵尸任务长时间空转、反复 reclaim 蚕食号池与 worker 槽位。这里把它
// 提升为控制面后台定时任务，保障高并发下整体吞吐稳定。
func (m *ClusterMaintenance) reapLoop(ctx context.Context) {
	t := time.NewTicker(m.reapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.runReapOnce(ctx)
		}
	}
}

func (m *ClusterMaintenance) runReapOnce(ctx context.Context) {
	if m.gen == nil || m.gen.db == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger.FromCtx(ctx).Error("cluster.reap.panic", zap.Any("err", r))
		}
	}()
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// userID=0 表示全局收尸（不按用户过滤）。
	m.gen.ReapStaleTasks(c, 0)
}

func (m *ClusterMaintenance) gcLoop(ctx context.Context) {
	t := time.NewTicker(m.gcInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.runGCOnce(ctx)
		}
	}
}

func (m *ClusterMaintenance) runGCOnce(ctx context.Context) {
	if m.loc == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger.FromCtx(ctx).Error("cluster.gc.panic", zap.Any("err", r))
		}
	}()
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n, err := m.loc.GC(c, 1000)
	if err != nil {
		logger.FromCtx(ctx).Warn("cluster.gc.failed", zap.Error(err))
		return
	}
	if n > 0 {
		logger.FromCtx(ctx).Info("cluster.gc.removed_locators", zap.Int64("count", n))
	}
}

// pingLoop 反向 health-ping：60s 扫一次启用中的边缘 / agent 节点。
// 失败 pingMaxFail 次连续 → 自动改 Maintenance，避免继续派活 / 路由。
// 恢复探活：一旦 ping 成功且当前状态仍是 Enabled，清零失败计数。
//
// 不主动把 Maintenance 节点改回 Enabled —— 必须人工 / agent 重新 handshake 才恢复，
// 防止一闪一闪的边缘频繁抖动调度结果。
func (m *ClusterMaintenance) pingLoop(ctx context.Context) {
	t := time.NewTicker(m.pingInterval)
	defer t.Stop()
	m.runPingOnce(ctx) // 立刻打一次
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.runPingOnce(ctx)
		}
	}
}

func (m *ClusterMaintenance) runPingOnce(ctx context.Context) {
	if m.nodes == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger.FromCtx(ctx).Error("cluster.ping.panic", zap.Any("err", r))
		}
	}()
	enabled := model.ClusterNodeEnabled
	rows, _, err := m.nodes.List(ctx, repo.ClusterNodeFilter{Status: &enabled, Page: 1, PageSize: 200})
	if err != nil {
		logger.FromCtx(ctx).Warn("cluster.ping.list_failed", zap.Error(err))
		return
	}
	for _, n := range rows {
		// control-main 在本进程内直接活着 → 用 EmbeddedAgent 心跳判断即可，不绕 HTTP
		if n.NodeID == model.ClusterEmbeddedNodeID {
			continue
		}
		// public_host 缺失 → 没法 ping，跳过；状态保持当前
		host := strings.TrimSpace(n.PublicHost)
		if host == "" {
			continue
		}
		go m.pingOne(ctx, n.NodeID, host)
	}
}

func (m *ClusterMaintenance) pingOne(ctx context.Context, nodeID, host string) {
	url := normalizePingURL(host)
	c, cancel := context.WithTimeout(ctx, m.pingTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := m.pingClient.Do(req)
	if err != nil {
		m.recordPingFailure(ctx, nodeID, err.Error())
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable {
		m.recordPingFailure(ctx, nodeID, "status="+resp.Status)
		return
	}
	// 任何 2xx / 3xx / 4xx 都视作"网络可达"。403/404 表示节点活着但路径走错了，
	// 这种情况比连不上更值得保留 Enabled（运维可单独排查）。
	m.pingFails.Store(nodeID, 0)
}

func (m *ClusterMaintenance) recordPingFailure(ctx context.Context, nodeID, reason string) {
	cur := 0
	if v, ok := m.pingFails.Load(nodeID); ok {
		if n, ok2 := v.(int); ok2 {
			cur = n
		}
	}
	cur++
	m.pingFails.Store(nodeID, cur)
	logger.FromCtx(ctx).Warn("cluster.ping.fail",
		zap.String("node", nodeID),
		zap.String("reason", reason),
		zap.Int("streak", cur))
	if cur < m.pingMaxFail {
		return
	}
	// 连续 N 次失败：踢到 Maintenance，并清零计数避免反复改写状态。
	if err := m.nodes.UpdateStatus(ctx, nodeID, model.ClusterNodeMaintenance); err != nil {
		logger.FromCtx(ctx).Warn("cluster.ping.demote_failed",
			zap.String("node", nodeID), zap.Error(err))
		return
	}
	m.pingFails.Store(nodeID, 0)
	logger.FromCtx(ctx).Warn("cluster.ping.demoted_to_maintenance",
		zap.String("node", nodeID))
}

// normalizePingURL 把 public_host 规整为 /healthz 探活地址。
// 接受 "host:port" / "http://host" / "https://host/" / "https://host/sub/" 等任意形态。
func normalizePingURL(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
		h = "https://" + h
	}
	if strings.HasSuffix(h, "/") {
		return h + "healthz"
	}
	return h + "/healthz"
}
