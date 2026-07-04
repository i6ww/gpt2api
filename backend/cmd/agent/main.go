// Command agent  ── 边缘节点 / 远端执行节点
//
// 职责：
//
//   1. 用 KLEIN_NODE_TOKEN（bootstrap）跟主控握手，换永久 hmac secret
//   2. 周期性心跳（POST /api/v1/cluster/heartbeat）
//   3. 周期性 lease（POST /api/v1/cluster/lease），并发执行 provider.Generate
//   4. 把每张图 / 每段视频拉到本地 storage_root/generated/YYYY/MM/DD/<task>_<seq>.<ext>
//   5. 完成后 POST /api/v1/cluster/result（成功 / 失败）
//   6. 本地 HTTP 服务：
//        GET /healthz                探活
//        GET /d/:ticket?path=<rel>   下载入口；验签后用 X-Accel-Redirect 让 nginx 直接 sendfile
//
// 详见 deploy/docs/AGENT_DEPLOY.md。
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/provider/factory"
)

// ── 全局配置 ───────────────────────────────────────────────

type Config struct {
	NodeID         string
	BootstrapToken string
	ControlURL     string
	PublicURL      string
	StorageRoot    string
	StateDir       string // 持久化 hmac secret 的目录；缺省 /var/klein/state
	BindAddr       string
	HeartbeatSec   int
	LeaseSec       int
	MaxConcurrency int
	TicketTTLSec   int
	Hostname       string
	Version        string
}

func loadConfig() (*Config, error) {
	c := &Config{
		NodeID:         strings.TrimSpace(os.Getenv("KLEIN_NODE_ID")),
		BootstrapToken: strings.TrimSpace(os.Getenv("KLEIN_NODE_TOKEN")),
		ControlURL:     strings.TrimSpace(os.Getenv("KLEIN_CONTROL_URL")),
		PublicURL:      strings.TrimSpace(os.Getenv("KLEIN_NODE_PUBLIC_URL")),
		StorageRoot:    strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT")),
		StateDir:       strings.TrimSpace(os.Getenv("KLEIN_STATE_DIR")),
		BindAddr:       strings.TrimSpace(os.Getenv("KLEIN_AGENT_BIND")),
		HeartbeatSec:   atoi(os.Getenv("KLEIN_HEARTBEAT_SEC"), 10),
		LeaseSec:       atoi(os.Getenv("KLEIN_LEASE_INTERVAL_SEC"), 2),
		MaxConcurrency: atoi(os.Getenv("KLEIN_MAX_CONCURRENCY"), 4),
		TicketTTLSec:   atoi(os.Getenv("KLEIN_TICKET_TTL_SEC"), 600),
		Version:        "agent/0.1.0",
	}
	if c.ControlURL == "" {
		return nil, errors.New("KLEIN_CONTROL_URL required")
	}
	// 注：BootstrapToken 不再是硬性必要。如果本地 state 已经持久化过 secret，
	// 可在没有 KLEIN_NODE_TOKEN 的情况下直接重启复用，省一次 admin reissue。
	if c.StorageRoot == "" {
		c.StorageRoot = "/var/klein/storage/public"
	}
	if c.StateDir == "" {
		c.StateDir = "/var/klein/state"
	}
	if c.BindAddr == "" {
		c.BindAddr = ":18080"
	}
	c.ControlURL = strings.TrimRight(c.ControlURL, "/")
	hn, _ := os.Hostname()
	c.Hostname = hn
	return c, nil
}

func atoi(raw string, dflt int) int {
	if raw == "" {
		return dflt
	}
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		return v
	}
	return dflt
}

// ── 主控会话 ───────────────────────────────────────────────

type Session struct {
	cfg            *Config
	hc             *http.Client
	NodeID         string
	Secret         []byte
	ProviderScope  []string
	MaxConcurrency int
	HeartbeatSec   int
	LeaseSec       int
	StorageRoot    string

	inflight atomic.Int64
	logger   *jsonLogger
}

type jsonLogger struct{}

func (*jsonLogger) infof(format string, args ...any)  { fmt.Fprintf(os.Stdout, "[INFO ] "+format+"\n", args...) }
func (*jsonLogger) warnf(format string, args ...any)  { fmt.Fprintf(os.Stdout, "[WARN ] "+format+"\n", args...) }
func (*jsonLogger) errorf(format string, args ...any) { fmt.Fprintf(os.Stderr, "[ERROR] "+format+"\n", args...) }

func NewSession(cfg *Config) *Session {
	return &Session{
		cfg: cfg,
		hc: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        16,
				IdleConnTimeout:     60 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		},
		logger: &jsonLogger{},
	}
}

// stateFilePath 返回 hmac secret 持久化路径。
func (s *Session) stateFilePath() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	dir := strings.TrimSpace(s.cfg.StateDir)
	if dir == "" {
		dir = "/var/klein/state"
	}
	return filepath.Join(dir, "agent-state.json")
}

// agentState 写入磁盘的 session snapshot。
// secret 用 base64url 编码避免 JSON 转义；目录权限 0o700、文件权限 0o600。
type agentState struct {
	NodeID         string   `json:"node_id"`
	HMACSecret     string   `json:"hmac_secret"`
	ProviderScope  []string `json:"provider_scope"`
	MaxConcurrency int      `json:"max_concurrency"`
	HeartbeatSec   int      `json:"heartbeat_sec"`
	LeaseSec       int      `json:"lease_sec"`
	StorageRoot    string   `json:"storage_root"`
	ControlURL     string   `json:"control_url"`
	SavedAt        int64    `json:"saved_at_unix"`
}

// LoadPersistedState 启动期尝试复用上次握手结果；返回 true 表示成功。
// 校验：控制面 URL 必须匹配；KLEIN_NODE_ID（若 env 给了）也必须匹配。
func (s *Session) LoadPersistedState() bool {
	p := s.stateFilePath()
	if p == "" {
		return false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var st agentState
	if err := json.Unmarshal(raw, &st); err != nil {
		s.logger.warnf("agent-state.json decode: %v", err)
		return false
	}
	if st.ControlURL != "" && st.ControlURL != s.cfg.ControlURL {
		s.logger.warnf("agent-state.json control_url mismatch (saved=%s, env=%s) → ignoring", st.ControlURL, s.cfg.ControlURL)
		return false
	}
	if s.cfg.NodeID != "" && st.NodeID != s.cfg.NodeID {
		s.logger.warnf("agent-state.json node_id mismatch (saved=%s, env=%s) → ignoring", st.NodeID, s.cfg.NodeID)
		return false
	}
	secret, err := base64.RawURLEncoding.DecodeString(st.HMACSecret)
	if err != nil || len(secret) < 16 {
		return false
	}
	s.NodeID = st.NodeID
	s.Secret = secret
	s.ProviderScope = st.ProviderScope
	s.MaxConcurrency = st.MaxConcurrency
	s.HeartbeatSec = st.HeartbeatSec
	s.LeaseSec = st.LeaseSec
	s.StorageRoot = st.StorageRoot
	if s.MaxConcurrency <= 0 {
		s.MaxConcurrency = s.cfg.MaxConcurrency
	}
	if s.HeartbeatSec <= 0 {
		s.HeartbeatSec = s.cfg.HeartbeatSec
	}
	if s.LeaseSec <= 0 {
		s.LeaseSec = s.cfg.LeaseSec
	}
	if s.StorageRoot == "" {
		s.StorageRoot = s.cfg.StorageRoot
	}
	if err := os.MkdirAll(s.StorageRoot, 0o755); err != nil {
		s.logger.warnf("mkdir storage from state: %v", err)
	}
	s.logger.infof("session restored from state node=%s scope=%v conc=%d", s.NodeID, s.ProviderScope, s.MaxConcurrency)
	return true
}

// persistState 把当前会话写盘；忽略错误（不致命）。
func (s *Session) persistState() {
	p := s.stateFilePath()
	if p == "" || len(s.Secret) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		s.logger.warnf("state dir: %v", err)
		return
	}
	st := agentState{
		NodeID:         s.NodeID,
		HMACSecret:     base64.RawURLEncoding.EncodeToString(s.Secret),
		ProviderScope:  s.ProviderScope,
		MaxConcurrency: s.MaxConcurrency,
		HeartbeatSec:   s.HeartbeatSec,
		LeaseSec:       s.LeaseSec,
		StorageRoot:    s.StorageRoot,
		ControlURL:     s.cfg.ControlURL,
		SavedAt:        time.Now().Unix(),
	}
	buf, err := json.MarshalIndent(&st, "", "  ")
	if err != nil {
		return
	}
	// 原子写：tmp + rename
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		s.logger.warnf("write state tmp: %v", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		s.logger.warnf("rename state: %v", err)
		return
	}
}

// HandshakeWithBackoff 包了一层指数退避：1s → 2s → 4s → 8s → 16s → 30s（上限），
// 总等待不超过 ctx 的截止时间；ctx 取消立即返回。
//
// 与 main 的 os.Exit 兼容：仅在 ctx.Done 时返回最近一次错误，调用方决定退出。
func (s *Session) HandshakeWithBackoff(ctx context.Context) error {
	if s.cfg.BootstrapToken == "" {
		return errors.New("no persisted state and KLEIN_NODE_TOKEN missing; nothing to handshake with")
	}
	var lastErr error
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.Handshake(hctx)
		cancel()
		if err == nil {
			s.persistState()
			return nil
		}
		lastErr = err
		s.logger.warnf("handshake attempt %d failed: %v; sleep=%s", attempt, err, backoff)
		select {
		case <-ctx.Done():
			return lastErr
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

// Handshake 用 bootstrap token 换永久 secret。
func (s *Session) Handshake(ctx context.Context) error {
	type req struct {
		Token     string `json:"token"`
		PublicURL string `json:"public_url"`
		Version   string `json:"version"`
		Hostname  string `json:"hostname"`
	}
	type resp struct {
		NodeID         string   `json:"node_id"`
		HMACSecret     string   `json:"hmac_secret"`
		ProviderScope  []string `json:"provider_scope"`
		MaxConcurrency int      `json:"max_concurrency"`
		HeartbeatSec   int      `json:"heartbeat_sec"`
		LeaseSec       int      `json:"lease_sec"`
		StorageRoot    string   `json:"storage_root"`
	}
	body, _ := json.Marshal(req{
		Token: s.cfg.BootstrapToken, PublicURL: s.cfg.PublicURL,
		Version: s.cfg.Version, Hostname: s.cfg.Hostname,
	})
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", s.cfg.ControlURL+"/admin/api/v1/cluster/handshake", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	r, err := s.hc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("handshake transport: %w", err)
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	if r.StatusCode/100 != 2 {
		return fmt.Errorf("handshake status %d: %s", r.StatusCode, truncate(string(raw), 256))
	}
	var env struct {
		Code int  `json:"code"`
		Data resp `json:"data"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("handshake decode: %w (body=%s)", err, truncate(string(raw), 256))
	}
	if env.Code != 0 {
		return fmt.Errorf("handshake code %d: %s", env.Code, env.Msg)
	}
	secret, err := base64.RawURLEncoding.DecodeString(env.Data.HMACSecret)
	if err != nil || len(secret) < 16 {
		return fmt.Errorf("handshake bad secret: %v", err)
	}
	s.NodeID = env.Data.NodeID
	s.Secret = secret
	s.ProviderScope = env.Data.ProviderScope
	s.MaxConcurrency = env.Data.MaxConcurrency
	s.HeartbeatSec = env.Data.HeartbeatSec
	s.LeaseSec = env.Data.LeaseSec
	s.StorageRoot = env.Data.StorageRoot
	if s.MaxConcurrency <= 0 {
		s.MaxConcurrency = s.cfg.MaxConcurrency
	}
	if s.HeartbeatSec <= 0 {
		s.HeartbeatSec = s.cfg.HeartbeatSec
	}
	if s.LeaseSec <= 0 {
		s.LeaseSec = s.cfg.LeaseSec
	}
	if s.StorageRoot == "" {
		s.StorageRoot = s.cfg.StorageRoot
	}
	if s.cfg.NodeID != "" && s.cfg.NodeID != s.NodeID {
		return fmt.Errorf("node id mismatch: env=%s, control=%s", s.cfg.NodeID, s.NodeID)
	}
	if err := os.MkdirAll(s.StorageRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir storage: %w", err)
	}
	s.logger.infof("handshake ok node=%s scope=%v conc=%d heartbeat=%ds lease=%ds storage=%s",
		s.NodeID, s.ProviderScope, s.MaxConcurrency, s.HeartbeatSec, s.LeaseSec, s.StorageRoot)
	return nil
}

// signedPost 走 ClusterHMAC 中间件的 POST。
func (s *Session) signedPost(ctx context.Context, urlPath string, body any, into any) error {
	bs, _ := json.Marshal(body)
	full := s.cfg.ControlURL + urlPath
	u, _ := url.Parse(full)
	signedPath := u.Path
	if u.RawQuery != "" {
		signedPath += "?" + u.RawQuery
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := sign(s.Secret, ts, "POST", signedPath, bs)
	req, _ := http.NewRequestWithContext(ctx, "POST", full, bytes.NewReader(bs))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Klein-Node", s.NodeID)
	req.Header.Set("X-Klein-Ts", ts)
	req.Header.Set("X-Klein-Sig", sig)
	r, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	if r.StatusCode/100 != 2 {
		return fmt.Errorf("status %d: %s", r.StatusCode, truncate(string(raw), 256))
	}
	if into == nil {
		return nil
	}
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode: %w (body=%s)", err, truncate(string(raw), 256))
	}
	if env.Code != 0 {
		return fmt.Errorf("code %d: %s", env.Code, env.Msg)
	}
	if len(env.Data) > 0 {
		return json.Unmarshal(env.Data, into)
	}
	return nil
}

func sign(secret []byte, ts, method, path string, body []byte) string {
	h := sha256.Sum256(body)
	canonical := ts + "\n" + strings.ToUpper(method) + "\n" + path + "\n" + hex.EncodeToString(h[:])
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// Heartbeat 心跳。
func (s *Session) Heartbeat(ctx context.Context) error {
	return s.signedPost(ctx, "/admin/api/v1/cluster/heartbeat", map[string]any{
		"inflight": s.inflight.Load(),
		"version":  s.cfg.Version,
	}, nil)
}

// ── lease & 任务执行 ──────────────────────────────────────────

type leasedTask struct {
	TaskID     string         `json:"task_id"`
	Kind       string         `json:"kind"`
	Mode       string         `json:"mode"`
	ModelCode  string         `json:"model_code"`
	Provider   string         `json:"provider"`
	Prompt     string         `json:"prompt"`
	NegPrompt  string         `json:"neg_prompt"`
	Params     map[string]any `json:"params"`
	RefAssets  []string       `json:"ref_assets"`
	Count      int            `json:"count"`
	UserID     uint64         `json:"user_id"`
	AccountID  uint64         `json:"account_id"`
	Credential string         `json:"credential"`
	BaseURL    string         `json:"base_url"`
	LeaseUntil int64          `json:"lease_until_unix"`
	CostPoints int64          `json:"cost_points"`
}

type leaseResp struct {
	Tasks []*leasedTask `json:"tasks"`
}

func (s *Session) Lease(ctx context.Context, max int) ([]*leasedTask, error) {
	out := leaseResp{}
	if err := s.signedPost(ctx, "/admin/api/v1/cluster/lease", map[string]any{
		"max":       max,
		"providers": s.ProviderScope,
	}, &out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

// resultRow 与 dto.ClusterResultRowItem 字段保持一致。
type resultRow struct {
	Seq        int            `json:"seq"`
	URL        string         `json:"url,omitempty"`
	RelPath    string         `json:"rel_path,omitempty"`
	ThumbRel   string         `json:"thumb_rel,omitempty"`
	Width      *int           `json:"width,omitempty"`
	Height     *int           `json:"height,omitempty"`
	DurationMs *int           `json:"duration_ms,omitempty"`
	SizeBytes  *int64         `json:"size_bytes,omitempty"`
	SHA256     string         `json:"sha256,omitempty"`
	MIME       string         `json:"mime,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

func (s *Session) Report(ctx context.Context, taskID string, status int8, errMsg string, cost int64, rows []resultRow) error {
	return s.signedPost(ctx, "/admin/api/v1/cluster/result", map[string]any{
		"task_id":     taskID,
		"status":      status,
		"error":       errMsg,
		"cost_points": cost,
		"results":     rows,
	}, nil)
}

// runTask 执行一条 lease 到的任务：跑 provider，落盘，发回 result。
func (s *Session) runTask(ctx context.Context, providers map[string]provider.Provider, t *leasedTask) {
	s.inflight.Add(1)
	defer s.inflight.Add(-1)

	prov, ok := providers[t.Provider]
	if !ok {
		_ = s.Report(ctx, t.TaskID, model.GenStatusFailed, "provider not built on agent: "+t.Provider, 0, nil)
		return
	}
	// 把 ref_assets 里的相对路径补成主控可达的绝对 URL；agent 不存历史素材。
	refs := make([]string, 0, len(t.RefAssets))
	for _, r := range t.RefAssets {
		if strings.HasPrefix(r, "/api/v1/gen/cached/") {
			refs = append(refs, s.cfg.ControlURL+r)
		} else {
			refs = append(refs, r)
		}
	}

	req := &provider.Request{
		TaskID:     t.TaskID,
		Kind:       provider.Kind(t.Kind),
		Mode:       provider.Mode(t.Mode),
		ModelCode:  t.ModelCode,
		Prompt:     t.Prompt,
		NegPrompt:  t.NegPrompt,
		Params:     t.Params,
		RefAssets:  refs,
		Count:      t.Count,
		Credential: t.Credential,
		BaseURL:    t.BaseURL,
	}
	// 简易 *model.Account 占位，部分 provider 通过 Account.Provider/Email 打日志。
	req.Account = &model.Account{
		ID:       t.AccountID,
		Name:     "agent-stub",
		Provider: t.Provider,
	}

	timeout := 5 * time.Minute
	if t.Kind == "video" {
		timeout = 15 * time.Minute
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := prov.Generate(pctx, req)
	if err != nil {
		s.logger.warnf("task %s failed: %v", t.TaskID, err)
		_ = s.Report(ctx, t.TaskID, model.GenStatusFailed, err.Error(), 0, nil)
		return
	}
	rows := make([]resultRow, 0, len(res.Assets))
	for i, a := range res.Assets {
		rel, sha, size, mime, ok := s.cacheAsset(ctx, t.TaskID, i, false, a.URL, a.Mime)
		if !ok {
			continue
		}
		row := resultRow{
			Seq:     i,
			RelPath: rel,
			SHA256:  sha,
			MIME:    mime,
		}
		if size > 0 {
			row.SizeBytes = &size
		}
		if a.Width > 0 {
			w := a.Width
			row.Width = &w
		}
		if a.Height > 0 {
			h := a.Height
			row.Height = &h
		}
		if a.DurationMs > 0 {
			d := a.DurationMs
			row.DurationMs = &d
		}
		if a.ThumbURL != "" {
			thumbRel, _, _, _, ok := s.cacheAsset(ctx, t.TaskID, i, true, a.ThumbURL, "image/jpeg")
			if ok {
				row.ThumbRel = thumbRel
			}
		}
		if len(a.Meta) > 0 {
			row.Meta = a.Meta
		}
		rows = append(rows, row)
	}
	if err := s.Report(ctx, t.TaskID, model.GenStatusSucceeded, "", 0, rows); err != nil {
		s.logger.warnf("task %s report failed: %v", t.TaskID, err)
	} else {
		s.logger.infof("task %s done assets=%d", t.TaskID, len(rows))
	}
}

// cacheAsset 把 provider 返回的 URL 拉到本地 storage_root，返回 rel_path / sha / size / mime。
func (s *Session) cacheAsset(ctx context.Context, taskID string, seq int, thumb bool, src, ctHint string) (string, string, int64, string, bool) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", "", 0, "", false
	}
	// data:url
	if strings.HasPrefix(src, "data:") {
		ct, payload, ok := strings.Cut(src, ",")
		if !ok || !strings.Contains(ct, ";base64") {
			return "", "", 0, "", false
		}
		mime := strings.TrimPrefix(ct, "data:")
		if i := strings.Index(mime, ";"); i > 0 {
			mime = mime[:i]
		}
		raw, err := base64.StdEncoding.DecodeString(payload)
		if err != nil || len(raw) == 0 {
			return "", "", 0, "", false
		}
		rel := relPathFor(taskID, seq, thumb, mime)
		dst := filepath.Join(s.StorageRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", "", 0, "", false
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return "", "", 0, "", false
		}
		sum := sha256.Sum256(raw)
		return rel, hex.EncodeToString(sum[:]), int64(len(raw)), mime, true
	}
	// http(s)://
	req, err := http.NewRequestWithContext(ctx, "GET", src, nil)
	if err != nil {
		return "", "", 0, "", false
	}
	r, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return "", "", 0, "", false
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return "", "", 0, "", false
	}
	mime := r.Header.Get("Content-Type")
	if mime == "" {
		mime = ctHint
	}
	rel := relPathFor(taskID, seq, thumb, mime)
	dst := filepath.Join(s.StorageRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", "", 0, "", false
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", "", 0, "", false
	}
	defer f.Close()
	hsh := sha256.New()
	tee := io.MultiWriter(f, hsh)
	n, err := io.Copy(tee, r.Body)
	if err != nil || n <= 0 {
		_ = os.Remove(dst)
		return "", "", 0, "", false
	}
	return rel, hex.EncodeToString(hsh.Sum(nil)), n, mime, true
}

func relPathFor(taskID string, seq int, thumb bool, mime string) string {
	now := time.Now()
	ext := extFromMime(mime, thumb)
	tail := ""
	if thumb {
		tail = "_thumb"
	}
	return path.Join("generated", now.Format("2006"), now.Format("01"), now.Format("02"),
		fmt.Sprintf("%s_%d%s%s", taskID, seq, tail, ext))
}

func extFromMime(mime string, thumb bool) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.Contains(mime, "png"):
		return ".png"
	case strings.Contains(mime, "webp"):
		return ".webp"
	case strings.Contains(mime, "jpeg"), strings.Contains(mime, "jpg"):
		return ".jpg"
	case strings.Contains(mime, "mp4"):
		return ".mp4"
	case strings.Contains(mime, "webm"):
		return ".webm"
	}
	if thumb {
		return ".jpg"
	}
	return ".bin"
}

// ── 主循环 ─────────────────────────────────────────────────

func (s *Session) Run(ctx context.Context, providers map[string]provider.Provider) {
	leaseTicker := time.NewTicker(time.Duration(s.LeaseSec) * time.Second)
	defer leaseTicker.Stop()

	// 心跳放到独立 goroutine 里，避免一次慢调用堵住 lease 流水。
	// 连续失败时记录 streak；不主动重新握手——secret 已被 admin revoke 的情况
	// 没有 bootstrap token 也救不回来，留给运维。
	go s.heartbeatLoop(ctx)

	taskCh := make(chan *leasedTask, s.MaxConcurrency*2)
	var wg sync.WaitGroup
	for i := 0; i < s.MaxConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range taskCh {
				s.runTask(ctx, providers, t)
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			close(taskCh)
			wg.Wait()
			return
		case <-leaseTicker.C:
			free := s.MaxConcurrency - int(s.inflight.Load()) - len(taskCh)
			if free <= 0 {
				continue
			}
			tasks, err := s.Lease(ctx, free)
			if err != nil {
				s.logger.warnf("lease: %v", err)
				continue
			}
			for _, t := range tasks {
				taskCh <- t
			}
			if len(tasks) > 0 {
				s.logger.infof("leased %d tasks (free=%d)", len(tasks), free)
			}
		}
	}
}

// heartbeatLoop 后台周期心跳；失败带上限 2x 的退避抖动，避免雪崩控制面。
//
// 节奏：基础间隔 = s.HeartbeatSec；首次立刻打一次。
// 失败：连续 N 次后日志升级为 error，间隔 cap 在 HeartbeatSec * 5。
func (s *Session) heartbeatLoop(ctx context.Context) {
	base := time.Duration(s.HeartbeatSec) * time.Second
	if base <= 0 {
		base = 10 * time.Second
	}
	cap := base * 5

	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := s.Heartbeat(hctx); err != nil {
		s.logger.warnf("first heartbeat failed: %v", err)
	}
	cancel()

	interval := base
	streak := 0
	t := time.NewTimer(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hctx, hcancel := context.WithTimeout(ctx, 10*time.Second)
			err := s.Heartbeat(hctx)
			hcancel()
			if err != nil {
				streak++
				if streak >= 5 {
					s.logger.errorf("heartbeat down for %d cycles: %v", streak, err)
				} else {
					s.logger.warnf("heartbeat fail (streak=%d): %v", streak, err)
				}
				// 失败退避：interval *= 2，最大 cap
				interval *= 2
				if interval > cap {
					interval = cap
				}
			} else {
				if streak > 0 {
					s.logger.infof("heartbeat recovered after %d failures", streak)
				}
				streak = 0
				interval = base
			}
			t.Reset(interval)
		}
	}
}

// ── 本地 HTTP（/healthz + /d/:ticket） ───────────────────────

func startLocalHTTP(ctx context.Context, s *Session) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"node":"%s","inflight":%d,"version":"%s"}`, s.NodeID, s.inflight.Load(), s.cfg.Version)
	})
	mux.HandleFunc("/d/", func(w http.ResponseWriter, r *http.Request) {
		// 路径形如 /d/<node_id>.<encPayload>.<sig>
		// 与 service.BuildDownloadURL / SignTicket 完全对齐。
		raw := strings.TrimPrefix(r.URL.Path, "/d/")
		first := strings.IndexByte(raw, '.')
		if first <= 0 || first == len(raw)-1 {
			http.Error(w, "bad ticket", http.StatusBadRequest)
			return
		}
		nodeID := raw[:first]
		ticket := raw[first+1:]
		if nodeID != s.NodeID {
			http.Error(w, "node mismatch", http.StatusForbidden)
			return
		}
		assetKey, err := verifyTicket(s.Secret, nodeID, ticket)
		if err != nil {
			http.Error(w, "invalid ticket: "+err.Error(), http.StatusForbidden)
			return
		}
		// 走 X-Accel-Redirect：让 nginx sendfile 真正负责传输
		// nginx.conf 必须有：
		//     location /_internal/ { internal; alias /var/klein/storage/public/; }
		w.Header().Set("X-Accel-Redirect", "/_internal/"+assetKey)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", mimeFromPath(assetKey))
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{
		Addr:              s.cfg.BindAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		s.logger.infof("agent http listen %s (storage=%s)", s.cfg.BindAddr, s.StorageRoot)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.errorf("agent http: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
}

// verifyTicket 复刻 service.SignTicket / VerifyTicket 的反向校验。
//   ticketStr = "<encPayload>.<sig>"
//   sig = base64url(hmac_sha256(secret, nodeID + "." + encPayload))
//   encPayload 解码后是 TicketPayload JSON：{k,a,s,e,n,u}
//
// 校验通过返回 payload.Key（rel path）。
func verifyTicket(secret []byte, nodeID, ticketStr string) (string, error) {
	parts := strings.SplitN(ticketStr, ".", 2)
	if len(parts) != 2 {
		return "", errors.New("format")
	}
	encPayload, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(nodeID + "." + encPayload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "", errors.New("sig")
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return "", errors.New("payload")
	}
	var p struct {
		Kind  string `json:"k"`
		Key   string `json:"a"`
		Shape string `json:"s"`
		Exp   int64  `json:"e"`
	}
	if err := json.Unmarshal(rawPayload, &p); err != nil {
		return "", errors.New("payload json")
	}
	if p.Key == "" {
		return "", errors.New("empty key")
	}
	if time.Now().Unix() > p.Exp {
		return "", errors.New("expired")
	}
	return p.Key, nil
}

func mimeFromPath(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	}
	return "application/octet-stream"
}

// ── main ───────────────────────────────────────────────────

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	cfg.Version = fmt.Sprintf("agent/0.1.0 (%s/%s)", runtime.GOOS, runtime.GOARCH)

	sess := NewSession(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 启动顺序：
	//   1) 先尝试从 KLEIN_STATE_DIR/agent-state.json 复用上次握手结果；
	//   2) 没有 / 不可用 → 用 KLEIN_NODE_TOKEN 走握手，按指数退避重试直到成功 / ctx done；
	//   3) 握手成功后立即写盘，下次重启不再消耗 bootstrap token。
	if !sess.LoadPersistedState() {
		if err := sess.HandshakeWithBackoff(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "handshake:", err)
			os.Exit(2)
		}
	}
	startLocalHTTP(ctx, sess)

	providers := factory.Build()
	sess.Run(ctx, providers)
	sess.logger.infof("agent shutdown done")
}
