// Package gpt 实现 ChatGPT / OpenAI 账号自动注册 dispatcher。
//
// Sentinel 工作量证明实现，参考 basketikun/chatgpt2api 的 services/register/openai_register.py：
//
//   - SentinelTokenGenerator 生成"配置数组" + base64 + FNV1a-32 PoW
//   - /backend-api/sentinel/req 拿 seed + difficulty
//   - 返回 openai-sentinel-token 头值（JSON 字符串）
//
// 注：本实现需要使用与注册请求 *相同* 的 http client（共享 cookie、UA 与代理），
// 否则 sentinel 后端会发现"req 来源与下游 API 不一致"并把 token 标为可疑。
package gpt

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math/big"
	mrand "math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// sentinelMaxAttempts 单次 PoW 求解最多尝试次数。Python 上限是 500 000；
	// 实测一般在 1 万内即可命中常见 difficulty（4 位 hex）。
	sentinelMaxAttempts = 500000
	// sentinelErrPrefix Python 实现里"放弃后"的占位前缀；保持一致以便未来对比。
	sentinelErrPrefix = "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D"

	sentinelSDKURL = "https://sentinel.openai.com/sentinel/20260124ceb8/sdk.js"
)

// SentinelGenerator 生成 OpenAI Sentinel token 的辅助器。
//
// 一个 PlatformRegistrar 实例持有一个 generator；deviceID 与 sid 在生命周期内固定。
type SentinelGenerator struct {
	deviceID  string
	userAgent string
	sid       string

	// 这些是 PoW 配置数组里的几个会跨次共享的字段，避免每次都换。
	resolution   string
	language     string
	cores        int
	historyLen   int64
	flagsVendor  string
	flagsDoc     string
	flagsGlobal  string
	rngSeed      *mrand.Rand
	rngMu        sync.Mutex
}

// NewSentinelGenerator 创建一个生成器。device_id / sid 都是 UUIDv4。
func NewSentinelGenerator(deviceID, userAgent string) *SentinelGenerator {
	return &SentinelGenerator{
		deviceID:    deviceID,
		userAgent:   userAgent,
		sid:         newDeviceID(),
		resolution:  "1920x1080",
		language:    "en-US",
		cores:       []int{4, 8, 12, 16}[time.Now().UnixNano()%4],
		historyLen:  4294705152,
		flagsVendor: pickRand([]string{"vendorSub-undefined", "plugins-undefined", "mimeTypes-undefined", "hardwareConcurrency-undefined"}),
		flagsDoc:    pickRand([]string{"location", "implementation", "URL", "documentURI", "compatMode"}),
		flagsGlobal: pickRand([]string{"Object", "Function", "Array", "Number", "parseFloat", "undefined"}),
		rngSeed:     mrand.New(mrand.NewSource(time.Now().UnixNano())),
	}
}

// configArray 构造 PoW 数组。布局严格对齐 Python 实现，尤其是字段索引：
//
//	[0]  屏幕分辨率
//	[1]  当前 GMT 时间字符串
//	[2]  windows.history.length-类大数
//	[3]  随机/迭代计数（求解时被覆写）
//	[4]  user-agent
//	[5]  sentinel sdk.js URL
//	[6]  null
//	[7]  null
//	[8]  navigator.language
//	[9]  随机值（求解时被覆写为 elapsed_ms）
//	[10] vendor 反指纹标志
//	[11] document 反指纹标志
//	[12] global 反指纹标志
//	[13] performance.now() 抖动
//	[14] sid（uuid）
//	[15] 空串
//	[16] hardwareConcurrency
//	[17] (Date.now() * 1000 - perf_now)
func (g *SentinelGenerator) configArray() []any {
	g.rngMu.Lock()
	perfNow := 1000.0 + g.rngSeed.Float64()*49000.0
	rng := g.rngSeed.Float64()
	g.rngMu.Unlock()
	return []any{
		g.resolution,
		time.Now().UTC().Format("Mon Jan _2 2026 15:04:05 GMT+0000 (Coordinated Universal Time)"),
		g.historyLen,
		rng,
		g.userAgent,
		sentinelSDKURL,
		nil,
		nil,
		g.language,
		rng,
		g.flagsVendor,
		g.flagsDoc,
		g.flagsGlobal,
		perfNow,
		g.sid,
		"",
		g.cores,
		float64(time.Now().UnixMilli()) - perfNow,
	}
}

// b64 把 JSON 序列化后做标准 base64（不含 padding 影响时再裁）。
//
// 注意：与 Python json.dumps(separators=(",",":")) 保持一致，禁止空格。
func (g *SentinelGenerator) b64(v any) string {
	b, _ := json.Marshal(v)
	// Go 默认在 map / struct 之间的 ","" 和 ":" 已经无空格；slice 同理。
	return base64.StdEncoding.EncodeToString(b)
}

// RequirementsToken 生成"先验"token，用于初次发 /sentinel/req 时携带。
//
// data[3]=1; data[9]=round(uniform(5,50)) — 与 Python 实现保持完全一致。
func (g *SentinelGenerator) RequirementsToken() string {
	data := g.configArray()
	data[3] = 1
	g.rngMu.Lock()
	data[9] = float64(int(g.rngSeed.Float64()*45 + 5))
	g.rngMu.Unlock()
	return "gAAAAAC" + g.b64(data)
}

// SolvePoW 给定 seed + difficulty，暴力求解使 fnv1a_32(seed + payload).hex[:n] <= difficulty。
//
//   - seed 来自 /sentinel/req 响应 proofofwork.seed
//   - difficulty 是 hex 字符串前缀（如 "00fff"），返回 hex 必须 ≤ 它（字典序比较）
//
// 失败回到占位字符串（与 Python 保持兼容，调用方会得到一份能通过 OpenAI 接受度
// 较低 endpoint 的 fallback token）。
func (g *SentinelGenerator) SolvePoW(seed, difficulty string) string {
	if difficulty == "" {
		difficulty = "0"
	}
	start := time.Now()
	data := g.configArray()
	dl := len(difficulty)
	for i := 0; i < sentinelMaxAttempts; i++ {
		data[3] = i
		data[9] = float64(time.Since(start).Milliseconds())
		payload := g.b64(data)
		h := fnv.New32a()
		h.Write([]byte(seed))
		h.Write([]byte(payload))
		hexStr := fmt.Sprintf("%08x", h.Sum32())
		if dl > len(hexStr) {
			dl = len(hexStr)
		}
		if hexStr[:dl] <= difficulty[:dl] {
			return "gAAAAAB" + payload + "~S"
		}
	}
	// 没解出来：返回 Python 同样的占位串，让调用方仍能尝试一下。
	return "gAAAAAB" + sentinelErrPrefix + g.b64(nil)
}

// SentinelToken 调 /backend-api/sentinel/req 拿 seed/difficulty，再算 PoW，
// 最后拼成 openai-sentinel-token 头值（JSON 字符串）。
//
// 必须用与下游 API 相同的 client（共享 cookie / UA / 代理），否则 sentinel
// 会判 token 来源异常。
//
// 容错：sentinel.openai.com 走某些代理时偶尔会出现 TLS 抖动或 CONNECT 反复
// 被 reset；这里加一次自动重试 + 失败时退化为 RequirementsToken 兜底（与
// Python 参考实现的"放弃后仍用占位 token 继续"策略一致）。
func (g *SentinelGenerator) SentinelToken(ctx context.Context, hc *http.Client, flow string) (string, error) {
	tok, err := g.callSentinelReq(ctx, hc, flow)
	if err != nil {
		// 重试一次：sentinel.openai.com 容易因 CONNECT 抖动失败。
		time.Sleep(800 * time.Millisecond)
		tok2, err2 := g.callSentinelReq(ctx, hc, flow)
		if err2 == nil {
			return tok2, nil
		}
		// 真的拿不到 — 退化用 RequirementsToken 拼一个 best-effort token。
		// OpenAI 对 email-otp/validate 这类弱端点接受度较高，至少能让流程跑完。
		return g.fallbackToken(flow), nil
	}
	return tok, nil
}

func (g *SentinelGenerator) callSentinelReq(ctx context.Context, hc *http.Client, flow string) (string, error) {
	body := map[string]any{
		"p":    g.RequirementsToken(),
		"id":   g.deviceID,
		"flow": flow,
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://sentinel.openai.com/backend-api/sentinel/req", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Origin", "https://sentinel.openai.com")
	req.Header.Set("Referer", "https://sentinel.openai.com/backend-api/sentinel/frame.html")
	req.Header.Set("User-Agent", g.userAgent)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("sentinel/req 网络错误：%w", err)
	}
	defer resp.Body.Close()
	respRaw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sentinel/req HTTP %d: %s", resp.StatusCode, snippetBytes(respRaw))
	}
	var data struct {
		Token       string `json:"token"`
		Proofofwork struct {
			Required   bool   `json:"required"`
			Seed       string `json:"seed"`
			Difficulty string `json:"difficulty"`
		} `json:"proofofwork"`
	}
	if err := json.Unmarshal(respRaw, &data); err != nil {
		return "", fmt.Errorf("sentinel/req 响应非 JSON: %s", snippetBytes(respRaw))
	}
	if data.Token == "" {
		return "", fmt.Errorf("sentinel/req 响应缺少 token: %s", snippetBytes(respRaw))
	}
	var p string
	if data.Proofofwork.Required && data.Proofofwork.Seed != "" {
		p = g.SolvePoW(data.Proofofwork.Seed, data.Proofofwork.Difficulty)
	} else {
		p = g.RequirementsToken()
	}
	out := map[string]any{
		"p":    p,
		"t":    "",
		"c":    data.Token,
		"id":   g.deviceID,
		"flow": flow,
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// fallbackToken 当 /sentinel/req 完全连不通时，用本地 RequirementsToken 拼
// 一个不带 server-side challenge 的 token。能让 register / validate 类弱
// 端点继续往下走，create_account / authorize_continue 这种强端点仍可能拒绝。
func (g *SentinelGenerator) fallbackToken(flow string) string {
	out := map[string]any{
		"p":    g.RequirementsToken(),
		"t":    "",
		"c":    "",
		"id":   g.deviceID,
		"flow": flow,
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// PKCEPair 构造 PKCE code_verifier / code_challenge（S256）。
type PKCEPair struct {
	Verifier  string
	Challenge string
}

// NewPKCE 生成新 PKCE 对（S256）。
//
// verifier = base64url(64 random bytes); challenge = base64url(sha256(verifier))
// 严格按 OpenAI auth0 SPA 客户端的实现来——bytes 数量很关键。
func NewPKCE() PKCEPair {
	v := base64URL(randomBytes(64))
	sum := sha256.Sum256([]byte(v))
	c := base64URL(sum[:])
	return PKCEPair{Verifier: v, Challenge: c}
}

// === 内部工具 ===

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func base64URL(b []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}

// 设备 ID（UUIDv4 简化）。
func newDeviceID() string {
	b := randomBytes(16)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// pickRand 用 crypto/rand 随机选一个字符串（保证 dispatch 之间多样性）。
func pickRand(opts []string) string {
	if len(opts) == 0 {
		return ""
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(opts))))
	return opts[n.Int64()]
}

func snippetBytes(b []byte) string {
	const max = 240
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
