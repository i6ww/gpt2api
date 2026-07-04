package smspool

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
)

// 与 system_config 对齐的 setting key。
const (
	settingProvider        = "sms.provider"
	settingAPIURL          = "sms.api_url"
	settingAPIKey          = "sms.api_key"
	settingService         = "sms.service"
	settingCountry         = "sms.country"
	settingPrice           = "sms.max_price"
	settingMaxUses         = "sms.max_uses"
	settingPrefixAllowlist = "sms.phone_prefix_allowlist"
)

// Manager 整合 hero-sms 客户端 + phone_pool repo + system_config 配置。
//
// dispatcher 在遇到 add-phone 墙时使用 Manager.AcquirePhone 获取一个可用号码，
// 验证完成后调用 MarkVerified（成功）或 MarkFailed（失败）。
type Manager struct {
	repo   *repo.PhonePoolRepo
	sysCfg *service.SystemConfigService
}

// NewManager 构造。
func NewManager(r *repo.PhonePoolRepo, sysCfg *service.SystemConfigService) *Manager {
	return &Manager{repo: r, sysCfg: sysCfg}
}

// LoadConfig 从 system_config 拼出 hero-sms 客户端配置。
//
// 调用方负责检查 cfg.APIKey 是否非空（空表示运维还没配置 SMS）。
func (m *Manager) LoadConfig(ctx context.Context) HeroSMSConfig {
	if m == nil || m.sysCfg == nil {
		return HeroSMSConfig{}
	}
	cfg := HeroSMSConfig{
		APIURL:    strings.TrimSpace(m.sysCfg.GetString(ctx, settingAPIURL, "https://hero-sms.com/stubs/handler_api.php")),
		APIKey:    strings.TrimSpace(m.sysCfg.GetString(ctx, settingAPIKey, "")),
		Service:   strings.TrimSpace(m.sysCfg.GetString(ctx, settingService, "dr")),
		Countries: parseCountryList(m.sysCfg.GetString(ctx, settingCountry, "")),
		MaxPrice:  parseFloat(m.sysCfg.GetString(ctx, settingPrice, "0")),
	}
	return cfg
}

// parseCountryList 把 "52,12,16" 之类逗号 / 分号 / 空白分隔的字符串拆成 []int。
//
// 对历史的纯数字（"52"）也兼容；解析失败的元素会被忽略。
func parseCountryList(raw string) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]int, 0, len(parts))
	seen := make(map[int]struct{}, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// MaxUses 全局复用上限（默认 3）。
func (m *Manager) MaxUses(ctx context.Context) int {
	if m == nil || m.sysCfg == nil {
		return 3
	}
	v := int(m.sysCfg.GetInt(ctx, settingMaxUses, 3))
	if v < 1 {
		v = 1
	}
	if v > 10 {
		v = 10
	}
	return v
}

// IsConfigured 判断 SMS 是否已配置好（API key 非空）。
func (m *Manager) IsConfigured(ctx context.Context) bool {
	cfg := m.LoadConfig(ctx)
	return cfg.APIKey != ""
}

// AcquireResult 一次 SMS 接码握手结果。
type AcquireResult struct {
	Row    *model.PhonePool // 数据库记录
	Client *Client          // 已绑定 cfg + http client 的 hero-sms 客户端
}

// AcquirePhone 拿到一个可用手机号 — 优先复用 phone_pool 中 used_count<max_uses 的号，
// 否则向 hero-sms 申请一个新号并入池。
//
// 注意：本函数不做"号码已成功用于 OpenAI"的副作用 — 上层在 OTP 校验通过后必须
// 调 MarkVerified；任何过程失败都要调 MarkFailed 增加 failure_count。
func (m *Manager) AcquirePhone(ctx context.Context, httpClient *http.Client) (*AcquireResult, error) {
	return m.AcquirePhoneWithCountries(ctx, httpClient, nil)
}

// AcquirePhoneWithCountries 同 AcquirePhone，但允许临时覆盖 cfg.Countries —
// 用于调度方按任务粒度指定国家测试 / 串并发跑多国家比对。
//
// countriesOverride 为 nil 时使用全局 sms.country；非 nil（含空切片）时直接覆盖。
//
// 同时应用 sms.phone_prefix_allowlist 过滤：例如 "628389" 表示只接受 +62 838 9...
// 段的印尼号（实测只有这段在 hero-sms 池里能真正投递 OpenAI SMS）。不命中
// 白名单的号会被立即 setStatus=8 释放并标 broken，循环最多 10 次。
func (m *Manager) AcquirePhoneWithCountries(ctx context.Context, httpClient *http.Client, countriesOverride []int) (*AcquireResult, error) {
	cfg := m.LoadConfig(ctx)
	if cfg.APIKey == "" {
		return nil, errors.New("hero-sms 未配置：sms.api_key 为空")
	}
	if countriesOverride != nil {
		cfg.Countries = countriesOverride
	}
	cli := New(cfg, httpClient)
	if err := cli.Validate(); err != nil {
		return nil, err
	}

	maxUses := m.MaxUses(ctx)
	allowlist := m.phonePrefixAllowlist(ctx)

	// 先尝试在 phone_pool 中复用：找一条 used_count<max_uses 的、且符合 allowlist
	// 与 country 过滤（如果指定）；没有再 getNumberV2 拿新号入池。
	//
	// 复用流程：
	//   - DB 拿到候选 row 后，向 hero-sms 申请一个新 activation（号可能与 row.phone 一致：
	//     hero-sms 会在 maxUses 期内复用同一号，对接收端来说就是"再发一次"）；
	//   - 不一致：释放 DB 行回 available，把新号当首次申请处理；
	//   - 不符合 allowlist：本条 row 永久标 broken（fail++ 直接超 maxFailure=1）。
	if row, err := m.repo.AcquireOrInsert(ctx, "herosms", cfg.Service, cfg.Countries, nil); err == nil && row != nil {
		if phonePrefixAllowed(row.Phone, allowlist) {
			entry, err := cli.AcquireNumber(ctx)
			if err != nil {
				_ = m.repo.Release(ctx, row.ID)
				return nil, fmt.Errorf("hero-sms 申请复用 SMS 失败: %w", err)
			}
			if entry.Phone != row.Phone {
				_ = m.repo.Release(ctx, row.ID)
				if !phonePrefixAllowed(entry.Phone, allowlist) {
					_ = cli.SetStatus(ctx, entry.ActivationID, 8)
					// 落到下面新申请循环里继续过滤
				} else {
					newRow, err := m.repo.AcquireOrInsert(ctx, "herosms", cfg.Service, cfg.Countries, &model.PhonePool{
						Provider:     "herosms",
						Service:      cfg.Service,
						Phone:        entry.Phone,
						Country:      entry.Country,
						ActivationID: ptr(entry.ActivationID),
						MaxUses:      maxUses,
					})
					if err != nil {
						return nil, err
					}
					return &AcquireResult{Row: newRow, Client: cli}, nil
				}
			} else {
				if err := m.repo.UpdateActivationID(ctx, row.ID, entry.ActivationID); err != nil {
					return nil, err
				}
				row.ActivationID = ptr(entry.ActivationID)
				return &AcquireResult{Row: row, Client: cli}, nil
			}
		} else {
			// 池里那条号不符合 allowlist —— 把它永久标 broken，避免下次再被取出。
			_ = m.repo.MarkFailed(ctx, row.ID, "phone prefix not allowed", 1)
		}
	}

	// 池中没有可复用（或池中号不符合 allowlist）— 新申请，
	// 配合 allowlist 做最多 10 次"丢号重申"。
	const maxPrefixTries = 10
	for try := 0; try < maxPrefixTries; try++ {
		entry, err := cli.AcquireNumber(ctx)
		if err != nil {
			return nil, fmt.Errorf("hero-sms 首次申请号码失败: %w", err)
		}
		if !phonePrefixAllowed(entry.Phone, allowlist) {
			// 号不符合 — setStatus=8 释放（hero-sms < 2min 可能拒，软错），继续下一轮。
			_ = cli.SetStatus(ctx, entry.ActivationID, 8)
			continue
		}
		row, err := m.repo.AcquireOrInsert(ctx, "herosms", cfg.Service, cfg.Countries, &model.PhonePool{
			Provider:     "herosms",
			Service:      cfg.Service,
			Phone:        entry.Phone,
			Country:      entry.Country,
			ActivationID: ptr(entry.ActivationID),
			MaxUses:      maxUses,
		})
		if err != nil {
			return nil, err
		}
		return &AcquireResult{Row: row, Client: cli}, nil
	}
	return nil, fmt.Errorf("hero-sms 连续 %d 个号都不在 allowlist=%v 段内", maxPrefixTries, allowlist)
}

// phonePrefixAllowlist 读 sms.phone_prefix_allowlist —— 逗号分隔的 E164（不带 +）前缀。
// 空表示不过滤。
func (m *Manager) phonePrefixAllowlist(ctx context.Context) []string {
	if m == nil || m.sysCfg == nil {
		return nil
	}
	raw := m.sysCfg.GetString(ctx, settingPrefixAllowlist, "")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "+")
		// 把"+62 838 9"、"62-838-9" 之类常见分隔符去掉，留纯数字前缀。
		p = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// phonePrefixAllowed 判断 phone（E164 不带 +）是否命中 allowlist 中任意一项前缀。
//
// allowlist 为空时一律放行。
func phonePrefixAllowed(phone string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	phone = strings.TrimSpace(phone)
	phone = strings.TrimPrefix(phone, "+")
	for _, p := range allowlist {
		if p != "" && strings.HasPrefix(phone, p) {
			return true
		}
	}
	return false
}

// Release 把号码放回 available。OpenAI 报错"无法发送 SMS 到该号码"时调用。
func (m *Manager) Release(ctx context.Context, id uint64) error {
	return m.repo.Release(ctx, id)
}

// MarkVerified OpenAI phone-otp/validate 200 之后调用：used_count++。
func (m *Manager) MarkVerified(ctx context.Context, id, accountID uint64) error {
	return m.repo.MarkVerified(ctx, id, accountID)
}

// MarkFailed OpenAI 拒绝该号 / 接码超时 / 验证码错时调用。maxFailure 由调用方按
// 业务策略决定（一般 2 次以内允许）。
func (m *Manager) MarkFailed(ctx context.Context, id uint64, reason string, maxFailure int) error {
	if maxFailure <= 0 {
		maxFailure = 2
	}
	return m.repo.MarkFailed(ctx, id, reason, maxFailure)
}

// MarkSoftFailure 智能版 MarkFailed：根据号是否曾经成功过自动调整 maxFailure。
//
//   - used_count == 0（从未收过 SMS 的"陌生号"）：maxFailure = 1，第一次拒就 broken，
//     避免反复浪费在虚号 / 坏段上；
//   - used_count > 0（已经成功收过至少一次的"热号"）：maxFailure = 3，OpenAI 的
//     suspicious / phone_number_in_use 偶发性拒绝不会立刻吞掉宝贵的 reusable 号。
//
// 调用方不需要再关心数字阈值。
func (m *Manager) MarkSoftFailure(ctx context.Context, id uint64, reason string) error {
	row, err := m.repo.GetByID(ctx, id)
	if err != nil || row == nil {
		// 拿不到 row（一般是 id 失效）就用最严格的 1 次。
		return m.repo.MarkFailed(ctx, id, reason, 1)
	}
	maxFailure := 1
	if row.UsedCount > 0 {
		maxFailure = 3
	}
	return m.repo.MarkFailed(ctx, id, reason, maxFailure)
}

// WaitOTP 简单包装到 client.WaitOTP，附上默认超时/轮询周期。
func (m *Manager) WaitOTP(ctx context.Context, cli *Client, activationID string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 240 * time.Second
	}
	return cli.WaitOTP(ctx, activationID, timeout, 5*time.Second)
}

// === helpers ===

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func ptr[T any](v T) *T { return &v }
