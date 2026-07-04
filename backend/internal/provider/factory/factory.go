// Package factory 根据环境变量选择 真实 / mock provider。
//
// env：
//   KLEIN_PROVIDER_GPT  = "real" | "mock"   (默认 mock)
//   KLEIN_PROVIDER_GROK = "real" | "mock"   (默认 mock)
//   KLEIN_GPT_BASE_URL  = 默认 base url（账号未配置 base_url 时使用）
//   KLEIN_GROK_BASE_URL = 默认 base url
//
// 这样可以做：开发期 mock，生产期 real，无需改代码。
package factory

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/provider/adobe"
	"github.com/kleinai/backend/internal/provider/flowmusic"
	"github.com/kleinai/backend/internal/provider/gpt"
	"github.com/kleinai/backend/internal/provider/grok"
	"github.com/kleinai/backend/internal/provider/mock"
	"github.com/kleinai/backend/internal/provider/pic2api"
	"github.com/kleinai/backend/internal/provider/xai"
)

// Build 根据环境变量构造 provider 集。
func Build() map[string]provider.Provider {
	return map[string]provider.Provider{
		"gpt":       buildGPT(),
		"grok":      buildGrok(),
		"xai":       buildXAI(),
		"pic2api":   buildPIC2API(),
		"adobe":     buildAdobe(),
		"flowmusic": buildFlowMusic(),
	}
}

func buildXAI() provider.Provider {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("KLEIN_PROVIDER_XAI")))
	switch mode {
	case "real", "live", "prod":
		return xai.New(strings.TrimSpace(os.Getenv("KLEIN_XAI_BASE_URL")))
	default:
		return mock.New("xai")
	}
}

func buildGPT() provider.Provider {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("KLEIN_PROVIDER_GPT")))
	switch mode {
	case "real", "live", "prod":
		return gpt.New(strings.TrimSpace(os.Getenv("KLEIN_GPT_BASE_URL")))
	default:
		return mock.New("gpt")
	}
}

func buildGrok() provider.Provider {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("KLEIN_PROVIDER_GROK")))
	switch mode {
	case "real", "live", "prod":
		return grok.New(strings.TrimSpace(os.Getenv("KLEIN_GROK_BASE_URL")))
	default:
		return mock.New("grok")
	}
}

func buildPIC2API() provider.Provider {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("KLEIN_PROVIDER_PIC2API")))
	switch mode {
	case "real", "live", "prod":
		return pic2api.New(strings.TrimSpace(os.Getenv("KLEIN_PIC2API_BASE_URL")))
	default:
		return mock.New("pic2api")
	}
}

func buildAdobe() provider.Provider {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("KLEIN_PROVIDER_ADOBE")))
	switch mode {
	case "real", "live", "prod":
		return adobe.New()
	default:
		return mock.New("adobe")
	}
}

func buildFlowMusic() provider.Provider {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("KLEIN_PROVIDER_FLOWMUSIC")))
	switch mode {
	case "real", "live", "prod":
		return flowmusic.New(flowmusic.Config{
			BaseURL:                 strings.TrimSpace(os.Getenv("KLEIN_FLOWMUSIC_BASE_URL")),
			SupabaseBaseURL:         strings.TrimSpace(os.Getenv("KLEIN_FLOWMUSIC_SUPABASE_BASE_URL")),
			SupabaseAnonKey:         strings.TrimSpace(os.Getenv("KLEIN_FLOWMUSIC_SUPABASE_ANON_KEY")),
			GoogleOAuthTokenURL:     strings.TrimSpace(os.Getenv("KLEIN_FLOWMUSIC_GOOGLE_OAUTH_TOKEN_URL")),
			GoogleOAuthClientID:     strings.TrimSpace(os.Getenv("KLEIN_FLOWMUSIC_GOOGLE_OAUTH_CLIENT_ID")),
			GoogleOAuthClientSecret: strings.TrimSpace(os.Getenv("KLEIN_FLOWMUSIC_GOOGLE_OAUTH_CLIENT_SECRET")),
			UpstreamTimeout:         envDuration("KLEIN_FLOWMUSIC_UPSTREAM_TIMEOUT_SECONDS", 0),
			StreamIdleTimeout:       envDuration("KLEIN_FLOWMUSIC_STREAM_IDLE_TIMEOUT_SECONDS", 0),
		})
	default:
		return mock.New("flowmusic")
	}
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return time.Duration(n) * time.Second
}
