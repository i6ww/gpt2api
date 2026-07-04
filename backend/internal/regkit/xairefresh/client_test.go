package xairefresh

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestGeneratePKCECodes(t *testing.T) {
	codes, err := GeneratePKCECodes()
	if err != nil {
		t.Fatal(err)
	}
	if codes.CodeVerifier == "" || codes.CodeChallenge == "" {
		t.Fatal("empty pkce codes")
	}
	if codes.CodeVerifier == codes.CodeChallenge {
		t.Fatal("verifier should differ from challenge")
	}
}

func TestBuildAuthorizeURL(t *testing.T) {
	u, err := BuildAuthorizeURL(AuthorizeURLParams{
		AuthorizationEndpoint: "https://auth.x.ai/oauth2/authorize",
		RedirectURI:           RedirectURI(),
		CodeChallenge:         "challenge",
		State:                 "state123",
		Nonce:                 "nonce123",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"client_id=" + ClientID,
		"code_challenge_method=S256",
		"response_type=code",
		"state=state123",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("authorize url missing %q: %s", want, u)
		}
	}
}

func TestBuildAuthorizeURLRejectsNonXAIHost(t *testing.T) {
	_, err := BuildAuthorizeURL(AuthorizeURLParams{
		AuthorizationEndpoint: "https://evil.com/authorize",
		RedirectURI:           RedirectURI(),
		CodeChallenge:         "c",
		State:                 "s",
		Nonce:                 "n",
	})
	if err == nil {
		t.Fatal("expected rejection of non-x.ai host")
	}
}

func TestParseJWTIdentity(t *testing.T) {
	claims := map[string]any{"email": "user@example.com", "sub": "abc-123"}
	raw, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	token := "header." + payload + ".sig"
	email, sub := ParseJWTIdentity(token)
	if email != "user@example.com" || sub != "abc-123" {
		t.Fatalf("identity = %q / %q", email, sub)
	}
}
