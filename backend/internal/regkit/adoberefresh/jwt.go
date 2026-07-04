// Package adoberefresh 实现 Adobe IMS access_token 静默续期 + Firefly 积分查询。
//
// 完整对齐 Python 参考实现 newwork/token_refresh.py：
//
//	1. ExtractJWTExpiry        - 解 JWT payload 拿 exp 时间戳
//	2. ExtractAccountIDFromJWT - 解 JWT payload 拿 user_id / aa_id / sub
//	3. RefreshAccessTokenViaCookie - POST /ims/check/v6/token 用 cookie 静默换新 token
//	4. FetchAccountInfo        - GET /ims/profile/v1 拿 displayName / userId
//	5. FetchCredits            - GET firefly.adobe.io/v1/credits/balance 拿 Firefly 积分
//	6. RefreshOne              - 串起以上 4 步，一次完整刷新一个账号
//
// 使用：
//
//	import "github.com/kleinai/backend/internal/regkit/adoberefresh"
//	expAt := adoberefresh.ExtractJWTExpiry(token)
//	res, err := adoberefresh.RefreshAccessTokenViaCookie(ctx, cookie, "")
//
// 注意：
//
//   - JWT 的 created_at / expires_in 字段经常用毫秒，本实现自动归一到秒
//   - cookie 必须是 Adobe IMS 完整会话（含 ims_sid 等），由注册流程或导入文本提供
//   - proxyURL 留空 = 直连；建议外部传入轮转后的代理 URL，让多账号续期不集中在一个出口 IP
package adoberefresh

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// JWTClaims Adobe IMS access_token 的最小投影；只关心过期时间 + 账号 ID 字段。
type JWTClaims struct {
	Exp        any    `json:"exp,omitempty"`
	CreatedAt  any    `json:"created_at,omitempty"`
	ExpiresIn  any    `json:"expires_in,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	AaID       string `json:"aa_id,omitempty"`
	Sub        string `json:"sub,omitempty"`
}

// DecodeJWTPayload base64url 解 JWT 第二段（payload）。失败返回 nil。
//
// Adobe 不签名校验也能正常解（我们不验签），用 segment[1] 即可。
func DecodeJWTPayload(token string) *JWTClaims {
	if token == "" || !strings.Contains(token, ".") {
		return nil
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload := parts[1]
	// base64url 补 padding
	if pad := len(payload) % 4; pad > 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	var c JWTClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil
	}
	return &c
}

// ExtractJWTExpiry 返回 access_token 的绝对过期 Unix 时间戳（秒）。
//
// 解析顺序：
//
//   1. claims.exp 直接取（标准 JWT 字段）
//   2. claims.created_at + claims.expires_in 自己加（Adobe IMS 私货字段）
//      - created_at / expires_in 经常用毫秒，自动归一到秒
//
// 解不出来返回 0（调用方应当 fallback 到 expires_in 头或 now+24h 兜底）。
func ExtractJWTExpiry(token string) int64 {
	c := DecodeJWTPayload(token)
	if c == nil {
		return 0
	}
	if v := toInt64(c.Exp); v > 0 {
		return v
	}
	created := toInt64(c.CreatedAt)
	expIn := toInt64(c.ExpiresIn)
	if created > 0 && expIn > 0 {
		// 大于 10^10 几乎肯定是毫秒
		if created > 10_000_000_000 {
			created /= 1000
		}
		// 超过 2 天的 expires_in 也很可能是毫秒
		if expIn > 86400*2 {
			expIn /= 1000
		}
		return created + expIn
	}
	return 0
}

// ExtractAccountIDFromJWT 拿 user_id / aa_id / sub，按顺序首个非空。
//
// Adobe 部分 token 只在 sub 字段里塞 userId，部分塞在 user_id；这里都试。
func ExtractAccountIDFromJWT(token string) string {
	c := DecodeJWTPayload(token)
	if c == nil {
		return ""
	}
	for _, v := range []string{c.UserID, c.AaID, c.Sub} {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// IsExpiringWithin 判断 token 是否在 within 时间内过期（true=接近过期）。
// expAt 为 0 视为"未知"，按"已过期"处理（保守续期）。
func IsExpiringWithin(expAt int64, within time.Duration) bool {
	if expAt <= 0 {
		return true
	}
	return time.Until(time.Unix(expAt, 0)) <= within
}

// toInt64 把 any（可能是 float64 / json.Number / string / int）安全转 int64。
// 解析失败返回 0。
func toInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return int64(f)
		}
	case string:
		// 尝试当数字串解
		var n json.Number = json.Number(strings.TrimSpace(x))
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return int64(f)
		}
	}
	return 0
}
