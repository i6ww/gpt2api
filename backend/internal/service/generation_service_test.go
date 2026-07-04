package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/provider/adobe/firefly"
)

func TestAccountUsesNativeGPTImage2DefaultsToOpenAI(t *testing.T) {
	if !accountUsesNativeGPTImage2(&model.Account{}) {
		t.Fatal("expected empty base url to use native gpt-image-2 routing")
	}
}

func TestAccountUsesNativeGPTImage2TreatsPic2APIAsCompatibilityGateway(t *testing.T) {
	base := "https://pic2api.com"
	if accountUsesNativeGPTImage2(&model.Account{BaseURL: &base}) {
		t.Fatal("expected pic2api base url to bypass native gpt-image-2 routing")
	}
}

func TestIsCompatibilityGatewayImageModelGemini(t *testing.T) {
	if !isCompatibilityGatewayImageModel("gemini-3.0-pro-image") {
		t.Fatal("expected gemini image model to require compatibility gateway accounts")
	}
}

func TestIsCompatibilityGatewayImageModelGPTImage2(t *testing.T) {
	if isCompatibilityGatewayImageModel("gpt-image-2") {
		t.Fatal("expected gpt-image-2 to keep mixed native/gateway routing")
	}
}

func TestGenerationServiceUpstreamModelUsesConfiguredAlias(t *testing.T) {
	cfg := &SystemConfigService{
		cache: map[string]string{
			"billing.model_prices": `[{"model_code":"gemini-3.0-pro-image","upstream_model":"gemini-2.5-flash-image-preview","enabled":true}]`,
		},
		loaded: time.Now(),
		ttl:    time.Hour,
	}
	svc := &GenerationService{cfg: cfg}
	got := svc.upstreamModel("gemini-3.0-pro-image")
	if got != "gemini-2.5-flash-image-preview" {
		t.Fatalf("expected upstream alias, got %q", got)
	}
}

func TestShouldKeepUpstreamAPIEnabledForCustomAPIKeyBaseURL(t *testing.T) {
	base := "https://pic2api.com"
	acc := &model.Account{AuthType: model.AuthTypeAPIKey, BaseURL: &base}
	if !shouldKeepUpstreamAPIEnabled(acc) {
		t.Fatal("expected custom api-key upstream to stay enabled after failures")
	}
}

func TestShouldKeepUpstreamAPIEnabledFalseForDefaultProviderAccount(t *testing.T) {
	acc := &model.Account{AuthType: model.AuthTypeAPIKey}
	if shouldKeepUpstreamAPIEnabled(acc) {
		t.Fatal("expected default provider account to keep existing cooldown logic")
	}
}

func TestMarkProviderQuotaLimitedKeepsCustomUpstreamEnabled(t *testing.T) {
	base := "https://pic2api.com"
	acc := &model.Account{
		ID:       321,
		Provider: model.ProviderGPT,
		AuthType: model.AuthTypeAPIKey,
		BaseURL:  &base,
	}
	pool := NewAccountPool(nil, time.Hour)
	svc := &GenerationService{pool: pool}

	svc.markProviderQuotaLimited(context.Background(), acc, "usage_limit_reached", time.Now().UTC().Add(time.Hour))

	if acc.Status == model.AccountStatusBroken {
		t.Fatal("expected custom upstream api account to avoid cooldown status")
	}
}

func TestShouldDirectConnectCustomUpstreamTrueWithoutDedicatedProxy(t *testing.T) {
	base := "https://pic2api.com"
	acc := &model.Account{AuthType: model.AuthTypeAPIKey, BaseURL: &base}
	if !shouldDirectConnectCustomUpstream(acc) {
		t.Fatal("expected custom upstream api account to bypass global proxy")
	}
}

func TestShouldDirectConnectCustomUpstreamFalseWithDedicatedProxy(t *testing.T) {
	base := "https://pic2api.com"
	proxyID := uint64(33)
	acc := &model.Account{AuthType: model.AuthTypeAPIKey, BaseURL: &base, ProxyID: &proxyID}
	if shouldDirectConnectCustomUpstream(acc) {
		t.Fatal("expected account-scoped proxy to keep proxy routing")
	}
}

func TestShouldPreserveInlineRefsForGPTImageTask(t *testing.T) {
	task := &model.GenerationTask{Provider: model.ProviderGPT, Kind: string(provider.KindImage)}
	if !shouldPreserveInlineRefsForTask(task) {
		t.Fatal("expected gpt image task to preserve inline refs")
	}
}

func TestShouldPreserveInlineRefsForNonGPTImageTaskFalse(t *testing.T) {
	task := &model.GenerationTask{Provider: model.ProviderGROK, Kind: string(provider.KindVideo)}
	if shouldPreserveInlineRefsForTask(task) {
		t.Fatal("expected non-gpt-image task to keep existing ref normalization")
	}
}

func TestProviderCooldownGrokForbiddenIsTransient(t *testing.T) {
	err := errors.New(`grok upload HTTP 403: <!DOCTYPE html><html><head><title>Just a moment...</title></head></html>`)
	if got := providerCooldown(err); got != 0 {
		t.Fatalf("expected transient cooldown 0, got %s", got)
	}
}

func TestProviderCooldownRetryable429StillCooldowns(t *testing.T) {
	err := errors.New(`provider call: grok video HTTP 429: {"error":{"code":8,"message":"Too many requests"}}`)
	got := providerCooldown(err)
	if got < 30*time.Minute {
		t.Fatalf("expected 429 cooldown >= 30m, got %s", got)
	}
}

func TestShouldRotateProxyOnRetryGrokCloudflare(t *testing.T) {
	err := errors.New(`grok upload HTTP 403: <!DOCTYPE html><html><head><title>Just a moment...</title></head></html>`)
	if !shouldRotateProxyOnRetry(model.ProviderGROK, err) {
		t.Fatal("expected Grok Cloudflare failure to rotate proxy on retry")
	}
}

func TestShouldRotateProxyOnRetryNonGrokDoesNotRotate(t *testing.T) {
	err := errors.New(`grok upload HTTP 403: cloudflare forbidden`)
	if shouldRotateProxyOnRetry(model.ProviderGPT, err) {
		t.Fatal("expected non-codex GPT error to keep proxy selection unchanged")
	}
}

func TestShouldRotateProxyOnRetryCodexCloudflare(t *testing.T) {
	err := errors.New(`codex chat http 403: <!DOCTYPE html><html><head><title>Just a moment...</title></head></html>`)
	if !shouldRotateProxyOnRetry(model.ProviderGPT, err) {
		t.Fatal("expected codex chat Cloudflare failure to rotate proxy on retry")
	}
}

func TestRetryableProviderErrorCodexChat403(t *testing.T) {
	err := errors.New(`codex chat http 403: blocked`)
	if !retryableProviderError(err) {
		t.Fatal("expected codex chat 403 to be retryable")
	}
}

func TestShouldRotateProxyOnRetryGrokBrokenPipe(t *testing.T) {
	err := errors.New(`Post "https://grok.com/rest/app-chat/upload-file": write request failed: write tcp 172.21.0.6:60458->38.246.244.5:58589: write: broken pipe`)
	if !shouldRotateProxyOnRetry(model.ProviderGROK, err) {
		t.Fatal("expected Grok broken pipe upload failure to rotate proxy on retry")
	}
}

func TestRetryableProviderErrorBrokenPipe(t *testing.T) {
	err := errors.New(`Post "https://grok.com/rest/app-chat/upload-file": write request failed: write tcp 172.21.0.6:60458->38.246.244.5:58589: write: broken pipe`)
	if !retryableProviderError(err) {
		t.Fatal("expected broken pipe upload failure to be retryable")
	}
}

func TestShouldRotateProxyOnRetryGrokTLSHandshakeEOF(t *testing.T) {
	err := errors.New(`Post "https://grok.com/rest/app-chat/upload-file": tls handshake to grok.com failed: EOF`)
	if !shouldRotateProxyOnRetry(model.ProviderGROK, err) {
		t.Fatal("expected Grok TLS handshake EOF failure to rotate proxy on retry")
	}
}

func TestShouldRotateProxyOnRetryAdobeRegionBlocked451(t *testing.T) {
	err := firefly.ClassifyError(451, map[string]string{}, "region restricted")
	if !isAdobeRegionBlockedError(err) {
		t.Fatal("expected HTTP 451 to be detected as Adobe region-blocked error")
	}
	if shouldRotateProxyOnRetry(model.ProviderADOBE, err) {
		t.Fatal("expected Adobe 451 region restriction to fail fast without proxy rotation")
	}
	if retryableProviderError(err) {
		t.Fatal("expected Adobe 451 region restriction to be non-retryable")
	}
}

func TestPickAccountForTaskSkipsExcludedGrokAccounts(t *testing.T) {
	acc1 := &model.Account{
		ID:       308,
		Provider: model.ProviderGROK,
		AuthType: model.AuthTypeCookie,
		Status:   model.AccountStatusEnabled,
	}
	acc2 := &model.Account{
		ID:       309,
		Provider: model.ProviderGROK,
		AuthType: model.AuthTypeCookie,
		Status:   model.AccountStatusEnabled,
	}
	pool := &AccountPool{
		cacheTTL: time.Hour,
		buckets: map[string]*providerBucket{
			model.ProviderGROK: {
				loadedAt: time.Now(),
				items:    []*model.Account{acc1, acc2},
			},
		},
		busy: map[uint64]struct{}{},
	}
	svc := &GenerationService{pool: pool}
	task := &model.GenerationTask{Provider: model.ProviderGROK, Kind: string(provider.KindVideo), ModelCode: "grok-imagine-video"}

	got, err := svc.pickAccountForTask(context.Background(), task, nil, map[uint64]struct{}{acc1.ID: {}})
	if err != nil {
		t.Fatalf("pickAccountForTask() error = %v", err)
	}
	if got == nil || got.ID != acc2.ID {
		t.Fatalf("pickAccountForTask() got %#v, want account %d", got, acc2.ID)
	}
}

func TestParseMP4DimensionsReadsRealTrackSize(t *testing.T) {
	raw, err := hex.DecodeString("" +
		"000000186674797069736f6d0000020069736f6d69736f32" +
		"0000006c6d6f6f76000000647472616b0000005c746b6864" +
		"000000070000000000000000000000010000000000000000" +
		"000000000000000000000000000100000000000000000000" +
		"00000000000190000002e000")
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	width, height, err := parseMP4Dimensions(raw)
	if err != nil {
		t.Fatalf("parseMP4Dimensions error: %v", err)
	}
	if width != 400 || height != 736 {
		t.Fatalf("expected 400x736, got %dx%d", width, height)
	}
}

func TestRetryableProviderErrorTLSHandshakeEOF(t *testing.T) {
	err := errors.New(`Post "https://grok.com/rest/app-chat/upload-file": tls handshake to grok.com failed: EOF`)
	if !retryableProviderError(err) {
		t.Fatal("expected TLS handshake EOF failure to be retryable")
	}
}

func TestRetryableProviderErrorZeroImageNotRetryable(t *testing.T) {
	err := errors.New("gpt image2 returned 0 image")
	if retryableProviderError(err) {
		t.Fatal("zero image should fail fast without account rotation")
	}
	if isGPTCodexTransientError(err) {
		t.Fatal("zero image should not trigger adobe fallback")
	}
}

// 复现现场上游日志里的 "gpt image2 web bootstrap 403: <html>..." 案例：
// 这种错误必须进入换号重试，而不是 failTask。
func TestRetryableProviderErrorGPTImage2WebBootstrap403(t *testing.T) {
	cases := map[string]string{
		"bootstrap_403_with_html_body": `gpt image2 web bootstrap 403: <html><head><meta name="viewport" content="width=device-width, initial-scale=1" /><style global>body{font-family:Arial,Helvetica,sans-serif}.container{align-items:center;`,
		"requirements_401":             `gpt image2 web requirements 401: {"detail":"unauthorized"}`,
		"prepare_403":                  `gpt image2 web prepare 403: forbidden`,
		"conversation_429":             `gpt image2 web conversation 429: rate limited`,
		"poll_502":                     `gpt image2 web poll 502: bad gateway`,
		"upload_blob_503":              `gpt image2 web upload blob 503: service unavailable`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			err := errors.New(raw)
			if !retryableProviderError(err) {
				t.Fatalf("expected %q to be retryable so the dispatcher swaps account on attempt+1", name)
			}
		})
	}
}

func TestIsGPTWebChallengeOnlyMatchesGPTWebMessages(t *testing.T) {
	if isGPTWebChallenge("usage_limit_reached") {
		t.Fatal("usage_limit_reached must not be classified as gpt web challenge")
	}
	if isGPTWebChallenge("grok upload http 403: forbidden") {
		t.Fatal("grok 403 must not be classified as gpt web challenge")
	}
	if !isGPTWebChallenge("gpt image2 web bootstrap 403: cf challenge body") {
		t.Fatal("expected gpt image2 web bootstrap 403 to be classified as challenge")
	}
}

// 上游 4xx 客户端错误（请求参数错）不应该被误判为可重试，
// 否则同一个 task 反复换号都注定失败。
func TestRetryableProviderErrorGPTImage2WebBadRequestNotRetryable(t *testing.T) {
	err := errors.New(`gpt image2 web requirements 400: {"error":"invalid_request"}`)
	if retryableProviderError(err) {
		t.Fatal("expected 400 from gpt image2 web step to be non-retryable")
	}
}

// 复现现场上游日志里的 Adobe Firefly 4K user_not_entitled 案例：
// 调度器必须：
//  1. 把它当成"可重试"（说不定其他 Adobe 号有此权益）；
//  2. 走 transient 路径，不冷却该号（该号 1K/2K 还能正常用）；
//  3. 用户面向消息能明确告诉是"权益未开通"，而不是"403 / 鉴权失败"那样含糊。
func TestAdobeNotEntitledClassification(t *testing.T) {
	err := firefly.ClassifyError(403,
		map[string]string{"x-access-error": "user_not_entitled"},
		`{"error_code":"access_error","message":"Unauthorized to perform request."}`,
	)

	var entitledErr *firefly.NotEntitledError
	if !errors.As(err, &entitledErr) {
		t.Fatalf("expected NotEntitledError, got %T: %v", err, err)
	}
	if entitledErr.AccessError != "user_not_entitled" {
		t.Fatalf("expected access_error preserved, got %q", entitledErr.AccessError)
	}

	if !isAdobeRetryableError(err) {
		t.Fatal("NotEntitledError must remain retryable so dispatcher can swap to other accounts")
	}
	if !isAdobeNotEntitledError(err) {
		t.Fatal("expected isAdobeNotEntitledError to detect the NotEntitledError specifically")
	}
	if isAdobeNonRetryableError(err) {
		t.Fatal("NotEntitledError must not be treated as Adobe non-retryable (would fail before any account swap)")
	}
}

// AuthError 与 NotEntitledError 必须严格区分：
//   - AuthError 走 markProviderFailed → 累计 error_count → 触发 cooldown
//   - NotEntitledError 走 MarkTransientFailed → 不冷却（该号其他档位仍可用）
//
// isAdobeNotEntitledError 不应把普通 403/401 AuthError 误判为 not entitled。
func TestAdobeAuthErrorNotMisclassifiedAsNotEntitled(t *testing.T) {
	err := firefly.ClassifyError(403,
		map[string]string{},
		`{"error_code":"access_error","message":"Unauthorized to perform request."}`,
	)
	if isAdobeNotEntitledError(err) {
		t.Fatal("plain 403 AuthError must not be classified as not-entitled")
	}
	var authErr *firefly.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T", err)
	}
}

// 用户面向消息：x-access-error=user_not_entitled 时应该
// 提示「权益未开通，请改用 1K/2K」，而不是泛泛的「403 上游权限限制」。
func TestUserFacingMessageForNotEntitled(t *testing.T) {
	cases := []string{
		`adobe submit 403: user_not_entitled`,
		`not entitled 403: Adobe 账号未开通该能力（例如 4K 出图权益）`,
	}
	for _, raw := range cases {
		got := userFacingGenerationError(raw)
		if !strings.Contains(got, "权益未开通") && !strings.Contains(got, "改用 1K / 2K") {
			t.Fatalf("expected sanitized message to mention entitlement / 1K-2K fallback for %q, got %q", raw, got)
		}
	}
}

// adobeResolutionTier 必须能从前端常见的几种 params 形状里抽出标准档位。
// 默认值 2K 与 firefly.payloads.sizeFromRatio 保持一致，避免 4K 学习
// 落到错误的 key 上。
func TestAdobeResolutionTier(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"front_4K", map[string]any{"resolution": "4K"}, "4K"},
		{"lower_4k", map[string]any{"resolution": "4k"}, "4K"},
		{"alias_size_tier", map[string]any{"size_tier": "2K"}, "2K"},
		{"default_when_empty", map[string]any{}, "2K"},
		{"resolution_wins_over_size_tier", map[string]any{"resolution": "1K", "size_tier": "4K"}, "1K"},
		{"numeric_string", map[string]any{"resolution": "1"}, "1K"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adobeResolutionTier(tc.in)
			if got != tc.want {
				t.Fatalf("adobeResolutionTier(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// 没有 OAuthMeta 的账号 → 默认乐观地认为它"全档位都能跑"，让它至少试一次。
// 这是必须保留的语义：新导入的 Premium 号必须立刻被 4K 任务选中，
// 不能因为 meta 为空就跳过它。
func TestAccountSupportsAdobeTierDefaultsToTrue(t *testing.T) {
	acc := &model.Account{Provider: model.ProviderADOBE}
	for _, tier := range []string{"1K", "2K", "4K"} {
		if !accountSupportsAdobeTier(acc, tier) {
			t.Fatalf("default account must support tier %s when meta is empty", tier)
		}
	}
}

// 一旦学到 no_4k = true 且 checked_at 在 TTL 内 → 应该被跳过；
// 同一个号 1K / 2K 仍然能用（这是与冷却 / Auth 失败的根本区别）。
func TestAccountSupportsAdobeTierSkipsRecentlyMarkedTier(t *testing.T) {
	now := time.Now().UTC().Unix()
	meta := map[string]any{
		"no_4k":            true,
		"no_4k_checked_at": now,
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	acc := &model.Account{Provider: model.ProviderADOBE, OAuthMeta: &s}

	if accountSupportsAdobeTier(acc, "4K") {
		t.Fatal("expected 4K to be filtered out when no_4k=true within TTL")
	}
	if !accountSupportsAdobeTier(acc, "1K") {
		t.Fatal("1K must still be allowed even if 4K is filtered out")
	}
	if !accountSupportsAdobeTier(acc, "2K") {
		t.Fatal("2K must still be allowed even if 4K is filtered out")
	}
}

// 7 天 TTL 一旦过期 → 允许再次探测一次：运营可能给老号充值升级了 Premium。
func TestAccountSupportsAdobeTierExpiresAfterTTL(t *testing.T) {
	stale := time.Now().UTC().Add(-adobeEntitlementTTL - time.Hour).Unix()
	meta := map[string]any{
		"no_4k":            true,
		"no_4k_checked_at": stale,
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	acc := &model.Account{Provider: model.ProviderADOBE, OAuthMeta: &s}

	if !accountSupportsAdobeTier(acc, "4K") {
		t.Fatal("expected expired no_4k mark to allow re-probing the account")
	}
}

// 没记 checked_at（手动改 DB 写了 no_4k=true 但忘了 timestamp）→ 也按"过期"处理，
// 这是宽容路径，避免一次手工改库导致整个号永远被跳过。
func TestAccountSupportsAdobeTierMissingTimestampActsExpired(t *testing.T) {
	meta := map[string]any{"no_4k": true}
	raw, _ := json.Marshal(meta)
	s := string(raw)
	acc := &model.Account{Provider: model.ProviderADOBE, OAuthMeta: &s}
	if !accountSupportsAdobeTier(acc, "4K") {
		t.Fatal("expected missing timestamp to be treated as expired (lenient)")
	}
}

// ok_<tier> 比 no_<tier> 更晚 → 调度判定应该回到"允许使用"，避免一次旧的
// not_entitled 永远把后来升级了 Premium 的号拒之门外。
func TestAccountSupportsAdobeTierOKOverridesOlderBlocked(t *testing.T) {
	now := time.Now().UTC().Unix()
	meta := map[string]any{
		"no_4k":            true,
		"no_4k_checked_at": now - 3600, // 1 小时前
		"ok_4k":            true,
		"ok_4k_checked_at": now, // 刚刚成功
	}
	raw, _ := json.Marshal(meta)
	s := string(raw)
	acc := &model.Account{Provider: model.ProviderADOBE, OAuthMeta: &s}
	if !accountSupportsAdobeTier(acc, "4K") {
		t.Fatal("expected newer ok_4k to override older no_4k")
	}
}

// 反过来：no 比 ok 更晚（账号被吊销了）→ 仍然按"跳过"对待。
func TestAccountSupportsAdobeTierOlderOKDoesNotResurrectBlocked(t *testing.T) {
	now := time.Now().UTC().Unix()
	meta := map[string]any{
		"no_4k":            true,
		"no_4k_checked_at": now,
		"ok_4k":            true,
		"ok_4k_checked_at": now - 3600,
	}
	raw, _ := json.Marshal(meta)
	s := string(raw)
	acc := &model.Account{Provider: model.ProviderADOBE, OAuthMeta: &s}
	if accountSupportsAdobeTier(acc, "4K") {
		t.Fatal("expected newer no_4k to keep account filtered out")
	}
}

func TestShouldPreferGrokProxyPool(t *testing.T) {
	if !shouldPreferGrokProxyPool(&model.Account{Provider: model.ProviderGROK}) {
		t.Fatal("expected grok account to prefer rotating proxy pool")
	}
	if shouldPreferGrokProxyPool(&model.Account{Provider: model.ProviderGPT}) {
		t.Fatal("expected non-grok account to keep existing proxy order")
	}
}

func TestGrokProxyPoolIDsRotateHTTPPool(t *testing.T) {
	grokProxyPoolCursor = 0
	items := []*model.Proxy{
		{ID: 25, Protocol: model.ProxyProtoHTTP, Status: model.ProxyStatusEnabled},
		{ID: 47, Protocol: model.ProxyProtoSOCKS5, Status: model.ProxyStatusEnabled},
		{ID: 26, Protocol: model.ProxyProtoHTTP, Status: model.ProxyStatusEnabled},
	}

	first := grokProxyPoolIDs(&model.Account{ID: 0, Provider: model.ProviderGROK}, items)
	second := grokProxyPoolIDs(&model.Account{ID: 0, Provider: model.ProviderGROK}, items)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("expected two http proxies, got %v and %v", first, second)
	}
	if first[0] != 25 || first[1] != 26 {
		t.Fatalf("unexpected first rotation order: %v", first)
	}
	if second[0] != 26 || second[1] != 25 {
		t.Fatalf("unexpected second rotation order: %v", second)
	}
}
