package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/regkit/xairefresh"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/errcode"
)

// SettingXAIRefresh xAI 续期调度器配置 key（system_config）。
const SettingXAIRefresh = "xai.refresh"

// XAIRefreshOptions 单次刷新选项。
type XAIRefreshOptions struct {
	ProxyURL string
	Caller   string
}

// RefreshOne 用 refresh_token 给单个账号换新 access_token，原地回写。
//
// 成功：更新 credential_enc / refresh_token_enc / expires_at / status=valid，清 failure。
// refresh_token 失效（400/401）：标记 invalid 终态。
// 临时错误：累计 failure_count，进短冷却。
func (s *PoolXAIService) RefreshOne(ctx context.Context, id uint64, opt XAIRefreshOptions) (*model.PoolXAI, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errcode.ResourceMissing
		}
		return nil, errcode.DBError.Wrap(err)
	}
	if len(row.RefreshTokenEnc) == 0 {
		return row, errors.New("缺少 refresh_token，无法续期")
	}
	rtPlain, err := s.aes.Decrypt(row.RefreshTokenEnc)
	if err != nil {
		return row, errcode.Internal.Wrap(err)
	}
	tokenEndpoint := ""
	if row.TokenEndpoint != nil {
		tokenEndpoint = *row.TokenEndpoint
	}

	cli, err := xairefresh.New(opt.ProxyURL, 30*time.Second)
	if err != nil {
		return row, err
	}
	td, err := cli.RefreshTokens(ctx, string(rtPlain), tokenEndpoint)
	if err != nil {
		now := time.Now().UTC()
		if errors.Is(err, xairefresh.ErrTokenInvalid) {
			_ = s.repo.MarkGatewayInvalid(ctx, id, truncateMsg("refresh token invalid: "+err.Error(), 480))
			_ = s.repo.Update(ctx, id, map[string]any{
				"last_checked_at":     now,
				"last_refresh_result": truncateMsg(err.Error(), 240),
			})
			row.Status = model.XAIStatusInvalid
			return row, err
		}
		_ = s.repo.MarkGatewayFailed(ctx, id, truncateMsg(err.Error(), 480), 10*time.Minute)
		_ = s.repo.Update(ctx, id, map[string]any{
			"last_checked_at":     now,
			"last_refresh_result": truncateMsg(err.Error(), 240),
		})
		return row, err
	}

	encAccess, err := s.aes.Encrypt([]byte(td.AccessToken))
	if err != nil {
		return row, errcode.Internal.Wrap(err)
	}
	now := time.Now().UTC()
	fields := map[string]any{
		"credential_enc":      encAccess,
		"status":              model.XAIStatusValid,
		"cooldown_until":      nil,
		"failure_count":       0,
		"error_message":       nil,
		"last_refresh_at":     now,
		"last_checked_at":     now,
		"last_refresh_result": "ok",
	}
	if !td.Expire.IsZero() {
		fields["expires_at"] = td.Expire
	}
	if td.RefreshToken != "" {
		if enc, e := s.aes.Encrypt([]byte(td.RefreshToken)); e == nil {
			fields["refresh_token_enc"] = enc
		}
	}
	if td.IDToken != "" {
		if enc, e := s.aes.Encrypt([]byte(td.IDToken)); e == nil {
			fields["id_token_enc"] = enc
		}
	}
	if td.Subject != "" {
		fields["subject"] = td.Subject
	}
	// 续期后用新 access_token 的 tier 刷新档位（消费上去后 xAI 会自动升档）。
	if tier := xairefresh.ParseJWTTier(td.AccessToken); tier >= 0 {
		fields["account_type"] = fmt.Sprintf("tier%d", tier)
	}
	if td.Email != "" && (row.Email == "" || isXAIPlaceholderEmail(row.Email)) {
		fields["email"] = td.Email
	}
	if err := s.repo.Update(ctx, id, fields); err != nil {
		return row, errcode.DBError.Wrap(err)
	}
	row.Status = model.XAIStatusValid
	return row, nil
}

// RefreshByScope 同步批量刷新（≤limit 条）。返回 (ok, fail)。
func (s *PoolXAIService) RefreshByScope(ctx context.Context, scope repo.PoolXAIRefreshScope, concurrency, limit int, pickProxy func() string) (int, int) {
	rows, err := s.repo.ListForRefresh(ctx, scope, limit)
	if err != nil || len(rows) == 0 {
		return 0, 0
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, fail := 0, 0
	for _, row := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(id uint64) {
			defer wg.Done()
			defer func() { <-sem }()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
			defer cancel()
			_, e := s.RefreshOne(rctx, id, XAIRefreshOptions{ProxyURL: proxy, Caller: "scheduler"})
			mu.Lock()
			if e != nil {
				fail++
			} else {
				ok++
			}
			mu.Unlock()
		}(row.ID)
	}
	wg.Wait()
	return ok, fail
}

func isXAIPlaceholderEmail(email string) bool {
	return len(email) > 4 && email[:4] == "xai-" && hasSuffix(email, "@token.local")
}

func hasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}

func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// XAIRefreshScheduler xAI 后台续期调度器。默认每 60s 扫一次，<15min 过期 → silent refresh。
type XAIRefreshScheduler struct {
	pool      *PoolXAIService
	sysCfg    *SystemConfigService
	pickProxy func() string
	logger    *zap.Logger
	stop      chan struct{}
	once      sync.Once
}

// NewXAIRefreshScheduler 构造。
func NewXAIRefreshScheduler(pool *PoolXAIService, sysCfg *SystemConfigService, pickProxy func() string, logger *zap.Logger) *XAIRefreshScheduler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &XAIRefreshScheduler{
		pool:      pool,
		sysCfg:    sysCfg,
		pickProxy: pickProxy,
		logger:    logger,
		stop:      make(chan struct{}),
	}
}

// Start 启动后台 goroutine（幂等）。
func (s *XAIRefreshScheduler) Start(ctx context.Context) {
	s.once.Do(func() {
		go s.loop(ctx)
		go s.billingLoop(ctx)
		s.logger.Info("xai refresh scheduler started")
	})
}

// billingLoop 周期性刷新所有可用账号的额度（cli-chat-proxy.grok.com/v1/billing）。
// 默认 30min 一轮；启动后先等 30s 让 token 续期就绪再首刷。
func (s *XAIRefreshScheduler) billingLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-s.stop:
		return
	case <-time.After(30 * time.Second):
	}
	for {
		s.billingTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-time.After(s.billingInterval(ctx)):
		}
	}
}

func (s *XAIRefreshScheduler) billingTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Warn("xai billing refresh tick panic", zap.Any("recover", r))
		}
	}()
	if !s.enabled(ctx) {
		return
	}
	ok, fail := s.pool.RefreshBillingAll(ctx, s.maxConcurrent(ctx), s.pickProxy)
	if ok+fail > 0 {
		s.logger.Info("xai billing refresh tick", zap.Int("ok", ok), zap.Int("fail", fail))
	}
}

// billingInterval 额度刷新周期。system_config 的 xai.refresh.billing_interval_sec 可覆盖（≥60s）。
func (s *XAIRefreshScheduler) billingInterval(ctx context.Context) time.Duration {
	def := 30 * time.Minute
	if s.sysCfg == nil {
		return def
	}
	v := s.sysCfg.GetJSON(ctx, SettingXAIRefresh)
	if v != nil {
		if n, ok := v["billing_interval_sec"].(float64); ok && n >= 60 {
			return time.Duration(n) * time.Second
		}
	}
	return def
}

// Stop 停止。
func (s *XAIRefreshScheduler) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *XAIRefreshScheduler) loop(ctx context.Context) {
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-time.After(s.scanInterval(ctx)):
		}
	}
}

func (s *XAIRefreshScheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Warn("xai refresh scheduler tick panic", zap.Any("recover", r))
		}
	}()
	if !s.enabled(ctx) {
		return
	}
	conc := s.maxConcurrent(ctx)
	ok, fail := s.pool.RefreshByScope(ctx, repo.XAIRefreshScopeExpiring, conc, 200, s.pickProxy)
	if ok+fail > 0 {
		s.logger.Info("xai refresh scheduler tick", zap.Int("ok", ok), zap.Int("fail", fail))
	}
}

func (s *XAIRefreshScheduler) enabled(ctx context.Context) bool {
	if s.sysCfg == nil {
		return true
	}
	v := s.sysCfg.GetJSON(ctx, SettingXAIRefresh)
	if v == nil {
		return true
	}
	if b, ok := v["enabled"].(bool); ok {
		return b
	}
	return true
}

func (s *XAIRefreshScheduler) scanInterval(ctx context.Context) time.Duration {
	if s.sysCfg == nil {
		return 60 * time.Second
	}
	v := s.sysCfg.GetJSON(ctx, SettingXAIRefresh)
	if v != nil {
		if n, ok := v["scan_interval_sec"].(float64); ok && n >= 5 {
			return time.Duration(n) * time.Second
		}
	}
	return 60 * time.Second
}

func (s *XAIRefreshScheduler) maxConcurrent(ctx context.Context) int {
	if s.sysCfg == nil {
		return 4
	}
	v := s.sysCfg.GetJSON(ctx, SettingXAIRefresh)
	if v != nil {
		if n, ok := v["max_concurrent"].(float64); ok && n >= 1 {
			return int(n)
		}
	}
	return 4
}
