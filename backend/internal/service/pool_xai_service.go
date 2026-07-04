package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/regkit/xairefresh"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
)

// PoolXAIService 官方 xAI API 号池服务。
type PoolXAIService struct {
	repo *repo.PoolXAIRepo
	aes  *crypto.AESGCM
}

// NewPoolXAIService 构造。
func NewPoolXAIService(r *repo.PoolXAIRepo, aes *crypto.AESGCM) *PoolXAIService {
	return &PoolXAIService{repo: r, aes: aes}
}

// xaiBillingSnapshot 存进 remark 列的紧凑额度快照（金额单位：美分）。
type xaiBillingSnapshot struct {
	LimitCents int64  `json:"l"`
	UsedCents  int64  `json:"u"`
	CapCents   int64  `json:"c"`
	PeriodEnd  string `json:"e"`
	At         int64  `json:"t"`
}

// RefreshBilling 查询单个账号额度（cli-chat-proxy.grok.com/v1/billing，用 access_token），
// 把快照写入 remark 列。无需 Management Key。
func (s *PoolXAIService) RefreshBilling(ctx context.Context, id uint64, proxyURL string) (*xairefresh.Billing, error) {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errcode.ResourceMissing
		}
		return nil, errcode.DBError.Wrap(err)
	}
	if len(row.CredentialEnc) == 0 {
		return nil, errors.New("缺少 access_token")
	}
	tok, err := s.aes.Decrypt(row.CredentialEnc)
	if err != nil {
		return nil, errcode.Internal.Wrap(err)
	}
	cli, err := xairefresh.New(proxyURL, 30*time.Second)
	if err != nil {
		return nil, err
	}
	b, err := cli.FetchBilling(ctx, string(tok))
	if err != nil {
		return nil, err
	}
	snap, _ := json.Marshal(xaiBillingSnapshot{
		LimitCents: b.MonthlyLimitCents,
		UsedCents:  b.UsedCents,
		CapCents:   b.OnDemandCapCents,
		PeriodEnd:  b.PeriodEnd,
		At:         time.Now().Unix(),
	})
	if err := s.repo.Update(ctx, id, map[string]any{"remark": string(snap)}); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return b, nil
}

// RefreshBillingAll 并发刷新所有 valid 账号额度。返回 (ok, fail)。
func (s *PoolXAIService) RefreshBillingAll(ctx context.Context, concurrency int, pickProxy func() string) (int, int) {
	items, _, err := s.repo.List(ctx, repo.PoolXAIFilter{Status: model.XAIStatusValid, Page: 1, PageSize: 1000})
	if err != nil || len(items) == 0 {
		return 0, 0
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, fail := 0, 0
	for _, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(id uint64) {
			defer wg.Done()
			defer func() { <-sem }()
			proxy := ""
			if pickProxy != nil {
				proxy = pickProxy()
			}
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 40*time.Second)
			defer cancel()
			_, e := s.RefreshBilling(rctx, id, proxy)
			mu.Lock()
			if e != nil {
				fail++
			} else {
				ok++
			}
			mu.Unlock()
		}(it.ID)
	}
	wg.Wait()
	return ok, fail
}

// List 列表。
func (s *PoolXAIService) List(ctx context.Context, req *dto.XAIPoolListReq) ([]*dto.XAIPoolResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.PoolXAIFilter{
		Status:      strings.TrimSpace(req.Status),
		AccountType: strings.TrimSpace(req.AccountType),
		Keyword:     strings.TrimSpace(req.Keyword),
		Page:        req.Page,
		PageSize:    req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.XAIPoolResp, 0, len(items))
	for _, it := range items {
		out = append(out, xaiToResp(it))
	}
	return out, total, nil
}

// Stats 状态分布。
func (s *PoolXAIService) Stats(ctx context.Context) (*dto.XAIPoolStatsResp, error) {
	m, err := s.repo.Stats(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return &dto.XAIPoolStatsResp{
		Total:    m["total"],
		Valid:    m["valid"],
		Invalid:  m["invalid"],
		Disabled: m["disabled"],
		Cooldown: m["cooldown"],
	}, nil
}

// Create 单条新增。access_token 必填（业务校验的 Bearer）。
func (s *PoolXAIService) Create(ctx context.Context, req *dto.XAIPoolCreateReq) (*model.PoolXAI, error) {
	p, err := s.buildModel(req)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return p, nil
}

func (s *PoolXAIService) buildModel(req *dto.XAIPoolCreateReq) (*model.PoolXAI, error) {
	accessToken := strings.TrimSpace(req.AccessToken)
	if accessToken == "" {
		return nil, errcode.InvalidParam.WithMsg("缺少 access_token")
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	subject := strings.TrimSpace(req.Subject)
	// 缺 email 时尝试从 id_token / access_token 解析；再不行用 hash 占位。
	if email == "" {
		if e, sub := xairefresh.ParseJWTIdentity(strings.TrimSpace(req.IDToken)); e != "" {
			email = strings.ToLower(e)
			if subject == "" {
				subject = sub
			}
		}
	}
	if email == "" {
		h := sha256.Sum256([]byte(accessToken))
		email = "xai-" + hex.EncodeToString(h[:8]) + "@token.local"
	}

	// account_type：优先用显式传入；否则从 access_token 的 tier claim 推断（tierN）。
	acctType := strings.TrimSpace(req.AccountType)
	if acctType == "" {
		if tier := xairefresh.ParseJWTTier(accessToken); tier >= 0 {
			acctType = fmt.Sprintf("tier%d", tier)
		} else {
			acctType = model.XAIAccountTypeUnknown
		}
	}
	p := &model.PoolXAI{
		Email:       email,
		AccountType: acctType,
		Status:      model.XAIStatusValid,
		Source:      model.XAISourceImport,
	}
	if subject != "" {
		p.Subject = &subject
	}
	if v := defaultStr(strings.TrimSpace(req.TokenEndpoint), ""); v != "" {
		p.TokenEndpoint = &v
	}
	base := defaultStr(strings.TrimSpace(req.BaseURL), model.DefaultXAIBaseURL)
	p.BaseURL = &base
	if v := strings.TrimSpace(req.Notes); v != "" {
		p.Notes = &v
	}
	if req.ExpiresAt > 0 {
		t := time.UnixMilli(req.ExpiresAt).UTC()
		p.ExpiresAt = &t
	}

	enc, err := s.aes.Encrypt([]byte(accessToken))
	if err != nil {
		return nil, errcode.Internal.Wrap(err)
	}
	p.CredentialEnc = enc
	if rt := strings.TrimSpace(req.RefreshToken); rt != "" {
		e, err := s.aes.Encrypt([]byte(rt))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.RefreshTokenEnc = e
	}
	if it := strings.TrimSpace(req.IDToken); it != "" {
		e, err := s.aes.Encrypt([]byte(it))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.IDTokenEnc = e
	}
	p.RefreshEnabled = 1
	return p, nil
}

// Update 单条更新。
func (s *PoolXAIService) Update(ctx context.Context, id uint64, req *dto.XAIPoolUpdateReq) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}
	fields := map[string]any{}
	if req.AccountType != nil {
		fields["account_type"] = strings.TrimSpace(*req.AccountType)
	}
	if req.Status != nil {
		fields["status"] = *req.Status
	}
	if req.TokenEndpoint != nil {
		fields["token_endpoint"] = strings.TrimSpace(*req.TokenEndpoint)
	}
	if req.BaseURL != nil {
		fields["base_url"] = strings.TrimSpace(*req.BaseURL)
	}
	if req.Notes != nil {
		fields["notes"] = strings.TrimSpace(*req.Notes)
	}
	if req.BalanceNote != nil {
		// 余额(U) 手填，复用 remark 列存储（xAI 不开放余额 API，只能人工记录）。
		fields["remark"] = strings.TrimSpace(*req.BalanceNote)
	}
	if req.RefreshEnabled != nil {
		if *req.RefreshEnabled {
			fields["refresh_enabled"] = 1
		} else {
			fields["refresh_enabled"] = 0
		}
	}
	if req.ExpiresAt != nil {
		if *req.ExpiresAt == 0 {
			fields["expires_at"] = nil
		} else {
			fields["expires_at"] = time.UnixMilli(*req.ExpiresAt).UTC()
		}
	}
	if req.AccessToken != nil && strings.TrimSpace(*req.AccessToken) != "" {
		enc, err := s.aes.Encrypt([]byte(strings.TrimSpace(*req.AccessToken)))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["credential_enc"] = enc
	}
	if req.RefreshToken != nil && strings.TrimSpace(*req.RefreshToken) != "" {
		enc, err := s.aes.Encrypt([]byte(strings.TrimSpace(*req.RefreshToken)))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["refresh_token_enc"] = enc
	}
	if req.IDToken != nil && strings.TrimSpace(*req.IDToken) != "" {
		enc, err := s.aes.Encrypt([]byte(strings.TrimSpace(*req.IDToken)))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["id_token_enc"] = enc
	}
	if err := s.repo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// Import 文本批量导入。支持：
//
//  1. 整段 JSON Array（cmd/xailogin 导出的多条）
//  2. 每行一个 JSON 对象（JSONL，cmd/xailogin 单条导出）
//  3. 简形 email----access_token----refresh_token[----token_endpoint]
//  4. 裸 access_token（JWT，eyJ 开头），email 从 token 派生
func (s *PoolXAIService) Import(ctx context.Context, req *dto.XAIPoolImportReq) (*dto.XAIPoolImportResult, error) {
	res := &dto.XAIPoolImportResult{}
	batch := make([]*model.PoolXAI, 0, 32)
	seen := map[string]struct{}{}

	trimmed := strings.TrimSpace(req.Text)
	var reqs []dto.XAIPoolCreateReq
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &reqs); err != nil {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("JSON Array 解析失败：%v", err))
			return res, nil
		}
	} else {
		for i, raw := range strings.Split(req.Text, "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			var item dto.XAIPoolCreateReq
			switch {
			case strings.HasPrefix(line, "{"):
				if err := json.Unmarshal([]byte(line), &item); err != nil {
					res.Skipped++
					res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行 JSON 解析失败：%v", i+1, err))
					continue
				}
				normalizeImportAliases(line, &item)
			case strings.HasPrefix(line, "eyJ") && strings.Count(line, ".") == 2:
				item.AccessToken = line
			default:
				parts := splitFlex(line, []string{"----", "|"})
				if len(parts) < 2 {
					res.Skipped++
					res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行字段不足", i+1))
					continue
				}
				item.Email = parts[0]
				item.AccessToken = parts[1]
				if len(parts) >= 3 {
					item.RefreshToken = parts[2]
				}
				if len(parts) >= 4 {
					item.TokenEndpoint = parts[3]
				}
			}
			reqs = append(reqs, item)
		}
	}

	for i := range reqs {
		p, err := s.buildModel(&reqs[i])
		if err != nil {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 条：%v", i+1, err))
			continue
		}
		if _, dup := seen[p.Email]; dup {
			res.Skipped++
			continue
		}
		seen[p.Email] = struct{}{}
		batch = append(batch, p)
	}
	if len(batch) == 0 {
		return res, nil
	}
	affected, err := s.repo.UpsertMany(ctx, batch)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	res.Imported = int(affected)
	return res, nil
}

// normalizeImportAliases 兼容 cmd/xailogin / CLIProxyAPI 导出 JSON 的字段别名：
// access_token / refresh_token / id_token / sub / expired。
func normalizeImportAliases(line string, item *dto.XAIPoolCreateReq) {
	var alias struct {
		Sub     string `json:"sub"`
		Expired string `json:"expired"`
	}
	_ = json.Unmarshal([]byte(line), &alias)
	if item.Subject == "" && alias.Sub != "" {
		item.Subject = alias.Sub
	}
	if item.ExpiresAt == 0 && alias.Expired != "" {
		if t, err := time.Parse(time.RFC3339, alias.Expired); err == nil {
			item.ExpiresAt = t.UnixMilli()
		}
	}
}

// Delete 删除。
func (s *PoolXAIService) Delete(ctx context.Context, id uint64) error {
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// BatchDelete 批量删除。
func (s *PoolXAIService) BatchDelete(ctx context.Context, ids []uint64) (int64, error) {
	n, err := s.repo.SoftDeleteByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// Purge 按条件批量删除。
func (s *PoolXAIService) Purge(ctx context.Context, f repo.PoolXAIPurgeFilter) (int64, error) {
	n, err := s.repo.Purge(ctx, f)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

func xaiToResp(m *model.PoolXAI) *dto.XAIPoolResp {
	r := &dto.XAIPoolResp{
		ID:              m.ID,
		Email:           m.Email,
		HasAccessToken:  len(m.CredentialEnc) > 0,
		HasRefreshToken: len(m.RefreshTokenEnc) > 0,
		AccountType:     m.AccountType,
		Status:          m.Status,
		Source:          m.Source,
		RefreshEnabled:  m.RefreshEnabled == 1,
		FailureCount:    m.FailureCount,
		SuccessCount:    m.SuccessCount,
		CreatedAt:       m.CreatedAt.UnixMilli(),
		UpdatedAt:       m.UpdatedAt.UnixMilli(),
	}
	if m.Subject != nil {
		r.Subject = *m.Subject
	}
	if m.TokenEndpoint != nil {
		r.TokenEndpoint = *m.TokenEndpoint
	}
	if m.BaseURL != nil {
		r.BaseURL = *m.BaseURL
	}
	if m.ExpiresAt != nil {
		r.ExpiresAt = m.ExpiresAt.UnixMilli()
	}
	if m.LastRefreshAt != nil {
		r.LastRefreshAt = m.LastRefreshAt.UnixMilli()
	}
	if m.LastRefreshResult != nil {
		r.LastRefreshResult = *m.LastRefreshResult
	}
	if m.LastUsedAt != nil {
		r.LastUsedAt = m.LastUsedAt.UnixMilli()
	}
	if m.ErrorMessage != nil {
		r.ErrorMessage = *m.ErrorMessage
	}
	if m.Notes != nil {
		r.Notes = *m.Notes
	}
	if m.Remark != nil {
		rk := strings.TrimSpace(*m.Remark)
		if strings.HasPrefix(rk, "{") {
			// 自动额度快照（JSON）→ 解析成结构化 Billing。
			var snap xaiBillingSnapshot
			if json.Unmarshal([]byte(rk), &snap) == nil && snap.LimitCents > 0 {
				limit := float64(snap.LimitCents) / 100.0
				used := float64(snap.UsedCents) / 100.0
				pct := 0
				if snap.LimitCents > 0 {
					pct = int(snap.UsedCents * 100 / snap.LimitCents)
				}
				r.Billing = &dto.XAIBillingResp{
					LimitUSD:     limit,
					UsedUSD:      used,
					RemainingUSD: limit - used,
					CapUSD:       float64(snap.CapCents) / 100.0,
					UsedPct:      pct,
					PeriodEnd:    snap.PeriodEnd,
					UpdatedAt:    snap.At * 1000,
				}
			} else {
				r.BalanceNote = rk
			}
		} else if rk != "" {
			// 手填的余额备注（非 JSON）。
			r.BalanceNote = rk
		}
	}
	return r
}
