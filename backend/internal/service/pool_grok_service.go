package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
)

// isGrokJWT 判断输入是否像一个 X.com 的 JWT (eyJ... + 两个点)。
//
// X.com / SuperGrok 的 sso/auth_token 是 HS256 JWT，格式 header.payload.signature，
// 三段都是 base64url。最简易的识别方法 = 以 eyJ 开头 + 恰好两个点。
func isGrokJWT(s string) bool {
	if !strings.HasPrefix(s, "eyJ") {
		return false
	}
	// 必须是 3 段，否则不是标准 JWT
	if strings.Count(s, ".") != 2 {
		return false
	}
	// 不能有空白（防止误匹配 email----password----eyJ 之类）
	if strings.ContainsAny(s, " \t") {
		return false
	}
	return true
}

// grokJWTPlaceholderEmail 给"裸 token"导入生成稳定的占位 email。
//
// 解析顺序：
//
//  1. 解开 JWT payload，取 session_id（X.com 的 token 携带 UUID 形式 session_id）
//     → email = grok-<session_id>@token.local
//  2. payload 解析失败 / 没 session_id：
//     → email = grok-<sha256(token)[0:8]>@token.local（保证不同 token 不撞）
//
// 这样多次导入同一 token 会复用同一行（email 是 upsert key），不会重复入库。
func grokJWTPlaceholderEmail(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) >= 2 {
		payload := parts[1]
		if pad := len(payload) % 4; pad > 0 {
			payload += strings.Repeat("=", 4-pad)
		}
		if raw, err := base64.URLEncoding.DecodeString(payload); err == nil {
			var p struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(raw, &p) == nil && p.SessionID != "" {
				return "grok-" + strings.ToLower(strings.TrimSpace(p.SessionID)) + "@token.local"
			}
		}
	}
	h := sha256.Sum256([]byte(jwt))
	return "grok-" + hex.EncodeToString(h[:8]) + "@token.local"
}

// PoolGrokService GROK 号池服务。
type PoolGrokService struct {
	repo *repo.PoolGrokRepo
	aes  *crypto.AESGCM

	// batchJobMu 保护 batchJob slot。同一时刻只允许一个 batch refresh 任务在跑，
	// 这避免万级账号的批量扫描互相挤占 outbound HTTP / 代理池 / DB 连接。
	batchJobMu sync.RWMutex
	batchJob   *grokBatchJob
}

// NewPoolGrokService 构造。
func NewPoolGrokService(r *repo.PoolGrokRepo, aes *crypto.AESGCM) *PoolGrokService {
	return &PoolGrokService{repo: r, aes: aes}
}

// List 列表。
func (s *PoolGrokService) List(ctx context.Context, req *dto.GrokPoolListReq) ([]*dto.GrokPoolResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.PoolGrokFilter{
		TrialStatus:        req.TrialStatus,
		AccountType:        strings.TrimSpace(req.AccountType),
		SubscriptionStatus: strings.TrimSpace(req.SubscriptionStatus),
		Keyword:            strings.TrimSpace(req.Keyword),
		Page:               req.Page,
		PageSize:           req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.GrokPoolResp, 0, len(items))
	for _, it := range items {
		out = append(out, grokToResp(it))
	}
	return out, total, nil
}

// Stats 试用状态分布。
func (s *PoolGrokService) Stats(ctx context.Context) (*dto.GrokPoolStatsResp, error) {
	m, err := s.repo.Stats(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return &dto.GrokPoolStatsResp{
		Total:      m["total"],
		Pending:    m["pending"],
		Activating: m["activating"],
		Active:     m["active"],
		Failed:     m["failed"],
		Expired:    m["expired"],
	}, nil
}

// Create 单条新增。
func (s *PoolGrokService) Create(ctx context.Context, req *dto.GrokPoolCreateReq) (*model.PoolGrok, error) {
	p := &model.PoolGrok{
		Email:       strings.ToLower(strings.TrimSpace(req.Email)),
		TrialStatus: defaultStr(req.TrialStatus, model.GrokTrialPending),
		AccountType: strings.TrimSpace(req.AccountType),
		Credits:     req.Credits,
	}
	if v := strings.TrimSpace(req.GivenName); v != "" {
		p.GivenName = &v
	}
	if v := strings.TrimSpace(req.FamilyName); v != "" {
		p.FamilyName = &v
	}
	if v := strings.TrimSpace(req.UserAgent); v != "" {
		p.UserAgent = &v
	}
	if v := strings.TrimSpace(req.PaymentURL); v != "" {
		p.PaymentURL = &v
	}
	if v := strings.TrimSpace(req.Notes); v != "" {
		p.Notes = &v
	}
	if req.TrialExpiresAt > 0 {
		t := time.UnixMilli(req.TrialExpiresAt).UTC()
		p.TrialExpiresAt = &t
	}
	if req.Password != "" {
		enc, err := s.aes.Encrypt([]byte(req.Password))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.PasswordEnc = enc
	}
	if req.SSO != "" {
		enc, err := s.aes.Encrypt([]byte(req.SSO))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.SSOEnc = enc
	}
	if req.SSORW != "" {
		enc, err := s.aes.Encrypt([]byte(req.SSORW))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.SSORWEnc = enc
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return p, nil
}

// Update 单条更新。
func (s *PoolGrokService) Update(ctx context.Context, id uint64, req *dto.GrokPoolUpdateReq) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}
	fields := map[string]any{}
	if req.GivenName != nil {
		fields["given_name"] = strings.TrimSpace(*req.GivenName)
	}
	if req.FamilyName != nil {
		fields["family_name"] = strings.TrimSpace(*req.FamilyName)
	}
	if req.UserAgent != nil {
		fields["user_agent"] = strings.TrimSpace(*req.UserAgent)
	}
	if req.TrialStatus != nil {
		fields["trial_status"] = *req.TrialStatus
	}
	if req.AccountType != nil {
		fields["account_type"] = strings.TrimSpace(*req.AccountType)
	}
	if req.Credits != nil {
		fields["credits"] = *req.Credits
	}
	if req.PaymentURL != nil {
		fields["payment_url"] = strings.TrimSpace(*req.PaymentURL)
	}
	if req.Notes != nil {
		fields["notes"] = strings.TrimSpace(*req.Notes)
	}
	if req.TrialExpiresAt != nil {
		if *req.TrialExpiresAt == 0 {
			fields["trial_expires_at"] = nil
		} else {
			fields["trial_expires_at"] = time.UnixMilli(*req.TrialExpiresAt).UTC()
		}
	}
	if req.Password != nil && *req.Password != "" {
		enc, err := s.aes.Encrypt([]byte(*req.Password))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["password_enc"] = enc
	}
	if req.SSO != nil && *req.SSO != "" {
		enc, err := s.aes.Encrypt([]byte(*req.SSO))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["sso_enc"] = enc
	}
	if req.SSORW != nil && *req.SSORW != "" {
		enc, err := s.aes.Encrypt([]byte(*req.SSORW))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["sso_rw_enc"] = enc
	}
	if err := s.repo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// Import 文本批量导入。一行一条，支持 5 种格式（自动识别）：
//
//  1. 整段 JSON Array（粘贴导出文件即可）：以 `[` 开头一次解析全部
//  2. 每行一个 JSON 对象（JSONL，以 `{` 开头）
//  3. 简形 `email----password----sso[----sso_rw]`（`|` 也作分隔符）
//  4. 裸 JWT token（以 `eyJ` 开头 + 两个点）：
//     整行当作 sso，email 自动从 JWT payload 的 session_id 派生（占位）
//  5. 被忽略：空行 + `#` 开头的注释行
func (s *PoolGrokService) Import(ctx context.Context, req *dto.GrokPoolImportReq) (*dto.GrokPoolImportResult, error) {
	res := &dto.GrokPoolImportResult{}
	batch := make([]*model.PoolGrok, 0, 64)
	seen := map[string]struct{}{}

	// 整段 JSON Array 优先识别：trim 后以 [ 开头时把整个文本当作数组。
	trimmed := strings.TrimSpace(req.Text)
	type lineItem struct {
		ord  int
		item dto.GrokPoolCreateReq
		err  error
	}
	var lineItems []lineItem
	if strings.HasPrefix(trimmed, "[") {
		var arr []dto.GrokPoolCreateReq
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("JSON Array 解析失败：%v", err))
			return res, nil
		}
		for i, it := range arr {
			lineItems = append(lineItems, lineItem{ord: i + 1, item: it})
		}
	} else {
		for i, raw := range strings.Split(req.Text, "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			var item dto.GrokPoolCreateReq
			switch {
			case strings.HasPrefix(line, "{"):
				if err := json.Unmarshal([]byte(line), &item); err != nil {
					lineItems = append(lineItems, lineItem{ord: i + 1, err: fmt.Errorf("JSON 解析失败：%w", err)})
					continue
				}
			case isGrokJWT(line):
				// 裸 JWT：当作 sso，email 自动派生
				item.SSO = line
				item.Email = grokJWTPlaceholderEmail(line)
			default:
				parts := splitFlex(line, []string{"----", "|"})
				if len(parts) < 2 {
					lineItems = append(lineItems, lineItem{ord: i + 1, err: fmt.Errorf("字段不足")})
					continue
				}
				item.Email = parts[0]
				item.Password = parts[1]
				if len(parts) >= 3 {
					item.SSO = parts[2]
				}
				if len(parts) >= 4 {
					item.SSORW = parts[3]
				}
			}
			lineItems = append(lineItems, lineItem{ord: i + 1, item: item})
		}
	}

	for _, li := range lineItems {
		if li.err != nil {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行 %v", li.ord, li.err))
			continue
		}
		item := li.item
		email := strings.ToLower(strings.TrimSpace(item.Email))
		if email == "" {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行缺少 email / token", li.ord))
			continue
		}
		if _, dup := seen[email]; dup {
			res.Skipped++
			continue
		}
		seen[email] = struct{}{}

		p := &model.PoolGrok{
			Email:       email,
			TrialStatus: defaultStr(item.TrialStatus, model.GrokTrialPending),
			AccountType: strings.TrimSpace(item.AccountType),
			Credits:     item.Credits,
		}
		if v := strings.TrimSpace(item.GivenName); v != "" {
			p.GivenName = &v
		}
		if v := strings.TrimSpace(item.FamilyName); v != "" {
			p.FamilyName = &v
		}
		if v := strings.TrimSpace(item.UserAgent); v != "" {
			p.UserAgent = &v
		}
		if v := strings.TrimSpace(item.PaymentURL); v != "" {
			p.PaymentURL = &v
		}
		if v := strings.TrimSpace(item.Notes); v != "" {
			p.Notes = &v
		}
		if item.TrialExpiresAt > 0 {
			t := time.UnixMilli(item.TrialExpiresAt).UTC()
			p.TrialExpiresAt = &t
		}
		if item.Password != "" {
			enc, err := s.aes.Encrypt([]byte(item.Password))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.PasswordEnc = enc
		}
		if item.SSO != "" {
			enc, err := s.aes.Encrypt([]byte(item.SSO))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.SSOEnc = enc
		}
		if item.SSORW != "" {
			enc, err := s.aes.Encrypt([]byte(item.SSORW))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.SSORWEnc = enc
		}
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

// Delete 软删。
func (s *PoolGrokService) Delete(ctx context.Context, id uint64) error {
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// BatchDelete 批量软删。
func (s *PoolGrokService) BatchDelete(ctx context.Context, ids []uint64) (int64, error) {
	n, err := s.repo.SoftDeleteByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// ExpireOverdue 把过期的试用置为 expired（可被 cron / 手动触发）。
func (s *PoolGrokService) ExpireOverdue(ctx context.Context) (int64, error) {
	n, err := s.repo.ExpireOverdueTrials(ctx)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// === helpers ===

func grokToResp(m *model.PoolGrok) *dto.GrokPoolResp {
	r := &dto.GrokPoolResp{
		ID:           m.ID,
		Email:        m.Email,
		HasPassword:  len(m.PasswordEnc) > 0,
		HasSSO:       len(m.SSOEnc) > 0,
		HasSSORW:     len(m.SSORWEnc) > 0,
		TrialStatus:  m.TrialStatus,
		AccountType:  m.AccountType,
		Credits:      m.Credits,
		QuotaTotal:   m.QuotaTotal,
		FailureCount: m.FailureCount,
		RegisteredAt: m.RegisteredAt.UnixMilli(),
		CreatedAt:    m.CreatedAt.UnixMilli(),
		UpdatedAt:    m.UpdatedAt.UnixMilli(),
	}
	if m.LastCheckedAt != nil {
		r.LastCheckedAt = m.LastCheckedAt.UnixMilli()
	}
	if m.GivenName != nil {
		r.GivenName = *m.GivenName
	}
	if m.FamilyName != nil {
		r.FamilyName = *m.FamilyName
	}
	if m.UserAgent != nil {
		r.UserAgent = *m.UserAgent
	}
	if m.TrialStartedAt != nil {
		r.TrialStartedAt = m.TrialStartedAt.UnixMilli()
	}
	if m.TrialExpiresAt != nil {
		r.TrialExpiresAt = m.TrialExpiresAt.UnixMilli()
	}
	if m.ExpiresAt != nil {
		r.ExpiresAt = m.ExpiresAt.UnixMilli()
	}
	r.CancelAtPeriodEnd = m.CancelAtPeriodEnd
	r.BillingInterval = m.BillingInterval
	r.SubscriptionStatus = m.SubscriptionStatus
	r.ProductID = m.ProductID
	if m.TrialError != nil {
		r.TrialError = *m.TrialError
	}
	if m.PaymentURL != nil {
		r.PaymentURL = *m.PaymentURL
	}
	if m.Notes != nil {
		r.Notes = *m.Notes
	}
	return r
}
