package flowmusic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CredentialBundleJSON 是 pool_google.credential_enc 解密后的明文结构。
//
// provider 生成时只读 access_token + cookies；续期调度器读全部字段。
type CredentialBundleJSON struct {
	RefreshToken         string `json:"refresh_token,omitempty"`
	AccessToken          string `json:"access_token,omitempty"`
	JWT                  string `json:"jwt,omitempty"`
	ProviderToken        string `json:"provider_token,omitempty"`
	ProviderRefreshToken string `json:"provider_refresh_token,omitempty"`
	FlowBearer           string `json:"flow_bearer,omitempty"`
	Cookies              string `json:"cookies,omitempty"`
	Email                string `json:"email,omitempty"`
	Name                 string `json:"name,omitempty"`
	ExpiresAt            int64  `json:"expires_at,omitempty"` // unix 秒
}

// ParseCredentialBundle 把明文凭证（JSON bundle 或裸 JWT）解析成 Credentials。
func ParseCredentialBundle(raw string) Credentials {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Credentials{}
	}
	if strings.HasPrefix(raw, "{") {
		var b CredentialBundleJSON
		if err := json.Unmarshal([]byte(raw), &b); err == nil {
			creds := Credentials{
				RefreshToken:         b.RefreshToken,
				AccessToken:          firstNonEmpty(b.AccessToken, b.JWT),
				ProviderToken:        b.ProviderToken,
				ProviderRefreshToken: b.ProviderRefreshToken,
				FlowBearer:           b.FlowBearer,
				Cookies:              b.Cookies,
				Email:                b.Email,
				Name:                 b.Name,
			}
			if b.ExpiresAt > 0 {
				t := time.Unix(b.ExpiresAt, 0).UTC()
				creds.ExpiresAt = &t
			}
			return creds
		}
	}
	return Credentials{AccessToken: raw}
}

// EncodeCredentialBundle 把 Credentials 序列化成明文 bundle JSON（入库前再加密）。
func EncodeCredentialBundle(c Credentials) string {
	b := CredentialBundleJSON{
		RefreshToken:         strings.TrimSpace(c.RefreshToken),
		AccessToken:          strings.TrimSpace(c.AccessToken),
		ProviderToken:        strings.TrimSpace(c.ProviderToken),
		ProviderRefreshToken: strings.TrimSpace(c.ProviderRefreshToken),
		FlowBearer:           strings.TrimSpace(c.FlowBearer),
		Cookies:              strings.TrimSpace(c.Cookies),
		Email:                strings.TrimSpace(c.Email),
		Name:                 strings.TrimSpace(c.Name),
	}
	if c.ExpiresAt != nil {
		b.ExpiresAt = c.ExpiresAt.Unix()
	}
	data, err := json.Marshal(b)
	if err != nil {
		return ""
	}
	return string(data)
}

// CredentialsFromCookieExport 解析浏览器导出的 cookie JSON 数组（EditThisCookie 风格），
// 还原 Supabase 会话并组装成 Credentials。
//
// 入参形如：[{"name":"sb-sb-auth-token.0","value":"base64-..."}, ...]
func CredentialsFromCookieExport(cookiesJSON []byte) (Credentials, error) {
	var items []map[string]any
	if err := json.Unmarshal(cookiesJSON, &items); err != nil {
		return Credentials{}, fmt.Errorf("解析 cookie JSON 失败：%w", err)
	}
	return CredentialsFromCookieItems(items)
}

// CredentialsFromCookieItems 同 CredentialsFromCookieExport，但接收已解析的数组。
func CredentialsFromCookieItems(items []map[string]any) (Credentials, error) {
	type chunk struct {
		index int
		value string
	}
	groups := map[string][]chunk{}
	var headerParts []string
	for _, it := range items {
		name := strings.TrimSpace(stringFromAnyMap(it, "name"))
		value := stringFromAnyMap(it, "value")
		if name == "" || value == "" {
			continue
		}
		headerParts = append(headerParts, name+"="+value)
		if prefix, idx, ok := authCookieName(name); ok {
			groups[prefix] = append(groups[prefix], chunk{index: idx, value: value})
		}
	}
	if len(groups) == 0 {
		return Credentials{}, fmt.Errorf("未找到 FlowMusic 鉴权 cookie（sb-*-auth-token.N）")
	}

	const preferred = "sb-sb-auth-token."
	chunks, ok := groups[preferred]
	if !ok {
		for _, g := range groups {
			chunks = g
			break
		}
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].index < chunks[j].index })
	var b strings.Builder
	for _, ch := range chunks {
		b.WriteString(ch.value)
	}
	session, err := decodeCookieSession(b.String())
	if err != nil {
		return Credentials{}, err
	}

	user := mapFromAnyMap(session, "user")
	metadata := mapFromAnyMap(user, "user_metadata")
	creds := Credentials{
		AccessToken:          stringFromAnyMap(session, "access_token"),
		RefreshToken:         stringFromAnyMap(session, "refresh_token"),
		ProviderToken:        stringFromAnyMap(session, "provider_token"),
		ProviderRefreshToken: stringFromAnyMap(session, "provider_refresh_token"),
		Cookies:              strings.Join(headerParts, "; "),
		Email:                firstNonEmpty(stringFromAnyMap(user, "email"), stringFromAnyMap(metadata, "email")),
		Name:                 firstNonEmpty(stringFromAnyMap(metadata, "name"), stringFromAnyMap(metadata, "full_name")),
	}
	if creds.AccessToken == "" && creds.RefreshToken == "" {
		return Credentials{}, fmt.Errorf("cookie 会话缺少 access_token / refresh_token")
	}
	if exp, ok := numericValue(session["expires_at"]); ok && exp > 0 {
		t := time.Unix(int64(exp), 0).UTC()
		creds.ExpiresAt = &t
	}
	return creds, nil
}

func authCookieName(name string) (string, int, bool) {
	name = strings.TrimSpace(name)
	const marker = "-auth-token."
	if !strings.HasPrefix(name, "sb-") {
		return "", 0, false
	}
	pos := strings.LastIndex(name, marker)
	if pos <= len("sb-") {
		return "", 0, false
	}
	prefix := name[:pos+len(marker)]
	idx, err := strconv.Atoi(name[pos+len(marker):])
	if err != nil || idx < 0 {
		return "", 0, false
	}
	return prefix, idx, true
}

func decodeCookieSession(value string) (map[string]any, error) {
	value = strings.TrimSpace(value)
	if unescaped, err := url.PathUnescape(value); err == nil {
		value = unescaped
	}
	value = strings.TrimPrefix(value, "base64-")
	if value == "" {
		return nil, fmt.Errorf("cookie 会话为空")
	}
	payload, err := decodeFlexibleBase64(value)
	if err != nil {
		return nil, fmt.Errorf("cookie base64 解码失败：%w", err)
	}
	var session map[string]any
	if err := json.Unmarshal(payload, &session); err != nil {
		return nil, fmt.Errorf("cookie 会话 JSON 解析失败：%w", err)
	}
	return session, nil
}

func decodeFlexibleBase64(value string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if decoded, err := enc.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	padded := value
	if r := len(padded) % 4; r != 0 {
		padded += strings.Repeat("=", 4-r)
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		if decoded, err := enc.DecodeString(padded); err == nil {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("invalid base64 payload")
}

func stringFromAnyMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mapFromAnyMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}
