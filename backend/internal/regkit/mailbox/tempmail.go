package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/model"
)

// TempmailBackend 第三方临时邮箱 HTTP API（jzqkwl 系列）。
type TempmailBackend struct{}

// NewTempmailBackend 构造。
func NewTempmailBackend() *TempmailBackend {
	return &TempmailBackend{}
}

// Name 实现 Backend。
func (b *TempmailBackend) Name() string { return model.MailModeTempmail }

// Open 打开会话。tempmail 的 jwt 通过 secrets.RefreshToken 注入。
// 拉邮件也通过注册任务选定的代理出口。
func (b *TempmailBackend) Open(ctx context.Context, m *model.MailPool, secrets Secrets, cfg BackendConfig) (Mailbox, error) {
	if cfg.TempmailBase == "" {
		return nil, errors.New("tempmail: 缺少 api_base_url（请在系统配置 → 邮箱配置 → 临时邮箱 API 中填写）")
	}
	mailsURL, err := joinURL(cfg.TempmailBase, cfg.TempmailMailsPath)
	if err != nil {
		return nil, fmt.Errorf("tempmail: 收件路径无效：%w", err)
	}
	return &tempmailMailbox{
		http:     HTTPClientWithProxy(cfg.Proxy, 30*time.Second),
		mailsURL: mailsURL,
		jwt:      secrets.RefreshToken,
		email:    m.Email,
	}, nil
}

type tempmailMailbox struct {
	http     *http.Client
	mailsURL string
	jwt      string
	email    string
}

func (m *tempmailMailbox) Close() error { return nil }

func (m *tempmailMailbox) WaitCode(ctx context.Context, opts WaitOptions) (string, error) {
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
			for _, mail := range mails {
				id := pickStr(mail, "id", "uid")
				if id == "" {
					id = fmt.Sprint(mail)
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				if !opts.SinceTS.IsZero() {
					if ts := pickFloat(mail, "created_at", "createdAt", "date"); ts > 0 {
						if time.Unix(int64(ts), 0).Before(opts.SinceTS) {
							continue
						}
					}
				}
				sender := pickStr(mail, "source", "from", "sender", "from_address")
				subject := pickStr(mail, "subject", "title")
				body := pickStr(mail, "raw", "text", "body", "html", "content")
				if !MatchSender(opts.Provider, sender, subject) {
					continue
				}
				if code, ok := ExtractCode(opts.Provider, subject, body); ok {
					return code, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(opts.PollInterval):
		}
	}
	return "", ErrCodeNotFound
}

func (m *tempmailMailbox) fetch(ctx context.Context) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.mailsURL, nil)
	if err != nil {
		return nil, err
	}
	if m.jwt != "" {
		req.Header.Set("Authorization", "Bearer "+m.jwt)
	}
	// tempmail 服务也常挂在 Cloudflare 后面；Go 默认 UA 会被 1010 拦截。
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tempmail: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
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
	return nil, errors.New("tempmail: 响应无法解析为邮件列表")
}

// === helpers ===

func joinURL(base, path string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	if path == "" {
		return u.String(), nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// 路径里可能带 query
	parts := strings.SplitN(path, "?", 2)
	u.Path = parts[0]
	if len(parts) == 2 {
		u.RawQuery = parts[1]
	}
	return u.String(), nil
}

// pickStr 从 map 里依次按 keys 取值并转成 string。
//
// 关键修复：CF Worker / tempmail 的 mail.id 是 JSON 整数（例如 6268），
// 之前只支持 string 取值，会直接返回 ""，导致 WaitCode 把每封邮件当"id 缺失"
// 跳过，邮件其实早已到达却查不到 OTP，最终 timeout。这里把数字也吃下来。
func pickStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			if x != "" {
				return x
			}
		case float64:
			if x != 0 {
				return strconv.FormatFloat(x, 'f', -1, 64)
			}
		case int:
			if x != 0 {
				return strconv.Itoa(x)
			}
		case int64:
			if x != 0 {
				return strconv.FormatInt(x, 10)
			}
		case bool:
			return strconv.FormatBool(x)
		}
	}
	return ""
}

func pickFloat(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case int64:
				return float64(n)
			case int:
				return float64(n)
			}
		}
	}
	return 0
}

func castSlice(in []any) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, x := range in {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

