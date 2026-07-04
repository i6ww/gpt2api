// Package provider 第三方生成提供方抽象（GPT 生图 / GROK 生视频 等）。
//
// 真正的协议适配在子包内（gpt / grok / mock），调度器只依赖接口。
package provider

import (
	"context"
	"time"

	"github.com/kleinai/backend/internal/model"
)

// Kind 生成类型。
type Kind string

const (
	KindChat  Kind = "chat"
	KindImage Kind = "image"
	KindVideo Kind = "video"
	KindMusic Kind = "music"
)

// Mode 生成模式。
type Mode string

const (
	ModeT2I Mode = "t2i"
	ModeI2I Mode = "i2i"
	ModeT2V Mode = "t2v"
	ModeI2V Mode = "i2v"
	// ModeT2A text-to-audio（音乐/歌曲生成，FlowMusic）。
	ModeT2A Mode = "t2a"
)

// Request 通用生成请求。
type Request struct {
	TaskID    string
	Kind      Kind
	Mode      Mode
	ModelCode string
	Prompt    string
	NegPrompt string
	Params    map[string]any
	RefAssets []string
	Count     int
	Account   *model.Account
	// Credential 是 Account.CredentialEnc 解密后的明文（API Key / Cookie / OAuth Token）。
	// 调用方负责解密，provider 不再持有 AESGCM。
	Credential string
	// BaseURL 优先级：account.base_url > provider 默认。
	BaseURL  string
	ProxyURL string
	// AdobeSubmitMode Adobe 上游提交通道："clio"（默认空也按 clio）| "psweb"。
	// psweb 时 Credential 已被服务层换成 PSWebApp1 token，provider 据此切换 x-api-key /
	// Origin 并去掉 x-nonce / x-arp-session-id。
	AdobeSubmitMode string
	// UpstreamLog records provider stage diagnostics for admin troubleshooting.
	UpstreamLog UpstreamLogger
	// OnPollProgress 上游异步轮询进度（如 Adobe Firefly poll retry-after / progress）。
	OnPollProgress PollProgressFunc
}

type PollProgressFunc func(ctx context.Context, progress, retryAfterSec int)

type UpstreamLogEntry struct {
	Provider        string
	Stage           string
	Method          string
	URL             string
	StatusCode      int
	DurationMs      int64
	RequestExcerpt  string
	ResponseExcerpt string
	Error           string
	Meta            map[string]any
}

type UpstreamLogger func(ctx context.Context, entry UpstreamLogEntry)

// Asset 单个生成资产（一张图 / 一段视频）。
type Asset struct {
	URL        string
	ThumbURL   string
	Width      int
	Height     int
	DurationMs int
	SizeBytes  int64
	Mime       string
	Meta       map[string]any
}

// Result 通用生成结果。
//
// EffectiveModelCode：实际跑通的模型 code。一般等于 Request.ModelCode，但
// 当 provider 在内部做了"通道降级"（例如 grok 主通道 429 → 自动 fallback
// 到免额度 imagine pipeline）时，会把 EffectiveModelCode 改成真实跑的那个
// model_code（比如 "grok-imagine-video-6s-free"）。上层（generation_service）
// 据此触发"按差价部分退款"，避免用户被按主通道价收费却拿到了免费通道的输出。
type Result struct {
	TaskID             string
	Assets             []Asset
	Latency            time.Duration
	EffectiveModelCode string
}

// Provider 提供方接口。
type Provider interface {
	// Name 返回 provider 标识，例如 "gpt" / "grok"。
	Name() string
	// Generate 同步发起一次生成（worker 内部使用）。
	Generate(ctx context.Context, req *Request) (*Result, error)
}
