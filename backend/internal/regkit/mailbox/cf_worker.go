package mailbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/model"
)

// CFWorkerBackend Cloudflare Worker 自建邮箱网关。
//
// 协议参考 e:\2026\gptauto\auto 实现：
//
//	GET {worker_domain}/api/mails  Authorization: Bearer <jwt>
//
// jwt 通过 secrets.RefreshToken 注入。
type CFWorkerBackend struct{}

// NewCFWorkerBackend 构造。
func NewCFWorkerBackend() *CFWorkerBackend {
	return &CFWorkerBackend{}
}

// Name 实现 Backend。
func (b *CFWorkerBackend) Name() string { return model.MailModeCF }

// Open 打开会话。
//
// CF Worker 拉邮件也走注册任务的代理，避免来源 IP 与注册请求不一致。
func (b *CFWorkerBackend) Open(ctx context.Context, m *model.MailPool, secrets Secrets, cfg BackendConfig) (Mailbox, error) {
	domain := strings.TrimRight(cfg.CFWorkerDomain, "/")
	if domain == "" {
		return nil, errors.New("cf_worker: 缺少 worker_domain（请在系统配置 → 邮箱配置 → CF Worker 中填写）")
	}
	if secrets.RefreshToken == "" {
		return nil, errors.New("cf_worker: 缺少 jwt（mail_pool 行的 refresh_token 字段未填）")
	}
	return &cfWorkerMailbox{
		http: HTTPClientWithProxy(cfg.Proxy, 30*time.Second),
		// 用户的 CF Worker（参考 jzqkwl/temp-email-api）/api/mails 是分页接口，
		// 必须带 limit/offset 否则 400 "Invalid limit"。20 条足够拉到任意 OTP（实测 OTP
		// 邮件总会出现在最近 1-2 封内）。
		mailURL: domain + "/api/mails?limit=20&offset=0",
		jwt:     secrets.RefreshToken,
		email:   m.Email,
	}, nil
}

type cfWorkerMailbox struct {
	http    *http.Client
	mailURL string
	jwt     string
	email   string
}

func (m *cfWorkerMailbox) Close() error { return nil }

func (m *cfWorkerMailbox) WaitCode(ctx context.Context, opts WaitOptions) (string, error) {
	opts.normalize()
	deadline := time.Now().Add(opts.Timeout)
	seen := map[string]struct{}{}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		mails, err := m.fetch(ctx)
		if err == nil {
			log.Printf("[mailbox cf] addr=%s provider=%s fetched=%d mails", m.email, opts.Provider, len(mails))
			for _, mail := range mails {
				id := pickStr(mail, "id")
				if id == "" {
					continue
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				ts := pickFloat(mail, "created_at", "createdAt")
				sender := pickStr(mail, "source", "from")
				subject := pickStr(mail, "subject")
				toAddr := pickStr(mail, "to", "destination", "address")
				body := pickStr(mail, "raw", "html", "text", "body")
				log.Printf("[mailbox cf] mail id=%s ts=%d to=%s from=%s subject=%q body_len=%d",
					id, int64(ts), toAddr, sender, subject, len(body))
				// 把整段 body 分块打出来；OpenAI 邮件 body 通常 5~10KB。
				for off := 0; off < len(body); off += 800 {
					end := off + 800
					if end > len(body) {
						end = len(body)
					}
					log.Printf("[mailbox cf] body[%d:%d]=%q", off, end, body[off:end])
				}
				if !opts.SinceTS.IsZero() && ts > 0 {
					if time.Unix(int64(ts), 0).Before(opts.SinceTS) {
						log.Printf("[mailbox cf] skip mail id=%s: ts=%d before since=%s", id, int64(ts), opts.SinceTS.Format(time.RFC3339))
						continue
					}
				}
				if !MatchSender(opts.Provider, sender, subject) {
					log.Printf("[mailbox cf] skip mail id=%s: MatchSender(%s) false", id, opts.Provider)
					continue
				}
				if code, ok := ExtractCode(opts.Provider, subject, body); ok {
					log.Printf("[mailbox cf] extracted code=%s from mail id=%s", code, id)
					return code, nil
				}
				log.Printf("[mailbox cf] mail id=%s matched sender but no code extracted", id)
			}
		} else {
			log.Printf("[mailbox cf] fetch error: %v", err)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(opts.PollInterval):
		}
	}
	return "", ErrCodeNotFound
}

// 用户的 CF Worker 域名（如 temp-email-api.jzqkwl.com）默认开启 Cloudflare bot management。
// Go 默认 UA 是 "Go-http-client/1.1"，会被 CF 直接 403（error_code 1010 browser_signature_banned），
// 邮件取不下来导致 MFA OTP 永远等不到。下面这组 header 模拟一个普通的现代浏览器请求，
// CF 的启发式不会再拦截。
var cfWorkerBrowserHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Accept":          "application/json, text/plain, */*",
	"Accept-Language": "en-US,en;q=0.9",
	"Sec-Ch-Ua":       `"Chromium";v="131", "Not_A Brand";v="24", "Google Chrome";v="131"`,
	"Sec-Ch-Ua-Mobile":   "?0",
	"Sec-Ch-Ua-Platform": `"Windows"`,
	"Sec-Fetch-Dest":     "empty",
	"Sec-Fetch-Mode":     "cors",
	"Sec-Fetch-Site":     "same-origin",
}

// cfWorkerMintHeaders 仅用于 POST /admin/new_address：服务端发出的 JSON 请求若伪造
// Sec-Fetch-Site: same-origin 会与真实跨站语义不符；部分 Cloudflare / Worker 组合下宁可少带头也不造假。
var cfWorkerMintHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Accept":          "application/json, text/plain, */*",
	"Accept-Language": "en-US,en;q=0.9",
}

func (m *cfWorkerMailbox) fetch(ctx context.Context) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.mailURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.jwt)
	for k, v := range cfWorkerBrowserHeaders {
		req.Header.Set(k, v)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cf_worker HTTP %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		if v, ok := obj["results"].([]any); ok {
			return castSlice(v), nil
		}
	}
	return nil, errors.New("cf_worker: 响应无法解析为邮件列表")
}

// MintCFAddress 调用自建 CF Worker 的 POST /admin/new_address 即时签发一封新邮箱。
//
// 因为 worker 是用户自有 + admin password 在手 → 邮箱地址是无限资源，调用次数
// 只受 worker 的 KV 写入速率限制（单地区 ~1k qps 量级），所以 dispatcher 走
// "即时模式"时每个注册任务都现签现用，完全不入 mail_pool。
//
// 与原 service.callCFNewAddress 同源，保留在 mailbox 包以避免反向依赖。
func MintCFAddress(ctx context.Context, hc *http.Client, worker, adminPwd, name, domain string, enablePrefix bool) (string, string, error) {
	worker = strings.TrimRight(strings.TrimSpace(worker), "/")
	adminPwd = strings.TrimSpace(adminPwd)
	if worker == "" {
		return "", "", errors.New("cf_worker: 缺少 worker_domain")
	}
	if adminPwd == "" {
		return "", "", errors.New("cf_worker: 缺少 admin_password")
	}
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	// 注意：many Cloudflare temp-email Workers 校验 domain 必须与 Worker 控制台配置的收件域一致；
	// JSON 里带 "domain":"" 会与「缺省域名」语义不同 → 易被直接 400。
	bodyMap := map[string]any{
		"enablePrefix": enablePrefix,
		"name":         name,
	}
	domain = strings.TrimSpace(domain)
	if domain != "" {
		bodyMap["domain"] = domain
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, worker+"/admin/new_address", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-admin-auth", adminPwd)
	// CF bot management 会拦 Go 默认 UA；mint 用精简浏览器头，避免伪造 Sec-Fetch-Site: same-origin。
	for k, v := range cfWorkerMintHeaders {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		short := strings.TrimSpace(string(raw))
		if len(short) > 600 {
			short = short[:600] + "…"
		}
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, short)
	}
	var data struct {
		Address string `json:"address"`
		JWT     string `json:"jwt"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", "", fmt.Errorf("解析响应失败：%v", err)
	}
	if data.Address == "" || data.JWT == "" {
		return "", "", fmt.Errorf("响应缺少 address/jwt：%s", string(raw))
	}
	return data.Address, data.JWT, nil
}

// RandomCFName 生成 CF 邮箱本地部分 — 全小写字母 + 1-2 个数字穿插。
//
// n 默认 12（够长避免撞库）；建议 ≥ 8。
func RandomCFName(n int) string {
	if n < 4 {
		n = 4
	}
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[secureIntn(len(letters))]
	}
	digits := 1
	if n >= 10 {
		digits = 2
	}
	for j := 0; j < digits; j++ {
		pos := 2 + secureIntn(n-2)
		b[pos] = byte('0' + secureIntn(10))
	}
	return string(b)
}

// secureIntn 从 crypto/rand 取 [0, n)；失败回退 math/rand。
func secureIntn(n int) int {
	if n <= 0 {
		return 0
	}
	maxN := int64(n)
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err == nil {
		v := int64(0)
		for _, by := range buf {
			v = (v << 8) | int64(by)
		}
		if v < 0 {
			v = -v
		}
		return int(v % maxN)
	}
	return mathrand.Intn(n) //nolint:gosec
}
