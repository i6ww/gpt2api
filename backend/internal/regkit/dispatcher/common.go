package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/regkit/captcha"
	"github.com/kleinai/backend/internal/regkit/mailbox"
	"github.com/kleinai/backend/internal/regkit/proxypicker"
	"github.com/kleinai/backend/internal/regkit/smspool"
	"github.com/kleinai/backend/internal/service"
)

// Deps 真实 provider dispatcher 共用依赖集。
//
// 通过组合（embed）的方式分发到 grok/adobe/gpt 三个子 dispatcher。
type Deps struct {
	MailMgr     *mailbox.Manager
	ProxyPicker *proxypicker.Picker
	SysCfg      *service.SystemConfigService
	// SMSMgr 仅 GPT dispatcher 用得到（OpenAI 强制手机号验证），
	// 没配置时为 nil；dispatcher 必须先 nil-check 再调用。
	SMSMgr *smspool.Manager
}

// CommonPayload 三家通用注册参数（前端 / API 提交进来的字段）。
//
// 各家自有字段（如 Adobe 的 country_code、Grok 的 trial）可通过 raw map 取。
type CommonPayload struct {
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Password  string `json:"password,omitempty"`
	ProxyID   uint64 `json:"proxy_id,omitempty"`
	Country   string `json:"country,omitempty"`
	Notes     string `json:"notes,omitempty"`
	// SMSCountry 仅当 dispatcher 走到 hero-sms 兜底时用作 country 覆盖
	// （格式同 system_config 里 sms.country：单值 "16"，或逗号分隔 "16,73,4,6"）。
	SMSCountry string `json:"sms_country,omitempty"`
}

// ParsePayload 把 task.Payload (JSON) 解析为 CommonPayload + 原始 map。
func ParsePayload(raw []byte) (*CommonPayload, map[string]any) {
	if len(raw) == 0 {
		return &CommonPayload{}, map[string]any{}
	}
	var p CommonPayload
	_ = json.Unmarshal(raw, &p)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m == nil {
		m = map[string]any{}
	}
	return &p, m
}

// CaptchaKind 区分注册流程要打哪种码。
type CaptchaKind string

const (
	CaptchaKindArkose    CaptchaKind = "arkose"    // Banana（Adobe）/ GPT 走 FunCaptcha
	CaptchaKindTurnstile CaptchaKind = "turnstile" // Grok 走 Cloudflare Turnstile
)

// BuildCaptcha 等同于 BuildCaptchaArkose；保留作为旧调用方兼容入口。
//
// Deprecated: 新代码请用 BuildCaptchaArkose / BuildCaptchaTurnstile。
func BuildCaptcha(ctx context.Context, sysCfg *service.SystemConfigService) (captcha.Solver, error) {
	return BuildCaptchaArkose(ctx, sysCfg)
}

// BuildCaptchaArkose 给 Banana / GPT 用：读 captcha.arkose.* 配置（缺失回落 captcha.*）。
//
// 当 captcha.arkose.fallbacks 配置了非空列表时，自动把主配置 + 各备用配置串成
// ChainSolver：链上单家失败立刻 fail-over 到下一家，整体仍实现 Solver 接口，
// dispatcher 无需感知"单家 / 链"差异。
//
// per-attempt 超时按链长度自适应：1 家走 solver 内置 MaxWait（60s），2 家 45s/家，
// ≥3 家 35s/家。这样总等待时间被卡在 60-105s 范围内不会失控。
func BuildCaptchaArkose(ctx context.Context, sysCfg *service.SystemConfigService) (captcha.Solver, error) {
	if sysCfg == nil {
		return nil, errors.New("系统配置服务未注入（内部错误）")
	}
	provider, apiKey, endpoint := sysCfg.CaptchaArkose(ctx)
	primary, err := buildCaptchaSolver(provider, apiKey, endpoint, CaptchaKindArkose)
	if err != nil {
		return nil, err
	}
	fallbacks := sysCfg.CaptchaArkoseFallbacks(ctx)
	return wrapWithFallbacks(primary, fallbacks, CaptchaKindArkose), nil
}

// BuildCaptchaTurnstile 给 Grok 用：读 captcha.turnstile.* 配置（缺失回落 captcha.*）。
//
// 同 BuildCaptchaArkose，自动把 captcha.turnstile.fallbacks 拼到链上。
func BuildCaptchaTurnstile(ctx context.Context, sysCfg *service.SystemConfigService) (captcha.Solver, error) {
	if sysCfg == nil {
		return nil, errors.New("系统配置服务未注入（内部错误）")
	}
	provider, apiKey, endpoint := sysCfg.CaptchaTurnstile(ctx)
	primary, err := buildCaptchaSolver(provider, apiKey, endpoint, CaptchaKindTurnstile)
	if err != nil {
		return nil, err
	}
	fallbacks := sysCfg.CaptchaTurnstileFallbacks(ctx)
	return wrapWithFallbacks(primary, fallbacks, CaptchaKindTurnstile), nil
}

// wrapWithFallbacks 把主 solver 和 fallback 列表组装成 ChainSolver。
//
// fallback 项里 build 失败（如非法 provider / 缺 api_key）的会被静默跳过，
// 不影响其他可用项的串联。
//
// 单家场景（fallback 空 / 全部非法）直接返回主 solver，避免多套一层 ChainSolver 包装。
func wrapWithFallbacks(primary captcha.Solver, fallbacks []service.CaptchaProviderEntry, kind CaptchaKind) captcha.Solver {
	if primary == nil {
		return primary
	}
	solvers := []captcha.Solver{primary}
	for _, e := range fallbacks {
		s, err := buildCaptchaSolver(e.Provider, e.APIKey, e.Endpoint, kind)
		if err != nil || s == nil {
			continue
		}
		solvers = append(solvers, s)
	}
	if len(solvers) <= 1 {
		return primary
	}
	chain := captcha.NewChain(solvers...)
	if chain == nil {
		return primary
	}
	// 按链长度自适应单家超时预算。Arkose / Turnstile 两类共用同一档位策略：
	//   - 2 家：45s/家（总预算 90s，比单家 60s 慢一点但解题率 ~91%）
	//   - 3+：35s/家（总预算 105s，解题率 ~97%）
	switch len(solvers) {
	case 2:
		chain.PerAttempt = 45 * time.Second
	default:
		if len(solvers) >= 3 {
			chain.PerAttempt = 35 * time.Second
		}
	}
	return chain
}

// buildCaptchaSolver 公共内核：根据 provider / api_key / endpoint 实例化 solver。
//
// 支持 provider（按 Adobe Arkose 解题率从高到低排序）：
//
//	anti-captcha        Anti-Captcha（Adobe Arkose 70–85%，推荐）
//	nopecha             NopeCHA      （Adobe Arkose 60–80%）
//	yescaptcha          YesCaptcha   （Adobe Arkose 50–60%）
//	2captcha            2Captcha     （Adobe Arkose 30–45%）
//	capsolver           CapSolver    （Adobe Arkose 不可用，2024-12 起废弃 FunCaptcha）
//	local / camoufox    本地 Camoufox solver（HTTP 服务，仅 Turnstile）
//	none / 空           返回 ErrNotConfigured
//
// local 不要求 api_key；其他 provider 要求 api_key。
// kind=arkose 时拒绝 local 和 capsolver（前者只会 Turnstile，后者已废弃）。
//
// 容错：用户填了某家域名却选了别家 provider，按 endpoint 推断真实意图。
func buildCaptchaSolver(provider, apiKey, endpoint string, kind CaptchaKind) (captcha.Solver, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	apiKey = strings.TrimSpace(apiKey)
	ep := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	// 容错：把人工误带的 path 砍掉，避免拼出 /in.php/createTask 这种东西。
	for _, suffix := range []string{"/createTask", "/getTaskResult", "/in.php", "/res.php"} {
		ep = strings.TrimSuffix(ep, suffix)
	}
	if provider == "" || provider == "none" {
		return nil, captcha.ErrNotConfigured
	}
	if provider != "local" && provider != "camoufox" && provider != "local_camoufox" && apiKey == "" {
		return nil, captcha.ErrNotConfigured
	}

	// inferByEndpoint endpoint 域名暗示真实 provider，让用户填错 provider 时也能跑。
	inferByEndpoint := func() captcha.Solver {
		switch {
		case strings.Contains(ep, "anti-captcha"):
			return captcha.NewAntiCaptcha(apiKey)
		case strings.Contains(ep, "nopecha"):
			return captcha.NewNopeCHA(apiKey)
		case strings.Contains(ep, "yescaptcha"):
			return captcha.NewYesCaptcha(apiKey)
		case strings.Contains(ep, "2captcha"):
			return captcha.New2Captcha(apiKey)
		case strings.Contains(ep, "capsolver"):
			return captcha.NewCapSolver(apiKey)
		}
		return nil
	}
	applyEp := func(s *captcha.CapSolver) *captcha.CapSolver {
		if ep != "" {
			s.Endpoint = ep
		}
		return s
	}

	switch provider {
	case "anti-captcha", "anticaptcha":
		s := captcha.NewAntiCaptcha(apiKey)
		return applyEp(s), nil
	case "nopecha":
		s := captcha.NewNopeCHA(apiKey)
		return applyEp(s), nil
	case "yescaptcha":
		s := captcha.NewYesCaptcha(apiKey)
		return applyEp(s), nil
	case "2captcha", "twocaptcha":
		s := captcha.New2Captcha(apiKey)
		return applyEp(s), nil
	case "capsolver":
		// 兼容老配置：如果用户 endpoint 指向了别家，按 endpoint 走真实 provider。
		if inf := inferByEndpoint(); inf != nil {
			return inf, nil
		}
		// kind=arkose 时 capsolver 已废弃 FunCaptcha，直接报错引导换 provider。
		if kind == CaptchaKindArkose {
			return nil, errors.New("CapSolver 已于 2024-12 废弃 FunCaptcha 支持；Adobe / GPT Arkose 请改用 anti-captcha / nopecha / yescaptcha / 2captcha")
		}
		s := captcha.NewCapSolver(apiKey)
		return applyEp(s), nil
	case "local", "camoufox", "local_camoufox":
		if kind == CaptchaKindArkose {
			return nil, errors.New("本地 Camoufox solver 仅支持 Turnstile（Grok）；Banana / GPT 请配置商用打码服务")
		}
		return captcha.NewLocalCamoufox(ep), nil
	default:
		return nil, fmt.Errorf("不支持的 captcha provider: %q（支持 anti-captcha / nopecha / yescaptcha / 2captcha / capsolver / local）", provider)
	}
}

// BuildMailBackendConfig 把系统配置里的邮箱 backend 设置摊平成 mailbox.BackendConfig。
//
// 与 Pending.buildMailBackendConfig 一致；提取到这里给真实 dispatcher 复用。
func BuildMailBackendConfig(ctx context.Context, sysCfg *service.SystemConfigService) mailbox.BackendConfig {
	out := mailbox.BackendConfig{}
	if sysCfg == nil {
		return out
	}
	// "默认收件后端"是 AcquireFresh 的总开关：决定走 CF 即时签发 还是 mail_pool。
	out.DefaultMode = sysCfg.MailDefaultBackend(ctx)
	if v := sysCfg.GetJSON(ctx, service.SettingMailOutlook); v != nil {
		if mode, ok := v["mode"].(string); ok {
			out.OutlookMode = mode
		}
		if s, ok := v["scope_imap"].(string); ok {
			out.OutlookScopeIMAP = s
		}
		if s, ok := v["scope_graph"].(string); ok {
			out.OutlookScopeGraph = s
		}
	}
	if v := sysCfg.GetJSON(ctx, service.SettingMailTempmail); v != nil {
		if s, ok := v["api_base_url"].(string); ok {
			out.TempmailBase = s
		}
		if s, ok := v["new_address_path"].(string); ok {
			out.TempmailNewAddressPath = s
		}
		if s, ok := v["mails_path"].(string); ok {
			out.TempmailMailsPath = s
		}
		if s, ok := v["address_name"].(string); ok {
			out.TempmailAddressName = s
		}
		if a, ok := v["address_domains"].([]any); ok {
			for _, x := range a {
				if s, ok := x.(string); ok {
					out.TempmailAddressDomains = append(out.TempmailAddressDomains, s)
				}
			}
		}
	}
	if v := sysCfg.GetJSON(ctx, service.SettingMailCF); v != nil {
		if s, ok := v["worker_domain"].(string); ok {
			out.CFWorkerDomain = s
		}
		if s, ok := v["email_domain"].(string); ok {
			out.CFEmailDomain = s
		}
		if s, ok := v["admin_password"].(string); ok {
			out.CFAdminPassword = s
		}
	}
	return out
}

// PoolMaxFailure 邮箱在 mail_pool 内单封最多失败次数（超过置 failed）。
//
// 临时邮箱（cf_worker mode）失败一次后，OpenAI / Grok / Adobe 都会把这封邮箱
// 标记为"已开了 OAuth 草稿"，再用同一封邮箱必撞 invalid_auth_step。所以这里
// 设成 1 — 失败一次直接 retire，反正 cf 临时邮箱可以无限领新的。
const PoolMaxFailure = 1

// MaskProxy 输出代理时去掉密码部分，避免日志泄露凭据。
//
//	http://user:pass@host:port -> http://user:***@host:port
func MaskProxy(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	at := strings.LastIndex(rawURL, "@")
	if at < 0 {
		return rawURL
	}
	colon := strings.Index(rawURL[:at], ":")
	if colon < 0 {
		return rawURL
	}
	// 找到 "://"
	scheme := strings.Index(rawURL[:colon], "://")
	if scheme < 0 {
		return rawURL[:colon] + ":***" + rawURL[at:]
	}
	userStart := scheme + 3
	userEnd := strings.Index(rawURL[userStart:at], ":")
	if userEnd < 0 {
		return rawURL
	}
	userEnd += userStart
	return rawURL[:userEnd] + ":***" + rawURL[at:]
}

// MaskOTP 邮箱验证码不打全；仅用于日志。
func MaskOTP(otp string) string {
	otp = strings.TrimSpace(otp)
	if len(otp) <= 2 {
		return "**"
	}
	if len(otp) <= 4 {
		return otp[:1] + strings.Repeat("*", len(otp)-1)
	}
	return otp[:2] + strings.Repeat("*", len(otp)-3) + otp[len(otp)-1:]
}
