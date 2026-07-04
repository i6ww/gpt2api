package service

import (
	"strings"
	"testing"
	"time"
)

func TestMediaTokenRoundTrip(t *testing.T) {
	secret := []byte("test-media-signing-secret-32bytes!!")
	in := MediaTokenPayload{TaskID: "01HZX9TASKID0000000000000", Seq: 2, Thumb: true, Exp: time.Now().Add(time.Hour).Unix()}
	tok, err := SignMediaToken(secret, in)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	out, err := VerifyMediaToken(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.TaskID != in.TaskID || out.Seq != in.Seq || out.Thumb != in.Thumb {
		t.Fatalf("payload mismatch: got %+v want %+v", out, in)
	}
}

func TestMediaTokenCompactFormat(t *testing.T) {
	secret := []byte("test-media-signing-secret-32bytes!!")
	// 26 位十六进制 task_id（newULID 实际产出形态）→ 走紧凑二进制格式。
	in := MediaTokenPayload{TaskID: "499d464b0df342ad92c59ab5d2", Seq: 3, Thumb: true, Exp: time.Now().Add(time.Hour).Unix()}
	tok, err := SignMediaToken(secret, in)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if strings.Contains(tok, ".") {
		t.Fatalf("compact token should not contain '.': %q", tok)
	}
	if len(tok) > 48 {
		t.Fatalf("compact token unexpectedly long (%d): %q", len(tok), tok)
	}
	out, err := VerifyMediaToken(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.TaskID != in.TaskID || out.Seq != in.Seq || out.Thumb != in.Thumb || out.Exp != in.Exp {
		t.Fatalf("payload mismatch: got %+v want %+v", out, in)
	}
	// 篡改一个字符应被拒。
	bad := []byte(tok)
	if bad[0] == 'A' {
		bad[0] = 'B'
	} else {
		bad[0] = 'A'
	}
	if _, err := VerifyMediaToken(secret, string(bad)); err == nil {
		t.Fatal("expected tampered compact token to be rejected")
	}
}

func TestMediaTokenRejectsTamperedSignature(t *testing.T) {
	secret := []byte("test-media-signing-secret-32bytes!!")
	tok, err := SignMediaToken(secret, MediaTokenPayload{TaskID: "abc", Seq: 0, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := VerifyMediaToken([]byte("different-secret-different-secret!"), tok); err == nil {
		t.Fatal("expected signature mismatch with wrong secret")
	}
}

func TestMediaTokenRejectsExpired(t *testing.T) {
	secret := []byte("test-media-signing-secret-32bytes!!")
	tok, err := SignMediaToken(secret, MediaTokenPayload{TaskID: "abc", Seq: 0, Exp: time.Now().Add(-time.Minute).Unix()})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := VerifyMediaToken(secret, tok); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestIsRedirectableUpstreamURL(t *testing.T) {
	cases := map[string]bool{
		"https://firefly-epo855232.adobe.io/v2/x.png?sig=abc": true,
		"http://example.com/a.png":                            true,
		"https://assets.grok.com/users/x/y.jpg":               false,
		"/api/v1/gen/cached/generated/2026/05/x.png":          false,
		"data:image/png;base64,AAAA":                          false,
		"":                                                    false,
	}
	for url, want := range cases {
		if got := isRedirectableUpstreamURL(url); got != want {
			t.Errorf("isRedirectableUpstreamURL(%q)=%v want %v", url, got, want)
		}
	}
}
