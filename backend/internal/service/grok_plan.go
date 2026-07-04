package service

import (
	"strings"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
)

func requiredGrokPlanForTask(modelCode string, kind provider.Kind) string {
	switch strings.ToLower(strings.TrimSpace(modelCode)) {
	case "grok-4.20-heavy", "grok-4-heavy":
		return "heavy"
	case "grok-4.20-auto", "grok-4.20-expert", "grok-4.1-expert":
		return "super"
	case "grok-imagine-video", "grok-video", "grok-i2v", "vid-v1", "vid-i2v":
		return "super"
	}
	if kind == provider.KindVideo {
		return "super"
	}
	return "basic"
}

func accountSupportsGrokPlan(acc *model.Account, required string) bool {
	if acc == nil || acc.Provider != model.ProviderGROK {
		return false
	}
	required = normalizeGrokPlanType(required)
	if required == "" {
		return true
	}
	actual := normalizeGrokPlanType(accountPlanType(acc))
	if actual == "" {
		return required == "basic"
	}
	return grokPlanRank(actual) >= grokPlanRank(required)
}

func accountPlanType(acc *model.Account) string {
	meta := accountOAuthMeta(acc)
	if v, ok := meta["plan_type"].(string); ok {
		return v
	}
	return ""
}

// normalizeGrokPlanType 把 pool_grok.account_type（或 oauth_meta.plan_type）
// 归一成 "basic" / "super" / "heavy" 三档，用来和 requiredGrokPlanForTask 比较。
//
// 历史：pool_grok 用 "free / super_grok / super_grok_heavy / team / unknown"
// 这套字面值入库（见 backend/internal/regkit/grokrefresh/client.go），而
// requiredGrokPlanForTask 用 "basic / super / heavy" 三档。这里负责把前者翻译成后者。
//
//   - free / unknown / 空     → basic
//   - super / super_grok / team → super
//   - heavy / super_grok_heavy → heavy
func normalizeGrokPlanType(plan string) string {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "basic", "free", "unknown", "":
		return "basic"
	case "heavy", "super_grok_heavy", "supergrokheavy", "grok_heavy":
		return "heavy"
	case "super", "super_grok", "supergrok", "grok_super", "team":
		return "super"
	default:
		return ""
	}
}

func grokPlanRank(plan string) int {
	switch normalizeGrokPlanType(plan) {
	case "basic":
		return 1
	case "super":
		return 2
	case "heavy":
		return 3
	default:
		return 0
	}
}
