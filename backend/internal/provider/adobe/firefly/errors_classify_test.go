package firefly

import (
	"errors"
	"testing"
)

// TestClassifyAuthVsAntibot 锁定 token 失效判定的精度：
//   - 干净的 JSON 401/403 → AuthError（token 真死，上层据此置 invalid 终态）；
//   - HTML body 的 401/403（cloudflare / just-a-moment 等反爬挑战）→
//     UpstreamTemporaryError 可重试（换 IP/重试即恢复），绝不能误判成 token 死，
//     否则一个被风控的代理会连带打死一大批正常号。
func TestClassifyAuthVsAntibot(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		wantFn  func(error) bool
	}{
		{
			name:   "clean json 401 -> AuthError",
			status: 401,
			body:   `{"error":"invalid_token"}`,
			wantFn: func(e error) bool { var a *AuthError; return errors.As(e, &a) },
		},
		{
			name:   "clean json 403 -> AuthError",
			status: 403,
			body:   `{"error":"forbidden"}`,
			wantFn: func(e error) bool { var a *AuthError; return errors.As(e, &a) },
		},
		{
			name:   "html 403 antibot -> retryable temp error",
			status: 403,
			body:   `<!DOCTYPE html><html><head><title>Just a moment...</title></head></html>`,
			wantFn: func(e error) bool {
				var u *UpstreamTemporaryError
				return errors.As(e, &u) && u.Retryable
			},
		},
		{
			name:   "html 401 challenge -> retryable temp error",
			status: 401,
			body:   `<html><body>Access denied</body></html>`,
			wantFn: func(e error) bool {
				var u *UpstreamTemporaryError
				return errors.As(e, &u) && u.Retryable
			},
		},
		{
			name:    "taste_exhausted 403 -> quota, not auth",
			status:  403,
			headers: map[string]string{"x-access-error": "taste_exhausted"},
			body:    `{}`,
			wantFn:  func(e error) bool { var q *QuotaExhaustedError; return errors.As(e, &q) },
		},
		{
			name:    "blocked_by_3p 403 -> provider blocked, not auth",
			status:  403,
			headers: map[string]string{"x-access-error": "blocked_by_3p_model_provider"},
			body:    `{}`,
			wantFn:  func(e error) bool { var b *ProviderBlockedError; return errors.As(e, &b) },
		},
		{
			name:   "408 system under load -> retryable temp",
			status: 408,
			body:   `{"error_code":"timeout_error","message":"system under load"}`,
			wantFn: func(e error) bool {
				var u *UpstreamTemporaryError
				return errors.As(e, &u) && u.Retryable
			},
		},
		{
			name:   "429 backpressure -> rate limited",
			status: 429,
			body:   `{"error_code":"backpressure_limited","message":"Worker is overloaded. Please try again later."}`,
			wantFn: func(e error) bool { var r *RateLimitedError; return errors.As(e, &r) },
		},
		{
			name:   "451 keyword unsafe -> non-retryable temp (fail fast)",
			status: 451,
			body:   `{"message":"Your keyword is unsafe. Please update or change your keyword."}`,
			wantFn: func(e error) bool {
				var u *UpstreamTemporaryError
				return errors.As(e, &u) && u.StatusCode == 451 && !u.Retryable
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ClassifyError(c.status, c.headers, c.body)
			if err == nil || !c.wantFn(err) {
				t.Fatalf("ClassifyError(%d) = %v, classification mismatch", c.status, err)
			}
		})
	}
}
