package service

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/pkg/logger"
)

// markChatFailure applies conservative pool marking for text chat: only auth failures
// (401 / invalid_grant / fatal OAuth refresh) cooldown or disable an account; everything
// else is transient so the scheduler can retry with another account.
func (s *ChatService) markChatFailure(ctx context.Context, acc *model.Account, err error) {
	if s == nil || s.pool == nil || acc == nil || err == nil {
		return
	}
	reason := err.Error()
	if shouldKeepUpstreamAPIEnabled(acc) {
		s.pool.MarkTransientFailed(ctx, acc.ID, reason)
		return
	}
	if isTransientProviderPathError(acc.Provider, err) || isChatRequestError(err) {
		s.pool.MarkTransientFailed(ctx, acc.ID, reason)
		return
	}
	if isFatalOAuthRefreshError(err) {
		disablePoolAccount(ctx, s.pool, acc, reason)
		return
	}
	if cd := chatAccountCooldown(err); cd > 0 {
		markProviderAccountFailed(ctx, s.pool, s.cfg, acc, reason, cd)
		return
	}
	s.pool.MarkTransientFailed(ctx, acc.ID, reason)
}

func chatAccountCooldown(err error) time.Duration {
	if err == nil {
		return 0
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "http 401"),
		strings.Contains(msg, " 401:"),
		strings.Contains(msg, " 401 "),
		strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "invalid_grant"),
		strings.Contains(msg, "invalid_token"),
		strings.Contains(msg, "token expired"),
		strings.Contains(msg, "authentication failed"),
		strings.Contains(msg, "refresh oauth access_token failed"):
		return 30 * time.Minute
	default:
		return 0
	}
}

func isChatRequestError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "messages is required") ||
		strings.Contains(msg, "no user/assistant messages") ||
		strings.Contains(msg, "invalid json") ||
		strings.Contains(msg, "model is required") {
		return true
	}
	if strings.Contains(msg, "http 400") || strings.Contains(msg, " 400:") {
		return !strings.Contains(msg, "invalid_grant") &&
			!strings.Contains(msg, "invalid_token") &&
			!strings.Contains(msg, "unauthorized")
	}
	return false
}

func markProviderAccountFailed(ctx context.Context, pool *AccountPool, cfg *SystemConfigService, acc *model.Account, reason string, desiredCooldown time.Duration) {
	if acc == nil || pool == nil {
		return
	}
	if shouldKeepUpstreamAPIEnabled(acc) {
		pool.MarkTransientFailed(ctx, acc.ID, reason)
		return
	}
	threshold := int64(3)
	cooldown := desiredCooldown
	if cfg != nil {
		threshold = cfg.CircuitFailureThreshold(ctx)
		if desiredCooldown > 0 {
			if sec := cfg.CircuitCooldownSeconds(ctx); sec > 0 {
				cooldown = time.Duration(sec) * time.Second
			}
		}
	}
	acc.ErrorCount++
	if threshold > 1 && int64(acc.ErrorCount) < threshold {
		cooldown = 0
	}
	pool.MarkFailed(ctx, acc.ID, reason, cooldown)
}

func disablePoolAccount(ctx context.Context, pool *AccountPool, acc *model.Account, reason string) {
	if acc == nil || pool == nil || pool.repo == nil {
		return
	}
	if shouldKeepUpstreamAPIEnabled(acc) {
		pool.MarkTransientFailed(ctx, acc.ID, reason)
		return
	}
	now := time.Now().UTC()
	fields := map[string]any{
		"status":           model.AccountStatusDisabled,
		"last_error":       truncate(reason, 240),
		"last_test_status": model.AccountTestFail,
		"last_test_error":  truncate(reason, 240),
		"last_test_at":     now,
		"cooldown_until":   nil,
		"error_count":      gorm.Expr("error_count + 1"),
	}
	if err := pool.repo.UpdateForProvider(ctx, acc.ID, acc.Provider, fields); err != nil {
		logger.FromCtx(ctx).Warn("account.disable_failed", zap.Uint64("account_id", acc.ID), zap.Error(err))
		return
	}
	acc.Status = model.AccountStatusDisabled
	pool.Reload(acc.Provider)
	logger.FromCtx(ctx).Warn("account.disabled_after_oauth_refresh_401", zap.Uint64("account_id", acc.ID), zap.String("provider", acc.Provider), zap.String("reason", truncate(reason, 240)))
}
