package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/regkit/adoberefresh"
	"github.com/kleinai/backend/internal/repo"

	"go.uber.org/zap"
)

// AdobeRefreshOptions 单次刷新调用的可覆盖项；service 层兜底从系统配置取。
type AdobeRefreshOptions struct {
	ProxyURL     string
	OnlyCredits  bool   // true = 跳过 silent refresh，仅拿积分
	Caller       string // "manual" / "scheduler" / "register"，写日志用
	FailureLimit int    // 连续失败多少次后置 invalid（0 = 5 次）
}

// RefreshOne 用账号 ID 触发一次刷新。线程安全。
//
// 步骤（与 Python newwork/token_refresh.py 一致）：
//
//  1. 读 row + 解 access_token / cookie
//  2. cookie 在 → 走完整 silent refresh 链（换 token → profile → credits）
//  3. cookie 空但 token 在 → 退化为 FetchOnly（只拿 profile + credits）
//  4. 更新 expires_at / credits / display_name / adobe_user_id / status / last_*
//
// 返回更新后的 row（未持久化失败前，row 已经反映新字段）。
func (s *PoolAdobeService) RefreshOne(ctx context.Context, id uint64, opt AdobeRefreshOptions) (*model.PoolAdobe, error) {
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

	// 解密 access_token / cookie / device_token
	var accessToken, cookie, deviceToken string
	if len(row.AccessTokenEnc) > 0 && s.aes != nil {
		if b, err := s.aes.Decrypt(row.AccessTokenEnc); err == nil {
			accessToken = string(b)
		}
	}
	if len(row.CookieEnc) > 0 && s.aes != nil {
		if b, err := s.aes.Decrypt(row.CookieEnc); err == nil {
			cookie = string(b)
		}
	}
	if len(row.DeviceTokenEnc) > 0 && s.aes != nil {
		if b, err := s.aes.Decrypt(row.DeviceTokenEnc); err == nil {
			deviceToken = string(b)
		}
	}
	if accessToken == "" && cookie == "" && (deviceToken == "" || strings.TrimSpace(row.DeviceID) == "") {
		return nil, errors.New("账号缺少 access_token / cookie / device_token，无法刷新")
	}

	rOpt := adoberefresh.RefreshOptions{
		ProxyURL: opt.ProxyURL,
		// 续期超时 30s→10s：60 并发刷新时，挂死/慢连接占满 worker 会让整轮 tick
		// 卡住十几分钟。10s 足够正常 IMS 往返(<2s)，又能让死号快速失败、快速过号，
		// 一轮就能churn 完一大批、尽快暴露真实可救数量。
		Timeout: 10 * time.Second,
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"last_checked_at": now,
	}

	switch {
	case !opt.OnlyCredits && deviceToken != "" && strings.TrimSpace(row.DeviceID) != "":
		// FF-iOS 受信任设备路径：免验证码刷新 access_token，优先于 cookie。
		full, ferr := adoberefresh.RefreshOneViaDeviceToken(ctx, deviceToken, row.DeviceID, rOpt)
		if ferr != nil {
			return s.markRefreshFailure(ctx, row, ferr.Error(), opt.FailureLimit)
		}
		updates["last_refresh_at"] = now
		if full.AccessToken != "" {
			enc, encErr := s.aes.Encrypt([]byte(full.AccessToken))
			if encErr == nil {
				updates["access_token_enc"] = enc
				accessToken = full.AccessToken
			}
		}
		if full.ExpiresAt > 0 {
			t := time.Unix(full.ExpiresAt, 0).UTC()
			updates["expires_at"] = t
			row.ExpiresAt = &t
		}
		if full.Credits >= 0 {
			updates["credits"] = full.Credits
			updates["last_credits_check_at"] = now
			row.Credits = full.Credits
		}
		if full.DisplayName != "" {
			updates["display_name"] = full.DisplayName
		}
		if full.UserID != "" {
			updates["adobe_user_id"] = full.UserID
		}
	case !opt.OnlyCredits && cookie != "":
		// 完整路径：换 token + 拉 profile + 拉 credits
		full, ferr := adoberefresh.RefreshOne(ctx, cookie, rOpt)
		if ferr != nil {
			return s.markRefreshFailure(ctx, row, ferr.Error(), opt.FailureLimit)
		}
		updates["last_refresh_at"] = now
		if full.AccessToken != "" {
			enc, encErr := s.aes.Encrypt([]byte(full.AccessToken))
			if encErr == nil {
				updates["access_token_enc"] = enc
				accessToken = full.AccessToken
			}
		}
		if full.ExpiresAt > 0 {
			t := time.Unix(full.ExpiresAt, 0).UTC()
			updates["expires_at"] = t
			row.ExpiresAt = &t
		}
		if full.Credits >= 0 {
			updates["credits"] = full.Credits
			updates["last_credits_check_at"] = now
			row.Credits = full.Credits
		}
		if full.DisplayName != "" {
			updates["display_name"] = full.DisplayName
		}
		if full.UserID != "" {
			updates["adobe_user_id"] = full.UserID
		}
	default:
		// 轻量路径：用现有 access_token 直接打 profile + credits
		fc := adoberefresh.FetchOnly(ctx, accessToken, rOpt)
		if fc == nil {
			return s.markRefreshFailure(ctx, row, "FetchOnly 返回空", opt.FailureLimit)
		}
		if fc.ExpiresAt > 0 {
			t := time.Unix(fc.ExpiresAt, 0).UTC()
			updates["expires_at"] = t
			row.ExpiresAt = &t
		}
		if fc.Credits >= 0 {
			updates["credits"] = fc.Credits
			updates["last_credits_check_at"] = now
			row.Credits = fc.Credits
		}
		if fc.DisplayName != "" {
			updates["display_name"] = fc.DisplayName
		}
		if fc.UserID != "" {
			updates["adobe_user_id"] = fc.UserID
		}
	}

	// 任一刷新成功后，把状态置 valid，failure_count 归零
	updates["status"] = model.AdobeStatusValid
	updates["failure_count"] = 0
	updates["error_message"] = ""
	updates["cooldown_until"] = nil

	if err := s.repo.Update(ctx, id, updates); err != nil {
		return nil, fmt.Errorf("写库失败：%w", err)
	}
	return row, nil
}

// markRefreshFailure 失败时累加 failure_count，达到 limit 后置 invalid。
//
// 默认 limit=5；< limit 时进 cooldown（10min 退避），>= limit 时直接 invalid。
func (s *PoolAdobeService) markRefreshFailure(ctx context.Context, row *model.PoolAdobe, msg string, limit int) (*model.PoolAdobe, error) {
	if limit <= 0 {
		limit = 5
	}
	if len(msg) > 480 {
		msg = msg[:480] + "…"
	}
	failCount := row.FailureCount + 1
	now := time.Now().UTC()
	updates := map[string]any{
		"failure_count":   failCount,
		"error_message":   msg,
		"last_checked_at": now,
	}
	if failCount >= limit {
		updates["status"] = model.AdobeStatusInvalid
	} else {
		updates["status"] = model.AdobeStatusCooldown
		cool := now.Add(10 * time.Minute)
		updates["cooldown_until"] = cool
	}
	_ = s.repo.Update(ctx, row.ID, updates)
	return row, fmt.Errorf("refresh 失败：%s", msg)
}

// RefreshExpiring 后台扫描入口：把 within 内即将过期的账号都刷一遍（并发 max 个）。
//
// 返回刷新成功 / 失败计数；within=12h 与 newbanana / newwork python 默认一致。
func (s *PoolAdobeService) RefreshExpiring(ctx context.Context, within time.Duration, max int, pickProxy func() string) (ok, fail int) {
	// 单轮最多取 2000 个待续期账号：配合后台高并发（max 可设几十），
	// 让一轮扫描就能把过期积压（曾达 1800+）基本抽干，而不是被旧的 200 上限
	// 卡成长期欠债——欠债会直接缩小生成可用池（AvailableForGateway 要求 token 未过期）。
	rows, err := s.repo.ListExpiringSoon(ctx, within, 2000)
	if err != nil || len(rows) == 0 {
		return
	}
	if max <= 0 {
		max = 4
	}
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, r := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(r *model.PoolAdobe) {
			defer wg.Done()
			defer func() { <-sem }()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			_, err := s.RefreshOne(ctx, r.ID, AdobeRefreshOptions{
				ProxyURL: proxy,
				Caller:   "scheduler",
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

// RefreshStaleCredits 后台扫描入口：拿现有 access_token 刷新 credits。
//
// 比 RefreshExpiring 便宜得多（不换 token），用来让 UI 上 "积分" 列保持新鲜。
func (s *PoolAdobeService) RefreshStaleCredits(ctx context.Context, staleAfter time.Duration, max int, pickProxy func() string) (ok, fail int) {
	rows, err := s.repo.ListStaleCredits(ctx, staleAfter, 200)
	if err != nil || len(rows) == 0 {
		return
	}
	if max <= 0 {
		max = 4
	}
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, r := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(r *model.PoolAdobe) {
			defer wg.Done()
			defer func() { <-sem }()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			_, err := s.RefreshOne(ctx, r.ID, AdobeRefreshOptions{
				ProxyURL:    proxy,
				OnlyCredits: true,
				Caller:      "scheduler",
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

// RefreshQuotaRecovery 定时处理「额度回收中」账号：
//   - 只走轻量 FetchOnly 拉 credits，不换 token，降低风控压力；
//   - credits > 0：RefreshOne 已自动拉回 valid；
//   - credits <= 0：保持 cooldown，24h 后再试，避免被 gateway 继续调度；
//   - 连续刷新失败按 RefreshOne 的失败计数处理，达到阈值会变 invalid。
func (s *PoolAdobeService) RefreshQuotaRecovery(ctx context.Context, staleAfter time.Duration, max int, pickProxy func() string) (ok, fail int) {
	rows, err := s.repo.ListQuotaRecovery(ctx, staleAfter, 200)
	if err != nil || len(rows) == 0 {
		return
	}
	if max <= 0 {
		max = 4
	}
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, r := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(r *model.PoolAdobe) {
			defer wg.Done()
			defer func() { <-sem }()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			fresh, e := s.RefreshOne(ctx, r.ID, AdobeRefreshOptions{
				ProxyURL:    proxy,
				OnlyCredits: true,
				Caller:      "quota_recovery",
			})
			if e == nil && fresh != nil && fresh.Credits <= 0 {
				_ = s.repo.Update(ctx, r.ID, map[string]any{
					"status":         model.AdobeStatusCooldown,
					"cooldown_until": time.Now().UTC().Add(24 * time.Hour),
					"error_message":  "quota recovery: still zero credits",
				})
			}
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

// RefreshByScope 后台手动入口：按 scope（all / zero_credits / abnormal / expiring）批量刷新。
//
// onlyCredits=true → 全部走轻量路径（只拿 profile + credits，省 token 重发）。
//
// 返回 (ok, fail, total)；并发由 max 控制（<=0 默认 4）。
//
// 用例：
//
//   - "刷新 0 积分"        → scope=zero_credits, onlyCredits=true
//   - "刷新异常账号 token"  → scope=abnormal, onlyCredits=false
//   - "刷新全部账号 token"  → scope=all, onlyCredits=false
//   - "刷新异常账号完整"    → scope=abnormal, onlyCredits=false（与 token 同效，更显式）
//   - "刷新全部完整"        → scope=all, onlyCredits=false
func (s *PoolAdobeService) RefreshByScope(
	ctx context.Context,
	scope repo.PoolAdobeRefreshScope,
	onlyCredits bool,
	max int,
	pickProxy func() string,
) (ok, fail, total int) {
	rows, err := s.repo.ListForRefresh(ctx, scope, 500)
	if err != nil || len(rows) == 0 {
		return
	}
	total = len(rows)
	if max <= 0 {
		max = 4
	}
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, r := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(r *model.PoolAdobe) {
			defer wg.Done()
			defer func() { <-sem }()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			fresh, e := s.RefreshOne(ctx, r.ID, AdobeRefreshOptions{
				ProxyURL:    proxy,
				OnlyCredits: onlyCredits,
				Caller:      "manual",
			})
			if e == nil && scope == repo.AdobeRefreshScopeRecovery && fresh != nil && fresh.Credits <= 0 {
				_ = s.repo.Update(ctx, r.ID, map[string]any{
					"status":         model.AdobeStatusCooldown,
					"cooldown_until": time.Now().UTC().Add(24 * time.Hour),
					"error_message":  "quota recovery: still zero credits",
				})
			}
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

// AdobeRefreshScheduler 后台 daemon：每 interval 扫描一遍，自动刷新即将过期的账号。
//
// 对齐 Python newwork/token_refresh.py 的 RefreshScheduler：
//
//   - 默认 12 小时阈值（< 12h 过期触发完整 silent refresh）
//   - 默认每 60s 扫一次
//   - credits 单独 30min 增量刷新（不换 token，更便宜）
//   - 并发 4 个 worker（防止 firefly / IMS 限流）
//   - 失败 5 次置 invalid，期间 10 分钟 cooldown
//
// 由 bootstrap 启动；通过 system_config 实时调整阈值 / 并发数 / 是否启用。
type AdobeRefreshScheduler struct {
	pool      *PoolAdobeService
	sysCfg    *SystemConfigService
	pickProxy func() string
	logger    *zap.Logger
	stop      chan struct{}
	once      sync.Once
}

// NewAdobeRefreshScheduler 构造调度器。
//
// pickProxy 留空时所有续期请求走直连；通常应该传一个轮转函数（每次返回一个不同的代理 URL），
// 让多账号并发续期分摊到不同出口 IP。
func NewAdobeRefreshScheduler(
	pool *PoolAdobeService,
	sysCfg *SystemConfigService,
	pickProxy func() string,
	logger *zap.Logger,
) *AdobeRefreshScheduler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &AdobeRefreshScheduler{
		pool:      pool,
		sysCfg:    sysCfg,
		pickProxy: pickProxy,
		logger:    logger,
		stop:      make(chan struct{}),
	}
}

// Start 启动后台 goroutine（幂等）。
func (s *AdobeRefreshScheduler) Start(ctx context.Context) {
	s.once.Do(func() {
		go s.loop(ctx)
		s.logger.Info("adobe refresh scheduler started")
	})
}

// Stop 停止；注意 sync.Once 的存在意味着停了不能再 Start 同一实例。
func (s *AdobeRefreshScheduler) Stop() {
	select {
	case <-s.stop:
		// already closed
	default:
		close(s.stop)
	}
}

func (s *AdobeRefreshScheduler) loop(ctx context.Context) {
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

func (s *AdobeRefreshScheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Warn("adobe refresh scheduler tick panic", zap.Any("recover", r))
		}
	}()
	if !s.enabled(ctx) {
		return
	}
	threshold := s.thresholdHours(ctx)
	credConcRefresh := s.maxConcurrent(ctx)

	// 1) 12h 内即将过期的账号：完整 silent refresh
	ok1, f1 := s.pool.RefreshExpiring(ctx, threshold, credConcRefresh, s.pickProxy)
	// 2) 30min 没看 credits 的 valid 账号：只刷 credits
	stale := s.creditsStale(ctx)
	ok2, f2 := s.pool.RefreshStaleCredits(ctx, stale, credConcRefresh, s.pickProxy)
	// 3) taste_exhausted / quota_exhausted / 0 credits 账号：额度回收探测。
	recoveryStale := s.quotaRecoveryStale(ctx)
	ok3, f3 := s.pool.RefreshQuotaRecovery(ctx, recoveryStale, credConcRefresh, s.pickProxy)

	if ok1+f1+ok2+f2+ok3+f3 > 0 {
		s.logger.Info("adobe refresh scheduler tick",
			zap.Int("token_ok", ok1), zap.Int("token_fail", f1),
			zap.Int("credits_ok", ok2), zap.Int("credits_fail", f2),
			zap.Int("recovery_ok", ok3), zap.Int("recovery_fail", f3),
			zap.Duration("threshold", threshold))
	}
}

// 配置取值；都从 system_config 走，缺省给安全默认。
func (s *AdobeRefreshScheduler) enabled(ctx context.Context) bool {
	if s.sysCfg == nil {
		return true
	}
	v := s.sysCfg.GetJSON(ctx, SettingAdobeRefresh)
	if v == nil {
		return true
	}
	if b, ok := v["enabled"].(bool); ok {
		return b
	}
	return true
}
func (s *AdobeRefreshScheduler) scanInterval(ctx context.Context) time.Duration {
	if s.sysCfg == nil {
		return 60 * time.Second
	}
	v := s.sysCfg.GetJSON(ctx, SettingAdobeRefresh)
	if v == nil {
		return 60 * time.Second
	}
	if n, ok := v["scan_interval_sec"].(float64); ok && n >= 5 {
		return time.Duration(n) * time.Second
	}
	return 60 * time.Second
}
func (s *AdobeRefreshScheduler) thresholdHours(ctx context.Context) time.Duration {
	if s.sysCfg == nil {
		return 12 * time.Hour
	}
	v := s.sysCfg.GetJSON(ctx, SettingAdobeRefresh)
	if v == nil {
		return 12 * time.Hour
	}
	if n, ok := v["threshold_hours"].(float64); ok && n >= 1 {
		return time.Duration(n) * time.Hour
	}
	return 12 * time.Hour
}
func (s *AdobeRefreshScheduler) maxConcurrent(ctx context.Context) int {
	if s.sysCfg == nil {
		return 4
	}
	v := s.sysCfg.GetJSON(ctx, SettingAdobeRefresh)
	if v == nil {
		return 4
	}
	if n, ok := v["max_concurrent"].(float64); ok && n >= 1 {
		return int(n)
	}
	return 4
}
func (s *AdobeRefreshScheduler) creditsStale(ctx context.Context) time.Duration {
	if s.sysCfg == nil {
		return 30 * time.Minute
	}
	v := s.sysCfg.GetJSON(ctx, SettingAdobeRefresh)
	if v == nil {
		return 30 * time.Minute
	}
	if n, ok := v["credits_recheck_minutes"].(float64); ok && n >= 1 {
		return time.Duration(n) * time.Minute
	}
	return 30 * time.Minute
}

func (s *AdobeRefreshScheduler) quotaRecoveryStale(ctx context.Context) time.Duration {
	if s.sysCfg == nil {
		return 60 * time.Minute
	}
	v := s.sysCfg.GetJSON(ctx, SettingAdobeRefresh)
	if v == nil {
		return 60 * time.Minute
	}
	if n, ok := v["quota_recovery_minutes"].(float64); ok && n >= 5 {
		return time.Duration(n) * time.Minute
	}
	return 60 * time.Minute
}

// SettingAdobeRefresh system_config key for adobe.refresh JSON 块。
//
// 期望结构：
//
//	{
//	  "enabled": true,
//	  "threshold_hours": 12,
//	  "scan_interval_sec": 60,
//	  "credits_recheck_minutes": 30,
//	  "quota_recovery_minutes": 60,
//	  "max_concurrent": 4
//	}
const SettingAdobeRefresh = "adobe.refresh"

// AdobeProxyPickerFunc 返回一个轮转代理 URL 的函数；同一个函数每次返回不同的代理。
//
// 由 bootstrap 在注入 scheduler 时构造一次（基于 ProxyService）。
//
// 提取出来便于测试 + 让其它 service（GPT/Grok 后台续期，未来扩展）也能用一致的轮转策略。
func AdobeProxyPickerFunc(proxySvc *ProxyService) func() string {
	if proxySvc == nil {
		return nil
	}
	return func() string {
		rows, err := proxySvc.ListEnabled(context.Background())
		if err != nil || len(rows) == 0 {
			return ""
		}
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(rows))))
		row := rows[idx.Int64()]
		u, err := proxySvc.BuildURL(row)
		if err != nil || u == nil {
			return ""
		}
		// 防止序列化时把 user:pass 暴露成空，再次 trim 一下。
		s := strings.TrimSpace(u.String())
		return s
	}
}
