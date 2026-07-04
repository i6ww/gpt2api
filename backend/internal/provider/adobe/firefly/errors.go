// Package firefly 是 Adobe Firefly 3p 生成网关的底层客户端。
//
// 这一层只关心和上游 firefly-3p.ff.adobe.io 的 HTTP 协议，不关心账号/池/计费。
// 调用者负责提供有效的 access_token + 可选 proxy。
//
// 端口自 newbanana/backend/internal/adobe，保留全部生产经验（含 anti-bot header、
// x-nonce 计算、payload candidate fallback、错误分类等），只做最小化的包路径调整。
package firefly

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AuthError 401/403 — token 鉴权失败。
type AuthError struct {
	StatusCode int
	Message    string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("auth error %d: %s", e.StatusCode, e.Message)
}

// ProviderBlockedError 上游模型提供方主动拒绝（x-access-error=blocked_by_3p_model_provider）。
type ProviderBlockedError struct {
	StatusCode  int
	Message     string
	AccessError string
}

func (e *ProviderBlockedError) Error() string {
	return fmt.Sprintf("provider blocked %d: %s", e.StatusCode, e.Message)
}

// QuotaExhaustedError 账号配额耗尽（taste_exhausted）。需要切号。
type QuotaExhaustedError struct {
	StatusCode int
	Message    string
}

func (e *QuotaExhaustedError) Error() string {
	return fmt.Sprintf("quota exhausted %d: %s", e.StatusCode, e.Message)
}

// UpstreamTemporaryError 上游临时错误（5xx / 网络），同 token 可重试。
type UpstreamTemporaryError struct {
	StatusCode int
	Message    string
	Retryable  bool
}

func (e *UpstreamTemporaryError) Error() string {
	return fmt.Sprintf("upstream temporary error %d: %s", e.StatusCode, e.Message)
}

// RateLimitedError 429 — 必须切 token，重试同 token 无意义。
type RateLimitedError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("upstream rate limited %d: %s", e.StatusCode, e.Message)
}

// AdobeRequestError 上游永久性请求错误（4xx 非鉴权 / 非限流）。
type AdobeRequestError struct {
	StatusCode int
	// Message 是面向下游用户的可读消息，不含内部实现细节。
	Message string
	// InternalDetail 是只写日志的内部说明（可含 nonce/provider 等敏感字段）。
	InternalDetail string
	Body           string
}

func (e *AdobeRequestError) Error() string {
	return fmt.Sprintf("upstream request error %d: %s", e.StatusCode, e.Message)
}

// IsInvalidUsage422 命中 Adobe 2026Q2 anti-bot 触发的 422 "Invalid Usage"。
func (e *AdobeRequestError) IsInvalidUsage422() bool {
	if e == nil || e.StatusCode != 422 {
		return false
	}
	s := strings.ToLower(e.Body + " " + e.InternalDetail)
	return strings.Contains(s, "invalid usage for")
}

// ContentPolicyError 内容审核不通过。
type ContentPolicyError struct {
	Message string
}

func (e *ContentPolicyError) Error() string {
	return e.Message
}

// NotEntitledError 当前 Adobe 账号未开通指定能力的权益（x-access-error=user_not_entitled）。
//
// 与 AuthError 的区别：
//   - AuthError 表示 token 失效，账号当前不可用，应换号 + 冷却；
//   - NotEntitledError 表示这个账号永远不能用这个 endpoint / detailLevel，
//     但同一个账号在其他档位/接口下仍然完全正常。
//     例如：Free Adobe 账号没买 4K Premium 出图权益，但 1K/2K 完全可用。
//
// 调度策略：
//   - 走"transient"通道：不累计 error_count、不进入 cooldown，避免误伤；
//   - 仍然可重试（其他账号可能开通了），调用方 triedAccountIDs 自动绕开当前号；
//   - 全部账号都不开通时，用户看到「权益未开通」类清晰提示，而不是泛泛的 403。
type NotEntitledError struct {
	StatusCode  int
	Message     string
	AccessError string
}

func (e *NotEntitledError) Error() string {
	return fmt.Sprintf("not entitled %d: %s", e.StatusCode, e.Message)
}

// SanitizeErrorMessage 把内部实现细节从错误消息里抹掉，再返回给下游用户。
func SanitizeErrorMessage(msg string) string {
	if isRegionBlockedMessage(msg) {
		return "Your keyword is unsafe. Please update or change your keyword."
	}
	const invalidUsageUserMessage = "您的提示词出现了违禁词或者长度超出最大限制,请修改后重试."
	if strings.Contains(msg, invalidUsageUserMessage) {
		return invalidUsageUserMessage
	}
	replacer := strings.NewReplacer(
		"adobe request error", "upstream request error",
		"Adobe request error", "Upstream request error",
		"adobe ", "upstream ",
		"Adobe ", "Upstream ",
		"adobe.io", "upstream",
		"firefly-", "",
		"firefly_", "",
		"x-nonce 校验失败", "参数校验失败",
		"nonce 公式已变更", "请稍后重试",
		"x-nonce", "",
		"nonce ", "",
	)
	return replacer.Replace(msg)
}

func isRegionBlockedMessage(msg string) bool {
	s := strings.ToLower(msg)
	return strings.Contains(s, "http 451") ||
		strings.Contains(s, "temporary error 451") ||
		strings.Contains(s, "区域限制") ||
		strings.Contains(s, "region restricted") ||
		strings.Contains(s, "region blocked")
}

// ClassifyError 把上游 HTTP 状态码 + headers + body 归类成我们的错误类型，
// 调用方可以 errors.As 走分支决定 retry / cooldown / surface to user。
func ClassifyError(statusCode int, headers map[string]string, body string) error {
	accessError := headers["x-access-error"]

	msg := body
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	msgLower := strings.ToLower(msg)
	isHTMLBody := strings.Contains(msgLower, "<html") || strings.Contains(msgLower, "<!doctype html")

	// 2026Q2 anti-bot：x-nonce 缺失/错误时上游伪装为 422 "Invalid Usage for Image"。
	if statusCode == 422 && strings.Contains(msgLower, "invalid usage for") {
		return &AdobeRequestError{
			StatusCode:     statusCode,
			Message:        "您的提示词出现了违禁词或者长度超出最大限制,请修改后重试.",
			InternalDetail: fmt.Sprintf("invalid usage from upstream (HTTP %d), possible payload/nonce/content-policy rejection, body=%s", statusCode, body),
			Body:           body,
		}
	}

	if statusCode == 413 {
		return &AdobeRequestError{
			StatusCode: statusCode,
			Message:    "请求体过大 (HTTP 413)，请压缩图片/视频或减少上传内容大小",
			Body:       body,
		}
	}

	// taste_exhausted: 普号试用次数耗尽（Banana/Image 系列常见）
	// quota_exhausted: 视频通道（sora/veo3.1*）独立配额耗尽（HAR 抓包显示 video 专用）
	if (statusCode == 401 || statusCode == 403) &&
		(accessError == "taste_exhausted" || accessError == "quota_exhausted") {
		return &QuotaExhaustedError{StatusCode: statusCode, Message: fmt.Sprintf("配额耗尽 (HTTP %d, %s)", statusCode, accessError)}
	}

	if (statusCode == 401 || statusCode == 403) && accessError == "blocked_by_3p_model_provider" {
		return &ProviderBlockedError{
			StatusCode:  statusCode,
			Message:     fmt.Sprintf("模型提供方限制/拒绝了本次生成 (HTTP %d)，请稍后重试或更换模型/提示词", statusCode),
			AccessError: accessError,
		}
	}

	// user_not_entitled: 账号没开通此能力（典型场景：4K Premium 出图）。
	// 见 NotEntitledError 注释——和 token 失效完全不同的语义。
	if (statusCode == 401 || statusCode == 403) &&
		(accessError == "user_not_entitled" || accessError == "not_entitled") {
		return &NotEntitledError{
			StatusCode:  statusCode,
			Message:     "Adobe 账号未开通该能力（例如 4K 出图权益）",
			AccessError: accessError,
		}
	}

	if statusCode == 401 || statusCode == 403 {
		// 反爬 / 网关挑战常以 HTML body 形式返回 401/403（cloudflare、"just a moment"、
		// akamai 等），根因是当前出口 IP/代理被挑战，而不是 token 失效。这类必须归为
		// 可重试的临时错误（换 IP/重试即可恢复），否则调度层会把好号误判成 token 死、
		// 直接踢出池——一个被风控的代理能在一轮里连带打死一大批正常号。
		// 只有"干净的 JSON 401/403"才认定为真正的 token 鉴权失败（AuthError）。
		if isHTMLBody {
			return &UpstreamTemporaryError{StatusCode: statusCode, Message: fmt.Sprintf("上游网关挑战 (HTTP %d)", statusCode), Retryable: true}
		}
		return &AuthError{StatusCode: statusCode, Message: fmt.Sprintf("Token 鉴权失败 (HTTP %d)", statusCode)}
	}

	// 408 timeout_error / system under load：Adobe 2026-06 起高峰期常见，表示 worker
	// 过载而非 payload/token 问题。必须归为可重试临时错误，否则调度层直接 fail。
	if statusCode == 408 ||
		strings.Contains(msgLower, "timeout_error") ||
		strings.Contains(msgLower, "system under load") {
		return &UpstreamTemporaryError{
			StatusCode: statusCode,
			Message:    fmt.Sprintf("上游繁忙 (HTTP %d): %s", statusCode, msg),
			Retryable:  true,
		}
	}

	if statusCode == 429 ||
		strings.Contains(msgLower, "backpressure_limited") ||
		strings.Contains(msgLower, "worker is overloaded") {
		var retryAfter time.Duration
		if v := headers["retry-after"]; v != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				retryAfter = time.Duration(n) * time.Second
			}
		}
		return &RateLimitedError{
			StatusCode: statusCode,
			Message:    "上游限流/过载 (HTTP 429)",
			RetryAfter: retryAfter,
		}
	}

	if statusCode == 451 {
		return &UpstreamTemporaryError{StatusCode: statusCode, Message: "Your keyword is unsafe. Please update or change your keyword.", Retryable: false}
	}

	if statusCode >= 500 {
		if isHTMLBody {
			return &UpstreamTemporaryError{StatusCode: statusCode, Message: fmt.Sprintf("上游服务错误 (HTTP %d)", statusCode), Retryable: true}
		}
		return &UpstreamTemporaryError{StatusCode: statusCode, Message: fmt.Sprintf("上游服务错误 (HTTP %d): %s", statusCode, msg), Retryable: true}
	}

	if isHTMLBody {
		return &AdobeRequestError{
			StatusCode: statusCode,
			Message:    fmt.Sprintf("上游网关拒绝请求 (HTTP %d)", statusCode),
			Body:       body,
		}
	}
	return &AdobeRequestError{StatusCode: statusCode, Message: fmt.Sprintf("上游请求失败 (HTTP %d): %s", statusCode, msg), Body: body}
}
