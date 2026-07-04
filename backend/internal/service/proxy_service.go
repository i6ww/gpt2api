package service

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
)

type ProxyService struct {
	repo *repo.ProxyRepo
	aes  *crypto.AESGCM

	mu     sync.RWMutex
	cached map[uint64]*model.Proxy
	loaded time.Time
}

func NewProxyService(r *repo.ProxyRepo, aes *crypto.AESGCM) *ProxyService {
	return &ProxyService{repo: r, aes: aes, cached: map[uint64]*model.Proxy{}}
}

func (s *ProxyService) Create(ctx context.Context, adminID uint64, req *dto.ProxyCreateReq) (*model.Proxy, error) {
	if err := validateProtocol(req.Protocol); err != nil {
		return nil, err
	}

	p := &model.Proxy{
		Name:      strings.TrimSpace(req.Name),
		Protocol:  strings.ToLower(strings.TrimSpace(req.Protocol)),
		Host:      strings.TrimSpace(req.Host),
		Port:      req.Port,
		Status:    model.ProxyStatusEnabled,
		CreatedBy: &adminID,
	}

	if u := strings.TrimSpace(req.Username); u != "" {
		p.Username = &u
	}

	if pw := req.Password; pw != "" {
		enc, err := s.aes.Encrypt([]byte(pw))
		if err != nil {
			return nil, errcode.Internal.Wrap(err)
		}
		p.PasswordEnc = enc
	}

	if r := strings.TrimSpace(req.Remark); r != "" {
		p.Remark = &r
	}

	if err := s.repo.Create(ctx, p); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}

	s.invalidate()
	return p, nil
}

func (s *ProxyService) Update(ctx context.Context, id uint64, req *dto.ProxyUpdateReq) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		return errcode.ResourceMissing
	}

	fields := map[string]any{}
	if req.Name != nil {
		fields["name"] = strings.TrimSpace(*req.Name)
	}
	if req.Protocol != nil {
		if err := validateProtocol(*req.Protocol); err != nil {
			return err
		}
		fields["protocol"] = strings.ToLower(strings.TrimSpace(*req.Protocol))
	}
	if req.Host != nil {
		fields["host"] = strings.TrimSpace(*req.Host)
	}
	if req.Port != nil {
		fields["port"] = *req.Port
	}
	if req.Username != nil {
		if u := strings.TrimSpace(*req.Username); u == "" {
			fields["username"] = nil
		} else {
			fields["username"] = u
		}
	}
	if req.Password != nil {
		if pw := *req.Password; pw == "" {
			fields["password_enc"] = nil
		} else {
			enc, err := s.aes.Encrypt([]byte(pw))
			if err != nil {
				return errcode.Internal.Wrap(err)
			}
			fields["password_enc"] = enc
		}
	}
	if req.Status != nil {
		fields["status"] = *req.Status
	}
	if req.Remark != nil {
		if r := strings.TrimSpace(*req.Remark); r == "" {
			fields["remark"] = nil
		} else {
			fields["remark"] = r
		}
	}

	if err := s.repo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}

	s.invalidate()
	return nil
}

func (s *ProxyService) Delete(ctx context.Context, id uint64) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		return errcode.ResourceMissing
	}
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}

	s.invalidate()
	return nil
}

func (s *ProxyService) BatchDeleteByIDs(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, errcode.InvalidParam.WithMsg("ids is required")
	}

	n, err := s.repo.SoftDeleteByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}

	s.invalidate()
	return n, nil
}

func (s *ProxyService) BatchImport(ctx context.Context, adminID uint64, req *dto.ProxyImportReq) (*dto.ProxyImportResult, error) {
	lines := strings.Split(strings.ReplaceAll(req.Text, "\r\n", "\n"), "\n")
	remark := strings.TrimSpace(req.Remark)
	imported := 0
	skipped := 0

	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		u, err := url.Parse(line)
		if err != nil || u == nil || u.Host == "" {
			skipped++
			continue
		}

		proto := strings.ToLower(strings.TrimSpace(u.Scheme))
		if err := validateProtocol(proto); err != nil {
			skipped++
			continue
		}

		host := strings.TrimSpace(u.Hostname())
		portNum, err := strconv.Atoi(u.Port())
		if err != nil || portNum <= 0 || portNum > 65535 || host == "" {
			skipped++
			continue
		}

		name := fmt.Sprintf("%s://%s:%d", proto, host, portNum)
		if u.User != nil {
			if user := u.User.Username(); user != "" {
				name = fmt.Sprintf("%s@%s://%s:%d", user, proto, host, portNum)
			}
		}
		if len(lines) > 1 {
			name = fmt.Sprintf("代理 %02d - %s", idx+1, name)
		}

		createReq := &dto.ProxyCreateReq{
			Name:     name,
			Protocol: proto,
			Host:     host,
			Port:     uint16(portNum),
			Remark:   remark,
		}
		if u.User != nil {
			createReq.Username = u.User.Username()
			if pw, ok := u.User.Password(); ok {
				createReq.Password = pw
			}
		}

		if _, err := s.Create(ctx, adminID, createReq); err != nil {
			skipped++
			continue
		}
		imported++
	}

	return &dto.ProxyImportResult{
		Imported: imported,
		Skipped:  skipped,
	}, nil
}

func (s *ProxyService) List(ctx context.Context, req *dto.ProxyListReq) ([]*dto.ProxyResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.ProxyListFilter{
		Status:   req.Status,
		Keyword:  req.Keyword,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}

	resp := make([]*dto.ProxyResp, 0, len(items))
	for _, it := range items {
		resp = append(resp, proxyToResp(it))
	}
	return resp, total, nil
}

func (s *ProxyService) GetByID(ctx context.Context, id uint64) (*model.Proxy, error) {
	if id == 0 {
		return nil, nil
	}

	s.mu.RLock()
	if p, ok := s.cached[id]; ok && time.Since(s.loaded) < 30*time.Second {
		s.mu.RUnlock()
		return p, nil
	}
	s.mu.RUnlock()

	p, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cached[id] = p
	s.loaded = time.Now()
	s.mu.Unlock()
	return p, nil
}

func (s *ProxyService) ListEnabled(ctx context.Context) ([]*model.Proxy, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	return s.repo.ListEnabled(ctx)
}

func (s *ProxyService) ResolvePassword(p *model.Proxy) (string, error) {
	if len(p.PasswordEnc) == 0 {
		return "", nil
	}
	plain, err := s.aes.Decrypt(p.PasswordEnc)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *ProxyService) BuildURL(p *model.Proxy) (*url.URL, error) {
	if p == nil {
		return nil, nil
	}

	u := &url.URL{
		Scheme: p.Protocol,
		Host:   p.Host + ":" + strconv.Itoa(int(p.Port)),
	}

	if p.Username != nil && *p.Username != "" {
		pw, err := s.ResolvePassword(p)
		if err != nil {
			return nil, err
		}
		u.User = url.UserPassword(*p.Username, pw)
	}

	return u, nil
}

func (s *ProxyService) MarkCheck(ctx context.Context, id uint64, ok bool, latencyMs int, errMsg string) error {
	if errMsg != "" && len(errMsg) > 250 {
		errMsg = errMsg[:250]
	}

	if err := s.repo.MarkCheck(ctx, id, ok, latencyMs, errMsg); err != nil {
		return errcode.DBError.Wrap(err)
	}

	s.invalidate()
	return nil
}

func (s *ProxyService) invalidate() {
	s.mu.Lock()
	s.cached = map[uint64]*model.Proxy{}
	s.mu.Unlock()
}

func validateProtocol(proto string) error {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case model.ProxyProtoHTTP,
		model.ProxyProtoHTTPS,
		model.ProxyProtoSOCKS5,
		model.ProxyProtoSOCKS5H:
		return nil
	default:
		return errcode.InvalidParam.WithMsg(fmt.Sprintf("不支持的协议: %s", proto))
	}
}

func proxyToResp(p *model.Proxy) *dto.ProxyResp {
	r := &dto.ProxyResp{
		ID:          p.ID,
		Name:        p.Name,
		Protocol:    p.Protocol,
		Host:        p.Host,
		Port:        p.Port,
		Status:      p.Status,
		HasPassword: len(p.PasswordEnc) > 0,
		LastCheckOK: p.LastCheckOK,
		LastCheckMs: p.LastCheckMs,
		CreatedAt:   p.CreatedAt.Unix(),
		UpdatedAt:   p.UpdatedAt.Unix(),
	}

	if p.Username != nil {
		r.Username = *p.Username
	}
	if p.LastCheckAt != nil {
		r.LastCheckAt = p.LastCheckAt.Unix()
	}
	if p.LastError != nil {
		r.LastError = *p.LastError
	}
	if p.Remark != nil {
		r.Remark = *p.Remark
	}

	return r
}
