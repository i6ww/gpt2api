// Command xaiseed 用项目同款 AES-256-GCM 加密 xAI 登录产物，输出可直接执行的 pool_xai INSERT SQL（blob 走 UNHEX）。
// 用法：KLEIN_AES_KEY=... go run ./cmd/xaiseed -credfile login.json [-type supergrok]
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kleinai/backend/pkg/crypto"
)

func decodeAESKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 64 {
		if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("KLEIN_AES_KEY must be 32 bytes raw or 64 hex chars (got %d)", len(raw))
}

func sqlBlob(aes *crypto.AESGCM, s string) string {
	if strings.TrimSpace(s) == "" {
		return "NULL"
	}
	enc, err := aes.Encrypt([]byte(s))
	if err != nil {
		fmt.Fprintln(os.Stderr, "encrypt:", err)
		os.Exit(1)
	}
	return "UNHEX('" + hex.EncodeToString(enc) + "')"
}

func sqlStr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "NULL"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func main() {
	credFile := flag.String("credfile", "", "登录产物 JSON")
	accType := flag.String("type", "supergrok", "account_type")
	flag.Parse()

	key, err := decodeAESKey(os.Getenv("KLEIN_AES_KEY"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	aes, err := crypto.NewAESGCM(key)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	b, err := os.ReadFile(*credFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var m struct {
		AccessToken   string `json:"access_token"`
		RefreshToken  string `json:"refresh_token"`
		IDToken       string `json:"id_token"`
		Email         string `json:"email"`
		Subject       string `json:"subject"`
		TokenEndpoint string `json:"token_endpoint"`
		BaseURL       string `json:"base_url"`
		ExpiresAt     int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if m.BaseURL == "" {
		m.BaseURL = "https://api.x.ai/v1"
	}
	exp := "NULL"
	if m.ExpiresAt > 0 {
		exp = "'" + time.UnixMilli(m.ExpiresAt).UTC().Format("2006-01-02 15:04:05") + "'"
	}

	fmt.Printf("DELETE FROM pool_xai WHERE email=%s;\n", sqlStr(strings.ToLower(m.Email)))
	fmt.Printf(`INSERT INTO pool_xai
(email, subject, credential_enc, refresh_token_enc, id_token_enc, token_endpoint, base_url, account_type, status, source, refresh_enabled, expires_at, weight, created_at, updated_at)
VALUES (%s, %s, %s, %s, %s, %s, %s, %s, 'valid', 'import', 1, %s, 10, NOW(3), NOW(3));
`,
		sqlStr(strings.ToLower(m.Email)),
		sqlStr(m.Subject),
		sqlBlob(aes, m.AccessToken),
		sqlBlob(aes, m.RefreshToken),
		sqlBlob(aes, m.IDToken),
		sqlStr(m.TokenEndpoint),
		sqlStr(m.BaseURL),
		sqlStr(*accType),
		exp,
	)
}
