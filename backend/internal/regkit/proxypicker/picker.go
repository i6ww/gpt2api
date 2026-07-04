// Package proxypicker 注册流程使用的代理选择器。
//
// 选择优先级（由高到低）：
//  1. dispatcher 入参中明确指定的 proxy_id
//  2. 全局默认代理（system_config: proxy.global_id）
//  3. 不使用代理（直连）
//
// 如需"随机代理"行为，可在调用方传 explicitID = 一个随机选出的可用代理 ID。
package proxypicker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
)

// Resolved 解析出的代理 URL 与原始 ID。
type Resolved struct {
	ID  uint64 // 0 表示未使用
	URL string // 形如 http://user:pass@host:port，AES 解密后的完整代理 URL
}

// Picker 代理选择器。
type Picker struct {
	proxySvc  *service.ProxyService
	proxyRepo *repo.ProxyRepo
	sysCfg    *service.SystemConfigService
}

// NewPicker 构造。
func NewPicker(
	proxySvc *service.ProxyService,
	proxyRepo *repo.ProxyRepo,
	sysCfg *service.SystemConfigService,
) *Picker {
	return &Picker{proxySvc: proxySvc, proxyRepo: proxyRepo, sysCfg: sysCfg}
}

// Pick 解析出可用代理。
//
//   - explicitID > 0：优先使用指定代理（必须 enabled）
//   - explicitID == 0：从启用中的代理池里随机挑一个；池为空则退回到
//     系统配置中的全局代理；两者都没有时直连。
//
// 选择"随机"是为了让多个并行任务命中不同的出口 IP，避免 Adobe Arkose /
// FunCaptcha 因为同一个 IP 被 2Captcha 大量解题而被打码平台/Arkose 风控。
func (p *Picker) Pick(ctx context.Context, explicitID uint64) (*Resolved, error) {
	if explicitID > 0 {
		row, err := p.proxyRepo.GetByID(ctx, explicitID)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return nil, fmt.Errorf("指定代理 %d 不存在", explicitID)
			}
			return nil, err
		}
		if row.Status != 1 {
			return nil, fmt.Errorf("指定代理 %d 已禁用", explicitID)
		}
		u, err := p.proxySvc.BuildURL(row)
		if err != nil {
			return nil, fmt.Errorf("解码代理 %d 失败：%w", explicitID, err)
		}
		s := ""
		if u != nil {
			s = u.String()
		}
		return &Resolved{ID: row.ID, URL: s}, nil
	}

	// 优先从启用代理池里随机选一个，让并行任务有不同出口
	if rows, err := p.proxySvc.ListEnabled(ctx); err == nil && len(rows) > 0 {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(rows))))
		row := rows[idx.Int64()]
		if u, e := p.proxySvc.BuildURL(row); e == nil {
			s := ""
			if u != nil {
				s = u.String()
			}
			return &Resolved{ID: row.ID, URL: s}, nil
		}
	}

	// 池为空或读取失败，退回到全局默认代理（向后兼容）
	if !p.sysCfg.GlobalProxyEnabled(ctx) {
		return &Resolved{}, nil
	}
	gid := p.sysCfg.GlobalProxyID(ctx)
	if gid == 0 {
		return &Resolved{}, nil
	}
	row, err := p.proxyRepo.GetByID(ctx, gid)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return &Resolved{}, nil
		}
		return nil, err
	}
	if row.Status != 1 {
		return &Resolved{}, nil
	}
	u, err := p.proxySvc.BuildURL(row)
	if err != nil {
		return nil, err
	}
	s := ""
	if u != nil {
		s = u.String()
	}
	return &Resolved{ID: row.ID, URL: s}, nil
}

// PickRandom 从启用中的代理里随机挑一个；池为空时返回零值。
func (p *Picker) PickRandom(ctx context.Context) (*Resolved, error) {
	rows, err := p.proxySvc.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &Resolved{}, nil
	}
	idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(rows))))
	row := rows[idx.Int64()]
	u, err := p.proxySvc.BuildURL(row)
	if err != nil {
		return nil, err
	}
	s := ""
	if u != nil {
		s = u.String()
	}
	return &Resolved{ID: row.ID, URL: s}, nil
}

// PickExcluding 从启用中的代理里挑一个，排除给定 ID 列表（用于 CF 403 重试时换代理）。
//
// 用例：第一次随机挑到的代理被 OpenAI / grok.com 的 Cloudflare 直接 403，
// 第二次重试时希望换一个出口 IP；excluded 里塞上次失败的 ID。
//
// 候选都被排除时返回零值（直连），由调用方决定是否要降级跑。
func (p *Picker) PickExcluding(ctx context.Context, excluded []uint64) (*Resolved, error) {
	rows, err := p.proxySvc.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &Resolved{}, nil
	}
	skip := make(map[uint64]bool, len(excluded))
	for _, id := range excluded {
		skip[id] = true
	}
	candidates := rows[:0:0]
	for _, r := range rows {
		if !skip[r.ID] {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return &Resolved{}, nil
	}
	idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
	row := candidates[idx.Int64()]
	u, err := p.proxySvc.BuildURL(row)
	if err != nil {
		return nil, err
	}
	s := ""
	if u != nil {
		s = u.String()
	}
	return &Resolved{ID: row.ID, URL: s}, nil
}
