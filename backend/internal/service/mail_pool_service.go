package service

import (
	"context"
	cryptoRand "crypto/rand"
	"errors"
	"fmt"
	mathrand "math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/regkit/mailbox"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
)

// MailPoolService 共享邮箱池服务。
type MailPoolService struct {
	repo   *repo.MailPoolRepo
	aes    *crypto.AESGCM
	sysCfg *SystemConfigService
}

// NewMailPoolService 构造。
func NewMailPoolService(r *repo.MailPoolRepo, aes *crypto.AESGCM, sysCfg *SystemConfigService) *MailPoolService {
	return &MailPoolService{repo: r, aes: aes, sysCfg: sysCfg}
}

// List 列表。
func (s *MailPoolService) List(ctx context.Context, req *dto.MailPoolListReq) ([]*dto.MailPoolResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.MailPoolFilter{
		Status:   req.Status,
		Mode:     req.Mode,
		Keyword:  strings.TrimSpace(req.Keyword),
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.MailPoolResp, 0, len(items))
	for _, it := range items {
		out = append(out, mailPoolToResp(it))
	}
	return out, total, nil
}

// Stats 状态统计。
func (s *MailPoolService) Stats(ctx context.Context) (*dto.MailPoolStatsResp, error) {
	m, err := s.repo.Stats(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return &dto.MailPoolStatsResp{
		Total:      m["total"],
		Available:  m["available"],
		InUse:      m["in_use"],
		Registered: m["registered"],
		Failed:     m["failed"],
		Disabled:   m["disabled"],
	}, nil
}

// Import 批量导入。按段数自动识别格式：
//
//   - 4 段（旧）：email----password----client_id----refresh_token
//   - 7 段（卡密）：email----password----[读邮链接]----email----password----client_id----refresh_token
//     第 3 / 4 / 5 段是供卡商展示用的冗余字段（第三方读邮 URL、email/pass 重复），
//     我们只取 0/1/5/6 段，其余忽略。
//
// 自定义分隔符：通过 req.Separator 传入。
func (s *MailPoolService) Import(ctx context.Context, req *dto.MailPoolImportReq) (*dto.MailPoolImportResult, error) {
	sep := req.Separator
	if sep == "" {
		sep = "----"
	}
	mode := req.Mode
	if mode == "" {
		mode = model.MailModeOutlookGraph
	}

	res := &dto.MailPoolImportResult{}
	batch := make([]*model.MailPool, 0, 64)
	seen := map[string]struct{}{}
	for i, raw := range strings.Split(req.Text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, sep)
		var email, password, clientID, refresh string
		switch {
		case len(parts) >= 7:
			// 7 段卡密：parts[2/3/4] = 读邮链接 / email 重复 / password 重复，忽略
			email = strings.ToLower(strings.TrimSpace(parts[0]))
			password = strings.TrimSpace(parts[1])
			clientID = strings.TrimSpace(parts[5])
			refresh = strings.TrimSpace(strings.Join(parts[6:], sep))
		case len(parts) == 4:
			email = strings.ToLower(strings.TrimSpace(parts[0]))
			password = strings.TrimSpace(parts[1])
			clientID = strings.TrimSpace(parts[2])
			refresh = strings.TrimSpace(parts[3])
		default:
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行字段数 %d 不支持（要 4 段或 7 段）", i+1, len(parts)))
			continue
		}

		if email == "" || password == "" || clientID == "" || refresh == "" {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行有空字段", i+1))
			continue
		}
		if _, dup := seen[email]; dup {
			res.Skipped++
			continue
		}
		seen[email] = struct{}{}

		pwEnc, err := s.aes.Encrypt([]byte(password))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		rtEnc, err := s.aes.Encrypt([]byte(refresh))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}

		batch = append(batch, &model.MailPool{
			Email:           email,
			PasswordEnc:     pwEnc,
			ClientID:        clientID,
			RefreshTokenEnc: rtEnc,
			Mode:            mode,
			Status:          model.MailStatusAvailable,
		})
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

// Update 单条更新。
func (s *MailPoolService) Update(ctx context.Context, id uint64, req *dto.MailPoolUpdateReq) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}

	fields := map[string]any{}
	if req.Mode != nil {
		fields["mode"] = *req.Mode
	}
	if req.Status != nil {
		fields["status"] = *req.Status
	}
	if req.ClientID != nil {
		fields["client_id"] = strings.TrimSpace(*req.ClientID)
	}
	if req.Password != nil && *req.Password != "" {
		enc, err := s.aes.Encrypt([]byte(*req.Password))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["password_enc"] = enc
	}
	if req.Refresh != nil && *req.Refresh != "" {
		enc, err := s.aes.Encrypt([]byte(*req.Refresh))
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		fields["refresh_token_enc"] = enc
	}

	if err := s.repo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// Delete 单条软删。
func (s *MailPoolService) Delete(ctx context.Context, id uint64) error {
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// BatchDelete 批量软删。
func (s *MailPoolService) BatchDelete(ctx context.Context, ids []uint64) (int64, error) {
	n, err := s.repo.SoftDeleteByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// DeleteByStatus 按状态批量软删。
func (s *MailPoolService) DeleteByStatus(ctx context.Context, status string) (int64, error) {
	n, err := s.repo.SoftDeleteByStatus(ctx, status)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// Truncate 按当前筛选条件软删全部匹配；filter 全空 = 清空整张表。
// 调用方需要在 handler 层做二次确认（confirm == "DELETE"）。
func (s *MailPoolService) Truncate(ctx context.Context, req *dto.MailPoolTruncateReq) (int64, error) {
	f := repo.MailPoolFilter{
		Status:  req.Status,
		Mode:    req.Mode,
		Keyword: req.Keyword,
	}
	n, err := s.repo.SoftDeleteByFilter(ctx, f)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// Reset 把若干条邮箱状态置回 available。
func (s *MailPoolService) Reset(ctx context.Context, ids []uint64) (int64, error) {
	n, err := s.repo.ResetByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// Acquire 给注册流程领用一封邮箱，标 in_use。
func (s *MailPoolService) Acquire(ctx context.Context, provider string) (*model.MailPool, error) {
	m, err := s.repo.Acquire(ctx, provider)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errcode.ResourceMissing.WithMsg("无可用邮箱")
		}
		return nil, errcode.DBError.Wrap(err)
	}
	return m, nil
}

// Release 注册中途归还邮箱。
func (s *MailPoolService) Release(ctx context.Context, id uint64) error {
	if err := s.repo.Release(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// MarkRegistered 注册成功后标记。
func (s *MailPoolService) MarkRegistered(ctx context.Context, id, accountID uint64) error {
	if err := s.repo.MarkRegistered(ctx, id, accountID); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// MarkFailed 失败 +1，达到上限锁定。
func (s *MailPoolService) MarkFailed(ctx context.Context, id uint64, errMsg string, maxFail int) (terminal bool, err error) {
	t, e := s.repo.MarkFailed(ctx, id, errMsg, maxFail)
	if e != nil {
		return false, errcode.DBError.Wrap(e)
	}
	return t, nil
}

// GenerateCF 调用系统配置中的 CF Worker / admin_password / email_domain
// 一键生成 N 个临时邮箱并写入 mail_pool（mode=cf, refresh_token=jwt）。
func (s *MailPoolService) GenerateCF(ctx context.Context, req *dto.MailPoolCFGenerateReq) (*dto.MailPoolCFGenerateResp, error) {
	if s.sysCfg == nil {
		return nil, errcode.Internal.WithMsg("系统配置未注入")
	}
	cfg := s.sysCfg.GetJSON(ctx, SettingMailCF)
	worker, _ := cfg["worker_domain"].(string)
	adminPwd, _ := cfg["admin_password"].(string)
	defaultDomain, _ := cfg["email_domain"].(string)
	worker = strings.TrimRight(strings.TrimSpace(worker), "/")
	adminPwd = strings.TrimSpace(adminPwd)

	// email_domain 支持逗号 / 换行 / 分号分隔多个域名；逐个生成时随机挑一个。
	domainsRaw := strings.TrimSpace(req.Domain)
	if domainsRaw == "" {
		domainsRaw = strings.TrimSpace(defaultDomain)
	}
	domains := splitDomainList(domainsRaw)

	if worker == "" {
		return nil, errcode.InvalidParam.WithMsg("CF Worker 未配置 worker_domain（系统配置 → 邮箱配置 → CF Worker）")
	}
	if adminPwd == "" {
		return nil, errcode.InvalidParam.WithMsg("CF Worker 未配置 admin_password")
	}

	enablePrefix := true
	if req.EnablePrefix != nil {
		enablePrefix = *req.EnablePrefix
	}
	nameLen := req.NameLen
	if nameLen <= 0 {
		nameLen = 12
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	res := &dto.MailPoolCFGenerateResp{Samples: []string{}}

	for i := 0; i < req.Count; i++ {
		select {
		case <-ctx.Done():
			return res, nil
		default:
		}
		name := mailbox.RandomCFName(nameLen)
		// 多域名：每个邮箱随机挑一个；只有一个就一直用它；都为空就传 "" 走 worker 默认。
		oneDomain := ""
		if len(domains) == 1 {
			oneDomain = domains[0]
		} else if len(domains) > 1 {
			oneDomain = domains[secureIntn(len(domains))]
		}
		email, jwt, err := mailbox.MintCFAddress(ctx, httpClient, worker, adminPwd, name, oneDomain, enablePrefix)
		if err != nil {
			res.Skipped++
			if len(res.Errors) < 10 {
				res.Errors = append(res.Errors, fmt.Sprintf("第 %d 个失败：%v", i+1, err))
			}
			continue
		}
		// CF 模式下 password / client_id 实际不会被使用；存随机 placeholder 以满足 NOT NULL 约束。
		passEnc, err := s.aes.Encrypt([]byte("cf-no-password"))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		jwtEnc, err := s.aes.Encrypt([]byte(jwt))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		row := &model.MailPool{
			Email:           strings.ToLower(email),
			PasswordEnc:     passEnc,
			ClientID:        "cf-worker",
			RefreshTokenEnc: jwtEnc,
			Mode:            model.MailModeCF,
			Status:          model.MailStatusAvailable,
		}
		if _, err := s.repo.UpsertMany(ctx, []*model.MailPool{row}); err != nil {
			res.Skipped++
			if len(res.Errors) < 10 {
				res.Errors = append(res.Errors, fmt.Sprintf("第 %d 个写库失败：%v", i+1, err))
			}
			continue
		}
		res.Generated++
		if len(res.Samples) < 5 {
			res.Samples = append(res.Samples, email)
		}
	}
	return res, nil
}

// splitDomainList 把 "qq.qkmss.com, mail.qkmss.com\nqq.jzqkwl.com" 这种字串按 "," ";" "\n" "空白" 切开并 trim。
func splitDomainList(raw string) []string {
	if raw == "" {
		return nil
	}
	r := strings.NewReplacer(";", ",", "\n", ",", "\r", ",", "\t", ",", " ", ",")
	parts := strings.Split(r.Replace(raw), ",")
	out := make([]string, 0, len(parts))
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
		out = append(out, p)
	}
	return out
}

// secureIntn 从 crypto/rand 取 [0, n)；失败回退 math/rand。
//
// 仅 GenerateCF 还在用（多 domain 随机挑一个）；CF 邮箱本地名 + worker 调用
// 已下沉到 mailbox 包，避免 service / mailbox 两边维护两份。
func secureIntn(n int) int {
	if n <= 0 {
		return 0
	}
	maxN := int64(n)
	buf := make([]byte, 8)
	if _, err := cryptoRand.Read(buf); err == nil {
		v := int64(0)
		for _, by := range buf {
			v = (v << 8) | int64(by)
		}
		if v < 0 {
			v = -v
		}
		return int(v % maxN)
	}
	return mathrand.Intn(n) //nolint:gosec
}

// === 内部工具 ===

func mailPoolToResp(m *model.MailPool) *dto.MailPoolResp {
	r := &dto.MailPoolResp{
		ID:           m.ID,
		Email:        m.Email,
		ClientID:     m.ClientID,
		Mode:         m.Mode,
		Status:       m.Status,
		FailureCount: m.FailureCount,
		ImportedAt:   m.ImportedAt.UnixMilli(),
	}
	if m.LastError != nil {
		r.LastError = *m.LastError
	}
	if m.UsedByProvider != nil {
		r.UsedByProvider = *m.UsedByProvider
	}
	if m.UsedByAccountID != nil {
		r.UsedByAccountID = *m.UsedByAccountID
	}
	if m.UsedAt != nil {
		r.UsedAt = m.UsedAt.UnixMilli()
	}
	if m.RegisteredAt != nil {
		r.RegisteredAt = m.RegisteredAt.UnixMilli()
	}
	return r
}
