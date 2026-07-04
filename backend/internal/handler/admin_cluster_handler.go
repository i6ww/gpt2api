// Package handler · admin /cluster/* endpoints
//
// 路由由 admin router 挂载在 `/admin/api/v1/cluster/`，分两组：
//
//   1. **管理类**（需要 admin JWT）：
//      GET    /nodes              列表
//      POST   /nodes              新增 / 重发 bootstrap token
//      PUT    /nodes/:id          编辑元信息
//      POST   /nodes/:id/status   启停 / 维护
//      POST   /nodes/:id/revoke   吊销 secret
//      DELETE /nodes/:id          删除 + 清 locator
//
//   2. **节点内部接口**（需要 ClusterHMAC 中间件，由 agent 调）：
//      POST   /handshake          换 hmac secret
//      POST   /heartbeat          心跳
//      POST   /lease              拉一批待跑任务
//      POST   /result             上报结果
package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

type AdminClusterHandler struct {
	cluster    *service.ClusterService
	gen        *service.GenerationService
	controlURL string // 用于 admin 创建节点时回显给运维（agent 启动需要）
}

func NewAdminClusterHandler(c *service.ClusterService, g *service.GenerationService, controlURL string) *AdminClusterHandler {
	return &AdminClusterHandler{cluster: c, gen: g, controlURL: controlURL}
}

// ── 1. 管理类 ───────────────────────────────────────────────

func (h *AdminClusterHandler) ListNodes(c *gin.Context) {
	var req dto.AdminClusterNodeListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	nodes, total, err := h.cluster.ListNodes(c.Request.Context(), repo.ClusterNodeFilter{
		Role:     req.Role,
		Status:   req.Status,
		Keyword:  req.Keyword,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	out := make([]dto.AdminClusterNodeItem, 0, len(nodes))
	now := time.Now()
	for _, n := range nodes {
		item := h.convertNode(n, now)
		out = append(out, item)
	}
	response.Page(c, out, total, page, pageSize)
}

func (h *AdminClusterHandler) UpsertNode(c *gin.Context) {
	var req dto.AdminClusterNodeUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	n, token, err := h.cluster.RegisterNode(c.Request.Context(), service.NodeUpsertReq{
		NodeID:         req.NodeID,
		DisplayName:    req.DisplayName,
		Role:           req.Role,
		PublicHost:     req.PublicHost,
		InternalHost:   req.InternalHost,
		ProviderScope:  req.ProviderScope,
		Weight:         req.Weight,
		MaxConcurrency: req.MaxConcurrency,
		DownloadOnly:   req.DownloadOnly,
		AllowedIPs:     req.AllowedIPs,
	})
	if err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	response.OK(c, dto.AdminClusterNodeUpsertResp{
		Node:           h.convertNode(n, time.Now()),
		BootstrapToken: token,
		ControlURL:     h.controlURL,
	})
}

func (h *AdminClusterHandler) UpdateNodeStatus(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	var req dto.AdminClusterNodeStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	if err := h.cluster.SetNodeStatus(c.Request.Context(), id, req.Status); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	response.OK(c, gin.H{"ok": true})
}

func (h *AdminClusterHandler) RevokeNode(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if err := h.cluster.RevokeNode(c.Request.Context(), id); err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	response.OK(c, gin.H{"ok": true})
}

func (h *AdminClusterHandler) DeleteNode(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if err := h.cluster.DeleteNode(c.Request.Context(), id); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// TaintLocator 手动标记某节点上的资源损坏 / 失联，后续不再路由到该节点。
//
// 触发场景：
//   1. 用户反馈某张图 / 视频 404 / 时不时 5xx，admin 临时手动屏蔽（吊销整台节点过激）
//   2. 节点磁盘故障，部分文件丢失，运维一次性把这些 key 标黑
//
// 实现：将 download_locator.status 置为 tainted（2），ResolveDownload 自动跳过。
// 该操作可逆 —— ApplyAgentResult 再次写入会重置回 active。
func (h *AdminClusterHandler) TaintLocator(c *gin.Context) {
	var req dto.AdminClusterTaintReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	kind := strings.TrimSpace(req.AssetKind)
	if kind == "" {
		kind = model.AssetKindGen
	}
	h.cluster.MarkTainted(c.Request.Context(), kind, req.AssetKey, req.NodeID)
	response.OK(c, gin.H{"ok": true})
}

// ReissueBootstrap 当节点首次拿到的 token 过期 / 丢失 / 想重置时调用。
//
// **注意**：这里不会清掉旧 secret —— 旧 secret 仍然有效（agent 可继续工作）。
// 如果运维想强制让旧 agent 失联，请用 RevokeNode（或先 revoke 再 reissue）。
func (h *AdminClusterHandler) ReissueBootstrap(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	tok, err := h.cluster.IssueBootstrap(id)
	if err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	response.OK(c, gin.H{
		"node_id":         id,
		"bootstrap_token": tok,
		"control_url":     h.controlURL,
	})
}

// ── 2. agent 内部接口 ───────────────────────────────────────

// Handshake 不走 ClusterHMAC（首次握手没有 secret），用 bootstrap token 校验。
func (h *AdminClusterHandler) Handshake(c *gin.Context) {
	var req dto.ClusterHandshakeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	res, err := h.cluster.FinishHandshake(c.Request.Context(), req.Token, c.ClientIP(), req.Version)
	if err != nil {
		response.Fail(c, errcode.Unauthorized.Wrap(err))
		return
	}
	response.OK(c, dto.ClusterHandshakeResp{
		NodeID:         res.NodeID,
		HMACSecret:     res.HMACSecret,
		ProviderScope:  res.ProviderScope,
		MaxConcurrency: res.MaxConcurrency,
		HeartbeatSec:   res.HeartbeatSec,
		LeaseSec:       res.LeaseSec,
		StorageRoot:    "/var/klein/storage/public",
	})
}

func (h *AdminClusterHandler) Heartbeat(c *gin.Context) {
	n := middleware.ClusterNodeFromCtx(c)
	if n == nil {
		response.Fail(c, errcode.Unauthorized)
		return
	}
	var req dto.ClusterHeartbeatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	if err := h.cluster.AgentHeartbeat(c.Request.Context(), n.NodeID, c.ClientIP(), req.Version, req.Inflight); err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	response.OK(c, gin.H{"ok": true, "ts": time.Now().Unix()})
}

func (h *AdminClusterHandler) Lease(c *gin.Context) {
	n := middleware.ClusterNodeFromCtx(c)
	if n == nil {
		response.Fail(c, errcode.Unauthorized)
		return
	}
	var req dto.ClusterLeaseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// 允许空 body：用节点自己的默认
		req = dto.ClusterLeaseReq{}
	}
	max := req.Max
	if max <= 0 {
		max = 4
	}
	scope := req.Providers
	if len(scope) == 0 {
		var s []string
		_ = json.Unmarshal([]byte(n.ProviderScope), &s)
		scope = s
	}
	tasks, err := h.cluster.LeaseTasks(c.Request.Context(), h.gen, n.NodeID, scope, max)
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	response.OK(c, gin.H{"tasks": tasks})
}

func (h *AdminClusterHandler) Result(c *gin.Context) {
	n := middleware.ClusterNodeFromCtx(c)
	if n == nil {
		response.Fail(c, errcode.Unauthorized)
		return
	}
	var req dto.ClusterResultReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	results := make([]service.ResultIn, 0, len(req.Results))
	for _, r := range req.Results {
		results = append(results, service.ResultIn{
			Seq: r.Seq, URL: r.URL, RelPath: r.RelPath, ThumbRel: r.ThumbRel,
			Width: r.Width, Height: r.Height, DurationMs: r.DurationMs, SizeBytes: r.SizeBytes,
			SHA256: r.SHA256, MIME: r.MIME, Meta: r.Meta,
		})
	}
	if err := h.cluster.ApplyAgentResult(c.Request.Context(), h.gen, n.NodeID, req.TaskID, service.ResultReport{
		Status: req.Status, Error: req.Error, Cost: req.Cost, Results: results,
	}); err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// ── helpers ─────────────────────────────────────────────────

func (h *AdminClusterHandler) convertNode(n *model.ClusterNode, now time.Time) dto.AdminClusterNodeItem {
	scope := []string{}
	_ = json.Unmarshal([]byte(n.ProviderScope), &scope)

	item := dto.AdminClusterNodeItem{
		NodeID:          n.NodeID,
		DisplayName:     n.DisplayName,
		Role:            n.Role,
		PublicHost:      n.PublicHost,
		InternalHost:    n.InternalHost,
		ProviderScope:   scope,
		Weight:          n.Weight,
		MaxConcurrency:  n.MaxConcurrency,
		DownloadOnly:    n.DownloadOnly == 1,
		AllowedIPs:      n.AllowedIPs,
		Status:          n.Status,
		StatusLabel:     statusLabelFor(n, now),
		HasSecret:       len(n.HMACSecretEnc) > 0,
		BootstrapUsed:   n.BootstrapUsed == 1,
		LastHeartbeatAt: n.LastHeartbeatAt,
		LastInflight:    n.LastInflight,
		LastIP:          n.LastIP,
		Version:         n.Version,
		CreatedAt:       n.CreatedAt,
		UpdatedAt:       n.UpdatedAt,
	}
	if n.LastHeartbeatAt != nil {
		age := int(now.Sub(*n.LastHeartbeatAt).Seconds())
		item.HeartbeatAgeSec = &age
	}
	if h != nil && h.cluster != nil {
		item.PingFailStreak = h.cluster.NodePingFails(n.NodeID)
	}
	return item
}

// Overview GET /admin/api/v1/cluster/overview —— ConfigPage 集群卡片专用。
// 返回控制面看到的实时摘要：embedded 是否活、节点数、Maintenance 数等。
func (h *AdminClusterHandler) Overview(c *gin.Context) {
	ctx := c.Request.Context()
	all, _, err := h.cluster.ListNodes(ctx, repo.ClusterNodeFilter{Page: 1, PageSize: 500})
	if err != nil {
		response.Fail(c, errcode.DBError.Wrap(err))
		return
	}
	now := time.Now()
	deadSec := h.cluster.HeartbeatDead(ctx)
	if deadSec <= 0 {
		deadSec = 90
	}
	var (
		embeddedNode *model.ClusterNode
		online       int
		maintenance  int
	)
	for _, n := range all {
		if n.NodeID == model.ClusterEmbeddedNodeID {
			embeddedNode = n
			continue
		}
		switch n.Status {
		case model.ClusterNodeMaintenance:
			maintenance++
		case model.ClusterNodeEnabled:
			if n.LastHeartbeatAt != nil && now.Sub(*n.LastHeartbeatAt) <= time.Duration(deadSec)*time.Second {
				online++
			}
		}
	}
	ov := dto.AdminClusterOverview{
		Enabled:          h.cluster.Enabled(ctx),
		TotalNodes:       len(all),
		OnlineAgents:     online,
		MaintenanceNodes: maintenance,
		LeaseTTLSec:      int(h.cluster.LeaseTTL(ctx).Seconds()),
		HeartbeatDeadSec: deadSec,
		TicketTTLSec:     int(h.cluster.TicketTTL(ctx).Seconds()),
	}
	if embeddedNode != nil {
		ov.EmbeddedHeartbeatAt = embeddedNode.LastHeartbeatAt
		ov.EmbeddedInflight = embeddedNode.LastInflight
		if embeddedNode.LastHeartbeatAt != nil &&
			now.Sub(*embeddedNode.LastHeartbeatAt) <= time.Duration(deadSec)*time.Second {
			ov.EmbeddedAlive = true
		}
	}
	response.OK(c, ov)
}

func statusLabelFor(n *model.ClusterNode, now time.Time) string {
	switch n.Status {
	case model.ClusterNodePending:
		return "待激活"
	case model.ClusterNodeDisabled:
		return "禁用"
	case model.ClusterNodeMaintenance:
		return "维护中"
	case model.ClusterNodeRevoked:
		return "已吊销"
	case model.ClusterNodeEnabled:
		if n.Role == model.ClusterRoleControl {
			return "在线（主控）"
		}
		if n.LastHeartbeatAt == nil {
			return "等待心跳"
		}
		age := now.Sub(*n.LastHeartbeatAt)
		if age <= 90*time.Second {
			return "在线"
		}
		if age <= 10*time.Minute {
			return "掉线"
		}
		return "失联"
	}
	return "未知"
}

// 静态参考避免 IDE 标 net/http 不用（Status 比较辅助）
var _ = http.StatusOK
