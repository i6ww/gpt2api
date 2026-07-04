package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider/flowmusic"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
)

// PoolGoogleService FlowMusic（歌曲）Google 号池服务。镜像 PoolAdobeService。
type PoolGoogleService struct {
	repo   *repo.PoolGoogleRepo
	aes    *crypto.AESGCM
	client *flowmusic.Client
}

// NewPoolGoogleService 构造。client 可空（不可空才能续期/查积分）。
func NewPoolGoogleService(r *repo.PoolGoogleRepo, aes *crypto.AESGCM, client *flowmusic.Client) *PoolGoogleService {
	return &PoolGoogleService{repo: r, aes: aes, client: client}
}

// List 列表。
func (s *PoolGoogleService) List(ctx context.Context, req *dto.GooglePoolListReq) ([]*dto.GooglePoolResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.PoolGoogleFilter{
		Status:   req.Status,
		Source:   req.Source,
		Keyword:  strings.TrimSpace(req.Keyword),
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.GooglePoolResp, 0, len(items))
	for _, it := range items {
		out = append(out, googleToResp(it))
	}
	return out, total, nil
}

// Stats 状态分布。
func (s *PoolGoogleService) Stats(ctx context.Context) (*dto.GooglePoolStatsResp, error) {
	m, err := s.repo.Stats(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return &dto.GooglePoolStatsResp{
		Total:    m["total"],
		Valid:    m["valid"],
		Invalid:  m["invalid"],
		Disabled: m["disabled"],
		Cooldown: m["cooldown"],
	}, nil
}

// Create 单条新增。
func (s *PoolGoogleService) Create(ctx context.Context, req *dto.GooglePoolCreateReq) (*model.PoolGoogle, error) {
	item := googleImportItem{
		Email:                req.Email,
		Name:                 req.Name,
		DisplayName:          req.DisplayName,
		Cookies:              req.Cookies,
		AccessToken:          req.AccessToken,
		RefreshToken:         req.RefreshToken,
		ProviderToken:        req.ProviderToken,
		ProviderRefreshToken: req.ProviderRefreshToken,
		Status:               req.Status,
		Credits:              req.Credits,
		ExpiresAt:            req.ExpiresAt,
		Notes:                req.Notes,
	}
	p, err := s.buildPoolRow(item, defaultStr(req.Source, model.GoogleSourceImport))
	if err != nil {
		return nil, errcode.InvalidParam.WithMsg(err.Error())
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return p, nil
}

// Update 单条更新。凭证字段（cookies / access_token / refresh_token）任一非空时重建 bundle。
func (s *PoolGoogleService) Update(ctx context.Context, id uint64, req *dto.GooglePoolUpdateReq) error {
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}
	fields := map[string]any{}
	if req.DisplayName != nil {
		fields["display_name"] = strings.TrimSpace(*req.DisplayName)
	}
	if req.Status != nil {
		fields["status"] = *req.Status
	}
	if req.Credits != nil {
		fields["credits"] = *req.Credits
	}
	if req.RefreshEnabled != nil {
		fields["refresh_enabled"] = *req.RefreshEnabled
	}
	if req.Notes != nil {
		fields["notes"] = strings.TrimSpace(*req.Notes)
	}
	if req.ExpiresAt != nil {
		if *req.ExpiresAt == 0 {
			fields["expires_at"] = nil
		} else {
			fields["expires_at"] = time.UnixMilli(*req.ExpiresAt).UTC()
		}
	}
	// 凭证变更：在现有 bundle 基础上覆盖
	if (req.Cookies != nil && *req.Cookies != "") ||
		(req.AccessToken != nil && *req.AccessToken != "") ||
		(req.RefreshToken != nil && *req.RefreshToken != "") {
		creds := s.decryptCreds(row)
		if req.Cookies != nil && *req.Cookies != "" {
			if c, cerr := credsFromCookieInput(*req.Cookies); cerr == nil {
				mergeCreds(&creds, c)
			} else {
				creds.Cookies = *req.Cookies
			}
		}
		if req.AccessToken != nil && *req.AccessToken != "" {
			creds.AccessToken = strings.TrimSpace(*req.AccessToken)
		}
		if req.RefreshToken != nil && *req.RefreshToken != "" {
			creds.RefreshToken = strings.TrimSpace(*req.RefreshToken)
		}
		enc, eerr := s.aes.Encrypt([]byte(flowmusic.EncodeCredentialBundle(creds)))
		if eerr != nil {
			return errcode.Internal.Wrap(eerr)
		}
		fields["credential_enc"] = enc
	}
	if err := s.repo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// Import 批量导入。支持：
//   - cookie JSON 数组（单账号，浏览器导出，含 sb-*-auth-token.N）
//   - JSON 数组，每个元素是 {email/cookies/access_token/...} 对象（多账号）
//   - 每行一个 JSON 对象（JSONL，多账号）
func (s *PoolGoogleService) Import(ctx context.Context, req *dto.GooglePoolImportReq) (*dto.GooglePoolImportResult, error) {
	res := &dto.GooglePoolImportResult{}
	source := defaultStr(req.Source, model.GoogleSourceImport)
	items, parseErrs := parseGoogleImportText(req.Text)
	if len(parseErrs) > 0 {
		res.Skipped += len(parseErrs)
		res.Errors = append(res.Errors, parseErrs...)
	}
	batch := make([]*model.PoolGoogle, 0, len(items))
	seen := map[string]struct{}{}
	for i, item := range items {
		p, err := s.buildPoolRow(item, source)
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

// Delete 软删。
func (s *PoolGoogleService) Delete(ctx context.Context, id uint64) error {
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// BatchDelete 批量软删。
func (s *PoolGoogleService) BatchDelete(ctx context.Context, ids []uint64) (int64, error) {
	n, err := s.repo.SoftDeleteByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// === 续期 ===

// GoogleRefreshOptions 单次刷新可覆盖项。
type GoogleRefreshOptions struct {
	OnlyCredits  bool
	Caller       string
	FailureLimit int
}

// RefreshOne 用账号 ID 触发一次刷新（Supabase 续期 + 查积分），回写库。
func (s *PoolGoogleService) RefreshOne(ctx context.Context, id uint64, opt GoogleRefreshOptions) (*model.PoolGoogle, error) {
	if s.client == nil {
		return nil, errors.New("flowmusic client 未配置，无法续期")
	}
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errors.New("账号不存在")
		}
		return nil, fmt.Errorf("获取账号失败：%w", err)
	}
	creds := s.decryptCreds(row)
	if strings.TrimSpace(creds.AccessToken) == "" && strings.TrimSpace(creds.RefreshToken) == "" && strings.TrimSpace(creds.Cookies) == "" {
		return nil, errors.New("账号缺少可用凭证")
	}

	now := time.Now().UTC()
	updates := map[string]any{"last_checked_at": now}

	if !opt.OnlyCredits {
		refreshed, rerr := s.refreshCreds(ctx, creds)
		if rerr != nil {
			return s.markGoogleFailure(ctx, row, rerr.Error(), opt.FailureLimit)
		}
		creds = refreshed
		updates["last_refresh_at"] = now
		if v := strings.TrimSpace(creds.LastRefreshResult); v != "" {
			updates["last_refresh_result"] = v
		}
		enc, eerr := s.aes.Encrypt([]byte(flowmusic.EncodeCredentialBundle(creds)))
		if eerr != nil {
			return nil, fmt.Errorf("加密失败：%w", eerr)
		}
		updates["credential_enc"] = enc
		if creds.ExpiresAt != nil {
			updates["expires_at"] = *creds.ExpiresAt
			row.ExpiresAt = creds.ExpiresAt
		}
		if creds.Email != "" {
			updates["email"] = strings.ToLower(creds.Email)
		}
		if creds.Name != "" {
			updates["display_name"] = creds.Name
		}
	}

	// 查积分（成功即可佐证号可用）
	if info, cerr := s.client.GetCredits(ctx, creds); cerr == nil {
		updates["credits"] = info.CreditsRemaining
		updates["tokens_remaining"] = info.TokensRemaining
		if info.SubscriptionTier != "" {
			updates["subscription_tier"] = info.SubscriptionTier
		}
		row.Credits = info.CreditsRemaining
	} else if opt.OnlyCredits {
		return s.markGoogleFailure(ctx, row, cerr.Error(), opt.FailureLimit)
	}

	updates["status"] = model.GoogleStatusValid
	updates["failure_count"] = 0
	updates["error_message"] = ""
	updates["cooldown_until"] = nil
	if err := s.repo.Update(ctx, id, updates); err != nil {
		return nil, fmt.Errorf("写库失败：%w", err)
	}
	return row, nil
}

// refreshCreds 选择续期路径：有 refresh_token+anon key 走 Supabase；否则走 cookie 兜底。
func (s *PoolGoogleService) refreshCreds(ctx context.Context, creds flowmusic.Credentials) (flowmusic.Credentials, error) {
	if strings.TrimSpace(creds.RefreshToken) != "" {
		if refreshed, err := s.client.RefreshSupabase(ctx, creds); err == nil {
			return refreshed, nil
		} else if strings.TrimSpace(creds.Cookies) == "" {
			return creds, err
		}
	}
	return s.client.RefreshFromCookies(ctx, creds)
}

func (s *PoolGoogleService) markGoogleFailure(ctx context.Context, row *model.PoolGoogle, msg string, limit int) (*model.PoolGoogle, error) {
	if limit <= 0 {
		limit = 5
	}
	if len(msg) > 480 {
		msg = msg[:480] + "…"
	}
	failCount := row.FailureCount + 1
	now := time.Now().UTC()
	updates := map[string]any{
		"failure_count":   failCount,
		"error_message":   msg,
		"last_checked_at": now,
	}
	if failCount >= limit {
		updates["status"] = model.GoogleStatusInvalid
	} else {
		updates["status"] = model.GoogleStatusCooldown
		updates["cooldown_until"] = now.Add(10 * time.Minute)
	}
	_ = s.repo.Update(ctx, row.ID, updates)
	return row, fmt.Errorf("refresh 失败：%s", msg)
}

// RefreshExpiring 后台扫描：刷新即将过期的账号。
func (s *PoolGoogleService) RefreshExpiring(ctx context.Context, within time.Duration, max int) (ok, fail int) {
	rows, err := s.repo.ListExpiringSoon(ctx, within, 1000)
	if err != nil || len(rows) == 0 {
		return
	}
	for _, r := range rows {
		if _, e := s.RefreshOne(ctx, r.ID, GoogleRefreshOptions{Caller: "scheduler"}); e != nil {
			fail++
		} else {
			ok++
		}
	}
	return
}

// RefreshByScope 后台手动批量刷新。
func (s *PoolGoogleService) RefreshByScope(ctx context.Context, scope repo.PoolGoogleRefreshScope, onlyCredits bool) (ok, fail, total int) {
	rows, err := s.repo.ListForRefresh(ctx, scope, 500)
	if err != nil || len(rows) == 0 {
		return
	}
	total = len(rows)
	for _, r := range rows {
		if _, e := s.RefreshOne(ctx, r.ID, GoogleRefreshOptions{OnlyCredits: onlyCredits, Caller: "manual"}); e != nil {
			fail++
		} else {
			ok++
		}
	}
	return
}

// === helpers ===

type googleImportItem struct {
	Email                string  `json:"email"`
	Name                 string  `json:"name"`
	DisplayName          string  `json:"display_name"`
	Cookies              string  `json:"cookies"`
	AccessToken          string  `json:"access_token"`
	RefreshToken         string  `json:"refresh_token"`
	ProviderToken        string  `json:"provider_token"`
	ProviderRefreshToken string  `json:"provider_refresh_token"`
	Status               string  `json:"status"`
	Credits              float64 `json:"credits"`
	ExpiresAt            int64   `json:"expires_at"`
	Notes                string  `json:"notes"`
}

// buildPoolRow 把一条导入项装配成 PoolGoogle（含凭证 bundle 加密）。
func (s *PoolGoogleService) buildPoolRow(item googleImportItem, source string) (*model.PoolGoogle, error) {
	creds := flowmusic.Credentials{
		AccessToken:          strings.TrimSpace(item.AccessToken),
		RefreshToken:         strings.TrimSpace(item.RefreshToken),
		ProviderToken:        strings.TrimSpace(item.ProviderToken),
		ProviderRefreshToken: strings.TrimSpace(item.ProviderRefreshToken),
		Email:                strings.TrimSpace(item.Email),
		Name:                 strings.TrimSpace(item.DisplayName),
	}
	if c := strings.TrimSpace(item.Cookies); c != "" {
		if parsed, err := credsFromCookieInput(c); err == nil {
			mergeCreds(&creds, parsed)
		} else {
			creds.Cookies = c
		}
	}
	if creds.AccessToken == "" && creds.RefreshToken == "" && creds.Cookies == "" {
		return nil, errors.New("缺少凭证（cookies / access_token / refresh_token）")
	}

	email := strings.ToLower(firstNonEmpty(strings.TrimSpace(item.Email), strings.TrimSpace(item.Name), strings.TrimSpace(creds.Email)))
	if email == "" {
		email = placeholderGoogleEmail(creds)
	}

	bundle := flowmusic.EncodeCredentialBundle(creds)
	enc, err := s.aes.Encrypt([]byte(bundle))
	if err != nil {
		return nil, err
	}
	p := &model.PoolGoogle{
		Email:         email,
		CredentialEnc: enc,
		ProtocolMode:  "refresh_token",
		Source:        source,
		Status:        defaultStr(item.Status, model.GoogleStatusValid),
		Credits:       item.Credits,
	}
	dn := firstNonEmpty(strings.TrimSpace(item.DisplayName), strings.TrimSpace(creds.Name))
	if dn != "" {
		p.DisplayName = &dn
	}
	if v := strings.TrimSpace(item.Notes); v != "" {
		p.Notes = &v
	}
	switch {
	case item.ExpiresAt > 0:
		t := time.UnixMilli(item.ExpiresAt).UTC()
		p.ExpiresAt = &t
	case creds.ExpiresAt != nil:
		p.ExpiresAt = creds.ExpiresAt
	}
	return p, nil
}

func (s *PoolGoogleService) decryptCreds(row *model.PoolGoogle) flowmusic.Credentials {
	if row == nil || len(row.CredentialEnc) == 0 || s.aes == nil {
		return flowmusic.Credentials{}
	}
	plain, err := s.aes.Decrypt(row.CredentialEnc)
	if err != nil {
		return flowmusic.Credentials{}
	}
	return flowmusic.ParseCredentialBundle(string(plain))
}

// credsFromCookieInput 接收 cookie 输入（JSON 数组字符串 或 cookie header 串）。
func credsFromCookieInput(raw string) (flowmusic.Credentials, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") {
		return flowmusic.CredentialsFromCookieExport([]byte(raw))
	}
	// header 串：直接当 Cookie 头
	return flowmusic.Credentials{Cookies: raw}, nil
}

func mergeCreds(dst *flowmusic.Credentials, src flowmusic.Credentials) {
	if src.AccessToken != "" {
		dst.AccessToken = src.AccessToken
	}
	if src.RefreshToken != "" {
		dst.RefreshToken = src.RefreshToken
	}
	if src.ProviderToken != "" {
		dst.ProviderToken = src.ProviderToken
	}
	if src.ProviderRefreshToken != "" {
		dst.ProviderRefreshToken = src.ProviderRefreshToken
	}
	if src.Cookies != "" {
		dst.Cookies = src.Cookies
	}
	if src.Email != "" && dst.Email == "" {
		dst.Email = src.Email
	}
	if src.Name != "" && dst.Name == "" {
		dst.Name = src.Name
	}
	if src.ExpiresAt != nil {
		dst.ExpiresAt = src.ExpiresAt
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func placeholderGoogleEmail(creds flowmusic.Credentials) string {
	seed := firstNonEmpty(creds.RefreshToken, creds.AccessToken, creds.Cookies)
	sum := sha256.Sum256([]byte(strings.TrimSpace(seed)))
	return "flowmusic-" + hex.EncodeToString(sum[:6]) + "@token.local"
}

// parseGoogleImportText 把导入文本解析成 []googleImportItem。
func parseGoogleImportText(text string) ([]googleImportItem, []string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, nil
	}
	var errs []string
	if strings.HasPrefix(trimmed, "[") {
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, []string{fmt.Sprintf("JSON 数组解析失败：%v", err)}
		}
		// 判断是不是「单账号的 cookie 导出数组」：元素含 name+value。
		if len(arr) > 0 && looksLikeCookieObject(arr[0]) {
			return []googleImportItem{{Cookies: trimmed}}, nil
		}
		out := make([]googleImportItem, 0, len(arr))
		for i, raw := range arr {
			var it googleImportItem
			if err := json.Unmarshal(raw, &it); err != nil {
				errs = append(errs, fmt.Sprintf("第 %d 条解析失败：%v", i+1, err))
				continue
			}
			out = append(out, it)
		}
		return out, errs
	}
	// JSONL：每行一个对象
	var out []googleImportItem
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			errs = append(errs, fmt.Sprintf("第 %d 行不是 JSON 对象", i+1))
			continue
		}
		var it googleImportItem
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			errs = append(errs, fmt.Sprintf("第 %d 行解析失败：%v", i+1, err))
			continue
		}
		out = append(out, it)
	}
	return out, errs
}

func looksLikeCookieObject(raw json.RawMessage) bool {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, hasName := m["name"]
	_, hasValue := m["value"]
	return hasName && hasValue
}

func googleToResp(m *model.PoolGoogle) *dto.GooglePoolResp {
	r := &dto.GooglePoolResp{
		ID:              m.ID,
		Email:           m.Email,
		HasCredential:   len(m.CredentialEnc) > 0,
		HasCookie:       len(m.CredentialEnc) > 0,
		ProtocolMode:    m.ProtocolMode,
		Status:          m.Status,
		Source:          m.Source,
		Credits:         m.Credits,
		TokensRemaining: m.TokensRemaining,
		RefreshEnabled:  m.RefreshEnabled,
		FailureCount:    m.FailureCount,
		CreatedAt:       m.CreatedAt.UnixMilli(),
		UpdatedAt:       m.UpdatedAt.UnixMilli(),
	}
	if m.DisplayName != nil {
		r.DisplayName = *m.DisplayName
	}
	if m.SubscriptionTier != nil {
		r.SubscriptionTier = *m.SubscriptionTier
	}
	if m.ExpiresAt != nil {
		r.ExpiresAt = m.ExpiresAt.UnixMilli()
	}
	if m.LastCheckedAt != nil {
		r.LastCheckedAt = m.LastCheckedAt.UnixMilli()
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
	if m.CooldownUntil != nil {
		r.CooldownUntil = m.CooldownUntil.UnixMilli()
	}
	if m.Notes != nil {
		r.Notes = *m.Notes
	}
	return r
}
