package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/pkg/logger"
)

const defaultGrokCFStatePath = "/app/storage/grok_cf.json"

// grokCFProxySolveAttempts 配置了代理出口时，CF 挑战经代理求解的重试次数。
// 住宅/动态代理较慢且偶发拿不到 cf cookie，多试几次显著提高成功率；
// 关键：配置了代理就绝不回退直连——数据中心直连必被 Grok 业务端点判机器人 (403 code=7)。
const grokCFProxySolveAttempts = 3

type GrokCFRefreshService struct {
	cfg      *SystemConfigService
	proxySvc *ProxyService
	client   *http.Client
}

type grokCFState struct {
	Cookies     string `json:"cookies"`
	CFClearance string `json:"cf_clearance"`
	UserAgent   string `json:"user_agent"`
	Browser     string `json:"browser"`
	ProxyURL    string `json:"proxy_url,omitempty"`
	UpdatedAt   int64  `json:"updated_at"`

	// StatsigFingerprintHex 是 x-statsig-id 反爬签名所需的 48 字节指纹（hex），
	// 来自 FlareSolverr 渲染出的 grok.com 首页 <meta name="grok-site-verification">。
	// 它与本次 solve 的 IP/会话绑定，因此和 cf_clearance / x-challenge / x-signature 一起刷新，
	// 业务请求直接读它当指纹（见 provider/grok 的 grokFingerprint pin 分支），不必每请求再抓首页。
	StatsigFingerprintHex string `json:"statsig_fingerprint_hex,omitempty"`
}

// grokSiteVerifReA/B 从 FlareSolverr 渲染出的首页 HTML 抓 grok-site-verification meta（容忍属性顺序）。
var (
	grokSiteVerifReA = regexp.MustCompile(`(?i)name=["']grok-site-verification["'][^>]*content=["']([^"']+)["']`)
	grokSiteVerifReB = regexp.MustCompile(`(?i)content=["']([^"']+)["'][^>]*name=["']grok-site-verification["']`)
)

// parseGrokSiteVerificationHex 从首页 HTML 解出 grok-site-verification，base64 解码为 48 字节并返回其 hex。
// 解析失败返回空串（调用方保留旧指纹）。
func parseGrokSiteVerificationHex(html string) string {
	var v string
	if m := grokSiteVerifReA.FindStringSubmatch(html); len(m) == 2 {
		v = m[1]
	} else if m := grokSiteVerifReB.FindStringSubmatch(html); len(m) == 2 {
		v = m[1]
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == 48 {
		return hex.EncodeToString(b)
	}
	if b, err := base64.RawStdEncoding.DecodeString(v); err == nil && len(b) == 48 {
		return hex.EncodeToString(b)
	}
	return ""
}

func NewGrokCFRefreshService(cfg *SystemConfigService, proxySvc *ProxyService) *GrokCFRefreshService {
	return &GrokCFRefreshService{
		cfg:      cfg,
		proxySvc: proxySvc,
		client:   &http.Client{Timeout: 5 * time.Minute},
	}
}

func (s *GrokCFRefreshService) Start(ctx context.Context) {
	if s == nil || s.cfg == nil {
		return
	}
	go s.loop(ctx)
}

func (s *GrokCFRefreshService) loop(ctx context.Context) {
	s.refreshOnce(ctx)
	ticker := time.NewTicker(s.cfg.GrokCFRefreshInterval(ctx))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshOnce(ctx)
			ticker.Reset(s.cfg.GrokCFRefreshInterval(ctx))
		}
	}
}

func (s *GrokCFRefreshService) refreshOnce(parent context.Context) {
	if !s.cfg.GrokCFEnabled(parent) {
		return
	}
	solverURL := s.cfg.GrokCFSolverURL(parent)
	if solverURL == "" {
		return
	}
	timeout := s.cfg.GrokCFTimeout(parent)

	// 先解析代理出口（用 parent ctx 读配置），再据此决定整体预算：
	// 配了代理要为多次重试留足时间（住宅代理单次可达 40s+），否则会被 ctx 提前取消。
	proxyURL, err := s.globalProxyURL(parent)
	if err != nil {
		s.recordError(parent, fmt.Sprintf("resolve proxy: %v", err))
		return
	}
	budget := timeout + 15*time.Second
	if strings.TrimSpace(proxyURL) != "" {
		budget = timeout*time.Duration(grokCFProxySolveAttempts) + 20*time.Second
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()

	state, err := s.solveWithFallback(ctx, solverURL, proxyURL, timeout)
	if err != nil {
		s.recordError(ctx, err.Error())
		return
	}
	if state.Cookies == "" && state.CFClearance == "" {
		s.recordError(ctx, "flaresolverr returned no cf cookies")
		return
	}
	// 若本次没能解析出指纹（极少数情况：渲染页缺 meta），保留上一次的指纹，
	// 避免业务请求回落到失效的内置兜底指纹而被反爬拒绝。
	if state.StatsigFingerprintHex == "" {
		if prev := readGrokCFStateFingerprint(); prev != "" {
			state.StatsigFingerprintHex = prev
		}
	}
	if err := writeGrokCFState(state); err != nil {
		s.recordError(ctx, fmt.Sprintf("write state: %v", err))
		return
	}
	_ = s.cfg.UpsertMany(ctx, map[string]any{
		SettingGrokCFCookies:       state.Cookies,
		SettingGrokCFClearance:     state.CFClearance,
		SettingGrokCFUserAgent:     state.UserAgent,
		SettingGrokCFBrowser:       state.Browser,
		SettingGrokCFLastRefreshAt: state.UpdatedAt,
		SettingGrokCFLastError:     "",
	}, 0)
	logger.L().Info("grok cf refreshed",
		zap.Bool("has_clearance", state.CFClearance != ""),
		zap.Bool("has_proxy", state.ProxyURL != ""),
		zap.String("browser", state.Browser),
		zap.Bool("has_fingerprint", state.StatsigFingerprintHex != ""),
		zap.String("solve_proxy", sanitizeProxyForLog(state.ProxyURL)),
	)
}

// readGrokCFStateFingerprint 读出当前 grok_cf.json 里已有的指纹（用于刷新时兜底保留）。
func readGrokCFStateFingerprint() string {
	raw, err := os.ReadFile(grokCFStatePath())
	if err != nil {
		return ""
	}
	var st grokCFState
	if json.Unmarshal(raw, &st) != nil {
		return ""
	}
	return strings.TrimSpace(st.StatsigFingerprintHex)
}

// grokCFSolveEgress 返回最近一次 CF 挑战是用哪条出口（代理 URL，空串=直连）解出来的，
// 以及该状态是否可用（新鲜且确实拿到了 cf_clearance）。
//
// 这是 Grok 反爬能跑通的关键：x-statsig-id 指纹、cf_clearance、x-challenge、x-signature
// 都和「解 CF 挑战时的那个 IP」绑定。业务请求必须从同一出口发出，否则 xAI 判定为机器人 (HTTP 403 code=7)。
// 因此 Grok 的代理选择不再走「号池轮换」，而是严格对齐 CF 刷新用过的那条出口。
func grokCFSolveEgress() (proxyURL string, ok bool) {
	raw, err := os.ReadFile(grokCFStatePath())
	if err != nil {
		return "", false
	}
	var st grokCFState
	if json.Unmarshal(raw, &st) != nil {
		return "", false
	}
	if strings.TrimSpace(st.CFClearance) == "" && strings.TrimSpace(st.Cookies) == "" {
		return "", false
	}
	// 太旧的状态（> 1h）说明 CF 刷新早已停摆，此时对齐已无意义，交回上层用号池逻辑兜底。
	if st.UpdatedAt > 0 && time.Since(time.Unix(st.UpdatedAt, 0)) > time.Hour {
		return "", false
	}
	return strings.TrimSpace(st.ProxyURL), true
}

// sanitizeProxyForLog 去掉代理 URL 里的账号密码，只留 scheme://host:port，避免泄漏到日志。
func sanitizeProxyForLog(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "direct"
	}
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		if scheme := strings.Index(raw, "://"); scheme >= 0 && scheme < at {
			return raw[:scheme+3] + raw[at+1:]
		}
		return raw[at+1:]
	}
	return raw
}

func (s *GrokCFRefreshService) globalProxyURL(ctx context.Context) (string, error) {
	if s.proxySvc == nil || !s.cfg.GlobalProxyEnabled(ctx) {
		return "", nil
	}
	pid := s.cfg.GlobalProxyID(ctx)
	if pid == 0 {
		return "", nil
	}
	p, err := s.proxySvc.GetByID(ctx, pid)
	if err != nil || p == nil || p.Status != model.ProxyStatusEnabled {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(p.Protocol)) {
	case model.ProxyProtoHTTP, model.ProxyProtoHTTPS:
	default:
		logger.L().Info("skip global proxy for grok cf refresh",
			zap.String("protocol", p.Protocol),
			zap.Uint64("proxy_id", p.ID),
		)
		return "", nil
	}
	u, err := s.proxySvc.BuildURL(p)
	if err != nil || u == nil {
		return "", err
	}
	return u.String(), nil
}

func (s *GrokCFRefreshService) solveWithFallback(ctx context.Context, solverURL, proxyURL string, timeout time.Duration) (*grokCFState, error) {
	if strings.TrimSpace(proxyURL) == "" {
		// 没有配置代理出口：保持原直连行为（仅适用于本就允许直连的部署）。
		return s.solve(ctx, solverURL, "", timeout)
	}

	// 配置了代理出口：Grok 反爬要求「业务请求出口」与「CF 解挑战出口」严格一致。
	// 数据中心直连即使能拿到 cf_clearance，业务端点 (conversations/new) 仍会被判机器人 (403 code=7)。
	// 因此这里绝不回退直连，只在代理上重试；全部失败则报错并保留上一份（代理绑定的）状态。
	var lastErr error
	for attempt := 0; attempt < grokCFProxySolveAttempts; attempt++ {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		state, err := s.solve(ctx, solverURL, proxyURL, timeout)
		if err == nil && hasUsableGrokCFState(state) {
			return state, nil
		}
		if err != nil {
			lastErr = err
			logger.L().Warn("grok cf refresh via proxy failed; retrying (no direct fallback)",
				zap.Int("attempt", attempt+1),
				zap.String("proxy_url", sanitizeProxyForLog(proxyURL)),
				zap.String("error", err.Error()),
			)
		} else {
			lastErr = fmt.Errorf("flaresolverr returned no usable cookies")
			logger.L().Warn("grok cf refresh via proxy returned no cookies; retrying (no direct fallback)",
				zap.Int("attempt", attempt+1),
				zap.String("proxy_url", sanitizeProxyForLog(proxyURL)),
			)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("flaresolverr via proxy failed")
	}
	return nil, fmt.Errorf("grok cf refresh requires proxy egress but proxy solve failed after %d attempts: %w", grokCFProxySolveAttempts, lastErr)
}

func (s *GrokCFRefreshService) solve(ctx context.Context, solverURL, proxyURL string, timeout time.Duration) (*grokCFState, error) {
	reqBody := map[string]any{
		"cmd":        "request.get",
		"url":        "https://grok.com",
		"maxTimeout": int(timeout / time.Millisecond),
	}
	if proxyURL != "" {
		reqBody["proxy"] = buildFlareProxyObject(proxyURL)
	}
	rawReq, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(solverURL, "/")+"/v1", bytes.NewReader(rawReq))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("flaresolverr HTTP %d: %s", resp.StatusCode, snippetString(raw, 300))
	}
	var obj struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			UserAgent string `json:"userAgent"`
			Response  string `json:"response"`
			Cookies   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"cookies"`
		} `json:"solution"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("decode flaresolverr: %w", err)
	}
	if !strings.EqualFold(obj.Status, "ok") {
		return nil, fmt.Errorf("flaresolverr status %q: %s", obj.Status, obj.Message)
	}
	parts := make([]string, 0, len(obj.Solution.Cookies))
	cf := ""
	for _, c := range obj.Solution.Cookies {
		if strings.TrimSpace(c.Name) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(c.Name)+"="+strings.TrimSpace(c.Value))
		if c.Name == "cf_clearance" {
			cf = strings.TrimSpace(c.Value)
		}
	}
	fpHex := parseGrokSiteVerificationHex(obj.Solution.Response)
	return &grokCFState{
		Cookies:               strings.Join(parts, "; "),
		CFClearance:           cf,
		UserAgent:             strings.TrimSpace(obj.Solution.UserAgent),
		Browser:               browserFromUA(obj.Solution.UserAgent),
		ProxyURL:              proxyURL,
		UpdatedAt:             time.Now().Unix(),
		StatsigFingerprintHex: fpHex,
	}, nil
}

func (s *GrokCFRefreshService) recordError(ctx context.Context, msg string) {
	logger.L().Warn("grok cf refresh failed", zap.String("error", msg))
	_ = s.cfg.UpsertMany(ctx, map[string]any{
		SettingGrokCFLastError: msg,
	}, 0)
}

func grokCFStatePath() string {
	if v := strings.TrimSpace(os.Getenv("KLEIN_GROK_CF_STATE_PATH")); v != "" {
		return v
	}
	return defaultGrokCFStatePath
}

// buildFlareProxyObject 把 user:pass@host 形式的代理 URL 拆成 FlareSolverr 需要的
// {url, username, password} 结构。
//
// 关键：FlareSolverr 底层的 headless Chrome 不会从代理 URL 里解析 user:pass 认证段，
// 必须把账号密码作为独立字段下发，否则代理认证失败（407），页面根本加载不出来，
// 表现为 ~1s 秒回、0 cookie、"Challenge not detected"，CF 刷新随即回退直连 → 业务被绑死直连出口。
func buildFlareProxyObject(proxyURL string) map[string]any {
	obj := map[string]any{"url": proxyURL}
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil {
		return obj
	}
	user := u.User.Username()
	if user == "" {
		return obj
	}
	pass, _ := u.User.Password()
	stripped := *u
	stripped.User = nil
	obj["url"] = stripped.String()
	obj["username"] = user
	obj["password"] = pass
	return obj
}

func hasUsableGrokCFState(state *grokCFState) bool {
	return state != nil && (strings.TrimSpace(state.Cookies) != "" || strings.TrimSpace(state.CFClearance) != "")
}

func writeGrokCFState(state *grokCFState) error {
	path := grokCFStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func browserFromUA(ua string) string {
	ua = strings.ToLower(ua)
	if strings.Contains(ua, "chrome/") || strings.Contains(ua, "chromium/") {
		return "chrome"
	}
	if strings.Contains(ua, "firefox/") {
		return "firefox"
	}
	return ""
}

func snippetString(raw []byte, limit int) string {
	s := strings.TrimSpace(string(raw))
	if limit > 0 && len(s) > limit {
		return s[:limit] + "...(truncated)"
	}
	return s
}
