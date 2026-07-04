package flowmusic

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// UpstreamHTTPError 上游非 2xx 响应。
type UpstreamHTTPError struct {
	Operation  string
	StatusCode int
	Body       string
}

func (e *UpstreamHTTPError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("%s: HTTP %d", e.Operation, e.StatusCode)
	}
	if len(body) > 300 {
		body = body[:300]
	}
	return fmt.Sprintf("%s: HTTP %d %s", e.Operation, e.StatusCode, body)
}

// AuthError token 鉴权失败（401/403）→ 上层应换号 / 重新刷新 Bearer。
type AuthError struct {
	StatusCode int
	Message    string
}

func (e *AuthError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("FlowMusic 鉴权失败 (HTTP %d)", e.StatusCode)
}

// classifyHTTPError 把 UpstreamHTTPError 在 401/403 时升级成 AuthError。
func classifyHTTPError(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *UpstreamHTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden {
			return &AuthError{StatusCode: httpErr.StatusCode, Message: fmt.Sprintf("FlowMusic 鉴权失败 (HTTP %d)", httpErr.StatusCode)}
		}
	}
	return err
}

// IsAuthFailure 判断是否 401/403 鉴权失败。
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return true
	}
	var httpErr *UpstreamHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "http 401") || strings.Contains(text, "http 403") ||
		strings.Contains(text, "unauthorized") || strings.Contains(text, "forbidden")
}
