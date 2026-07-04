package grok

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// 来自 grok.com.har 的一条真实 x-statsig-id（POST /rest/app-chat/conversations/new）。
const harStatsigSample = "yCTbD2PFmzRfBX71IrAHETI4hrnPbrki5aZMNZCn7yb7lNgBwGQ5+mvkCVsBmEr33YMbBs3AqAM9C7uAKaFkKK0mB3k9yw"

// 实测从 grok.com 同一次页面加载抓到的「meta + 对应 token」配对（匿名会话）。
const (
	liveSiteVerifMeta = "MIKQDXG0EDvbsIhpoLuONHL1FEIkXP8NC3qsLtDFspSwPjA/XLKO6Pgc3/98NWfE"
	liveStatsigToken  = "wfFDUcywddH6GnFJqGF6T/WzNNWD5Z0+zMq7be8RBHNVcf/x/p1zTyk53R4+vfSmBfXJDsQCjOz9BRBXHc/n/o2CdRkPwg"
)

func decodeStatsigToken(t *testing.T, token string) []byte {
	t.Helper()
	raw, err := base64.RawStdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if len(raw) != 70 {
		t.Fatalf("want 70 bytes, got %d", len(raw))
	}
	rnd := raw[0]
	dec := make([]byte, len(raw)-1)
	for i, b := range raw[1:] {
		dec[i] = b ^ rnd
	}
	return append([]byte{rnd}, dec...) // [rnd, ...payload(69)]
}

// TestEncodeGrokStatsigToken 用真实 token 解出来的分量重新编码，必须逐字节还原原 token，
// 以此锁定布局 / XOR / base64(RawStd) 的实现正确性。
func TestEncodeGrokStatsigToken(t *testing.T) {
	d := decodeStatsigToken(t, harStatsigSample)
	rnd, dec := d[0], d[1:]
	fingerprint := dec[0:48]
	counter := int64(dec[48]) | int64(dec[49])<<8 | int64(dec[50])<<16 | int64(dec[51])<<24
	hash16 := dec[52:68]
	trailer := dec[68]

	if trailer != defaultStatsigTrailer {
		t.Errorf("trailer = %d, want %d", trailer, defaultStatsigTrailer)
	}
	if got := encodeGrokStatsigToken(fingerprint, counter, hash16, trailer, rnd); got != harStatsigSample {
		t.Fatalf("token roundtrip mismatch:\n got %s\nwant %s", got, harStatsigSample)
	}
}

// TestFingerprintEqualsSiteVerificationMeta 用实测配对证明核心发现：
// token 内嵌的 48 字节指纹 == base64decode(grok-site-verification meta)，
// 且我们的解析 + 编码能逐字节还原真实 token。
func TestFingerprintEqualsSiteVerificationMeta(t *testing.T) {
	html := `<head><meta name="grok-site-verification" content="` + liveSiteVerifMeta + `"></head>`
	fpFromMeta := parseGrokSiteVerification(html)
	if len(fpFromMeta) != 48 {
		t.Fatalf("parseGrokSiteVerification: got %d bytes, want 48", len(fpFromMeta))
	}

	d := decodeStatsigToken(t, liveStatsigToken)
	rnd, dec := d[0], d[1:]
	fpFromToken := dec[0:48]
	counter := int64(dec[48]) | int64(dec[49])<<8 | int64(dec[50])<<16 | int64(dec[51])<<24
	hash16 := dec[52:68]
	trailer := dec[68]

	if !bytes.Equal(fpFromMeta, fpFromToken) {
		t.Fatalf("fingerprint mismatch:\n meta  %s\n token %s", hex.EncodeToString(fpFromMeta), hex.EncodeToString(fpFromToken))
	}

	// 用「从 meta 抓到的指纹」重建 token，必须等于浏览器真实发出的 token。
	if got := encodeGrokStatsigToken(fpFromMeta, counter, hash16, trailer, rnd); got != liveStatsigToken {
		t.Fatalf("rebuilt token mismatch:\n got %s\nwant %s", got, liveStatsigToken)
	}
}

// TestGrokStatsigIDWithFingerprintShape 校验生成的 token 结构正确（70 字节、指纹/trailer 落位对）。
func TestGrokStatsigIDWithFingerprintShape(t *testing.T) {
	t.Setenv("KLEIN_GROK_STATSIG_SIGNED", "true")
	fp := bytes.Repeat([]byte{0xAB}, 48)
	tok := grokStatsigIDWithFingerprint("POST", "/rest/app-chat/conversations/new", fp)
	d := decodeStatsigToken(t, tok)
	dec := d[1:]
	if !bytes.Equal(dec[0:48], fp) {
		t.Fatalf("fingerprint not embedded correctly")
	}
	if dec[68] != grokStatsigTrailer() {
		t.Fatalf("trailer = %d, want %d", dec[68], grokStatsigTrailer())
	}
}
