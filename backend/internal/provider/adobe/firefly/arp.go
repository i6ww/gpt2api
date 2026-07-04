package firefly

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// x-arp-session-id 是 firefly.adobe.com 网页端的反爬/会话头。2026-06 起 Adobe
// 对缺失该头的 generate-async 请求伪装成 408 "system under load" 拒绝（实测：
// 不带 → 408，带任意结构合法值 → 200）。
//
// 经服务器实测：Adobe 不验证 ftr 里的滚动签名、也不验证设备指纹，仅校验该头
// 存在且 base64 解出 {sid, ftr} 结构。这里按浏览器形态合成一个即可，无需逆向。
//
// 当前 Firefly Web 会多带 ark 字段，缺失时部分边缘节点会把请求伪装成
// 408 "system under load"。ark 里的 public key / region / cdn 参数来自网页端
// Arkose 初始化请求；ftr 仍可每次随机生成。
//
// 结构（base64 of JSON）：
//
//	{"sid":"<uuid4>","ark":"...","ftr":"<fp32hex>_<unixMs>__UDF43-m4_31ck_<b64(8B)>-<4digit>-v2_tt"}
func generateARPSessionID() string {
	sid := randomUUIDv4()
	fp := randomHex(16) // 32 hex chars
	ts := time.Now().UnixMilli()
	sig := base64.StdEncoding.EncodeToString(randomBytes(8))
	num := randomDigits4()
	ftr := fmt.Sprintf("%s_%d__UDF43-m4_31ck_%s-%s-v2_tt", fp, ts, sig, num)
	ark := fmt.Sprintf("%s|r=ap-southeast-1|meta=3|metabgclr=transparent|metaiconclr=%%23757575|guitextcolor=%%23000000|pk=BBCC314C-4937-4CCD-B0A3-FDF0F0F7603C|at=40|sup=1|rid=18|ag=101|cdn_url=https%%3A%%2F%%2Farks-client.adobe.com%%2Fcdn%%2Ffc|surl=https%%3A%%2F%%2Farks-client.adobe.com|smurl=https%%3A%%2F%%2Farks-client.adobe.com%%2Fcdn%%2Ffc%%2Fassets%%2Fstyle-manager", randomArkID())

	payload, _ := json.Marshal(struct {
		SID string `json:"sid"`
		ARK string `json:"ark"`
		FTR string `json:"ftr"`
	}{SID: sid, ARK: ark, FTR: ftr})
	return base64.StdEncoding.EncodeToString(payload)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func randomHex(n int) string {
	return hex.EncodeToString(randomBytes(n))
}

func randomUUIDv4() string {
	b := randomBytes(16)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomDigits4() string {
	b := randomBytes(2)
	n := (int(b[0])<<8 | int(b[1])) % 9000
	return fmt.Sprintf("%d", 1000+n)
}

func randomArkID() string {
	return fmt.Sprintf("%s.%010d", randomHex(8), time.Now().UnixNano()%10_000_000_000)
}
