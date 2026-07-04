package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/errcode"
)

// 系统配置 key 常量。
const (
	SettingProxyGlobalEnabled = "proxy.global_enabled"
	SettingProxyGlobalID      = "proxy.global_id"
	// proxy.adobe_enabled / proxy.adobe_id：Adobe Firefly 出图链路专用代理。
	// 配置后会绕过 global proxy 直接生效 — 即"adobe 走 A 代理，gpt/grok 走全局或直连"
	// 这种场景。值类型与 global 一致：bool + uint64(proxy.id)。
	SettingProxyAdobeEnabled             = "proxy.adobe_enabled"
	SettingProxyAdobeID                  = "proxy.adobe_id"
	SettingOAuthRefreshHours             = "oauth.refresh_before_hours"
	SettingOAuthOpenAIClientID           = "oauth.openai_client_id"
	SettingOAuthOpenAITokenURL           = "oauth.openai_token_url"
	SettingRetryMaxAttempts              = "retry.max_attempts"
	SettingRetryBaseDelayMs              = "retry.base_delay_ms"
	SettingRetryTimeoutSeconds           = "retry.timeout_seconds"
	SettingCircuitFailures               = "tolerance.circuit_failures"
	SettingCircuitCooldown               = "tolerance.circuit_cooldown_seconds"
	SettingFreeInitialPoints             = "billing.free_initial_points"
	SettingOpenAIMaxPendingPerKey        = "openai.max_pending_per_key"
	SettingOpenAIMaxRunningPerKey        = "openai.max_running_per_key"
	SettingOpenAISyncWaitMax             = "openai.sync_wait_max_concurrent"
	SettingOpenAIAdmissionMaxInflight    = "openai.admission_max_inflight"
	SettingOpenAIPollRatePerMin          = "openai.poll_rate_per_minute"
	SettingSafetyKeywordBlocklistEnabled = "safety.keyword_blocklist.enabled"
	SettingSafetyKeywordBlocklistWords   = "safety.keyword_blocklist.words"
	SettingSafetyKeywordBlocklistMode    = "safety.keyword_blocklist.match_mode"
	SettingGrokCFEnabled                 = "grok.cf.enabled"
	SettingGrokCFSolverURL               = "grok.cf.flaresolverr_url"
	SettingGrokCFRefreshSec              = "grok.cf.refresh_interval_seconds"
	SettingGrokCFTimeoutSec              = "grok.cf.timeout_seconds"
	SettingGrokCFCookies                 = "grok.cf.cookies"
	SettingGrokCFClearance               = "grok.cf.clearance"
	SettingGrokCFUserAgent               = "grok.cf.user_agent"
	SettingGrokCFBrowser                 = "grok.cf.browser"
	SettingGrokCFLastError               = "grok.cf.last_error"
	SettingGrokCFLastRefreshAt           = "grok.cf.last_refresh_at"
	// Adobe 提交通道：clio（Firefly 网页，默认）| psweb（Photoshop Web 入口）
	SettingAdobeSubmitMode = "adobe.submit_mode"
	// 邮箱配置（号池注册流程统一收件源）
	SettingMailDefaultBackend = "mail.default_backend"
	SettingMailPollTimeoutSec = "mail.poll_timeout_sec"
	SettingMailMaxFailures    = "mail.max_failures"
	SettingMailOutlook        = "mail.outlook"
	SettingMailTempmail       = "mail.tempmail"
	SettingMailCF             = "mail.cf"
	// 验证码服务（号池注册时人机校验求解）
	//
	// 旧版单组配置（仍保留作为兜底；当 arkose.* / turnstile.* 任意字段缺失时回落到此）
	SettingCaptchaProvider = "captcha.provider"
	SettingCaptchaAPIKey   = "captcha.api_key"
	SettingCaptchaEndpoint = "captcha.endpoint"
	// 新版按用途分两组：Arkose（FunCaptcha，Banana / GPT） + Turnstile（Grok）
	SettingCaptchaArkoseProvider    = "captcha.arkose.provider"
	SettingCaptchaArkoseAPIKey      = "captcha.arkose.api_key"
	SettingCaptchaArkoseEndpoint    = "captcha.arkose.endpoint"
	SettingCaptchaTurnstileProvider = "captcha.turnstile.provider"
	SettingCaptchaTurnstileAPIKey   = "captcha.turnstile.api_key"
	SettingCaptchaTurnstileEndpoint = "captcha.turnstile.endpoint"
	// 备用服务商列表（JSON 数组，按顺序 fallback；主配置失败时依次尝试）
	// 格式：[{"provider":"nopecha","api_key":"...","endpoint":""},
	//       {"provider":"yescaptcha","api_key":"...","endpoint":""}]
	SettingCaptchaArkoseFallbacks    = "captcha.arkose.fallbacks"
	SettingCaptchaTurnstileFallbacks = "captcha.turnstile.fallbacks"
	// 号池注册执行池
	SettingRegisterConcurrency = "register.worker_concurrency"
	// Plus 升级（GoPay + 云手机）
	SettingPlusUpgradeEnabled          = "plus_upgrade.enabled"
	SettingPlusUpgradePythonPath       = "plus_upgrade.python_path"
	SettingPlusUpgradeGopayScriptPath  = "plus_upgrade.gopay_script_path"
	SettingPlusUpgradeTaskConcurrency  = "plus_upgrade.task_concurrency"
	SettingPlusUpgradePerWalletQuota   = "plus_upgrade.per_wallet_quota"
	SettingPlusUpgradeWalletCooldown   = "plus_upgrade.wallet_cooldown_min"
	SettingPlusUpgradeOTPInterval      = "plus_upgrade.otp_poll_interval_s"
	SettingPlusUpgradeOTPTimeout       = "plus_upgrade.otp_timeout_s"
	SettingPlusUpgradeCSProxyStrategy  = "plus_upgrade.cs_proxy_strategy"
	SettingPlusUpgradeExtProxyStrategy = "plus_upgrade.ext_proxy_strategy"
	SettingPlusUpgradeCSProxyCountry   = "plus_upgrade.cs_proxy_country"
	SettingPlusUpgradeExtProxyCountry  = "plus_upgrade.ext_proxy_country"
	// Plus 开通成功后是否在云手机内自动操作 GoPay「已连接应用」移除 OpenAI（默认 true）
	SettingPlusUpgradeAutoGopayUnlink = "plus_upgrade.auto_gopay_unlink"
	// GeeLark 云手机 OpenAPI
	SettingGeeLarkAPIBase = "geelark.api_base"
)

// MailBackend 收件后端枚举。
const (
	MailBackendOutlookIMAP  = "outlook_imap"
	MailBackendOutlookGraph = "outlook_graph"
	MailBackendTempmail     = "tempmail"
	MailBackendCF           = "cf"
)

// SystemConfigService 通用系统配置 KV 服务，带 30s 内存缓存。
type SystemConfigService struct {
	repo *repo.SystemConfigRepo

	mu     sync.RWMutex
	cache  map[string]string
	loaded time.Time
	ttl    time.Duration

	hookMu      sync.Mutex
	updateHooks []func(values map[string]any)
}

// NewSystemConfigService 构造。ttl<=0 时默认 30s。
func NewSystemConfigService(r *repo.SystemConfigRepo) *SystemConfigService {
	return &SystemConfigService{repo: r, cache: map[string]string{}, ttl: 30 * time.Second}
}

// OnUpdate 注册一个回调：每次 UpsertMany 成功后会被同步调用，参数即本次写入的原始 values。
// 用于让在线服务跟随系统配置变更（例如调整 worker pool 并发）。
func (s *SystemConfigService) OnUpdate(fn func(values map[string]any)) {
	if fn == nil {
		return
	}
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	s.updateHooks = append(s.updateHooks, fn)
}

func (s *SystemConfigService) fireUpdateHooks(values map[string]any) {
	s.hookMu.Lock()
	hooks := append([]func(values map[string]any){}, s.updateHooks...)
	s.hookMu.Unlock()
	for _, h := range hooks {
		func() {
			defer func() { _ = recover() }()
			h(values)
		}()
	}
}

// GetAll 全部 KV（已 JSON 解码）。
func (s *SystemConfigService) GetAll(ctx context.Context) (map[string]any, error) {
	rows, err := s.repo.GetAll(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	out := make(map[string]any, len(rows))
	for _, r := range rows {
		var v any
		_ = json.Unmarshal([]byte(r.Value), &v)
		out[r.Key] = v
	}
	return out, nil
}

// UpsertMany 批量更新。
// values 中每个值会先 JSON 序列化再写入。
func (s *SystemConfigService) UpsertMany(ctx context.Context, values map[string]any, updatedBy uint64) error {
	if len(values) == 0 {
		return nil
	}
	kvs := make(map[string]string, len(values))
	for k, v := range values {
		raw, err := json.Marshal(v)
		if err != nil {
			return errcode.InvalidParam.Wrap(err)
		}
		kvs[k] = string(raw)
	}
	uid := updatedBy
	if err := s.repo.UpsertMany(ctx, kvs, &uid); err != nil {
		return errcode.DBError.Wrap(err)
	}
	s.invalidate()
	s.fireUpdateHooks(values)
	return nil
}

// GetJSON 读 JSON 对象配置；返回 nil 表示 key 不存在或值非对象。
func (s *SystemConfigService) GetJSON(ctx context.Context, key string) map[string]any {
	raw, ok := s.getRaw(ctx, key)
	if !ok {
		return nil
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

// GetString 读字符串配置。fallback 为默认。
func (s *SystemConfigService) GetString(ctx context.Context, key, fallback string) string {
	raw, ok := s.getRaw(ctx, key)
	if !ok {
		return fallback
	}
	var v string
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		// 兼容字符串本身不带引号的旧值
		return strings.Trim(raw, "\"")
	}
	if v == "" {
		return fallback
	}
	return v
}

// GetInt 读 int64 配置。
func (s *SystemConfigService) GetInt(ctx context.Context, key string, fallback int64) int64 {
	raw, ok := s.getRaw(ctx, key)
	if !ok {
		return fallback
	}
	var v int64
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	if n, err := strconv.ParseInt(strings.Trim(raw, "\""), 10, 64); err == nil {
		return n
	}
	return fallback
}

// GetBool 读 bool 配置。
func (s *SystemConfigService) GetBool(ctx context.Context, key string, fallback bool) bool {
	raw, ok := s.getRaw(ctx, key)
	if !ok {
		return fallback
	}
	var v bool
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	switch strings.ToLower(strings.Trim(raw, "\"")) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return fallback
}

// GetUint64 读 uint64 配置。
func (s *SystemConfigService) GetUint64(ctx context.Context, key string, fallback uint64) uint64 {
	raw, ok := s.getRaw(ctx, key)
	if !ok {
		return fallback
	}
	var v uint64
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	if n, err := strconv.ParseUint(strings.Trim(raw, "\""), 10, 64); err == nil {
		return n
	}
	return fallback
}

// === 类型化便捷方法 ===

// GlobalProxyEnabled 是否启用全局代理。
func (s *SystemConfigService) GlobalProxyEnabled(ctx context.Context) bool {
	return s.GetBool(ctx, SettingProxyGlobalEnabled, false)
}

// GlobalProxyID 全局默认代理 ID（0 = 无）。
func (s *SystemConfigService) GlobalProxyID(ctx context.Context) uint64 {
	return s.GetUint64(ctx, SettingProxyGlobalID, 0)
}

// AdobeProxyEnabled Adobe Firefly 出图链路是否启用专用代理。
//
// 用于解决 Adobe 对部分服务器机房 IP 报 451 区域限制的问题。优先级高于 global proxy：
//
//	adobe 账号  → 先看 proxy.adobe_id（adobe_enabled=true 时） → fallback global → 直连
//	其它 provider → 不受 adobe 配置影响
func (s *SystemConfigService) AdobeProxyEnabled(ctx context.Context) bool {
	return s.GetBool(ctx, SettingProxyAdobeEnabled, false)
}

// AdobeProxyID Adobe Firefly 专用代理 ID（0 = 无）。
func (s *SystemConfigService) AdobeProxyID(ctx context.Context) uint64 {
	return s.GetUint64(ctx, SettingProxyAdobeID, 0)
}

// RefreshBeforeHours OAuth 提前刷新窗口（小时）。
func (s *SystemConfigService) RefreshBeforeHours(ctx context.Context) int64 {
	v := s.GetInt(ctx, SettingOAuthRefreshHours, 24)
	if v <= 0 {
		v = 24
	}
	if v > 168 {
		v = 168
	}
	return v
}

// OpenAIClientID Codex CLI 公开 client_id。
func (s *SystemConfigService) OpenAIClientID(ctx context.Context) string {
	return s.GetString(ctx, SettingOAuthOpenAIClientID, "app_EMoamEEZ73f0CkXaXp7hrann")
}

// OpenAITokenURL OAuth Token Endpoint。
func (s *SystemConfigService) OpenAITokenURL(ctx context.Context) string {
	return s.GetString(ctx, SettingOAuthOpenAITokenURL, "https://auth.openai.com/oauth/token")
}

func (s *SystemConfigService) RetryMaxAttempts(ctx context.Context) int {
	v := s.GetInt(ctx, SettingRetryMaxAttempts, 2)
	if v < 0 {
		v = 0
	}
	if v > 20 {
		v = 20
	}
	return int(v) + 1
}

func (s *SystemConfigService) RetryBaseDelay(ctx context.Context) time.Duration {
	v := s.GetInt(ctx, SettingRetryBaseDelayMs, 800)
	if v < 0 {
		v = 0
	}
	if v > 60000 {
		v = 60000
	}
	return time.Duration(v) * time.Millisecond
}

// RetryTimeout 单次 attempt 的硬超时。
//
// 设计要点：
//   - fallback 是任务自己声明的"合理上限"（图 5min / 视频 8min / gpt-image-2 10min），
//     代表 provider 单次正常完成需要的最大时间。
//   - 全局 retry.timeout_seconds 只允许**缩短**单次 attempt，或**最多放大到 fallback × 1.5**。
//     之前没有这层上限，运营把 retry.timeout_seconds 改成 1200/1800 时，单 task
//     会被撑到 (maxAttempts+adobe_fallback) × 1200s = 数十分钟才返回错误。
//   - 仍保留 3600s 物理上限做最后兜底（防止运营误填几小时）。
func (s *SystemConfigService) RetryTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = 5 * time.Minute
	}
	v := s.GetInt(ctx, SettingRetryTimeoutSeconds, 0)
	if v <= 0 {
		return fallback
	}
	cfg := time.Duration(v) * time.Second
	if cfg > 3600*time.Second {
		cfg = 3600 * time.Second
	}
	// 关键保护：不允许全局配置把任务默认 timeout 放大太多，避免出错时用户等几十分钟。
	maxAllowed := fallback + fallback/2 // fallback × 1.5
	if cfg > maxAllowed {
		cfg = maxAllowed
	}
	return cfg
}

// FreeInitialPoints 新用户注册成功后自动赠送的初始积分（×100 内部精度入库）。
// 0 表示不赠送。后台「新用户赠送积分」配置项的后端读取入口。
func (s *SystemConfigService) FreeInitialPoints(ctx context.Context) int64 {
	v := s.GetInt(ctx, SettingFreeInitialPoints, 0)
	if v < 0 {
		return 0
	}
	return v
}

// OpenAIMaxPendingPerKey 单个 API Key 最多允许积压的 pending 任务数。
// 0/负数表示不限制；正数才启用 per-key 上限。
func (s *SystemConfigService) OpenAIMaxPendingPerKey(ctx context.Context) int64 {
	v := s.GetInt(ctx, SettingOpenAIMaxPendingPerKey, 0)
	if v <= 0 {
		return 0
	}
	return v
}

// OpenAIMaxRunningPerKey 单个 API Key 最多允许同时 running 的任务数。
// 0/负数表示不限制；正数才启用 per-key 上限。
func (s *SystemConfigService) OpenAIMaxRunningPerKey(ctx context.Context) int64 {
	v := s.GetInt(ctx, SettingOpenAIMaxRunningPerKey, 0)
	if v <= 0 {
		return 0
	}
	return v
}

// OpenAIAdmissionMaxInflight 全站 pending+running 任务上限；达到后拒绝新建任务并返回 429。
// 未单独配置（<0）时沿用 openai.sync_wait_max_concurrent（默认 200）；显式 0 = 不限制。
func (s *SystemConfigService) OpenAIAdmissionMaxInflight(ctx context.Context) int64 {
	v := s.GetInt(ctx, SettingOpenAIAdmissionMaxInflight, -1)
	if v < 0 {
		return s.OpenAISyncWaitMaxConcurrent(ctx)
	}
	if v == 0 {
		return 0
	}
	return int64(v)
}

// AdobeSubmitMode Adobe 上游提交通道：
//   - "clio"  : Firefly 网页入口（x-api-key=clio-playground-web + x-nonce + x-arp-session-id），默认。
//   - "psweb" : Photoshop Web 入口（用 cookie 现铸 PSWebApp1 token + x-api-key=PSWebApp1，
//     去掉 x-nonce / x-arp-session-id），用于规避合作模型在 clio 入口的配额/授权差异。
//
// 任何非 "psweb" 的取值都归一为 "clio"。
func (s *SystemConfigService) AdobeSubmitMode(ctx context.Context) string {
	v := strings.ToLower(strings.TrimSpace(s.GetString(ctx, SettingAdobeSubmitMode, "clio")))
	if v == "psweb" {
		return "psweb"
	}
	return "clio"
}

// OpenAISyncWaitMaxConcurrent OpenAI sync wait(async=false) 同时阻塞等待上限。
// 超过该值时直接返回 task envelope，让客户端自行轮询，避免大量 DB 轮询。
func (s *SystemConfigService) OpenAISyncWaitMaxConcurrent(ctx context.Context) int64 {
	v := s.GetInt(ctx, SettingOpenAISyncWaitMax, 200)
	if v <= 0 {
		return 200
	}
	return v
}

// OpenAIPollRatePerMinute 查询任务状态 / 模型列表的 API Key 限流。
// 与创建任务分桶，避免 SDK 高频 poll 把 create quota 打满。默认 3000/min。
func (s *SystemConfigService) OpenAIPollRatePerMinute(ctx context.Context) int {
	v := s.GetInt(ctx, SettingOpenAIPollRatePerMin, 3000)
	if v <= 0 {
		return 3000
	}
	if v > 100000 {
		return 100000
	}
	return int(v)
}

// CircuitFailureThreshold 连续失败达到该次数后才把账号置为熔断。
func (s *SystemConfigService) CircuitFailureThreshold(ctx context.Context) int64 {
	v := s.GetInt(ctx, SettingCircuitFailures, 3)
	if v <= 0 {
		return 1
	}
	return v
}

// CircuitCooldownSeconds 账号熔断后的冷却秒数。
func (s *SystemConfigService) CircuitCooldownSeconds(ctx context.Context) int64 {
	v := s.GetInt(ctx, SettingCircuitCooldown, 300)
	if v < 0 {
		return 0
	}
	return v
}

// RegisterConcurrency 号池注册任务全局并发数。范围 [1, 256]，默认 5。
//
// 上限 256 是个软兜底，真正能跑多大并发取决于：
//   - 代理池规模 / 代理质量（同 IP 高频会被 OpenAI / Adobe / Grok 限频）
//   - 邮箱池规模（CF Worker 一次只能存这么多 address，IMAP / Graph 也会限 RPS）
//   - 验证码 / SMS 通道并发（有些 solver 单 key 限 30 并发）
//
// 经验值：
//   - 5–10  ：单代理供应商 + 单邮箱后端的"稳跑"档
//   - 16–32 ：多代理供应商（混 ISP / 住宅）+ catch-all 邮箱
//   - 64+   ：必须要有大代理池（>=200 IP）+ 多邮箱后端 + 高额度 captcha key
func (s *SystemConfigService) RegisterConcurrency(ctx context.Context) int {
	v := s.GetInt(ctx, SettingRegisterConcurrency, 5)
	if v <= 0 {
		return 5
	}
	if v > 256 {
		return 256
	}
	return int(v)
}

func (s *SystemConfigService) GrokCFEnabled(ctx context.Context) bool {
	return s.GetBool(ctx, SettingGrokCFEnabled, true)
}

func (s *SystemConfigService) GrokCFSolverURL(ctx context.Context) string {
	return strings.TrimRight(s.GetString(ctx, SettingGrokCFSolverURL, "http://flaresolverr:8191"), "/")
}

func (s *SystemConfigService) GrokCFRefreshInterval(ctx context.Context) time.Duration {
	v := s.GetInt(ctx, SettingGrokCFRefreshSec, 600)
	if v < 60 {
		v = 60
	}
	if v > 86400 {
		v = 86400
	}
	return time.Duration(v) * time.Second
}

func (s *SystemConfigService) GrokCFTimeout(ctx context.Context) time.Duration {
	v := s.GetInt(ctx, SettingGrokCFTimeoutSec, 90)
	if v < 30 {
		v = 30
	}
	if v > 300 {
		v = 300
	}
	return time.Duration(v) * time.Second
}

// MailDefaultBackend 默认收件后端。
func (s *SystemConfigService) MailDefaultBackend(ctx context.Context) string {
	v := s.GetString(ctx, SettingMailDefaultBackend, MailBackendOutlookGraph)
	switch v {
	case MailBackendOutlookIMAP, MailBackendOutlookGraph, MailBackendTempmail, MailBackendCF:
		return v
	default:
		return MailBackendOutlookGraph
	}
}

// MailPollTimeout 邮件等待超时。
func (s *SystemConfigService) MailPollTimeout(ctx context.Context) time.Duration {
	v := s.GetInt(ctx, SettingMailPollTimeoutSec, 180)
	if v < 30 {
		v = 30
	}
	if v > 1800 {
		v = 1800
	}
	return time.Duration(v) * time.Second
}

// MailMaxFailures 单条邮箱最大失败次数（达阈值标 failed）。
func (s *SystemConfigService) MailMaxFailures(ctx context.Context) int {
	v := s.GetInt(ctx, SettingMailMaxFailures, 3)
	if v <= 0 {
		return 1
	}
	if v > 100 {
		return 100
	}
	return int(v)
}

// CaptchaProvider 旧版兜底 provider（号池注册的默认人机求解器）。
func (s *SystemConfigService) CaptchaProvider(ctx context.Context) string {
	v := s.GetString(ctx, SettingCaptchaProvider, "capsolver")
	if v == "" {
		v = "capsolver"
	}
	return v
}

// CaptchaAPIKey 旧版兜底 solver 的 API key。
func (s *SystemConfigService) CaptchaAPIKey(ctx context.Context) string {
	return s.GetString(ctx, SettingCaptchaAPIKey, "")
}

// CaptchaEndpoint 旧版兜底 endpoint（留空使用默认）。
func (s *SystemConfigService) CaptchaEndpoint(ctx context.Context) string {
	return s.GetString(ctx, SettingCaptchaEndpoint, "")
}

// CaptchaArkose 返回 Arkose / FunCaptcha（Banana / GPT）专用打码配置；
// 任意字段缺失时回落到旧版 captcha.* 兜底。
func (s *SystemConfigService) CaptchaArkose(ctx context.Context) (provider, apiKey, endpoint string) {
	provider = strings.TrimSpace(s.GetString(ctx, SettingCaptchaArkoseProvider, ""))
	apiKey = strings.TrimSpace(s.GetString(ctx, SettingCaptchaArkoseAPIKey, ""))
	endpoint = strings.TrimSpace(s.GetString(ctx, SettingCaptchaArkoseEndpoint, ""))
	if provider == "" {
		provider = s.CaptchaProvider(ctx)
	}
	if apiKey == "" {
		apiKey = s.CaptchaAPIKey(ctx)
	}
	if endpoint == "" {
		endpoint = s.CaptchaEndpoint(ctx)
	}
	return
}

// CaptchaTurnstile 返回 Turnstile（Grok）专用打码配置；缺失时同样回落到 captcha.*。
func (s *SystemConfigService) CaptchaTurnstile(ctx context.Context) (provider, apiKey, endpoint string) {
	provider = strings.TrimSpace(s.GetString(ctx, SettingCaptchaTurnstileProvider, ""))
	apiKey = strings.TrimSpace(s.GetString(ctx, SettingCaptchaTurnstileAPIKey, ""))
	endpoint = strings.TrimSpace(s.GetString(ctx, SettingCaptchaTurnstileEndpoint, ""))
	if provider == "" {
		provider = s.CaptchaProvider(ctx)
	}
	if apiKey == "" {
		apiKey = s.CaptchaAPIKey(ctx)
	}
	if endpoint == "" {
		endpoint = s.CaptchaEndpoint(ctx)
	}
	return
}

// CaptchaProviderEntry 打码 fallback 列表的单项。endpoint 留空走 provider 默认。
type CaptchaProviderEntry struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
	Endpoint string `json:"endpoint,omitempty"`
}

// CaptchaArkoseFallbacks 读取 Arkose 备用服务商列表。
//
// 配置形如：[{"provider":"nopecha","api_key":"NP-..."}, ...]
// 解析失败 / 缺失返回 nil。无 api_key 的项目自动跳过；provider 大小写不敏感。
func (s *SystemConfigService) CaptchaArkoseFallbacks(ctx context.Context) []CaptchaProviderEntry {
	return s.readCaptchaProviderList(ctx, SettingCaptchaArkoseFallbacks)
}

// CaptchaTurnstileFallbacks 读取 Turnstile 备用服务商列表。语义同 Arkose。
func (s *SystemConfigService) CaptchaTurnstileFallbacks(ctx context.Context) []CaptchaProviderEntry {
	return s.readCaptchaProviderList(ctx, SettingCaptchaTurnstileFallbacks)
}

// readCaptchaProviderList 复用解析逻辑：JSON 数组 → 清洗 → 返回切片。
func (s *SystemConfigService) readCaptchaProviderList(ctx context.Context, key string) []CaptchaProviderEntry {
	raw, ok := s.getRaw(ctx, key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var entries []CaptchaProviderEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil
	}
	out := entries[:0]
	for _, e := range entries {
		e.Provider = strings.ToLower(strings.TrimSpace(e.Provider))
		e.APIKey = strings.TrimSpace(e.APIKey)
		e.Endpoint = strings.TrimSpace(e.Endpoint)
		if e.Provider == "" || e.Provider == "none" {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ─── Plus 升级相关 ───

// PlusUpgradeEnabled 是否启用 GPT 批量开 Plus。
func (s *SystemConfigService) PlusUpgradeEnabled(ctx context.Context) bool {
	return s.GetBool(ctx, SettingPlusUpgradeEnabled, true)
}

// PlusUpgradePythonPath gopay.py 解释器路径。
func (s *SystemConfigService) PlusUpgradePythonPath(ctx context.Context) string {
	return s.GetString(ctx, SettingPlusUpgradePythonPath, "/usr/local/bin/python3")
}

// PlusUpgradeGopayScriptPath 改造后的 gopay.py 脚本路径（容器内）。
func (s *SystemConfigService) PlusUpgradeGopayScriptPath(ctx context.Context) string {
	return s.GetString(ctx, SettingPlusUpgradeGopayScriptPath, "/app/scripts/gopay.py")
}

// PlusUpgradeTaskConcurrency Plus 升级任务并发数 [1, 64]，默认 8。
func (s *SystemConfigService) PlusUpgradeTaskConcurrency(ctx context.Context) int {
	v := s.GetInt(ctx, SettingPlusUpgradeTaskConcurrency, 8)
	if v <= 0 {
		return 8
	}
	if v > 64 {
		return 64
	}
	return int(v)
}

// PlusUpgradePerWalletQuota 一个钱包最多绑多少个活跃 Plus [1, 100]，默认 30。
func (s *SystemConfigService) PlusUpgradePerWalletQuota(ctx context.Context) int {
	v := s.GetInt(ctx, SettingPlusUpgradePerWalletQuota, 30)
	if v <= 0 {
		return 30
	}
	if v > 100 {
		return 100
	}
	return int(v)
}

// PlusUpgradeWalletCooldownMin 钱包失败后冷却分钟 [1, 1440]，默认 60。
func (s *SystemConfigService) PlusUpgradeWalletCooldownMin(ctx context.Context) int {
	v := s.GetInt(ctx, SettingPlusUpgradeWalletCooldown, 60)
	if v <= 0 {
		return 60
	}
	if v > 1440 {
		return 1440
	}
	return int(v)
}

// PlusUpgradeOTPPollInterval 云手机 OTP 拉取间隔 [1s, 30s]，默认 2s。
func (s *SystemConfigService) PlusUpgradeOTPPollInterval(ctx context.Context) time.Duration {
	v := s.GetInt(ctx, SettingPlusUpgradeOTPInterval, 2)
	if v <= 0 {
		v = 2
	}
	if v > 30 {
		v = 30
	}
	return time.Duration(v) * time.Second
}

// PlusUpgradeOTPTimeout 云手机 OTP 超时 [30s, 600s]，默认 180s。
func (s *SystemConfigService) PlusUpgradeOTPTimeout(ctx context.Context) time.Duration {
	v := s.GetInt(ctx, SettingPlusUpgradeOTPTimeout, 180)
	if v < 30 {
		v = 30
	}
	if v > 600 {
		v = 600
	}
	return time.Duration(v) * time.Second
}

// PlusUpgradeCSProxyStrategy Phase A 代理策略：
//   - account_proxy（默认）= 用 GPT 账号自身注册时的代理
//   - payment_pool          = 用印尼支付池（不推荐，会让 ChatGPT 风控触发）
func (s *SystemConfigService) PlusUpgradeCSProxyStrategy(ctx context.Context) string {
	v := s.GetString(ctx, SettingPlusUpgradeCSProxyStrategy, "account_proxy")
	switch v {
	case "account_proxy", "payment_pool":
		return v
	default:
		return "account_proxy"
	}
}

// PlusUpgradeExtProxyStrategy Phase B 代理策略：
//   - payment_pool（默认）= 用印尼支付池（推荐）
//   - account_proxy       = 用账号自身代理（注册代理是印尼时才合理）
func (s *SystemConfigService) PlusUpgradeExtProxyStrategy(ctx context.Context) string {
	v := s.GetString(ctx, SettingPlusUpgradeExtProxyStrategy, "payment_pool")
	switch v {
	case "account_proxy", "payment_pool":
		return v
	default:
		return "payment_pool"
	}
}

// PlusUpgradeCSProxyCountry Phase A (ChatGPT/Stripe) 代理国家代号。
//
// dispatcher 在 Phase A 按这个国家从 payment_proxy_pool 抢一条代理（country 字段匹配）。
// 这是为了让账号注册地区（如 JP）和支付链接获取阶段保持一致，避免 OpenAI 异地风控。
// 取空字符串表示禁用 CS 专属代理，会回退到 Phase B 代理（不推荐，建议总是配 JP 或 US）。
func (s *SystemConfigService) PlusUpgradeCSProxyCountry(ctx context.Context) string {
	v := strings.ToUpper(strings.TrimSpace(s.GetString(ctx, SettingPlusUpgradeCSProxyCountry, "JP")))
	return v
}

// PlusUpgradeExtProxyCountry Phase B (GoPay/Midtrans) 代理国家代号。
//
// 默认 ID（印尼）。GoPay 强制要求印尼出口，理论上不会改。
func (s *SystemConfigService) PlusUpgradeExtProxyCountry(ctx context.Context) string {
	v := strings.ToUpper(strings.TrimSpace(s.GetString(ctx, SettingPlusUpgradeExtProxyCountry, "ID")))
	if v == "" {
		v = "ID"
	}
	return v
}

// GeeLarkAPIBase GeeLark OpenAPI 基础地址（无尾斜杠）。
func (s *SystemConfigService) GeeLarkAPIBase(ctx context.Context) string {
	v := strings.TrimRight(s.GetString(ctx, SettingGeeLarkAPIBase, "https://openapi.geelark.cn/open/v1"), "/")
	if v == "" {
		v = "https://openapi.geelark.cn/open/v1"
	}
	return v
}

// === internal ===

// getRaw 拿原始 JSON 字符串（命中缓存）。
func (s *SystemConfigService) getRaw(ctx context.Context, key string) (string, bool) {
	s.mu.RLock()
	if time.Since(s.loaded) < s.ttl {
		v, ok := s.cache[key]
		s.mu.RUnlock()
		return v, ok
	}
	s.mu.RUnlock()
	if err := s.reload(ctx); err != nil {
		return "", false
	}
	s.mu.RLock()
	v, ok := s.cache[key]
	s.mu.RUnlock()
	return v, ok
}

func (s *SystemConfigService) reload(ctx context.Context) error {
	rows, err := s.repo.GetAll(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	s.mu.Lock()
	s.cache = m
	s.loaded = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *SystemConfigService) invalidate() {
	s.mu.Lock()
	s.loaded = time.Time{}
	s.mu.Unlock()
}

var _ = model.SystemConfig{} // 防止 import 被裁剪
