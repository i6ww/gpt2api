package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// VariantLocalCamoufox 本地 Camoufox solver 的 Variant 名。
const VariantLocalCamoufox = "local"

// LocalCamoufoxSolver 适配自建/参考项目里的 Camoufox HTTP solver：
//
//	GET /turnstile?url=<page>&sitekey=<key>     -> {"taskId":"..."}
//	GET /result?id=<taskId>                     -> {"solution":{"token":"..."}} 或 "CAPTCHA_NOT_READY"
//
// 默认监听 http://127.0.0.1:5072。仅支持 Turnstile；Arkose 直接返回未配置错误。
//
// 本 solver 跑在本机 → grok.com 的真实出口仍由注册流水线自身代理决定，
// 这里 RoundTrip 都用直连 http.DefaultTransport（绕开 HTTP_PROXY），
// 防止内层 Camoufox 浏览器被代理转一道带来不可预期的指纹/cookie。
type LocalCamoufoxSolver struct {
	Endpoint     string        // 默认 http://127.0.0.1:5072
	HTTP         *http.Client  // 没传时用直连，超时 15s
	PollInterval time.Duration // 默认 2s
	MaxWait      time.Duration // 默认 120s
}

// NewLocalCamoufox 构造。endpoint 为空时回退到 http://127.0.0.1:5072。
func NewLocalCamoufox(endpoint string) *LocalCamoufoxSolver {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:5072"
	}
	return &LocalCamoufoxSolver{
		Endpoint: endpoint,
		HTTP: &http.Client{
			// 显式直连：本机的 Camoufox 不需要走我们配的出口代理。
			Transport: &http.Transport{Proxy: nil},
			Timeout:   15 * time.Second,
		},
		PollInterval: 2 * time.Second,
		MaxWait:      120 * time.Second,
	}
}

// Name 实现 Solver。
func (s *LocalCamoufoxSolver) Name() string { return VariantLocalCamoufox }

// SolveArkose 本地 Camoufox solver 不支持 FunCaptcha。
func (s *LocalCamoufoxSolver) SolveArkose(_ context.Context, _ *ArkoseTask) (string, error) {
	return "", errors.New("local camoufox solver: 不支持 Arkose / FunCaptcha")
}

// SolveTurnstile 创建任务 + 轮询结果，返回 token。
func (s *LocalCamoufoxSolver) SolveTurnstile(ctx context.Context, t *TurnstileTask) (string, error) {
	if s == nil {
		return "", errors.New("local camoufox solver: 实例为空")
	}
	if strings.TrimSpace(t.WebsiteURL) == "" || strings.TrimSpace(t.WebsiteKey) == "" {
		return "", errors.New("local camoufox solver: 缺少 websiteURL / sitekey")
	}
	taskID, err := s.createTask(ctx, t)
	if err != nil {
		return "", err
	}
	return s.pollResult(ctx, taskID)
}

func (s *LocalCamoufoxSolver) createTask(ctx context.Context, t *TurnstileTask) (string, error) {
	q := url.Values{}
	q.Set("url", t.WebsiteURL)
	q.Set("sitekey", t.WebsiteKey)
	if t.UserAgent != "" {
		q.Set("ua", t.UserAgent)
	}
	endpoint := s.Endpoint + "/turnstile?" + q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("local solver create_task 网络异常：%w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("local solver create_task HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", fmt.Errorf("local solver create_task 响应非 JSON：%s", snippet(raw))
	}
	if id, _ := data["taskId"].(string); id != "" {
		return id, nil
	}
	if id, _ := data["task_id"].(string); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("local solver create_task 响应缺 taskId：%s", snippet(raw))
}

func (s *LocalCamoufoxSolver) pollResult(ctx context.Context, taskID string) (string, error) {
	maxWait := s.MaxWait
	if maxWait <= 0 {
		maxWait = 120 * time.Second
	}
	pollInt := s.PollInterval
	if pollInt <= 0 {
		pollInt = 2 * time.Second
	}
	deadline := time.Now().Add(maxWait)
	endpoint := s.Endpoint + "/result?id=" + url.QueryEscape(taskID)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInt):
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		resp, err := s.HTTP.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		text := strings.TrimSpace(string(raw))
		if text == "" || text == "CAPTCHA_NOT_READY" || text == "null" {
			continue
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if !strings.Contains(ct, "json") {
			// 兼容裸字符串 token / 状态字面量。
			if text == "CAPTCHA_FAIL" {
				return "", errors.New("local solver: CAPTCHA_FAIL")
			}
			return text, nil
		}
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			continue
		}
		sol, _ := data["solution"].(map[string]any)
		token, _ := sol["token"].(string)
		if token == "" {
			token, _ = data["token"].(string)
		}
		switch token {
		case "":
			continue
		case "CAPTCHA_NOT_READY":
			continue
		case "CAPTCHA_FAIL":
			return "", errors.New("local solver: CAPTCHA_FAIL")
		}
		return token, nil
	}
	return "", fmt.Errorf("local solver: 等待 token 超时（>%s）", maxWait)
}

func snippet(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
