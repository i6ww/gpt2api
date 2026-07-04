package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
)

// GptExportFormat 导出格式。
//
//   - internal         : 我们自家的扁平 JSON Array（导入完全互通，含全部字段）
//   - crs              : claude-relay-service / chatgpt-pool 系导出格式
//                        {"exported_at":"...", "proxies":[], "accounts":[{...}]}
//   - codex            : token_xxx_xxx_<unix>.json 单 object 格式合并成 Array
//                        每元素 = { id_token, access_token, refresh_token,
//                                  client_id, account_id, email, type,
//                                  expired, last_refresh, password }
//   - account_password : 纯文本 "email:password" 一行一条（无 token）
type GptExportFormat string

const (
	GptExportFmtInternal        GptExportFormat = "internal"
	GptExportFmtCRS             GptExportFormat = "crs"
	GptExportFmtCodex           GptExportFormat = "codex"
	GptExportFmtAccountPassword GptExportFormat = "account_password"
)

// GptExportContentType 给定 format 应该用的 HTTP Content-Type。
func GptExportContentType(f GptExportFormat) string {
	if f == GptExportFmtAccountPassword {
		return "text/plain; charset=utf-8"
	}
	return "application/json; charset=utf-8"
}

// GptExportFileExt 给定 format 应该用的文件扩展名（不含点）。
func GptExportFileExt(f GptExportFormat) string {
	if f == GptExportFmtAccountPassword {
		return "txt"
	}
	return "json"
}

// gptInternalExportItem 内部导出格式的单条结构。
//
// 字段名与 dto.GptPoolCreateReq + 几个运维元数据对齐，让导出文件可以
// 直接粘回导入对话框（按 email upsert）。
type gptInternalExportItem struct {
	ID               uint64  `json:"id,omitempty"`
	Email            string  `json:"email"`
	Password         string  `json:"password,omitempty"`
	AccessToken      string  `json:"access_token,omitempty"`
	RefreshToken     string  `json:"refresh_token,omitempty"`
	IDToken          string  `json:"id_token,omitempty"`
	APIKey           string  `json:"api_key,omitempty"`
	OAuthIssuer      string  `json:"oauth_issuer,omitempty"`
	OAuthClientID    string  `json:"oauth_client_id,omitempty"`
	Status           string  `json:"status"`
	PlanType         string  `json:"plan_type,omitempty"`
	ChatGPTAccountID string  `json:"chatgpt_account_id,omitempty"`
	ExpiresAt        int64   `json:"expires_at,omitempty"`         // 毫秒
	QuotaPrimary     float64 `json:"quota_primary_used,omitempty"` // 0~100
	QuotaSecondary   float64 `json:"quota_secondary_used,omitempty"`
	LastRefreshAt    int64   `json:"last_refresh_at,omitempty"`
	LastQuotaCheckAt int64   `json:"last_quota_check_at,omitempty"`
	FailureCount     int     `json:"failure_count,omitempty"`
	ErrorMessage     string  `json:"error_message,omitempty"`
	Notes            string  `json:"notes,omitempty"`
	RegisteredAt     int64   `json:"registered_at"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
}

// crsExportFile claude-relay-service 风格导出文件外层。
type crsExportFile struct {
	ExportedAt string       `json:"exported_at"`
	Proxies    []any        `json:"proxies"`
	Accounts   []crsAccount `json:"accounts"`
}

// crsAccount 单条账号（CRS 格式）。
type crsAccount struct {
	Name               string         `json:"name"`
	Platform           string         `json:"platform"`
	Type               string         `json:"type"`
	Credentials        crsCredentials `json:"credentials"`
	Extra              map[string]any `json:"extra"`
	Concurrency        int            `json:"concurrency"`
	Priority           int            `json:"priority"`
	RateMultiplier     int            `json:"rate_multiplier"`
	AutoPauseOnExpired bool           `json:"auto_pause_on_expired"`
	PlanType           string         `json:"plan_type,omitempty"`
}

type crsCredentials struct {
	AccessToken      string `json:"access_token"`
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	ChatGPTUserID    string `json:"chatgpt_user_id,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"` // 秒
	ExpiresIn        int    `json:"expires_in,omitempty"`
	OrganizationID   string `json:"organization_id"`
	RefreshToken     string `json:"refresh_token,omitempty"`
}

// codexFileItem token_xxx 风格单 object（合并到 Array 输出）。
type codexFileItem struct {
	IDToken      string `json:"id_token,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	LastRefresh  string `json:"last_refresh,omitempty"`
	Email        string `json:"email"`
	Type         string `json:"type"`
	Expired      string `json:"expired,omitempty"`
	Password     string `json:"password,omitempty"`
}

// ExportJSON 按 scope + format 批量导出。
//
// scope ∈ all / valid / invalid / selected；selected 时 ids 必须非空。
//
// 凭证字段（password / access_token / refresh_token / id_token）在导出时
// 解密为明文。Codex / CRS 都对齐第三方常见格式，可直接被 claude-relay /
// chatgpt-pool / codex CLI 等工具读入。
//
// 返回 (body, count, err)。
func (s *PoolGptService) ExportJSON(
	ctx context.Context,
	scope, format string,
	ids []uint64,
) ([]byte, int, error) {
	repoScope := normalizeGptExportScope(scope)
	rows, err := s.repo.ListForExport(ctx, repoScope, ids, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("查询失败：%w", err)
	}

	// 解密所有凭证一次（避免每个 format 重复解一遍）
	type decRow struct {
		row          *model.PoolGpt
		password     string
		accessToken  string
		refreshToken string
		idToken      string
		apiKey       string
	}
	decoded := make([]decRow, 0, len(rows))
	for _, r := range rows {
		d := decRow{row: r}
		d.password = decryptOptional(s.aes, r.PasswordEnc)
		d.accessToken = decryptOptional(s.aes, r.AccessTokenEnc)
		d.refreshToken = decryptOptional(s.aes, r.RefreshTokenEnc)
		d.idToken = decryptOptional(s.aes, r.IDTokenEnc)
		d.apiKey = decryptOptional(s.aes, r.APIKeyEnc)
		decoded = append(decoded, d)
	}

	switch GptExportFormat(format) {
	case GptExportFmtCRS:
		f := crsExportFile{
			ExportedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Proxies:    []any{},
			Accounts:   make([]crsAccount, 0, len(decoded)),
		}
		for _, d := range decoded {
			r := d.row
			cred := crsCredentials{
				AccessToken:    d.accessToken,
				RefreshToken:   d.refreshToken,
				OrganizationID: "",
			}
			if r.ExpiresAt != nil {
				cred.ExpiresAt = r.ExpiresAt.Unix()
				rest := int(time.Until(*r.ExpiresAt).Seconds())
				if rest < 0 {
					rest = 0
				}
				cred.ExpiresIn = rest
			}
			if r.ChatGPTAccountID != nil {
				cred.ChatGPTAccountID = *r.ChatGPTAccountID
				cred.ChatGPTUserID = *r.ChatGPTAccountID
			}
			ac := crsAccount{
				Name:               r.Email,
				Platform:           "openai",
				Type:               "oauth",
				Credentials:        cred,
				Extra:              map[string]any{"email": r.Email},
				Concurrency:        10,
				Priority:           1,
				RateMultiplier:     1,
				AutoPauseOnExpired: true,
			}
			if r.PlanType != nil {
				ac.PlanType = *r.PlanType
			}
			f.Accounts = append(f.Accounts, ac)
		}
		body, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return nil, 0, err
		}
		return body, len(decoded), nil

	case GptExportFmtCodex:
		out := make([]codexFileItem, 0, len(decoded))
		for _, d := range decoded {
			r := d.row
			it := codexFileItem{
				Email:        r.Email,
				Type:         "codex",
				IDToken:      d.idToken,
				AccessToken:  d.accessToken,
				RefreshToken: d.refreshToken,
				Password:     d.password,
			}
			if r.OAuthClientID != nil {
				it.ClientID = *r.OAuthClientID
			}
			if r.ChatGPTAccountID != nil {
				it.AccountID = *r.ChatGPTAccountID
			}
			if r.LastRefreshAt != nil {
				it.LastRefresh = r.LastRefreshAt.UTC().Format(time.RFC3339)
			}
			if r.ExpiresAt != nil {
				it.Expired = r.ExpiresAt.UTC().Format(time.RFC3339)
			}
			out = append(out, it)
		}
		body, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, 0, err
		}
		return body, len(decoded), nil

	case GptExportFmtAccountPassword:
		var sb strings.Builder
		for _, d := range decoded {
			if d.password == "" {
				continue
			}
			sb.WriteString(d.row.Email)
			sb.WriteByte(':')
			sb.WriteString(d.password)
			sb.WriteByte('\n')
		}
		return []byte(sb.String()), len(decoded), nil

	default:
		// internal：扁平 JSON Array，与 Import 完全互通。
		out := make([]gptInternalExportItem, 0, len(decoded))
		for _, d := range decoded {
			r := d.row
			it := gptInternalExportItem{
				ID:           r.ID,
				Email:        r.Email,
				Password:     d.password,
				AccessToken:  d.accessToken,
				RefreshToken: d.refreshToken,
				IDToken:      d.idToken,
				APIKey:       d.apiKey,
				Status:       r.Status,
				FailureCount: r.FailureCount,
				RegisteredAt: r.RegisteredAt.UnixMilli(),
				CreatedAt:    r.CreatedAt.UnixMilli(),
				UpdatedAt:    r.UpdatedAt.UnixMilli(),
			}
			if r.OAuthIssuer != nil {
				it.OAuthIssuer = *r.OAuthIssuer
			}
			if r.OAuthClientID != nil {
				it.OAuthClientID = *r.OAuthClientID
			}
			if r.PlanType != nil {
				it.PlanType = *r.PlanType
			}
			if r.ChatGPTAccountID != nil {
				it.ChatGPTAccountID = *r.ChatGPTAccountID
			}
			if r.ExpiresAt != nil {
				it.ExpiresAt = r.ExpiresAt.UnixMilli()
			}
			if r.QuotaPrimaryUsedPercent != nil {
				it.QuotaPrimary = *r.QuotaPrimaryUsedPercent
			}
			if r.QuotaSecondaryUsedPercent != nil {
				it.QuotaSecondary = *r.QuotaSecondaryUsedPercent
			}
			if r.LastRefreshAt != nil {
				it.LastRefreshAt = r.LastRefreshAt.UnixMilli()
			}
			if r.LastQuotaCheckAt != nil {
				it.LastQuotaCheckAt = r.LastQuotaCheckAt.UnixMilli()
			}
			if r.ErrorMessage != nil {
				it.ErrorMessage = *r.ErrorMessage
			}
			if r.Notes != nil {
				it.Notes = *r.Notes
			}
			out = append(out, it)
		}
		body, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, 0, err
		}
		return body, len(decoded), nil
	}
}

// normalizeGptExportScope 容错地把字符串 scope 转成 enum；未知值 → all。
func normalizeGptExportScope(scope string) repo.PoolGptExportScope {
	switch scope {
	case "valid":
		return repo.GPTExportScopeValid
	case "invalid":
		return repo.GPTExportScopeInvalid
	case "selected":
		return repo.GPTExportScopeSelected
	}
	return repo.GPTExportScopeAll
}
