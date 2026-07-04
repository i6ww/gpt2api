// Package service 生成任务编排：创建 → 预扣 → 调度账号 → 调用 provider → 结算 / 退款。
//
// 当前实现为同步 inline 执行（开发期）。生产建议替换为 asynq 投递到 worker。
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/provider/adobe/firefly"
	"github.com/kleinai/backend/internal/regkit/adoberefresh"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/logger"
)

// GenerationService 生成调度服务。
type GenerationService struct {
	db        *gorm.DB
	repo      *repo.GenerationRepo
	pool      *AccountPool
	billing   *BillingService
	providers map[string]provider.Provider // key: "gpt" / "grok"
	priceFn   PriceFunc
	aes       *crypto.AESGCM // 用于解密 account.credential_enc
	proxySvc  *ProxyService
	cfg       *SystemConfigService
	cost      *CostRecorder   // 可选；nil 时不写 task_cost_log
	cluster   *ClusterService // 可选；nil 时退化为单机本地直读
	webhooks  *WebhookService // 可选；nil 时不发 callback webhook

	// pswebMu / pswebTok 缓存 Adobe PSWeb（PSWebApp1）现铸 token，按 account.ID 复用，
	// 避免每次生成都打一次 IMS。仅 adobe.submit_mode=psweb 时使用。
	pswebMu  sync.Mutex
	pswebTok map[uint64]pswebTokenEntry
}

type pswebTokenEntry struct {
	token string
	exp   time.Time
}

// PriceFunc 模型计费：返回单次成本（点 *100）。
type PriceFunc func(modelCode string, kind provider.Kind, params map[string]any) int64

// NewGenerationService 构造。aes 必须非空（账号凭证加密强制）。
func NewGenerationService(db *gorm.DB, r *repo.GenerationRepo, pool *AccountPool, billing *BillingService, providers map[string]provider.Provider, priceFn PriceFunc, aes *crypto.AESGCM, proxySvc *ProxyService, cfg *SystemConfigService) *GenerationService {
	return &GenerationService{
		db:        db,
		repo:      r,
		pool:      pool,
		billing:   billing,
		providers: providers,
		priceFn:   priceFn,
		aes:       aes,
		proxySvc:  proxySvc,
		cfg:       cfg,
	}
}

// SetCostRecorder 注入上游成本记录器（Phase B 落账）。可重复调用，nil 表示禁用。
func (s *GenerationService) SetCostRecorder(c *CostRecorder) {
	if s == nil {
		return
	}
	s.cost = c
}

// SetClusterService 注入集群服务，开启「资源就近下载」与 locator 持久化。
// 单机部署可保持 nil，行为完全等同旧版本。
func (s *GenerationService) SetClusterService(c *ClusterService) {
	if s == nil {
		return
	}
	s.cluster = c
}

// CreateRequest 创建生成请求 DTO（被 handler 填充）。
type CreateRequest struct {
	UserID    uint64
	APIKeyID  *uint64
	Kind      provider.Kind
	Mode      provider.Mode
	ModelCode string
	Provider  string
	Prompt    string
	NegPrompt string
	Params    map[string]any
	RefAssets []string
	Count     int
	IdemKey   string
	ClientIP  string
}

// Create 同步创建 + 触发任务。返回最终 task。
func (s *GenerationService) Create(ctx context.Context, req CreateRequest) (*model.GenerationTask, error) {
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.IdemKey == "" {
		req.IdemKey = uuid.NewString()
	}
	req.Prompt = sanitizeDBText(req.Prompt)
	req.NegPrompt = sanitizeDBText(req.NegPrompt)
	if s.cfg != nil {
		if err := s.cfg.ValidateKeywordSafe(ctx, req.Prompt, req.NegPrompt); err != nil {
			return nil, err
		}
	}

	if existing, err := s.repo.GetByIdem(ctx, req.UserID, req.IdemKey); err == nil && existing != nil {
		return existing, nil
	}

	// 全局并发准入：pending+running 达到上限时直接拒绝，不创建 task、不预扣费。
	if s.cfg != nil && s.repo != nil {
		limit := s.cfg.OpenAIAdmissionMaxInflight(ctx)
		if limit > 0 {
			active, err := s.repo.CountGlobalActive(ctx)
			if err == nil && active >= limit {
				return nil, errcode.GenRateLimited.WithMsg("Too many concurrent generation requests, please retry later")
			}
		}
	}

	cost := int64(0)
	if s.priceFn != nil {
		cost = s.priceFn(req.ModelCode, req.Kind, req.Params) * int64(req.Count)
	}
	if cost < 0 {
		return nil, errcode.InvalidParam.WithMsg("model price not configured")
	}

	taskID := newULID()
	req.RefAssets = s.normalizeInputRefs(ctx, &model.GenerationTask{TaskID: taskID, Provider: req.Provider, Kind: string(req.Kind)}, req.RefAssets)
	req.Params = compactLargeInlineParams(req.Params)
	paramsJSON, _ := json.Marshal(req.Params)
	var refJSON *string
	if len(req.RefAssets) > 0 {
		b, _ := json.Marshal(req.RefAssets)
		s := string(b)
		refJSON = &s
	}
	t := &model.GenerationTask{
		TaskID:       taskID,
		UserID:       req.UserID,
		Kind:         string(req.Kind),
		Mode:         string(req.Mode),
		ModelCode:    req.ModelCode,
		Prompt:       req.Prompt,
		Params:       string(paramsJSON),
		RefAssets:    refJSON,
		Count:        req.Count,
		CostPoints:   cost,
		IdemKey:      req.IdemKey,
		Provider:     req.Provider,
		Status:       model.GenStatusPending,
		FromAPIKeyID: req.APIKeyID,
	}
	if req.NegPrompt != "" {
		ng := req.NegPrompt
		t.NegPrompt = &ng
	}
	if req.ClientIP != "" {
		ip := req.ClientIP
		t.ClientIP = &ip
	}

	if err := s.repo.Create(ctx, t); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}

	if cost > 0 {
		if err := s.billing.PreDeduct(ctx, PreDeductReq{
			UserID:     req.UserID,
			TaskID:     taskID,
			Kind:       string(req.Kind),
			ModelCode:  req.ModelCode,
			Count:      req.Count,
			UnitPoints: cost / int64(req.Count),
		}); err != nil {
			_ = s.repo.SetFailed(ctx, taskID, err.Error())
			if failed, ferr := s.repo.GetByTaskID(ctx, taskID); ferr == nil && failed != nil {
				s.dispatchTaskWebhook(ctx, taskID)
			}
			return nil, err
		}
	}

	// 如果集群开启且至少有一个在线 agent 的 provider_scope 覆盖了 t.Provider，
	// 任务交给 lease loop 抢锁 —— 不起 inline goroutine 避免与远端 agent 抢同一行。
	// 没有合格 agent 时（关集群 / agent 全离线）回退到本地 inline，保留单机可用性。
	if s.shouldDispatchToCluster(ctx, t) {
		logger.FromCtx(ctx).Info("gen.dispatch.cluster",
			zap.String("task", t.TaskID), zap.String("provider", t.Provider))
		return t, nil
	}
	go s.runTask(context.Background(), t)
	return t, nil
}

// shouldDispatchToCluster 判断当前 provider 是否有 lease 通道可以接手。
// 任何一处 nil / err / 空结果都按"走 inline"兜底，避免引入新的死任务。
//
// 接手者两类：
//   - 远端 agent：cluster_node 表里 status=Enabled + 心跳新鲜 + provider_scope 命中
//   - 主控进程 EmbeddedAgent：cluster_node[control-main] 心跳新鲜（跨进程一致视图）
//
// 任一存在即跳过 inline runTask，让 lease loop 抢锁。
func (s *GenerationService) shouldDispatchToCluster(ctx context.Context, t *model.GenerationTask) bool {
	if s == nil || s.cluster == nil || t == nil {
		return false
	}
	if !s.cluster.Enabled(ctx) {
		return false
	}
	dead := s.cluster.HeartbeatDead(ctx)
	nodes, err := s.cluster.ListActiveAgentsForProvider(ctx, t.Provider, dead)
	if err == nil && len(nodes) > 0 {
		return true
	}
	if s.cluster.EmbeddedAlive(ctx) {
		return true
	}
	return false
}

// runTask 后台执行：取池中账号 → 调 provider → 结算 / 退款。
func (s *GenerationService) runTask(ctx context.Context, t *model.GenerationTask) {
	log := logger.L().With(zap.String("task", t.TaskID))

	prov, ok := s.providers[t.Provider]
	if !ok {
		s.failTask(ctx, t, "provider not registered: "+t.Provider)
		return
	}

	var params map[string]any
	_ = json.Unmarshal([]byte(t.Params), &params)
	var refs []string
	if t.RefAssets != nil {
		_ = json.Unmarshal([]byte(*t.RefAssets), &refs)
	}
	refs = s.normalizeInputRefs(ctx, t, refs)

	timeout := 5 * time.Minute
	if t.Kind == "video" {
		// 视频生成实测：grok 6s≈90s / 30s≈4-6min；adobe veo3.1-* 4s≈110-135s / 8s≈3-4min。
		// 给 8 分钟硬上限：覆盖最慢档位（grok 30s 拼接 + adobe 1080P 8s）+ 留 2 倍 buffer。
		// 之前 15 分钟太长：上游卡住 / 号挂掉 时用户要等 14 分多才看到失败。
		timeout = 8 * time.Minute
	}
	if t.Provider == model.ProviderGPT && t.Kind == string(provider.KindImage) && strings.EqualFold(t.ModelCode, "gpt-image-2") {
		// codex 路径出图：单次 ≈ 30~60 s，留足缓冲（含 4K 大图 + Cloudflare 排队）。
		timeout = 10 * time.Minute
	}
	maxAttempts := 3
	retryDelay := 800 * time.Millisecond
	if s.cfg != nil {
		timeout = s.cfg.RetryTimeout(ctx, timeout)
		maxAttempts = s.cfg.RetryMaxAttempts(ctx)
		retryDelay = s.cfg.RetryBaseDelay(ctx)
	}

	// task 级总 budget：所有 attempt + Adobe 兜底 + retry sleep 加起来不能超过这个值，
	// 防止"全部失败"场景下用户等几十分钟才看到错误。
	//
	// 策略：单 attempt × 2 + 60s（够装下 1 次主路径 + 1 次 adobe 兜底 + 重试间隔）。
	// 这个 budget 当 wall-clock deadline 强制 enforce，避免 retry 循环里某一次 attempt
	// 卡满 timeout 后再 sleep 进入下一次又卡满。
	taskBudget := timeout*2 + 60*time.Second
	taskCtx, cancelTask := context.WithTimeout(ctx, taskBudget)
	defer cancelTask()
	// remainingTimeout 用于约束每次 attempt 的 ctx：取「单 attempt timeout」和「task 剩余 budget」的最小值，
	// 这样最后一次 attempt 不会比预算还长。
	remainingTimeout := func() time.Duration {
		dl, ok := taskCtx.Deadline()
		if !ok {
			return timeout
		}
		left := time.Until(dl)
		if left <= 0 {
			return 0
		}
		if left > timeout {
			return timeout
		}
		return left
	}
	var acc *model.Account
	var res *provider.Result
	var lastErr error
	allowProxyPoolFallback := false
	releaseAcc := func(a *model.Account) {
		if a != nil {
			s.pool.ReleaseForTask(ctx, t.Provider, a.ID, t.TaskID)
		}
	}
	triedProxyIDs := map[uint64]struct{}{}
	triedAccountIDs := map[uint64]struct{}{}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// 进入下一次 attempt 前先看 task 总 budget 是否已耗尽：
		// 上一次 attempt 卡满 timeout 后，剩余预算可能已经不够再跑一次有意义的请求，
		// 此时直接 fail，别拖到下游 ctx-deadline-exceeded 又把用户多 hang 一个 timeout。
		if attempt > 1 && remainingTimeout() < 5*time.Second {
			log.Warn("task budget exhausted before retry, failing fast",
				zap.String("task", t.TaskID),
				zap.Int("attempt", attempt),
				zap.Error(lastErr))
			if lastErr != nil {
				s.failTask(ctx, t, fmt.Sprintf("provider call: %v", lastErr))
			} else {
				s.failTask(ctx, t, "task budget exhausted")
			}
			return
		}
		picked, err := s.pickAccountForTask(ctx, t, params, triedAccountIDs)
		if err != nil {
			if isNoAvailableAccountError(err) && t.ClaimNodeID != nil && strings.TrimSpace(*t.ClaimNodeID) != "" {
				nodeID := strings.TrimSpace(*t.ClaimNodeID)
				if relErr := s.repo.ReleaseClaim(ctx, t.TaskID, nodeID); relErr != nil {
					log.Warn("release claim after no account failed", zap.String("task", t.TaskID), zap.String("node_id", nodeID), zap.Error(relErr))
					s.failTask(ctx, t, unavailableAccountReason(t, err))
				} else {
					log.Warn("no account available, task requeued", zap.String("task", t.TaskID), zap.String("node_id", nodeID), zap.String("provider", t.Provider), zap.String("kind", t.Kind), zap.String("model", t.ModelCode), zap.Error(err))
				}
				return
			}
			// 没号 / 全部 cooldown 时也优先尝试 Gemini 兜底（Adobe Firefly 池里有
			// firefly-gpt-image-2-* 全档位别名）。对用户来说"GPT 池子空"不应该是
			// 硬错误 —— Adobe 池有号就出图，不要硬 fail "暂无可用账号"。
			//
			// 仅对 gpt-image-2 这一种主路径明确有 Adobe 镜像 catalog 的模型开启。
			if t.Provider == model.ProviderGPT &&
				t.Kind == string(provider.KindImage) &&
				strings.EqualFold(t.ModelCode, "gpt-image-2") {
				fallbackTimeout := timeout
				if fallbackTimeout > 5*time.Minute {
					fallbackTimeout = 5 * time.Minute
				}
				if left := remainingTimeout(); left < fallbackTimeout {
					fallbackTimeout = left
				}
				if fallbackTimeout >= 5*time.Second {
					log.Warn("no gpt account available, falling back to gemini",
						zap.String("task", t.TaskID),
						zap.Error(err))
					if fbRes, fbAcc, fbErr := s.runImageFallbackToAdobe(taskCtx, t, params, refs, fallbackTimeout); fbErr == nil && fbRes != nil {
						log.Info("gemini fallback succeeded (no gpt account path)",
							zap.String("task", t.TaskID),
							zap.Uint64("fallback_account_id", fbAcc))
						res = fbRes
						acc = &model.Account{ID: fbAcc, Provider: model.ProviderADOBE}
						if res.EffectiveModelCode == "" {
							res.EffectiveModelCode = t.ModelCode + "@gemini-fallback"
						}
						break // 跳出 attempt 循环走结算路径
					} else if fbErr != nil {
						log.Warn("gemini fallback also failed (no gpt account path)",
							zap.String("task", t.TaskID), zap.Error(fbErr))
						// 把 Gemini 池的失败 reason 也塞给 lastErr，下面 failTask 文案更准确
						err = fmt.Errorf("no gpt account; gemini fallback: %w", fbErr)
					}
				}
			}
			if lastErr != nil {
				s.failTask(ctx, t, fmt.Sprintf("provider call: %v", lastErr))
			} else {
				s.failTask(ctx, t, unavailableAccountReason(t, err))
			}
			return
		}
		acc = picked
		rows, srErr := s.repo.SetRunning(ctx, t.TaskID, acc.ID)
		if srErr != nil {
			log.Warn("set running failed", zap.Error(srErr))
		}
		if srErr == nil && rows == 0 {
			// rows=0 在新 SetRunning 实现下意味着任务既不是 Pending 也不是 Running
			// （典型：已经被另一个节点跑成 Succeeded/Failed/Refunded 终态）。
			// 直接让出账号即可，不再调 failTask（已经是终态了，重新写 status 会冲掉真实结果）。
			//
			// 历史 bug：老 SetRunning 把 ClaimBatch 路径误归到这里 → 静默 return →
			// 任务卡 Running 等 lease 过期 → 反复抢 → "待处理" 拖几十分钟。新 SetRunning
			// 已经兼容 ClaimBatch 路径，这条分支只剩"任务已终结"一种语义。
			log.Info("gen.run.yield",
				zap.String("task", t.TaskID),
				zap.String("reason", "already_terminal"))
			releaseAcc(acc)
			return
		}

		provReq := &provider.Request{
			TaskID:    t.TaskID,
			Kind:      provider.Kind(t.Kind),
			Mode:      provider.Mode(t.Mode),
			ModelCode: s.upstreamModel(t.ModelCode),
			Prompt:    t.Prompt,
			Params:    params,
			RefAssets: refs,
			Count:     t.Count,
			Account:   acc,
		}
		provReq.UpstreamLog = s.makeUpstreamLogger(t, acc)
		provReq.OnPollProgress = s.makePollProgressUpdater(t)
		if t.NegPrompt != nil {
			provReq.NegPrompt = *t.NegPrompt
		}
		if acc.BaseURL != nil {
			provReq.BaseURL = *acc.BaseURL
		} else if t.Provider == model.ProviderGPT && t.Kind == string(provider.KindImage) && strings.EqualFold(t.ModelCode, "gpt-image-2") {
			// gpt-image-2 1K/2K/4K 一律走 ChatGPT Codex Responses API（用 Plus/Pro 订阅免费摊销）。
			// platform OAuth client 颁的 access_token 也能直接打这个端点（已实测）。
			provReq.BaseURL = "https://chatgpt.com/backend-api/codex"
		}
		proxyURL, proxyID, perr := s.resolveProxyURL(ctx, acc, triedProxyIDs, allowProxyPoolFallback)
		if perr == nil {
			provReq.ProxyURL = proxyURL
		} else {
			log.Warn("resolve proxy failed", zap.Error(perr))
		}
		if s.aes != nil {
			cred, derr := s.providerCredential(ctx, acc, provReq.ProxyURL)
			if derr != nil {
				lastErr = derr
				if isFatalOAuthRefreshError(derr) {
					s.disableProviderAccount(ctx, acc, derr.Error())
				} else {
					s.markProviderFailed(ctx, acc, derr.Error(), 30*time.Minute)
				}
				releaseAcc(acc)
				acc = nil
				if attempt == maxAttempts || !retryableProviderError(derr) {
					s.failTask(ctx, t, fmt.Sprintf("provider call: %v", derr))
					return
				}
				sleepBeforeRetry(taskCtx, retryDelay, attempt)
				continue
			}
			provReq.Credential = cred
			s.applyAdobeSubmitMode(ctx, provReq, acc)
		}

		// 单次 attempt 用 taskCtx 派生，min(单 attempt timeout, task 剩余预算)，
		// 保证最后一次 attempt 不会超出总预算。
		rctx, cancel := context.WithTimeout(taskCtx, remainingTimeout())
		out, err := prov.Generate(rctx, provReq)
		cancel()
		if err == nil {
			res = out
			break
		}
		lastErr = err
		rotateProxy := shouldRotateProxyOnRetry(t.Provider, err)
		if rotateProxy && proxyID != 0 {
			triedProxyIDs[proxyID] = struct{}{}
		}
		triedAccountIDs[acc.ID] = struct{}{}
		if rotateProxy || isRetryableProxyTransportError(err) {
			allowProxyPoolFallback = true
		}
		if isZeroImageReturnedError(err) {
			releaseAcc(acc)
			acc = nil
			s.failTask(ctx, t, fmt.Sprintf("provider call: %v", err))
			return
		} else if isProviderQuotaLimitedError(err) {
			s.markProviderQuotaLimited(ctx, acc, err.Error(), usageLimitResetAt(err))
		} else if isAdobeNotEntitledError(err) {
			// "账号没买这个档位权益" 不是 token 失效，不应该让该号 cooldown：
			// 同一个号下次跑 1K/2K 任务还能正常用。只更新 last_error 让运营在
			// 后台看得到「该号未开通某档位」的提示。
			//
			// 同时把"该号在此档位上 not entitled"持久化到 entitlements_json，
			// 下次相同档位的任务 pickAccountForTask 会直接跳过这个号，
			// 不再浪费一次 retry slot 来撞同样的 403（见 adobeEntitlementTTL）。
			if t.Provider == model.ProviderADOBE && t.Kind == string(provider.KindImage) {
				if tier := adobeResolutionTier(params); tier != "" {
					s.recordAdobeNotEntitled(ctx, acc, tier)
				}
			}
			s.pool.MarkTransientFailed(ctx, acc.ID, err.Error())
		} else if isAdobeRegionBlockedError(err) {
			// 451 区域限制：根因是当前出口 IP/代理被上游按地区封禁，
			// 不是账号或 token 的问题。按瞬时错误处理（不冷却账号），
			// 让该号换一个出口 IP 后还能继续用；同时 shouldRotateProxyOnRetry
			// 会在重试时轮换代理，下一次 attempt 用不同 IP 重试。
			s.pool.MarkTransientFailed(ctx, acc.ID, err.Error())
		} else if isAdobeAuthError(err) {
			// firefly 干净的 401/403（非 taste_exhausted / 非 blocked_by_3p / 非 not_entitled /
			// 非反爬 HTML 挑战，这些已在前面的分支 / ClassifyError 里分流）= token 真的死了。
			//
			// 对齐 newbanana 判断：一次判死，直接置 invalid 终态，而不是 30min cooldown。
			// 原因：cooldown 号会被后台续期调度器（ListExpiringSoon 含 cooldown）反复刷新
			// 拉回 valid（IMS 能续 token，但 firefly 端仍拒绝）→ 僵尸号在 cooldown↔valid
			// 之间反复入选、反复 401，正是批量任务失败的主因之一。invalid 终态会被自动续期
			// 与网关同时跳过，彻底打断这个循环；行记录保留（error_message+failure_count），
			// 后台「失效」可查、人工"刷新异常账号"仍可恢复。
			s.markProviderInvalid(ctx, acc, err.Error())
		} else if isTransientProviderPathError(t.Provider, err) {
			s.pool.MarkTransientFailed(ctx, acc.ID, err.Error())
		} else if t.Provider == model.ProviderXAI {
			// xAI 官方 API 账号：除"额度耗尽"（上面 isProviderQuotaLimitedError 已分流，
			// 那是真没钱、按 billing 周期 cooldown 合理）外，任何错误都不该 cooldown——
			// 4xx 客户端错误（prompt 过长/参数非法）、429 限速、5xx、超时、网络抖动都不是
			// "账号坏了"。API 号不像网页号会被封；access_token 过期由后台续期调度器负责。
			// 冷却只会让本就稀少的 xAI 号被一条错误踢出轮转 → 后续任务全部"暂无可用账号"。
			// 统一按瞬时失败处理（只记 last_error，号继续可用）。retryableProviderError 决定
			// 是否换号重试：4xx 不可重试→快速失败；429/5xx/超时→换号重试。
			s.pool.MarkTransientFailed(ctx, acc.ID, err.Error())
		} else {
			cooldown := providerCooldown(err)
			s.markProviderFailed(ctx, acc, err.Error(), cooldown)
		}
		releaseAcc(acc)
		acc = nil
		// 451 内容安全 / 422 永久错误：立刻失败，不消耗 retry 预算让用户白等。
		if isAdobeRegionBlockedError(err) || isAdobeNonRetryableError(err) {
			s.failTask(ctx, t, fmt.Sprintf("provider call: %v", err))
			return
		}
		if attempt == maxAttempts || !retryableProviderError(err) {
			s.failTask(ctx, t, fmt.Sprintf("provider call: %v", err))
			return
		}
		log.Warn(
			"provider retrying with next account",
			zap.Int("attempt", attempt),
			zap.Uint64("account_id", picked.ID),
			zap.Uint64("proxy_id", proxyID),
			zap.Bool("rotate_proxy", rotateProxy),
			zap.Duration("remaining_budget", remainingTimeout()),
			zap.Error(err),
		)
		sleepBeforeRetry(taskCtx, retryDelay, attempt)
	}
	if res == nil {
		releaseAcc(acc)
		acc = nil
		// 关键 fallback：gpt-image-2 走 ChatGPT Codex 主路径全部失败时，
		// 如果是 transient 错误（429/5xx/超时/网络抖动），切到 Gemini 兜底渠道
		// 重试一次（404 / 401 / invalid_grant / usage_limit 不 fallback：号挂了让用户重试）。
		// （内部实现走 Adobe Firefly Image-2，仅在内部代码 / 日志 / DB 暴露真实厂商名；
		//   任何用户可见路径上对外统一称作"Gemini 兜底渠道"。）
		//
		// 重要：fallback 的 timeout 用 min(单 attempt timeout, 5min, 剩余 budget)。
		// 兜底不是核心路径，给的时间预算应小于主链路，避免出错时用户等几十分钟。
		if lastErr != nil &&
			t.Provider == model.ProviderGPT &&
			t.Kind == string(provider.KindImage) &&
			strings.EqualFold(t.ModelCode, "gpt-image-2") &&
			isGPTCodexTransientError(lastErr) {
			fallbackTimeout := timeout
			if fallbackTimeout > 5*time.Minute {
				fallbackTimeout = 5 * time.Minute
			}
			if left := remainingTimeout(); left < fallbackTimeout {
				fallbackTimeout = left
			}
			if fallbackTimeout < 5*time.Second {
				log.Warn("gpt codex transient failure, skipping adobe fallback (no budget left)",
					zap.String("task", t.TaskID), zap.Duration("remaining", remainingTimeout()))
			} else {
				log.Warn("gpt codex transient failure, falling back to adobe firefly",
					zap.String("task", t.TaskID),
					zap.Duration("fallback_timeout", fallbackTimeout),
					zap.Error(lastErr))
				if fallbackRes, fallbackAcc, fallbackErr := s.runImageFallbackToAdobe(taskCtx, t, params, refs, fallbackTimeout); fallbackErr == nil && fallbackRes != nil {
					log.Info("adobe firefly fallback succeeded",
						zap.String("task", t.TaskID),
						zap.Uint64("fallback_account_id", fallbackAcc))
					res = fallbackRes
					acc = &model.Account{ID: fallbackAcc, Provider: model.ProviderADOBE}
					if res.EffectiveModelCode == "" {
						// 标记字符串改用 "@gemini-fallback"：纯内部审计 / 退款定价 key，
						// 不希望任何用户可见路径意外吐出真实上游厂商名。
						res.EffectiveModelCode = t.ModelCode + "@gemini-fallback"
					}
				} else if fallbackErr != nil {
					log.Warn("adobe firefly fallback also failed",
						zap.String("task", t.TaskID), zap.Error(fallbackErr))
					lastErr = fmt.Errorf("gpt: %v; adobe fallback: %w", lastErr, fallbackErr)
				}
			}
		}
		if res == nil {
			if lastErr != nil {
				s.failTask(ctx, t, fmt.Sprintf("provider call: %v", lastErr))
			} else {
				s.failTask(ctx, t, "provider call failed")
			}
			return
		}
	}
	if acc != nil {
		releaseAcc(acc)
		s.pool.MarkUsed(ctx, acc.ID)
	}

	results := make([]*model.GenerationResult, 0, len(res.Assets))
	for i, a := range res.Assets {
		gr := &model.GenerationResult{
			TaskID: t.TaskID,
			UserID: t.UserID,
			Kind:   t.Kind,
			Seq:    int8(i),
			URL:    a.URL,
			Width:  intPtr(a.Width),
			Height: intPtr(a.Height),
		}
		if a.ThumbURL != "" {
			s := a.ThumbURL
			gr.ThumbURL = &s
		}
		if a.DurationMs > 0 {
			d := a.DurationMs
			gr.DurationMs = &d
		}
		if a.SizeBytes > 0 {
			b := a.SizeBytes
			gr.SizeBytes = &b
		}
		if len(a.Meta) > 0 {
			b, _ := json.Marshal(a.Meta)
			s := string(b)
			gr.Meta = &s
		}
		results = append(results, gr)
	}
	s.cacheResultAssets(ctx, t, acc, results)

	if err := s.repo.SetSucceeded(ctx, t.TaskID, results); err != nil {
		log.Error("set succeeded failed", zap.Error(err))
	}
	s.updateAccountUsageMeta(ctx, acc, t, len(results))
	// Adobe 图像任务成功后正向写入权益标记：把"该号确实跑通了这个档位"持久化，
	// 后台运营能直观看到「这个号 4K 是绿的」。和 recordAdobeNotEntitled 配对，
	// 共享 7 天 TTL；冲突解析在 parseAdobeEntitlements 里按 checked_at 取近。
	if t.Provider == model.ProviderADOBE && t.Kind == string(provider.KindImage) {
		if tier := adobeResolutionTier(params); tier != "" {
			s.recordAdobeEntitlementOK(ctx, acc, tier)
		}
	}
	if t.CostPoints > 0 {
		s.settleGenerationBilling(ctx, t, acc, params, results, res.EffectiveModelCode)
	}
	s.recordTaskCost(ctx, t, params, acc, len(results), results)
	s.dispatchTaskWebhook(ctx, t.TaskID)
}

func maxResultDimensions(results []*model.GenerationResult) (int, int) {
	maxW, maxH := 0, 0
	for _, r := range results {
		if r == nil || r.Width == nil || r.Height == nil {
			continue
		}
		if *r.Width > maxW {
			maxW = *r.Width
		}
		if *r.Height > maxH {
			maxH = *r.Height
		}
	}
	return maxW, maxH
}

// settleGenerationBilling 按实际生成结果 / 通道降级重算价，再 Settle 或 FinalizeUsage。
func (s *GenerationService) settleGenerationBilling(
	ctx context.Context,
	t *model.GenerationTask,
	acc *model.Account,
	params map[string]any,
	results []*model.GenerationResult,
	effectiveModelCode string,
) {
	if s == nil || s.billing == nil || t == nil || t.CostPoints <= 0 {
		return
	}
	log := logger.FromCtx(ctx)
	billingParams := params
	if t.Kind == string(provider.KindImage) {
		outW, outH := maxResultDimensions(results)
		billingParams = ImageBillingParams(t.ModelCode, params, outW, outH)
	}
	actualTotal := t.CostPoints
	if s.priceFn != nil {
		modelCode := t.ModelCode
		effective := strings.TrimSpace(effectiveModelCode)
		if effective != "" && effective != t.ModelCode {
			modelCode = effective
		}
		actualUnit := s.priceFn(modelCode, provider.Kind(t.Kind), billingParams)
		if actualUnit < 0 {
			actualUnit = 0
		}
		// 按实际交付数量结算：Adobe 等 provider 单次只产 1 张图，客户端请求
		// n=4 时预扣 4 份但只交付 1 份，结算时按 len(results) 收，差价由
		// FinalizeUsage 自动退回。
		billCount := int64(t.Count)
		if n := int64(len(results)); n > 0 && n < billCount {
			billCount = n
		}
		actualTotal = actualUnit * billCount
	}
	var accountID *uint64
	if acc != nil {
		accountID = &acc.ID
	} else if t.AccountID != nil && *t.AccountID > 0 {
		accountID = t.AccountID
	}
	if actualTotal != t.CostPoints {
		if actualTotal < t.CostPoints {
			log.Info("billing.adjust_refund",
				zap.String("task", t.TaskID),
				zap.String("model", t.ModelCode),
				zap.Int64("charged", t.CostPoints),
				zap.Int64("actual", actualTotal))
		} else {
			log.Info("billing.adjust_extra",
				zap.String("task", t.TaskID),
				zap.String("model", t.ModelCode),
				zap.Int64("charged", t.CostPoints),
				zap.Int64("actual", actualTotal))
		}
		if err := s.billing.FinalizeUsage(ctx, t.TaskID, actualTotal, accountID); err != nil {
			log.Error("finalize usage failed", zap.Error(err))
			return
		}
		if err := s.repo.UpdateCostPoints(ctx, t.TaskID, actualTotal); err != nil {
			log.Error("update task cost_points failed", zap.Error(err))
		}
		t.CostPoints = actualTotal
		return
	}
	if err := s.billing.Settle(ctx, t.TaskID, accountID); err != nil {
		log.Error("settle failed", zap.Error(err))
	}
}

// recordTaskCost 任务成功后写一条 task_cost_log。
//
// 通道解析按 (model_code, variant_key)：图片用 1k/2k/4k；视频用模型自己的 duration 档；
// 其它（暂无 variant 的）传空串走 fallback。
// cost 不存在通道（运营还没在 admin 配）会被 CostRecorder 静默跳过。
func (s *GenerationService) recordTaskCost(ctx context.Context, t *model.GenerationTask, params map[string]any, acc *model.Account, unitQty int, results []*model.GenerationResult) {
	if s == nil || s.cost == nil || t == nil {
		return
	}
	variant := ""
	switch t.Kind {
	case string(provider.KindImage):
		variant = strings.ToLower(adobeResolutionTier(params))
		if outW, outH := maxResultDimensions(results); outW > 0 && outH > 0 {
			if outTier := TierFromPixels(outW, outH); imageTierRank(outTier) > imageTierRank(variant) {
				variant = outTier
			}
		}
	case string(provider.KindVideo):
		if dur, ok := videoDurationFromParams(params); ok {
			variant = strconv.Itoa(NormalizeVideoDurationForModel(t.ModelCode, dur))
		}
	}
	qty := float64(unitQty)
	if qty <= 0 {
		qty = float64(t.Count)
	}
	if qty <= 0 {
		qty = 1
	}
	req := CostRecordReq{
		RefType:    model.CostRefGeneration,
		RefID:      t.TaskID,
		UserID:     t.UserID,
		ModelCode:  t.ModelCode,
		VariantKey: variant,
		Provider:   t.Provider,
		Kind:       t.Kind,
		UnitQty:    qty,
		SalePoints: t.CostPoints,
	}
	if acc != nil {
		req.AccountID = acc.ID
	}
	s.cost.Record(ctx, req)
}

func (s *GenerationService) makeUpstreamLogger(t *model.GenerationTask, acc *model.Account) provider.UpstreamLogger {
	return func(ctx context.Context, e provider.UpstreamLogEntry) {
		if t == nil {
			return
		}
		meta := ""
		if len(e.Meta) > 0 {
			if b, err := json.Marshal(e.Meta); err == nil {
				meta = string(b)
			}
		}
		row := &model.GenerationUpstreamLog{
			TaskID:     t.TaskID,
			Provider:   e.Provider,
			Stage:      e.Stage,
			Method:     e.Method,
			URL:        truncate(e.URL, 512),
			StatusCode: e.StatusCode,
			DurationMs: e.DurationMs,
		}
		if row.Provider == "" {
			row.Provider = t.Provider
		}
		if acc != nil {
			row.AccountID = &acc.ID
		}
		if e.RequestExcerpt != "" {
			v := truncate(e.RequestExcerpt, 12000)
			row.RequestExcerpt = &v
		}
		if e.ResponseExcerpt != "" {
			v := truncate(e.ResponseExcerpt, 12000)
			row.ResponseExcerpt = &v
		}
		if e.Error != "" {
			v := truncate(e.Error, 4000)
			row.Error = &v
		}
		if meta != "" {
			row.Meta = &meta
		}
		if err := s.repo.CreateUpstreamLog(ctx, row); err != nil {
			logger.FromCtx(ctx).Warn("generation.upstream_log_failed", zap.String("task_id", t.TaskID), zap.String("stage", e.Stage), zap.Error(err))
		}
		if len(e.Meta) > 0 {
			retryAfter := metaRetryAfterSeconds(e.Meta)
			if retryAfter > 0 {
				_ = s.repo.UpdatePollState(ctx, t.TaskID, -1, retryAfter)
			}
		}
	}
}

func metaRetryAfterSeconds(meta map[string]any) int {
	headers, ok := meta["headers"].(map[string]string)
	if !ok {
		raw, ok := meta["headers"].(map[string]any)
		if !ok {
			return 0
		}
		if v, ok := raw["retry-after"]; ok {
			return parseRetryAfterValue(v)
		}
		return 0
	}
	return parseRetryAfterValue(headers["retry-after"])
}

func parseRetryAfterValue(v any) int {
	switch n := v.(type) {
	case string:
		var sec int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &sec); err == nil && sec > 0 {
			return sec
		}
	case int:
		if n > 0 {
			return n
		}
	case int64:
		if n > 0 {
			return int(n)
		}
	case float64:
		if n > 0 {
			return int(n)
		}
	}
	return 0
}

func (s *GenerationService) makePollProgressUpdater(t *model.GenerationTask) provider.PollProgressFunc {
	return func(ctx context.Context, progress, retryAfterSec int) {
		if s == nil || s.repo == nil || t == nil {
			return
		}
		p := -1
		if progress > 0 {
			p = progress
		}
		_ = s.repo.UpdatePollState(ctx, t.TaskID, p, retryAfterSec)
	}
}

func (s *GenerationService) providerCredential(ctx context.Context, acc *model.Account, proxyURL string) (string, error) {
	if acc == nil {
		return "", fmt.Errorf("missing account")
	}
	if acc.AuthType == model.AuthTypeOAuth && acc.Provider == model.ProviderGPT {
		return s.gptOAuthAccessToken(ctx, acc, proxyURL)
	}
	if len(acc.CredentialEnc) == 0 {
		return "", fmt.Errorf("account credential is empty")
	}
	plain, err := s.aes.Decrypt(acc.CredentialEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt credential failed: %w", err)
	}
	cred := strings.TrimSpace(string(plain))
	if cred == "" {
		return "", fmt.Errorf("account credential is empty")
	}
	return cred, nil
}

// applyAdobeSubmitMode 当 adobe.submit_mode=psweb 且账号有 cookie 时，把 provReq 的
// Bearer 凭证换成现铸的 PSWebApp1 token 并打上 psweb 标记，让 firefly client 走
// Photoshop Web 入口。任何一步失败都静默回退到 clio（不影响生成）。
func (s *GenerationService) applyAdobeSubmitMode(ctx context.Context, provReq *provider.Request, acc *model.Account) {
	if provReq == nil || acc == nil || acc.Provider != model.ProviderADOBE || s.cfg == nil || s.aes == nil {
		return
	}
	if s.cfg.AdobeSubmitMode(ctx) != "psweb" {
		return
	}
	cookie := s.adobeAccountCookie(ctx, acc.ID)
	if cookie == "" {
		return
	}
	token := s.pswebToken(ctx, acc.ID, cookie, provReq.ProxyURL)
	if token == "" {
		return
	}
	provReq.Credential = token
	provReq.AdobeSubmitMode = "psweb"
}

// adobeAccountCookie 读取 pool_adobe.cookie_enc 并解密；失败返回 ""。
func (s *GenerationService) adobeAccountCookie(ctx context.Context, accID uint64) string {
	if s.db == nil || s.aes == nil {
		return ""
	}
	var row model.PoolAdobe
	if err := s.db.WithContext(ctx).Select("cookie_enc").First(&row, accID).Error; err != nil {
		return ""
	}
	if len(row.CookieEnc) == 0 {
		return ""
	}
	b, err := s.aes.Decrypt(row.CookieEnc)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// pswebToken 取（或现铸）账号的 PSWebApp1 token，带 45min/到期 缓存。
func (s *GenerationService) pswebToken(ctx context.Context, accID uint64, cookie, proxyURL string) string {
	s.pswebMu.Lock()
	if s.pswebTok == nil {
		s.pswebTok = map[uint64]pswebTokenEntry{}
	}
	if e, ok := s.pswebTok[accID]; ok && time.Until(e.exp) > 60*time.Second {
		tok := e.token
		s.pswebMu.Unlock()
		return tok
	}
	s.pswebMu.Unlock()

	res, err := adoberefresh.MintPSWebToken(ctx, cookie, proxyURL, 15*time.Second)
	if err != nil || res == nil || res.AccessToken == "" {
		if err != nil {
			logger.L().Warn("adobe psweb mint failed, fallback to clio",
				zap.Uint64("account_id", accID), zap.Error(err))
		}
		return ""
	}
	exp := time.Now().Add(45 * time.Minute)
	if res.ExpiresAt > 0 {
		exp = time.Unix(res.ExpiresAt, 0)
	}
	s.pswebMu.Lock()
	s.pswebTok[accID] = pswebTokenEntry{token: res.AccessToken, exp: exp}
	s.pswebMu.Unlock()
	return res.AccessToken
}

func (s *GenerationService) pickAccountForTask(ctx context.Context, t *model.GenerationTask, params map[string]any, excludeAccountIDs map[uint64]struct{}) (*model.Account, error) {
	if t == nil {
		return nil, errcode.NoAvailableAcc
	}
	notExcluded := func(acc *model.Account) bool {
		if acc == nil {
			return false
		}
		if len(excludeAccountIDs) == 0 {
			return true
		}
		_, blocked := excludeAccountIDs[acc.ID]
		return !blocked
	}
	if t.Provider == model.ProviderGROK {
		requiredPlan := requiredGrokPlanForTask(t.ModelCode, provider.Kind(t.Kind))
		return s.pool.ReserveForTaskWhere(ctx, t.Provider, "round_robin", t.TaskID, "runTask", func(acc *model.Account) bool {
			return notExcluded(acc) && accountSupportsGrokPlan(acc, requiredPlan)
		})
	}
	if t.Provider == model.ProviderADOBE && t.Kind == string(provider.KindImage) {
		// 提前过滤掉已学到的"无此档位权益"的号，避免每次都拿一个无 4K 权益的
		// Free 号去撞 403，把 retry slot 浪费在必败的请求上。
		// accountSupportsAdobeTier 有 7 天 TTL，允许运营升级账号后重新被探测。
		tier := adobeResolutionTier(params)
		return s.pool.ReserveForTaskWhere(ctx, t.Provider, "round_robin", t.TaskID, "runTask", func(acc *model.Account) bool {
			return notExcluded(acc) && accountSupportsAdobeTier(acc, tier)
		})
	}
	if t.Provider == model.ProviderGPT && t.Kind == string(provider.KindImage) && isCompatibilityGatewayImageModel(t.ModelCode) {
		return s.pool.ReserveForTaskWhere(ctx, t.Provider, "round_robin", t.TaskID, "runTask", func(acc *model.Account) bool {
			return notExcluded(acc) && !accountUsesNativeGPTImage2(acc)
		})
	}
	if t.Provider != model.ProviderGPT || t.Kind != string(provider.KindImage) || !strings.EqualFold(t.ModelCode, "gpt-image-2") {
		if len(excludeAccountIDs) == 0 {
			return s.pool.ReserveForTaskWhere(ctx, t.Provider, "round_robin", t.TaskID, "runTask", nil)
		}
		return s.pool.ReserveForTaskWhere(ctx, t.Provider, "round_robin", t.TaskID, "runTask", notExcluded)
	}
	return s.pool.ReserveForTaskWhere(ctx, t.Provider, "round_robin", t.TaskID, "runTask", func(acc *model.Account) bool {
		if !notExcluded(acc) {
			return false
		}
		if !accountUsesNativeGPTImage2(acc) {
			return true
		}
		// gpt-image-2 走 ChatGPT Codex Responses 端点：任何 OAuth GPT 账号
		// （platform client `app_2SKx67Edpo...` 或 codex client `app_EMoamEEZ73f0...`）
		// 颁的 access_token 都能直接调 chatgpt.com/backend-api/codex/responses（已实测），
		// 因此不再要求必须是 codex client。这样号池里 platform OAuth 拿到的 plus/pro 号
		// 也能直接给 image-2 用，不用强制 re-auth 走 codex CLI 流程换 client_id。
		return acc.AuthType == model.AuthTypeOAuth
	})
}

func unavailableAccountReason(t *model.GenerationTask, err error) string {
	if t != nil && t.Provider == model.ProviderGROK && t.Kind == string(provider.KindVideo) {
		if e, ok := errcode.As(err); ok && e != nil && e.Code == errcode.NoAvailableAcc.Code {
			return errcode.NoAvailableAcc.Msg
		}
	}
	// Adobe 图片任务 4K：池里所有号都已学到 no_4k=true，filter 全部刷掉。
	// 这里走一个清晰的提示，提醒运营该补 Premium 号了。
	if t != nil && t.Provider == model.ProviderADOBE && t.Kind == string(provider.KindImage) {
		if e, ok := errcode.As(err); ok && e != nil && e.Code == errcode.NoAvailableAcc.Code {
			return "当前档位（如 4K）暂未开通，请改用 1K / 2K 或联系运营"
		}
	}
	return fmt.Sprintf("pick account: %v", err)
}

func isNoAvailableAccountError(err error) bool {
	if e, ok := errcode.As(err); ok && e != nil && e.Code == errcode.NoAvailableAcc.Code {
		return true
	}
	return false
}

// accountRequiresCodexRoute 判断任务是否走 chatgpt.com/backend-api/codex 路径。
// 当前 gpt-image-2 全档位都走 codex，所以只要是 gpt-image-2 就返回 true。
func accountRequiresCodexRoute(t *model.GenerationTask, params map[string]any) bool {
	if t == nil || t.Provider != model.ProviderGPT || t.Kind != string(provider.KindImage) || !strings.EqualFold(t.ModelCode, "gpt-image-2") {
		return false
	}
	return true
}

// shouldUseGPTWebRoute 历史标志位，已废弃；保留签名是为了兼容尚未清理完的 caller，
// 永远返回 false（即 gpt-image-2 不再有"web 路径"，全部 codex）。
func shouldUseGPTWebRoute(params map[string]any) bool { return false }

func accountUsesNativeGPTImage2(acc *model.Account) bool {
	if acc == nil || acc.BaseURL == nil {
		return true
	}
	base := strings.TrimSpace(*acc.BaseURL)
	if base == "" {
		return true
	}
	base = strings.ToLower(strings.TrimRight(base, "/"))
	return strings.Contains(base, "api.openai.com") || strings.Contains(base, "chatgpt.com") || strings.Contains(base, "/backend-api/codex")
}

func isCompatibilityGatewayImageModel(modelCode string) bool {
	modelCode = strings.ToLower(strings.TrimSpace(modelCode))
	return strings.HasPrefix(modelCode, "gemini-")
}

func (s *GenerationService) imageProvider(modelCode string) string {
	return s.providerForKind(modelCode, provider.KindImage, model.ProviderGPT)
}

func (s *GenerationService) videoProvider(modelCode string) string {
	return s.providerForKind(modelCode, provider.KindVideo, model.ProviderGROK)
}

// providerForKind 从 billing.model_prices 里查 (model_code, kind) → provider 映射，
// 命中且 enabled 时返回；否则返回 fallback。
func (s *GenerationService) providerForKind(modelCode string, kind provider.Kind, fallback string) string {
	if s == nil || s.cfg == nil {
		return fallback
	}
	raw := s.cfg.GetString(context.Background(), "billing.model_prices", "")
	if raw == "" {
		return fallback
	}
	var rows []struct {
		ModelCode string `json:"model_code"`
		Kind      string `json:"kind"`
		Provider  string `json:"provider"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return fallback
	}
	for _, row := range rows {
		if strings.TrimSpace(row.ModelCode) != modelCode {
			continue
		}
		if row.Enabled != nil && !*row.Enabled {
			continue
		}
		if strings.TrimSpace(row.Kind) != "" && strings.TrimSpace(row.Kind) != string(kind) {
			continue
		}
		if v := strings.TrimSpace(row.Provider); v != "" {
			return v
		}
	}
	return fallback
}

func (s *GenerationService) ImageProviderForModel(modelCode string) string {
	return s.imageProvider(strings.TrimSpace(modelCode))
}

// ImageProviderForModelWithParams 把 (modelCode, params) 映射到 provider。
//
// 当前路由：
//
//   - gpt-image-2 全档位（1K/2K/4K）   → GPT provider（chatgpt.com/backend-api/codex/responses）
//   - 其他模型（nano-banana-* 等）     → 按 billing.model_prices 配置，不受 params 影响
//
// 注意：gpt-image-2 在 codex 路径出图失败（429 / 5xx / 超时）时，由 runTask 的
// transient fallback 路径自动切到 Adobe Firefly（firefly-gpt-image-2-* alias）兜底，
// 不再在路由层"按 size 强切 adobe"。401 / 配额耗尽不 fallback（号挂了，让用户重试）。
func (s *GenerationService) ImageProviderForModelWithParams(modelCode string, params map[string]any) string {
	return s.imageProvider(strings.TrimSpace(modelCode))
}

func (s *GenerationService) VideoProviderForModel(modelCode string) string {
	return s.videoProvider(strings.TrimSpace(modelCode))
}

// MusicProviderForModel 把音乐模型映射到 provider。
// 查 billing.model_prices (model_code, kind=music)；未配置 fallback=flowmusic。
func (s *GenerationService) MusicProviderForModel(modelCode string) string {
	return s.providerForKind(strings.TrimSpace(modelCode), provider.KindMusic, model.ProviderFLOWMUSIC)
}

func (s *GenerationService) upstreamModel(modelCode string) string {
	if s == nil || s.cfg == nil {
		return modelCode
	}
	raw := s.cfg.GetString(context.Background(), "billing.model_prices", "")
	if raw == "" {
		return modelCode
	}
	var rows []struct {
		ModelCode     string `json:"model_code"`
		UpstreamModel string `json:"upstream_model"`
		Enabled       *bool  `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return modelCode
	}
	for _, row := range rows {
		if row.ModelCode != modelCode || strings.TrimSpace(row.UpstreamModel) == "" {
			continue
		}
		if row.Enabled != nil && !*row.Enabled {
			return modelCode
		}
		return strings.TrimSpace(row.UpstreamModel)
	}
	return modelCode
}

func strParamAny(p map[string]any, key, def string) string {
	if p == nil {
		return def
	}
	if v, ok := p[key]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return def
}

func parseWH(size string) (int, int) {
	parts := strings.SplitN(strings.TrimSpace(size), "x", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	var w, h int
	fmt.Sscanf(parts[0], "%d", &w)
	fmt.Sscanf(parts[1], "%d", &h)
	return w, h
}

func (s *GenerationService) markProviderFailed(ctx context.Context, acc *model.Account, reason string, desiredCooldown time.Duration) {
	markProviderAccountFailed(ctx, s.pool, s.cfg, acc, reason, desiredCooldown)
}

func (s *GenerationService) disableProviderAccount(ctx context.Context, acc *model.Account, reason string) {
	disablePoolAccount(ctx, s.pool, acc, reason)
}

// markProviderInvalid 把账号标记为 token 永久失效（invalid 终态）。
//
// 与 markProviderFailed 的区别：不进 cooldown 复活循环，自动续期不会再把它拉回 valid，
// 专治"IMS 能续 token、firefly 端却拒绝"的僵尸号。仍保留行记录，后台可查、可人工恢复。
func (s *GenerationService) markProviderInvalid(ctx context.Context, acc *model.Account, reason string) {
	if acc == nil || s.pool == nil {
		return
	}
	s.pool.MarkInvalid(ctx, acc.ID, reason)
	acc.Status = model.AccountStatusBroken
	logger.FromCtx(ctx).Warn("account.token_invalidated",
		zap.Uint64("account_id", acc.ID),
		zap.String("provider", acc.Provider),
		zap.String("reason", truncate(reason, 240)))
}

func shouldKeepUpstreamAPIEnabled(acc *model.Account) bool {
	if acc == nil || acc.AuthType != model.AuthTypeAPIKey || acc.BaseURL == nil {
		return false
	}
	return strings.TrimSpace(*acc.BaseURL) != ""
}

func (s *GenerationService) markProviderQuotaLimited(ctx context.Context, acc *model.Account, reason string, until time.Time) {
	if acc == nil || s.pool == nil || s.pool.repo == nil {
		return
	}
	if shouldKeepUpstreamAPIEnabled(acc) {
		s.pool.MarkTransientFailed(ctx, acc.ID, reason)
		return
	}
	fields := map[string]any{
		"status":      model.AccountStatusBroken,
		"last_error":  truncate(reason, 240),
		"error_count": gorm.Expr("error_count + 1"),
	}
	if until.IsZero() {
		until = time.Now().UTC().Add(24 * time.Hour)
	}
	fields["cooldown_until"] = until.UTC()
	if err := s.pool.repo.UpdateForProvider(ctx, acc.ID, acc.Provider, fields); err != nil {
		logger.FromCtx(ctx).Warn("account.quota_limit_failed", zap.Uint64("account_id", acc.ID), zap.Error(err))
		return
	}
	acc.Status = model.AccountStatusBroken
	s.pool.Reload(acc.Provider)
	logger.FromCtx(ctx).Warn("account.quota_limited", zap.Uint64("account_id", acc.ID), zap.String("provider", acc.Provider), zap.Time("cooldown_until", until), zap.String("reason", truncate(reason, 240)))
}

func sleepBeforeRetry(ctx context.Context, base time.Duration, attempt int) {
	if base <= 0 || attempt <= 0 {
		return
	}
	delay := base * time.Duration(attempt)
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	// 如果 ctx 剩余时间不够再多睡这一会儿（且至少给下一次 attempt 留 5s），
	// 直接 skip sleep，免得 sleep 本身把预算耗光让用户多等。
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if left <= 5*time.Second {
			return
		}
		if delay > left-5*time.Second {
			delay = left - 5*time.Second
		}
		if delay <= 0 {
			return
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (s *GenerationService) updateAccountUsageMeta(ctx context.Context, acc *model.Account, t *model.GenerationTask, units int) {
	if acc == nil || units <= 0 || t == nil || acc.OAuthMeta == nil || strings.TrimSpace(*acc.OAuthMeta) == "" {
		return
	}
	if t.Kind != string(provider.KindImage) && t.Kind != string(provider.KindVideo) {
		return
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(*acc.OAuthMeta), &meta); err != nil || meta == nil {
		return
	}
	remaining, ok := metaInt(meta, "image_quota_remaining")
	if !ok {
		return
	}
	remaining -= units
	if remaining < 0 {
		remaining = 0
	}
	meta["image_quota_remaining"] = remaining
	if total, ok := metaInt(meta, "image_quota_total"); ok && total >= remaining {
		meta["image_quota_used"] = total - remaining
	}
	meta["usage_updated_at"] = time.Now().UTC().Unix()
	raw, err := json.Marshal(meta)
	if err != nil {
		return
	}
	sv := string(raw)
	if err := s.db.WithContext(ctx).Model(&model.Account{}).Where("id = ?", acc.ID).Update("oauth_meta", sv).Error; err != nil {
		logger.FromCtx(ctx).Warn("account.usage_meta_update", zap.Uint64("id", acc.ID), zap.Error(err))
		return
	}
	acc.OAuthMeta = &sv
}

func metaInt(meta map[string]any, key string) (int, bool) {
	switch v := meta[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}

func (s *GenerationService) gptOAuthAccessToken(ctx context.Context, acc *model.Account, proxyURL string) (string, error) {
	return resolveGPToAuthAccessToken(ctx, gptOAuthTokenDeps{aes: s.aes, cfg: s.cfg, pool: s.pool}, acc, proxyURL)
}

func (s *GenerationService) resolveProxyURL(ctx context.Context, acc *model.Account, exclude map[uint64]struct{}, allowPoolFallback bool) (string, uint64, error) {
	return resolveAccountProxyURL(ctx, s.proxySvc, s.cfg, acc, exclude, allowPoolFallback)
}

func (s *GenerationService) cacheResultAssets(ctx context.Context, t *model.GenerationTask, acc *model.Account, results []*model.GenerationResult) {
	if len(results) == 0 || s.cfg == nil || s.aes == nil || acc == nil {
		return
	}
	driver := strings.ToLower(strings.TrimSpace(s.cfg.GetString(ctx, "storage.result_cache_driver", "local")))
	if driver == "off" || driver == "none" {
		return
	}
	// redirect / proxy：都不落地、不转存，把上游临时直链（Adobe 预签名 URL 等）
	// 改写成我们自己的 HMAC 短链 /api/v1/m/<token>，真实地址只存在 meta 里。
	//   - redirect：命中短链后 302 跳上游（零带宽，但抓包能看到 Location 里的真实地址）。
	//   - proxy   ：命中短链后由服务器流式拉取并转发字节（走服务器带宽，但真实地址完全隐藏）。
	if driver == "redirect" || driver == "proxy" {
		s.signedMediaResultAssets(ctx, t, results, driver)
		return
	}
	if driver == "oss" && !s.cfg.GetBool(ctx, "oss.enabled", false) {
		driver = "local"
	}
	if driver != "local" && driver != "oss" {
		driver = "local"
	}
	plain, err := s.aes.Decrypt(acc.CredentialEnc)
	if err != nil {
		logger.FromCtx(ctx).Warn("asset.cache.decrypt_failed", zap.Error(err))
		return
	}
	cookie := buildCookieForAssetDownload(string(plain))
	for i, gr := range results {
		if u, ok, meta := s.cacheOneAsset(ctx, driver, cookie, gr.URL, t.TaskID, i, false); ok {
			gr.URL = u
			applyResultMediaMeta(gr, meta)
			s.persistResultMediaMeta(ctx, t.TaskID, i, gr, meta)
		}
		if gr.ThumbURL != nil && *gr.ThumbURL != "" {
			if u, ok, _ := s.cacheOneAsset(ctx, driver, cookie, *gr.ThumbURL, t.TaskID, i, true); ok {
				gr.ThumbURL = &u
			}
		}
	}
}

// redirectResultAssets 实现「重定向直链」存储模式。
//
// 不下载、不转存：把每个 result 的上游临时直链原样存进 meta.upstream_url，
// 再把对外 URL 改写成我们自己签名的短链 /api/v1/gen/media/<token>。
// 用户/客户端访问短链时由 RedirectMedia 验签 → 查回 meta → 302 跳上游。
//
// 仅对「可直接公开访问」的 http(s) 直链生效（典型是 Adobe 预签名 URL）；
// grok 资源需要 cookie 鉴权、data: / 站内相对路径不适用，统统跳过保持原样。
//
// 重要权衡：上游临时直链有寿命，过期后短链会 302 到一个已失效的地址，
// 历史记录里的图就打不开了。这是「省服务器资源 + 隐藏地址」换来的代价。
func (s *GenerationService) signedMediaResultAssets(ctx context.Context, t *model.GenerationTask, results []*model.GenerationResult, mode string) {
	if len(results) == 0 || s.cfg == nil {
		return
	}
	secret := MediaSigningSecret(ctx, s.cfg)
	if len(secret) == 0 {
		// 没配签名密钥时不改写：保持上游 URL（等价于 off），避免功能直接失效。
		logger.FromCtx(ctx).Warn("asset.redirect.no_signing_secret")
		return
	}
	ttl := s.cfg.GetInt(ctx, "storage.redirect_ttl_sec", 86400)
	if ttl <= 0 {
		ttl = 86400
	}
	exp := time.Now().Add(time.Duration(ttl) * time.Second).Unix()
	for i, gr := range results {
		upstream := strings.TrimSpace(gr.URL)
		if !isRedirectableUpstreamURL(upstream) {
			continue
		}
		add := map[string]any{
			"upstream_url": upstream,
			"storage_mode": mode,
		}
		thumbUpstream := ""
		if gr.ThumbURL != nil {
			thumbUpstream = strings.TrimSpace(*gr.ThumbURL)
		}
		if isRedirectableUpstreamURL(thumbUpstream) {
			add["upstream_thumb_url"] = thumbUpstream
		}
		gr.Meta = mergeMetaJSON(gr.Meta, add)
		if tok, err := SignMediaToken(secret, MediaTokenPayload{TaskID: t.TaskID, Seq: i, Exp: exp}); err == nil {
			gr.URL = mediaShortURL + tok + mediaURLSuffix(t.Kind, false)
		} else {
			logger.FromCtx(ctx).Warn("asset.redirect.sign_failed", zap.Error(err))
		}
		if isRedirectableUpstreamURL(thumbUpstream) {
			if tok, err := SignMediaToken(secret, MediaTokenPayload{TaskID: t.TaskID, Seq: i, Thumb: true, Exp: exp}); err == nil {
				u := mediaShortURL + tok + mediaURLSuffix(t.Kind, true)
				gr.ThumbURL = &u
			}
		}
	}
}

// mediaShortURL 是签名媒体短链的统一前缀（短而规范，仍在 /api/ 下以便反代正确路由）。
const mediaShortURL = "/api/v1/m/"

// mediaURLSuffix 给重定向短链补一个统一的文件后缀，方便客户端/浏览器按扩展名识别类型。
// 后缀只是“装饰”，真正的 Content-Type 由 302 跳转后的上游响应决定。
//   - 视频主链 → .mp4，视频缩略图（首帧）→ .jpg
//   - 音乐主链 → .mp3，音乐封面 → .jpg
//   - 图片（含缩略图） → .png
func mediaURLSuffix(kind string, thumb bool) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "video":
		if thumb {
			return ".jpg"
		}
		return ".mp4"
	case "music":
		if thumb {
			return ".jpg"
		}
		return ".mp3"
	}
	return ".png"
}

// isRedirectableUpstreamURL 判断某个 URL 是否能用裸 302 直接对外暴露。
// 必须是 http(s) 绝对地址，且不是需要 cookie 鉴权的 grok 资源。
func isRedirectableUpstreamURL(u string) bool {
	u = strings.TrimSpace(u)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return false
	}
	if strings.Contains(u, "assets.grok.com") {
		return false
	}
	return true
}

// mergeMetaJSON 把 add 合并进已有的 meta JSON（保留原有键），返回新的 JSON 串。
func mergeMetaJSON(existing *string, add map[string]any) *string {
	m := map[string]any{}
	if existing != nil && strings.TrimSpace(*existing) != "" {
		_ = json.Unmarshal([]byte(*existing), &m)
	}
	for k, v := range add {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return existing
	}
	out := string(b)
	return &out
}

type cachedAssetMeta struct {
	Width     *int
	Height    *int
	SizeBytes *int64
}

func (s *GenerationService) cacheOneAsset(ctx context.Context, driver, cookie, rawURL, taskID string, seq int, thumb bool) (string, bool, *cachedAssetMeta) {
	if strings.HasPrefix(strings.TrimSpace(rawURL), "data:") {
		return s.cacheDataURLAsset(ctx, driver, rawURL, taskID, seq, thumb)
	}
	source := normalizeAssetSourceURL(rawURL)
	if source == "" || strings.HasPrefix(source, "/api/v1/gen/cached/") {
		return rawURL, false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return rawURL, false, nil
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", "https://grok.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		logger.FromCtx(ctx).Warn("asset.cache.download_failed", zap.String("url", source), zap.Error(err))
		return rawURL, false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		logger.FromCtx(ctx).Warn("asset.cache.bad_status", zap.String("url", source), zap.Int("status", resp.StatusCode))
		return rawURL, false, nil
	}
	ext := assetExt(source, resp.Header.Get("Content-Type"), thumb)
	now := time.Now()
	rel := path.Join("generated", now.Format("2006"), now.Format("01"), now.Format("02"), fmt.Sprintf("%s_%d%s%s", taskID, seq, map[bool]string{true: "_thumb", false: ""}[thumb], ext))
	root := strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT"))
	if root == "" {
		root = "/app/storage/public"
	}
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		logger.FromCtx(ctx).Warn("asset.cache.mkdir_failed", zap.Error(err))
		return rawURL, false, nil
	}
	f, err := os.Create(dst)
	if err != nil {
		logger.FromCtx(ctx).Warn("asset.cache.create_failed", zap.Error(err))
		return rawURL, false, nil
	}
	defer f.Close()
	written, err := io.Copy(f, resp.Body)
	if err != nil {
		logger.FromCtx(ctx).Warn("asset.cache.write_failed", zap.Error(err))
		return rawURL, false, nil
	}
	if written <= 0 {
		_ = f.Close()
		_ = os.Remove(dst)
		logger.FromCtx(ctx).Warn("asset.cache.empty_file", zap.String("url", source), zap.String("file", dst))
		return rawURL, false, nil
	}
	meta := detectCachedAssetMeta(dst, resp.Header.Get("Content-Type"), written, thumb)
	localURL := "/api/v1/gen/cached/" + rel
	if driver == "oss" {
		if ossURL, err := s.uploadCachedAssetToOSS(ctx, dst, rel, resp.Header.Get("Content-Type")); err == nil && ossURL != "" {
			s.enforceGeneratedCacheLimit(ctx, dst)
			return ossURL, true, meta
		} else if err != nil {
			logger.FromCtx(ctx).Warn("asset.cache.oss_upload_failed", zap.String("file", dst), zap.Error(err))
		}
	}
	s.enforceGeneratedCacheLimit(ctx, dst)
	// 落 cluster locator（单机模式 cluster==nil 时是 no-op）
	if s.cluster != nil {
		kind := model.AssetKindGen
		if thumb {
			kind = model.AssetKindThumb
		}
		s.cluster.RecordLocalLocator(ctx, kind, rel, rel, written, resp.Header.Get("Content-Type"))
	}
	return localURL, true, meta
}

func (s *GenerationService) cacheDataURLAsset(ctx context.Context, driver, rawURL, taskID string, seq int, thumb bool) (string, bool, *cachedAssetMeta) {
	contentType, payload, ok := strings.Cut(strings.TrimSpace(rawURL), ",")
	if !ok || !strings.Contains(contentType, ";base64") {
		return rawURL, false, nil
	}
	contentType = strings.TrimPrefix(contentType, "data:")
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = contentType[:idx]
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		logger.FromCtx(ctx).Warn("asset.cache.data_url_decode_failed", zap.Error(err))
		return rawURL, false, nil
	}
	if len(data) == 0 {
		logger.FromCtx(ctx).Warn("asset.cache.data_url_empty")
		return rawURL, false, nil
	}
	ext := assetExt("", contentType, thumb)
	now := time.Now()
	rel := path.Join("generated", now.Format("2006"), now.Format("01"), now.Format("02"), fmt.Sprintf("%s_%d%s%s", taskID, seq, map[bool]string{true: "_thumb", false: ""}[thumb], ext))
	root := strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT"))
	if root == "" {
		root = "/app/storage/public"
	}
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		logger.FromCtx(ctx).Warn("asset.cache.mkdir_failed", zap.Error(err))
		return rawURL, false, nil
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		logger.FromCtx(ctx).Warn("asset.cache.write_failed", zap.Error(err))
		return rawURL, false, nil
	}
	sizeBytes := int64(len(data))
	meta := detectCachedAssetMeta(dst, contentType, sizeBytes, thumb)
	localURL := "/api/v1/gen/cached/" + rel
	if driver == "oss" {
		if ossURL, err := s.uploadCachedAssetToOSS(ctx, dst, rel, contentType); err == nil && ossURL != "" {
			s.enforceGeneratedCacheLimit(ctx, dst)
			return ossURL, true, meta
		} else if err != nil {
			logger.FromCtx(ctx).Warn("asset.cache.oss_upload_failed", zap.String("file", dst), zap.Error(err))
		}
	}
	s.enforceGeneratedCacheLimit(ctx, dst)
	if s.cluster != nil {
		kind := model.AssetKindGen
		if thumb {
			kind = model.AssetKindThumb
		}
		s.cluster.RecordLocalLocator(ctx, kind, rel, rel, sizeBytes, contentType)
	}
	return localURL, true, meta
}

func applyResultMediaMeta(gr *model.GenerationResult, meta *cachedAssetMeta) {
	if gr == nil || meta == nil {
		return
	}
	if meta.Width != nil {
		gr.Width = intPtr(*meta.Width)
	}
	if meta.Height != nil {
		gr.Height = intPtr(*meta.Height)
	}
	if meta.SizeBytes != nil {
		gr.SizeBytes = int64Ptr(*meta.SizeBytes)
	}
}

func (s *GenerationService) persistResultMediaMeta(ctx context.Context, taskID string, seq int, gr *model.GenerationResult, meta *cachedAssetMeta) {
	if s == nil || s.repo == nil || meta == nil {
		return
	}
	if err := s.repo.UpdateResultMediaMeta(ctx, taskID, seq, meta.Width, meta.Height, meta.SizeBytes); err != nil {
		logger.FromCtx(ctx).Warn("asset.cache.meta_update_failed", zap.String("task_id", taskID), zap.Int("seq", seq), zap.Error(err))
	}
}

func detectCachedAssetMeta(filePath, contentType string, sizeBytes int64, thumb bool) *cachedAssetMeta {
	meta := &cachedAssetMeta{SizeBytes: int64Ptr(sizeBytes)}
	if thumb {
		return meta
	}
	lower := strings.ToLower(strings.TrimSpace(contentType))
	ext := strings.ToLower(filepath.Ext(filePath))
	if strings.Contains(lower, "video/") || ext == ".mp4" || ext == ".webm" {
		if w, h, err := detectMP4Dimensions(filePath); err == nil && w > 0 && h > 0 {
			meta.Width = intPtr(w)
			meta.Height = intPtr(h)
		}
	}
	return meta
}

func detectMP4Dimensions(filePath string) (int, int, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return 0, 0, err
	}
	return parseMP4Dimensions(data)
}

func parseMP4Dimensions(data []byte) (int, int, error) {
	width, height, ok := parseMP4BoxesForDimensions(data, 0, len(data))
	if !ok {
		return 0, 0, fmt.Errorf("mp4 dimensions not found")
	}
	return width, height, nil
}

func parseMP4BoxesForDimensions(data []byte, start, end int) (int, int, bool) {
	offset := start
	for offset+8 <= end {
		size := int(readMP4U32(data, offset))
		header := 8
		if size == 1 {
			if offset+16 > end {
				return 0, 0, false
			}
			size64 := readMP4U64(data, offset+8)
			if size64 > uint64(end-offset) {
				return 0, 0, false
			}
			size = int(size64)
			header = 16
		} else if size == 0 {
			size = end - offset
		}
		if size < header || offset+size > end {
			return 0, 0, false
		}
		boxType := string(data[offset+4 : offset+8])
		payloadStart := offset + header
		payloadEnd := offset + size
		switch boxType {
		case "moov", "trak", "mdia", "minf", "stbl":
			if w, h, ok := parseMP4BoxesForDimensions(data, payloadStart, payloadEnd); ok {
				return w, h, true
			}
		case "tkhd":
			if w, h, ok := parseTKHDDimensions(data[offset:payloadEnd]); ok {
				return w, h, true
			}
		}
		offset += size
	}
	return 0, 0, false
}

func parseTKHDDimensions(box []byte) (int, int, bool) {
	if len(box) < 8+4+8 {
		return 0, 0, false
	}
	header := 8
	if readMP4U32(box, 0) == 1 {
		header = 16
		if len(box) < header+4 {
			return 0, 0, false
		}
	}
	version := box[header]
	var widthOff, heightOff int
	switch version {
	case 1:
		widthOff = len(box) - 8
		heightOff = len(box) - 4
	default:
		widthOff = len(box) - 8
		heightOff = len(box) - 4
	}
	if widthOff < 0 || heightOff < 0 || heightOff+4 > len(box) {
		return 0, 0, false
	}
	width := int(readMP4U32(box, widthOff) >> 16)
	height := int(readMP4U32(box, heightOff) >> 16)
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func readMP4U32(data []byte, offset int) uint32 {
	return uint32(data[offset])<<24 |
		uint32(data[offset+1])<<16 |
		uint32(data[offset+2])<<8 |
		uint32(data[offset+3])
}

func readMP4U64(data []byte, offset int) uint64 {
	return uint64(readMP4U32(data, offset))<<32 | uint64(readMP4U32(data, offset+4))
}

func (s *GenerationService) normalizeInputRefs(ctx context.Context, t *model.GenerationTask, refs []string) []string {
	if len(refs) == 0 || s == nil || s.cfg == nil {
		return refs
	}
	if shouldPreserveInlineRefsForTask(t) {
		return refs
	}
	driver := strings.ToLower(strings.TrimSpace(s.cfg.GetString(ctx, "storage.result_cache_driver", "local")))
	if driver == "off" || driver == "none" {
		driver = "local"
	}
	if driver != "local" && driver != "oss" {
		driver = "local"
	}
	out := make([]string, 0, len(refs))
	for i, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if strings.HasPrefix(ref, "data:") {
			if cached, ok, _ := s.cacheDataURLAsset(ctx, driver, ref, t.TaskID, i, false); ok && cached != "" {
				out = append(out, s.externalRefURL(ctx, t, cached))
				continue
			}
		}
		out = append(out, s.externalRefURL(ctx, t, ref))
	}
	return out
}

func (s *GenerationService) externalRefURL(ctx context.Context, t *model.GenerationTask, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || t == nil {
		return ref
	}
	if t.Provider != model.ProviderPIC2API {
		return ref
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "data:") {
		return ref
	}
	if !strings.HasPrefix(ref, "/api/v1/gen/cached/") {
		return ref
	}
	return AbsolutizeMediaURL(ctx, s.cfg, "", ref)
}

func shouldPreserveInlineRefsForTask(t *model.GenerationTask) bool {
	if t == nil {
		return false
	}
	return t.Provider == model.ProviderGPT && t.Kind == string(provider.KindImage)
}

func compactLargeInlineParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return params
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = compactLargeInlineValue(v)
	}
	return out
}

func compactLargeInlineValue(v any) any {
	switch x := v.(type) {
	case string:
		if len(x) > 2048 && strings.HasPrefix(strings.TrimSpace(x), "data:image/") {
			return "[inline image cached in ref_assets]"
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = compactLargeInlineValue(x[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = compactLargeInlineValue(vv)
		}
		return out
	default:
		return v
	}
}

func (s *GenerationService) uploadCachedAssetToOSS(ctx context.Context, filePath, rel, contentType string) (string, error) {
	if s.cfg == nil {
		return "", fmt.Errorf("missing system config")
	}
	provider := strings.ToLower(strings.TrimSpace(s.cfg.GetString(ctx, "oss.provider", "aliyun")))
	if provider != "" && provider != "aliyun" && provider != "oss" {
		return "", fmt.Errorf("unsupported oss provider %s", provider)
	}
	endpoint := strings.TrimSpace(s.cfg.GetString(ctx, "oss.endpoint", ""))
	bucket := strings.TrimSpace(s.cfg.GetString(ctx, "oss.bucket", ""))
	accessKeyID := strings.TrimSpace(s.cfg.GetString(ctx, "oss.access_key_id", ""))
	accessKeySecret := strings.TrimSpace(s.cfg.GetString(ctx, "oss.access_key_secret", ""))
	if endpoint == "" || bucket == "" || accessKeyID == "" || accessKeySecret == "" {
		return "", fmt.Errorf("oss config incomplete")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	key := s.ossObjectKey(ctx, rel)
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	resource := "/" + bucket + "/" + key
	signing := "PUT\n\n" + contentType + "\n" + date + "\n" + resource
	mac := hmac.New(sha1.New, []byte(accessKeySecret))
	_, _ = mac.Write([]byte(signing))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	putURL := ossObjectURL(endpoint, bucket, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, f)
	if err != nil {
		return "", err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Date", date)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "OSS "+accessKeyID+":"+signature)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("oss upload HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	publicBase := strings.TrimRight(strings.TrimSpace(s.cfg.GetString(ctx, "oss.public_base_url", "")), "/")
	if publicBase != "" {
		return publicBase + "/" + key, nil
	}
	return ossObjectURL(endpoint, bucket, key), nil
}

func (s *GenerationService) ossObjectKey(ctx context.Context, rel string) string {
	prefix := "generated/{yyyy}/{mm}/{dd}"
	if s.cfg != nil {
		prefix = strings.TrimSpace(s.cfg.GetString(ctx, "oss.path_prefix", prefix))
	}
	now := time.Now()
	prefix = strings.Trim(prefix, "/")
	prefix = strings.ReplaceAll(prefix, "{yyyy}", now.Format("2006"))
	prefix = strings.ReplaceAll(prefix, "{mm}", now.Format("01"))
	prefix = strings.ReplaceAll(prefix, "{dd}", now.Format("02"))
	if prefix == "" {
		return path.Base(rel)
	}
	return prefix + "/" + path.Base(rel)
}

func ossObjectURL(endpoint, bucket, key string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint + "/" + escapePathSegments(key)
	}
	if !strings.HasPrefix(u.Host, bucket+".") {
		u.Host = bucket + "." + u.Host
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + escapePathSegments(key)
	u.RawQuery = ""
	return u.String()
}

func escapePathSegments(v string) string {
	parts := strings.Split(v, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

type cacheEntry struct {
	path    string
	size    int64
	modTime time.Time
}

func (s *GenerationService) enforceGeneratedCacheLimit(ctx context.Context, keepPath string) {
	if s == nil || s.cfg == nil {
		return
	}
	limitGB := s.cfg.GetInt(ctx, "storage.result_cache_max_gb", 0)
	if limitGB <= 0 {
		return
	}
	root := generatedCacheRootPath()
	keepPath = filepath.Clean(keepPath)
	limitBytes := limitGB * 1024 * 1024 * 1024

	var total int64
	entries := make([]cacheEntry, 0, 64)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		total += info.Size()
		entries = append(entries, cacheEntry{
			path:    filepath.Clean(path),
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		return nil
	})
	if total <= limitBytes {
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].modTime.Equal(entries[j].modTime) {
			return entries[i].path < entries[j].path
		}
		return entries[i].modTime.Before(entries[j].modTime)
	})

	var deletedFiles int64
	var deletedBytes int64
	for _, entry := range entries {
		if total <= limitBytes {
			break
		}
		if entry.path == keepPath {
			continue
		}
		if err := os.Remove(entry.path); err != nil && !os.IsNotExist(err) {
			continue
		}
		total -= entry.size
		deletedFiles++
		deletedBytes += entry.size
	}
	if deletedFiles == 0 {
		return
	}

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || !d.IsDir() || filepath.Clean(path) == filepath.Clean(root) {
			return nil
		}
		_ = os.Remove(path)
		return nil
	})

	logger.FromCtx(ctx).Info(
		"asset.cache.limit_enforced",
		zap.Int64("limit_gb", limitGB),
		zap.Int64("deleted_files", deletedFiles),
		zap.Int64("deleted_bytes", deletedBytes),
		zap.Int64("remain_bytes", total),
	)
}

func generatedCacheRootPath() string {
	root := strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT"))
	if root == "" {
		root = "/app/storage/public"
	}
	return filepath.Clean(filepath.Join(filepath.Clean(root), "generated"))
}

func normalizeAssetSourceURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "data:") {
		return ""
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return v
	}
	return "https://assets.grok.com/" + strings.TrimLeft(v, "/")
}

func assetExt(source, contentType string, thumb bool) string {
	lower := strings.ToLower(source)
	for _, ext := range []string{".mp4", ".webm", ".png", ".jpg", ".jpeg", ".webp"} {
		if strings.Contains(lower, ext) {
			if ext == ".jpeg" {
				return ".jpg"
			}
			return ext
		}
	}
	for _, ext := range []string{".mp3", ".m4a", ".wav", ".ogg", ".flac"} {
		if strings.Contains(lower, ext) {
			return ext
		}
	}
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "video/webm"):
		return ".webm"
	case strings.Contains(ct, "video/"):
		return ".mp4"
	case strings.Contains(ct, "audio/mpeg") || strings.Contains(ct, "audio/mp3"):
		return ".mp3"
	case strings.Contains(ct, "audio/mp4") || strings.Contains(ct, "audio/x-m4a") || strings.Contains(ct, "audio/aac"):
		return ".m4a"
	case strings.Contains(ct, "audio/wav") || strings.Contains(ct, "audio/x-wav"):
		return ".wav"
	case strings.Contains(ct, "audio/ogg"):
		return ".ogg"
	case strings.Contains(ct, "audio/"):
		return ".mp3"
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case thumb:
		return ".jpg"
	default:
		return ".bin"
	}
}

func buildCookieForAssetDownload(cred string) string {
	cred = strings.TrimSpace(cred)
	if strings.Contains(cred, "=") {
		if !strings.Contains(cred, "sso-rw=") {
			if token := extractCookieValue(cred, "sso"); token != "" {
				cred = strings.TrimRight(cred, "; ") + "; sso-rw=" + token
			}
		}
		return cred
	}
	return "sso=" + cred + "; sso-rw=" + cred
}

func extractCookieValue(cookie, name string) string {
	prefix := name + "="
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, prefix) {
			return strings.TrimPrefix(part, prefix)
		}
	}
	return ""
}

func (s *GenerationService) failTask(ctx context.Context, t *model.GenerationTask, reason string) {
	// 记录原始 reason 到日志便于排查；写库 / 返用户的只用脱敏后的版本。
	logger.FromCtx(ctx).Info("gen.fail.raw_reason",
		zap.String("task", t.TaskID),
		zap.String("reason", reason),
	)
	displayReason := userFacingGenerationError(reason)
	if err := s.repo.SetFailed(ctx, t.TaskID, displayReason); err != nil {
		logger.FromCtx(ctx).Warn("gen.fail.update_status", zap.Error(err))
	}
	if t.CostPoints > 0 {
		if err := s.billing.FailRefund(ctx, t.TaskID, displayReason); err != nil {
			logger.FromCtx(ctx).Warn("gen.fail.refund", zap.Error(err))
		}
	}
	s.dispatchTaskWebhook(ctx, t.TaskID)
}

// ReapStaleTasks closes tasks that were left pending/running after a restart or
// a killed provider request. Normal in-flight jobs have much shorter context
// deadlines than these cutoffs, so this only catches genuinely abandoned rows.
func (s *GenerationService) ReapStaleTasks(ctx context.Context, userID uint64) {
	if s == nil || s.db == nil {
		return
	}
	now := time.Now().UTC()
	// 收尾两类卡死 task：
	//   1) 年龄 > 1 小时还没终态（兼容 normal 慢任务场景）
	//   2) attempt 已撞到 ClaimAttemptHardCap（即使年龄 < 1h，也已经反复被抢，必失败）
	//      —— 配合 ClaimBatch 的 attempt < cap 过滤，这两个加起来确保循环 reclaim 一旦
	//      被检测出，下次 ReapStaleTasks 调用立刻收尾退款。
	cutoff := now.Add(-1 * time.Hour)
	var tasks []*model.GenerationTask
	q := s.db.WithContext(ctx).
		Where("deleted_at IS NULL AND status IN ?", []int8{model.GenStatusPending, model.GenStatusRunning}).
		Where(
			"((started_at IS NOT NULL AND started_at < ?) OR (started_at IS NULL AND created_at < ?)) OR attempt >= ?",
			cutoff, cutoff, repo.ClaimAttemptHardCap,
		).
		Order("id ASC").
		Limit(200)
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.Find(&tasks).Error; err != nil {
		logger.FromCtx(ctx).Warn("gen.stale.query_failed", zap.Error(err))
		return
	}
	for _, t := range tasks {
		reason := "任务执行超时，已自动结束"
		if int(t.Attempt) >= repo.ClaimAttemptHardCap {
			reason = "任务调度异常（reclaim 次数超阈值），已自动结束"
		}
		s.failTask(ctx, t, reason)
	}
}

// ReapStuckRunningTasks manually closes running tasks that have not changed for
// at least minAge. Admin uses this after deploy/restart incidents so frozen
// rows leave "生成中" and users get the normal failure refund path.
func (s *GenerationService) ReapStuckRunningTasks(ctx context.Context, minAge time.Duration, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	if minAge <= 0 {
		minAge = 10 * time.Minute
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	cutoff := time.Now().UTC().Add(-minAge)
	var tasks []*model.GenerationTask
	if err := s.db.WithContext(ctx).
		Where("deleted_at IS NULL AND status = ?", model.GenStatusRunning).
		Where("COALESCE(updated_at, started_at, created_at) < ?", cutoff).
		Order("id ASC").
		Limit(limit).
		Find(&tasks).Error; err != nil {
		return 0, err
	}
	for _, t := range tasks {
		s.failTask(ctx, t, "任务执行被中断，已自动结束并退款")
	}
	return len(tasks), nil
}

// === helpers ===

func intPtr(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func int64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

// newULID 生成一个 26 字符 ULID（Crockford base32 简化版）。
//
// 用 UUID 转 hex 后截 26 位（在严格 ULID 库引入前的过渡方案）。
func newULID() string {
	id := uuid.NewString()
	clean := ""
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if ch == '-' {
			continue
		}
		clean += string(ch)
		if len(clean) == 26 {
			break
		}
	}
	return clean
}

var _ = errors.New

var usageLimitResetAtRe = regexp.MustCompile(`"resets_at"\s*:\s*([0-9]+)`)

func isFatalOAuthRefreshError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "refresh oauth access_token failed") {
		return false
	}
	return strings.Contains(msg, " 401") ||
		strings.Contains(msg, "返回 401") ||
		strings.Contains(msg, "already been used") ||
		strings.Contains(msg, "please try signing in again") ||
		strings.Contains(msg, "invalid_request_error")
}

func retryableProviderError(err error) bool {
	if err == nil {
		return false
	}
	if isZeroImageReturnedError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	if isAdobeNonRetryableError(err) {
		return false
	}
	if isAdobeRetryableError(err) {
		return true
	}
	return isFatalOAuthRefreshError(err) ||
		isUsageLimitReachedError(err) ||
		strings.Contains(msg, "http 408") ||
		strings.Contains(msg, "timeout_error") ||
		strings.Contains(msg, "system under load") ||
		strings.Contains(msg, "backpressure_limited") ||
		strings.Contains(msg, "http 429") ||
		strings.Contains(msg, "too many requests") ||
		// HTTP/2 流被对端切：换号或重试同号都可能恢复（典型上游 chatgpt/Cloudflare 抖动）。
		strings.Contains(msg, "stream error") ||
		strings.Contains(msg, "internal_error") ||
		strings.Contains(msg, "rst_stream") ||
		strings.Contains(msg, "http2: server sent goaway") ||
		strings.Contains(msg, "client connection lost") ||
		isGrokRetryableForbiddenError(msg) ||
		isGPTWebChallenge(msg) ||
		isCodexChatRetryableError(msg) ||
		isRetryableProxyTransportError(err)
}

// isCodexChatRetryableError 匹配 chatgpt.com/backend-api/codex 的 Cloudflare / 临时 upstream 错误。
// 命中后 completeCodex 会换号 / 换代理重试，但不 cooldown 账号（见 markChatFailure）。
func isCodexChatRetryableError(msg string) bool {
	if msg == "" || !strings.Contains(msg, "codex chat http") {
		return false
	}
	return strings.Contains(msg, "http 403") ||
		strings.Contains(msg, " 403:") ||
		strings.Contains(msg, "http 429") ||
		strings.Contains(msg, " 429:") ||
		strings.Contains(msg, "http 500") ||
		strings.Contains(msg, " 500:") ||
		strings.Contains(msg, "http 502") ||
		strings.Contains(msg, " 502:") ||
		strings.Contains(msg, "http 503") ||
		strings.Contains(msg, " 503:") ||
		strings.Contains(msg, "http 504") ||
		strings.Contains(msg, " 504:") ||
		strings.Contains(msg, "cloudflare") ||
		strings.Contains(msg, "just a moment") ||
		strings.Contains(msg, "forbidden")
}

// isGPTWebChallenge 匹配 GPT image2 web 链路上的"换号即可恢复"型错误。
//
// 错误格式由 provider/gpt/gpt.go 的 webBootstrap / webRequirements / webPrepare
// / webConversation / webPoll / webLibrary / webUpload* 这些 step 抛出，
// 模式都是 "gpt image2 web <step> <status>: <body>"。
//
// 4xx (401 / 403 / 429) → 当前号 + 当前出口 IP 在这个时间窗进不去，
// 换号 / 换代理最有效；调度器会把当前 acc.ID 加入 triedAccountIDs，
// 下一轮 ReserveWhere 自动绕开。
// 5xx → 上游临时抖动，换号或重试同号都可恢复。
//
// 命中这个判断后：
//   - retryableProviderError → true，进入 attempt+1 循环；
//   - markProviderFailed 会按 circuit_cooldown_seconds 配置走熔断（默认 5min），
//     ErrorCount 累计到 tolerance.circuit_failures 阈值后该号才真正进入冷却，
//     单次抖动不会把号扔进冷却堆。
func isGPTWebChallenge(msg string) bool {
	if msg == "" {
		return false
	}
	if !strings.Contains(msg, "gpt image2 web") {
		return false
	}
	return strings.Contains(msg, " 401:") ||
		strings.Contains(msg, " 403:") ||
		strings.Contains(msg, " 429:") ||
		strings.Contains(msg, " 500:") ||
		strings.Contains(msg, " 502:") ||
		strings.Contains(msg, " 503:") ||
		strings.Contains(msg, " 504:")
}

// isAdobeRetryableError 命中后调度器会换号 / 换代理重试。
//
// 对应 firefly 子包：
//   - AuthError      (401/403)               → 当前 token 失效，换号
//   - RateLimitedError (429)                  → 换号
//   - QuotaExhaustedError (taste_exhausted)   → 换号
//   - ProviderBlockedError (上游 3p 拒绝)     → 同 token 重试一次后换号
//   - NotEntitledError (user_not_entitled)   → 当前号不开通此档位，换号试是否有号开通；
//     走 transient 路径，不冷却当前号
//   - UpstreamTemporaryError                  → 临时网络 / 5xx 可重试
func isAdobeRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var (
		authErr     *firefly.AuthError
		rateErr     *firefly.RateLimitedError
		quotaErr    *firefly.QuotaExhaustedError
		blockedErr  *firefly.ProviderBlockedError
		entitledErr *firefly.NotEntitledError
		tempErr     *firefly.UpstreamTemporaryError
	)
	if errors.As(err, &authErr) || errors.As(err, &rateErr) ||
		errors.As(err, &quotaErr) || errors.As(err, &blockedErr) ||
		errors.As(err, &entitledErr) {
		return true
	}
	if errors.As(err, &tempErr) {
		return tempErr.Retryable
	}
	return false
}

// isAdobeAuthError 识别 firefly token 鉴权失败（干净的 401/403，已排除 taste_exhausted /
// blocked_by_3p / not_entitled / 反爬 HTML 挑战）。命中即认定 token 死了，调度层据此
// 把账号置 invalid 终态（一次判死，不复活），打断僵尸号循环。
func isAdobeAuthError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *firefly.AuthError
	return errors.As(err, &authErr)
}

// isAdobeNotEntitledError 单独识别 NotEntitledError。
//
// 调度器命中这个判定后会走 MarkTransientFailed 路径，避免该账号 error_count++
// 进而被熔断——这个账号在低档位/其他模型下完全正常，只是没买高档位权益。
func isAdobeNotEntitledError(err error) bool {
	if err == nil {
		return false
	}
	var entitledErr *firefly.NotEntitledError
	return errors.As(err, &entitledErr)
}

// adobeEntitlementTTL 学到的"该号无 X 档位权益"标记的有效期。
// 7 天之后再撞到同档位任务会让它"重新试一次"，避免运营把老号充值升级
// 之后系统仍然死板地跳过它。
const adobeEntitlementTTL = 7 * 24 * time.Hour

// adobeResolutionTier 把任务 params 里的 resolution / size_tier 归一成
// "1K" / "2K" / "4K"，对应 firefly.payloads 里 sizeFromRatio 的三档。
//
// 默认值 "2K"——这是 firefly 默认 quality。视频任务不调用本函数（视频另有
// VideoResolution，与图片档位不同语义）。
func adobeResolutionTier(params map[string]any) string {
	raw := ""
	if v, ok := params["resolution"]; ok {
		if s, ok := v.(string); ok {
			raw = s
		}
	}
	if raw == "" {
		if v, ok := params["size_tier"]; ok {
			if s, ok := v.(string); ok {
				raw = s
			}
		}
	}
	raw = strings.ToUpper(strings.TrimSpace(raw))
	switch raw {
	case "1K", "1":
		return "1K"
	case "4K", "4":
		return "4K"
	case "2K", "2", "":
		return "2K"
	}
	return raw
}

// adobeEntitlementKey 把档位字符串转成 oauth_meta JSON 里的 "no_<tier>" 字段名。
// 仅 "1K" / "2K" / "4K" 会返回非空值，其它档位（含视频）返回空字符串表示
// "不学习这一类错误"。
func adobeEntitlementKey(tier string) string {
	switch tier {
	case "1K":
		return "no_1k"
	case "2K":
		return "no_2k"
	case "4K":
		return "no_4k"
	}
	return ""
}

// adobeEntitlementOKKey 与 adobeEntitlementKey 对应，但写"该号确实开通了
// <tier> 档位"。两条 key 在同一份 entitlements_json 里共存，谁的
// checked_at 更新就以谁为准（parseAdobeEntitlements 里做冲突解析）。
func adobeEntitlementOKKey(tier string) string {
	switch tier {
	case "1K":
		return "ok_1k"
	case "2K":
		return "ok_2k"
	case "4K":
		return "ok_4k"
	}
	return ""
}

// accountSupportsAdobeTier 检查 Account.OAuthMeta（实际存 pool_adobe.entitlements_json）
// 里是否记录了"该号在某档位上 not entitled"。
//
// 语义：
//   - 没有 meta / 没有对应 key → 返回 true（默认乐观，让他试一次）
//   - 有 no_<tier> = true 且 checked_at 在 TTL 内 → 返回 false（跳过该号）
//   - 有 no_<tier> = true 但 checked_at 已过期 → 返回 true（允许重试探测）
//   - 同时存在 no_<tier> 与 ok_<tier>：取 checked_at 更新的那个为准
//     （场景：先撞 not_entitled，运营升级账号后 1 次成功跑通 → ok 应该覆盖 no）
//
// pickAccountForTask 在 ReserveWhere 的 predicate 里调用这个函数，
// 提前过滤掉确认无权益的号，避免浪费重试次数。
func accountSupportsAdobeTier(acc *model.Account, tier string) bool {
	noKey := adobeEntitlementKey(tier)
	okKey := adobeEntitlementOKKey(tier)
	if noKey == "" || acc == nil {
		return true
	}
	meta := accountOAuthMeta(acc)
	noFlag, _ := meta[noKey].(bool)
	if !noFlag {
		return true
	}
	noStamp := int64FromMeta(meta, noKey+"_checked_at")
	if noStamp <= 0 {
		// 没记时间戳，按"过期"处理 → 允许重试探测。
		return true
	}
	if time.Since(time.Unix(noStamp, 0)) >= adobeEntitlementTTL {
		return true
	}
	// 如果同时记录了"成功跑通"，且 ok 更晚于 no，按"现在可用"对待。
	if okFlag, _ := meta[okKey].(bool); okFlag {
		okStamp := int64FromMeta(meta, okKey+"_checked_at")
		if okStamp > noStamp {
			return true
		}
	}
	return false
}

// recordAdobeNotEntitled 把"该号在某档位 not entitled"写到 entitlements_json。
// 调用方应只在 isAdobeNotEntitledError(err) 命中且 tier 已识别时调用。
func (s *GenerationService) recordAdobeNotEntitled(ctx context.Context, acc *model.Account, tier string) {
	s.recordAdobeEntitlement(ctx, acc, tier, adobeEntitlementKey(tier))
}

// recordAdobeEntitlementOK 与 recordAdobeNotEntitled 对称：在 Adobe 图像任务
// 成功后调用，写一个 ok_<tier>=true 标记 + 时间戳。
//
// 重点：复用同一份 meta，所以已有的 no_<other> 字段不会被抹平
// （例如 1K 成功时不会把 no_4k 误删）。
func (s *GenerationService) recordAdobeEntitlementOK(ctx context.Context, acc *model.Account, tier string) {
	s.recordAdobeEntitlement(ctx, acc, tier, adobeEntitlementOKKey(tier))
}

// recordAdobeEntitlement 是 record{NotEntitled,EntitlementOK} 共享的实现，
// 只负责"读出现有 meta → 加上指定 key=true + key_checked_at → 写回"。
func (s *GenerationService) recordAdobeEntitlement(ctx context.Context, acc *model.Account, tier, key string) {
	if key == "" || acc == nil || s.pool == nil || s.pool.repo == nil {
		return
	}
	meta := accountOAuthMeta(acc)
	meta[key] = true
	meta[key+"_checked_at"] = time.Now().UTC().Unix()
	raw, err := json.Marshal(meta)
	if err != nil {
		return
	}
	sv := string(raw)
	if err := s.pool.repo.UpdateForProvider(ctx, acc.ID, acc.Provider, map[string]any{
		"oauth_meta": sv,
	}); err != nil {
		logger.FromCtx(ctx).Warn(
			"account.entitlement_record",
			zap.Uint64("id", acc.ID),
			zap.String("tier", tier),
			zap.String("key", key),
			zap.Error(err),
		)
		return
	}
	acc.OAuthMeta = &sv
}

// isAdobeNonRetryableError 命中后立刻失败，不重试不熔断号。
// 用户提示词违规 / 上游 422 永久错误等不应该耗号。
func isAdobeNonRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var (
		policyErr *firefly.ContentPolicyError
		reqErr    *firefly.AdobeRequestError
	)
	if errors.As(err, &policyErr) {
		return true
	}
	if errors.As(err, &reqErr) {
		// IsInvalidUsage422 仍是 anti-bot 触发的 422，反而要换号。
		return !reqErr.IsInvalidUsage422()
	}
	return false
}

func shouldRotateProxyOnRetry(providerName string, err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch providerName {
	case model.ProviderGROK:
		return isGrokRetryableForbiddenError(msg) || isRetryableProxyTransportError(err)
	case model.ProviderGPT:
		return isCodexChatRetryableError(msg) || isRetryableProxyTransportError(err)
	case model.ProviderADOBE:
		// 451 现在按内容安全直接返回，不再切代理 / 换号重试。
		return !isAdobeRegionBlockedError(err) && (isAdobeSystemUnderLoadError(err) || isRetryableProxyTransportError(err))
	default:
		return false
	}
}

func isAdobeSystemUnderLoadError(err error) bool {
	if err == nil {
		return false
	}
	var tempErr *firefly.UpstreamTemporaryError
	if errors.As(err, &tempErr) {
		if tempErr.StatusCode == 408 {
			return true
		}
		msg := strings.ToLower(tempErr.Message)
		return strings.Contains(msg, "timeout_error") || strings.Contains(msg, "system under load")
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 408") ||
		strings.Contains(msg, "timeout_error") ||
		strings.Contains(msg, "system under load")
}

// isAdobeRegionBlockedError 命中 Adobe firefly 返回的 451 区域限制错误。
//
// 现在产品上把这类 451 作为提示词安全失败直接返回给用户，不做重试。
func isAdobeRegionBlockedError(err error) bool {
	if err == nil {
		return false
	}
	var tempErr *firefly.UpstreamTemporaryError
	if errors.As(err, &tempErr) {
		return tempErr.StatusCode == 451
	}
	return false
}

func isRetryableProxyTransportError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "write request failed") ||
		strings.Contains(msg, "read response failed: eof") ||
		strings.Contains(msg, "failed: eof") ||
		strings.Contains(msg, "unexpected eof")
}

func isTransientProviderPathError(provider string, err error) bool {
	if err == nil || provider != model.ProviderGROK {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 403") && isGrokRetryableForbiddenError(msg)
}

func isGrokRetryableForbiddenError(msg string) bool {
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "grok upload http 403") ||
		strings.Contains(msg, "grok video http 403") ||
		strings.Contains(msg, "grok media post http 403") ||
		strings.Contains(msg, "grok http 403") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "cloudflare") ||
		strings.Contains(msg, "just a moment") ||
		strings.Contains(msg, "request rejected by anti-bot rules")
}

func isUsageLimitReachedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "usage_limit_reached") ||
		strings.Contains(msg, "the usage limit has been reached") ||
		strings.Contains(msg, "\"plan_type\":\"free\"") ||
		strings.Contains(msg, "\"plan_type\": \"free\"")
}

func isZeroImageReturnedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "returned 0 image")
}

func isProviderQuotaLimitedError(err error) bool {
	if err == nil {
		return false
	}
	var quotaErr *firefly.QuotaExhaustedError
	if errors.As(err, &quotaErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return isUsageLimitReachedError(err) ||
		strings.Contains(msg, "taste_exhausted") ||
		strings.Contains(msg, "quota_exhausted") ||
		strings.Contains(msg, "quota exhausted") ||
		strings.Contains(msg, "配额耗尽")
}

func usageLimitResetAt(err error) time.Time {
	if err == nil {
		return time.Time{}
	}
	m := usageLimitResetAtRe.FindStringSubmatch(err.Error())
	if len(m) != 2 {
		return time.Time{}
	}
	sec, e := strconv.ParseInt(m[1], 10, 64)
	if e != nil || sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func providerCooldown(err error) time.Duration {
	if err == nil {
		return 5 * time.Minute
	}
	if d, ok := adobeCooldown(err); ok {
		return d
	}
	msg := strings.ToLower(err.Error())
	if isGrokRetryableForbiddenError(msg) {
		return 0
	}
	switch {
	case strings.Contains(msg, "http 429"), strings.Contains(msg, "too many requests"):
		return 30 * time.Minute
	case strings.Contains(msg, "http 403"), strings.Contains(msg, "forbidden"),
		strings.Contains(msg, "cloudflare"), strings.Contains(msg, "just a moment"),
		strings.Contains(msg, "anti-bot"), strings.Contains(msg, "request rejected"):
		return 2 * time.Hour
	case strings.Contains(msg, "anti-bot"), strings.Contains(msg, "request rejected"):
		return 2 * time.Hour
	default:
		return 10 * time.Minute
	}
}

// adobeCooldown 把 firefly 子包的结构化错误映射成 cooldown 时长。
// 返回 (duration, true) 时跳过通用 cooldown 分支。
func adobeCooldown(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	var (
		authErr  *firefly.AuthError
		quotaErr *firefly.QuotaExhaustedError
		rateErr  *firefly.RateLimitedError
		blocked  *firefly.ProviderBlockedError
		tempErr  *firefly.UpstreamTemporaryError
	)
	switch {
	case errors.As(err, &authErr):
		// 401/403：当前 token 不可用，半小时后再试（让 refresh worker 有机会拿新 token）。
		return 30 * time.Minute, true
	case errors.As(err, &quotaErr):
		// 配额耗尽：今天剩下时间内别再点这个号。
		return 24 * time.Hour, true
	case errors.As(err, &rateErr):
		if rateErr.RetryAfter > 0 {
			return rateErr.RetryAfter, true
		}
		return 30 * time.Minute, true
	case errors.As(err, &blocked):
		// 上游 3p 提供方拒绝（modelProvider 临时下线）：短熔断，让其他号继续跑。
		return 5 * time.Minute, true
	case errors.As(err, &tempErr):
		return 1 * time.Minute, true
	}
	return 0, false
}

// isGPTCodexTransientError 判断 chatgpt.com/backend-api/codex 的错误是不是
// 「该让 Gemini 兜底」的错误。命中后 runTask 会切到 Adobe Firefly 兜底重试。
//
// 命中（fallback to gemini）：
//   - HTTP 408 / 429 / 500 / 502 / 503 / 504
//   - context deadline exceeded / timeout / EOF / broken pipe
//   - cloudflare / "just a moment" 等 anti-bot 拦截
//   - HTTP 401 / invalid_grant / invalid_token / unauthorized（号挂了，但 Gemini 池能跑就出图）
//   - usage_limit_reached（GPT 配额耗尽，但 Gemini 池跟 GPT 池是不同的计费来源，能跑就跑）
//
// 不命中（业务错误 / 内容违规，换上游也救不了）：
//   - "model is not supported when using codex" / "instructions are required" 等参数错误
//   - content_policy / moderation / safety / unsafe / nsfw 等内容审核（adobe 一样会拒）
func isGPTCodexTransientError(err error) bool {
	if err == nil {
		return false
	}
	if isZeroImageReturnedError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	// 业务参数错误（model/instructions/size）：fallback 也只会同样错，不浪费。
	if strings.Contains(msg, "is not supported when using codex") ||
		strings.Contains(msg, "instructions are required") ||
		strings.Contains(msg, "stream must be set") ||
		strings.Contains(msg, "invalid_request_error") {
		return false
	}
	// 内容审核类错误：换上游不会让违规 prompt 通过，省一次 fallback。
	if strings.Contains(msg, "content_policy") ||
		strings.Contains(msg, "content policy") ||
		strings.Contains(msg, "moderation") ||
		strings.Contains(msg, "safety_violation") ||
		strings.Contains(msg, "prompt violates") ||
		strings.Contains(msg, "unsafe") ||
		strings.Contains(msg, "nsfw") {
		return false
	}
	// 401 / invalid auth：GPT 号挂了。改成 fallback：Adobe 池里也有 image2
	// 完整别名，能跑就出图，不应该让用户硬等 token 刷新。
	if strings.Contains(msg, "http 401") ||
		strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "invalid_token") ||
		strings.Contains(msg, "unauthorized") {
		return true
	}
	// 配额耗尽：GPT plus / pro 配额跟 Adobe Firefly 配额完全独立，应该让 Gemini 池兜底。
	if isUsageLimitReachedError(err) {
		return true
	}
	// 命中 transient：429 / 5xx / 网络抖动 / cloudflare / context 超时
	switch {
	case strings.Contains(msg, "http 408"),
		strings.Contains(msg, "http 429"),
		strings.Contains(msg, "too many requests"):
		return true
	case strings.Contains(msg, "http 500"),
		strings.Contains(msg, "http 502"),
		strings.Contains(msg, "http 503"),
		strings.Contains(msg, "http 504"),
		strings.Contains(msg, "bad gateway"),
		strings.Contains(msg, "gateway timeout"),
		strings.Contains(msg, "service unavailable"):
		return true
	case strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "timeout"),
		strings.Contains(msg, "eof"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "i/o timeout"):
		return true
	// HTTP/2 流被对端 RST_STREAM 切断（典型：Cloudflare 流式超时 / chatgpt 风控 /
	// 复用了对端已关闭的连接）。Go 的 http2.Transport 错误文本固定为：
	//   "stream error: stream ID N; <CODE>; received from peer"
	// 命中后让 runTask 换号重试 / Adobe fallback；同号一般会 cooldown 配套。
	case strings.Contains(msg, "stream error"),
		strings.Contains(msg, "internal_error"),
		strings.Contains(msg, "rst_stream"),
		strings.Contains(msg, "stream id "),
		strings.Contains(msg, "http2: server sent goaway"),
		strings.Contains(msg, "client connection lost"):
		return true
	case strings.Contains(msg, "cloudflare"),
		strings.Contains(msg, "just a moment"),
		strings.Contains(msg, "anti-bot"):
		return true
	}
	return false
}

// runImageFallbackToAdobe gpt-image-2 走 ChatGPT Codex 路径全部失败后，把同一任务
// 改成走 Adobe Firefly（firefly-gpt-image-2-* alias）兜底重跑一次。
//
// 注意：
//   - 不修改 t.Provider 字段（保持原 "gpt" 让 history 看着是 gpt 任务）；
//   - res.EffectiveModelCode 会被主流程改成 "gpt-image-2@adobe-fallback"，
//     若 priceFn 命中差价就触发 FinalizeUsage 退款；
//   - 返回的 fallbackAccountID 会被主流程包成空壳 *Account 用于结算 lock。
func (s *GenerationService) runImageFallbackToAdobe(
	ctx context.Context,
	t *model.GenerationTask,
	params map[string]any,
	refs []string,
	timeout time.Duration,
) (*provider.Result, uint64, error) {
	adobeProv, ok := s.providers[model.ProviderADOBE]
	if !ok || adobeProv == nil {
		return nil, 0, fmt.Errorf("adobe provider not registered")
	}
	tier := adobeResolutionTier(params)
	acc, err := s.pool.ReserveForTaskWhere(ctx, model.ProviderADOBE, "round_robin", t.TaskID, "fallback-adobe", func(a *model.Account) bool {
		return accountSupportsAdobeTier(a, tier)
	})
	if err != nil || acc == nil {
		return nil, 0, fmt.Errorf("pick adobe account: %w", err)
	}
	defer s.pool.ReleaseForTask(ctx, model.ProviderADOBE, acc.ID, t.TaskID)

	provReq := &provider.Request{
		TaskID:    t.TaskID,
		Kind:      provider.Kind(t.Kind),
		Mode:      provider.Mode(t.Mode),
		ModelCode: t.ModelCode, // 保持 "gpt-image-2"，由 firefly resolver 命中 publicModelAliases["gpt-image-2"] 走 firefly-gpt-image-2-* alias
		Prompt:    t.Prompt,
		Params:    params,
		RefAssets: refs,
		Count:     t.Count,
		Account:   acc,
	}
	provReq.UpstreamLog = s.makeUpstreamLogger(t, acc)
	provReq.OnPollProgress = s.makePollProgressUpdater(t)
	if t.NegPrompt != nil {
		provReq.NegPrompt = *t.NegPrompt
	}
	if acc.BaseURL != nil {
		provReq.BaseURL = *acc.BaseURL
	}
	proxyURL, _, perr := s.resolveProxyURL(ctx, acc, nil, true)
	if perr == nil {
		provReq.ProxyURL = proxyURL
	}
	if s.aes != nil {
		cred, derr := s.providerCredential(ctx, acc, provReq.ProxyURL)
		if derr != nil {
			return nil, acc.ID, fmt.Errorf("decrypt adobe credential: %w", derr)
		}
		provReq.Credential = cred
		s.applyAdobeSubmitMode(ctx, provReq, acc)
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, callErr := adobeProv.Generate(rctx, provReq)
	if callErr != nil {
		// 给运营一个 transient 标记，不立刻进 cooldown
		if isAdobeNotEntitledError(callErr) && t.Kind == string(provider.KindImage) && tier != "" {
			s.recordAdobeNotEntitled(ctx, acc, tier)
		}
		s.pool.MarkTransientFailed(ctx, acc.ID, callErr.Error())
		return nil, acc.ID, callErr
	}
	s.pool.MarkUsed(ctx, acc.ID)
	return out, acc.ID, nil
}

// userFacingGenerationError 把内部 / 上游错误翻译成「用户视角能看懂、且不泄露上游品牌
// 或具体接口路径」的中文短句。
//
// 设计目标：
//   - 默认完全脱敏：未匹配到具体类别就返回通用「生成失败，请稍后重试」，
//     不把上游 (adobe / firefly / grok / openai / chatgpt) 名字 / JSON body /
//     接口路径直接吐到用户面前。
//   - 已匹配类别的描述统一中文化，避免 "HTTP 429 too many requests" 这种
//     直接暴露 status code 的串。
//   - 运营 / 开发者依然能通过 admin 后台「请求日志」看到原始 reason
//     （failTask 内会把 raw reason 打 Info 日志）。
func userFacingGenerationError(reason string) string {
	return userFacingGenerationErrorImpl(reason)
}

// UserFacingGenerationError is the exported alias for handlers outside this package.
func UserFacingGenerationError(reason string) string {
	return userFacingGenerationErrorImpl(reason)
}

func userFacingGenerationErrorImpl(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "生成失败，请稍后重试"
	}
	msg := strings.ToLower(reason)

	switch {
	case strings.Contains(msg, "http 451"),
		strings.Contains(msg, "temporary error 451"),
		strings.Contains(msg, "区域限制"),
		strings.Contains(msg, "region restricted"),
		strings.Contains(msg, "region blocked"),
		strings.Contains(msg, "your keyword is unsafe"):
		return "Your keyword is unsafe. Please update or change your keyword."

	// === 账号 / 池层 ===
	// 文案不再提"账号"——避免让用户感知存在账号池转发。
	case strings.Contains(msg, "no available account"),
		strings.Contains(msg, "暂无可用账号"),
		strings.Contains(msg, "no_available_account"):
		return "服务暂时繁忙，请稍后再试"
	case strings.Contains(msg, "provider not registered"):
		return "暂不支持该模型，请联系客服"

	// === 频控 / Cloudflare / 风控 ===
	case strings.Contains(msg, "just a moment"), strings.Contains(msg, "cloudflare"):
		return "本次请求被验证拦截，请稍后再试"
	case strings.Contains(msg, "429"),
		strings.Contains(msg, "too many requests"),
		strings.Contains(msg, "rate limit"),
		strings.Contains(msg, "rate_limit"),
		strings.Contains(msg, "频率"):
		return "请求频率过高，请稍后重试"
	case strings.Contains(msg, "anti-bot"),
		strings.Contains(msg, "request rejected"),
		strings.Contains(msg, "blocked"),
		strings.Contains(msg, "captcha"):
		return "本次请求被风控拦截，请稍后再试"

	// === 内容安全 ===
	case strings.Contains(msg, "content_policy"),
		strings.Contains(msg, "content policy"),
		strings.Contains(msg, "moderation"),
		strings.Contains(msg, "safety"),
		strings.Contains(msg, "unsafe"),
		strings.Contains(msg, "nsfw"),
		strings.Contains(msg, "prompt violates"):
		return "提示词或参考图触发了内容安全策略，请调整后重试"

	// === GPT image2 空输出（上游 completed 但没产出图片，多为 prompt 问题） ===
	case strings.Contains(msg, "returned 0 image"):
		return "提示词可能无法生成有效图片，请更换提示词后重试"

	// === 超时 ===
	case strings.Contains(msg, "context deadline"),
		strings.Contains(msg, "deadline exceeded"),
		strings.Contains(msg, "timeout"),
		strings.Contains(msg, "timed out"),
		strings.Contains(msg, "超时"):
		return "生成超时，请稍后重试"

	// === 权益未开通：账号没买这个档位（典型：Adobe 4K 出图） ===
	// 必须排在 401/403 通用分支前面，否则会被吞掉。
	// 注意：文案不再提"上游"——避免让用户感知存在转发链路。
	case strings.Contains(msg, "user_not_entitled"),
		strings.Contains(msg, "not entitled"),
		strings.Contains(msg, "not_entitled"),
		strings.Contains(msg, "未开通该能力"),
		strings.Contains(msg, "4k 出图权益"):
		return "当前档位（如 4K）暂未开通，请改用 1K / 2K 或联系运营"

	// === 鉴权 / 权限（已自动切号，对用户视角等价于"稍后再试"） ===
	case strings.Contains(msg, "401"),
		strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "invalid_token"),
		strings.Contains(msg, "token expired"):
		return "服务暂时不可用，请稍后重试"
	case strings.Contains(msg, "403"),
		strings.Contains(msg, "forbidden"),
		strings.Contains(msg, "no permission"):
		return "当前选项暂不可用，请稍后重试或更换模型 / 尺寸"

	// === 账单 / 额度（多见于 OpenAI billing 路径） ===
	case strings.Contains(msg, "insufficient"),
		strings.Contains(msg, "billing"),
		strings.Contains(msg, "quota"),
		strings.Contains(msg, "余额"):
		return "账户额度不足，请稍后重试"

	// === 网络 / 5xx ===
	case strings.Contains(msg, "no such host"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "dial tcp"),
		strings.Contains(msg, "network is unreachable"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "eof"):
		return "网络异常，请稍后重试"
	case strings.Contains(msg, "500"),
		strings.Contains(msg, "502"),
		strings.Contains(msg, "503"),
		strings.Contains(msg, "504"),
		strings.Contains(msg, "bad gateway"),
		strings.Contains(msg, "gateway timeout"),
		strings.Contains(msg, "service unavailable"),
		strings.Contains(msg, "internal server error"):
		return "服务暂时不可用，请稍后重试"

	// === 参考图无法访问 ===
	case strings.Contains(msg, "pic2api"),
		strings.Contains(msg, "ossdown.com"),
		strings.Contains(msg, "cf-error-code"),
		strings.Contains(msg, "fetch image"),
		strings.Contains(msg, "download image"),
		strings.Contains(msg, "reference image"):
		return "无法读取参考图，请检查图片链接是否公开可访问或重新上传"

	// === 业务异常（参数错误） ===
	case strings.Contains(msg, "invalid_request_error"),
		strings.Contains(msg, "invalid parameter"),
		strings.Contains(msg, "bad request"),
		strings.Contains(msg, "400 "),
		strings.Contains(msg, "422"):
		return "请求参数有误，请检查模型、尺寸或时长后重试"
	}

	// 默认：完全脱敏，不回带 upstream 信息。
	return "生成失败，请稍后重试"
}
