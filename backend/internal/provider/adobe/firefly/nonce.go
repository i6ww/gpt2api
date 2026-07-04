package firefly

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// computeNonce 计算 Adobe 3p generate-async 端点要求的 x-nonce header。
//
//	nonce = sha256_hex( userId + "-" + first 256 Unicode characters of prompt )
//
// 缺失或错误会让上游返回伪装成 payload 校验错的 422 "Invalid Usage for Image"。
// userId 是 JWT 里形如 "0AD5813969E6FF880A495C3B@AdobeID" 的 user_id claim。
func computeNonce(userID, prompt string) string {
	if userID == "" {
		return ""
	}
	p := firstRunes(prompt, 256)
	sum := sha256.Sum256([]byte(userID + "-" + p))
	return hex.EncodeToString(sum[:])
}

func firstRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// extractPromptFromPayload 从任意 payload 里捞 prompt 字段（用于 nonce 计算）。
func extractPromptFromPayload(payload interface{}) string {
	m, ok := payload.(ImagePayload)
	if !ok {
		if mm, ok2 := payload.(map[string]interface{}); ok2 {
			m = ImagePayload(mm)
		} else {
			return ""
		}
	}
	if p, ok := m["_noncePrompt"].(string); ok {
		return p
	}
	if p, ok := m["prompt"].(string); ok {
		return p
	}
	return ""
}

// extractUserIDFromJWT 不验签解 JWT 取 Adobe user_id。
// 依次试 user_id / aa_id / sub，匹配 newbanana refresh manager 的约定。
func extractUserIDFromJWT(token string) string {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims map[string]interface{}
	if json.Unmarshal(decoded, &claims) != nil {
		return ""
	}
	for _, key := range []string{"user_id", "aa_id", "sub"} {
		if v, ok := claims[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
