// Package smspool 接码服务封装。
//
// 当前只实现了 hero-sms.com 的 handler_api.php 兼容协议（与
// sms-activate.org / 5sim.net 大同小异）。一个号码默认允许复用 3 次（OpenAI
// 限制每个手机号最多绑定 3 个账号），由调用方通过 phone_pool 控制 used_count。
//
// 文档：https://hero-sms.com/cn/api 或 chrome 扩展 lovepiece/codex-oauth-automation-extension
package smspool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HeroSMSConfig hero-sms 配置（来自 system_config）。
//
// APIURL 默认 https://hero-sms.com/stubs/handler_api.php；不要带尾斜杠。
//
// Countries 支持配置多国家代号（按 sms-activate 标准编号），acquire 时按顺序
// 逐个尝试；空列表表示不传 country（hero-sms 端按 service 默认派号）。
//
// 常用：0=俄罗斯 / 1=乌克兰 / 6=印尼 / 12=美国 / 16=英国 / 22=印度 / 52=墨西哥。
type HeroSMSConfig struct {
	APIURL    string
	APIKey    string
	Service   string  // 服务代号，OpenAI = "dr"
	Countries []int   // 国家 ID 列表（按优先级顺序）；空表示不指定
	MaxPrice  float64 // 单次最高价格（USD），0 表示不限
	HTTPProxy string  // 可选：通过同一代理走 hero-sms 调用
}

// PhoneEntry hero-sms 返回的一条租用记录。
type PhoneEntry struct {
	ActivationID string // hero-sms 的 activation id
	Phone        string // E.164 不带 + 的手机号（例如 14155551234）
	Country      int    // 实际下发的国家 ID
}

// E164 把原始 phone（不含 +）补成 +E.164 形式。
func (e PhoneEntry) E164() string {
	num := strings.TrimPrefix(strings.TrimSpace(e.Phone), "+")
	if num == "" {
		return ""
	}
	return "+" + num
}

// Client hero-sms API 客户端。
type Client struct {
	cfg HeroSMSConfig
	hc  *http.Client
}

// New 构造一个 hero-sms 客户端。
//
// httpClient 留 nil 时使用 30s 超时的默认 client；建议复用与 OpenAI 同代理的
// httpc.Client，使账号 IP / 短信 IP 出口一致（hero-sms 不严格校验来源 IP，
// 但保持一致更安全）。
func New(cfg HeroSMSConfig, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if strings.TrimSpace(cfg.APIURL) == "" {
		cfg.APIURL = "https://hero-sms.com/stubs/handler_api.php"
	}
	return &Client{cfg: cfg, hc: httpClient}
}

// Validate 检查必填字段。
func (c *Client) Validate() error {
	if c == nil {
		return errors.New("hero-sms 客户端未初始化")
	}
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return errors.New("hero-sms api_key 未配置")
	}
	if strings.TrimSpace(c.cfg.Service) == "" {
		return errors.New("hero-sms service 未配置（OpenAI 用 dr）")
	}
	return nil
}

// Balance 查询账户余额（USD）。
func (c *Client) Balance(ctx context.Context) (float64, error) {
	body, err := c.do(ctx, url.Values{"action": {"getBalance"}})
	if err != nil {
		return 0, err
	}
	// 形如 "ACCESS_BALANCE:1.23"
	parts := strings.SplitN(body, ":", 2)
	if len(parts) != 2 || parts[0] != "ACCESS_BALANCE" {
		return 0, fmt.Errorf("getBalance 异常响应: %s", body)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, fmt.Errorf("getBalance 解析余额失败: %s", body)
	}
	return v, nil
}

// AcquireNumber 调用 getNumberV2 申请一个号码。
//
// 多国家配置时按 cfg.Countries 顺序逐个尝试，全部 NO_NUMBERS 才算失败。
// 失败返回 hero-sms 原始错误码（如 NO_NUMBERS / NO_BALANCE / BAD_KEY）。
func (c *Client) AcquireNumber(ctx context.Context) (*PhoneEntry, error) {
	countries := c.cfg.Countries
	if len(countries) == 0 {
		countries = []int{-1} // 哨兵：不带 country 参数
	}

	var lastErr error
	for _, ctry := range countries {
		entry, err := c.acquireOnce(ctx, ctry)
		if err == nil {
			return entry, nil
		}
		lastErr = err
		// NO_NUMBERS / NO_BALANCE 之外的错误（BAD_KEY / 网络错误等）直接放弃，
		// 不要消耗后续国家配额。
		if !isRetryableAcquireErr(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("getNumberV2 未配置任何国家")
	}
	return nil, lastErr
}

// acquireOnce 用单一国家尝试一次。country < 0 表示不带 country 参数。
func (c *Client) acquireOnce(ctx context.Context, country int) (*PhoneEntry, error) {
	form := url.Values{
		"action":  {"getNumberV2"},
		"service": {c.cfg.Service},
	}
	if country >= 0 {
		form.Set("country", strconv.Itoa(country))
	}
	if c.cfg.MaxPrice > 0 {
		form.Set("maxPrice", strconv.FormatFloat(c.cfg.MaxPrice, 'f', -1, 64))
	}
	body, err := c.do(ctx, form)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(body, "NO_") || strings.HasPrefix(body, "BAD_") || strings.HasPrefix(body, "ERROR_") {
		return nil, fmt.Errorf("getNumberV2 拒绝: %s", body)
	}
	// V2 返回 JSON：{"activationId":"123","phoneNumber":"79991234567","country":"0",...}
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		entry, err := parseV2JSON(body)
		if err != nil {
			return nil, err
		}
		// 兜底：JSON 没回 country 时填回我们请求的那个国家
		if entry.Country == 0 && country >= 0 {
			entry.Country = country
		}
		return entry, nil
	}
	// 回退老协议 ACCESS_NUMBER:id:phone
	parts := strings.SplitN(body, ":", 3)
	if len(parts) >= 3 && parts[0] == "ACCESS_NUMBER" {
		fallback := country
		if fallback < 0 {
			fallback = 0
		}
		return &PhoneEntry{ActivationID: parts[1], Phone: parts[2], Country: fallback}, nil
	}
	return nil, fmt.Errorf("getNumberV2 异常响应: %s", body)
}

// isRetryableAcquireErr 仅 NO_NUMBERS / NO_ACTIVATION 之类临时缺号错误才允许换国家重试，
// BAD_KEY / NO_BALANCE / 网络错误这些不重试避免烧 quota。
func isRetryableAcquireErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "NO_NUMBERS"),
		strings.Contains(msg, "NO_ACTIVATION"),
		strings.Contains(msg, "NO_FREE_PHONES"):
		return true
	}
	return false
}

// WaitOTP 调用 getStatusV2 轮询 OTP，直到 STATUS_OK:CODE 或超时 / 不可恢复错误。
//
//   - timeout：总等待时间
//   - interval：每次轮询间隔
//
// 第一次调用 WaitOTP 之前，调用方应该先 SetStatus(activationID, 1) 提示
// hero-sms"号码已使用，准备接收 SMS"。
func (c *Client) WaitOTP(ctx context.Context, activationID string, timeout, interval time.Duration) (string, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		body, err := c.do(ctx, url.Values{
			"action": {"getStatusV2"},
			"id":     {activationID},
		})
		if err != nil {
			return "", err
		}
		switch {
		case strings.HasPrefix(body, "STATUS_OK"):
			parts := strings.SplitN(body, ":", 2)
			if len(parts) != 2 {
				return "", fmt.Errorf("getStatusV2 OK 但缺 OTP: %s", body)
			}
			return strings.TrimSpace(parts[1]), nil
		case strings.HasPrefix(body, "STATUS_WAIT_CODE"),
			strings.HasPrefix(body, "STATUS_WAIT_RETRY"),
			strings.HasPrefix(body, "STATUS_WAIT_RESEND"):
			// 继续轮询
		case strings.HasPrefix(body, "STATUS_CANCEL"),
			strings.HasPrefix(body, "STATUS_REVOKED"):
			return "", fmt.Errorf("hero-sms: %s", body)
		case strings.HasPrefix(strings.TrimSpace(body), "{"):
			// hero-sms V2 协议：{"verificationType":N,"sms":"123456"|null,"call":null}
			//   sms 非空且非 "null" 时即为收到的 OTP；为空 / null 表示仍在等待。
			//   后端可能在同一字段返回 OTP 字符串 / 数字 / OTP 列表，简化只取
			//   sms 字段的字符串值。
			otp := extractV2SMS(body)
			if otp != "" {
				return otp, nil
			}
		default:
			return "", fmt.Errorf("getStatusV2 异常响应: %s", body)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("等待 OTP 超时（%s）", timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}
}

// SetStatus 修改 activation 状态：
//
//	1 = 准备接收（OpenAI 真正发出 SMS 之前调一次）
//	3 = 请求新短信 / 重发
//	6 = 完成（确认收到正确 OTP）
//	8 = 取消（释放号码）
//
// 注意 6 之后号码会被标记"已用",不能再用同一个 activation id 申请新短信;
// 想复用号码必须用 setStatus=3 重发,或重新 getNumberV2(同一手机号 hero-sms 会优先派回)。
func (c *Client) SetStatus(ctx context.Context, activationID string, status int) error {
	body, err := c.do(ctx, url.Values{
		"action": {"setStatus"},
		"id":     {activationID},
		"status": {strconv.Itoa(status)},
	})
	if err != nil {
		return err
	}
	switch body {
	case "ACCESS_READY", "ACCESS_RETRY_GET", "ACCESS_ACTIVATION", "ACCESS_CANCEL":
		return nil
	}
	if strings.HasPrefix(body, "EARLY_CANCEL_DENIED") {
		// 太早取消（hero-sms 要 ≥2 分钟）。这是软错误，让上层兜底。
		return fmt.Errorf("hero-sms 拒绝过早取消: %s", body)
	}
	return fmt.Errorf("setStatus 异常响应: %s", body)
}

// === 内部 ===

func (c *Client) do(ctx context.Context, form url.Values) (string, error) {
	form.Set("api_key", c.cfg.APIKey)
	endpoint := strings.TrimRight(c.cfg.APIURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+form.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("hero-sms 网络错误: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	bodyStr := strings.TrimSpace(string(raw))

	if resp.StatusCode == http.StatusConflict {
		// 409：通常是 OTP_RECEIVED / EARLY_CANCEL_DENIED 等业务约束，body 里有详情。
		return bodyStr, nil
	}
	if resp.StatusCode == http.StatusOK {
		return bodyStr, nil
	}
	// 4xx：hero-sms V2 协议会用 HTTP 4xx + JSON {"title":"NO_NUMBERS","details":"..."} 表达
	// 业务错误。把 title 抽出来当老协议字符串返回，让上层（AcquireNumber 等）能识别。
	if title := extractJSONTitle(bodyStr); title != "" {
		return title, nil
	}
	return "", fmt.Errorf("hero-sms HTTP %d: %s", resp.StatusCode, snippet(raw))
}

// extractJSONTitle 抽取形如 {"title":"NO_NUMBERS","details":"..."} 中的 title。
//
// 用最简陋的 substring 方式，避免引入 encoding/json 依赖（当前包刻意零依赖）。
func extractJSONTitle(s string) string {
	if !strings.HasPrefix(s, "{") {
		return ""
	}
	const key = `"title"`
	idx := strings.Index(s, key)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(key):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t")
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	end := strings.IndexByte(rest, '"')
	if end <= 0 {
		return ""
	}
	return rest[:end]
}

// parseV2JSON 解析 getNumberV2 的 JSON 响应。
//
// 不引入额外的 json 依赖（只解析 3 个固定字段）以保持包零依赖。
func parseV2JSON(body string) (*PhoneEntry, error) {
	get := func(key string) string {
		// 简陋的 JSON 字符串提取：找 "key":"value" 或 "key":number。
		// hero-sms 的 V2 返回字段非常稳定，足以应付。
		patterns := []string{`"` + key + `":"`, `"` + key + `":`}
		for _, pat := range patterns {
			idx := strings.Index(body, pat)
			if idx < 0 {
				continue
			}
			rest := body[idx+len(pat):]
			if strings.HasPrefix(pat, `"`+key+`":"`) {
				end := strings.IndexByte(rest, '"')
				if end > 0 {
					return rest[:end]
				}
			} else {
				end := strings.IndexAny(rest, ",}")
				if end > 0 {
					return strings.TrimSpace(rest[:end])
				}
			}
		}
		return ""
	}
	id := get("activationId")
	phone := get("phoneNumber")
	if id == "" || phone == "" {
		return nil, fmt.Errorf("getNumberV2 响应解析失败: %s", body)
	}
	cstr := get("country")
	country, _ := strconv.Atoi(strings.Trim(cstr, "\""))
	return &PhoneEntry{ActivationID: id, Phone: phone, Country: country}, nil
}

// extractV2SMS 从 hero-sms V2 getStatusV2 JSON 中抽出 sms 字段的有效 OTP。
//
// 返回空串表示尚未收到（sms == null / 不存在 / 空字符串），上层应继续轮询。
//
// 兼容四种 sms 字段格式：
//
//	"sms":"123456"                                       → 直接字符串
//	"sms":["123456",...]                                 → 取第一条字符串
//	"sms":{"code":"123456","text":"...","dateTime":...}  → 取 code 字段；
//	                                                       缺 code 时从 text 抠 4-8 位数字
//	"sms":null                                           → 等待中
func extractV2SMS(body string) string {
	const key = `"sms"`
	idx := strings.Index(body, key)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(key):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t")
	switch {
	case strings.HasPrefix(rest, "null"):
		return ""
	case strings.HasPrefix(rest, `"`):
		rest = rest[1:]
		end := strings.IndexByte(rest, '"')
		if end <= 0 {
			return ""
		}
		return rest[:end]
	case strings.HasPrefix(rest, "["):
		rest = rest[1:]
		open := strings.IndexByte(rest, '"')
		if open < 0 {
			return ""
		}
		rest = rest[open+1:]
		end := strings.IndexByte(rest, '"')
		if end <= 0 {
			return ""
		}
		return rest[:end]
	case strings.HasPrefix(rest, "{"):
		// hero-sms V2 也会派回 object 形式：
		//   "sms":{"dateTime":"...","code":"804473","text":"Your OpenAI verification code is: 804473"}
		// 先取 object 的子串（到匹配的右花括号），再在子串里找 code/text。
		obj := extractFirstJSONObject(rest)
		if obj == "" {
			return ""
		}
		if code := extractStringField(obj, "code"); code != "" {
			return code
		}
		if text := extractStringField(obj, "text"); text != "" {
			return digitsBetween(text, 4, 8)
		}
		return ""
	}
	return ""
}

// extractFirstJSONObject 抠出 s 起始位置的 {...} 子串（含两端花括号），
// 不严格地按字符匹配花括号深度，遇引号内的 { } 不计数。
func extractFirstJSONObject(s string) string {
	if !strings.HasPrefix(s, "{") {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' && inStr {
			esc = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return ""
}

// extractStringField 在简短 JSON 子串里取 "<field>":"<value>" 字符串值。
func extractStringField(s, field string) string {
	pat := `"` + field + `":"`
	idx := strings.Index(s, pat)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(pat):]
	end := strings.IndexByte(rest, '"')
	if end <= 0 {
		return ""
	}
	return rest[:end]
}

// digitsBetween 在 s 中找首个长度落在 [min,max] 的连续数字串（贪婪取最长）。
//
// 用于从 SMS text "Your OpenAI verification code is: 804473" 中拿 OTP。
func digitsBetween(s string, minLen, maxLen int) string {
	best := ""
	cur := ""
	flush := func() {
		if len(cur) >= minLen && len(cur) <= maxLen && len(cur) > len(best) {
			best = cur
		}
		cur = ""
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur += string(r)
			continue
		}
		flush()
	}
	flush()
	return best
}

func snippet(b []byte) string {
	const max = 200
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
