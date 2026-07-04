package service

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// SettingFlowMusicRefresh system_config key：flowmusic.refresh JSON 块。
//
// 期望结构：
//
//	{"enabled": true, "threshold_minutes": 15, "scan_interval_sec": 120, "max_concurrent": 2}
//
// 说明：Supabase access_token 寿命仅 ~1h，所以「提前续期窗口」按分钟算（默认 15min），
// 而不是小时——否则每次扫描都判定为即将过期，会每个 tick 都刷一遍。
// 兼容旧字段 threshold_hours（>=1 小时）。
const SettingFlowMusicRefresh = "flowmusic.refresh"

// defaultRefreshLead 默认提前续期窗口：token 还剩 < 15min 才刷。
const defaultRefreshLead = 15 * time.Minute

// GoogleRefreshScheduler 后台 daemon：定期刷新即将过期的 FlowMusic 账号 token。
//
// 由 bootstrap 启动；通过 system_config 实时调阈值 / 间隔 / 启停。
type GoogleRefreshScheduler struct {
	pool   *PoolGoogleService
	sysCfg *SystemConfigService
	logger *zap.Logger
	stop   chan struct{}
	once   sync.Once
}

// NewGoogleRefreshScheduler 构造调度器。
func NewGoogleRefreshScheduler(pool *PoolGoogleService, sysCfg *SystemConfigService, logger *zap.Logger) *GoogleRefreshScheduler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &GoogleRefreshScheduler{pool: pool, sysCfg: sysCfg, logger: logger, stop: make(chan struct{})}
}

// Start 启动后台 goroutine（幂等）。
func (s *GoogleRefreshScheduler) Start(ctx context.Context) {
	if s == nil || s.pool == nil {
		return
	}
	s.once.Do(func() {
		go s.loop(ctx)
		s.logger.Info("flowmusic refresh scheduler started")
	})
}

// Stop 停止。
func (s *GoogleRefreshScheduler) Stop() {
	if s == nil {
		return
	}
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *GoogleRefreshScheduler) loop(ctx context.Context) {
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

func (s *GoogleRefreshScheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Warn("flowmusic refresh tick panic", zap.Any("recover", r))
		}
	}()
	if !s.enabled(ctx) {
		return
	}
	ok, fail := s.pool.RefreshExpiring(ctx, s.refreshLead(ctx), s.maxConcurrent(ctx))
	if ok+fail > 0 {
		s.logger.Info("flowmusic refresh tick", zap.Int("ok", ok), zap.Int("fail", fail))
	}
}

func (s *GoogleRefreshScheduler) cfgJSON(ctx context.Context) map[string]any {
	if s.sysCfg == nil {
		return nil
	}
	return s.sysCfg.GetJSON(ctx, SettingFlowMusicRefresh)
}

func (s *GoogleRefreshScheduler) enabled(ctx context.Context) bool {
	v := s.cfgJSON(ctx)
	if v == nil {
		return true
	}
	if b, ok := v["enabled"].(bool); ok {
		return b
	}
	return true
}

func (s *GoogleRefreshScheduler) scanInterval(ctx context.Context) time.Duration {
	v := s.cfgJSON(ctx)
	if v != nil {
		if n, ok := v["scan_interval_sec"].(float64); ok && n >= 10 {
			return time.Duration(n) * time.Second
		}
	}
	return 120 * time.Second
}

// refreshLead 返回「提前续期窗口」：expires_at 距现在小于该值才刷。
//
// 优先级：threshold_minutes（分钟）> threshold_hours（小时，旧字段）> 默认 15min。
func (s *GoogleRefreshScheduler) refreshLead(ctx context.Context) time.Duration {
	v := s.cfgJSON(ctx)
	if v != nil {
		if n, ok := v["threshold_minutes"].(float64); ok && n >= 1 {
			return time.Duration(n) * time.Minute
		}
		if n, ok := v["threshold_hours"].(float64); ok && n >= 1 {
			return time.Duration(n) * time.Hour
		}
	}
	return defaultRefreshLead
}

func (s *GoogleRefreshScheduler) maxConcurrent(ctx context.Context) int {
	v := s.cfgJSON(ctx)
	if v != nil {
		if n, ok := v["max_concurrent"].(float64); ok && n >= 1 {
			return int(n)
		}
	}
	return 2
}
