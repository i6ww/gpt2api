// Package xai 实现「官方 xAI API」provider（api.x.ai/v1，OAuth Bearer）。
//
// 与 provider/grok（grok.com Web SSO 通道）是两条独立通道：
//   - chat  : POST {base}/responses （Codex/Responses 协议，SSE，解析 response.completed）
//   - video : POST {base}/videos/generations + GET {base}/videos/{id} 轮询
//
// 凭证（access_token）由 pool_xai 提供，调用方解密后通过 req.Credential / token 传入。
// 参考 router-for-me/CLIProxyAPI（internal/runtime/executor/xai_executor.go）。
package xai

import "strings"

// DefaultBaseURL xAI 官方 API base。
const DefaultBaseURL = "https://api.x.ai/v1"

// 默认 chat 模型 ID（官方 Responses 端点支持的 grok 系列）。
// 实际可用模型以账号权限为准；这里登记常见值用于路由识别 + /v1/models 列举。
var chatModels = []string{
	"grok-4",
	"grok-4-fast",
	"grok-4.1",
	"grok-4.3",
	"grok-3",
	"grok-3-mini",
	"grok-code-fast-1",
}

// 默认 video 模型 ID。
var videoModels = []string{
	"grok-video",
	"grok-imagine-video",
}

// 基础文生视频模型（t2v，也兼容无图请求）。
const baseVideoModel = "grok-imagine-video"

// I2VVideoModel 图生视频专用模型。grok-imagine-video-1.5 上游**只支持图生视频**
// （纯文字会被拒：Text-to-video is not supported for this model），画质更新。
const I2VVideoModel = "grok-imagine-video-1.5"

// ResolveVideoModel 根据请求是否携带输入图，选真正发给 api.x.ai 的视频模型：
//
//	有图 → grok-imagine-video-1.5（图生视频）
//	无图 → grok-imagine-video（文生视频）
//
// 对外始终是同一个 xai/grok-imagine-video，计费不分档——用户传不传图都一个价。
// 仅当外部模型解析后是基础 grok-imagine-video 时才做这个切换，避免影响其它 video id。
func ResolveVideoModel(modelCode string, hasImage bool) string {
	base := UpstreamModel(modelCode)
	if hasImage && base == baseVideoModel {
		return I2VVideoModel
	}
	return base
}

// ChatModelIDs 返回官方 xAI chat 模型列表（用于 /v1/models 列举）。
func ChatModelIDs() []string {
	out := make([]string, len(chatModels))
	copy(out, chatModels)
	return out
}

// IsChatModel 判断 modelCode 是否走官方 xAI chat 通道。
//
// 约定：**必须**以 "xai/" 前缀显式指定（如 "xai/grok-4.3"）。这样和 provider/grok
// 的 Web chat 通道（裸 grok-* ID）明确区分，避免裸 ID 把 grok web 请求劫持到官方 API。
func IsChatModel(modelCode string) bool {
	return strings.HasPrefix(normalize(modelCode), "xai/")
}

// IsVideoModel 判断 modelCode 是否走官方 xAI video 通道。
func IsVideoModel(modelCode string) bool {
	code := normalize(modelCode)
	for _, m := range videoModels {
		if code == m {
			return true
		}
	}
	return strings.HasPrefix(code, "xai/") && strings.Contains(code, "video")
}

// UpstreamModel 去掉 "xai/" 路由前缀，返回真正发给 api.x.ai 的模型名。
func UpstreamModel(modelCode string) string {
	code := normalize(modelCode)
	return strings.TrimPrefix(code, "xai/")
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
