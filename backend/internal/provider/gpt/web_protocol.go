package gpt

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kleinai/backend/pkg/outbound"
)

const (
	webRequirementsRetryMax = 3
)

// webImageHTTPClient 为 gpt-image-2 web 链路单独建客户端：uTLS + CookieJar（对齐 GoGPTImg）。
func (p *Provider) webImageHTTPClient(proxyURL string) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	base, err := outbound.NewClient(outbound.Options{
		ProxyURL: proxyURL,
		Timeout:  defaultTimeout,
		Mode:     outbound.ModeUTLS,
		Profile:  outbound.ProfileChrome,
	})
	if err != nil {
		if proxyURL == "" {
			return p.client, nil
		}
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("gpt image2 web cookie jar: %w", err)
	}
	base.Jar = jar
	return base, nil
}

// webBootstrap 模拟浏览器打开首页；403/CF 页不致命，尽量从 cookie 取 oai-did（对齐 GoGPTImg）。
// 返回 warn 供上游日志记录，仅 >=500 或网络错误返回 err。
func (p *Provider) webBootstrap(ctx context.Context, client *http.Client, base string, fp *webFP) (warn string, err error) {
	if fp == nil {
		return "", fmt.Errorf("gpt image2 web bootstrap: missing fingerprint")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/", nil)
	if err != nil {
		return "", err
	}
	for k, v := range webBaseHeaders(*fp, "", "") {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	httpReq.Header.Set("Sec-Fetch-Dest", "document")
	httpReq.Header.Set("Sec-Fetch-Mode", "navigate")
	httpReq.Header.Set("Sec-Fetch-Site", "none")
	httpReq.Header.Set("Sec-Fetch-User", "?1")
	httpReq.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("gpt image2 web bootstrap: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	fp.DeviceID = resolveOAIDeviceID(client, base, resp, fp.DeviceID)

	switch {
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("gpt image2 web bootstrap %d: %s", resp.StatusCode, snippet(raw, 320))
	case resp.StatusCode >= 400:
		return fmt.Sprintf("bootstrap HTTP %d (continuing with device_id=%s)", resp.StatusCode, fp.DeviceID), nil
	default:
		return "", nil
	}
}

func resolveOAIDeviceID(client *http.Client, base string, resp *http.Response, preset string) string {
	if preset = strings.TrimSpace(preset); preset != "" {
		return preset
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "oai-did" && strings.TrimSpace(cookie.Value) != "" {
			return strings.TrimSpace(cookie.Value)
		}
	}
	if client != nil && client.Jar != nil {
		if u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/")); err == nil {
			for _, cookie := range client.Jar.Cookies(u) {
				if cookie.Name == "oai-did" && strings.TrimSpace(cookie.Value) != "" {
					return strings.TrimSpace(cookie.Value)
				}
			}
		}
	}
	return uuid.NewString()
}

func (p *Provider) webRequirementsWithRetry(ctx context.Context, client *http.Client, base string, fp webFP, token string) (webRequirement, error) {
	var lastErr error
	for attempt := 1; attempt <= webRequirementsRetryMax; attempt++ {
		reqs, err := p.webRequirements(ctx, client, base, fp, token)
		if err == nil {
			return reqs, nil
		}
		lastErr = err
		if !isRetriableWebStepErr(err) || attempt == webRequirementsRetryMax {
			break
		}
		delay := time.Duration(attempt) * 400 * time.Millisecond
		select {
		case <-ctx.Done():
			return webRequirement{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	return webRequirement{}, lastErr
}

func isRetriableWebStepErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, " 401:") ||
		strings.Contains(msg, " 403:") ||
		strings.Contains(msg, " 429:") ||
		strings.Contains(msg, " 500:") ||
		strings.Contains(msg, " 502:") ||
		strings.Contains(msg, " 503:") ||
		strings.Contains(msg, " 504:")
}
