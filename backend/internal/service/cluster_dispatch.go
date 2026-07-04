package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/pkg/logger"
)

// ── 主控 → agent：lease + result 数据结构 ─────────────────────

// LeaseTaskOut 一条 lease 任务在传输面的形态。
//
// **凭证字段（Credential / BaseURL）由主控解密后塞进；agent 用完即弃**。
// 不存数据库、不持久到 agent 磁盘，全部在 agent 进程内存中度过任务生命周期。
type LeaseTaskOut struct {
	TaskID     string         `json:"task_id"`
	Kind       string         `json:"kind"` // image / video / chat
	Mode       string         `json:"mode"`
	ModelCode  string         `json:"model_code"`
	Provider   string         `json:"provider"`
	Prompt     string         `json:"prompt"`
	NegPrompt  string         `json:"neg_prompt,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	RefAssets  []string       `json:"ref_assets,omitempty"`
	Count      int            `json:"count"`
	UserID     uint64         `json:"user_id"`
	AccountID  uint64         `json:"account_id"`
	Credential string         `json:"credential"`
	BaseURL    string         `json:"base_url,omitempty"`
	LeaseUntil int64          `json:"lease_until_unix"`
	CostPoints int64          `json:"cost_points"`
}

// ResultIn agent 跑完后给主控回传的一条结果。
type ResultIn struct {
	Seq        int            `json:"seq"`
	URL        string         `json:"url,omitempty"`      // 可选：远端 URL 兜底
	RelPath    string         `json:"rel_path,omitempty"` // agent 本地文件相对路径
	ThumbRel   string         `json:"thumb_rel,omitempty"`
	Width      *int           `json:"width,omitempty"`
	Height     *int           `json:"height,omitempty"`
	DurationMs *int           `json:"duration_ms,omitempty"`
	SizeBytes  *int64         `json:"size_bytes,omitempty"`
	SHA256     string         `json:"sha256,omitempty"`
	MIME       string         `json:"mime,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

// ResultReport agent 回报。
type ResultReport struct {
	Status  int8       `json:"status"` // 2=succeeded 3=failed
	Error   string     `json:"error,omitempty"`
	Cost    int64      `json:"cost_points,omitempty"`
	Results []ResultIn `json:"results,omitempty"`
}

// ── 主控 lease 与 result 入口（被 admin_cluster_handler 调用） ──

// LeaseTasks 替远端 agent 拉一批任务；解密 account 凭证；写 status=running；返回任务 + 凭证。
func (s *ClusterService) LeaseTasks(ctx context.Context, gen *GenerationService, nodeID string, providerScope []string, max int) ([]*LeaseTaskOut, error) {
	if s == nil || gen == nil || gen.repo == nil {
		return nil, errors.New("cluster/gen service not ready")
	}
	if len(providerScope) == 0 {
		// 兜底：使用节点表里持久化的 scope
		n, err := s.nodes.Get(ctx, nodeID)
		if err == nil {
			_ = json.Unmarshal([]byte(n.ProviderScope), &providerScope)
		}
	}
	if len(providerScope) == 0 {
		return nil, nil
	}
	leaseTTL := s.LeaseTTL(ctx)
	tasks, err := gen.repo.ClaimBatch(ctx, nodeID, providerScope, leaseTTL, max)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	out := make([]*LeaseTaskOut, 0, len(tasks))
	for _, t := range tasks {
		acc, accErr := gen.accFromPool(ctx, t)
		if accErr != nil || acc == nil {
			// 暂时放回队列，等下个周期；同时记 inspector 日志
			logger.FromCtx(ctx).Warn("cluster.lease.account_unavailable",
				zap.String("task", t.TaskID), zap.String("provider", t.Provider), zap.Error(accErr))
			_ = gen.repo.ReleaseClaim(ctx, t.TaskID, nodeID)
			continue
		}
		credPlain, err := gen.aes.Decrypt(acc.CredentialEnc)
		if err != nil {
			logger.FromCtx(ctx).Warn("cluster.lease.cred_decrypt_failed",
				zap.String("task", t.TaskID), zap.Error(err))
			if gen.pool != nil {
				gen.pool.ReleaseForTask(ctx, t.Provider, acc.ID, t.TaskID)
			}
			_ = gen.repo.ReleaseClaim(ctx, t.TaskID, nodeID)
			continue
		}
		// 写 account_id（与单机 SetRunning 行为对齐）
		_ = gen.repo.SetRunningClaim(ctx, t.TaskID, acc.ID)

		var params map[string]any
		_ = json.Unmarshal([]byte(t.Params), &params)
		var refs []string
		if t.RefAssets != nil {
			_ = json.Unmarshal([]byte(*t.RefAssets), &refs)
		}
		baseURL := ""
		if acc.BaseURL != nil {
			baseURL = *acc.BaseURL
		}
		neg := ""
		if t.NegPrompt != nil {
			neg = *t.NegPrompt
		}
		out = append(out, &LeaseTaskOut{
			TaskID:     t.TaskID,
			Kind:       t.Kind,
			Mode:       t.Mode,
			ModelCode:  t.ModelCode,
			Provider:   t.Provider,
			Prompt:     t.Prompt,
			NegPrompt:  neg,
			Params:     params,
			RefAssets:  refs,
			Count:      t.Count,
			UserID:     t.UserID,
			AccountID:  acc.ID,
			Credential: string(credPlain),
			BaseURL:    baseURL,
			LeaseUntil: time.Now().Add(leaseTTL).Unix(),
			CostPoints: t.CostPoints,
		})
	}
	return out, nil
}

// ApplyAgentResult agent 跑完后回报；写 generation_result + download_locator + 更新任务状态。
// 失败时根据 status 退点 / 标记失败。
func (s *ClusterService) ApplyAgentResult(ctx context.Context, gen *GenerationService, nodeID, taskID string, rep ResultReport) error {
	if s == nil || gen == nil || gen.repo == nil {
		return errors.New("service not ready")
	}
	t, err := gen.repo.GetByTaskID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("task %s: %w", taskID, err)
	}
	if t.ClaimNodeID == nil || *t.ClaimNodeID != nodeID {
		// 防止抢错任务
		return fmt.Errorf("task %s not claimed by %s", taskID, nodeID)
	}
	if gen.pool != nil && t.AccountID != nil && *t.AccountID > 0 {
		defer gen.pool.ReleaseForTask(ctx, t.Provider, *t.AccountID, t.TaskID)
	}
	switch rep.Status {
	case model.GenStatusSucceeded:
		// 1) 写 generation_result：URL 走 /api/v1/gen/cached/<rel_path>
		results := make([]*model.GenerationResult, 0, len(rep.Results))
		for _, r := range rep.Results {
			if r.RelPath == "" && r.URL == "" {
				continue
			}
			rel := strings.TrimLeft(r.RelPath, "/")
			gr := &model.GenerationResult{
				TaskID: t.TaskID,
				UserID: t.UserID,
				Kind:   t.Kind,
				Seq:    int8(r.Seq),
			}
			if rel != "" {
				gr.URL = "/api/v1/gen/cached/" + rel
			} else {
				gr.URL = r.URL
			}
			if r.ThumbRel != "" {
				thumb := "/api/v1/gen/cached/" + strings.TrimLeft(r.ThumbRel, "/")
				gr.ThumbURL = &thumb
			}
			gr.Width = r.Width
			gr.Height = r.Height
			gr.DurationMs = r.DurationMs
			gr.SizeBytes = r.SizeBytes
			if r.Meta != nil {
				if b, err := json.Marshal(r.Meta); err == nil {
					m := string(b)
					gr.Meta = &m
				}
			}
			results = append(results, gr)
		}
		if err := gen.repo.SetSucceeded(ctx, taskID, results); err != nil {
			return err
		}
		// 2) 写 download_locator
		for _, r := range rep.Results {
			rel := strings.TrimLeft(r.RelPath, "/")
			if rel == "" {
				continue
			}
			size := int64(0)
			if r.SizeBytes != nil {
				size = *r.SizeBytes
			}
			if err := s.RecordRemoteLocator(ctx, nodeID, model.AssetKindGen, rel, rel, size, r.SHA256, r.MIME); err != nil {
				logger.FromCtx(ctx).Warn("cluster.result.locator_write_failed",
					zap.String("task", taskID), zap.String("node", nodeID), zap.Error(err))
			}
			if r.ThumbRel != "" {
				thumbRel := strings.TrimLeft(r.ThumbRel, "/")
				_ = s.RecordRemoteLocator(ctx, nodeID, model.AssetKindThumb, thumbRel, thumbRel, 0, "", "")
			}
		}
		// 3) Cost / Settle：与单机 runTask 一致，按实际输出档位补扣或退款。
		if rep.Cost > 0 {
			_ = gen.repo.UpdateCost(ctx, taskID, rep.Cost)
		}
		var params map[string]any
		if t.Params != "" {
			_ = json.Unmarshal([]byte(t.Params), &params)
		}
		gen.settleGenerationBilling(ctx, t, nil, params, results, "")
		gen.dispatchTaskWebhook(ctx, taskID)
		return nil
	case model.GenStatusFailed:
		return s.handleAgentFailure(ctx, gen, nodeID, t, rep.Error)
	default:
		return fmt.Errorf("invalid report status %d", rep.Status)
	}
}

// handleAgentFailure 把远端 agent 上报的失败接到主控本地 runTask 同一套
// 错误分类 / 账号冷却 / 重试调度上。
//
// 历史问题：以前这里只 SetFailed + FailRefund，等于 lease 一次失败任务即终结。
// 当 agent 被分到一个 429 的 Grok 号时，它跑完即报错，主控直接判失败，绕不开
// 被限流的账号；同样的任务交给本地 worker 时，runTask 内的 maxAttempts 循环
// 会自动换号继续，所以"开节点反而失败"的现象就出现了。
//
// 修复后：
//  1. 错误字符串包成 error，喂给 inline runTask 同款分类工具：
//     isFatalOAuthRefreshError → 禁用账号；
//     isUsageLimitReachedError → quota_limited 冷却；
//     isTransientProviderPathError → transient 失败计数；
//     其它 → 按 providerCooldown 时长冷却。
//  2. 是否重试：retryableProviderError && attempt < cfg.retry_max_attempts
//     → ReleaseClaim 让任务回 pending，让下一轮 lease 换号 / 换节点重新跑；
//     不可重试 / 达上限 → SetFailed + FailRefund，跟以前一样。
//
// 这样 embedded / 远端 agent 的失败全部接到统一的"换号继续"语义上，集群
// 部署不再丢任务给某个被限流的账号。
func (s *ClusterService) handleAgentFailure(
	ctx context.Context,
	gen *GenerationService,
	nodeID string,
	t *model.GenerationTask,
	errMsg string,
) error {
	cause := errors.New(errMsg)
	log := logger.FromCtx(ctx).With(
		zap.String("task", t.TaskID),
		zap.String("node", nodeID),
		zap.String("provider", t.Provider),
	)

	// 1) 给账号打上 cooldown / disable / transient 标记 —— 即使本次任务
	// 走 ReleaseClaim 回 pending 重试，下一轮 pickAccountForTask 也会因为
	// 此次写入的 cooldown_until 自动跳过这个号，不再撞同样的 429。
	if t.AccountID != nil && *t.AccountID > 0 && gen.pool != nil && gen.pool.repo != nil {
		if acc, accErr := gen.pool.repo.GetByID(ctx, *t.AccountID); accErr == nil && acc != nil {
			switch {
			case isZeroImageReturnedError(cause):
				// 空图多为 prompt / 内容策略问题，不应冷却账号。
				gen.pool.MarkTransientFailed(ctx, acc.ID, errMsg)
			case isFatalOAuthRefreshError(cause):
				gen.disableProviderAccount(ctx, acc, errMsg)
			case isProviderQuotaLimitedError(cause):
				gen.markProviderQuotaLimited(ctx, acc, errMsg, usageLimitResetAt(cause))
			case isTransientProviderPathError(t.Provider, cause):
				gen.pool.MarkTransientFailed(ctx, acc.ID, errMsg)
			case isAdobeNotEntitledError(cause):
				// 远端 agent 把 firefly 结构化 error 序列化成字符串后，errors.As
				// 已经匹配不了；保留这个 case 是为了将来 agent 上报结构化错误时
				// 也能复用。当前路径主要靠 providerCooldown 兜底。
				gen.pool.MarkTransientFailed(ctx, acc.ID, errMsg)
			default:
				gen.markProviderFailed(ctx, acc, errMsg, providerCooldown(cause))
			}
		} else if accErr != nil {
			log.Warn("cluster.result.account_lookup_failed",
				zap.Uint64p("account_id", t.AccountID), zap.Error(accErr))
		}
	}

	// 2) 决定重试还是终结
	maxAttempts := 3
	if gen.cfg != nil {
		maxAttempts = gen.cfg.RetryMaxAttempts(ctx)
	}
	retryable := retryableProviderError(cause)
	canRetry := retryable && int(t.Attempt) < maxAttempts

	if canRetry {
		if err := gen.repo.ReleaseClaim(ctx, t.TaskID, nodeID); err != nil {
			log.Warn("cluster.result.release_failed", zap.Error(err))
			// ReleaseClaim 失败：兜底真失败，避免任务永远卡 running
			_ = gen.repo.SetFailed(ctx, t.TaskID, errMsg)
			if gen.billing != nil {
				_ = gen.billing.FailRefund(ctx, t.TaskID, errMsg)
			}
			gen.dispatchTaskWebhook(ctx, t.TaskID)
			return nil
		}
		log.Info("cluster.result.requeue",
			zap.Int8("attempt", t.Attempt),
			zap.Int("max_attempts", maxAttempts),
			zap.String("reason", truncate(errMsg, 200)))
		return nil
	}

	log.Info("cluster.result.fail_final",
		zap.Int8("attempt", t.Attempt),
		zap.Int("max_attempts", maxAttempts),
		zap.Bool("retryable", retryable),
		zap.String("reason", truncate(errMsg, 200)))
	_ = gen.repo.SetFailed(ctx, t.TaskID, errMsg)
	if gen.billing != nil {
		_ = gen.billing.FailRefund(ctx, t.TaskID, errMsg)
	}
	gen.dispatchTaskWebhook(ctx, t.TaskID)
	return nil
}

// ── GenerationService 配套小工具（被 cluster_dispatch 用） ────

// accFromPool 复刻 GenerationService.runTask 里取池逻辑的「单次取号」精简版。
// agent v1 不带 plan / proxy 重试 —— 拿不到号就放回队列。
func (g *GenerationService) accFromPool(ctx context.Context, t *model.GenerationTask) (*model.Account, error) {
	if g == nil || g.pool == nil {
		return nil, errors.New("pool unset")
	}
	picked, err := g.pickAccountForTask(ctx, t, nil, nil)
	if err != nil {
		return nil, err
	}
	if picked == nil {
		return nil, fmt.Errorf("no account available for provider %s", t.Provider)
	}
	return picked, nil
}
