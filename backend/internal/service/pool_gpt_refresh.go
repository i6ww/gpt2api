package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/gptauth"

	"go.uber.org/zap"
)

// GptRefreshOptions 单次刷新调用的可覆盖项；service 层兜底从 system_config 取。
type GptRefreshOptions struct {
	ProxyURL     string
	OnlyQuota    bool   // true = 跳过 silent refresh，仅拿 wham/usage
	Caller       string // "manual" / "scheduler"，写日志用
	FailureLimit int    // 连续失败多少次后置 invalid（0 = 5 次）
	// MinTTL：access_token 剩余 TTL < MinTTL 时强制 refresh；
	// 0 表示不主动 refresh（仅在 OnlyQuota=false 时为完整路径默认 12h）。
	MinTTL time.Duration
	// PostExpiresIn：refresh 成功后用什么覆写 expires_at；
	// 当 oauth response 的 expires_in=0 时，由 caller 决定是否兜底。
}

// 刷新节流配置（仅作用于 caller="manual"）。
//   - manualThrottleTTL：每个 GPT 账号"任意一次刷新"的最小间隔
//   - invalidThrottleTTL：已 invalid 的账号再刷新的最小间隔（更长，避免无谓打 OpenAI）
const (
	manualThrottleTTL  = 60 * time.Second
	invalidThrottleTTL = 30 * time.Minute
)

// throttleKey 构造 redis key。
func gptRefreshThrottleKey(id uint64) string {
	return fmt.Sprintf("klein:gpt:refresh:throttle:%d", id)
}

// throttleManualRefresh 检查 + 占用刷新节流 key。
//
// 行为按 caller 区分：
//   - manual    → 检查节流；命中返回 ProbeThrottled，否则占用 key
//   - batch     → 不检查节流，但占用 key（让单点紧跟批量后会被节流）
//   - scheduler → 不检查、不占用（后台调度器自己有 interval 节奏）
//
// rdb 未注入 / redis 故障 → 不阻塞业务，全部放行。
func (s *PoolGptService) throttleManualRefresh(ctx context.Context, id uint64, caller string, isInvalid bool) error {
	if s.rdb == nil || caller == "scheduler" {
		return nil
	}
	ttl := manualThrottleTTL
	if isInvalid {
		ttl = invalidThrottleTTL
	}
	key := gptRefreshThrottleKey(id)
	if caller == "batch" {
		// 不检查、只占用：批量刷新一律放行，但写入 key 阻挡随后的"手动单点"。
		_ = s.rdb.Set(ctx, key, time.Now().Unix(), ttl).Err()
		return nil
	}
	// caller == "manual" / "" 走完整节流。
	ok, err := s.rdb.SetNX(ctx, key, time.Now().Unix(), ttl).Result()
	if err != nil {
		// redis 故障时不阻塞业务
		return nil
	}
	if !ok {
		left, _ := s.rdb.TTL(ctx, key).Result()
		secs := int(left.Seconds())
		if secs <= 0 {
			secs = int(ttl.Seconds())
		}
		hint := fmt.Sprintf("账号刚刷过，请 %d 秒后再试", secs)
		if isInvalid {
			hint = fmt.Sprintf("账号已失效，冷却中（剩 %d 秒）；请稍后再试或直接删除", secs)
		}
		return errcode.ProbeThrottled.WithMsg(hint)
	}
	return nil
}

// RefreshOne 用账号 ID 触发一次刷新。线程安全。
//
// 步骤（与 ref AlexANSO/gpt-codex-pool token-validator.ts 一致）：
//
//   1. 读 row + 解 access_token / refresh_token
//   2. 不 OnlyQuota 且有 RT 且（强制 / token < MinTTL）→ POST /oauth/token 拿新 AT/RT/expires_in
//   3. 调 chatgpt.com/backend-api/wham/usage → 拿 plan_type / 短长窗口百分比 / reset_after
//   4. JWT decode 新 AT → 兜底 expires_at
//   5. 写库：access_token_enc / refresh_token_enc / id_token_enc / expires_at /
//      plan_type / chatgpt_account_id / quota_* / last_refresh_at / last_quota_check_at
//
// 返回更新后的 row（已带最新字段，未持久化失败时返回错误）。
func (s *PoolGptService) RefreshOne(ctx context.Context, id uint64, opt GptRefreshOptions) (*model.PoolGpt, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errcode.ResourceMissing.WithMsg("账号不存在")
		}
		return nil, errcode.DBError.Wrap(err)
	}
	if row.DeletedAt != nil {
		return nil, errcode.ResourceMissing.WithMsg("账号已删除")
	}

	// 节流：避免反复点扫描续期把 OpenAI 上游打爆 / 加速封号。
	if err := s.throttleManualRefresh(ctx, id, opt.Caller, row.Status == model.GPTStatusInvalid); err != nil {
		return nil, err
	}

	// 解 AT/RT
	var accessToken, refreshToken string
	if len(row.AccessTokenEnc) > 0 && s.aes != nil {
		if b, err := s.aes.Decrypt(row.AccessTokenEnc); err == nil {
			accessToken = string(b)
		}
	}
	if len(row.RefreshTokenEnc) > 0 && s.aes != nil {
		if b, err := s.aes.Decrypt(row.RefreshTokenEnc); err == nil {
			refreshToken = string(b)
		}
	}
	if accessToken == "" && refreshToken == "" {
		return nil, errcode.AccountMissingCred
	}

	// 决定 OAuth client_id：优先用 row 上记录的，缺省 platform。
	clientID := gptauth.PlatformClientID
	if row.OAuthClientID != nil && strings.TrimSpace(*row.OAuthClientID) != "" {
		clientID = strings.TrimSpace(*row.OAuthClientID)
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"last_checked_at": now,
	}

	// === 1) 视情况换 RT/AT ===
	needRefresh := false
	switch {
	case opt.OnlyQuota:
		needRefresh = false
	case refreshToken == "":
		needRefresh = false // 没 RT 没法换
	case opt.MinTTL > 0:
		if row.ExpiresAt == nil || row.ExpiresAt.Before(now.Add(opt.MinTTL)) {
			needRefresh = true
		}
	default:
		needRefresh = true // 完整路径强制换
	}

	if needRefresh {
		ts, err := gptauth.RefreshAccessToken(ctx, refreshToken, clientID, opt.ProxyURL, 30*time.Second)
		if err != nil {
			// 401 / invalid_grant 直接置 invalid；其它走 cooldown 累计。
			msg := err.Error()
			if isFatalRefreshError(msg) {
				return s.markGptFatal(ctx, row, msg)
			}
			return s.markGptRefreshFailure(ctx, row, msg, opt.FailureLimit)
		}
		// 加密落盘
		if enc, err := s.aes.Encrypt([]byte(ts.AccessToken)); err == nil {
			updates["access_token_enc"] = enc
			accessToken = ts.AccessToken
		}
		if ts.RefreshToken != "" {
			if enc, err := s.aes.Encrypt([]byte(ts.RefreshToken)); err == nil {
				updates["refresh_token_enc"] = enc
				refreshToken = ts.RefreshToken
			}
		}
		if ts.IDToken != "" {
			if enc, err := s.aes.Encrypt([]byte(ts.IDToken)); err == nil {
				updates["id_token_enc"] = enc
			}
		}
		if !ts.ExpiresAt.IsZero() {
			t := ts.ExpiresAt
			updates["expires_at"] = t
			row.ExpiresAt = &t
		}
		updates["last_refresh_at"] = now
	} else if accessToken != "" {
		// 没换 token，但还是用 JWT exp 兜底刷新一下 expires_at（万一之前没记好）。
		if c, err := gptauth.DecodeAccessToken(accessToken); err == nil && c.Exp > 0 {
			t := c.ExpiresAt()
			if row.ExpiresAt == nil || !row.ExpiresAt.Equal(t) {
				updates["expires_at"] = t
				row.ExpiresAt = &t
			}
		}
	}

	// === 2) 拿 plan_type + quota ===
	if accessToken != "" {
		usage, err := gptauth.FetchUsage(ctx, accessToken, opt.ProxyURL, 25*time.Second)
		if err != nil {
			// 401 = AT 失效（即使刚 refresh）
			if strings.Contains(err.Error(), "HTTP 401") {
				return s.markGptFatal(ctx, row, "wham/usage 401: token 失效")
			}
			// 其它是软错（限流 / 临时 5xx），不阻断 refresh 成功。
			updates["error_message"] = truncateErr(err.Error())
		} else {
			if usage.PlanType != "" {
				p := usage.PlanType
				updates["plan_type"] = p
				row.PlanType = &p
			}
			if usage.AccountID != "" {
				a := usage.AccountID
				updates["chatgpt_account_id"] = a
				row.ChatGPTAccountID = &a
			}
			updates["quota_primary_used_percent"] = usage.PrimaryUsedPct
			updates["quota_secondary_used_percent"] = usage.SecondaryUsedPct
			updates["quota_code_review_used_percent"] = usage.CodeReviewUsedPct
			if usage.PrimaryResetSec > 0 {
				t := now.Add(time.Duration(usage.PrimaryResetSec) * time.Second)
				updates["quota_primary_reset_at"] = t
			}
			if usage.SecondaryResetSec > 0 {
				t := now.Add(time.Duration(usage.SecondaryResetSec) * time.Second)
				updates["quota_secondary_reset_at"] = t
			}
			updates["last_quota_check_at"] = now
		}
	}

	// 任一刷新成功后，把状态置 valid，failure_count 归零
	updates["status"] = model.GPTStatusValid
	updates["failure_count"] = 0
	if _, ok := updates["error_message"]; !ok {
		// 只要没设软错，就清掉旧 error
		updates["error_message"] = ""
	}

	if err := s.repo.Update(ctx, id, updates); err != nil {
		return nil, fmt.Errorf("写库失败：%w", err)
	}
	// 重读一遍拿到最终 row（updates 可能没有覆盖所有字段）。
	final, _ := s.repo.GetByID(ctx, id)
	if final != nil {
		return final, nil
	}
	return row, nil
}

// markGptRefreshFailure 失败时累加 failure_count，达到 limit 后置 invalid。
//
// 默认 limit=5；< limit 时进 cooldown（10min 退避），>= limit 时直接 invalid。
//
// 返回的 error 是 *errcode.Error：
//   - 已 invalid（达 limit）→ AccountInvalid，HTTP 410
//   - 仍 cooldown            → GPTUnavailable，HTTP 502，含原始失败 msg
func (s *PoolGptService) markGptRefreshFailure(ctx context.Context, row *model.PoolGpt, msg string, limit int) (*model.PoolGpt, error) {
	if limit <= 0 {
		limit = 5
	}
	failCount := row.FailureCount + 1
	now := time.Now().UTC()
	updates := map[string]any{
		"failure_count":   failCount,
		"error_message":   truncateErr(msg),
		"last_checked_at": now,
	}
	if failCount >= limit {
		updates["status"] = model.GPTStatusInvalid
	} else {
		updates["status"] = model.GPTStatusCooldown
	}
	_ = s.repo.Update(ctx, row.ID, updates)
	if failCount >= limit {
		return row, errcode.AccountInvalid.WithMsg(fmt.Sprintf("账号连续 %d 次刷新失败已自动失效：%s", failCount, truncateErr(msg)))
	}
	return row, errcode.GPTUnavailable.WithMsg(fmt.Sprintf("刷新失败（第 %d/%d 次，进入 cooldown）：%s", failCount, limit, truncateErr(msg)))
}

// markGptFatal 401 / invalid_grant 等"号已废"信号：直接 invalid，不进 cooldown。
//
// 返回 *errcode.AccountInvalid（HTTP 410，msg 含具体原因），handler 透传即可。
func (s *PoolGptService) markGptFatal(ctx context.Context, row *model.PoolGpt, msg string) (*model.PoolGpt, error) {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":          model.GPTStatusInvalid,
		"error_message":   truncateErr(msg),
		"last_checked_at": now,
		"failure_count":   row.FailureCount + 1,
	}
	_ = s.repo.Update(ctx, row.ID, updates)
	return row, errcode.AccountInvalid.WithMsg("账号已失效：" + truncateErr(msg))
}

// isFatalRefreshError 判定 refresh API 错误是否"号已废"——直接 invalid 不再重试。
//
// 命中条件：
//   - HTTP 400/401 + invalid_grant / invalid_token / unauthorized_client
func isFatalRefreshError(msg string) bool {
	low := strings.ToLower(msg)
	if !strings.Contains(low, "http 400") && !strings.Contains(low, "http 401") {
		return false
	}
	for _, k := range []string{"invalid_grant", "invalid_token", "unauthorized_client", "invalid_client"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

func truncateErr(msg string) string {
	if len(msg) > 480 {
		return msg[:480] + "…"
	}
	return msg
}

// RefreshExpiring 后台扫描入口：把 within 内即将过期的账号都刷一遍（并发 max 个）。
func (s *PoolGptService) RefreshExpiring(ctx context.Context, within time.Duration, maxConc int, pickProxy func() string) (ok, fail int) {
	rows, err := s.repo.ListExpiringSoon(ctx, within, 200)
	if err != nil || len(rows) == 0 {
		return
	}
	if maxConc <= 0 {
		maxConc = 3
	}
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, r := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(r *model.PoolGpt) {
			defer wg.Done()
			defer func() { <-sem }()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			_, err := s.RefreshOne(ctx, r.ID, GptRefreshOptions{
				ProxyURL: proxy,
				Caller:   "scheduler",
				MinTTL:   within, // 只换 ttl < within 的
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fail++
			} else {
				ok++
			}
		}(r)
	}
	wg.Wait()
	return
}

// RefreshStaleQuota 后台扫描入口：拿现有 access_token 刷新 quota（不换 token，便宜得多）。
func (s *PoolGptService) RefreshStaleQuota(ctx context.Context, staleAfter time.Duration, maxConc int, pickProxy func() string) (ok, fail int) {
	rows, err := s.repo.ListStaleQuota(ctx, staleAfter, 200)
	if err != nil || len(rows) == 0 {
		return
	}
	if maxConc <= 0 {
		maxConc = 3
	}
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, r := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(r *model.PoolGpt) {
			defer wg.Done()
			defer func() { <-sem }()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			_, err := s.RefreshOne(ctx, r.ID, GptRefreshOptions{
				ProxyURL:  proxy,
				OnlyQuota: true,
				Caller:    "scheduler",
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fail++
			} else {
				ok++
			}
		}(r)
	}
	wg.Wait()
	return
}

// RefreshByScope 后台手动入口：按 scope 批量刷新。
//
// 用例（与 Adobe 对齐）：
//
//   - "刷新过期账号 token"  → scope=expiring,    OnlyQuota=false
//   - "刷新异常账号 token"  → scope=abnormal,    OnlyQuota=false
//   - "刷新全部账号 token"  → scope=all,         OnlyQuota=false
//   - "刷新全部账号配额"    → scope=all,         OnlyQuota=true
//   - "刷新过期配额信息"    → scope=quota_stale, OnlyQuota=true
func (s *PoolGptService) RefreshByScope(
	ctx context.Context,
	scope repo.PoolGptRefreshScope,
	onlyQuota bool,
	maxConc int,
	pickProxy func() string,
) (ok, fail, total int) {
	rows, err := s.repo.ListForRefresh(ctx, scope, 500)
	if err != nil || len(rows) == 0 {
		return
	}
	total = len(rows)
	if maxConc <= 0 {
		maxConc = 3
	}
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, r := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(r *model.PoolGpt) {
			defer wg.Done()
			defer func() { <-sem }()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			_, e := s.RefreshOne(ctx, r.ID, GptRefreshOptions{
				ProxyURL:  proxy,
				OnlyQuota: onlyQuota,
				Caller:    "batch", // 批量刷新不节流，但单点节流仍然生效
			})
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				fail++
			} else {
				ok++
			}
		}(r)
	}
	wg.Wait()
	return
}

// PurgeBy 按 scope 物理删除（软删）。
//
// scope 取值：
//   - all            → 全清
//   - invalid        → status=invalid
//   - token_expired  → expires_at <= now
//   - quota_exceeded → primary_used_percent >= 100
//   - no_refresh     → 没 refresh_token 的号
func (s *PoolGptService) PurgeBy(ctx context.Context, scope string) (int64, error) {
	f := repo.PoolGptPurgeFilter{}
	switch scope {
	case "all":
		f.All = true
	case "invalid":
		f.Status = model.GPTStatusInvalid
	case "token_expired":
		f.TokenExpired = true
	case "quota_exceeded":
		f.QuotaExceeded = true
	case "no_refresh":
		f.NoRefresh = true
	default:
		return 0, fmt.Errorf("unknown scope: %s", scope)
	}
	n, err := s.repo.PurgeBy(ctx, f)
	if err != nil {
		return 0, fmt.Errorf("删除失败：%w", err)
	}
	return n, nil
}

// =================== Scheduler ===================

// SettingGptRefresh system_config key for gpt.refresh JSON 块。
//
// 期望结构：
//
//	{
//	  "enabled": true,
//	  "threshold_hours": 12,
//	  "scan_interval_sec": 120,
//	  "quota_recheck_minutes": 30,
//	  "max_concurrent": 3
//	}
const SettingGptRefresh = "gpt.refresh"

// GptRefreshScheduler 后台 daemon：每 interval 扫描一遍，自动刷新即将过期 + 配额过时的账号。
//
// 对齐 AdobeRefreshScheduler 的设计：
//
//   - 默认 12 小时阈值（< 12h 触发完整 silent refresh）
//   - 默认每 120s 扫一次
//   - quota 单独 30min 增量刷新（不换 token，更便宜）
//   - 并发 3 个 worker
//   - 失败 5 次置 invalid，期间 cooldown
type GptRefreshScheduler struct {
	pool      *PoolGptService
	sysCfg    *SystemConfigService
	pickProxy func() string
	logger    *zap.Logger
	stop      chan struct{}
	once      sync.Once
}

// NewGptRefreshScheduler 构造调度器。
func NewGptRefreshScheduler(
	pool *PoolGptService,
	sysCfg *SystemConfigService,
	pickProxy func() string,
	logger *zap.Logger,
) *GptRefreshScheduler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &GptRefreshScheduler{
		pool:      pool,
		sysCfg:    sysCfg,
		pickProxy: pickProxy,
		logger:    logger,
		stop:      make(chan struct{}),
	}
}

// Start 启动后台 goroutine（幂等）。
func (s *GptRefreshScheduler) Start(ctx context.Context) {
	s.once.Do(func() {
		go s.loop(ctx)
		s.logger.Info("gpt refresh scheduler started")
	})
}

// Stop 停止；sync.Once 的存在意味着停了不能再 Start 同一实例。
func (s *GptRefreshScheduler) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *GptRefreshScheduler) loop(ctx context.Context) {
	for {
		s.tick(ctx)
		interval := s.scanInterval(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-time.After(interval):
		}
	}
}

func (s *GptRefreshScheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Warn("gpt refresh scheduler tick panic", zap.Any("recover", r))
		}
	}()
	if !s.enabled(ctx) {
		return
	}
	threshold := s.thresholdHours(ctx)
	conc := s.maxConcurrent(ctx)

	ok1, f1 := s.pool.RefreshExpiring(ctx, threshold, conc, s.pickProxy)
	stale := s.quotaStale(ctx)
	ok2, f2 := s.pool.RefreshStaleQuota(ctx, stale, conc, s.pickProxy)

	if ok1+f1+ok2+f2 > 0 {
		s.logger.Info("gpt refresh scheduler tick",
			zap.Int("token_ok", ok1), zap.Int("token_fail", f1),
			zap.Int("quota_ok", ok2), zap.Int("quota_fail", f2),
			zap.Duration("threshold", threshold))
	}
}

// 配置取值；都从 system_config 走，缺省给安全默认。
func (s *GptRefreshScheduler) enabled(ctx context.Context) bool {
	if s.sysCfg == nil {
		return true
	}
	v := s.sysCfg.GetJSON(ctx, SettingGptRefresh)
	if v == nil {
		return true
	}
	if b, ok := v["enabled"].(bool); ok {
		return b
	}
	return true
}
func (s *GptRefreshScheduler) scanInterval(ctx context.Context) time.Duration {
	if s.sysCfg == nil {
		return 120 * time.Second
	}
	v := s.sysCfg.GetJSON(ctx, SettingGptRefresh)
	if v == nil {
		return 120 * time.Second
	}
	if n, ok := v["scan_interval_sec"].(float64); ok && n >= 5 {
		return time.Duration(n) * time.Second
	}
	return 120 * time.Second
}
func (s *GptRefreshScheduler) thresholdHours(ctx context.Context) time.Duration {
	if s.sysCfg == nil {
		return 12 * time.Hour
	}
	v := s.sysCfg.GetJSON(ctx, SettingGptRefresh)
	if v == nil {
		return 12 * time.Hour
	}
	if n, ok := v["threshold_hours"].(float64); ok && n >= 1 {
		return time.Duration(n) * time.Hour
	}
	return 12 * time.Hour
}
func (s *GptRefreshScheduler) maxConcurrent(ctx context.Context) int {
	if s.sysCfg == nil {
		return 3
	}
	v := s.sysCfg.GetJSON(ctx, SettingGptRefresh)
	if v == nil {
		return 3
	}
	if n, ok := v["max_concurrent"].(float64); ok && n >= 1 {
		return int(n)
	}
	return 3
}
func (s *GptRefreshScheduler) quotaStale(ctx context.Context) time.Duration {
	if s.sysCfg == nil {
		return 30 * time.Minute
	}
	v := s.sysCfg.GetJSON(ctx, SettingGptRefresh)
	if v == nil {
		return 30 * time.Minute
	}
	if n, ok := v["quota_recheck_minutes"].(float64); ok && n >= 1 {
		return time.Duration(n) * time.Minute
	}
	return 30 * time.Minute
}
