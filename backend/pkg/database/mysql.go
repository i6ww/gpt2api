// Package database 封装 MySQL（GORM）与 Redis 客户端。
package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/kleinai/backend/pkg/config"
	"github.com/kleinai/backend/pkg/logger"
)

// NewMySQL 用 GORM 创建 MySQL 连接（含连接池配置与慢查询日志）。
func NewMySQL(c *config.MySQL) (*gorm.DB, error) {
	if c.DSN == "" {
		return nil, fmt.Errorf("mysql dsn empty")
	}

	gormLog := gormlogger.New(
		zapWriter{l: logger.L()},
		gormlogger.Config{
			SlowThreshold:             c.SlowThreshold,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	db, err := gorm.Open(mysql.Open(ensureUTF8MB4(c.DSN)), &gorm.Config{
		Logger:                                   gormLog,
		PrepareStmt:                              true,
		DisableForeignKeyConstraintWhenMigrating: true,
		NowFunc:                                  func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql db: %w", err)
	}

	maxOpen := c.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 100
	}
	maxIdle := c.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 20
	}
	lifetime := c.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	idleTime := c.ConnMaxIdleTime
	if idleTime <= 0 {
		idleTime = 10 * time.Minute
	}

	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(lifetime)
	sqlDB.SetConnMaxIdleTime(idleTime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	logger.L().Info("mysql connected",
		zap.Int("max_open", maxOpen),
		zap.Int("max_idle", maxIdle),
		zap.Duration("lifetime", lifetime),
		zap.Duration("idle_time", idleTime),
	)
	return db, nil
}

// ensureUTF8MB4 强制连接使用 charset=utf8mb4。
//
// 表与列虽然是 utf8mb4，但若连接层 charset 不是 utf8mb4（DSN 未显式指定时驱动可能用
// utf8/latin1），写入 emoji 等 4 字节字符会报 Error 1366 (Incorrect string value)。
// 历史故障：含 emoji 的 prompt 让 generation_upstream_log 落库失败，xAI 上游错误日志丢失。
// 这里在打开连接前规范化 DSN，不依赖各机器手改环境变量。
func ensureUTF8MB4(dsn string) string {
	if cfg, err := mysqldriver.ParseDSN(dsn); err == nil {
		if cfg.Params == nil {
			cfg.Params = map[string]string{}
		}
		cfg.Params["charset"] = "utf8mb4"
		return cfg.FormatDSN()
	}
	// 解析失败兜底：query 上没有 charset 就补一个。
	if !strings.Contains(strings.ToLower(dsn), "charset=") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "charset=utf8mb4"
	}
	return dsn
}

// zapWriter 让 GORM logger 写入 zap。
type zapWriter struct{ l *zap.Logger }

func (z zapWriter) Printf(format string, args ...any) {
	z.l.Sugar().Infof(format, args...)
}
