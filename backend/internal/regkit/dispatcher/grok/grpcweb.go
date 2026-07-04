// Package grok 实现 GROK / x.ai 账号自动注册 dispatcher。
//
// 流程参考：cashappv2_code/grokreg。
//
// 与 Python 版本对齐的关键技术：
//
//   - HTTP/1.1 + utls Chrome131 TLS 指纹（regkit/browser）
//   - Cloudflare Turnstile（regkit/captcha CapSolver）
//   - Outlook IMAP / Graph 邮箱（regkit/mailbox）
//   - gRPC-web binary protobuf 编码（本文件）
//
// 落库表：pool_grok（trial_status=pending 留待后续 SuperGrok 试用 / Stripe 流程接管）。
package grok

import (
	"encoding/binary"
)

// gRPC-web binary frame：1 byte flag + 4 byte BE length + protobuf payload。
//
// flag = 0x00 表示 data；trailer 帧用 0x80（这里都不需要）。
func grpcWebFrame(payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = 0x00
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

// protobuf wire 类型。
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// pbAppendVarint 追加 protobuf varint。
func pbAppendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

// pbAppendTag 追加 protobuf tag（field number + wire type）。
func pbAppendTag(dst []byte, fieldNum int, wireType int) []byte {
	return pbAppendVarint(dst, uint64((fieldNum<<3)|wireType))
}

// pbAppendString 追加 string 字段（wire = bytes）。
func pbAppendString(dst []byte, fieldNum int, s string) []byte {
	dst = pbAppendTag(dst, fieldNum, wireBytes)
	dst = pbAppendVarint(dst, uint64(len(s)))
	return append(dst, s...)
}

// encodeCreateEmailValidationCode 编码 CreateEmailValidationCodeRequest{ email = field 1 }。
func encodeCreateEmailValidationCode(email string) []byte {
	body := pbAppendString(nil, 1, email)
	return grpcWebFrame(body)
}

// encodeVerifyEmailValidationCode 编码 VerifyEmailValidationCodeRequest{ email = 1, code = 2 }。
func encodeVerifyEmailValidationCode(email, code string) []byte {
	body := pbAppendString(nil, 1, email)
	body = pbAppendString(body, 2, code)
	return grpcWebFrame(body)
}

// encodeSetTosAcceptedVersion 编码 SetTosAcceptedVersionRequest{ version = 1 }（field 2 = uint64 1）。
//
// 与参考 Python 实现完全一致：固定 7 字节 b"\x00\x00\x00\x00\x02\x10\x01"。
func encodeSetTosAcceptedVersion() []byte {
	body := pbAppendTag(nil, 2, wireVarint)
	body = pbAppendVarint(body, 1)
	return grpcWebFrame(body)
}
