// Package bootstrap 集中初始化所有基础设施，供 cmd/* 复用。
package bootstrap

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/kleinai/backend/pkg/config"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/database"
	"github.com/kleinai/backend/pkg/jwtx"
	"github.com/kleinai/backend/pkg/logger"
	"github.com/kleinai/backend/pkg/ratelimit"
	"github.com/kleinai/backend/pkg/snowflake"
	"github.com/kleinai/backend/pkg/version"
)

// Deps 启动后向业务层注入的依赖集合。
type Deps struct {
	Cfg              *config.Config
	DB               *gorm.DB
	Redis            *redis.Client
	JWT              *jwtx.Manager
	Limiter          *ratelimit.Limiter
	AES              *crypto.AESGCM
	ClusterBootstrap []byte // 解码后的 cluster bootstrap 根密钥；空表示集群禁用
}

// Init 完整初始化（config / logger / mysql / redis / jwt / aes / snowflake）。
func Init(serviceName string) (*Deps, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if err := logger.Init(cfg); err != nil {
		return nil, fmt.Errorf("init logger: %w", err)
	}
	logger.L().Info("kleinai starting",
		zap.String("service", serviceName),
		zap.String("env", cfg.App.Env),
		zap.String("version", version.Info()),
	)

	if err := snowflake.Init(cfg.Snowflake.NodeID); err != nil {
		return nil, err
	}

	jwtMgr, err := jwtx.New(cfg.JWT.Secret, cfg.JWT.RefreshSecret, cfg.JWT.AccessTTL, cfg.JWT.RefreshTTL)
	if err != nil {
		return nil, fmt.Errorf("init jwt: %w", err)
	}

	aes, err := initAES(cfg.AESKey)
	if err != nil {
		return nil, fmt.Errorf("init aes: %w", err)
	}

	// 启动时 mysql / redis 可能因为同一台 host 同时启动还没就绪（dev-full
	// compose 经常出现：admin 比 mysql / redis 先就绪 → 一次失败立刻进 degraded mode，
	// 路由只剩 /ping，全 API 都 404）。这里加 60s 内的退避重试，把瞬时不可用磨平。
	db, err := connectMySQLWithRetry(cfg)
	if err != nil {
		if cfg.IsDev() {
			logger.L().Warn("mysql unavailable, running in degraded mode", zap.Error(err))
			db = nil
		} else {
			return nil, err
		}
	}

	rdb, err := connectRedisWithRetry(cfg)
	if err != nil {
		if cfg.IsDev() {
			logger.L().Warn("redis unavailable, running in degraded mode", zap.Error(err))
			rdb = nil
		} else {
			return nil, err
		}
	}

	var limiter *ratelimit.Limiter
	if rdb != nil {
		limiter = ratelimit.New(rdb)
	}

	clusterBootstrap, err := decodeClusterBootstrap(cfg.Cluster.BootstrapSecret)
	if err != nil {
		// dev 容忍：cluster 关闭即可
		if cfg.IsDev() {
			logger.L().Warn("KLEIN_CLUSTER_BOOTSTRAP_SECRET invalid; cluster disabled", zap.Error(err))
			clusterBootstrap = nil
		} else {
			return nil, fmt.Errorf("cluster bootstrap secret: %w", err)
		}
	}

	return &Deps{
		Cfg:              cfg,
		DB:               db,
		Redis:            rdb,
		JWT:              jwtMgr,
		Limiter:          limiter,
		AES:              aes,
		ClusterBootstrap: clusterBootstrap,
	}, nil
}

// decodeClusterBootstrap 接受 hex(64) 或 32 字节明文。
func decodeClusterBootstrap(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) >= 16 {
		return b, nil
	}
	if len(raw) >= 16 {
		return []byte(raw), nil
	}
	return nil, errors.New("must be hex(>=32 chars) or raw(>=16 bytes)")
}

// 启动连接 MySQL / Redis 的退避重试参数。挑 60s 是因为 docker compose
// 上 mysql 冷启动到 healthy 一般 10-20s，redis 加载 AOF 也很少超过 30s，
// 留一倍 buffer。重试间隔从 500ms 起逐步加到 4s，避免风暴。
const (
	depRetryMax      = 60 * time.Second
	depRetryMinSleep = 500 * time.Millisecond
	depRetryMaxSleep = 4 * time.Second
)

func connectMySQLWithRetry(cfg *config.Config) (*gorm.DB, error) {
	return retryDial[*gorm.DB]("mysql", func() (*gorm.DB, error) {
		return database.NewMySQL(&cfg.MySQL)
	})
}

func connectRedisWithRetry(cfg *config.Config) (*redis.Client, error) {
	return retryDial[*redis.Client]("redis", func() (*redis.Client, error) {
		return database.NewRedis(&cfg.Redis)
	})
}

func retryDial[T any](name string, dial func() (T, error)) (T, error) {
	deadline := time.Now().Add(depRetryMax)
	sleep := depRetryMinSleep
	for attempt := 1; ; attempt++ {
		val, err := dial()
		if err == nil {
			if attempt > 1 {
				logger.L().Info(name+" connected after retry", zap.Int("attempts", attempt))
			}
			return val, nil
		}
		if time.Now().After(deadline) {
			var zero T
			return zero, fmt.Errorf("%s connect timeout after %s: %w", name, depRetryMax, err)
		}
		// 只在 attempt=1 和每 5 次打一条日志，避免刷屏。
		if attempt == 1 || attempt%5 == 0 {
			logger.L().Warn(name+" connect failed, retrying",
				zap.Int("attempt", attempt), zap.Duration("sleep", sleep), zap.Error(err))
		}
		time.Sleep(sleep)
		if sleep < depRetryMaxSleep {
			sleep *= 2
			if sleep > depRetryMaxSleep {
				sleep = depRetryMaxSleep
			}
		}
	}
}

func initAES(raw string) (*crypto.AESGCM, error) {
	if raw == "" {
		return nil, nil
	}
	key, err := decodeAESKey(raw)
	if err != nil {
		return nil, err
	}
	return crypto.NewAESGCM(key)
}

func decodeAESKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	return nil, errors.New("KLEIN_AES_KEY must be 32 bytes raw or 64 hex chars")
}

// Run 优雅启停 HTTP 服务。
func Run(srv *http.Server, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		logger.L().Info("http listening", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.L().Info("shutting down http server")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Sync()
	logger.L().Info("graceful shutdown done")
	return nil
}
