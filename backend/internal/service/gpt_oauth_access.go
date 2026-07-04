package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/jwtpayload"
)

const codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

type gptOAuthTokenDeps struct {
	aes  *crypto.AESGCM
	cfg  *SystemConfigService
	pool *AccountPool
}

func resolveGPToAuthAccessToken(ctx context.Context, deps gptOAuthTokenDeps, acc *model.Account, proxyURL string) (string, error) {
	if deps.aes == nil || acc == nil {
		return "", fmt.Errorf("OAuth token resolver not configured")
	}
	at, err := decryptOptionalCredential(deps.aes, acc.AccessTokenEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt access_token failed: %w", err)
	}
	rt, err := decryptOptionalCredential(deps.aes, acc.RefreshTokenEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt refresh_token failed: %w", err)
	}
	if rt == "" {
		rt, err = decryptOptionalCredential(deps.aes, acc.CredentialEnc)
		if err != nil {
			return "", fmt.Errorf("decrypt refresh credential failed: %w", err)
		}
	}
	if at != "" && rt == "" && !accessTokenNeedsRefresh(ctx, deps.cfg, acc, at) {
		return at, nil
	}
	if at != "" && rt != "" && !accessTokenNeedsRefresh(ctx, deps.cfg, acc, at) && !accessTokenShouldRefreshForCodex(acc) {
		return at, nil
	}
	if rt == "" {
		return "", fmt.Errorf("OAuth account missing refresh_token")
	}
	clientID, err := oauthRefreshClientID(acc)
	if err != nil {
		return "", err
	}
	oauth := NewOpenAIOAuthService(deps.cfg)
	tr, err := oauth.RefreshToken(ctx, rt, clientID, proxyURL)
	if err != nil {
		return "", fmt.Errorf("refresh OAuth access_token failed: %w", err)
	}
	now := time.Now().UTC()
	updates := map[string]any{"last_refresh_at": now}
	atEnc, err := deps.aes.Encrypt([]byte(strings.TrimSpace(tr.AccessToken)))
	if err != nil {
		return "", fmt.Errorf("encrypt access_token failed: %w", err)
	}
	updates["access_token_enc"] = atEnc
	if exp, ok := jwtpayload.ExpUnixFromJWT(tr.AccessToken); ok {
		t := time.Unix(exp, 0).UTC()
		updates["access_token_expires_at"] = t
	} else if tr.ExpiresIn > 0 {
		t := now.Add(time.Duration(tr.ExpiresIn) * time.Second)
		updates["access_token_expires_at"] = t
	}
	if strings.TrimSpace(tr.RefreshToken) != "" {
		rtEnc, err := deps.aes.Encrypt([]byte(strings.TrimSpace(tr.RefreshToken)))
		if err != nil {
			return "", fmt.Errorf("encrypt refresh_token failed: %w", err)
		}
		updates["refresh_token_enc"] = rtEnc
		updates["credential_enc"] = rtEnc
	}
	meta := accountOAuthMeta(acc)
	meta["scope"] = tr.Scope
	meta["updated"] = now.Unix()
	if tr.IDToken != "" {
		meta["id_token_present"] = true
	}
	if raw, err := json.Marshal(meta); err == nil {
		updates["oauth_meta"] = string(raw)
	}
	if deps.pool != nil && deps.pool.repo != nil {
		if err := deps.pool.repo.Update(ctx, acc.ID, updates); err != nil {
			return "", errcode.DBError.Wrap(err)
		}
	}
	acc.AccessTokenEnc = atEnc
	if v, ok := updates["access_token_expires_at"].(time.Time); ok {
		acc.AccessTokenExpiresAt = &v
	}
	if raw, ok := updates["oauth_meta"].(string); ok {
		acc.OAuthMeta = &raw
	}
	return strings.TrimSpace(tr.AccessToken), nil
}

func decryptOptionalCredential(aes *crypto.AESGCM, cipher []byte) (string, error) {
	if len(cipher) == 0 {
		return "", nil
	}
	plain, err := aes.Decrypt(cipher)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(plain)), nil
}

func accessTokenNeedsRefresh(ctx context.Context, cfg *SystemConfigService, acc *model.Account, at string) bool {
	if strings.TrimSpace(at) == "" {
		return true
	}
	expAt := acc.AccessTokenExpiresAt
	if expAt == nil {
		if exp, ok := jwtpayload.ExpUnixFromJWT(at); ok {
			t := time.Unix(exp, 0).UTC()
			expAt = &t
		}
	}
	if expAt == nil {
		return false
	}
	hours := int64(24)
	if cfg != nil {
		hours = cfg.RefreshBeforeHours(ctx)
	}
	return expAt.Before(time.Now().UTC().Add(time.Duration(hours) * time.Hour))
}

func accessTokenShouldRefreshForCodex(acc *model.Account) bool {
	if !isCodexOAuthAccount(acc) {
		return false
	}
	if acc.BaseURL != nil && strings.TrimSpace(*acc.BaseURL) != "" && !strings.Contains(strings.ToLower(*acc.BaseURL), "/codex") {
		return false
	}
	if acc.LastRefreshAt == nil {
		return true
	}
	return acc.LastRefreshAt.Before(time.Now().UTC().Add(-30 * time.Minute))
}

func isCodexOAuthAccount(acc *model.Account) bool {
	return acc != nil && acc.Provider == model.ProviderGPT && acc.AuthType == model.AuthTypeOAuth && strings.EqualFold(accountOAuthClientID(acc), codexOAuthClientID)
}

func oauthRefreshClientID(acc *model.Account) (string, error) {
	cid := strings.TrimSpace(accountOAuthClientID(acc))
	if isCodexOAuthAccount(acc) {
		return codexOAuthClientID, nil
	}
	if cid == "" {
		return "", fmt.Errorf("OAuth account missing client_id; ordinary ChatGPT accounts cannot fall back to Codex client_id")
	}
	return cid, nil
}

func isCodexOAuthGPTAccount(acc *model.Account) bool {
	return acc != nil && acc.Provider == model.ProviderGPT && acc.AuthType == model.AuthTypeOAuth
}
