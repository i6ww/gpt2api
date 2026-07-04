package service

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
)

// Custom upstream API accounts should use direct egress by default.
// Only an explicitly bound account proxy should affect them.
func shouldDirectConnectCustomUpstream(acc *model.Account) bool {
	if acc == nil || acc.AuthType != model.AuthTypeAPIKey || acc.BaseURL == nil {
		return false
	}
	if acc.ProxyID != nil {
		return false
	}
	return strings.TrimSpace(*acc.BaseURL) != ""
}

type proxyResolverConfig interface {
	GlobalProxyEnabled(ctx context.Context) bool
	GlobalProxyID(ctx context.Context) uint64
	// AdobeProxyEnabled / AdobeProxyID 让 Adobe 走专用代理（绕过 Adobe 区域风控）。
	// 仅当账号 provider=adobe 时生效；为 false / 0 时回落到普通解析逻辑。
	AdobeProxyEnabled(ctx context.Context) bool
	AdobeProxyID(ctx context.Context) uint64
}

var grokProxyPoolCursor uint64

func resolveAccountProxyURL(
	ctx context.Context,
	proxySvc *ProxyService,
	cfg proxyResolverConfig,
	acc *model.Account,
	exclude map[uint64]struct{},
	allowPoolFallback bool,
) (string, uint64, error) {
	if proxySvc == nil || cfg == nil {
		return "", 0, nil
	}
	if shouldDirectConnectCustomUpstream(acc) {
		return "", 0, nil
	}

	seen := map[uint64]struct{}{}
	tryProxy := func(pid uint64) (string, uint64, bool, error) {
		if pid == 0 {
			return "", 0, false, nil
		}
		if _, ok := seen[pid]; ok {
			return "", 0, false, nil
		}
		seen[pid] = struct{}{}
		if exclude != nil {
			if _, skip := exclude[pid]; skip {
				return "", 0, false, nil
			}
		}
		p, err := proxySvc.GetByID(ctx, pid)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return "", 0, false, nil
			}
			return "", 0, false, err
		}
		if p == nil || p.Status != model.ProxyStatusEnabled {
			return "", 0, false, nil
		}
		u, err := proxySvc.BuildURL(p)
		if err != nil {
			return "", 0, false, err
		}
		if u == nil {
			return "", 0, false, nil
		}
		return u.String(), pid, true, nil
	}

	tryIDs := func(ids []uint64) (string, uint64, error) {
		for _, pid := range ids {
			if proxyURL, selectedID, ok, err := tryProxy(pid); err != nil {
				return "", 0, err
			} else if ok {
				return proxyURL, selectedID, nil
			}
		}
		return "", 0, nil
	}

	orderedIDs := orderedAccountProxyIDs(ctx, cfg, acc)
	if shouldPreferGrokProxyPool(acc) {
		// Grok 反爬会话（cf_clearance / x-challenge / x-signature / statsig 指纹）和「解 CF 挑战的出口 IP」绑定，
		// 业务请求必须从同一出口发出，否则被判机器人（HTTP 403 code=7）。这里严格对齐 CF 刷新用过的那条出口：
		//   - 解挑战时是直连 → 业务也直连（返回空 URL）；
		//   - 解挑战时用了某条代理 → 业务也走同一条代理。
		// 不再按号池轮换，避免出口 IP 与反爬会话错配。
		if egress, ok := grokCFSolveEgress(); ok {
			if egress == "" {
				return "", 0, nil
			}
			if pid, found := proxyIDForURL(ctx, proxySvc, egress); found {
				return egress, pid, nil
			}
			return egress, 0, nil
		}
		items, err := proxySvc.ListEnabled(ctx)
		if err != nil {
			return "", 0, err
		}
		if proxyURL, selectedID, err := tryIDs(grokProxyPoolIDs(acc, items)); err != nil {
			return "", 0, err
		} else if selectedID != 0 {
			return proxyURL, selectedID, nil
		}
	}

	if proxyURL, selectedID, err := tryIDs(orderedIDs); err != nil {
		return "", 0, err
	} else if selectedID != 0 {
		return proxyURL, selectedID, nil
	}

	if allowPoolFallback {
		items, err := proxySvc.ListEnabled(ctx)
		if err != nil {
			return "", 0, err
		}
		fallbackIDs := make([]uint64, 0, len(items))
		for _, item := range items {
			if item == nil {
				continue
			}
			fallbackIDs = append(fallbackIDs, item.ID)
		}
		if proxyURL, selectedID, err := tryIDs(fallbackIDs); err != nil {
			return "", 0, err
		} else if selectedID != 0 {
			return proxyURL, selectedID, nil
		}
	}
	return "", 0, nil
}

func orderedAccountProxyIDs(ctx context.Context, cfg proxyResolverConfig, acc *model.Account) []uint64 {
	orderedIDs := make([]uint64, 0, 3)
	if acc != nil && acc.ProxyID != nil {
		orderedIDs = append(orderedIDs, *acc.ProxyID)
	}
	// Adobe provider 专用代理：优先级高于 global，避免 Adobe 区域风控触发 HTTP 451。
	// pool_adobe 没有 acc.ProxyID 字段，所以这里负责给 firefly 出图加代理出口。
	if cfg != nil && acc != nil && acc.Provider == model.ProviderADOBE && cfg.AdobeProxyEnabled(ctx) {
		if pid := cfg.AdobeProxyID(ctx); pid > 0 {
			orderedIDs = append(orderedIDs, pid)
		}
	}
	if cfg != nil && cfg.GlobalProxyEnabled(ctx) {
		orderedIDs = append(orderedIDs, cfg.GlobalProxyID(ctx))
	}
	return orderedIDs
}

func shouldPreferGrokProxyPool(acc *model.Account) bool {
	return acc != nil && acc.Provider == model.ProviderGROK
}

// proxyIDForURL 把一条出口 URL 反查回它对应的 proxy 记录 ID（用于日志/重试排除）。
// 找不到匹配（例如该代理已删除或 URL 不在库里）返回 (0,false)，调用方仍可直接用该 URL。
func proxyIDForURL(ctx context.Context, proxySvc *ProxyService, target string) (uint64, bool) {
	target = strings.TrimSpace(target)
	if target == "" || proxySvc == nil {
		return 0, false
	}
	items, err := proxySvc.ListEnabled(ctx)
	if err != nil {
		return 0, false
	}
	for _, p := range items {
		if p == nil || p.Status != model.ProxyStatusEnabled {
			continue
		}
		u, err := proxySvc.BuildURL(p)
		if err != nil || u == nil {
			continue
		}
		if u.String() == target {
			return p.ID, true
		}
	}
	return 0, false
}

func grokProxyPoolIDs(acc *model.Account, items []*model.Proxy) []uint64 {
	ids := make([]uint64, 0, len(items))
	for _, item := range items {
		if item == nil || item.Status != model.ProxyStatusEnabled {
			continue
		}
		switch item.Protocol {
		case model.ProxyProtoHTTP, model.ProxyProtoHTTPS, model.ProxyProtoSOCKS5, model.ProxyProtoSOCKS5H:
			ids = append(ids, item.ID)
		}
	}
	if len(ids) <= 1 {
		return ids
	}
	start := int((atomic.AddUint64(&grokProxyPoolCursor, 1) - 1) % uint64(len(ids)))
	if acc != nil && acc.ID != 0 {
		start = (start + int(acc.ID%uint64(len(ids)))) % len(ids)
	}
	out := make([]uint64, 0, len(ids))
	out = append(out, ids[start:]...)
	out = append(out, ids[:start]...)
	return out
}
