// Package captcha 注册流程的人机验证求解抽象。
//
// 当前实现：CapSolver（推荐 — Arkose FunCaptcha 与 Turnstile 都支持）。
// 后续可加 YesCaptcha / 2Captcha 实现，只要满足 Solver 接口即可。
package captcha

import (
	"context"
	"errors"
)

// ArkoseTask Adobe FunCaptcha 求解请求参数。
type ArkoseTask struct {
	WebsiteURL    string // 触发验证的页面 URL（来源页 referer）
	WebsiteKey    string // funcaptcha publicKey
	APISubdomain  string // funcaptcha jsSubdomain（如 client-api.arkoselabs.com）
	Blob          string // x-ims-captcha-encrypted 头里的 blob，可空
	UserAgent     string
	Proxy         string // 形如 http://user:pass@host:port，留空走 solver 默认
}

// TurnstileTask Cloudflare Turnstile 求解请求参数。
type TurnstileTask struct {
	WebsiteURL string
	WebsiteKey string // sitekey
	UserAgent  string
	Proxy      string
}

// Solver 抽象，实际实现由配置注入。
type Solver interface {
	Name() string
	SolveArkose(ctx context.Context, t *ArkoseTask) (token string, err error)
	SolveTurnstile(ctx context.Context, t *TurnstileTask) (token string, err error)
}

// ErrNotConfigured 表示 captcha solver 没有配置（缺 api_key 等）。
// dispatcher 拿到这个错误时应当向用户报"请先在系统配置里设置 captcha.api_key"。
var ErrNotConfigured = errors.New("captcha solver 未配置：请先在「系统配置 → 验证码服务」设置 CapSolver API Key")
