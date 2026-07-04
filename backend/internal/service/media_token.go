package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/kleinai/backend/pkg/crypto"
)

// MediaTokenPayload 是「重定向直链」短链 token 的内部载荷。
//
// token 本身只带 task_id + seq + thumb 标记 + 过期时间，不嵌入真实上游 URL；
// 真实上游临时直链存在 generation_result.meta 里，命中短链后由 DB 查回再 302。
// 这样 token 短（能塞进 url VARCHAR(512)），且上游 URL 不暴露在任何对外链接里。
type MediaTokenPayload struct {
	TaskID string `json:"t"`
	Seq    int    `json:"q"`
	Thumb  bool   `json:"b,omitempty"`
	Exp    int64  `json:"e"`
	Nonce  string `json:"n"`
}

// MediaSigningSecret 解析签名媒体短链用的密钥。
//
// 优先用 system_config 里的 storage.media_signing_secret（便于运营独立轮换），
// 否则回退到 KLEIN_JWT_SECRET 环境变量（和其它服务端签名共用同一根密钥）。
func MediaSigningSecret(ctx context.Context, cfg *SystemConfigService) []byte {
	if cfg != nil {
		if v := strings.TrimSpace(cfg.GetString(ctx, "storage.media_signing_secret", "")); v != "" {
			return []byte(v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("KLEIN_JWT_SECRET")); v != "" {
		return []byte(v)
	}
	return nil
}

// mediaTokenSigLen 是紧凑 token 截断后的 HMAC 长度（字节）。
// 10 字节 = 80 bit，对短寿命媒体直链防伪足够，且大幅缩短 URL。
const mediaTokenSigLen = 10

// mediaCompactPayloadLen = taskID(13) + seq(1) + exp(4) + flags(1)。
// task_id 由 newULID() 产出，恒为 26 位十六进制，可解码成 13 字节。
const mediaCompactPayloadLen = 13 + 1 + 4 + 1

// SignMediaToken 生成签名媒体短链 token。
//
// 默认输出「紧凑二进制」格式：把 task_id 解码成 13 字节 + seq + exp + flags 打包，
// 再附 10 字节截断 HMAC，整体 base64url 后约 39 个字符（旧 JSON 格式约 90+ 字符）。
// 当 task_id 不是 26 位十六进制、或 seq 超出单字节时，回退到旧的 JSON 格式以保证正确性。
func SignMediaToken(secret []byte, p MediaTokenPayload) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("media signing secret missing")
	}
	if raw, ok := encodeCompactMediaPayload(p); ok {
		mac := hmac.New(sha256.New, secret)
		mac.Write(raw)
		full := append(raw, mac.Sum(nil)[:mediaTokenSigLen]...)
		return base64.RawURLEncoding.EncodeToString(full), nil
	}
	return signLegacyMediaToken(secret, p)
}

// encodeCompactMediaPayload 尝试把载荷打成紧凑二进制；不满足约束时返回 ok=false。
func encodeCompactMediaPayload(p MediaTokenPayload) ([]byte, bool) {
	id, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(p.TaskID)))
	if err != nil || len(id) != 13 {
		return nil, false
	}
	if p.Seq < 0 || p.Seq > 255 || p.Exp < 0 || p.Exp > int64(^uint32(0)) {
		return nil, false
	}
	buf := make([]byte, 0, mediaCompactPayloadLen)
	buf = append(buf, id...)
	buf = append(buf, byte(p.Seq))
	var exp [4]byte
	binary.BigEndian.PutUint32(exp[:], uint32(p.Exp))
	buf = append(buf, exp[:]...)
	var flags byte
	if p.Thumb {
		flags |= 0x01
	}
	buf = append(buf, flags)
	return buf, true
}

func signLegacyMediaToken(secret []byte, p MediaTokenPayload) (string, error) {
	if p.Nonce == "" {
		b, err := crypto.RandomBytes(6)
		if err != nil {
			return "", err
		}
		p.Nonce = base64.RawURLEncoding.EncodeToString(b)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(enc))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return enc + "." + sig, nil
}

// VerifyMediaToken 校验签名 + 过期，返回解析后的载荷。
// 自动识别格式：含 "." 的是旧 JSON 格式，否则按紧凑二进制解析。
func VerifyMediaToken(secret []byte, token string) (*MediaTokenPayload, error) {
	if len(secret) == 0 {
		return nil, errors.New("media signing secret missing")
	}
	token = strings.TrimSpace(token)
	if strings.Contains(token, ".") {
		return verifyLegacyMediaToken(secret, token)
	}
	return verifyCompactMediaToken(secret, token)
}

func verifyCompactMediaToken(secret []byte, token string) (*MediaTokenPayload, error) {
	full, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	if len(full) != mediaCompactPayloadLen+mediaTokenSigLen {
		return nil, errors.New("malformed media token")
	}
	raw := full[:mediaCompactPayloadLen]
	sig := full[mediaCompactPayloadLen:]
	mac := hmac.New(sha256.New, secret)
	mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil)[:mediaTokenSigLen], sig) {
		return nil, errors.New("media token signature mismatch")
	}
	p := &MediaTokenPayload{
		TaskID: hex.EncodeToString(raw[:13]),
		Seq:    int(raw[13]),
		Exp:    int64(binary.BigEndian.Uint32(raw[14:18])),
		Thumb:  raw[18]&0x01 != 0,
	}
	if time.Now().Unix() > p.Exp {
		return nil, errors.New("media token expired")
	}
	return p, nil
}

func verifyLegacyMediaToken(secret []byte, token string) (*MediaTokenPayload, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed media token")
	}
	enc, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(enc))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return nil, errors.New("media token signature mismatch")
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	var p MediaTokenPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if time.Now().Unix() > p.Exp {
		return nil, errors.New("media token expired")
	}
	return &p, nil
}
