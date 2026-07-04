package service

import (
	"context"
	"os"
	"strings"
)

// MediaPublicBase 返回对外暴露媒体资源用的站点根 URL（无尾斜杠）。
//
// 优先级：media.base_url → server.public_base_url → oss.public_base_url
// → 当前请求 Origin → KLEIN_CORS_ORIGINS 里第一个公网 origin。
func MediaPublicBase(ctx context.Context, cfg *SystemConfigService, requestOrigin string) string {
	if cfg != nil {
		for _, key := range []string{"media.base_url", "server.public_base_url", "oss.public_base_url"} {
			if v := strings.TrimRight(strings.TrimSpace(cfg.GetString(ctx, key, "")), "/"); v != "" {
				return v
			}
		}
	}
	if origin := strings.TrimRight(strings.TrimSpace(requestOrigin), "/"); origin != "" {
		return origin
	}
	return firstPublicOrigin(os.Getenv("KLEIN_CORS_ORIGINS"))
}

// AbsolutizeMediaURL 把 /api/v1/gen/cached/... 等站内相对路径补全为带域名的绝对 URL。
// 已是 http(s)/data 的原样返回；未配置 public base 时保留相对路径。
func AbsolutizeMediaURL(ctx context.Context, cfg *SystemConfigService, requestOrigin, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "data:") {
		return raw
	}
	if !strings.HasPrefix(raw, "/") {
		return raw
	}
	base := MediaPublicBase(ctx, cfg, requestOrigin)
	if base == "" {
		return raw
	}
	return base + raw
}

func firstPublicOrigin(raw string) string {
	for _, item := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(item)
		if origin == "" {
			continue
		}
		lower := strings.ToLower(origin)
		if strings.Contains(lower, "localhost") || strings.Contains(lower, "127.0.0.1") {
			continue
		}
		if strings.HasSuffix(lower, ":9001") || strings.Contains(lower, "/admin") {
			continue
		}
		return strings.TrimRight(origin, "/")
	}
	return ""
}

func RequestPublicOrigin(scheme, host string) string {
	scheme = strings.TrimSpace(scheme)
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if scheme == "" {
		scheme = "https"
	}
	return strings.TrimRight(scheme+"://"+host, "/")
}
