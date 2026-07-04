// Package mailbox 注册流程中"接收验证码邮件"的统一抽象。
//
// 4 种 backend：
//
//   - outlook_graph    Microsoft Graph API（推荐：长期可用）
//   - outlook_imap     IMAP + XOAUTH2（fallback）
//   - tempmail         第三方临时邮箱 HTTP API（一次性）
//   - cf_worker        Cloudflare Worker 自建邮箱网关
//
// 调用方流程：
//
//	box, err := mb.Open(ctx, mailRow, sysCfg)        // 打开邮箱（注入 access_token / jwt 等）
//	defer box.Close()
//	code, err := box.WaitCode(ctx, opts)             // 阻塞轮询直到拿到验证码或超时
package mailbox

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/model"
	"golang.org/x/net/proxy"
)

// Provider 注册目标，用来挑选合适的 sender / subject 过滤规则。
type Provider string

const (
	ProviderAdobe Provider = "adobe"
	ProviderGrok  Provider = "grok"
	ProviderGPT   Provider = "gpt"
)

// WaitOptions 控制单次轮询。
type WaitOptions struct {
	Provider     Provider
	SinceTS      time.Time     // 只关心这个时刻之后的邮件
	Timeout      time.Duration // 总等待超时（默认 180s）
	PollInterval time.Duration // 轮询间隔（默认 3s）
}

func (o *WaitOptions) normalize() {
	if o.Timeout <= 0 {
		o.Timeout = 180 * time.Second
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 3 * time.Second
	}
}

// Mailbox 单个邮箱的会话。具体实现可能持有 OAuth access_token、IMAP 连接等。
type Mailbox interface {
	WaitCode(ctx context.Context, opts WaitOptions) (string, error)
	Close() error
}

// Secrets mail_pool 行解密后的明文。由 service 层在 Open 之前准备好注入。
type Secrets struct {
	Password     string // 邮箱密码（IMAP 基础认证可能用）
	RefreshToken string // outlook OAuth refresh_token / tempmail jwt
}

// Backend 邮箱 backend 工厂。
type Backend interface {
	Name() string
	// Open 给定一个 mail_pool 行打开会话；secrets 是 service 层 AES 解密后的明文。
	Open(ctx context.Context, m *model.MailPool, secrets Secrets, cfg BackendConfig) (Mailbox, error)
}

// BackendConfig 由 service 层封装好后传进来的"系统配置"快照，
// 避免 Backend 反向依赖 service 层，也避免每次 Open 都重新读 DB。
type BackendConfig struct {
	// DefaultMode 系统配置里"默认收件后端"的选择，控制 AcquireFresh 走 CF 即时
	// 签发还是走 mail_pool 池化。可能值：
	//   - outlook_graph / outlook_imap → 从 mail_pool 拿 Outlook 行
	//   - tempmail                     → 走 tempmail（已预生成在池里）
	//   - cf                           → 走 CF Worker 即时签发（无需预生成）
	// 空串视为 outlook_graph（与 service 层默认一致）。
	DefaultMode string

	// Outlook
	OutlookMode       string // imap / graph
	OutlookScopeIMAP  string
	OutlookScopeGraph string
	// Tempmail
	TempmailBase           string
	TempmailNewAddressPath string
	TempmailMailsPath      string
	TempmailAddressName    string
	TempmailAddressDomains []string
	// CF Worker
	CFWorkerDomain  string
	CFEmailDomain   string
	CFAdminPassword string

	Proxy string // 可选，传给 backend 内部 HTTP client（IMAP 例外）
}

// 通用 OTP 提取规则。
//
// Adobe / GPT / Grok 都用「6 位数字」或「3-3」结构。
var (
	rePlain6      = regexp.MustCompile(`(?:^|[^0-9])([0-9]{6})(?:[^0-9]|$)`)
	reGrokDashed  = regexp.MustCompile(`\b([A-Z0-9]{3}-[A-Z0-9]{3})\b`)
	reAdobeKwEN   = regexp.MustCompile(`(?is)(?:verification|one-time|code|otp)[^0-9]{0,80}([0-9]{6})`)
	reAdobeKwZH   = regexp.MustCompile(`(?s)(?:验证码|验证)[^0-9]{0,40}([0-9]{6})`)
	reHTMLStripper = regexp.MustCompile(`<[^>]+>`)

	// OpenAI HTML 邮件 OTP 块的固定 CSS 特征：6 位 OTP 总是包在
	// background-color:#F3F3F3 + 24px font-size 的 <p> 容器里，前后 800 字节内
	// 出现且只出现真 OTP。raw 邮件 body 还带 MIME header 里的 6 位数字（如
	// "m=+846533.4675..."），所以必须借助这个稳定锚点定位。
	reGPTOTPBlock = regexp.MustCompile(`(?is)background-color:\s*#F3F3F3[\s\S]{0,800}?([0-9]{6})\b`)
	// 兜底：在 HTML 内的纯文本里找 "verification code" 关键词附近的 6 位数字
	// （Go RE2 最大重复是 1000，所以跨度严格小于 1000）。
	reGPTKwEN = regexp.MustCompile(`(?is)(?:verification\s+code|temporary\s+code|one-?time\s+code|your\s+code\s+is)[\s\S]{0,800}?([0-9]{6})\b`)
	// 最备用：subject 行常见 "ChatGPT — 123456 is your verification code"。
	reGPTSubject = regexp.MustCompile(`(?i)([0-9]{6})\s+is\s+your\s+(?:verification|chatgpt|openai)`)
)

// ExtractCode 从 (subject, body) 中按 provider 类型抽取验证码。
// body 允许是 HTML 原文，会自动剥标签。
func ExtractCode(provider Provider, subject, body string) (string, bool) {
	clean := reHTMLStripper.ReplaceAllString(body, " ")
	hay := []string{subject, clean}
	switch provider {
	case ProviderGrok:
		for _, h := range hay {
			if m := reGrokDashed.FindStringSubmatch(h); len(m) > 1 {
				code := strings.ReplaceAll(m[1], "-", "")
				if code != "" {
					return code, true
				}
			}
		}
	case ProviderAdobe:
		for _, re := range []*regexp.Regexp{reAdobeKwEN, reAdobeKwZH} {
			for _, h := range hay {
				if m := re.FindStringSubmatch(h); len(m) > 1 && m[1] != "000000" {
					return m[1], true
				}
			}
		}
	case ProviderGPT:
		// 1) subject 行（最稳）。
		for _, h := range hay {
			if m := reGPTSubject.FindStringSubmatch(h); len(m) > 1 && m[1] != "000000" {
				return m[1], true
			}
		}
		// 2) OpenAI HTML 邮件 OTP 容器的 CSS 锚点（在原始 raw body 里找）。
		if m := reGPTOTPBlock.FindStringSubmatch(body); len(m) > 1 && m[1] != "000000" {
			return m[1], true
		}
		// 3) 关键词兜底：先把 body 截到 <html> 之后，去掉 MIME headers 里的
		//    干扰 6 位数字（如 "m=+846533" / "client-ip=159.183..."）。
		htmlOnly := body
		if i := strings.Index(strings.ToLower(htmlOnly), "<html"); i >= 0 {
			htmlOnly = htmlOnly[i:]
		}
		htmlClean := reHTMLStripper.ReplaceAllString(htmlOnly, " ")
		for _, h := range []string{subject, htmlClean} {
			if m := reGPTKwEN.FindStringSubmatch(h); len(m) > 1 && m[1] != "000000" {
				return m[1], true
			}
		}
	}
	// 通用兜底：6 位数字
	for _, h := range hay {
		if m := rePlain6.FindStringSubmatch(h); len(m) > 1 && m[1] != "000000" && m[1] != "177010" {
			return m[1], true
		}
	}
	return "", false
}

// MatchSender 根据 provider 判断发件人 / 主题是否相关。
func MatchSender(provider Provider, sender, subject string) bool {
	s := strings.ToLower(sender + " " + subject)
	switch provider {
	case ProviderAdobe:
		return strings.Contains(s, "adobe")
	case ProviderGrok:
		return strings.Contains(s, "x.ai") || strings.Contains(s, "xai") ||
			strings.Contains(s, "grok") || strings.Contains(s, "verification") ||
			strings.Contains(s, "confirmation code")
	case ProviderGPT:
		return strings.Contains(s, "openai.com") || strings.Contains(s, "tm.openai") ||
			strings.Contains(s, "chatgpt") || strings.Contains(s, "verify your email")
	}
	return false
}

// ErrCodeNotFound 等待结束但没拿到验证码。
var ErrCodeNotFound = errors.New("mailbox: 验证码邮件未在超时时间内到达")

// HTTPClientWithProxy 构造一个带代理的 net/http.Client，用于邮箱 backend
// 拉取验证码邮件 + Microsoft / 临时邮箱 / CF Worker 的 OAuth 调用。
//
// 必须确保所有外发流量都从注册任务选定的代理出去，否则 Microsoft / Google
// 会发现"邮箱拉取来自服务器 IP 而注册请求来自代理 IP"的不一致信号，
// 进而对刚注册成功的账号下风控。proxyURL 为空时退化成直连 client。
//
// 与 regkit/browser 不同：邮箱 backend 不需要 utls / Chrome 指纹，
// 只要把代理挂在标准 transport 上即可。
func HTTPClientWithProxy(proxyURL string, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if proxyURL == "" {
		return &http.Client{Timeout: timeout}
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return &http.Client{Timeout: timeout}
	}
	dial := (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			pwd, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pwd}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: 15 * time.Second})
		if err != nil {
			return &http.Client{Timeout: timeout}
		}
		dial = func(_ context.Context, network, addr string) (net.Conn, error) {
			return d.Dial(network, addr)
		}
		return &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DialContext:         dial,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 15 * time.Second,
			},
		}
	case "http", "https":
		return &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy:               http.ProxyURL(u),
				DialContext:         dial,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 15 * time.Second,
			},
		}
	}
	return &http.Client{Timeout: timeout}
}
