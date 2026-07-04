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
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
)

// PoolAdobeService ADOBE 号池服务。
type PoolAdobeService struct {
	repo *repo.PoolAdobeRepo
	aes  *crypto.AESGCM
}

// NewPoolAdobeService 构造。
func NewPoolAdobeService(r *repo.PoolAdobeRepo, aes *crypto.AESGCM) *PoolAdobeService {
	return &PoolAdobeService{repo: r, aes: aes}
}

// List 列表。
func (s *PoolAdobeService) List(ctx context.Context, req *dto.AdobePoolListReq) ([]*dto.AdobePoolResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.PoolAdobeFilter{
		Status:   req.Status,
		Source:   req.Source,
		Keyword:  strings.TrimSpace(req.Keyword),
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.AdobePoolResp, 0, len(items))
	for _, it := range items {
		out = append(out, adobeToResp(it))
	}
	return out, total, nil
}

// Stats 状态分布。
func (s *PoolAdobeService) Stats(ctx context.Context) (*dto.AdobePoolStatsResp, error) {
	m, err := s.repo.Stats(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return &dto.AdobePoolStatsResp{
		Total:    m["total"],
		Valid:    m["valid"],
		Invalid:  m["invalid"],
		Disabled: m["disabled"],
		Cooldown: m["cooldown"],
	}, nil
}

// Create 单条新增。
func (s *PoolAdobeService) Create(ctx context.Context, req *dto.AdobePoolCreateReq) (*model.PoolAdobe, error) {
	p := &model.PoolAdobe{
		Email:   strings.ToLower(strings.TrimSpace(req.Email)),
		Source:  defaultStr(req.Source, model.AdobeSourceImport),
		Status:  defaultStr(req.Status, model.AdobeStatusValid),
		Credits: req.Credits,
	}
	if v := strings.TrimSpace(req.DisplayName); v != "" {
		p.DisplayName = &v
	}
	if v := strings.TrimSpace(req.AdobeUserID); v != "" {
		p.AdobeUserID = &v
	}
	if v := strings.TrimSpace(req.Notes); v != "" {
		p.Notes = &v
	}
	if req.ExpiresAt > 0 {
		t := adobeImportTime(req.ExpiresAt)
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
	if req.Cookie != "" {
		enc, err := s.aes.Encrypt([]byte(req.Cookie))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.CookieEnc = enc
	}
	if req.DeviceToken != "" {
		enc, err := s.aes.Encrypt([]byte(req.DeviceToken))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.DeviceTokenEnc = enc
	}
	if v := strings.TrimSpace(req.DeviceID); v != "" {
		p.DeviceID = v
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return p, nil
}

// Update 单条更新。
func (s *PoolAdobeService) Update(ctx context.Context, id uint64, req *dto.AdobePoolUpdateReq) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}
	fields := map[string]any{}
	if req.DisplayName != nil {
		fields["display_name"] = strings.TrimSpace(*req.DisplayName)
	}
	if req.AdobeUserID != nil {
		fields["adobe_user_id"] = strings.TrimSpace(*req.AdobeUserID)
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
	if req.Cookie != nil && *req.Cookie != "" {
		enc, err := s.aes.Encrypt([]byte(*req.Cookie))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["cookie_enc"] = enc
	}
	if req.DeviceToken != nil && *req.DeviceToken != "" {
		enc, err := s.aes.Encrypt([]byte(*req.DeviceToken))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["device_token_enc"] = enc
	}
	if req.DeviceID != nil {
		fields["device_id"] = strings.TrimSpace(*req.DeviceID)
	}
	if err := s.repo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// Import 文本批量导入。支持以下五种输入（按优先级识别）：
//
//  1. 整体 JSON Array：[{...}, {...}]（导出文件 / cookies (3).json 风格）
//  2. {"items": [...]} 包装格式
//  3. 单个 JSON 对象（含多行美化过的导出；如浏览器插件抓的 {email, cookies, user_id, ...}）
//  4. 每行一个 JSON 对象（JSONL，包含纯 cookie 文件 50个/100个/200个 adobe.txt）
//  5. email----password[----access_token[----cookie]] 简形
//  6. email|password 简形
//  7. email,password 简形
//
// JSON 各形态都兼容字段别名（见 applyAdobeImportAliases）：cookies→cookie、user_id→adobe_user_id。
//
// 缺 email 时的两条派生规则：
//   - 优先回落到 JSON 里的 `name` 字段（cookies (3).json 用 name 代表邮箱）
//   - 仍为空、但 cookie 非空 → 用 cookie 摘要派生稳定占位邮箱
//     `adobe-cookie-<sha12>@token.local`，让重复导入幂等（同 cookie 不会建多条）
//
// 后续 RefreshOne 会用 cookie 静默换 token、调 IMS profile 拿真实 displayName /
// adobe_user_id / 真实邮箱等元数据写库；占位 email 仅作 upsert 主键，不影响刷新。
func (s *PoolAdobeService) Import(ctx context.Context, req *dto.AdobePoolImportReq) (*dto.AdobePoolImportResult, error) {
	res := &dto.AdobePoolImportResult{}
	source := defaultStr(req.Source, model.AdobeSourceImport)
	batch := make([]*model.PoolAdobe, 0, 64)
	seen := map[string]struct{}{}

	items, parseErrs := ParseAdobeImportText(req.Text)
	if len(parseErrs) > 0 {
		res.Skipped += len(parseErrs)
		res.Errors = append(res.Errors, parseErrs...)
	}

	for _, item := range items {
		email := item.Email // ParseAdobeImportText 已经 lower+trim
		if _, dup := seen[email]; dup {
			res.Skipped++
			continue
		}
		seen[email] = struct{}{}

		p := &model.PoolAdobe{
			Email:   email,
			Source:  source,
			Status:  defaultStr(item.Status, model.AdobeStatusValid),
			Credits: item.Credits,
		}
		if v := strings.TrimSpace(item.DisplayName); v != "" {
			p.DisplayName = &v
		}
		if v := strings.TrimSpace(item.AdobeUserID); v != "" {
			p.AdobeUserID = &v
		}
		if v := strings.TrimSpace(item.Notes); v != "" {
			p.Notes = &v
		}
		if item.ExpiresAt > 0 {
			t := adobeImportTime(item.ExpiresAt)
			p.ExpiresAt = &t
		}
		if item.Password != "" {
			enc, err := s.aes.Encrypt([]byte(item.Password))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.PasswordEnc = enc
		}
		if item.AccessToken != "" {
			enc, err := s.aes.Encrypt([]byte(item.AccessToken))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.AccessTokenEnc = enc
		}
		if item.Cookie != "" {
			enc, err := s.aes.Encrypt([]byte(item.Cookie))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.CookieEnc = enc
		}
		if item.DeviceToken != "" {
			enc, err := s.aes.Encrypt([]byte(item.DeviceToken))
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			p.DeviceTokenEnc = enc
		}
		if v := strings.TrimSpace(item.DeviceID); v != "" {
			p.DeviceID = v
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
func (s *PoolAdobeService) Delete(ctx context.Context, id uint64) error {
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// BatchDelete 批量软删。
func (s *PoolAdobeService) BatchDelete(ctx context.Context, ids []uint64) (int64, error) {
	n, err := s.repo.SoftDeleteByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// Purge 按过滤条件批量软删。
//
// 见 repo.PoolAdobePurgeFilter；空条件 + All=false 时返回 0 + nil（safety）。
func (s *PoolAdobeService) Purge(ctx context.Context, f repo.PoolAdobePurgeFilter) (int64, error) {
	n, err := s.repo.Purge(ctx, f)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// AdobeExportItem 导出文件中的单条记录。
//
// 字段命名与 dto.AdobePoolCreateReq 对齐，便于"导出 → 重新导入"做完整克隆。
// password / access_token / cookie 解密后明文输出，请妥善保管。
//
// 额外补了 created_at / updated_at / failure_count / refresh_enabled / last_*_at
// 等运维字段（int64 毫秒时间戳；0 表示未设置），仅用于人工查阅，import 时会忽略未识别字段。
type AdobeExportItem struct {
	ID                 uint64  `json:"id"`
	Email              string  `json:"email"`
	DisplayName        string  `json:"display_name,omitempty"`
	AdobeUserID        string  `json:"adobe_user_id,omitempty"`
	Password           string  `json:"password,omitempty"`
	AccessToken        string  `json:"access_token,omitempty"`
	Cookie             string  `json:"cookie,omitempty"`
	DeviceToken        string  `json:"device_token,omitempty"`
	DeviceID           string  `json:"device_id,omitempty"`
	Status             string  `json:"status"`
	Source             string  `json:"source,omitempty"`
	Credits            float64 `json:"credits"`
	ExpiresAt          int64   `json:"expires_at,omitempty"`
	RefreshEnabled     int8    `json:"refresh_enabled"`
	FailureCount       int     `json:"failure_count,omitempty"`
	ErrorMessage       string  `json:"error_message,omitempty"`
	LastCheckedAt      int64   `json:"last_checked_at,omitempty"`
	LastRefreshAt      int64   `json:"last_refresh_at,omitempty"`
	LastCreditsCheckAt int64   `json:"last_credits_check_at,omitempty"`
	LastUsedAt         int64   `json:"last_used_at,omitempty"`
	CooldownUntil      int64   `json:"cooldown_until,omitempty"`
	Notes              string  `json:"notes,omitempty"`
	CreatedAt          int64   `json:"created_at"`
	UpdatedAt          int64   `json:"updated_at"`
}

// ExportJSON 按 scope 批量导出，输出格式化的 JSON Array。
//
// 凭证字段（password / access_token / cookie）在导出时解密为明文。
// 与 Import 完全互通：导出的 JSON 文件可以直接粘到导入对话框（or 用同接口）→
// 完整恢复账号（依据 email upsert）。
//
// 返回 (jsonBytes, count, err)。
func (s *PoolAdobeService) ExportJSON(ctx context.Context, scope repo.PoolAdobeExportScope) ([]byte, int, error) {
	rows, err := s.repo.ListForExport(ctx, scope, 0)
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]AdobeExportItem, 0, len(rows))
	for _, r := range rows {
		item := AdobeExportItem{
			ID:             r.ID,
			Email:          r.Email,
			Status:         r.Status,
			Source:         r.Source,
			Credits:        r.Credits,
			DeviceID:       r.DeviceID,
			RefreshEnabled: r.RefreshEnabled,
			FailureCount:   r.FailureCount,
			CreatedAt:      r.CreatedAt.UnixMilli(),
			UpdatedAt:      r.UpdatedAt.UnixMilli(),
		}
		if r.DisplayName != nil {
			item.DisplayName = *r.DisplayName
		}
		if r.AdobeUserID != nil {
			item.AdobeUserID = *r.AdobeUserID
		}
		if r.Notes != nil {
			item.Notes = *r.Notes
		}
		if r.ErrorMessage != nil {
			item.ErrorMessage = *r.ErrorMessage
		}
		if r.ExpiresAt != nil {
			item.ExpiresAt = r.ExpiresAt.UnixMilli()
		}
		if r.LastCheckedAt != nil {
			item.LastCheckedAt = r.LastCheckedAt.UnixMilli()
		}
		if r.LastRefreshAt != nil {
			item.LastRefreshAt = r.LastRefreshAt.UnixMilli()
		}
		if r.LastCreditsCheckAt != nil {
			item.LastCreditsCheckAt = r.LastCreditsCheckAt.UnixMilli()
		}
		if r.LastUsedAt != nil {
			item.LastUsedAt = r.LastUsedAt.UnixMilli()
		}
		if r.CooldownUntil != nil {
			item.CooldownUntil = r.CooldownUntil.UnixMilli()
		}
		// 解密凭证；解密失败留空（不阻断导出）
		if len(r.PasswordEnc) > 0 && s.aes != nil {
			if v, derr := s.aes.Decrypt(r.PasswordEnc); derr == nil {
				item.Password = string(v)
			}
		}
		if len(r.AccessTokenEnc) > 0 && s.aes != nil {
			if v, derr := s.aes.Decrypt(r.AccessTokenEnc); derr == nil {
				item.AccessToken = string(v)
			}
		}
		if len(r.CookieEnc) > 0 && s.aes != nil {
			if v, derr := s.aes.Decrypt(r.CookieEnc); derr == nil {
				item.Cookie = string(v)
			}
		}
		if len(r.DeviceTokenEnc) > 0 && s.aes != nil {
			if v, derr := s.aes.Decrypt(r.DeviceTokenEnc); derr == nil {
				item.DeviceToken = string(v)
			}
		}
		out = append(out, item)
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, 0, errcode.Internal.Wrap(err)
	}
	return body, len(rows), nil
}

// === helpers ===

func adobeToResp(m *model.PoolAdobe) *dto.AdobePoolResp {
	r := &dto.AdobePoolResp{
		ID:             m.ID,
		Email:          m.Email,
		HasPassword:    len(m.PasswordEnc) > 0,
		HasAccessToken: len(m.AccessTokenEnc) > 0,
		HasCookie:      len(m.CookieEnc) > 0,
		HasDeviceToken: len(m.DeviceTokenEnc) > 0,
		DeviceID:       m.DeviceID,
		Status:         m.Status,
		Source:         m.Source,
		Credits:        m.Credits,
		RefreshEnabled: m.RefreshEnabled,
		FailureCount:   m.FailureCount,
		CreatedAt:      m.CreatedAt.UnixMilli(),
		UpdatedAt:      m.UpdatedAt.UnixMilli(),
	}
	if m.DisplayName != nil {
		r.DisplayName = *m.DisplayName
	}
	if m.AdobeUserID != nil {
		r.AdobeUserID = *m.AdobeUserID
	}
	if m.ExpiresAt != nil {
		r.ExpiresAt = m.ExpiresAt.UnixMilli()
	}
	if m.LastCheckedAt != nil {
		r.LastCheckedAt = m.LastCheckedAt.UnixMilli()
	}
	if m.LastCreditsCheckAt != nil {
		r.LastCreditsCheckAt = m.LastCreditsCheckAt.UnixMilli()
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
	if m.CooldownUntil != nil {
		r.CooldownUntil = m.CooldownUntil.UnixMilli()
	}
	if m.Notes != nil {
		r.Notes = *m.Notes
	}
	r.Entitlements = parseAdobeEntitlements(m.EntitlementsJSON)
	return r
}

// parseAdobeEntitlements 把 pool_adobe.entitlements_json 翻译成给 admin
// 前端用的三态结构（unknown / blocked / ok）。
//
// 入参 NULL / 空 / 解析失败 → 返回 nil，前端按"从未测过，默认乐观"显示。
//
// DB 原生 schema 同时存"否决证据"和"肯定证据"两条 key：
//   - no_<tier>=true / no_<tier>_checked_at  来自 NotEntitledError，自动跳过
//   - ok_<tier>=true / ok_<tier>_checked_at  来自该档位上一次成功生成，明确确认开通
//
// 翻给前端时按"最新观察"决定单一状态：
//   - 两条都过期（或都不存在）→ unknown
//   - 只有 no_* 且在 TTL 内       → blocked
//   - 只有 ok_* 且在 TTL 内       → ok
//   - 两条都在 TTL 内             → checked_at 更新的那条获胜
//
// 返回的 checked_at 是被选中那一边的时间戳，前端用它显示「上次验证于 X 天前」。
func parseAdobeEntitlements(raw *string) *dto.AdobePoolEntitlements {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(*raw), &meta); err != nil || len(meta) == 0 {
		return nil
	}
	// ttlSec 与 generation_service.adobeEntitlementTTL 保持一致（7 天）。
	const ttlSec = int64(7 * 24 * 60 * 60)
	now := time.Now().UTC().Unix()
	tierStatus := func(tier string) (string, int64) {
		noFlag, _ := meta["no_"+tier].(bool)
		okFlag, _ := meta["ok_"+tier].(bool)
		noTs := int64FromMetaAny(meta, "no_"+tier+"_checked_at")
		okTs := int64FromMetaAny(meta, "ok_"+tier+"_checked_at")
		noFresh := noFlag && noTs > 0 && now-noTs < ttlSec
		okFresh := okFlag && okTs > 0 && now-okTs < ttlSec
		switch {
		case !noFresh && !okFresh:
			return "unknown", 0
		case noFresh && !okFresh:
			return "blocked", noTs
		case !noFresh && okFresh:
			return "ok", okTs
		}
		if okTs > noTs {
			return "ok", okTs
		}
		return "blocked", noTs
	}
	s1, ts1 := tierStatus("1k")
	s2, ts2 := tierStatus("2k")
	s4, ts4 := tierStatus("4k")
	return &dto.AdobePoolEntitlements{
		Image1K:          s1,
		Image1KCheckedAt: ts1 * 1000,
		Image2K:          s2,
		Image2KCheckedAt: ts2 * 1000,
		Image4K:          s4,
		Image4KCheckedAt: ts4 * 1000,
	}
}

// int64FromMetaAny 通用 meta 整数读取，兼容 json.Number / float64 / int 几种
// JSON 解码结果。这里独立一份避免依赖 generation_service 的私有 helper。
func int64FromMetaAny(meta map[string]any, key string) int64 {
	v, ok := meta[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

func defaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func adobeImportTime(v int64) time.Time {
	if v > 0 && v < 10_000_000_000 {
		return time.Unix(v, 0).UTC()
	}
	return time.UnixMilli(v).UTC()
}

// splitFlex 多分隔符尝试切分（取第一个命中的）
func splitFlex(line string, seps []string) []string {
	for _, sep := range seps {
		if strings.Contains(line, sep) {
			parts := strings.Split(line, sep)
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				out = append(out, strings.TrimSpace(p))
			}
			return out
		}
	}
	return []string{strings.TrimSpace(line)}
}

// unmarshalAdobeItem 把单条 JSON（array 元素 / items 元素 / JSONL 行 / 单对象）
// 解析成 AdobePoolCreateReq，并补齐常见的字段别名（见 applyAdobeImportAliases）。
func unmarshalAdobeItem(raw []byte) (dto.AdobePoolCreateReq, error) {
	var it dto.AdobePoolCreateReq
	if err := json.Unmarshal(raw, &it); err != nil {
		return it, err
	}
	applyAdobeImportAliases(raw, &it)
	return it, nil
}

// applyAdobeImportAliases 兼容第三方/插件导出文件里的字段别名，仅在标准字段为空时回填：
//   - cookies（复数）→ cookie：IMS 会话 cookie 串，cookie 刷新唯一必需项
//   - user_id        → adobe_user_id：Adobe 用户 ID（"...@AdobeID"）
//   - deviceId       → device_id：okad/脚本导出的 FF-iOS 设备 ID 别名
//
// 刷新链的 client_id / scope / endpoint 全部走 adoberefresh 的 Default*（clio-playground-web +
// 标准 firefly scope + /ims/check/v6/token），与这些导出文件里的同名字段一致，故无需读取。
func applyAdobeImportAliases(raw []byte, it *dto.AdobePoolCreateReq) {
	var a struct {
		Cookies  string `json:"cookies"`
		UserID   string `json:"user_id"`
		DeviceID string `json:"deviceId"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return
	}
	if strings.TrimSpace(it.Cookie) == "" {
		it.Cookie = strings.TrimSpace(a.Cookies)
	}
	if strings.TrimSpace(it.AdobeUserID) == "" {
		it.AdobeUserID = strings.TrimSpace(a.UserID)
	}
	if strings.TrimSpace(it.DeviceID) == "" {
		it.DeviceID = strings.TrimSpace(a.DeviceID)
	}
}

// tryParseSingleAdobeObject 尝试把整段文本当成「单个 JSON 对象」解析。
//
// 仅当文本是合法的单对象且带可用载荷（email / name / cookie 任一非空）时返回 ok=true。
// JSONL（多行各一个对象）整体不是合法单对象，json.Unmarshal 会失败 → 回落逐行解析。
func tryParseSingleAdobeObject(s string) (dto.AdobePoolCreateReq, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return dto.AdobePoolCreateReq{}, false
	}
	it, err := unmarshalAdobeItem([]byte(s))
	if err != nil || !adobeItemHasPayload(it) {
		return dto.AdobePoolCreateReq{}, false
	}
	return it, true
}

// adobeItemHasPayload 判断一条记录是否带足以入库的内容（避免把空对象/噪声当账号）。
func adobeItemHasPayload(it dto.AdobePoolCreateReq) bool {
	return strings.TrimSpace(it.Email) != "" ||
		strings.TrimSpace(it.Name) != "" ||
		strings.TrimSpace(it.Cookie) != "" ||
		strings.TrimSpace(it.DeviceToken) != ""
}

// ParseAdobeImportText 把批量导入文本拆解成 []AdobePoolCreateReq；纯解析层，
// 不依赖 DB / AES，便于单测覆盖。
//
// 见 PoolAdobeService.Import 注释里的格式枚举。返回 (items, errs)：
//   - items：每条 Email 已经 lowercase+trim、补好占位邮箱，调用方拿来直接 upsert
//   - errs ：人类可读的「第 N 行 …」错误列表；会一并并入 ImportResult.Errors
//
// 行号从原始 split 后的索引 +1 计算（JSON Array 模式下从 1 开始递增）。
func ParseAdobeImportText(text string) ([]dto.AdobePoolCreateReq, []string) {
	trimmed := strings.TrimSpace(text)
	type stage struct {
		ord  int
		item dto.AdobePoolCreateReq
		err  error
	}
	var raws []stage
	if strings.HasPrefix(trimmed, "[") {
		// 形态 A：顶层 JSON Array，例：cookies (21).json 风格
		//   [ { "cookie": "...", "name": "alice@example.com" }, ... ]
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, []string{fmt.Sprintf("JSON Array 解析失败：%v", err)}
		}
		for i, rm := range arr {
			it, err := unmarshalAdobeItem(rm)
			if err != nil {
				raws = append(raws, stage{ord: i + 1, err: fmt.Errorf("JSON 解析失败：%w", err)})
				continue
			}
			raws = append(raws, stage{ord: i + 1, item: it})
		}
	} else if strings.HasPrefix(trimmed, "{") && looksLikeWrappedItems(trimmed) {
		// 形态 B：{"items": [...]} 包装格式，例：adobe_items_*.json
		//   { "items": [ { "cookie":"...", "name":"...", "email":"...", "password":"..." }, ... ] }
		// 既兼容 name 字段，又兼容直接给 email/password 的完整记录。
		var wrapper struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal([]byte(trimmed), &wrapper); err != nil {
			return nil, []string{fmt.Sprintf(`JSON {"items": [...]} 解析失败：%v`, err)}
		}
		for i, rm := range wrapper.Items {
			it, err := unmarshalAdobeItem(rm)
			if err != nil {
				raws = append(raws, stage{ord: i + 1, err: fmt.Errorf("JSON 解析失败：%w", err)})
				continue
			}
			raws = append(raws, stage{ord: i + 1, item: it})
		}
	} else if single, ok := tryParseSingleAdobeObject(trimmed); ok {
		// 形态 C：单个 JSON 对象（含多行美化过的导出）。例如浏览器插件抓的
		//   { "email": "...", "cookies": "...", "user_id": "...", ... }
		// 这种整体是一个对象、不是 array / items 包装、也不是 JSONL，
		// 之前的逐行解析会把每行当乱码 → 全部失败。这里整体当一条处理。
		raws = append(raws, stage{ord: 1, item: single})
	} else {
		for i, raw := range strings.Split(text, "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			var it dto.AdobePoolCreateReq
			if strings.HasPrefix(line, "{") {
				var err error
				if it, err = unmarshalAdobeItem([]byte(line)); err != nil {
					raws = append(raws, stage{ord: i + 1, err: fmt.Errorf("JSON 解析失败：%w", err)})
					continue
				}
			} else {
				parts := splitFlex(line, []string{"----", "|", ","})
				if len(parts) < 2 {
					raws = append(raws, stage{ord: i + 1, err: fmt.Errorf("字段不足")})
					continue
				}
				it.Email = parts[0]
				it.Password = parts[1]
				if len(parts) >= 3 {
					it.AccessToken = parts[2]
				}
				if len(parts) >= 4 {
					it.Cookie = parts[3]
				}
			}
			raws = append(raws, stage{ord: i + 1, item: it})
		}
	}

	out := make([]dto.AdobePoolCreateReq, 0, len(raws))
	var errs []string
	for _, r := range raws {
		if r.err != nil {
			errs = append(errs, fmt.Sprintf("第 %d 行 %v", r.ord, r.err))
			continue
		}
		it := r.item
		email := strings.ToLower(strings.TrimSpace(it.Email))
		if email == "" {
			email = strings.ToLower(strings.TrimSpace(it.Name))
		}
		if email == "" && strings.TrimSpace(it.Cookie) != "" {
			email = placeholderAdobeEmailFromCookie(it.Cookie)
		}
		if email == "" {
			errs = append(errs, fmt.Sprintf("第 %d 行缺少 email/name/cookie", r.ord))
			continue
		}
		it.Email = email
		// 防止 Name 残留导致下游字段当 displayName 处理；Import 不再使用 Name
		it.Name = ""
		out = append(out, it)
	}
	return out, errs
}

// looksLikeWrappedItems 判断顶层是不是 {"items":[...]} 这种包装格式。
//
// 用 json.Decoder 浅扫一遍 token，遇到 "items" key 后下一个 token 必须是 `[`。
// 不直接 Unmarshal 是为了走快路径，避免把整段大文件解析两次。
func looksLikeWrappedItems(s string) bool {
	dec := json.NewDecoder(strings.NewReader(s))
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return false
				}
			}
		case string:
			if depth == 1 && strings.EqualFold(v, "items") {
				next, err := dec.Token()
				if err != nil {
					return false
				}
				if d, ok := next.(json.Delim); ok && d == '[' {
					return true
				}
				return false
			}
		}
	}
}

// placeholderAdobeEmailFromCookie 给「只有 cookie，没有 email」的导入条目派生
// 一个稳定的占位邮箱，作为 pool_adobe.email 唯一键。
//
// 实现：sha256(cookie) 取前 12 位 hex，套上 `@token.local` 域。同样的 cookie
// 永远派生出同样的 email → 重复导入会走 ON DUPLICATE KEY UPDATE 而不是建新行。
//
// 之后 RefreshOne 拿 cookie 换 token、调 /ims/profile 拿真实邮箱/displayName 写
// 进 display_name + adobe_user_id；如果运营想把占位 email 替换成真实 email，
// 需要走「编辑账号」单独改（migration 不会自动重写主键）。
func placeholderAdobeEmailFromCookie(cookie string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(cookie)))
	return "adobe-cookie-" + hex.EncodeToString(sum[:6]) + "@token.local"
}
