package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/regkit/grokrefresh"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/logger"
)

// GrokRefreshOptions 单次刷新调用的覆盖项。
type GrokRefreshOptions struct {
	ProxyURL     string
	Caller       string // "manual" / "scheduler"
	FailureLimit int    // 默认 5
}

// RefreshOne 用账号 ID 触发一次探测：调 grok.com /rest/rate-limits，回填
// account_type / credits / quota_total / failure_count / trial_status / last_checked_at。
//
// 行为对齐 Python 参考实现 (token/manager.py#sync_usage_windows + record_fail)：
//
//   - 200 OK + 有 quota → trial_status=active, failure_count=0
//   - 200 OK + quota=0 → trial_status=expired（已过期但 token 仍能查）
//   - 401 → failure_count++，达 FailureLimit 时 trial_status=expired
//   - 403 → trial_status=failed（直接禁用）
//   - 其他错误 → 临时失败，仅记 last_checked_at + error_message，不计 fail
func (s *PoolGrokService) RefreshOne(ctx context.Context, id uint64, opt GrokRefreshOptions) (*model.PoolGrok, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errors.New("账号不存在")
		}
		return nil, fmt.Errorf("获取账号失败：%w", err)
	}
	if row.DeletedAt != nil {
		return nil, errors.New("账号已删除")
	}

	var sso string
	if len(row.SSOEnc) > 0 && s.aes != nil {
		if b, e := s.aes.Decrypt(row.SSOEnc); e == nil {
			sso = string(b)
		}
	}
	if strings.TrimSpace(sso) == "" {
		return nil, errors.New("账号缺少 sso，无法探测")
	}

	now := time.Now().UTC()
	prof, perr := grokrefresh.Probe(ctx, sso, grokrefresh.Options{
		ProxyURL: opt.ProxyURL,
	})

	// 不论成功失败，都更新 last_checked_at
	updates := map[string]any{
		"last_checked_at": now,
	}

	if perr != nil {
		switch {
		case errors.Is(perr, grokrefresh.ErrTokenExpired):
			return s.markGrokRefreshFailure(ctx, row, "401 sso 已失效", opt.FailureLimit, false)
		case errors.Is(perr, grokrefresh.ErrTokenForbidden):
			// 直接置 failed，不再重试
			updates["trial_status"] = model.GrokTrialFailed
			updates["trial_error"] = "403 风控 / 账号被禁"
			updates["failure_count"] = row.FailureCount + 1
			_ = s.repo.Update(ctx, id, updates)
			return row, errors.New("403 风控 / 账号被禁")
		case errors.Is(perr, grokrefresh.ErrEmptyToken):
			updates["trial_status"] = model.GrokTrialFailed
			updates["trial_error"] = "sso 为空"
			_ = s.repo.Update(ctx, id, updates)
			return row, errors.New("sso 为空")
		default:
			// 临时失败 — 不改 trial_status，只记 error
			msg := sanitizeForDB(perr.Error())
			if len(msg) > 480 {
				msg = msg[:480] + "…"
			}
			updates["trial_error"] = msg
			_ = s.repo.Update(ctx, id, updates)
			return row, errors.New(msg)
		}
	}

	// rate-limits 探测成功
	updates["account_type"] = prof.AccountType
	updates["credits"] = prof.RemainingQueries
	updates["quota_total"] = prof.TotalQueries
	updates["failure_count"] = 0
	updates["trial_error"] = ""

	// 顺手拉一次 /rest/subscriptions（grok 个人中心用的真订阅 endpoint）— 拿到的话覆盖
	// account_type + 写入真订阅到期 expires_at。
	// 失败不视为整次探测失败 — 401/403 会被 rate-limits 那一步同样命中，到这里多半是
	// 临时网络问题，我们只在 trial_error 里加个轻提示。
	if subProf, subErr := grokrefresh.ProbeSubscription(ctx, sso, grokrefresh.Options{
		ProxyURL: opt.ProxyURL,
	}); subErr == nil && subProf != nil {
		// account_type 决策矩阵 v3：quota 反推为主，subscription 用于细化。
		//
		// 历史教训：
		//   v1：纯 quota 反推 → 不能区分 SuperGrok 和 Lite（quota 接近）。
		//   v2：subscription tier 优先 → 试用过期降为 Free（quota=30）的账号
		//        仍被错标成 SuperGrok（因为 subscription endpoint 还残留旧
		//        SuperGrok 试用订阅），并继续显示"下次续费"。
		//   v3：以 quota 反推为权威，再用 subscription tier 细化档位。
		//
		// 关键洞察：rate-limits.totalQueries 是 grok 当前**实际发放**给账号的
		// 额度上限，它最能反映账号当下可用 tier；subscription endpoint 有同步
		// 延迟和升级 / 降级残留，不可单独信任。
		//
		//   quota 反推    | sub tier        | 最终       | 说明
		//   --------------+-----------------+-----------+-----
		//   Heavy (>=150) | any             | Heavy     | 400 quota 必是 Heavy
		//   SuperGrok(50+)| lite            | Lite      | sub 细化（Lite quota 与 SuperGrok 接近）
		//   SuperGrok(50+)| 其它            | SuperGrok | 默认普通 SuperGrok
		//   Free (20~49)  | any             | Free      | 试用过期/降级，无视 sub 残留
		//   Unknown (<20) | 非空 tier        | sub tier  | 没 quota 信号，sub 兜底
		//   Unknown       | 空              | Unknown   | 都拉不到
		quotaTier := prof.AccountType
		subTier := strings.TrimSpace(subProf.Tier)
		switch quotaTier {
		case grokrefresh.AccountTypeSuperGrokHeavy:
			updates["account_type"] = quotaTier
		case grokrefresh.AccountTypeSuperGrok:
			if subTier == grokrefresh.AccountTypeSuperGrokLite {
				updates["account_type"] = subTier
			} else {
				updates["account_type"] = quotaTier
			}
		case grokrefresh.AccountTypeFree:
			updates["account_type"] = quotaTier
		default:
			// quota 反推 unknown（quota=0 / 拉不到）→ sub 兜底
			if subTier != "" && subTier != grokrefresh.AccountTypeUnknown {
				updates["account_type"] = subTier
			}
		}
		// expires_at 写入策略：
		//
		//   情况 A：active + ExpiresAt 在未来 → 直接用
		//
		//   情况 B：status != active 但 ExpiresAt 已经过期且账号还活着（quota>0
		//          且 cancel_at_period_end=false）→ **stale 数据**。
		//          grok 的 /rest/subscriptions 在用户续费后经常滞后几小时甚至几天
		//          才把 status 刷成 active、把 billingPeriodEnd 推到下一周期。
		//          这期间 stripe 那边已经收钱、grok rate-limits 已经发新 quota，
		//          只有 subscriptions endpoint 还在用上一周期的快照。
		//
		//          做法：用 billing_interval（monthly/yearly）把 ExpiresAt 一步步
		//          外推到 > now 的下一个周期 end，让 UI 显示一个合理的预期续费日，
		//          而不是显示一个已经过期 N 天的时间或者 "—"。
		//          注意：cancel_at_period_end=true 时不外推（用户主动取消，真过期）。
		//
		//   情况 C：ExpiresAt 已过期 + cancel_at_period_end=true → 不写（NULL）
		//   情况 D：ExpiresAt 为空 → 保留 DB 已有值不动
		// 把"原样透传"的字段先无条件写入；下面的试用 / stale 分支可能再覆盖
		// subscription_status / expires_at 等。
		updates["cancel_at_period_end"] = subProf.CancelAtPeriodEnd
		updates["billing_interval"] = subProf.BillingInterval
		updates["subscription_status"] = subProf.Status
		updates["product_id"] = subProf.ProductID

		// 是否为"短周期试用"：billingPeriodEnd - createTime ≪ interval period。
		// 例：3 天试用 monthly 订阅 → periodEnd - createTime = 3d ≪ 30d 月周期。
		// 试用号 *不能* 用 createTime + interval 外推（会把 3 天试用错误标
		// 成 1 个月到期），而应该把 billingPeriodEnd 当作 trial_end 直接显示。
		isTrial := subProf.CreateTime != nil && subProf.ExpiresAt != nil &&
			grokrefresh.IsTrialPeriod(*subProf.CreateTime, *subProf.ExpiresAt, subProf.BillingInterval)

		switch {
		case isTrial:
			// === 试用号路径 ===
			// billingPeriodEnd 就是 trial_end，原样写入，UI 渲染成"试用到期"。
			updates["expires_at"] = *subProf.ExpiresAt
			// 强制 status 标记：grok 返回 inactive 但实际是"试用已结束"，
			// 显示 trial_ended 比 inactive 更准确。
			if subProf.ExpiresAt.Before(now) && subProf.Status != grokrefresh.SubStatusTrialing {
				updates["subscription_status"] = "trial_ended"
			} else if subProf.Status == "" || subProf.Status == grokrefresh.SubStatusInactive {
				// 试用且还在期内但 grok 没返回 trialing → 用 trialing 兜底
				if subProf.ExpiresAt.After(now) {
					updates["subscription_status"] = grokrefresh.SubStatusTrialing
				}
			}
			logger.FromCtx(ctx).Info("grok.subscription.trial_detected",
				zap.Uint64("id", row.ID),
				zap.Time("create_time", *subProf.CreateTime),
				zap.Time("trial_end", *subProf.ExpiresAt),
				zap.Duration("trial_length", subProf.ExpiresAt.Sub(*subProf.CreateTime)),
				zap.Bool("expired", subProf.ExpiresAt.Before(now)))

		default:
			// === 付费订阅路径 ===
			// grok 的 /rest/subscriptions 经常返回"上一周期快照"：
			//   - status=INACTIVE + billingPeriodEnd 已过期（grok 没同步续费状态）
			//   - status=ACTIVE  + billingPeriodEnd 锚在初始周期（没推到当前周期）
			// 用 createTime + N × interval 推算"应有的"当前周期 end，与 grok 给的
			// billingPeriodEnd 取较晚那个。
			expired := subProf.ExpiresAt != nil && !subProf.ExpiresAt.After(now)
			canExtrapolate := !subProf.CancelAtPeriodEnd &&
				prof.RemainingQueries > 0 &&
				subProf.BillingInterval != ""
			var picked *time.Time
			if subProf.ExpiresAt != nil {
				picked = subProf.ExpiresAt
			}
			if canExtrapolate && subProf.CreateTime != nil {
				fromCreate := projectFromCreate(*subProf.CreateTime, subProf.BillingInterval, now)
				if picked == nil || fromCreate.After(*picked) {
					picked = &fromCreate
					logger.FromCtx(ctx).Info("grok.subscription.projected_from_create",
						zap.Uint64("id", row.ID),
						zap.Time("create_time", *subProf.CreateTime),
						zap.String("billing_interval", subProf.BillingInterval),
						zap.Time("projected", fromCreate))
				}
			} else if canExtrapolate && expired {
				projected := projectNextPeriodEnd(*subProf.ExpiresAt, subProf.BillingInterval, now)
				if projected.After(now) {
					picked = &projected
					logger.FromCtx(ctx).Info("grok.subscription.stale_projected",
						zap.Uint64("id", row.ID),
						zap.Time("raw_expires_at", *subProf.ExpiresAt),
						zap.String("billing_interval", subProf.BillingInterval),
						zap.Time("projected", projected))
				}
			}
			switch {
			case picked != nil && picked.After(now):
				updates["expires_at"] = *picked
			case subProf.ExpiresAt == nil:
				// no-op，保留 DB 已有值
			default:
				updates["expires_at"] = nil
			}
		}
		// status 不是必须字段（schema 还在观察中），暂不入库；若需要再加列。
		expiresAt := ""
		if subProf.ExpiresAt != nil {
			expiresAt = subProf.ExpiresAt.Format(time.RFC3339)
		}
		level := "debug"
		// 若关键字段没解出来 → 把 raw 字段名打出来，便于运营回头看 schema 接对。
		if subProf.Tier == "" || subProf.Tier == grokrefresh.AccountTypeUnknown || subProf.ExpiresAt == nil {
			level = "info"
		}
		// 若 expires_at 已经在过去（但账号还活着）→ 说明 pickTime 选到了
		// 错字段（很可能选成了"上一周期 end"或"账号注册时间"），把整个 raw
		// 序列化打 warn，便于回头看真正的"当前周期 end"字段名。
		expiresPast := subProf.ExpiresAt != nil && subProf.ExpiresAt.Before(now)
		rawKeys := make([]string, 0, len(subProf.Raw))
		for k := range subProf.Raw {
			rawKeys = append(rawKeys, k)
		}
		logFn := logger.FromCtx(ctx).Debug
		switch {
		case expiresPast:
			rawJSON, _ := json.Marshal(subProf.Raw)
			logger.FromCtx(ctx).Warn("grok.subscription.expires_in_past",
				zap.Uint64("id", row.ID),
				zap.String("tier", subProf.Tier),
				zap.String("status", subProf.Status),
				zap.String("expires_at", expiresAt),
				zap.Bool("cancel_at_period_end", subProf.CancelAtPeriodEnd),
				zap.String("raw_json", string(rawJSON)))
		case level == "info":
			logFn = logger.FromCtx(ctx).Info
			fallthrough
		default:
			logFn("grok.subscription.probe",
				zap.Uint64("id", row.ID),
				zap.String("tier", subProf.Tier),
				zap.String("status", subProf.Status),
				zap.String("expires_at", expiresAt),
				zap.Bool("cancel_at_period_end", subProf.CancelAtPeriodEnd),
				zap.Strings("raw_keys", rawKeys))
		}
	} else if subErr != nil {
		// 只记日志 — 第一次部署时这里很可能会失败，因为我们不知道字段名，借此把 raw
		// 体打出来。后续看到 raw 就能知道真实 schema。
		logger.FromCtx(ctx).Warn("grok.subscription.probe_failed",
			zap.Uint64("id", row.ID), zap.Error(subErr))
	}

	// 后处理：Free 账号不该有"下次续费"显示。
	//
	// 场景：用户开 SuperGrok 试用 → 试用过期没付费 → quota 降回 Free 标准（30），
	// 但 grok 的 /rest/subscriptions 还残留旧 SuperGrok trial 订阅（status=
	// INACTIVE）。前面 sub 处理可能把 trial_end / projected expires_at 写进
	// updates 了，但 quota 反推已经判 Free，UI 上"Free + 下次续费 6/15"
	// 显然不合理（试用都过期了，哪还有续费）。
	//
	// 这里统一兜底：account_type 最终落在 Free 时，强制清空 expires_at + cancel_at_period_end，
	// status 标 trial_ended（如果已经是 trial_ended 就保留）。
	if finalTier, _ := updates["account_type"].(string); finalTier == grokrefresh.AccountTypeFree {
		updates["expires_at"] = nil
		updates["cancel_at_period_end"] = false
		if curStatus, _ := updates["subscription_status"].(string); curStatus != "trial_ended" {
			updates["subscription_status"] = "trial_ended"
		}
	}

	// rate-limits 接口里 windowSizeSeconds 表示"当前 quota 窗口剩余秒数"，
	// 也就是 *下一次额度刷新* 的时刻（super_grok 一般 2h，free 24h）。
	//
	// ⚠️ 注意：这 *不是* grok 订阅到期时间。grok 官网没有把订阅到期通过
	//        /rest/rate-limits 暴露出来，目前仍未找到稳定可用的公开 endpoint
	//        来读取订阅到期（需要从用户的 grok.com 个人中心 HAR 抓取后挂上候选）。
	//
	// 历史代码（包括 admin UI）有用"trial_expires_at"字段来承载它 — 这里继续
	// 写入这个字段，但语义已经在前端列名/tooltip 上更正为"额度刷新于"。
	// 真订阅到期字段 pool_grok.expires_at 目前保持 NULL（待接入）。
	//
	// 合理性兜底：取 (1m, 30 天] 之间的值才采纳，避免脏数据写库。
	var nextExpiresAt *time.Time
	if prof.WindowSeconds >= 60 && prof.WindowSeconds <= 30*24*3600 {
		t := now.Add(time.Duration(prof.WindowSeconds) * time.Second)
		updates["trial_expires_at"] = t
		nextExpiresAt = &t
	}

	if prof.RemainingQueries > 0 {
		updates["trial_status"] = model.GrokTrialActive
	} else if row.TrialStatus == model.GrokTrialPending {
		// 0 余额 + 之前 pending → 视作 active 但没额度（用户可继续等下个窗口）
		updates["trial_status"] = model.GrokTrialActive
	}

	if err := s.repo.Update(ctx, id, updates); err != nil {
		return nil, fmt.Errorf("写库失败：%w", err)
	}

	row.AccountType = prof.AccountType
	row.Credits = prof.RemainingQueries
	row.QuotaTotal = prof.TotalQueries
	row.FailureCount = 0
	row.LastCheckedAt = &now
	if nextExpiresAt != nil {
		row.TrialExpiresAt = nextExpiresAt
	}
	if v, ok := updates["trial_status"].(string); ok {
		row.TrialStatus = v
	}
	// 同步把 subscription probe 覆盖过的字段也回填到返回 row，避免
	// 同步 API 响应里 account_type 还是 quota 反推的旧值（DB 里其实
	// 已经是 subscription tier）。
	if v, ok := updates["account_type"].(string); ok {
		row.AccountType = v
	}
	if v, ok := updates["expires_at"].(time.Time); ok {
		row.ExpiresAt = &v
	}
	return row, nil
}

// markGrokRefreshFailure 累加 failure_count，达 limit → trial_status=expired。
func (s *PoolGrokService) markGrokRefreshFailure(
	ctx context.Context,
	row *model.PoolGrok,
	reason string,
	limit int,
	_already bool,
) (*model.PoolGrok, error) {
	if limit <= 0 {
		limit = 5
	}
	reason = sanitizeForDB(reason)
	if len(reason) > 480 {
		reason = reason[:480] + "…"
	}
	failCount := row.FailureCount + 1
	updates := map[string]any{
		"failure_count":   failCount,
		"trial_error":     reason,
		"last_checked_at": time.Now().UTC(),
	}
	if failCount >= limit {
		updates["trial_status"] = model.GrokTrialExpired
	}
	_ = s.repo.Update(ctx, row.ID, updates)
	return row, fmt.Errorf("refresh 失败：%s（%d/%d）", reason, failCount, limit)
}

// GrokBatchRefreshSummary 批量扫描结果（同步 API 用）。
//
// Errors 是聚合后的错误样本（去重，按出现次数倒序）。
type GrokBatchRefreshSummary struct {
	Total  int
	OK     int
	Fail   int
	Errors []GrokBatchRefreshErrSample
}

// GrokBatchRefreshErrSample 一条聚合错误样本。
type GrokBatchRefreshErrSample struct {
	Message string
	Count   int
}

// grokBatchJob 一次后台批量刷新任务的内存级状态。
//
// 字段读写规则：
//   - 不可变字段 (ID, Scope, StartedAt, cancel) 启动后只读
//   - 可变字段（Status / Scanned / OK / Fail / EndedAt / ErrCounts / LastError）
//     必须持 mu 才能读/写
type grokBatchJob struct {
	ID        string
	Scope     string
	StartedAt time.Time
	cancel    context.CancelFunc

	mu        sync.RWMutex
	Status    string // running / completed / cancelled / failed
	Scanned   int    // 已完成的账号数（OK + Fail）
	OK        int
	Fail      int
	LastError string
	EndedAt   *time.Time
	// ErrCounts 错误样本聚合（key = 归一化后的错误描述）。
	ErrCounts map[string]int
}

// snapshot 把当前 job 状态拷贝出来，避免 caller 持锁。
func (j *grokBatchJob) snapshot() *GrokBatchRefreshJobSnapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := &GrokBatchRefreshJobSnapshot{
		ID:        j.ID,
		Scope:     j.Scope,
		Status:    j.Status,
		StartedAt: j.StartedAt,
		EndedAt:   j.EndedAt,
		Scanned:   j.Scanned,
		OK:        j.OK,
		Fail:      j.Fail,
		LastError: j.LastError,
	}
	out.Errors = topErrSamples(j.ErrCounts, 5)
	return out
}

// GrokBatchRefreshJobSnapshot 返回给前端轮询的进度快照。
//
// 没有 Total 字段，因为后台是流式分批扫表，"全部账号数"在跑完之前
// 无法精确知道；前端按 Scanned 显示已完成数即可，扫完 status 会切到
// completed / cancelled / failed。
type GrokBatchRefreshJobSnapshot struct {
	ID        string
	Scope     string
	Status    string
	StartedAt time.Time
	EndedAt   *time.Time
	Scanned   int
	OK        int
	Fail      int
	LastError string
	Errors    []GrokBatchRefreshErrSample
}

// 批量刷新关键常量。
//
//   - grokBatchChunkSize     : 一批从 DB 取出来分发给 worker 的账号数。
//     太大 → 单 SELECT 时间长 + 内存占用高；太小 → 翻页次数多、DB 压力分散。
//     200 在万级账号 + 6 并发下分摊到 ~50 批，每批最坏 ~30s 跑完。
//   - grokBatchAccountTimeout: 单个账号 RefreshOne 的硬超时。grokrefresh.Probe
//     内部默认 30-60s，遇到 CF 卡顿 + 重试可能继续叠，所以再加一层 90s
//     hard cap 兜底，避免一个 hung connection 让整个 worker 槽位永远占着。
//   - grokBatchDefaultMaxConc: 默认并发数。考虑 outbound IP 共享 + 代理池容量
//     大概率 6-10 比较合理，太高会被 CF 限流。
const (
	grokBatchChunkSize       = 200
	grokBatchAccountTimeout  = 90 * time.Second
	grokBatchDefaultMaxConc  = 6
	grokBatchMaxConcCeiling  = 32 // 上限：防止前端误传几百
)

// RefreshByScope 按 scope 批量探测（**同步** API，单批 ≤ 500 条；旧版兼容）。
//
// 用于 scheduler / 测试等只关心首批的场景。前端"检测全部账号"这种万级
// 场景请改用 StartBatchRefresh，那个是异步分批 + 可查进度 + 可取消的。
func (s *PoolGrokService) RefreshByScope(
	ctx context.Context,
	scope repo.PoolGrokRefreshScope,
	maxConc int,
	pickProxy func() string,
) GrokBatchRefreshSummary {
	out := GrokBatchRefreshSummary{}
	rows, err := s.repo.ListForRefresh(ctx, scope, 500)
	if err != nil || len(rows) == 0 {
		return out
	}
	out.Total = len(rows)
	if maxConc <= 0 {
		maxConc = grokBatchDefaultMaxConc
	}
	var (
		mu        sync.Mutex
		errCounts = map[string]int{}
		okCount   int
		failCount int
	)
	s.runBatchWorkers(ctx, rows, maxConc, pickProxy, func(_ *model.PoolGrok, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			failCount++
			errCounts[normalizeErrSample(err.Error())]++
		} else {
			okCount++
		}
	})
	out.OK = okCount
	out.Fail = failCount
	out.Errors = topErrSamples(errCounts, 5)
	return out
}

// StartBatchRefresh 启动一个**异步**批量刷新任务。
//
// 与旧 RefreshByScope 的关键区别：
//
//  1. **立即返回** jobID，handler 不再被 HTTP 连接挂着；
//  2. **分批扫表**：用 ListForRefreshChunk(scope, afterID, 200) cursor 翻页，
//     直到扫完全部账号（10000 个 = 50 批），不再受 500 上限限制；
//  3. **per-account 超时**：每个 RefreshOne 包独立 90s context，单个号
//     hang 不会卡死 worker 槽位；
//  4. **可取消**：context.CancelFunc 暴露给 CancelBatchRefresh，能立刻
//     停止扫表 + 让已经在跑的 worker 自然退出；
//  5. **同时只允许一个**：避免万级账号并发任务互相挤占代理 IP 配额。
//
// maxConc <= 0 → 默认 6；上限 32。
func (s *PoolGrokService) StartBatchRefresh(
	scope repo.PoolGrokRefreshScope,
	maxConc int,
	pickProxy func() string,
) (*GrokBatchRefreshJobSnapshot, error) {
	s.batchJobMu.Lock()
	defer s.batchJobMu.Unlock()
	if s.batchJob != nil {
		s.batchJob.mu.RLock()
		running := s.batchJob.Status == "running"
		s.batchJob.mu.RUnlock()
		if running {
			return s.batchJob.snapshot(), errors.New("已有批量刷新任务在跑，请等待完成或取消后再试")
		}
	}
	if maxConc <= 0 {
		maxConc = grokBatchDefaultMaxConc
	}
	if maxConc > grokBatchMaxConcCeiling {
		maxConc = grokBatchMaxConcCeiling
	}
	scopeStr := string(scope)
	if scopeStr == "" {
		scopeStr = string(repo.GrokRefreshScopeAll)
	}

	// 父 context 不挂在 HTTP 请求上 — 用 background，避免 handler return 后被取消。
	jobCtx, cancel := context.WithCancel(context.Background())
	job := &grokBatchJob{
		ID:        newGrokJobID(),
		Scope:     scopeStr,
		StartedAt: time.Now().UTC(),
		cancel:    cancel,
		Status:    "running",
		ErrCounts: map[string]int{},
	}
	s.batchJob = job
	snap := job.snapshot()

	go s.runBatchRefreshJob(jobCtx, job, scope, maxConc, pickProxy)
	return snap, nil
}

// BatchRefreshSnapshot 返回当前/最近一次任务的快照（无任务时返回 nil）。
func (s *PoolGrokService) BatchRefreshSnapshot() *GrokBatchRefreshJobSnapshot {
	s.batchJobMu.RLock()
	defer s.batchJobMu.RUnlock()
	if s.batchJob == nil {
		return nil
	}
	return s.batchJob.snapshot()
}

// CancelBatchRefresh 取消当前正在跑的批量刷新任务。
//
// 返回 true = 成功标记 cancel；false = 没在跑（已完成或从未启动）。
func (s *PoolGrokService) CancelBatchRefresh() bool {
	s.batchJobMu.RLock()
	job := s.batchJob
	s.batchJobMu.RUnlock()
	if job == nil {
		return false
	}
	job.mu.RLock()
	running := job.Status == "running"
	job.mu.RUnlock()
	if !running {
		return false
	}
	if job.cancel != nil {
		job.cancel()
	}
	return true
}

// runBatchRefreshJob 后台 goroutine：cursor 分页扫表 → 并发探测 → 写进度。
func (s *PoolGrokService) runBatchRefreshJob(
	ctx context.Context,
	job *grokBatchJob,
	scope repo.PoolGrokRefreshScope,
	maxConc int,
	pickProxy func() string,
) {
	defer func() {
		if r := recover(); r != nil {
			logger.FromCtx(ctx).Error("grok.batch_refresh.panic",
				zap.String("job_id", job.ID),
				zap.Any("recover", r),
				zap.String("stack", string(debug.Stack())))
			job.mu.Lock()
			job.Status = "failed"
			job.LastError = fmt.Sprintf("panic: %v", r)
			now := time.Now().UTC()
			job.EndedAt = &now
			job.mu.Unlock()
		}
	}()

	var afterID uint64
	for {
		if ctx.Err() != nil {
			break
		}
		rows, err := s.repo.ListForRefreshChunk(ctx, scope, afterID, grokBatchChunkSize)
		if err != nil {
			job.mu.Lock()
			job.LastError = "扫表失败：" + normalizeErrSample(err.Error())
			job.Status = "failed"
			now := time.Now().UTC()
			job.EndedAt = &now
			job.mu.Unlock()
			return
		}
		if len(rows) == 0 {
			break
		}

		s.runBatchWorkers(ctx, rows, maxConc, pickProxy, func(row *model.PoolGrok, err error) {
			job.mu.Lock()
			defer job.mu.Unlock()
			job.Scanned++
			if err != nil {
				job.Fail++
				key := normalizeErrSample(err.Error())
				job.ErrCounts[key]++
				job.LastError = key
			} else {
				job.OK++
			}
		})

		afterID = rows[len(rows)-1].ID
		// 留一个非常小的喘息空隙，给 DB / 代理池缓一缓 — 实测 200 个号
		// 一批跑完大概 20-40s，这里再 sleep 也无所谓。
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}

	job.mu.Lock()
	defer job.mu.Unlock()
	if ctx.Err() != nil && job.Status == "running" {
		job.Status = "cancelled"
	} else if job.Status == "running" {
		job.Status = "completed"
	}
	now := time.Now().UTC()
	job.EndedAt = &now
}

// runBatchWorkers 并发跑一批 RefreshOne。
//
// 每个 worker 包独立 per-account context.WithTimeout，hang 的连接最多
// 卡 grokBatchAccountTimeout 就被强制中断；如果 ctx 已取消，剩余账号
// 不再发起新调用（但已经在跑的会自然走完或被超时切断）。
func (s *PoolGrokService) runBatchWorkers(
	ctx context.Context,
	rows []*model.PoolGrok,
	maxConc int,
	pickProxy func() string,
	onDone func(*model.PoolGrok, error),
) {
	if maxConc <= 0 {
		maxConc = grokBatchDefaultMaxConc
	}
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	for _, r := range rows {
		if ctx.Err() != nil {
			// 取消：剩余账号视为"未跑" — 不报 ok/fail，直接跳过。
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		go func(r *model.PoolGrok) {
			defer wg.Done()
			defer func() {
				<-sem
				if rec := recover(); rec != nil {
					logger.FromCtx(ctx).Error("grok.batch_refresh.worker_panic",
						zap.Uint64("id", r.ID),
						zap.Any("recover", rec),
						zap.String("stack", string(debug.Stack())))
					onDone(r, fmt.Errorf("worker panic: %v", rec))
				}
			}()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			callCtx, cancel := context.WithTimeout(ctx, grokBatchAccountTimeout)
			defer cancel()
			_, e := s.RefreshOne(callCtx, r.ID, GrokRefreshOptions{
				ProxyURL: proxy,
				Caller:   "manual",
			})
			onDone(r, e)
		}(r)
	}
	wg.Wait()
}

// projectFromCreate 从订阅 createTime 出发，按月/年周期向前滚动直到 > now。
//
// 关键差异 vs projectNextPeriodEnd：
//   - projectNextPeriodEnd 从一个**已过期的** billingPeriodEnd 滚（修复 status=INACTIVE
//     的 stale 数据）
//   - projectFromCreate 从**永远不会变的** createTime 滚（修复 status=ACTIVE 但
//     billingPeriodEnd 也是 stale 的场景；grok 经常返回 createTime+几天 的初始
//     周期 end 而不是当前周期 end）
//
// 例：createTime=2026-05-11 09:54Z, monthly, now=2026-05-14
//     → 第 1 轮 +1 月 = 2026-06-11 09:54Z > now → 返回
func projectFromCreate(createTime time.Time, interval string, now time.Time) time.Time {
	t := createTime.UTC()
	// 至多滚 24 个周期：2 年的月订或 24 年的年订，防御性 cap。
	for i := 0; i < 24; i++ {
		if t.After(now) {
			return t
		}
		switch interval {
		case grokrefresh.BillingIntervalMonthly:
			t = t.AddDate(0, 1, 0)
		case grokrefresh.BillingIntervalYearly:
			t = t.AddDate(1, 0, 0)
		default:
			return t // 未知 interval → 不推
		}
	}
	return t
}

// projectNextPeriodEnd 把一个已过期的 billing_period_end，按月/年周期向前
// 滚动直到 > now，得到当前周期的预期 end。
//
// 用于 grok subscription 数据 stale 的兜底：用户已续费、stripe→grok 没同步前，
// 让我们能给出一个合理的"下次到期日"显示，而不是"—"或一个过去时间。
//
// 例：billingPeriodEnd = 2026-05-14T09:54Z, monthly, now = 2026-05-14T15:36Z
//   → 第 1 轮 +1 月 = 2026-06-14T09:54Z > now → 返回。
func projectNextPeriodEnd(base time.Time, interval string, now time.Time) time.Time {
	t := base.UTC()
	// 防呆：最多滚 24 个周期（2 年的月订/24 年的年订），避免极端脏数据死循环。
	for i := 0; i < 24; i++ {
		if t.After(now) {
			return t
		}
		switch interval {
		case grokrefresh.BillingIntervalMonthly:
			t = t.AddDate(0, 1, 0)
		case grokrefresh.BillingIntervalYearly:
			t = t.AddDate(1, 0, 0)
		default:
			return t // 未知 interval → 不外推
		}
	}
	return t
}

// newGrokJobID 8 字节随机 hex，足够区分人工触发场景。
func newGrokJobID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// normalizeErrSample 把单条错误归一化：剥离前缀 "refresh 失败：" 和长尾信息，方便聚合。
func normalizeErrSample(s string) string {
	s = sanitizeForDB(s)
	s = strings.TrimSpace(s)
	for _, pre := range []string{"refresh 失败：", "refresh 失败:"} {
		if strings.HasPrefix(s, pre) {
			s = strings.TrimSpace(s[len(pre):])
			break
		}
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// topErrSamples 取出现次数最多的 N 条错误。
func topErrSamples(m map[string]int, n int) []GrokBatchRefreshErrSample {
	if len(m) == 0 {
		return nil
	}
	out := make([]GrokBatchRefreshErrSample, 0, len(m))
	for k, v := range m {
		out = append(out, GrokBatchRefreshErrSample{Message: k, Count: v})
	}
	// 简单选择排序，N 很小
	for i := 0; i < len(out); i++ {
		mx := i
		for j := i + 1; j < len(out); j++ {
			if out[j].Count > out[mx].Count {
				mx = j
			}
		}
		out[i], out[mx] = out[mx], out[i]
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// sanitizeForDB 把任意字符串转成 utf8mb4 安全 + 可打印的 ASCII/中日韩字符。
// 防止 grok.com 把 gzip/二进制塞进 trial_error 触发 MySQL 1366。
func sanitizeForDB(s string) string {
	if s == "" {
		return ""
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if r == '\uFFFD' {
			continue
		}
		if r == '\n' || r == '\t' {
			sb.WriteRune(' ')
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		// 4 字节及以上 unicode 受 utf8mb4 支持，无须屏蔽
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		return "<binary>"
	}
	return out
}

// Purge 按条件批量软删 GROK 账号。
func (s *PoolGrokService) Purge(ctx context.Context, f repo.PoolGrokPurgeFilter) (int64, error) {
	return s.repo.Purge(ctx, f)
}
