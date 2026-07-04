package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/provider/grok"
	"github.com/kleinai/backend/internal/repo"
	svc "github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/crypto"
)

type probeConfig struct {
	DSN         string
	AESKey      string
	Model       string
	Prompt      string
	Size        string
	Aspect      string
	Duration    int
	IDs         map[uint64]struct{}
	ProxyIDs    []uint64
	Refs        []string
	RefFile     string
	Repeat      int
	IntervalSec int
	TimeoutSec  int
	StopOnLimit bool
}

type probeResult struct {
	Attempt       int    `json:"attempt,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
	AccountID     uint64 `json:"account_id"`
	AccountStatus int8   `json:"account_status"`
	ProxyID       uint64 `json:"proxy_id,omitempty"`
	ProxyURL      string `json:"proxy_url,omitempty"`
	Plan          string `json:"plan,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
}

var grokProbeProxyCursor uint64

func main() {
	cfg := loadConfig()
	if cfg.DSN == "" || cfg.AESKey == "" {
		fail("missing KLEIN_DB_DSN or KLEIN_AES_KEY")
	}

	db, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{})
	if err != nil {
		fail(fmt.Sprintf("open db: %v", err))
	}
	key, err := decodeAESKey(cfg.AESKey)
	if err != nil {
		fail(fmt.Sprintf("decode aes: %v", err))
	}
	aesgcm, err := crypto.NewAESGCM(key)
	if err != nil {
		fail(fmt.Sprintf("new aes: %v", err))
	}

	accountRepo := repo.NewAccountRepo(db)
	proxyRepo := repo.NewProxyRepo(db)
	systemRepo := repo.NewSystemConfigRepo(db)
	proxySvc := svc.NewProxyService(proxyRepo, aesgcm)
	sysCfgSvc := svc.NewSystemConfigService(systemRepo)

	ctx := context.Background()
	accounts, _, err := accountRepo.List(ctx, repo.AccountListFilter{
		Provider: model.ProviderGROK,
		Page:     1,
		PageSize: 500,
	})
	if err != nil {
		fail(fmt.Sprintf("list accounts: %v", err))
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })

	var results []probeResult
	if cfg.Repeat > 1 {
		results, err = runRepeatedProbe(ctx, cfg, accounts, aesgcm, proxySvc, sysCfgSvc)
	} else {
		results, err = runSingleProbe(ctx, cfg, accounts, aesgcm, proxySvc, sysCfgSvc)
	}
	if err != nil {
		fail(err.Error())
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(results)
}

func runSingleProbe(
	ctx context.Context,
	cfg probeConfig,
	accounts []*model.Account,
	aesgcm *crypto.AESGCM,
	proxySvc *svc.ProxyService,
	sysCfgSvc *svc.SystemConfigService,
) ([]probeResult, error) {
	results := make([]probeResult, 0, len(accounts))
	excludedProxyIDs := map[uint64]struct{}{}
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		if len(cfg.IDs) > 0 {
			if _, ok := cfg.IDs[acc.ID]; !ok {
				continue
			}
		}
		if acc.Status != model.AccountStatusEnabled {
			results = append(results, probeResult{
				AccountID:     acc.ID,
				AccountStatus: acc.Status,
				Plan:          accountPlan(acc),
				Status:        "disabled_but_testing",
			})
		}
		if !supportsVideoPlan(acc) {
			results = append(results, probeResult{
				AccountID:     acc.ID,
				AccountStatus: acc.Status,
				Plan:          accountPlan(acc),
				Status:        "skip_plan",
				Error:         "plan does not support video",
			})
			continue
		}
		cred, err := decryptCredential(aesgcm, acc)
		if err != nil {
			results = append(results, probeResult{
				AccountID:     acc.ID,
				AccountStatus: acc.Status,
				Plan:          accountPlan(acc),
				Status:        "credential_error",
				Error:         err.Error(),
			})
			continue
		}
		proxyURL, proxyID, err := resolveProxyForProbe(ctx, proxySvc, sysCfgSvc, cfg, acc, excludedProxyIDs)
		if err != nil {
			results = append(results, probeResult{
				AccountID:     acc.ID,
				AccountStatus: acc.Status,
				Plan:          accountPlan(acc),
				Status:        "proxy_error",
				Error:         err.Error(),
			})
			continue
		}

		web := grok.NewWebClientWithProxy("", proxyURL).WithUpstreamLogger(stdoutUpstreamLogger(acc.ID))
		rctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
		startedAt := time.Now()
		_, err = web.GenerateVideo(rctx, cred, grok.VideoRequest{
			ModelCode:   cfg.Model,
			Prompt:      cfg.Prompt,
			Refs:        cfg.Refs,
			DurationSec: cfg.Duration,
			Size:        cfg.Size,
			AspectRatio: cfg.Aspect,
			Count:       1,
		})
		cancel()

		item := probeResult{
			AccountID:     acc.ID,
			AccountStatus: acc.Status,
			ProxyID:       proxyID,
			ProxyURL:      proxyURL,
			Plan:          accountPlan(acc),
			DurationMs:    time.Since(startedAt).Milliseconds(),
		}
		if err != nil {
			item.Status = classifyStatus(err)
			item.Error = err.Error()
			if proxyID != 0 {
				excludedProxyIDs[proxyID] = struct{}{}
			}
		} else {
			item.Status = "success"
		}
		results = append(results, item)
	}
	return results, nil
}

func runRepeatedProbe(
	ctx context.Context,
	cfg probeConfig,
	accounts []*model.Account,
	aesgcm *crypto.AESGCM,
	proxySvc *svc.ProxyService,
	sysCfgSvc *svc.SystemConfigService,
) ([]probeResult, error) {
	selected := make([]*model.Account, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		if len(cfg.IDs) > 0 {
			if _, ok := cfg.IDs[acc.ID]; !ok {
				continue
			}
		}
		if acc.Status != model.AccountStatusEnabled {
			continue
		}
		if !supportsVideoPlan(acc) {
			continue
		}
		selected = append(selected, acc)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no enabled super/heavy account matched for repeated probe")
	}

	acc := selected[0]
	cred, err := decryptCredential(aesgcm, acc)
	if err != nil {
		return nil, err
	}
	proxyIDs, err := resolveProxyCandidates(ctx, proxySvc, sysCfgSvc, cfg, acc)
	if err != nil {
		return nil, err
	}
	if len(proxyIDs) == 0 {
		proxyIDs = []uint64{0}
	}

	results := make([]probeResult, 0, cfg.Repeat)
	for i := 0; i < cfg.Repeat; i++ {
		if i > 0 && cfg.IntervalSec > 0 {
			time.Sleep(time.Duration(cfg.IntervalSec) * time.Second)
		}

		requestedProxyID := proxyIDs[i%len(proxyIDs)]
		proxyURL, resolvedProxyID, err := resolveProxyByID(ctx, proxySvc, requestedProxyID)
		if err != nil {
			results = append(results, probeResult{
				Attempt:       i + 1,
				Timestamp:     time.Now().Format(time.RFC3339),
				AccountID:     acc.ID,
				AccountStatus: acc.Status,
				ProxyID:       requestedProxyID,
				Plan:          accountPlan(acc),
				Status:        "proxy_error",
				Error:         err.Error(),
			})
			continue
		}

		web := grok.NewWebClientWithProxy("", proxyURL).WithUpstreamLogger(stdoutUpstreamLogger(acc.ID))
		rctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
		startedAt := time.Now()
		_, err = web.GenerateVideo(rctx, cred, grok.VideoRequest{
			ModelCode:   cfg.Model,
			Prompt:      cfg.Prompt,
			Refs:        cfg.Refs,
			DurationSec: cfg.Duration,
			Size:        cfg.Size,
			AspectRatio: cfg.Aspect,
			Count:       1,
		})
		cancel()

		item := probeResult{
			Attempt:       i + 1,
			Timestamp:     startedAt.Format(time.RFC3339),
			AccountID:     acc.ID,
			AccountStatus: acc.Status,
			ProxyID:       resolvedProxyID,
			ProxyURL:      proxyURL,
			Plan:          accountPlan(acc),
			DurationMs:    time.Since(startedAt).Milliseconds(),
		}
		if err != nil {
			item.Status = classifyStatus(err)
			item.Error = err.Error()
		} else {
			item.Status = "success"
		}
		results = append(results, item)

		if err != nil && cfg.StopOnLimit && (item.Status == "rate_limited" || item.Status == "cf_blocked") {
			break
		}
	}
	return results, nil
}

func loadConfig() probeConfig {
	cfg := probeConfig{
		DSN:         strings.TrimSpace(os.Getenv("KLEIN_DB_DSN")),
		AESKey:      strings.TrimSpace(os.Getenv("KLEIN_AES_KEY")),
		Model:       firstNonEmpty(strings.TrimSpace(os.Getenv("PROBE_MODEL")), "grok-imagine-video"),
		Prompt:      firstNonEmpty(strings.TrimSpace(os.Getenv("PROBE_PROMPT")), "图生视频压测"),
		Size:        strings.TrimSpace(os.Getenv("PROBE_SIZE")),
		Aspect:      firstNonEmpty(strings.TrimSpace(os.Getenv("PROBE_ASPECT_RATIO")), "9:16"),
		Duration:    parseInt(strings.TrimSpace(os.Getenv("PROBE_DURATION")), 6),
		IDs:         parseIDs(strings.TrimSpace(os.Getenv("PROBE_ACCOUNT_IDS"))),
		ProxyIDs:    parseIDList(strings.TrimSpace(os.Getenv("PROBE_PROXY_IDS"))),
		Refs:        parseCSV(strings.TrimSpace(os.Getenv("PROBE_REFS"))),
		RefFile:     strings.TrimSpace(os.Getenv("PROBE_REF_FILE")),
		Repeat:      parseInt(strings.TrimSpace(os.Getenv("PROBE_REPEAT")), 1),
		IntervalSec: parseInt(strings.TrimSpace(os.Getenv("PROBE_INTERVAL_SEC")), 20),
		TimeoutSec:  parseInt(strings.TrimSpace(os.Getenv("PROBE_TIMEOUT_SEC")), 180),
		StopOnLimit: parseBoolDefault(strings.TrimSpace(os.Getenv("PROBE_STOP_ON_LIMIT")), true),
	}
	if cfg.RefFile != "" {
		dataURL, err := fileToDataURL(cfg.RefFile)
		if err != nil {
			fail(fmt.Sprintf("load PROBE_REF_FILE: %v", err))
		}
		cfg.Refs = append([]string{dataURL}, cfg.Refs...)
	}
	return cfg
}

func stdoutUpstreamLogger(accountID uint64) provider.UpstreamLogger {
	return func(_ context.Context, e provider.UpstreamLogEntry) {
		payload := map[string]any{
			"account_id": accountID,
			"provider":   e.Provider,
			"stage":      e.Stage,
			"method":     e.Method,
			"url":        e.URL,
			"status":     e.StatusCode,
			"error":      e.Error,
			"meta":       e.Meta,
		}
		if e.RequestExcerpt != "" {
			payload["request_excerpt"] = e.RequestExcerpt
		}
		if e.ResponseExcerpt != "" {
			payload["response_excerpt"] = e.ResponseExcerpt
		}
		if raw, err := json.Marshal(payload); err == nil {
			fmt.Fprintf(os.Stderr, "UPSTREAM %s\n", string(raw))
		}
	}
}

func decryptCredential(aesgcm *crypto.AESGCM, acc *model.Account) (string, error) {
	if aesgcm == nil || acc == nil || len(acc.CredentialEnc) == 0 {
		return "", fmt.Errorf("missing credential")
	}
	plain, err := aesgcm.Decrypt(acc.CredentialEnc)
	if err != nil {
		return "", err
	}
	cred := strings.TrimSpace(string(plain))
	if cred == "" {
		return "", fmt.Errorf("empty credential")
	}
	return cred, nil
}

func resolveProxyForProbe(
	ctx context.Context,
	proxySvc *svc.ProxyService,
	sysCfg *svc.SystemConfigService,
	probeCfg probeConfig,
	acc *model.Account,
	exclude map[uint64]struct{},
) (string, uint64, error) {
	if proxySvc == nil || sysCfg == nil {
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
			if err == repo.ErrNotFound {
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

	if len(probeCfg.ProxyIDs) > 0 {
		return tryIDs(probeCfg.ProxyIDs)
	}

	if shouldPreferGrokProxyPool(acc) {
		items, err := proxySvc.ListEnabled(ctx)
		if err != nil {
			return "", 0, err
		}
		if proxyURL, selectedID, err := tryIDs(grokProbePoolIDs(acc, items)); err != nil {
			return "", 0, err
		} else if selectedID != 0 {
			return proxyURL, selectedID, nil
		}
	}

	if proxyURL, selectedID, err := tryIDs(orderedProbeProxyIDs(ctx, sysCfg, acc)); err != nil {
		return "", 0, err
	} else if selectedID != 0 {
		return proxyURL, selectedID, nil
	}

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
	return tryIDs(fallbackIDs)
}

func resolveProxyCandidates(
	ctx context.Context,
	proxySvc *svc.ProxyService,
	sysCfg *svc.SystemConfigService,
	probeCfg probeConfig,
	acc *model.Account,
) ([]uint64, error) {
	if len(probeCfg.ProxyIDs) > 0 {
		out := make([]uint64, 0, len(probeCfg.ProxyIDs))
		seen := map[uint64]struct{}{}
		for _, pid := range probeCfg.ProxyIDs {
			if pid == 0 {
				continue
			}
			if _, ok := seen[pid]; ok {
				continue
			}
			seen[pid] = struct{}{}
			out = append(out, pid)
		}
		return out, nil
	}

	if shouldPreferGrokProxyPool(acc) {
		items, err := proxySvc.ListEnabled(ctx)
		if err != nil {
			return nil, err
		}
		if ids := grokProbePoolIDs(acc, items); len(ids) > 0 {
			return ids, nil
		}
	}

	if ids := orderedProbeProxyIDs(ctx, sysCfg, acc); len(ids) > 0 {
		return ids, nil
	}

	items, err := proxySvc.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]uint64, 0, len(items))
	for _, item := range items {
		if item != nil {
			out = append(out, item.ID)
		}
	}
	return out, nil
}

func resolveProxyByID(ctx context.Context, proxySvc *svc.ProxyService, pid uint64) (string, uint64, error) {
	if pid == 0 || proxySvc == nil {
		return "", 0, nil
	}
	p, err := proxySvc.GetByID(ctx, pid)
	if err != nil {
		return "", 0, err
	}
	if p == nil || p.Status != model.ProxyStatusEnabled {
		return "", 0, fmt.Errorf("proxy %d is not enabled", pid)
	}
	u, err := proxySvc.BuildURL(p)
	if err != nil {
		return "", 0, err
	}
	if u == nil {
		return "", 0, fmt.Errorf("proxy %d has empty url", pid)
	}
	return u.String(), pid, nil
}

func supportsVideoPlan(acc *model.Account) bool {
	plan := strings.ToLower(strings.TrimSpace(accountPlan(acc)))
	return plan == "super" || plan == "heavy"
}

func orderedProbeProxyIDs(ctx context.Context, cfg *svc.SystemConfigService, acc *model.Account) []uint64 {
	orderedIDs := make([]uint64, 0, 2)
	if acc != nil && acc.ProxyID != nil {
		orderedIDs = append(orderedIDs, *acc.ProxyID)
	}
	if cfg != nil && cfg.GlobalProxyEnabled(ctx) {
		orderedIDs = append(orderedIDs, cfg.GlobalProxyID(ctx))
	}
	return orderedIDs
}

func shouldPreferGrokProxyPool(acc *model.Account) bool {
	return acc != nil && acc.Provider == model.ProviderGROK
}

func grokProbePoolIDs(acc *model.Account, items []*model.Proxy) []uint64 {
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
	start := int((atomic.AddUint64(&grokProbeProxyCursor, 1) - 1) % uint64(len(ids)))
	if acc != nil && acc.ID != 0 {
		start = (start + int(acc.ID%uint64(len(ids)))) % len(ids)
	}
	out := make([]uint64, 0, len(ids))
	out = append(out, ids[start:]...)
	out = append(out, ids[:start]...)
	return out
}

func accountPlan(acc *model.Account) string {
	meta := accountOAuthMeta(acc)
	if v, ok := meta["plan_type"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func accountOAuthMeta(account *model.Account) map[string]any {
	if account == nil || account.OAuthMeta == nil || strings.TrimSpace(*account.OAuthMeta) == "" {
		return map[string]any{}
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(*account.OAuthMeta), &meta); err != nil || meta == nil {
		return map[string]any{}
	}
	return meta
}

func classifyStatus(err error) string {
	if err == nil {
		return "success"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "too many requests"), strings.Contains(msg, "http 429"):
		return "rate_limited"
	case strings.Contains(msg, "just a moment"), strings.Contains(msg, "cloudflare"), strings.Contains(msg, "http 403"):
		return "cf_blocked"
	case strings.Contains(msg, "broken pipe"), strings.Contains(msg, "tls handshake"), strings.Contains(msg, "timeout"), strings.Contains(msg, "eof"):
		return "transport_error"
	default:
		return "failed"
	}
}

func parseIDs(raw string) map[uint64]struct{} {
	out := map[uint64]struct{}{}
	for _, id := range parseIDList(raw) {
		out[id] = struct{}{}
	}
	return out
}

func parseIDList(raw string) []uint64 {
	out := make([]uint64, 0)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var id uint64
		_, _ = fmt.Sscanf(part, "%d", &id)
		if id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func parseCSV(raw string) []string {
	out := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 {
		return fallback
	}
	return n
}

func parseBoolDefault(raw string, fallback bool) bool {
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func decodeAESKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("KLEIN_AES_KEY must be 32 bytes raw or 64 hex chars")
}

func fileToDataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty file")
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
