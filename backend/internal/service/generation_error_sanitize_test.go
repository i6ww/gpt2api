package service

import (
	"strings"
	"testing"
)

// TestUserFacingGenerationErrorSanitizesUpstreamBrandsAndPaths 锁定脱敏契约：
//   - 已识别类别返回类别化中文短句
//   - 未识别 / 含上游品牌（adobe / firefly / grok / openai / chatgpt）或 JSON body 的
//     原始错误一律 fallback 到通用「生成失败，请稍后重试」
//   - 返回串里不允许出现品牌名 / URL / HTTP body 等敏感片段
func TestUserFacingGenerationErrorSanitizesUpstreamBrandsAndPaths(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		notHave []string
	}{
		{
			name:    "empty",
			in:      "",
			want:    "生成失败，请稍后重试",
			notHave: nil,
		},
		{
			name: "no available account",
			in:   "pick account: 暂无可用账号",
			want: "服务暂时繁忙，请稍后再试",
		},
		{
			name: "cloudflare just a moment",
			in:   "provider call: just a moment... grok.com waiting room",
			want: "本次请求被验证拦截，请稍后再试",
		},
		{
			name: "rate limit",
			in:   "provider call: grok video http 429: too many requests",
			want: "请求频率过高，请稍后重试",
		},
		{
			name: "content policy",
			in:   "provider call: openai content_policy_violation",
			want: "提示词或参考图触发了内容安全策略，请调整后重试",
		},
		{
			name: "http 451 keyword unsafe",
			in:   "provider call: upstream temporary error 451: 区域限制 (HTTP 451)",
			want: "Your keyword is unsafe. Please update or change your keyword.",
		},
		{
			name: "gpt image2 zero output",
			in:   "provider call: gpt image2 returned 0 image",
			want: "提示词可能无法生成有效图片，请更换提示词后重试",
		},
		{
			name: "timeout",
			in:   "provider call: context deadline exceeded",
			want: "生成超时，请稍后重试",
		},
		{
			name: "403 forbidden",
			in:   "provider call: adobe firefly HTTP 403 forbidden",
			want: "当前选项暂不可用，请稍后重试或更换模型 / 尺寸",
		},
		{
			name: "5xx",
			in:   "provider call: chatgpt returned 502 bad gateway",
			want: "服务暂时不可用，请稍后重试",
		},
		{
			name: "401 unauthorized",
			in:   "provider call: chatgpt 401 unauthorized invalid_grant",
			want: "服务暂时不可用，请稍后重试",
		},
		{
			name: "quota exhausted",
			in:   "provider call: openai usage_limit_reached quota exhausted",
			want: "账户额度不足，请稍后重试",
		},
		{
			name: "not entitled",
			in:   "provider call: firefly user_not_entitled for 4K tier",
			want: "当前档位（如 4K）暂未开通，请改用 1K / 2K 或联系运营",
		},
		{
			name: "network error",
			in:   "provider call: dial tcp api.openai.com: no such host",
			want: "网络异常，请稍后重试",
		},
		{
			name: "unknown raw body falls back to generic",
			in:   `provider call: adobe firefly: {"error":"some internal trace","trace_id":"xyz","x":1}`,
			want: "生成失败，请稍后重试",
			notHave: []string{
				"adobe", "firefly", "trace", "error", "x",
			},
		},
		{
			name: "unknown grok detail falls back to generic",
			in:   "provider call: grok websocket unexpected EOF while polling https://grok.com/rest/conversations",
			// EOF 落到「网络异常」分支，但消息里完全没有 upstream 字样。
			want: "网络异常，请稍后重试",
			notHave: []string{
				"grok", "websocket", "https://", "rest/conversations",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := userFacingGenerationError(tc.in)
			if got != tc.want {
				t.Fatalf("userFacingGenerationError(%q) = %q, want %q", tc.in, got, tc.want)
			}
			lc := strings.ToLower(got)
			for _, banned := range tc.notHave {
				if strings.Contains(lc, strings.ToLower(banned)) {
					t.Fatalf("sanitized message %q must not contain upstream token %q", got, banned)
				}
			}
			// 全局禁止任何分支泄露品牌 / URL / 协议片段。
			for _, banned := range []string{"adobe", "firefly", "openai", "chatgpt", "grok", "anthropic", "claude", "gemini", "veo", "sora", "https://", "http://", ".com"} {
				if strings.Contains(lc, banned) {
					t.Fatalf("sanitized message %q leaks token %q", got, banned)
				}
			}
			// 中文敏感词单独检测：「上游」「账号池」等会让用户感知存在转发架构。
			for _, banned := range []string{"上游", "账号池", "号池"} {
				if strings.Contains(got, banned) {
					t.Fatalf("sanitized message %q leaks Chinese keyword %q", got, banned)
				}
			}
		})
	}
}
