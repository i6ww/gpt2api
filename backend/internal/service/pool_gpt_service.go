package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/gptauth"
)

// PoolGptService GPT 号池服务。
type PoolGptService struct {
	repo *repo.PoolGptRepo
	aes  *crypto.AESGCM
	rdb  *redis.Client // 用于刷新节流（可空，nil 时退化为不节流）
}

// NewPoolGptService 构造。
func NewPoolGptService(r *repo.PoolGptRepo, aes *crypto.AESGCM) *PoolGptService {
	return &PoolGptService{repo: r, aes: aes}
}

// LoadCredentials 给 dispatcher（特别是 upgrade_plus）取一行账号 + 解密的 AT/RT。
//
// 不会触发任何刷新；如果 access_token 已过期由调用方决定要不要先 RefreshOne。
// 返回的 row 是只读 snapshot；任何写改请走 Update / MarkXxx 等高层方法。
func (s *PoolGptService) LoadCredentials(ctx context.Context, id uint64) (row *model.PoolGpt, accessToken, refreshToken string, err error) {
	row, err = s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, "", "", errcode.ResourceMissing
		}
		return nil, "", "", errcode.DBError.Wrap(err)
	}
	if row.DeletedAt != nil {
		return nil, "", "", errcode.ResourceMissing.WithMsg("账号已删除")
	}
	if s.aes != nil {
		if len(row.AccessTokenEnc) > 0 {
			if b, derr := s.aes.Decrypt(row.AccessTokenEnc); derr == nil {
				accessToken = string(b)
			}
		}
		if len(row.RefreshTokenEnc) > 0 {
			if b, derr := s.aes.Decrypt(row.RefreshTokenEnc); derr == nil {
				refreshToken = string(b)
			}
		}
	}
	if accessToken == "" && refreshToken == "" {
		return nil, "", "", errcode.AccountMissingCred
	}
	return row, accessToken, refreshToken, nil
}

// MarkPlusUpgraded dispatcher 在成功开通 Plus 后回写：
//   - plan_type=plus
//   - status=valid（确保后续刷新链路把它当好号看）
//   - last_used_at=now
//
// notes 可选，会拼接进 notes 字段（保留原 notes，不覆盖）。
func (s *PoolGptService) MarkPlusUpgraded(ctx context.Context, id uint64, notes string) error {
	now := time.Now().UTC()
	plan := "plus"
	fields := map[string]any{
		"plan_type":    plan,
		"status":       model.GPTStatusValid,
		"last_used_at": now,
	}
	if notes != "" {
		// 截断防止 size:500 列溢出。
		short := notes
		if len(short) > 480 {
			short = short[:480]
		}
		fields["notes"] = short
	}
	if err := s.repo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// WithRedis 注入 redis client；用于刷新节流，避免反复请求 OpenAI 加速封号。
//
// 节流策略（仅作用于"手动" caller，scheduler 不限）：
//   - 任意号刷新成功后，60s 内同一账号再点会返回 ProbeThrottled
//   - status=invalid 的号刷新后，30min 内同一账号再点会返回 ProbeThrottled
//
// 如果 rdb=nil（启动时 redis 不可用退化模式），节流退化为不开启。
func (s *PoolGptService) WithRedis(rdb *redis.Client) *PoolGptService {
	s.rdb = rdb
	return s
}

// List 列表。
func (s *PoolGptService) List(ctx context.Context, req *dto.GptPoolListReq) ([]*dto.GptPoolResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.PoolGptFilter{
		Status:   req.Status,
		PlanType: req.PlanType,
		Keyword:  strings.TrimSpace(req.Keyword),
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.GptPoolResp, 0, len(items))
	for _, it := range items {
		out = append(out, gptToResp(it))
	}
	return out, total, nil
}

// Stats 状态分布。
func (s *PoolGptService) Stats(ctx context.Context) (*dto.GptPoolStatsResp, error) {
	m, err := s.repo.Stats(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return &dto.GptPoolStatsResp{
		Total:    m["total"],
		Valid:    m["valid"],
		Invalid:  m["invalid"],
		Disabled: m["disabled"],
		Cooldown: m["cooldown"],
	}, nil
}

// Create 单条新增。
func (s *PoolGptService) Create(ctx context.Context, req *dto.GptPoolCreateReq) (*model.PoolGpt, error) {
	p := &model.PoolGpt{
		Email:  strings.ToLower(strings.TrimSpace(req.Email)),
		Status: defaultStr(req.Status, model.GPTStatusValid),
	}
	if v := strings.TrimSpace(req.OAuthIssuer); v != "" {
		p.OAuthIssuer = &v
	}
	if v := strings.TrimSpace(req.OAuthClientID); v != "" {
		p.OAuthClientID = &v
	}
	if v := strings.TrimSpace(req.Notes); v != "" {
		p.Notes = &v
	}
	if req.ExpiresAt > 0 {
		t := time.UnixMilli(req.ExpiresAt).UTC()
		p.ExpiresAt = &t
	}
	if req.Password != "" {
		enc, err := s.aes.Encrypt([]byte(req.Password))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.PasswordEnc = enc
	}
	if req.AccessToken != "" {
		enc, err := s.aes.Encrypt([]byte(req.AccessToken))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.AccessTokenEnc = enc
	}
	if req.RefreshToken != "" {
		enc, err := s.aes.Encrypt([]byte(req.RefreshToken))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.RefreshTokenEnc = enc
	}
	if req.IDToken != "" {
		enc, err := s.aes.Encrypt([]byte(req.IDToken))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.IDTokenEnc = enc
	}
	if req.APIKey != "" {
		enc, err := s.aes.Encrypt([]byte(req.APIKey))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.APIKeyEnc = enc
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return p, nil
}

// Detail 拿单条详情（含解密后的明文凭证）。
//
// 仅供管理后台编辑弹窗 / 离线导出使用——必须严格鉴权。
//
// 解密失败时该字段返回空串（而不是整体报错），避免一条坏数据让编辑页打不开。
func (s *PoolGptService) Detail(ctx context.Context, id uint64) (*dto.GptPoolDetailResp, error) {
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errcode.ResourceMissing
		}
		return nil, errcode.DBError.Wrap(err)
	}
	r := &dto.GptPoolDetailResp{
		ID:           m.ID,
		Email:        m.Email,
		Status:       m.Status,
		FailureCount: m.FailureCount,
		RegisteredAt: m.RegisteredAt.UnixMilli(),
		CreatedAt:    m.CreatedAt.UnixMilli(),
		UpdatedAt:    m.UpdatedAt.UnixMilli(),
	}
	r.Password = decryptOptional(s.aes, m.PasswordEnc)
	r.AccessToken = decryptOptional(s.aes, m.AccessTokenEnc)
	r.RefreshToken = decryptOptional(s.aes, m.RefreshTokenEnc)
	r.IDToken = decryptOptional(s.aes, m.IDTokenEnc)
	r.APIKey = decryptOptional(s.aes, m.APIKeyEnc)
	if m.OAuthIssuer != nil {
		r.OAuthIssuer = *m.OAuthIssuer
	}
	if m.OAuthClientID != nil {
		r.OAuthClientID = *m.OAuthClientID
	}
	if m.PlanType != nil {
		r.PlanType = *m.PlanType
	}
	if m.ChatGPTAccountID != nil {
		r.ChatGPTAccountID = *m.ChatGPTAccountID
	}
	if m.QuotaPrimaryUsedPercent != nil {
		v := *m.QuotaPrimaryUsedPercent
		r.QuotaPrimaryUsedPercent = &v
	}
	if m.QuotaPrimaryResetAt != nil {
		r.QuotaPrimaryResetAt = m.QuotaPrimaryResetAt.UnixMilli()
	}
	if m.QuotaSecondaryUsedPercent != nil {
		v := *m.QuotaSecondaryUsedPercent
		r.QuotaSecondaryUsedPercent = &v
	}
	if m.QuotaSecondaryResetAt != nil {
		r.QuotaSecondaryResetAt = m.QuotaSecondaryResetAt.UnixMilli()
	}
	if m.QuotaCodeReviewUsedPercent != nil {
		v := *m.QuotaCodeReviewUsedPercent
		r.QuotaCodeReviewUsedPercent = &v
	}
	if m.LastQuotaCheckAt != nil {
		r.LastQuotaCheckAt = m.LastQuotaCheckAt.UnixMilli()
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
	if m.LastUsedAt != nil {
		r.LastUsedAt = m.LastUsedAt.UnixMilli()
	}
	if m.ErrorMessage != nil {
		r.ErrorMessage = *m.ErrorMessage
	}
	if m.Notes != nil {
		r.Notes = *m.Notes
	}
	return r, nil
}

// decryptOptional 安全解密：空串/解密失败都返回空串而不是 panic。
func decryptOptional(aes *crypto.AESGCM, enc []byte) string {
	if len(enc) == 0 {
		return ""
	}
	plain, err := aes.Decrypt(enc)
	if err != nil {
		return ""
	}
	return string(plain)
}

// Update 单条更新。
func (s *PoolGptService) Update(ctx context.Context, id uint64, req *dto.GptPoolUpdateReq) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}
	fields := map[string]any{}
	if req.OAuthIssuer != nil {
		fields["oauth_issuer"] = strings.TrimSpace(*req.OAuthIssuer)
	}
	if req.OAuthClientID != nil {
		fields["oauth_client_id"] = strings.TrimSpace(*req.OAuthClientID)
	}
	if req.Status != nil {
		fields["status"] = *req.Status
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
	if req.Password != nil && *req.Password != "" {
		enc, err := s.aes.Encrypt([]byte(*req.Password))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["password_enc"] = enc
	}
	if req.AccessToken != nil && *req.AccessToken != "" {
		enc, err := s.aes.Encrypt([]byte(*req.AccessToken))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["access_token_enc"] = enc
	}
	if req.RefreshToken != nil && *req.RefreshToken != "" {
		enc, err := s.aes.Encrypt([]byte(*req.RefreshToken))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["refresh_token_enc"] = enc
	}
	if req.IDToken != nil && *req.IDToken != "" {
		enc, err := s.aes.Encrypt([]byte(*req.IDToken))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["id_token_enc"] = enc
	}
	if req.APIKey != nil && *req.APIKey != "" {
		enc, err := s.aes.Encrypt([]byte(*req.APIKey))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["api_key_enc"] = enc
	}
	if err := s.repo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// Import 文本批量导入。
// format=auto 时自动判断：
//   - 行以 `{` 开头按 JSON 处理
//   - 否则按 `email:password` 或多分隔符切分
// Import 文本批量导入 GPT 账号。自动识别 5 种格式：
//
//   1. **整体 JSON 对象**（CRS 风格）：{"exported_at":"...","accounts":[...]}
//      → 拆 accounts[]，每个 account 提取 credentials.{access,refresh}_token
//
//   2. **整体 JSON Array**（codex token_xxx 风格 / 我们自己的 internal export）：
//      [{...}, {...}]
//      → 直接逐元素解析；同时支持元素是 codexFileItem / gptInternalExportItem /
//        crsAccount / 自定义扁平字段
//
//   3. **整体 JSON 单 object**（codex 单 token 文件）：
//      {"id_token":"...","access_token":"...","email":"...","type":"codex",...}
//      → 当成 1 条导入
//
//   4. **每行一个 JSON 对象**：跟旧版兼容
//
//   5. **email:password[:refresh_token]** / **email----password----...** 等纯文本
//      → 跟旧版兼容
//
// 内部统一收敛到 `[]*model.PoolGpt` 后走 UpsertMany（按 email 去重 / 覆盖）。
func (s *PoolGptService) Import(ctx context.Context, req *dto.GptPoolImportReq) (*dto.GptPoolImportResult, error) {
	res := &dto.GptPoolImportResult{}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return res, nil
	}
	format := defaultStr(req.Format, "auto")

	// 尝试整体 JSON 解析（auto / json 模式都尝试）
	if format == "auto" || format == "json" {
		if items, ok := parseGptImportWholeJSON(text); ok {
			return s.importGptItems(ctx, items, res)
		}
	}

	// 退化到行级解析（每行一条 JSON 或 email:password 文本）
	items := make([]dto.GptPoolCreateReq, 0, 64)
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		isJSON := strings.HasPrefix(line, "{")
		if format == "json" || (format == "auto" && isJSON) {
			parsed, perr := parseGptImportSingleObject([]byte(line))
			if perr != nil {
				res.Skipped++
				res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行 JSON 解析失败：%v", i+1, perr))
				continue
			}
			items = append(items, parsed)
			continue
		}
		// 纯文本 email:password[:rt]
		parts := splitFlex(line, []string{"----", "|", ":"})
		if len(parts) < 2 {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行字段不足", i+1))
			continue
		}
		it := dto.GptPoolCreateReq{Email: parts[0], Password: parts[1]}
		if len(parts) >= 3 {
			it.RefreshToken = parts[2]
		}
		items = append(items, it)
	}
	return s.importGptItems(ctx, items, res)
}

// importGptItems 统一把解析后的 items[] 写库，更新 res。线程安全。
func (s *PoolGptService) importGptItems(
	ctx context.Context,
	items []dto.GptPoolCreateReq,
	res *dto.GptPoolImportResult,
) (*dto.GptPoolImportResult, error) {
	batch := make([]*model.PoolGpt, 0, len(items))
	seen := map[string]struct{}{}
	for i, it := range items {
		email := strings.ToLower(strings.TrimSpace(it.Email))
		if email == "" {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 条缺少 email", i+1))
			continue
		}
		if _, dup := seen[email]; dup {
			res.Skipped++
			continue
		}
		seen[email] = struct{}{}

		p := &model.PoolGpt{
			Email:  email,
			Status: defaultStr(it.Status, model.GPTStatusValid),
		}
		if v := strings.TrimSpace(it.OAuthIssuer); v != "" {
			p.OAuthIssuer = &v
		}
		if v := strings.TrimSpace(it.OAuthClientID); v != "" {
			p.OAuthClientID = &v
		}
		if v := strings.TrimSpace(it.Notes); v != "" {
			p.Notes = &v
		}
		if it.ExpiresAt > 0 {
			t := time.UnixMilli(it.ExpiresAt).UTC()
			p.ExpiresAt = &t
		}
		if it.Password != "" {
			enc, err := s.aes.Encrypt([]byte(it.Password))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.PasswordEnc = enc
		}
		if it.AccessToken != "" {
			enc, err := s.aes.Encrypt([]byte(it.AccessToken))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.AccessTokenEnc = enc
			// 解码 access_token 顺便填 plan_type / chatgpt_account_id /
			// expires_at / oauth_client_id / oauth_issuer。
			// 这样导入完成立刻能在列表看到完整画像，不必等 scheduler。
			if claims, derr := gptauth.DecodeAccessToken(it.AccessToken); derr == nil {
				if claims.PlanType != "" {
					pt := claims.PlanType
					p.PlanType = &pt
				}
				if claims.ChatGPTAccountID != "" {
					aid := claims.ChatGPTAccountID
					p.ChatGPTAccountID = &aid
				}
				if claims.Exp > 0 && p.ExpiresAt == nil {
					t := time.Unix(claims.Exp, 0).UTC()
					p.ExpiresAt = &t
				}
				if claims.ClientID != "" && p.OAuthClientID == nil {
					cid := claims.ClientID
					p.OAuthClientID = &cid
				}
				if p.OAuthIssuer == nil {
					iss := "https://auth.openai.com"
					p.OAuthIssuer = &iss
				}
				if claims.Email != "" && (p.Email == "" || p.Email == "-") {
					p.Email = strings.ToLower(claims.Email)
				}
			}
		}
		if it.RefreshToken != "" {
			enc, err := s.aes.Encrypt([]byte(it.RefreshToken))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.RefreshTokenEnc = enc
		}
		if it.IDToken != "" {
			enc, err := s.aes.Encrypt([]byte(it.IDToken))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.IDTokenEnc = enc
		}
		if it.APIKey != "" {
			enc, err := s.aes.Encrypt([]byte(it.APIKey))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.APIKeyEnc = enc
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

// parseGptImportWholeJSON 尝试把整段 text 当作单一 JSON 解析。
//
// 命中条件：
//
//   - JSON Array：[ { ... }, ... ]
//   - JSON Object 且根 key 含 "accounts" 数组（CRS 风格） → 拆 accounts
//   - JSON Object 单 token（codex 单文件 token_xxx）→ 单条
//
// 任一条件命中就返回 (items, true)；否则 (nil, false) 由 caller 退化到行级解析。
func parseGptImportWholeJSON(text string) ([]dto.GptPoolCreateReq, bool) {
	trim := strings.TrimSpace(text)
	if trim == "" {
		return nil, false
	}
	// Array
	if strings.HasPrefix(trim, "[") {
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(trim), &arr); err != nil {
			return nil, false
		}
		out := make([]dto.GptPoolCreateReq, 0, len(arr))
		for _, raw := range arr {
			it, err := parseGptImportSingleObject(raw)
			if err == nil && strings.TrimSpace(it.Email) != "" {
				out = append(out, it)
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	}
	// Object
	if !strings.HasPrefix(trim, "{") {
		return nil, false
	}
	// 先尝试 CRS 包装 {"accounts":[...]}
	var wrap struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(trim), &wrap); err == nil && len(wrap.Accounts) > 0 {
		out := make([]dto.GptPoolCreateReq, 0, len(wrap.Accounts))
		for _, raw := range wrap.Accounts {
			it, err := parseGptImportSingleObject(raw)
			if err == nil && strings.TrimSpace(it.Email) != "" {
				out = append(out, it)
			}
		}
		if len(out) > 0 {
			return out, true
		}
	}
	// 单 object：codex token 单文件 / 我们自己的内部 export 单条
	it, err := parseGptImportSingleObject([]byte(trim))
	if err == nil && strings.TrimSpace(it.Email) != "" {
		return []dto.GptPoolCreateReq{it}, true
	}
	return nil, false
}

// parseGptImportSingleObject 解析一个 JSON object → dto.GptPoolCreateReq。
//
// 兼容 4 种来源：
//
//  1. 我们的扁平 export：{"email":"...","access_token":"...","refresh_token":"...",
//                         "id_token":"...","oauth_client_id":"...","plan_type":"..."}
//
//  2. CRS account 内部对象：{"name":"...","credentials":{"access_token":"...",
//                            "refresh_token":"...","chatgpt_account_id":"..."},
//                            "extra":{"email":"..."}}
//
//  3. Codex 单文件 token：{"id_token":"...","client_id":"...","access_token":"...",
//                          "refresh_token":"...","email":"...","type":"codex",
//                          "password":"...","expired":"2026-05-18T..."}
//
//  4. 我们的 GptPoolCreateReq 字段名（用于直接粘 admin 编辑器输出）
//
// 字段提取按优先级：直接字段 > credentials.* > extra.email > name。
func parseGptImportSingleObject(raw []byte) (dto.GptPoolCreateReq, error) {
	var blob map[string]any
	if err := json.Unmarshal(raw, &blob); err != nil {
		return dto.GptPoolCreateReq{}, err
	}
	out := dto.GptPoolCreateReq{}

	// helper：从 map 里以多个候选 key 取 string
	pickStr := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}

	// 顶层字段
	out.Email = pickStr(blob, "email", "name", "username")
	out.Password = pickStr(blob, "password")
	out.AccessToken = pickStr(blob, "access_token", "accessToken")
	out.RefreshToken = pickStr(blob, "refresh_token", "refreshToken")
	out.IDToken = pickStr(blob, "id_token", "idToken")
	out.APIKey = pickStr(blob, "api_key", "apiKey")
	out.OAuthIssuer = pickStr(blob, "oauth_issuer", "issuer")
	out.OAuthClientID = pickStr(blob, "oauth_client_id", "client_id", "clientId")
	out.Notes = pickStr(blob, "notes")
	out.Status = pickStr(blob, "status")
	if v, ok := blob["expires_at"].(float64); ok {
		out.ExpiresAt = normalizeUnixToMilli(int64(v))
	}

	// CRS：credentials.* 嵌套结构
	if cred, ok := blob["credentials"].(map[string]any); ok {
		if out.AccessToken == "" {
			out.AccessToken = pickStr(cred, "access_token", "accessToken")
		}
		if out.RefreshToken == "" {
			out.RefreshToken = pickStr(cred, "refresh_token", "refreshToken")
		}
		if out.IDToken == "" {
			out.IDToken = pickStr(cred, "id_token", "idToken")
		}
		if out.OAuthClientID == "" {
			out.OAuthClientID = pickStr(cred, "client_id", "clientId")
		}
		if out.ExpiresAt == 0 {
			if v, ok := cred["expires_at"].(float64); ok {
				out.ExpiresAt = normalizeUnixToMilli(int64(v))
			}
		}
	}

	// CRS：extra.email 兜底
	if out.Email == "" {
		if extra, ok := blob["extra"].(map[string]any); ok {
			out.Email = pickStr(extra, "email", "username")
		}
	}

	// codex 单文件：expired 是 RFC3339 字符串
	if out.ExpiresAt == 0 {
		if s := pickStr(blob, "expired", "expires"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				out.ExpiresAt = t.UnixMilli()
			}
		}
	}
	return out, nil
}

// normalizeUnixToMilli 兼容传入的时间戳：秒（10 位）→ 毫秒（13 位）。
//
// 经验法则：< 1e12 当作秒，否则当作毫秒。1e12 ≈ 2001 年的毫秒时间戳。
func normalizeUnixToMilli(v int64) int64 {
	if v <= 0 {
		return 0
	}
	if v < 1_000_000_000_000 {
		return v * 1000
	}
	return v
}

// Delete 软删。
func (s *PoolGptService) Delete(ctx context.Context, id uint64) error {
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// BatchDelete 批量软删。
func (s *PoolGptService) BatchDelete(ctx context.Context, ids []uint64) (int64, error) {
	n, err := s.repo.SoftDeleteByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// === helpers ===

func gptToResp(m *model.PoolGpt) *dto.GptPoolResp {
	r := &dto.GptPoolResp{
		ID:              m.ID,
		Email:           m.Email,
		HasPassword:     len(m.PasswordEnc) > 0,
		HasAccessToken:  len(m.AccessTokenEnc) > 0,
		HasRefreshToken: len(m.RefreshTokenEnc) > 0,
		HasIDToken:      len(m.IDTokenEnc) > 0,
		HasAPIKey:       len(m.APIKeyEnc) > 0,
		Status:          m.Status,
		FailureCount:    m.FailureCount,
		RegisteredAt:    m.RegisteredAt.UnixMilli(),
		CreatedAt:       m.CreatedAt.UnixMilli(),
		UpdatedAt:       m.UpdatedAt.UnixMilli(),
	}
	if m.OAuthIssuer != nil {
		r.OAuthIssuer = *m.OAuthIssuer
	}
	if m.OAuthClientID != nil {
		r.OAuthClientID = *m.OAuthClientID
	}
	if m.PlanType != nil {
		r.PlanType = *m.PlanType
	}
	if m.ChatGPTAccountID != nil {
		r.ChatGPTAccountID = *m.ChatGPTAccountID
	}
	if m.QuotaPrimaryUsedPercent != nil {
		v := *m.QuotaPrimaryUsedPercent
		r.QuotaPrimaryUsedPercent = &v
	}
	if m.QuotaPrimaryResetAt != nil {
		r.QuotaPrimaryResetAt = m.QuotaPrimaryResetAt.UnixMilli()
	}
	if m.QuotaSecondaryUsedPercent != nil {
		v := *m.QuotaSecondaryUsedPercent
		r.QuotaSecondaryUsedPercent = &v
	}
	if m.QuotaSecondaryResetAt != nil {
		r.QuotaSecondaryResetAt = m.QuotaSecondaryResetAt.UnixMilli()
	}
	if m.QuotaCodeReviewUsedPercent != nil {
		v := *m.QuotaCodeReviewUsedPercent
		r.QuotaCodeReviewUsedPercent = &v
	}
	if m.LastQuotaCheckAt != nil {
		r.LastQuotaCheckAt = m.LastQuotaCheckAt.UnixMilli()
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
	if m.LastUsedAt != nil {
		r.LastUsedAt = m.LastUsedAt.UnixMilli()
	}
	if m.ErrorMessage != nil {
		r.ErrorMessage = *m.ErrorMessage
	}
	if m.Notes != nil {
		r.Notes = *m.Notes
	}
	return r
}
