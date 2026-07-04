package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/logger"
)

// ClusterService 集群高阶能力：
//   - 节点注册（admin 后台）
//   - 主控自身 embedded agent 路由
//   - 把 asset_key 解析成「就近边缘下载 URL」
//
// 它本身不持有 agent 进程逻辑（那个住在 cmd/agent）。
type ClusterService struct {
	nodes           *repo.ClusterNodeRepo
	loc             *repo.DownloadLocatorRepo
	sys             *SystemConfigService
	aes             *crypto.AESGCM
	bootstrapSecret []byte // 用于签发 / 校验 agent bootstrap token

	ping PingProbe // 反向探活的失败计数器，可选；admin 列表用
}

// NewClusterService 构造。
func NewClusterService(nodes *repo.ClusterNodeRepo, loc *repo.DownloadLocatorRepo, sys *SystemConfigService, aes *crypto.AESGCM) *ClusterService {
	return &ClusterService{
		nodes: nodes,
		loc:   loc,
		sys:   sys,
		aes:   aes,
	}
}

// SetBootstrapSecret 主控启动时注入 KLEIN_CLUSTER_BOOTSTRAP_SECRET（明文）。
// 没注入时 IssueBootstrap / Handshake 都会拒绝。
func (s *ClusterService) SetBootstrapSecret(secret []byte) {
	if s == nil {
		return
	}
	s.bootstrapSecret = append(s.bootstrapSecret[:0], secret...)
}

// PingProbe 抽象：admin 显示「连续探活失败次数」时查询用。
// 由 ClusterMaintenance 实现并通过 setPingProbe 注入。
type PingProbe interface {
	PingFails(nodeID string) int
}

func (s *ClusterService) setPingProbe(p PingProbe) {
	if s == nil {
		return
	}
	s.ping = p
}

// NodePingFails 返回连续探活失败计数；未启用反向 ping 时返回 0。
func (s *ClusterService) NodePingFails(nodeID string) int {
	if s == nil || s.ping == nil {
		return 0
	}
	return s.ping.PingFails(nodeID)
}

// EmbeddedAlive 跨进程探测：主控进程是否有 EmbeddedAgent 在跑。
//
// 实现方式：读 cluster_node[control-main]，检查 status 与 last_heartbeat_at；
// EmbeddedAgent 启动后会按 30s 节奏把 control-main 行的 last_heartbeat_at 刷新。
// 这样 admin / api / openai / worker 任一进程都能看到统一视图。
//
// 任意一处出错都按"没有 embedded"兜底，避免误吞掉 inline 流量。
func (s *ClusterService) EmbeddedAlive(ctx context.Context) bool {
	if s == nil || s.nodes == nil {
		return false
	}
	n, err := s.nodes.Get(ctx, model.ClusterEmbeddedNodeID)
	if err != nil || n == nil {
		return false
	}
	if n.Status == model.ClusterNodeDisabled || n.Status == model.ClusterNodeRevoked {
		return false
	}
	if n.LastHeartbeatAt == nil {
		return false
	}
	dead := s.HeartbeatDead(ctx)
	if dead <= 0 {
		dead = defaultHBDeadSec
	}
	return time.Since(*n.LastHeartbeatAt) <= time.Duration(dead)*time.Second
}

// 配置 key / 默认值
const (
	cfgClusterEnabled        = "cluster.enabled"
	cfgClusterTicketTTL      = "cluster.ticket_ttl_sec"
	cfgClusterHeartbeatDead  = "cluster.heartbeat_dead_sec"
	cfgClusterLeaseTTL       = "cluster.lease_ttl_sec"

	defaultTicketTTL     = 300
	defaultHBDeadSec     = 90
	defaultLeaseTTLSec   = 300
)

// Enabled 全局开关；关闭时所有调用方走旧的本地直读路径。
//
// 兼容两种存储形态：
//   - 老形态（migration seed）：{"enabled": true}
//   - 新形态（UI 写入）：bare bool / "true"
func (s *ClusterService) Enabled(ctx context.Context) bool {
	if s == nil || s.sys == nil {
		return false
	}
	if v := s.sys.GetJSON(ctx, cfgClusterEnabled); v != nil {
		if b, ok := v["enabled"].(bool); ok {
			return b
		}
	}
	return s.sys.GetBool(ctx, cfgClusterEnabled, false)
}

// TicketTTL 返回下载 ticket 过期秒数。兼容 {seconds: N} 与 bare int。
func (s *ClusterService) TicketTTL(ctx context.Context) time.Duration {
	return time.Duration(clusterSec(s, ctx, cfgClusterTicketTTL, defaultTicketTTL)) * time.Second
}

// HeartbeatDead 心跳静默超过该秒数即视为掉线。
func (s *ClusterService) HeartbeatDead(ctx context.Context) int {
	return clusterSec(s, ctx, cfgClusterHeartbeatDead, defaultHBDeadSec)
}

// LeaseTTL 默认 lease 时长。
func (s *ClusterService) LeaseTTL(ctx context.Context) time.Duration {
	return time.Duration(clusterSec(s, ctx, cfgClusterLeaseTTL, defaultLeaseTTLSec)) * time.Second
}

// clusterSec 读 system_config[key]；优先解析 {seconds: N}，回退 bare int。
// fallback 在 service 启动早期 / db 没就绪时兜底。
func clusterSec(s *ClusterService, ctx context.Context, key string, fallback int) int {
	if s == nil || s.sys == nil {
		return fallback
	}
	if v := s.sys.GetJSON(ctx, key); v != nil {
		if n, ok := v["seconds"].(float64); ok && n > 0 {
			return int(n)
		}
	}
	if n := s.sys.GetInt(ctx, key, int64(fallback)); n > 0 {
		return int(n)
	}
	return fallback
}

// RecordLocalLocator 在主控自身（embedded agent）写一条 locator，asset_key 形如「<task_id>/<seq>」。
// rel_path 是相对 KLEIN_STORAGE_ROOT 的路径。
func (s *ClusterService) RecordLocalLocator(ctx context.Context, kind, key, relPath string, size int64, mime string) {
	if s == nil || s.loc == nil || key == "" || relPath == "" {
		return
	}
	if kind == "" {
		kind = model.AssetKindGen
	}
	loc := &model.DownloadLocator{
		AssetKind: kind,
		AssetKey:  key,
		NodeID:    model.ClusterEmbeddedNodeID,
		RelPath:   relPath,
		Status:    model.LocatorActive,
	}
	if size > 0 {
		loc.SizeBytes = &size
	}
	if mime != "" {
		loc.MIME = &mime
	}
	if err := s.loc.Upsert(ctx, loc); err != nil {
		logger.FromCtx(ctx).Warn("cluster.locator.upsert_failed",
			zap.String("key", key), zap.String("rel", relPath), zap.Error(err))
	}
}

// RecordRemoteLocator agent 上报结果时调用，写一条远端节点的 locator。
func (s *ClusterService) RecordRemoteLocator(ctx context.Context, nodeID, kind, key, relPath string, size int64, sha string, mime string) error {
	if s == nil || s.loc == nil {
		return errors.New("locator repo unset")
	}
	if nodeID == "" || key == "" || relPath == "" {
		return errors.New("missing node/key/rel")
	}
	if kind == "" {
		kind = model.AssetKindGen
	}
	loc := &model.DownloadLocator{
		AssetKind: kind,
		AssetKey:  key,
		NodeID:    nodeID,
		RelPath:   relPath,
		Status:    model.LocatorActive,
	}
	if size > 0 {
		loc.SizeBytes = &size
	}
	if mime != "" {
		loc.MIME = &mime
	}
	if sha != "" {
		loc.SHA256 = &sha
	}
	return s.loc.Upsert(ctx, loc)
}

// ResolveDownload 根据 asset_key 找一个最优的边缘节点并签 ticket。
// 返回值：
//   - url   非空表示应当 302 到该 URL；空字符串表示「就在主控本地」，调用方走 c.File()
//   - localRel 当 url=="" 时，是主控本地的相对路径（KLEIN_STORAGE_ROOT 下）
//   - err   异常（DB 错误等）
func (s *ClusterService) ResolveDownload(ctx context.Context, kind, key string) (url, localRel string, err error) {
	if s == nil || s.loc == nil {
		return "", "", nil
	}
	if kind == "" {
		kind = model.AssetKindGen
	}
	locs, err := s.loc.ListByAsset(ctx, kind, key)
	if err != nil {
		return "", "", err
	}
	if len(locs) == 0 {
		// 没记录 locator → 兼容路径（旧任务或单机模式）
		return "", "", nil
	}

	// 集群关时，只走主控本地
	enabled := s.Enabled(ctx)

	// 优先主控本地
	for _, l := range locs {
		if l.NodeID == model.ClusterEmbeddedNodeID {
			return "", l.RelPath, nil
		}
	}
	if !enabled {
		return "", "", nil
	}

	dead := s.HeartbeatDead(ctx)
	cands, err := s.nodes.ListDownloadCapable(ctx, dead)
	if err != nil {
		return "", "", err
	}
	nodeIndex := make(map[string]*model.ClusterNode, len(cands))
	for _, n := range cands {
		nodeIndex[n.NodeID] = n
	}

	var pool []downloadCand
	for _, l := range locs {
		n, ok := nodeIndex[l.NodeID]
		if !ok {
			continue
		}
		if n.PublicHost == "" {
			continue
		}
		w := n.Weight
		if w <= 0 {
			w = 1
		}
		// 把 inflight 高的节点权重打折
		if n.LastInflight > 0 && n.MaxConcurrency > 0 {
			ratio := float64(n.LastInflight) / float64(n.MaxConcurrency)
			if ratio > 0.95 {
				w = w / 4
			} else if ratio > 0.75 {
				w = w / 2
			}
			if w <= 0 {
				w = 1
			}
		}
		pool = append(pool, downloadCand{loc: l, node: n, w: w})
	}
	if len(pool) == 0 {
		return "", "", nil
	}

	// 加权随机；以 asset_key 做 deterministic seed，保证同一资源大概率落到同一节点，利于浏览器缓存命中
	chosen := weightedPickCand(key, pool)

	secret, err := s.decryptSecret(chosen.node.HMACSecretEnc)
	if err != nil || len(secret) == 0 {
		logger.FromCtx(ctx).Warn("cluster.node.secret_unavailable",
			zap.String("node", chosen.node.NodeID), zap.Error(err))
		return "", "", nil
	}
	exp := time.Now().Add(s.TicketTTL(ctx)).Unix()
	tk, err := SignTicket(chosen.node.NodeID, secret, TicketPayload{
		Kind: kind, Key: key, Exp: exp,
	})
	if err != nil {
		return "", "", err
	}
	return BuildDownloadURL(chosen.node.PublicHost, chosen.node.NodeID, tk.Token), "", nil
}

// MarkTainted 用户态访问边缘节点失败后调用，让下次路由跳过该节点。
func (s *ClusterService) MarkTainted(ctx context.Context, kind, key, nodeID string) {
	if s == nil || s.loc == nil {
		return
	}
	_ = s.loc.MarkTainted(ctx, kind, key, nodeID)
}

// ── private helpers ────────────────────────────────────────

type downloadCand struct {
	loc  *model.DownloadLocator
	node *model.ClusterNode
	w    int
}

func weightedPickCand(key string, in []downloadCand) downloadCand {
	total := 0
	for _, c := range in {
		total += c.w
	}
	if total <= 0 {
		return in[0]
	}
	// deterministic seed: hash(key) 模 total，保证同 asset_key 大概率落同节点（CDN 缓存友好）
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	pos := int(h.Sum64()%uint64(total)) + 1
	for _, c := range in {
		pos -= c.w
		if pos <= 0 {
			return c
		}
	}
	return in[len(in)-1]
}

func (s *ClusterService) decryptSecret(enc []byte) ([]byte, error) {
	if len(enc) == 0 {
		return nil, errors.New("empty secret")
	}
	if s.aes == nil {
		return nil, errors.New("aes unset")
	}
	return s.aes.Decrypt(enc)
}

// EncryptSecret 节点注册 / 重发 secret 时使用。
func (s *ClusterService) EncryptSecret(plain []byte) ([]byte, error) {
	if s.aes == nil {
		return nil, errors.New("aes unset")
	}
	return s.aes.Encrypt(plain)
}

// ── 节点注册 / 握手 / 心跳 ───────────────────────────────────

// NodeUpsertReq 后台 / 内部代码统一用的节点注册参数。
type NodeUpsertReq struct {
	NodeID         string   `json:"node_id"`
	DisplayName    string   `json:"display_name"`
	Role           string   `json:"role"`
	PublicHost     string   `json:"public_host"`
	InternalHost   string   `json:"internal_host,omitempty"`
	ProviderScope  []string `json:"provider_scope"`
	Weight         int      `json:"weight"`
	MaxConcurrency int      `json:"max_concurrency"`
	DownloadOnly   bool     `json:"download_only"`
	AllowedIPs     string   `json:"allowed_ips,omitempty"`
}

// RegisterNode 注册一个新节点；如已存在则更新元信息（不动 status / secret）。
// 返回值 bootstrapToken 仅在「首次注册或 secret 已吊销」时非空——前端 **只展示一次**。
func (s *ClusterService) RegisterNode(ctx context.Context, in NodeUpsertReq) (node *model.ClusterNode, bootstrapToken string, err error) {
	if s == nil || s.nodes == nil {
		return nil, "", errors.New("cluster service not ready")
	}
	if strings.TrimSpace(in.NodeID) == "" {
		return nil, "", errors.New("node_id required")
	}
	if in.Role == "" {
		in.Role = model.ClusterRoleAgent
	}
	if in.Weight <= 0 {
		in.Weight = 100
	}
	if in.MaxConcurrency <= 0 {
		in.MaxConcurrency = 16
	}
	if len(in.ProviderScope) == 0 {
		in.ProviderScope = []string{"gpt", "grok", "adobe", "pic2api", "flowmusic"}
	}
	scopeJSON, _ := json.Marshal(in.ProviderScope)

	prev, _ := s.nodes.Get(ctx, in.NodeID)
	n := &model.ClusterNode{
		NodeID:         in.NodeID,
		DisplayName:    in.DisplayName,
		Role:           in.Role,
		PublicHost:     strings.TrimRight(in.PublicHost, "/"),
		InternalHost:   in.InternalHost,
		ProviderScope:  string(scopeJSON),
		Weight:         in.Weight,
		MaxConcurrency: in.MaxConcurrency,
		AllowedIPs:     in.AllowedIPs,
	}
	if in.DownloadOnly {
		n.DownloadOnly = 1
	}

	needBootstrap := false
	if prev == nil {
		// 全新注册
		n.Status = model.ClusterNodePending
		needBootstrap = true
	} else {
		// 已存在：保留 secret 与 status，仅更新元信息
		n.Status = prev.Status
		n.HMACSecretEnc = prev.HMACSecretEnc
		n.BootstrapUsed = prev.BootstrapUsed
		n.LastHeartbeatAt = prev.LastHeartbeatAt
		n.LastInflight = prev.LastInflight
		n.LastIP = prev.LastIP
		n.Version = prev.Version
		n.CreatedAt = prev.CreatedAt
		// secret 已吊销时允许重新签发 token
		if len(prev.HMACSecretEnc) == 0 && (prev.Status == model.ClusterNodeRevoked || prev.Status == model.ClusterNodePending) {
			needBootstrap = true
			n.Status = model.ClusterNodePending
			n.BootstrapUsed = 0
		}
	}
	if err := s.nodes.Upsert(ctx, n); err != nil {
		return nil, "", err
	}
	if needBootstrap {
		tok, err := s.IssueBootstrap(in.NodeID)
		if err != nil {
			return n, "", err
		}
		bootstrapToken = tok
	}
	return n, bootstrapToken, nil
}

// IssueBootstrap 重新发一个 bootstrap token（运维场景：节点忘了 token）。
// 该方法 **不重置** 当前 secret —— agent 需用旧 secret 继续工作；新 token 用于新建机器。
func (s *ClusterService) IssueBootstrap(nodeID string) (string, error) {
	if len(s.bootstrapSecret) == 0 {
		return "", errors.New("bootstrap secret not set; KLEIN_CLUSTER_BOOTSTRAP_SECRET missing")
	}
	return SignBootstrapToken(s.bootstrapSecret, nodeID)
}

// HandshakeResult 主控握手返回给 agent 的全部运行时配置。
type HandshakeResult struct {
	NodeID         string   `json:"node_id"`
	HMACSecret     string   `json:"hmac_secret"` // base64 raw url；只在握手响应里出现一次
	ProviderScope  []string `json:"provider_scope"`
	MaxConcurrency int      `json:"max_concurrency"`
	HeartbeatSec   int      `json:"heartbeat_sec"`
	LeaseSec       int      `json:"lease_sec"`
}

// FinishHandshake 校验 bootstrap token，给该节点签发一个新的 hmac_secret 入库（密文），并把明文返回。
//   token: agent 启动时携带的引导 token
//   ip:    agent 出口 IP（可选，仅记录）
//   ver:   agent 版本号（可选，仅记录）
func (s *ClusterService) FinishHandshake(ctx context.Context, token, ip, ver string) (*HandshakeResult, error) {
	if s == nil || s.nodes == nil {
		return nil, errors.New("cluster service not ready")
	}
	if len(s.bootstrapSecret) == 0 {
		return nil, errors.New("bootstrap secret missing")
	}
	p, err := VerifyBootstrapToken(s.bootstrapSecret, token, time.Hour)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	n, err := s.nodes.Get(ctx, p.NodeID)
	if err != nil {
		return nil, fmt.Errorf("node %s not found", p.NodeID)
	}
	if n.Status == model.ClusterNodeRevoked {
		return nil, errors.New("node revoked")
	}
	// 生成 32 字节 secret
	secret, err := crypto.RandomBytes(32)
	if err != nil {
		return nil, err
	}
	enc, err := s.EncryptSecret(secret)
	if err != nil {
		return nil, err
	}
	if err := s.nodes.SetSecret(ctx, p.NodeID, enc, model.ClusterNodeEnabled); err != nil {
		return nil, err
	}
	// 记录握手元信息
	if ip != "" || ver != "" {
		_ = s.nodes.Heartbeat(ctx, p.NodeID, ip, ver, 0)
	}
	scope := []string{}
	_ = json.Unmarshal([]byte(n.ProviderScope), &scope)
	return &HandshakeResult{
		NodeID:         n.NodeID,
		HMACSecret:     base64Encode(secret),
		ProviderScope:  scope,
		MaxConcurrency: n.MaxConcurrency,
		HeartbeatSec:   5,
		LeaseSec:       int(s.LeaseTTL(ctx) / time.Second),
	}, nil
}

// VerifyAgentRequest 校验来自 agent 的 HTTP 请求 HMAC 签名；nodeID 来自 X-Klein-Node 头。
// 返回该节点的元信息（含明文 secret），调用方拿到后可放入 ctx 传给业务层。
func (s *ClusterService) VerifyAgentRequest(ctx context.Context, nodeID, ts, sig, method, path string, body []byte) (*model.ClusterNode, []byte, error) {
	if s == nil || s.nodes == nil {
		return nil, nil, errors.New("cluster service not ready")
	}
	if nodeID == "" || sig == "" || ts == "" {
		return nil, nil, errors.New("missing X-Klein-* headers")
	}
	n, err := s.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, nil, fmt.Errorf("node %s not found", nodeID)
	}
	if n.Status != model.ClusterNodeEnabled && n.Status != model.ClusterNodeMaintenance {
		return nil, nil, fmt.Errorf("node %s not active (status=%d)", nodeID, n.Status)
	}
	secret, err := s.decryptSecret(n.HMACSecretEnc)
	if err != nil || len(secret) == 0 {
		return nil, nil, errors.New("node secret unavailable")
	}
	signer := NewNodeSigner(nodeID, secret)
	if err := signer.Verify(SignedRequest{Method: method, Path: path, Body: body}, ts, sig); err != nil {
		return nil, nil, err
	}
	return n, secret, nil
}

// AgentHeartbeat 写心跳；inflight 由调用方传入（agent 心跳消息携带）。
func (s *ClusterService) AgentHeartbeat(ctx context.Context, nodeID, ip, version string, inflight int) error {
	if s == nil || s.nodes == nil {
		return errors.New("cluster service not ready")
	}
	return s.nodes.Heartbeat(ctx, nodeID, ip, version, inflight)
}

// SetNodeStatus 后台改节点状态（启用 / 禁用 / 维护中）。吊销请用 RevokeNode。
func (s *ClusterService) SetNodeStatus(ctx context.Context, nodeID string, status int8) error {
	if status == model.ClusterNodeRevoked {
		return errors.New("use RevokeNode for revoke")
	}
	return s.nodes.UpdateStatus(ctx, nodeID, status)
}

// RevokeNode 吊销节点：secret 置空 + status=9，agent 后续所有 /cluster/* 调用都会失败。
func (s *ClusterService) RevokeNode(ctx context.Context, nodeID string) error {
	return s.nodes.Revoke(ctx, nodeID)
}

// DeleteNode 物理删除节点 + 清掉它持有的所有 download_locator。
// 注意：吊销后再删除是更安全的做法。
func (s *ClusterService) DeleteNode(ctx context.Context, nodeID string) error {
	if nodeID == model.ClusterEmbeddedNodeID {
		return errors.New("cannot delete embedded control-main node")
	}
	if err := s.loc.DeleteByNode(ctx, nodeID); err != nil {
		logger.FromCtx(ctx).Warn("cluster.delete.locator_clean_failed", zap.String("node", nodeID), zap.Error(err))
	}
	return s.nodes.Delete(ctx, nodeID)
}

// ListNodes 后台分页。
func (s *ClusterService) ListNodes(ctx context.Context, f repo.ClusterNodeFilter) ([]*model.ClusterNode, int64, error) {
	return s.nodes.List(ctx, f)
}

// ListActiveAgentsForProvider 返回 provider_scope 包含 providerCode、status=Enabled、
// 心跳在 deadSec 内的所有 agent / edge 节点；用于调度决策"是否走集群"。
//   - 排除 download_only=1 节点（这些只服务下载，不参与 lease）。
//   - control-main 自身也排除（它走 inline runTask，不通过 lease 通道）。
func (s *ClusterService) ListActiveAgentsForProvider(ctx context.Context, providerCode string, deadSec int) ([]*model.ClusterNode, error) {
	if s == nil || s.nodes == nil {
		return nil, nil
	}
	if deadSec <= 0 {
		deadSec = 60
	}
	all, err := s.nodes.ListActiveByProvider(ctx, providerCode, deadSec)
	if err != nil {
		return nil, err
	}
	out := make([]*model.ClusterNode, 0, len(all))
	for _, n := range all {
		if n.NodeID == model.ClusterEmbeddedNodeID {
			continue
		}
		if n.DownloadOnly == 1 {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// base64Encode RawURL.
func base64Encode(b []byte) string {
	return base64URLNoPad(b)
}
