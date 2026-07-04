package service

import (
	"strings"
	"testing"
)

// TestIsGrokJWT 校验裸 JWT 识别：以 eyJ 开头 + 恰好两个点 + 不含空白。
func TestIsGrokJWT(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid jwt", "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJzZXNzaW9uX2lkIjoiOTUxNWUwMDctY2E5Ni00YzgxLTkwODEtZGRkMDg5YWM5YjE3In0.6G4HggIgd7D5AQnCs7KNEZktoEl8pU8V60FCH6GENQc", true},
		{"compound triple-dash", "abc@example.com----secret----eyJ0eXAi.eyJ.x", false},
		{"non-jwt", "abc@example.com", false},
		{"missing dots", "eyJonlysegment", false},
		{"too many dots", "eyJ.eyJ.eyJ.eyJ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isGrokJWT(c.in); got != c.want {
				t.Errorf("isGrokJWT(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestGrokJWTPlaceholderEmail 校验 session_id 派生 email + sha256 fallback。
func TestGrokJWTPlaceholderEmail(t *testing.T) {
	// payload = {"session_id":"9515e007-ca96-4c81-9081-ddd089ac9b17"}
	jwt := "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJzZXNzaW9uX2lkIjoiOTUxNWUwMDctY2E5Ni00YzgxLTkwODEtZGRkMDg5YWM5YjE3In0.6G4HggIgd7D5AQnCs7KNEZktoEl8pU8V60FCH6GENQc"
	got := grokJWTPlaceholderEmail(jwt)
	want := "grok-9515e007-ca96-4c81-9081-ddd089ac9b17@token.local"
	if got != want {
		t.Errorf("grokJWTPlaceholderEmail = %q, want %q", got, want)
	}

	// 损坏的 payload 应当 fallback 到 sha256 短哈希
	bad := "eyJoZWFkZXI.<<<not-base64>>>.sig"
	gotBad := grokJWTPlaceholderEmail(bad)
	if !strings.HasPrefix(gotBad, "grok-") || !strings.HasSuffix(gotBad, "@token.local") {
		t.Errorf("fallback email format wrong: %q", gotBad)
	}
}
