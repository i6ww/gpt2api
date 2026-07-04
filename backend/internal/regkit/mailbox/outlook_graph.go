package mailbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/model"
)

// OutlookGraphBackend 通过 Microsoft Graph API 拉取邮件。
type OutlookGraphBackend struct{}

// NewOutlookGraphBackend 构造。
func NewOutlookGraphBackend() *OutlookGraphBackend {
	return &OutlookGraphBackend{}
}

// Name 实现 Backend。
func (b *OutlookGraphBackend) Name() string { return model.MailModeOutlookGraph }

// Open 通过 client_id + refresh_token 换取 access_token，构造 Graph 客户端。
//
// 关键决策：Microsoft 端点（login.microsoftonline.com / graph.microsoft.com）
// **直连**，不走注册任务的代理。原因：
//
//  1. 风控独立性：Outlook 邮箱只是"被动收件箱"，IP 不参与 OpenAI / Adobe / Grok
//     的注册风控判定（这些 provider 看的是浏览器流程的 IP，跟微软收邮件 IP 毫无
//     耦合）。早期注释里那个"反常信号"担忧是过度防御。
//
//  2. 商用代理（IPRoyal / Lumi / ArxLabs 等）对 login.microsoftonline.com 的
//     HTTPS CONNECT 隧道极不稳定，10 并发即频繁出现 `Post ...: EOF`。一个任务
//     240s 内对 graph.microsoft.com 拉取 80 次，命中失败概率几乎是 100%。
//     直连机器出口反而稳定（实测 token 接口 600ms / Graph list 1.5s）。
//
// 如果有"必须用代理出口收 Outlook 邮件"的特殊场景（例如调试用代理观察），
// 可以在系统配置里加个 outlook.use_proxy 开关，默认 false。
func (b *OutlookGraphBackend) Open(ctx context.Context, m *model.MailPool, secrets Secrets, cfg BackendConfig) (Mailbox, error) {
	if m.ClientID == "" {
		return nil, errors.New("outlook graph: mail_pool 行缺少 client_id")
	}
	if secrets.RefreshToken == "" {
		return nil, errors.New("outlook graph: mail_pool 行缺少 refresh_token")
	}
	scope := strings.TrimSpace(cfg.OutlookScopeGraph)
	if scope == "" {
		scope = "https://graph.microsoft.com/Mail.Read offline_access"
	}
	hc := HTTPClientWithProxy("", 30*time.Second)
	at, err := refreshOutlookAccessToken(ctx, hc, m.ClientID, secrets.RefreshToken, scope)
	if err != nil {
		return nil, fmt.Errorf("outlook graph: 刷新 access_token 失败（直连 microsoftonline）：%w", err)
	}
	return &outlookGraphMailbox{
		http:        hc,
		accessToken: at,
		email:       m.Email,
	}, nil
}

// refreshOutlookAccessToken 用 refresh_token 换 access_token。
func refreshOutlookAccessToken(ctx context.Context, c *http.Client, clientID, refreshToken, scope string) (string, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")
	form.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://login.microsoftonline.com/common/oauth2/v2.0/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var r struct {
		AccessToken      string `json:"access_token"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("响应非 JSON: %s", strings.TrimSpace(string(raw)))
	}
	if r.AccessToken == "" {
		return "", fmt.Errorf("响应缺少 access_token: %s", r.ErrorDescription)
	}
	return r.AccessToken, nil
}

// === graph mailbox ===

type outlookGraphMailbox struct {
	http        *http.Client
	accessToken string
	email       string
}

func (m *outlookGraphMailbox) Close() error { return nil }

const graphMailURLTpl = `https://graph.microsoft.com/v1.0/me/mailFolders('%s')/messages?$top=15&$orderby=receivedDateTime desc&$select=subject,from,receivedDateTime,body,bodyPreview,id`

func (m *outlookGraphMailbox) WaitCode(ctx context.Context, opts WaitOptions) (string, error) {
	opts.normalize()
	deadline := time.Now().Add(opts.Timeout)
	seen := map[string]struct{}{}
	folders := []string{"Inbox", "JunkEmail"}
	pollIdx := 0
	var lastFetchErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		pollIdx++
		for _, folder := range folders {
			msgs, err := m.fetchFolder(ctx, folder)
			if err != nil {
				lastFetchErr = err
				// 失败必须可见：以前静默 continue 导致"等了 4 分钟没邮件"看不出代理/
				// MS 风控等真因。每 5 轮打一次（首轮一定打），避免 240s × 0.33Hz 刷屏。
				if pollIdx == 1 || pollIdx%5 == 0 {
					log.Printf("[mailbox graph] poll#%d email=%s folder=%s fetch FAILED: %v",
						pollIdx, m.email, folder, err)
				}
				continue
			}
			if pollIdx == 1 {
				log.Printf("[mailbox graph] poll#%d email=%s folder=%s got %d msgs",
					pollIdx, m.email, folder, len(msgs))
			}
			for _, msg := range msgs {
				id, _ := msg["id"].(string)
				if id == "" {
					continue
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				rtRaw, _ := msg["receivedDateTime"].(string)
				if !opts.SinceTS.IsZero() && rtRaw != "" {
					if t, err := time.Parse(time.RFC3339, rtRaw); err == nil && t.Before(opts.SinceTS) {
						continue
					}
				}
				subject, _ := msg["subject"].(string)
				sender := ""
				if from, ok := msg["from"].(map[string]any); ok {
					if ea, ok := from["emailAddress"].(map[string]any); ok {
						sender, _ = ea["address"].(string)
					}
				}
				body := ""
				if b, ok := msg["body"].(map[string]any); ok {
					body, _ = b["content"].(string)
				}
				if body == "" {
					body, _ = msg["bodyPreview"].(string)
				}
				if !MatchSender(opts.Provider, sender, subject) {
					log.Printf("[mailbox graph] poll#%d email=%s skip msg (%s/%s) MatchSender=false: from=%s subj=%q",
						pollIdx, m.email, folder, rtRaw, sender, truncate(subject, 60))
					continue
				}
				if code, ok := ExtractCode(opts.Provider, subject, body); ok {
					log.Printf("[mailbox graph] poll#%d email=%s ✅ EXTRACTED code=%s from=%s subj=%q rt=%s",
						pollIdx, m.email, code, sender, truncate(subject, 60), rtRaw)
					return code, nil
				}
				log.Printf("[mailbox graph] poll#%d email=%s match sender but ExtractCode FAIL: from=%s subj=%q body_len=%d",
					pollIdx, m.email, sender, truncate(subject, 60), len(body))
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(opts.PollInterval):
		}
	}
	// 超时退出前打一次终态，方便看是"全程 fetch 失败"还是"fetch 成功但无匹配邮件"
	if lastFetchErr != nil {
		log.Printf("[mailbox graph] WaitCode timeout email=%s polls=%d (last fetch err: %v)",
			m.email, pollIdx, lastFetchErr)
	} else {
		log.Printf("[mailbox graph] WaitCode timeout email=%s polls=%d (fetch ok, no matching mail)",
			m.email, pollIdx)
	}
	return "", ErrCodeNotFound
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (m *outlookGraphMailbox) fetchFolder(ctx context.Context, folder string) ([]map[string]any, error) {
	u := fmt.Sprintf(graphMailURLTpl, folder)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		snippet := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\n", " "), "\r", " ")
		return nil, fmt.Errorf("graph HTTP %d body=%s", resp.StatusCode, snippet)
	}
	var body struct {
		Value []map[string]any `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Value, nil
}

// 让 bytes 不被 unused 抱怨（保留备用）。
var _ = bytes.NewBuffer
