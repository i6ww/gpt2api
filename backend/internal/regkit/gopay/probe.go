package gopay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/regkit/browser"
)

// MeInfo 是 /backend-api/me 关心的字段子集，用于 preflight 诊断账号 region。
type MeInfo struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Country  string `json:"country"`
	Object   string `json:"object"`
	PhoneNum string `json:"phone_number"`

	Status   int    `json:"-"`
	RawShort string `json:"-"`
}

// ProbeConfig ProbeMe 的入参。
type ProbeConfig struct {
	ProxyURL    string
	AccessToken string
	Cookies     string
	DeviceID    string
	UserAgent   string
	Timeout     time.Duration
}

// ProbeMe 通过指定代理调一次 chatgpt.com/backend-api/me，返回账号 country/email/id。
//
// 用途：在 GoPay 流程开始前先 fail-fast，避免 country != "ID" 的账号浪费钱包配额
// （非印尼账号 OpenAI 会在 Step 4 approve 直接 blocked）。
//
// 鉴权与 GoPay charger 一致：Authorization Bearer + OAI-Device-Id + 可选 Cookie。
// 不依赖 cookie jar；走 browser.Client 的 uTLS 通道，跟最终 approve 同一通道。
func ProbeMe(ctx context.Context, cfg ProbeConfig) (*MeInfo, error) {
	if cfg.AccessToken == "" {
		return nil, errors.New("ProbeMe: access_token required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cl, err := browser.New(browser.Options{ProxyURL: cfg.ProxyURL, Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("probe browser init: %w", err)
	}
	if cfg.UserAgent != "" {
		cl.Profile.UserAgent = cfg.UserAgent
	}

	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet,
		"https://chatgpt.com/backend-api/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	if cfg.Cookies != "" {
		req.Header.Set("Cookie", cfg.Cookies)
	}
	if cfg.DeviceID != "" {
		req.Header.Set("OAI-Device-Id", cfg.DeviceID)
	}
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/")

	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET /backend-api/me: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	info := &MeInfo{Status: resp.StatusCode, RawShort: shortBody(raw, 300)}
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("HTTP %d body=%s", resp.StatusCode, shortBody(raw, 200))
	}
	var data map[string]any
	if jerr := json.Unmarshal(raw, &data); jerr != nil {
		return info, fmt.Errorf("decode me: %s", shortBody(raw, 200))
	}
	info.ID = jsonString(data, "id")
	info.Email = jsonString(data, "email")
	info.Name = jsonString(data, "name")
	info.Country = strings.ToUpper(jsonString(data, "country"))
	info.Object = jsonString(data, "object")
	info.PhoneNum = jsonString(data, "phone_number")
	return info, nil
}
