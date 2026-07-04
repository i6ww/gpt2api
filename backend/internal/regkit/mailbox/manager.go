package mailbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
)

// ErrCFMintNotConfigured 默认收件后端为 CF，但 mail.cf 里未填写 worker_domain 或
// admin_password。此时 AcquireFresh 会尝试退回邮箱池；若池也无可用条目，会返回更易懂的
// 合成错误而非裸的 ErrNotFound。
var ErrCFMintNotConfigured = errors.New("mailbox: cf worker_domain or admin_password not configured")

// Manager 上层一站式入口：从 mail_pool acquire 一条 → 解密 → 选 backend → Open。
//
// 释放 / 标记成功 / 标记失败由 dispatcher 在使用完毕后调对应的 mail_pool repo 方法。
type Manager struct {
	pool     *repo.MailPoolRepo
	aes      *crypto.AESGCM
	backends map[string]Backend
}

// NewManager 构造，并注册全部 backend。
func NewManager(pool *repo.MailPoolRepo, aes *crypto.AESGCM) *Manager {
	m := &Manager{
		pool:     pool,
		aes:      aes,
		backends: map[string]Backend{},
	}
	m.Register(NewOutlookGraphBackend())
	m.Register(NewOutlookIMAPBackend())
	m.Register(NewTempmailBackend())
	m.Register(NewCFWorkerBackend())
	return m
}

// Register 注册一个 backend（同名覆盖）。
func (m *Manager) Register(b Backend) {
	m.backends[b.Name()] = b
}

// AcquireResult 一次 acquire 调用的返回。
type AcquireResult struct {
	Row     *model.MailPool
	Mailbox Mailbox
	// Ephemeral 表示这条邮箱是即时签发（如 CF Worker mint）的临时资源，没有
	// 持久化到 mail_pool 表；调用方仍可正常调 MarkRegistered / MarkFailed /
	// Release，manager 会自动 no-op，不会污染 DB。
	Ephemeral bool
}

// === Ephemeral mailbox 支持 ===
//
// 自建 CF Worker 这类"无限可签发"的邮箱网关不该走 mail_pool，否则要么生成
// 1 万行废数据、要么注册前必须先批量预生成卡半天。AcquireFresh 给这种场景
// 走"即时模式"：现签现用 → 内存里包成 row → 注册结束顺手丢。
//
// 为了避免触碰 DB（mail_pool 表里没这一行），ephemeral row 的 ID 从一个不可能
// 撞 autoinc 的高位起步（1<<60），manager 的所有标记方法都会判这个范围 no-op。
const ephemeralIDBase uint64 = 1 << 60

var ephemeralIDCounter atomic.Uint64

func nextEphemeralID() uint64 { return ephemeralIDBase + ephemeralIDCounter.Add(1) }

// IsEphemeralID 判断给定的 mail_pool 行 ID 是不是 manager 即时签发的临时邮箱。
//
// 调用方（如 register_task_service.AttachMail）可用它决定是否往 mail_id
// 字段写入这个值（ephemeral 时 mail_pool 表里没对应行，写进去会让前端 join
// 不到，最好留空）。
func IsEphemeralID(id uint64) bool { return id >= ephemeralIDBase }

// Release 把邮箱归还可用池（注册流程出错且不要消耗失败计数时调）。
//
// ephemeral ID 直接 no-op：CF Worker mint 出来的邮箱不在表里，无需归还。
func (m *Manager) Release(ctx context.Context, mailID uint64) error {
	if IsEphemeralID(mailID) {
		return nil
	}
	return m.pool.Release(ctx, mailID)
}

// MarkRegistered 注册成功后标记（绑到产生的 pool_account_id）。
//
// ephemeral ID 直接 no-op：临时邮箱用完即丢，无须留痕。
func (m *Manager) MarkRegistered(ctx context.Context, mailID, accountID uint64) error {
	if IsEphemeralID(mailID) {
		return nil
	}
	return m.pool.MarkRegistered(ctx, mailID, accountID)
}

// MarkFailed +1 失败计数；达到上限置 failed。
//
// ephemeral ID 直接 no-op：临时邮箱失败一次就该丢，没有"失败计数"概念。
func (m *Manager) MarkFailed(ctx context.Context, mailID uint64, errMsg string, maxFail int) (bool, error) {
	if IsEphemeralID(mailID) {
		return false, nil
	}
	return m.pool.MarkFailed(ctx, mailID, errMsg, maxFail)
}

// Acquire 从邮箱池领取一条 available 行并打开 backend 会话。
func (m *Manager) Acquire(ctx context.Context, provider string, cfg BackendConfig) (*AcquireResult, error) {
	row, err := m.pool.Acquire(ctx, provider)
	if err != nil {
		return nil, err
	}
	box, err := m.openWithRow(ctx, row, cfg)
	if err != nil {
		// 失败时归还邮箱（不计入失败计数）
		_ = m.pool.Release(ctx, row.ID)
		return nil, err
	}
	return &AcquireResult{Row: row, Mailbox: box}, nil
}

// AcquireFresh 注册任务领邮箱的优选入口。
//
// 决策表（按 cfg.DefaultMode 路由）：
//
//  1. DefaultMode = "cf"：CF Worker 即时模式
//     - worker_domain + admin_password 已配 → POST /admin/new_address 现签一封 →
//       包成 ephemeral row 返回（不写 mail_pool）。
//     - CF mint / backend Open 失败 → 兜底走 Acquire（从池里捞预生成的 cf 或其它 mode 邮箱）。
//     - 若 mint 跳过（未配 CF）、mint 报错、池化 Acquire 且无可用邮箱 → 返回明确错误，
//       避免出现裸的 repo.ErrNotFound（用户侧只看到 "acquire mail: repo: not found"）。
//
//  2. DefaultMode = "outlook_graph" / "outlook_imap" / "tempmail" / ""：池化模式
//     - 直接走 Acquire 从 mail_pool 拿一行。系统设置里默认收件后端是 Outlook
//       时，**绝不会**触发 CF 即时签发——即便 CF Worker 也填了（CF Worker 配置
//       是给 `cf` 后端的依赖，不该越权拦截 outlook 流程）。
//
// 这样设置层的"默认收件后端"才是单一事实源，用户切到 Outlook 就用 Outlook 池。
func (m *Manager) AcquireFresh(ctx context.Context, provider string, cfg BackendConfig) (*AcquireResult, error) {
	if cfg.DefaultMode != model.MailModeCF {
		return m.Acquire(ctx, provider, cfg)
	}

	acq, mintErr := m.mintCFEphemeral(ctx, cfg)
	if acq != nil {
		return acq, nil
	}
	if mintErr != nil && !errors.Is(mintErr, ErrCFMintNotConfigured) {
		log.Printf("[mailbox] AcquireFresh: CF mint/Open failed (%v), trying mail_pool", mintErr)
	}

	poolAcq, poolErr := m.Acquire(ctx, provider, cfg)
	if poolErr == nil {
		return poolAcq, nil
	}
	if errors.Is(poolErr, repo.ErrNotFound) {
		if errors.Is(mintErr, ErrCFMintNotConfigured) {
			return nil, fmt.Errorf(
				"收件方式为 Cloudflare，但未在「系统配置 → 邮箱 → CF Worker」填写 Worker 域名与 Admin 密码；" +
					"且邮箱池中也没有可用条目。Worker 域名请填 Worker 的根地址（示例：https://your-worker.xxx.workers.dev，勿带路径）",
			)
		}
		return nil, fmt.Errorf(
			"CF Worker 即时签发失败（%v），且邮箱池中无可用条目。%s",
			mintErr,
			cfMintFailureExtra(mintErr),
		)
	}
	return nil, poolErr
}

func cfMintFailureExtra(mintErr error) string {
	if mintErr == nil {
		return "请核对 Worker 根域（须为签发 API，如 …/temp-email-api…，勿用收件页 inbox 域名）、Admin 密码、POST /admin/new_address；" +
			"系统配置「邮箱域名」与 Worker 允许列表一致或可留空用默认域；或先在邮箱池导入可用邮箱作兜底。"
	}
	msg := strings.ToLower(mintErr.Error())
	if strings.Contains(msg, "invalid") && strings.Contains(msg, "domain") {
		return "错误含无效域名时请清空或修正系统配置里 CF「邮箱域名」，使之与 Worker 控制台允许的收件域一致。"
	}
	if strings.Count(msg, "failed to create address") >= 2 || strings.Contains(msg, "failed to create address:") {
		return "该类叠字报错通常来自 cloudflare_temp_email：D1「建地址」未成功（表结构/schema、配额、或域名未匹配 ALLOW_DOMAINS）；" +
			"请在 Cloudflare 该 Worker 观测日志或对同一请求用 curl 看完整正文；收件「邮箱域名」可与 Worker 配置逐字对齐或留空试默认域。"
	}
	return "请检查 Worker（含 D1/KV）、Admin 密码、/admin/new_address，或先入池兜底。"
}

// mintCFEphemeral Worker 即时签发（不入 mail_pool）；未配置 worker/password 时返回 ErrCFMintNotConfigured。
func (m *Manager) mintCFEphemeral(ctx context.Context, cfg BackendConfig) (*AcquireResult, error) {
	worker := strings.TrimRight(strings.TrimSpace(cfg.CFWorkerDomain), "/")
	adminPwd := strings.TrimSpace(cfg.CFAdminPassword)
	if worker == "" || adminPwd == "" {
		return nil, ErrCFMintNotConfigured
	}
	be, ok := m.backends[model.MailModeCF]
	if !ok {
		return nil, fmt.Errorf("cf backend not registered")
	}

	hc := HTTPClientWithProxy("", 15*time.Second)
	domain := strings.TrimSpace(pickRandomDomain(cfg.CFEmailDomain))

	type mintTry struct {
		enablePrefix bool
		domain       string
	}
	// cloudflare_temp_email 等 Worker 的 admin 接口与常见集成约定一致：多数场景先传 enablePrefix=false；
	// 若 Worker 配了 PREFIX 且 enablePrefix=true，会把 PREFIX 拼进本地部分，易导致超长或与非预期域名组合。
	tries := []mintTry{
		{false, domain},
		{true, domain},
	}
	if domain != "" {
		tries = append(tries, mintTry{false, ""}, mintTry{true, ""})
	}

	var lastErr error
	for _, tr := range tries {
		name := RandomCFName(12)
		email, jwt, err := MintCFAddress(ctx, hc, worker, adminPwd, name, tr.domain, tr.enablePrefix)
		if err != nil {
			log.Printf("[mailbox] CF mint retry enablePrefix=%v domain=%q: %v", tr.enablePrefix, tr.domain, err)
			lastErr = err
			continue
		}
		row := &model.MailPool{
			ID:       nextEphemeralID(),
			Email:    strings.ToLower(email),
			Mode:     model.MailModeCF,
			Status:   model.MailStatusInUse,
			ClientID: "cf-worker-ephemeral",
		}
		box, err := be.Open(ctx, row, Secrets{RefreshToken: jwt}, cfg)
		if err != nil {
			return nil, err
		}
		log.Printf("[mailbox] AcquireFresh: CF mint ok email=%s ephemeral_id=%d enablePrefix=%v domain=%q (no mail_pool write)",
			row.Email, row.ID, tr.enablePrefix, tr.domain)
		return &AcquireResult{Row: row, Mailbox: box, Ephemeral: true}, nil
	}
	return nil, lastErr
}

// pickRandomDomain 从 system_config 里 "a.com,b.com\nc.com" 这种串里随机挑一个域名。
//
// 多 domain 时使用 crypto/rand 均匀分布；单 domain 直接返回；空串返回空（让
// worker 用其默认域）。与 service.GenerateCF 的 splitDomainList + secureIntn
// 行为一致，避免两边逻辑漂移。
func pickRandomDomain(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	r := strings.NewReplacer(";", ",", "\n", ",", "\r", ",", "\t", ",", " ", ",")
	parts := strings.Split(r.Replace(raw), ",")
	domains := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		domains = append(domains, p)
	}
	switch len(domains) {
	case 0:
		return ""
	case 1:
		return domains[0]
	default:
		return domains[secureIntn(len(domains))]
	}
}

// PoolStats 暴露邮箱池统计（typed wrapper 给 dispatcher 的 preflight 用）。
func (m *Manager) PoolStats(ctx context.Context) (map[string]int64, error) {
	return m.pool.Stats(ctx)
}

// AcquireByID 用指定 mail_pool 行（必须 status=in_use 或 available；这里直接 GetByID 后切到 in_use）。
//
// 一般情况下不建议用，留作"指定邮箱"高级用法。
func (m *Manager) AcquireByID(ctx context.Context, id uint64, provider string, cfg BackendConfig) (*AcquireResult, error) {
	row, err := m.pool.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	box, err := m.openWithRow(ctx, row, cfg)
	if err != nil {
		return nil, err
	}
	return &AcquireResult{Row: row, Mailbox: box}, nil
}

// openWithRow 解密 mail_pool 行，挑 backend 并打开。
func (m *Manager) openWithRow(ctx context.Context, row *model.MailPool, cfg BackendConfig) (Mailbox, error) {
	mode := row.Mode
	if mode == "" {
		mode = model.MailModeOutlookGraph
	}
	be, ok := m.backends[mode]
	if !ok {
		return nil, fmt.Errorf("mail_pool 行 mode=%q 没有对应 backend", mode)
	}
	secrets := Secrets{}
	if len(row.PasswordEnc) > 0 && m.aes != nil {
		if pw, err := m.aes.Decrypt(row.PasswordEnc); err == nil {
			secrets.Password = string(pw)
		}
	}
	if len(row.RefreshTokenEnc) > 0 && m.aes != nil {
		if rt, err := m.aes.Decrypt(row.RefreshTokenEnc); err == nil {
			secrets.RefreshToken = string(rt)
		}
	}
	return be.Open(ctx, row, secrets, cfg)
}
