// Package grokrefresh 提供 GROK 账号探测：用 sso token 调 grok.com /rest/rate-limits
// 拿当前账号的 quota 窗口，进而推断订阅类型（Free / SuperGrok / Heavy）。
//
// 参考：grok2api-deploy 项目 app/services/reverse/rate_limits.py
// 与 app/services/token/manager.py 的 _infer_pool_name_from_windows。
//
// 推断规则（auto 模式 totalQueries）：
//
//	150 → super_grok_heavy
//	 50 → super_grok
//	 20 → free
//	其他 → unknown
//
// HTTP 状态：
//
//	200 → 解析 quota，trial_status = active
//	401 → ErrTokenExpired，调用方应累计 fail_count
//	403 → ErrTokenForbidden，调用方应直接置失败
//	其他 → ErrTransient，临时失败（不计入 fail_count）
package grokrefresh

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/kleinai/backend/pkg/outbound"
)

const (
	// RateLimitsURL grok.com 提供的 rate-limits 查询接口（仅查询，不消费）。
	RateLimitsURL = "https://grok.com/rest/rate-limits"

	// SubscriptionsURL grok.com 个人中心拉真订阅状态用的接口。
	// 通过 grok.com /?_s=account（个人等级页面）触发；返回 application/json，
	// 含订阅 tier、状态、period 起止时间等 — 这是 grok 唯一公开的"订阅到期"
	// 信号，比 rate-limits 的 windowSizeSeconds（额度刷新周期）准确得多。
	SubscriptionsURL = "https://grok.com/rest/subscriptions"

	// DefaultModelName 探测用模型；Free / SuperGrok / Heavy 都能查询其 quota
	// （即便没有访问权也会返回 totalQueries=0，仍可作为"账号活跃 + 类型识别"的探针）。
	DefaultModelName = "grok-3"

	// 默认 UA — 与 web_client 大致一致。
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
)

// 账号订阅类型常量。
const (
	AccountTypeFree           = "free"
	AccountTypeSuperGrokLite  = "super_grok_lite"
	AccountTypeSuperGrok      = "super_grok"
	AccountTypeSuperGrokHeavy = "super_grok_heavy"
	AccountTypeUnknown        = "unknown"
)

// 订阅状态常量 — `/rest/subscriptions.status` 归一后的值。
//
// grok 返回原始 enum 字面值如 "SUBSCRIPTION_STATUS_TRIALING"，normalizeSubscriptionStatus
// 会脱前缀转小写。这里列出常见 5 种 + ""（未拉到）。
const (
	SubStatusActive   = "active"
	SubStatusTrialing = "trialing" // 三天试用期内
	SubStatusInactive = "inactive" // grok 视为没订阅（含上周期已过未续）
	SubStatusPastDue  = "past_due" // 欠费
	SubStatusCanceled = "canceled"
)

// stripe product_id → 我们的 account_type / 友好名称的硬编码映射。
//
// 来源：2026-05 抓 billing.stripe.com HAR 看到 customer-portal 返回的产品配置。
// 这里只列我们见过的，未来 grok 上新档位时再补。
var stripeProductToTier = map[string]string{
	"prod_UB9qtKxWZvyvtM": AccountTypeSuperGrokLite,  // SuperGrok Lite ($10/月 / $100/年)
	"prod_RilhxLgoA7h5ri": AccountTypeSuperGrok,      // SuperGrok ($30/月)
	"prod_Sdnv8D1IKHvyAs": AccountTypeSuperGrokHeavy, // SuperGrok Heavy ($300/月 / $3000/年)
}

// TierFromStripeProduct 由 stripe productId 反查我们的 account_type。
// 未知 productId → ""，调用方需要回退到 tier 字段 / quota 推断。
func TierFromStripeProduct(productID string) string {
	if t, ok := stripeProductToTier[strings.TrimSpace(productID)]; ok {
		return t
	}
	return ""
}

// 探测错误类型 — 调用方据此决定是计入 fail_count 还是直接禁用。
var (
	// ErrEmptyToken sso 为空。
	ErrEmptyToken = errors.New("grokrefresh: 缺少 sso token")
	// ErrTokenExpired HTTP 401（cookie 已失效，需要重登）。
	ErrTokenExpired = errors.New("grokrefresh: sso 已失效（401）")
	// ErrTokenForbidden HTTP 403（被风控 / 账号禁用）。
	ErrTokenForbidden = errors.New("grokrefresh: 账号被风控（403）")
	// ErrTransient 其他可恢复的错误（5xx / 超时 / 网络）。
	ErrTransient = errors.New("grokrefresh: 临时失败")
)

// Options 单次探测可调项。
type Options struct {
	ProxyURL    string        // http://user:pass@host:port，留空走直连
	Timeout     time.Duration // 默认 25s
	UserAgent   string        // 默认 defaultUserAgent；环境变量 KLEIN_GROK_USER_AGENT 优先
	CFClearance string        // 写入 Cookie；空时尝试读 KLEIN_GROK_CF_CLEARANCE
	ExtraCookie string        // 追加进 Cookie；空时尝试读 KLEIN_GROK_CF_COOKIES
	ModelName   string        // 默认 DefaultModelName
}

// Profile rate-limits 解析结果。
type Profile struct {
	AccountType      string // free / super_grok / super_grok_heavy / unknown
	RemainingQueries float64
	TotalQueries     float64
	WindowSeconds    int
	RawTier          string // 原始 totalQueries 数值，便于日志 / 反推
	StatusCode       int    // HTTP 状态码（成功时填 200）
}

// Probe 用 sso 调用 /rest/rate-limits，返回 Profile。
//
// sso 可以是裸 token，也可以是带 'sso=' 前缀的 cookie 段；都会被规范化。
//
// 失败错误请用 errors.Is 比较 ErrTokenExpired / ErrTokenForbidden / ErrTransient。
func Probe(ctx context.Context, sso string, opt Options) (*Profile, error) {
	token := normalizeToken(sso)
	if token == "" {
		return nil, ErrEmptyToken
	}
	opt.applyDefaults()

	body, _ := json.Marshal(map[string]any{
		"requestKind": "DEFAULT",
		"modelName":   opt.ModelName,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, RateLimitsURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	setHeaders(req, token, opt)

	client, err := outbound.NewClient(outbound.Options{
		Timeout: opt.Timeout,
		Mode:    outbound.ModeUTLS,
		Profile: outbound.ProfileChrome,
		ProxyURL: opt.ProxyURL,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: build client: %v", ErrTransient, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	raw, rerr := readDecompressedBody(resp, 64*1024)
	if rerr != nil {
		return nil, fmt.Errorf("%w: 解压响应失败：%v", ErrTransient, rerr)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// 继续解析
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %s", ErrTokenExpired, sanitizedSnippet(raw, 200))
	case http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s", ErrTokenForbidden, sanitizedSnippet(raw, 200))
	default:
		return nil, fmt.Errorf("%w: HTTP %d %s", ErrTransient, resp.StatusCode, sanitizedSnippet(raw, 200))
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("%w: 响应非 JSON：%s", ErrTransient, sanitizedSnippet(raw, 200))
	}

	remaining := pickFloat(data, "remainingTokens", "remainingQueries")
	total := pickFloat(data, "totalTokens", "totalQueries")
	if total == 0 && remaining > 0 {
		// 后端有时只回 remaining，没 total — 兜底当成 total = remaining
		total = remaining
	}
	windowSec := int(pickFloat(data, "windowSizeSeconds"))

	prof := &Profile{
		RemainingQueries: remaining,
		TotalQueries:     total,
		WindowSeconds:    windowSec,
		StatusCode:       resp.StatusCode,
		RawTier:          fmt.Sprintf("%g", total),
		AccountType:      inferAccountType(int(total)),
	}
	return prof, nil
}

// SubscriptionProfile /rest/subscriptions 响应解析结果。
//
// grok.com 没有公开订阅 schema，字段名我们用多 key 兜底解析。Raw 保留原始 JSON，
// 字段名后续若变化时可以从 Raw 里再补字段，不用动调用方。
type SubscriptionProfile struct {
	// Tier 订阅档位：free / super_grok / super_grok_heavy / team / unknown。
	// 取 raw 里第一个非空的：subscriptionTier / tier / plan / planName / accountType / type / level。
	Tier string
	// Status 订阅状态：active / canceled / past_due / trialing 等。
	// 取 raw 里第一个非空的：status / subscriptionStatus / state。
	Status string
	// ExpiresAt 订阅"下一次到期"的时间点。空表示无信号。
	// 取 raw 里第一个能解析出时间的：currentPeriodEnd / current_period_end / expirationDate /
	// expiresAt / expiresAtMs / expires_at / endsAt / nextBillingDate / nextRenewalDate。
	// 支持 unix 秒/毫秒数字，也支持 RFC3339 字符串。
	ExpiresAt *time.Time
	// BillingInterval 订阅周期：monthly / yearly / unknown。
	// grok 返回 "BILLING_INTERVAL_MONTHLY" / "BILLING_INTERVAL_YEARLY"，已归一。
	// 用于 stale 数据自动外推：grok subscription endpoint 经常返回上一周期
	// 快照，调用方可以用 billingPeriodEnd + interval 推到当前周期。
	BillingInterval string
	// CancelAtPeriodEnd 用户已点退订但仍在 period 内能用 — 显示用。
	CancelAtPeriodEnd bool
	// ProductID stripe.productId — 比 tier 字段更精确，能区分 Lite/SuperGrok/Heavy。
	// 例：prod_RilhxLgoA7h5ri = SuperGrok / prod_Sdnv8D1IKHvyAs = Heavy /
	//     prod_UB9qtKxWZvyvtM = SuperGrok Lite。未知 productId → 空串。
	ProductID string
	// CreateTime 订阅创建时间（来自 createTime 字段）。
	// 关键用途：和 BillingInterval 一起推算"应有的"当前周期 end，校验
	// grok 给的 BillingPeriodEnd 是否 stale。
	CreateTime *time.Time
	// ModTime 订阅最后修改时间（来自 modTime 字段）。
	// 若 ModTime > BillingPeriodEnd 也是 stale 信号（账号还在被修改，但
	// billing 字段没跟上）。
	ModTime *time.Time
	// StatusCode HTTP 状态。
	StatusCode int
	// Raw 原始 JSON（数组顶层时取第一项；对象顶层时直接取）。Debug / 字段名兼容用。
	Raw map[string]any
}

const (
	BillingIntervalMonthly = "monthly"
	BillingIntervalYearly  = "yearly"
)

// IsTrialPeriod 判断这是不是"短周期试用"订阅。
//
// grok 的"3 天试用"特征是 billingPeriodEnd - createTime 远小于 interval period
// （monthly=30 天 / yearly=365 天），所以纯用时长比例就能可靠反推。
//   - monthly 订阅但首周期 < 20 天 → 试用号（典型值 3 天）
//   - yearly  订阅但首周期 < 180 天 → 试用号
//
// 调用方应该用这个判别决定：试用号不能用 createTime + interval 外推到下一周期
// （那会把 3 天试用错误变成 1 个月到期，UI 严重误导运营）。
func IsTrialPeriod(createTime, periodEnd time.Time, interval string) bool {
	if createTime.IsZero() || periodEnd.IsZero() {
		return false
	}
	d := periodEnd.Sub(createTime)
	switch interval {
	case BillingIntervalMonthly:
		return d > 0 && d < 20*24*time.Hour
	case BillingIntervalYearly:
		return d > 0 && d < 180*24*time.Hour
	}
	return false
}

// ProbeSubscription 调 GET /rest/subscriptions 拉真订阅信息。
//
// 错误语义与 Probe 保持一致：ErrTokenExpired / ErrTokenForbidden / ErrTransient。
// 200 但 body 为空数组（账号无订阅，free 用户）→ 返回 Tier="free" + StatusCode=200。
func ProbeSubscription(ctx context.Context, sso string, opt Options) (*SubscriptionProfile, error) {
	token := normalizeToken(sso)
	if token == "" {
		return nil, ErrEmptyToken
	}
	opt.applyDefaults()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SubscriptionsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	setHeaders(req, token, opt)
	// /rest/subscriptions 是 GET，挪掉 Content-Type 避免被 CF 视作异常。
	req.Header.Del("Content-Type")
	// Referer 改成个人中心 — 与浏览器行为一致，CF 风控更友好。
	req.Header.Set("Referer", "https://grok.com/?_s=account")

	client, err := outbound.NewClient(outbound.Options{
		Timeout:  opt.Timeout,
		Mode:     outbound.ModeUTLS,
		Profile:  outbound.ProfileChrome,
		ProxyURL: opt.ProxyURL,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: build client: %v", ErrTransient, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	raw, rerr := readDecompressedBody(resp, 64*1024)
	if rerr != nil {
		return nil, fmt.Errorf("%w: 解压响应失败：%v", ErrTransient, rerr)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// 继续解析
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %s", ErrTokenExpired, sanitizedSnippet(raw, 200))
	case http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s", ErrTokenForbidden, sanitizedSnippet(raw, 200))
	default:
		return nil, fmt.Errorf("%w: HTTP %d %s", ErrTransient, resp.StatusCode, sanitizedSnippet(raw, 200))
	}

	out := &SubscriptionProfile{StatusCode: resp.StatusCode}
	// 顶层可能是对象，也可能是数组（"subscriptions": [...]）。两种都试。
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("%w: 响应非 JSON：%s", ErrTransient, sanitizedSnippet(raw, 200))
	}
	first := flattenSubscription(data)
	if first == nil {
		// 空数组 / 空对象 → 没有有效订阅，按 free 处理（与 grok 网页 UI 一致）。
		out.Tier = AccountTypeFree
		out.Raw = map[string]any{}
		return out, nil
	}
	out.Raw = first
	out.Tier = normalizeSubscriptionTier(pickString(first,
		"subscriptionTier", "tier", "plan", "planName", "accountType", "type", "level"))
	out.Status = normalizeSubscriptionStatus(pickString(first,
		"status", "subscriptionStatus", "state"))
	out.CancelAtPeriodEnd = pickBool(first,
		"cancelAtPeriodEnd", "cancel_at_period_end", "willCancel")
	out.ExpiresAt = pickTime(first,
		// 2026-05 抓 HAR 看到 grok.com 真实 schema 用的是 billingPeriodEnd（驼峰）。
		// 其他候选保留作 schema 演进时的兜底。
		"billingPeriodEnd", "billing_period_end",
		"currentPeriodEnd", "current_period_end",
		"expirationDate", "expiresAt", "expiresAtMs", "expires_at",
		"endsAt", "endDate", "nextBillingDate", "nextRenewalDate",
		"renewsAt", "validUntil")
	out.BillingInterval = normalizeBillingInterval(pickString(first,
		"billingInterval", "billing_interval", "interval", "subscriptionType", "subscription_type"))
	out.CreateTime = pickTime(first, "createTime", "create_time", "createdAt", "created_at")
	out.ModTime = pickTime(first, "modTime", "mod_time", "modifiedAt", "modified_at", "updatedAt", "updated_at")
	// stripe 子对象里有更精确的 productId + subscriptionType。
	// 顶层兜底取 productId/product_id（grok schema 演进时可能直接平铺到顶层）。
	if stripe, ok := first["stripe"].(map[string]any); ok {
		out.ProductID = pickString(stripe, "productId", "product_id")
		if out.BillingInterval == "" {
			out.BillingInterval = normalizeBillingInterval(pickString(stripe,
				"subscriptionType", "subscription_type", "interval"))
		}
	}
	if out.ProductID == "" {
		out.ProductID = pickString(first, "productId", "product_id")
	}
	// Tier 兜底：grok 的 tier 字段只能区分 Pro/Heavy（不区分 Lite），用 stripe
	// productId 反查一遍能精确识别 Lite。
	if mapped := TierFromStripeProduct(out.ProductID); mapped != "" {
		out.Tier = mapped
	}
	return out, nil
}

// normalizeBillingInterval 把 grok 的 enum 字面值（"BILLING_INTERVAL_MONTHLY"
// / "BILLING_INTERVAL_YEARLY"）归一成短形式。
func normalizeBillingInterval(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	for _, prefix := range []string{"billing_interval_", "billinginterval_"} {
		t = strings.TrimPrefix(t, prefix)
	}
	switch t {
	case "monthly", "month":
		return BillingIntervalMonthly
	case "yearly", "year", "annual", "annually":
		return BillingIntervalYearly
	}
	return ""
}

// flattenSubscription 把多种可能的顶层结构压平成"最有意义的"订阅记录。
//
// 支持的形态：
//   - map[string]any 顶层，且含 "subscriptions": [...] / "data": [...] / "items": [...]
//   - map[string]any 顶层但不嵌套（单条订阅 flat 形态）
//   - []any 顶层
//
// **多条订阅挑选规则**（关键 — 升级账号会同时存在多条 subscription）：
//
// 用户先开 SuperGrok 试用 → 升级到 Heavy，grok 后台会保留**两条** subscription：
//   - 旧 trial: status=INACTIVE, productId=SuperGrok
//   - 新订阅: status=ACTIVE,   productId=Heavy, createTime 更晚
//
// 如果按数组第 0 项盲取，常会拿到 INACTIVE 的旧 trial，导致 UI 错误显示
// "试用已结束" / 旧产品。挑选优先级：
//   1. status ∈ {active, trialing, past_due} 的优先（账号正在用的订阅）
//   2. 同状态下 createTime 更晚的优先（最近升级的）
//   3. 实在都是终态（inactive/canceled）→ createTime 最晚的（最近的历史）
//
// 返回 nil 表示没有任何记录。
func flattenSubscription(v any) map[string]any {
	items := collectSubscriptionItems(v)
	if len(items) == 0 {
		return nil
	}
	if len(items) == 1 {
		return items[0]
	}
	return pickBestSubscription(items)
}

// collectSubscriptionItems 把所有可能形态压平成 []map[string]any。
// 与 flattenSubscription 解耦，便于挑选逻辑独立测试。
func collectSubscriptionItems(v any) []map[string]any {
	switch t := v.(type) {
	case map[string]any:
		for _, key := range []string{"subscriptions", "data", "items", "list", "results"} {
			if arrRaw, has := t[key]; has {
				return toMapSlice(arrRaw)
			}
		}
		// flat 单条
		if len(t) == 0 {
			return nil
		}
		return []map[string]any{t}
	case []any:
		return toMapSlice(v)
	}
	return nil
}

func toMapSlice(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok && len(m) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// pickBestSubscription 在多条订阅里挑一条最能代表"账号当前状态"的。
//
// 评分规则（高分胜出，同分则 createTime 晚者胜）：
//   active   → 100
//   trialing → 90
//   past_due → 80（账号还能用但欠费）
//   其他     → 50（inactive / canceled / 未知都算次要历史）
//   空状态   → 30
func pickBestSubscription(items []map[string]any) map[string]any {
	scoreOf := func(m map[string]any) int {
		s := normalizeSubscriptionStatus(pickString(m,
			"status", "subscriptionStatus", "state"))
		switch s {
		case SubStatusActive:
			return 100
		case SubStatusTrialing:
			return 90
		case SubStatusPastDue:
			return 80
		case "":
			return 30
		default:
			return 50
		}
	}
	createOf := func(m map[string]any) time.Time {
		if t := pickTime(m, "createTime", "create_time", "createdAt", "created_at"); t != nil {
			return *t
		}
		return time.Time{}
	}
	best := items[0]
	bestScore := scoreOf(best)
	bestCreate := createOf(best)
	for _, m := range items[1:] {
		s := scoreOf(m)
		c := createOf(m)
		if s > bestScore || (s == bestScore && c.After(bestCreate)) {
			best, bestScore, bestCreate = m, s, c
		}
	}
	return best
}

// normalizeSubscriptionStatus 把 grok 风格的 enum 字面值（"subscription_status_active"
// / "SUBSCRIPTION_STATUS_PAST_DUE"）归一成短形式（"active" / "past_due"）。
//
// 历史前缀有 "subscription_status_" / "subscriptionstatus_" 两种 — 都去掉。
func normalizeSubscriptionStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, prefix := range []string{"subscription_status_", "subscriptionstatus_"} {
		s = strings.TrimPrefix(s, prefix)
	}
	return s
}

// normalizeSubscriptionTier 把订阅 tier 字面值（可能是 "Super Grok"、"super_grok"、
// "SUPER_GROK_HEAVY"、"basic"、"team" 等）归一到内部 4 档 + unknown。
func normalizeSubscriptionTier(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.ReplaceAll(t, " ", "_")
	t = strings.ReplaceAll(t, "-", "_")
	for _, prefix := range []string{"subscription_tier_", "subscriptiontier_"} {
		t = strings.TrimPrefix(t, prefix)
	}
	switch t {
	case "", "free", "basic":
		return AccountTypeFree
	case "heavy", "super_grok_heavy", "supergrok_heavy", "grok_heavy",
		"grok_pro_heavy", "pro_heavy":
		return AccountTypeSuperGrokHeavy
	case "lite", "super_grok_lite", "supergrok_lite", "grok_lite",
		"grok_pro_lite", "pro_lite":
		return AccountTypeSuperGrokLite
	case "super", "super_grok", "supergrok", "grok_super",
		"grok_pro", "pro", "team":
		return AccountTypeSuperGrok
	}
	if strings.Contains(t, "heavy") {
		return AccountTypeSuperGrokHeavy
	}
	if strings.Contains(t, "lite") {
		return AccountTypeSuperGrokLite
	}
	if strings.Contains(t, "super") || strings.Contains(t, "pro") || strings.Contains(t, "premium") {
		return AccountTypeSuperGrok
	}
	return AccountTypeUnknown
}

func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

func pickBool(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch x := v.(type) {
			case bool:
				return x
			case string:
				return strings.EqualFold(strings.TrimSpace(x), "true")
			}
		}
	}
	return false
}

// pickTime 容错解析时间：支持 RFC3339 / ISO8601 字符串、unix 秒（10 位）、unix 毫秒（13 位）。
func pickTime(m map[string]any, keys ...string) *time.Time {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		if t := parseTimeAny(v); t != nil {
			return t
		}
	}
	return nil
}

func parseTimeAny(v any) *time.Time {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, s); err == nil {
				u := t.UTC()
				return &u
			}
		}
	case float64:
		return unixSecOrMs(int64(x))
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return unixSecOrMs(n)
		}
	case int64:
		return unixSecOrMs(x)
	case int:
		return unixSecOrMs(int64(x))
	}
	return nil
}

// unixSecOrMs 数值 ≥ 1e12 视为毫秒，否则秒。
func unixSecOrMs(n int64) *time.Time {
	if n <= 0 {
		return nil
	}
	var t time.Time
	if n >= 1_000_000_000_000 {
		t = time.UnixMilli(n).UTC()
	} else {
		t = time.Unix(n, 0).UTC()
	}
	return &t
}

// inferAccountType 仅依据 auto 模式 totalQueries 推断类型。
//
// 与参考实现 _infer_pool_name_from_windows 一致：
//
//	150 → super_grok_heavy
//	 50 → super_grok
//	 20 → free
//	其他 → unknown（保守，避免误判）
func inferAccountType(total int) string {
	switch {
	case total >= 150:
		return AccountTypeSuperGrokHeavy
	case total >= 50:
		return AccountTypeSuperGrok
	case total >= 20:
		return AccountTypeFree
	case total > 0:
		return AccountTypeFree // 未知更小窗口也按 free 计
	default:
		return AccountTypeUnknown
	}
}

// applyDefaults 填充零值。
func (o *Options) applyDefaults() {
	if o.Timeout <= 0 {
		o.Timeout = 25 * time.Second
	}
	if strings.TrimSpace(o.UserAgent) == "" {
		o.UserAgent = strings.TrimSpace(os.Getenv("KLEIN_GROK_USER_AGENT"))
		if o.UserAgent == "" {
			o.UserAgent = defaultUserAgent
		}
	}
	if strings.TrimSpace(o.CFClearance) == "" {
		o.CFClearance = strings.TrimSpace(os.Getenv("KLEIN_GROK_CF_CLEARANCE"))
	}
	if strings.TrimSpace(o.ExtraCookie) == "" {
		o.ExtraCookie = strings.TrimSpace(os.Getenv("KLEIN_GROK_CF_COOKIES"))
	}
	if strings.TrimSpace(o.ModelName) == "" {
		o.ModelName = DefaultModelName
	}
}

// setHeaders 与 web_client 的 setGrokHeaders 等效，但保持包独立，不复用其内部符号。
func setHeaders(req *http.Request, token string, opt Options) {
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Baggage", "sentry-environment=production,sentry-release=d6add6fb0460641fd482d767a335ef72b9b6abb8,sentry-public_key=b311e0f2690c81f25e2c4cf6d4f7ce1c")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", buildCookie(token, opt))
	req.Header.Set("Origin", "https://grok.com")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Priority", "u=1, i")
	req.Header.Set("Referer", "https://grok.com/")
	req.Header.Set("User-Agent", opt.UserAgent)
	req.Header.Set("Sec-Ch-Ua", secCHUA(opt.UserAgent))
	req.Header.Set("Sec-Ch-Ua-Arch", "x86")
	req.Header.Set("Sec-Ch-Ua-Bitness", "64")
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Model", "")
	req.Header.Set("Sec-Ch-Ua-Platform", secPlatform(opt.UserAgent))
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Statsig-ID", randomStatsigID())
	req.Header.Set("X-XAI-Request-ID", uuid.NewString())
}

func buildCookie(token string, opt Options) string {
	cookie := "sso=" + token + "; sso-rw=" + token
	if opt.CFClearance != "" {
		cookie += "; cf_clearance=" + opt.CFClearance
	}
	if extra := strings.TrimSpace(opt.ExtraCookie); extra != "" {
		cookie += "; " + extra
	}
	return cookie
}

func normalizeToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "=") {
		return s
	}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "sso=") {
			return strings.TrimPrefix(part, "sso=")
		}
	}
	return s
}

func pickFloat(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case float64:
			return x
		case int:
			return float64(x)
		case int64:
			return float64(x)
		case json.Number:
			f, _ := x.Float64()
			return f
		}
	}
	return 0
}

func secCHUA(ua string) string {
	v := "136"
	if m := regexp.MustCompile(`(?:Chrome|Chromium)/(\d+)`).FindStringSubmatch(ua); len(m) == 2 {
		v = m[1]
	}
	return fmt.Sprintf(`"Google Chrome";v="%s", "Chromium";v="%s", "Not(A:Brand";v="24"`, v, v)
}

func secPlatform(ua string) string {
	ua = strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "windows"):
		return `"Windows"`
	case strings.Contains(ua, "mac os"):
		return `"macOS"`
	case strings.Contains(ua, "linux"):
		return `"Linux"`
	default:
		return `"Windows"`
	}
}

// randomStatsigID 简化版 — 与服务端的强校验匹配难度高，用一个 base64 随机 32 字节通常能过。
//
// 真实生产里 web_client.go 的 grokStatsigID 有更精确的实现（与 statsig 协议匹配），
// 这里仅用于 rate-limits 这一只读探测，简化即可。
func randomStatsigID() string {
	buf := make([]byte, 16)
	_, _ = rand.New(rand.NewSource(time.Now().UnixNano())).Read(buf)
	h := sha1.Sum(buf)
	return base64.StdEncoding.EncodeToString(h[:])
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// sanitizedSnippet 把响应体转成可安全写入 utf8mb4 / 展示给前端的简短字符串：
// 1) 校验 utf-8，丢弃所有控制字符 / 非可打印字节，保留 ASCII + 常见中日韩
// 2) 至多 n 字符
func sanitizedSnippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if r == utf8RuneError() {
			continue
		}
		if r == '\n' || r == '\t' {
			sb.WriteRune(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if r > 0x10FFFF {
			continue
		}
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		return fmt.Sprintf("二进制响应 %d 字节", len(b))
	}
	if len(out) > n {
		return out[:n] + "…"
	}
	return out
}

func utf8RuneError() rune { return '\uFFFD' }

// readDecompressedBody 兼容 gzip / deflate / 明文响应。utls 自定义传输层不会自动解压。
func readDecompressedBody(resp *http.Response, max int64) ([]byte, error) {
	limited := io.LimitReader(resp.Body, max)
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch enc {
	case "gzip":
		gr, err := gzip.NewReader(limited)
		if err != nil {
			return io.ReadAll(limited)
		}
		defer gr.Close()
		return io.ReadAll(io.LimitReader(gr, max*8))
	case "deflate":
		fr := flate.NewReader(limited)
		defer fr.Close()
		return io.ReadAll(io.LimitReader(fr, max*8))
	default:
		raw, err := io.ReadAll(limited)
		if err != nil {
			return raw, err
		}
		// 若 server 没设 header 但实际是 gzip，按魔数嗅探
		if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
			gr, gerr := gzip.NewReader(bytes.NewReader(raw))
			if gerr == nil {
				defer gr.Close()
				if dec, derr := io.ReadAll(io.LimitReader(gr, max*8)); derr == nil {
					return dec, nil
				}
			}
		}
		return raw, nil
	}
}
