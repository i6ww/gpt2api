package service

import (
	"testing"
	"time"
)

func TestChatAccountCooldown(t *testing.T) {
	tests := []struct {
		err  string
		want int // minutes, 0 = no cooldown
	}{
		{"codex chat http 401: unauthorized", 30},
		{"refresh oauth access_token failed: 401 invalid_grant", 30},
		{"grok chat http 403: cloudflare", 0},
		{"codex chat http 429: too many requests", 0},
		{"codex chat http 500: internal error", 0},
		{"messages is required", 0},
	}
	for _, tc := range tests {
		got := chatAccountCooldown(testErr(tc.err))
		want := timeDurationMinutes(tc.want)
		if got != want {
			t.Fatalf("chatAccountCooldown(%q) = %v, want %v", tc.err, got, want)
		}
	}
}

func TestIsChatRequestError(t *testing.T) {
	if !isChatRequestError(testErr("messages is required")) {
		t.Fatal("expected messages is required to be request error")
	}
	if isChatRequestError(testErr("codex chat http 401: unauthorized")) {
		t.Fatal("401 should not be request error")
	}
	if isChatRequestError(testErr("codex chat http 403: forbidden")) {
		t.Fatal("403 should not be request error")
	}
}

func testErr(msg string) error { return &staticErr{msg} }

type staticErr struct{ msg string }

func (e *staticErr) Error() string { return e.msg }

func timeDurationMinutes(m int) time.Duration {
	if m == 0 {
		return 0
	}
	return time.Duration(m) * time.Minute
}
