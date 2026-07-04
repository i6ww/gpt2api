package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 默认 API 端点。
const (
	CapSolverEndpoint   = "https://api.capsolver.com"
	TwoCaptchaEndpoint  = "https://api.2captcha.com"
	YesCaptchaEndpoint  = "https://api.yescaptcha.com"
	AntiCaptchaEndpoint = "https://api.anti-captcha.com"
	NopeCHAEndpoint     = "https://api.nopecha.com"
)

// 支持的 Variant 值。
//
// 注意 — Adobe Arkose / FunCaptcha 解题能力 (2026-05 实测):
//   - capsolver           ✗  2024 年底已废弃 FunCaptcha 支持
//   - 2captcha            ⚠  Adobe 公钥 30–45%，OpenAI 公钥 60–80%
//   - yescaptcha          ⚠  Adobe 公钥 50–60%
//   - anti-captcha        ✓  Adobe 公钥 70–85%，老牌 human solver
//   - nopecha             ✓  Adobe 公钥 60–80%，AI-first
const (
	VariantCapSolver   = "capsolver"
	Variant2Captcha    = "2captcha"
	VariantYesCaptcha  = "yescaptcha"
	VariantAntiCaptcha = "anti-captcha"
	VariantNopeCHA     = "nopecha"
)

// 单次任务最大等待时间。
//
//   - ArkoseMaxWait = 60s：Anti-Captcha 简单题 ~25-45s 出，复杂题 60s+；超过 60s
//     不出 token 大概率是 3D 旋转难题，等出来 worker 也容易蒙错。直接 timeout
//     当成"按时间筛难度"的策略，规避把钱花在 Adobe 难题上。配合 dispatcher
//     的"失败立刻跳号"形成快速失败链路。
//   - TurnstileMaxWait = 180s：Cloudflare Turnstile 走 challenge fingerprint
//     更慢，且不存在难题分级，保留旧的 180s。
const (
	ArkoseMaxWait    = 60 * time.Second
	TurnstileMaxWait = 180 * time.Second
)

// CapSolver 适配兼容 anti-captcha 风格 v2 协议的打码平台：
// CapSolver / 2Captcha / Anti-Captcha / NopeCHA / YesCaptcha / 自建中转。
// 三家请求形状基本一致（clientKey + task / createTask + getTaskResult），
// 但 task.type 字段命名略有差异，由 Variant 选择对应字符串。
type CapSolver struct {
	APIKey string
	HTTP   *http.Client
	// Endpoint 可覆盖（自建中转 / 私有部署）。
	Endpoint string
	// Variant 控制 task.type 命名规范。"capsolver" | "2captcha"，留空走 capsolver。
	Variant string
	// MaxWait 兼容字段：旧调用方还在读这个字段；新代码请优先看 SolveArkose /
	// SolveTurnstile 内的 ArkoseMaxWait / TurnstileMaxWait 常量（按 captcha
	// 类型分别覆盖 c.MaxWait）。
	MaxWait time.Duration
}

// NewCapSolver 构造，apiKey 为空会在调用时返回 ErrNotConfigured。
func NewCapSolver(apiKey string) *CapSolver {
	return &CapSolver{
		APIKey: apiKey,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
		Endpoint: CapSolverEndpoint,
		Variant:  VariantCapSolver,
		MaxWait:  180 * time.Second,
	}
}

// New2Captcha 构造一个 2Captcha v2 风格 solver。
func New2Captcha(apiKey string) *CapSolver {
	return &CapSolver{
		APIKey: apiKey,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
		Endpoint: TwoCaptchaEndpoint,
		Variant:  Variant2Captcha,
		MaxWait:  180 * time.Second,
	}
}

// NewYesCaptcha 构造 YesCaptcha 客户端。
//
// YesCaptcha 与 2Captcha 同样使用 anti-captcha v2 协议、且 Turnstile/FunCaptcha
// 任务类型字符串与 2Captcha 一致（TurnstileTaskProxyless 等），所以直接复用
// CapSolver 内核 + Variant=yescaptcha 区分日志和账单。
func NewYesCaptcha(apiKey string) *CapSolver {
	return &CapSolver{
		APIKey: apiKey,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
		Endpoint: YesCaptchaEndpoint,
		Variant:  VariantYesCaptcha,
		MaxWait:  180 * time.Second,
	}
}

// NewAntiCaptcha 构造 Anti-Captcha 客户端。
//
// Anti-Captcha 是 anti-captcha v2 协议的祖宗（2014 年起），FunCaptcha task type
// 与 2Captcha / YesCaptcha 完全一致：FunCaptchaTaskProxyless（小写 l）。
//
// 对 Adobe Arkose 公钥（436DD567-…）解题率历史稳定在 70–85%，是 2026 年
// CapSolver 砍掉 FunCaptcha 后 anti-captcha v2 协议家族里最强的选择。
func NewAntiCaptcha(apiKey string) *CapSolver {
	return &CapSolver{
		APIKey: apiKey,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
		Endpoint: AntiCaptchaEndpoint,
		Variant:  VariantAntiCaptcha,
		MaxWait:  180 * time.Second,
	}
}

// NewNopeCHA 构造 NopeCHA 客户端。
//
// NopeCHA 用纯 AI 模型解题，对 Arkose 3D 旋转题型支持稳定（60–80%），
// 协议同 anti-captcha v2，task type 与 2Captcha 一致。
func NewNopeCHA(apiKey string) *CapSolver {
	return &CapSolver{
		APIKey: apiKey,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
		Endpoint: NopeCHAEndpoint,
		Variant:  VariantNopeCHA,
		MaxWait:  180 * time.Second,
	}
}

// Name 实现 Solver。
func (c *CapSolver) Name() string {
	if c == nil {
		return ""
	}
	if c.Variant != "" {
		return c.Variant
	}
	return VariantCapSolver
}

// isAntiCaptchaV2Family 当前 variant 是不是走 anti-captcha v2 协议族（task type
// 用小写 l 的那一系：FunCaptchaTaskProxyless / TurnstileTaskProxyless 等）。
//
// CapSolver 是个例外，他用大写 L（FunCaptchaTaskProxyLess）；其他都是小写 l。
func (c *CapSolver) isAntiCaptchaV2Family() bool {
	switch c.Variant {
	case Variant2Captcha, VariantYesCaptcha, VariantAntiCaptcha, VariantNopeCHA:
		return true
	}
	return false
}

// arkoseType 根据 variant 选 FunCaptcha 任务类型字符串。
func (c *CapSolver) arkoseType(withProxy bool) string {
	if withProxy {
		return "FunCaptchaTask"
	}
	if c.isAntiCaptchaV2Family() {
		return "FunCaptchaTaskProxyless" // anti-captcha 家族：小写 l
	}
	return "FunCaptchaTaskProxyLess" // capsolver：大写 L
}

// turnstileType 根据 variant 选 Turnstile 任务类型字符串。
func (c *CapSolver) turnstileType(withProxy bool) string {
	if c.isAntiCaptchaV2Family() {
		if withProxy {
			return "TurnstileTask"
		}
		return "TurnstileTaskProxyless"
	}
	if withProxy {
		return "AntiTurnstileTask"
	}
	return "AntiTurnstileTaskProxyLess"
}

// SolveArkose 调用 FunCaptcha solver。
//
// 当 t.Proxy 非空时，按 anti-captcha v2 规范把 proxyType / proxyAddress /
// proxyPort / proxyLogin / proxyPassword 拆开发给打码平台（CapSolver 与
// 2Captcha 的字段命名一致），并选用带 Proxy 的任务类型。
//
// 注：Adobe Arkose **不绑定客户端 IP**，强行转发代理只会增加 BAD_PROXY 风险，
// 因此 dispatcher 通常应当显式留空 t.Proxy 走 Proxyless。
func (c *CapSolver) SolveArkose(ctx context.Context, t *ArkoseTask) (string, error) {
	if c == nil || strings.TrimSpace(c.APIKey) == "" {
		return "", ErrNotConfigured
	}
	hasProxy := t.Proxy != "" && c.applyProxy(nil, t.Proxy) == nil
	task := map[string]any{
		"type":                     c.arkoseType(hasProxy),
		"websiteURL":               t.WebsiteURL,
		"websitePublicKey":         t.WebsiteKey,
		"funcaptchaApiJSSubdomain": t.APISubdomain,
		"userAgent":                t.UserAgent,
	}
	if t.Blob != "" {
		task["data"] = fmt.Sprintf("{\"blob\":%q}", t.Blob)
	}
	if hasProxy {
		_ = c.applyProxy(task, t.Proxy)
	}
	taskID, err := c.createTask(ctx, task)
	if err != nil {
		return "", err
	}
	res, err := c.waitTaskResult(ctx, taskID, ArkoseMaxWait)
	if err != nil {
		return "", err
	}
	if v, ok := res["token"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New(c.Name() + ": arkose 响应缺少 token 字段")
}

// SolveTurnstile 调用 Turnstile solver。
//
// 与 Arkose 不同，Turnstile 的 worker 经常被 Cloudflare 直接绑定客户端 IP，
// 因此 t.Proxy 非空时优先转发，让 worker 用相同出口求解。
func (c *CapSolver) SolveTurnstile(ctx context.Context, t *TurnstileTask) (string, error) {
	if c == nil || strings.TrimSpace(c.APIKey) == "" {
		return "", ErrNotConfigured
	}
	hasProxy := t.Proxy != "" && c.applyProxy(nil, t.Proxy) == nil
	task := map[string]any{
		"type":       c.turnstileType(hasProxy),
		"websiteURL": t.WebsiteURL,
		"websiteKey": t.WebsiteKey,
		"userAgent":  t.UserAgent,
	}
	if hasProxy {
		_ = c.applyProxy(task, t.Proxy)
	}
	taskID, err := c.createTask(ctx, task)
	if err != nil {
		return "", err
	}
	res, err := c.waitTaskResult(ctx, taskID, TurnstileMaxWait)
	if err != nil {
		return "", err
	}
	if v, ok := res["token"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New(c.Name() + ": turnstile 响应缺少 token 字段")
}

// applyProxy 把 http://user:pass@host:port 拆成 proxyType / proxyAddress /
// proxyPort / proxyLogin / proxyPassword 写入 task。task 为 nil 时仅做
// 解析校验（用来决定走 Proxyless 还是 Proxy 任务类型）。
//
// 解析失败时返回 error，调用方应该退化为 Proxyless 模式而不是把破代理
// 直接丢给打码平台 — 后者会立刻报 ERROR_BAD_PROXY。
func (c *CapSolver) applyProxy(task map[string]any, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("captcha: 代理 URL 为空")
	}
	if !strings.Contains(raw, "://") {
		// 兼容 host:port 形式，默认 http
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("captcha: 代理 URL 解析失败：%w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "socks4", "socks5":
	case "socks5h":
		scheme = "socks5"
	default:
		return fmt.Errorf("captcha: 不支持的代理 scheme: %q", u.Scheme)
	}
	host := u.Hostname()
	portStr := u.Port()
	if host == "" || portStr == "" {
		return errors.New("captcha: 代理 URL 缺少 host/port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("captcha: 代理端口非法: %q", portStr)
	}
	if task == nil {
		return nil
	}
	task["proxyType"] = scheme
	task["proxyAddress"] = host
	task["proxyPort"] = port
	if u.User != nil {
		task["proxyLogin"] = u.User.Username()
		if pwd, ok := u.User.Password(); ok && pwd != "" {
			task["proxyPassword"] = pwd
		}
	}
	return nil
}

// === internal ===

// taskID 兼容三种返回形式：
//   - CapSolver / YesCaptcha：字符串 UUID（带引号）
//   - 2Captcha：int64 裸数字
//   - 极个别 anti-captcha v2 兼容服务：浮点
//
// 因此用 any 接收，再用 normalizeTaskID 拉成字符串。
type capSolverResp struct {
	ErrorID          int             `json:"errorId"`
	ErrorCode        string          `json:"errorCode"`
	ErrorDescription string          `json:"errorDescription"`
	TaskID           any             `json:"taskId"`
	Status           string          `json:"status"`
	Solution         json.RawMessage `json:"solution"`
}

// normalizeTaskID 把 capSolverResp.TaskID 这个 any 字段统一为非空 string。
//
// 注意：JSON Decoder 默认会把数字解析成 float64。这里专门处理一下避免出现
// "1.703e+09" 这种浮点字面量。
func normalizeTaskID(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return strings.TrimSpace(string(x))
	case float64:
		// 整数走 %d，避免 1.7e+09 这种科学计数法。
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
	case int64:
		return fmt.Sprintf("%d", x)
	case int:
		return fmt.Sprintf("%d", x)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", x))
	}
}

func (c *CapSolver) createTask(ctx context.Context, task map[string]any) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"clientKey": c.APIKey,
		"task":      task,
	})
	r, err := c.do(ctx, "/createTask", body)
	if err != nil {
		return "", err
	}
	if r.ErrorID != 0 {
		return "", fmt.Errorf("%s createTask: %s — %s", c.Name(), r.ErrorCode, r.ErrorDescription)
	}
	taskID := normalizeTaskID(r.TaskID)
	if taskID == "" {
		return "", fmt.Errorf("%s createTask: 返回缺少 taskId", c.Name())
	}
	return taskID, nil
}

// waitTaskResult 轮询打码平台拿结果。
//
// maxWait 由调用方按 captcha 类型决定（Arkose 60s / Turnstile 180s）；
// 留 0 时回落到 c.MaxWait（保留旧入口的兼容行为）。
func (c *CapSolver) waitTaskResult(ctx context.Context, taskID string, maxWait time.Duration) (map[string]any, error) {
	if maxWait <= 0 {
		maxWait = c.MaxWait
	}
	if maxWait <= 0 {
		maxWait = 60 * time.Second
	}
	deadline := time.Now().Add(maxWait)
	// 2Captcha 期望 taskId 为数字；CapSolver 期望字符串 UUID。
	// 这里用 json.Number 承载，json 序列化时数字仍然以裸数字字面量出现（不带引号）。
	var taskIDValue any = taskID
	if _, err := json.Number(taskID).Int64(); err == nil {
		taskIDValue = json.Number(taskID)
	}
	body, _ := json.Marshal(map[string]any{
		"clientKey": c.APIKey,
		"taskId":    taskIDValue,
	})
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		r, err := c.do(ctx, "/getTaskResult", body)
		if err != nil {
			continue
		}
		if r.ErrorID != 0 {
			return nil, fmt.Errorf("%s getTaskResult: %s — %s", c.Name(), r.ErrorCode, r.ErrorDescription)
		}
		switch r.Status {
		case "ready":
			var sol map[string]any
			if err := json.Unmarshal(r.Solution, &sol); err != nil {
				return nil, fmt.Errorf("%s: 解析 solution 失败：%w", c.Name(), err)
			}
			return sol, nil
		case "processing":
			continue
		default:
		}
	}
	return nil, fmt.Errorf("%s: 等待求解超时", c.Name())
}

func (c *CapSolver) do(ctx context.Context, path string, body []byte) (*capSolverResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r capSolverResp
	if err := json.Unmarshal(raw, &r); err != nil {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return nil, fmt.Errorf("%s: 解析响应失败 (%s)：%s", c.Name(), err.Error(), snippet)
	}
	return &r, nil
}
