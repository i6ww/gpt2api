package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
)

// PaymentProxyService 印尼支付代理池管理。
//
// 跟 ProxyService（注册代理）独立。Phase B 的 Midtrans/GoPay 必须用印尼/东南亚 IP，
// 这一层只做 CRUD + 测试 + dispatcher 取代理（PickRandomActive）。
type PaymentProxyService struct {
	repo *repo.PaymentProxyPoolRepo
	aes  *crypto.AESGCM
}

// NewPaymentProxyService 构造。
func NewPaymentProxyService(r *repo.PaymentProxyPoolRepo, aes *crypto.AESGCM) *PaymentProxyService {
	return &PaymentProxyService{repo: r, aes: aes}
}

// Create 新增。
func (s *PaymentProxyService) Create(ctx context.Context, adminID uint64, req *dto.PaymentProxyCreateReq) (*model.PaymentProxyPool, error) {
	scheme := strings.ToLower(strings.TrimSpace(req.Scheme))
	if !validatePaymentProxyScheme(scheme) {
		return nil, errcode.InvalidParam.WithMsg("不支持的协议: " + scheme)
	}
	host := strings.TrimSpace(req.Host)
	apiURL := strings.TrimSpace(req.APIURL)
	if host == "" && apiURL == "" {
		return nil, errcode.InvalidParam.WithMsg("host 与 api_url 至少填一个")
	}
	if host != "" && req.Port <= 0 {
		return nil, errcode.InvalidParam.WithMsg("静态代理必须填 port")
	}

	p := &model.PaymentProxyPool{
		Name:      strings.TrimSpace(req.Name),
		Scheme:    scheme,
		Port:      req.Port,
		Country:   strings.ToUpper(strings.TrimSpace(req.Country)),
		Status:    model.PaymentProxyStatusActive,
		CreatedBy: &adminID,
	}
	if p.Country == "" {
		p.Country = "ID"
	}
	if host != "" {
		p.Host = &host
	}
	if apiURL != "" {
		p.APIURL = &apiURL
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
	return p, nil
}

// Update 编辑。
func (s *PaymentProxyService) Update(ctx context.Context, id uint64, req *dto.PaymentProxyUpdateReq) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}

	fields := map[string]any{}
	if req.Name != nil {
		fields["name"] = strings.TrimSpace(*req.Name)
	}
	if req.Scheme != nil {
		v := strings.ToLower(strings.TrimSpace(*req.Scheme))
		if !validatePaymentProxyScheme(v) {
			return errcode.InvalidParam.WithMsg("不支持的协议: " + v)
		}
		fields["scheme"] = v
	}
	if req.Host != nil {
		v := strings.TrimSpace(*req.Host)
		if v == "" {
			fields["host"] = nil
		} else {
			fields["host"] = v
		}
	}
	if req.Port != nil {
		fields["port"] = *req.Port
	}
	if req.Username != nil {
		v := strings.TrimSpace(*req.Username)
		if v == "" {
			fields["username"] = nil
		} else {
			fields["username"] = v
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
	if req.APIURL != nil {
		v := strings.TrimSpace(*req.APIURL)
		if v == "" {
			fields["api_url"] = nil
		} else {
			fields["api_url"] = v
		}
	}
	if req.Country != nil {
		fields["country"] = strings.ToUpper(strings.TrimSpace(*req.Country))
	}
	if req.Status != nil {
		fields["status"] = *req.Status
	}
	if req.Remark != nil {
		v := strings.TrimSpace(*req.Remark)
		if v == "" {
			fields["remark"] = nil
		} else {
			fields["remark"] = v
		}
	}

	if err := s.repo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// Delete 软删除。
func (s *PaymentProxyService) Delete(ctx context.Context, id uint64) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// BatchDelete 批量软删除。
func (s *PaymentProxyService) BatchDelete(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, errcode.InvalidParam.WithMsg("ids is required")
	}
	n, err := s.repo.SoftDeleteByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// List 分页列表。
func (s *PaymentProxyService) List(ctx context.Context, req *dto.PaymentProxyListReq) ([]*dto.PaymentProxyResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.PaymentProxyFilter{
		Status:   req.Status,
		Country:  req.Country,
		Keyword:  req.Keyword,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	resp := make([]*dto.PaymentProxyResp, 0, len(items))
	for _, it := range items {
		resp = append(resp, paymentProxyToResp(it))
	}
	return resp, total, nil
}

// Stats 状态统计。
func (s *PaymentProxyService) Stats(ctx context.Context) (map[string]int64, error) {
	out, err := s.repo.Stats(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return out, nil
}

// GetByID 给 dispatcher 用。
func (s *PaymentProxyService) GetByID(ctx context.Context, id uint64) (*model.PaymentProxyPool, error) {
	p, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// PickRandom dispatcher 取代理入口。country 留空 = 不过滤。
func (s *PaymentProxyService) PickRandom(ctx context.Context, country string) (*model.PaymentProxyPool, error) {
	p, err := s.repo.PickRandomActive(ctx, country)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// PickRandomExcluding 按 country 抽一个代理，但排除指定 ID 集合。
// 用于 swap 场景：避免在 rate-limited 时回锅同一个被标失败的代理。
func (s *PaymentProxyService) PickRandomExcluding(ctx context.Context, country string, excludeIDs []uint64) (*model.PaymentProxyPool, error) {
	p, err := s.repo.PickRandomActiveExcluding(ctx, country, excludeIDs)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// MarkUsed / MarkFailed 转发到 repo。
func (s *PaymentProxyService) MarkUsed(ctx context.Context, id uint64) error {
	return s.repo.MarkUsed(ctx, id)
}

// MarkFailed 失败计数。
func (s *PaymentProxyService) MarkFailed(ctx context.Context, id uint64, errMsg string) error {
	return s.repo.MarkFailed(ctx, id, errMsg)
}

// ResolvePassword 解密密码（dispatcher 拼 URL 用）。
func (s *PaymentProxyService) ResolvePassword(p *model.PaymentProxyPool) (string, error) {
	if len(p.PasswordEnc) == 0 {
		return "", nil
	}
	plain, err := s.aes.Decrypt(p.PasswordEnc)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// BuildURL 拼成可用的代理 URL；动态代理（api_url）由调用方先 fetch 拿到字符串再拼。
func (s *PaymentProxyService) BuildURL(p *model.PaymentProxyPool) (*url.URL, error) {
	if p == nil {
		return nil, nil
	}
	if p.Host == nil || *p.Host == "" {
		return nil, errors.New("payment proxy: 没有静态 host，调用方应自行调 api_url 取代理")
	}
	u := &url.URL{
		Scheme: p.Scheme,
		Host:   *p.Host + ":" + strconv.Itoa(p.Port),
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

// BatchImport 批量导入（仿 ProxyService 风格：一行一条 URL）。
func (s *PaymentProxyService) BatchImport(ctx context.Context, adminID uint64, req *dto.PaymentProxyImportReq) (*dto.PaymentProxyImportResult, error) {
	imported := 0
	skipped := 0
	country := strings.ToUpper(strings.TrimSpace(req.Country))
	if country == "" {
		country = "ID"
	}
	remark := strings.TrimSpace(req.Remark)

	for idx, raw := range strings.Split(strings.ReplaceAll(req.Text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		createReq, err := parsePaymentProxyLine(line, country, remark, idx+1)
		if err != nil {
			skipped++
			continue
		}

		if _, err := s.Create(ctx, adminID, createReq); err != nil {
			skipped++
			continue
		}
		imported++
	}

	return &dto.PaymentProxyImportResult{
		Imported: imported,
		Skipped:  skipped,
	}, nil
}

// Test 简单测试代理：通过它访问 https://api.ipify.org（带超时），返回 IP + 延时。
func (s *PaymentProxyService) Test(ctx context.Context, p *model.PaymentProxyPool) *dto.PaymentProxyTestResp {
	resp := &dto.PaymentProxyTestResp{}
	if p == nil {
		resp.Error = "proxy is nil"
		return resp
	}

	var proxyURL string
	// 动态：先调 api_url 拿到一个 host:port 串
	if p.APIURL != nil && *p.APIURL != "" {
		raw, err := fetchProxyAPI(ctx, *p.APIURL, 10*time.Second)
		if err != nil {
			resp.Error = "api_url fetch: " + err.Error()
			return resp
		}
		proxyURL = normalizeProxyEndpoint(raw, p.Scheme)
		if proxyURL == "" {
			resp.Error = "api_url 返回内容无法解析为代理"
			return resp
		}
	} else {
		u, err := s.BuildURL(p)
		if err != nil {
			resp.Error = err.Error()
			return resp
		}
		proxyURL = u.String()
	}

	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		resp.Error = "代理 URL 解析失败: " + err.Error()
		return resp
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyURL(parsedProxy),
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	cli := &http.Client{Transport: tr, Timeout: 15 * time.Second}

	t0 := time.Now()
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	r, err := cli.Do(httpReq)
	if err != nil {
		resp.LatencyMs = int(time.Since(t0).Milliseconds())
		resp.Error = err.Error()
		return resp
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 128))
	resp.LatencyMs = int(time.Since(t0).Milliseconds())
	resp.OK = r.StatusCode == 200
	resp.IP = strings.TrimSpace(string(body))
	if !resp.OK {
		resp.Error = fmt.Sprintf("HTTP %d", r.StatusCode)
	}
	return resp
}

// MarkCheck 转发：handler 在 Test 后写入测试结果。
func (s *PaymentProxyService) MarkCheck(ctx context.Context, id uint64, ok bool, latencyMs int, errMsg string) error {
	if errMsg != "" && len(errMsg) > 240 {
		errMsg = errMsg[:240]
	}
	return s.repo.MarkCheck(ctx, id, ok, latencyMs, errMsg)
}

func paymentProxyToResp(p *model.PaymentProxyPool) *dto.PaymentProxyResp {
	r := &dto.PaymentProxyResp{
		ID:          p.ID,
		Name:        p.Name,
		Scheme:      p.Scheme,
		Port:        p.Port,
		HasPassword: len(p.PasswordEnc) > 0,
		Country:     p.Country,
		Status:      p.Status,
		TotalUsed:   p.TotalUsed,
		TotalFailed: p.TotalFailed,
		LastCheckOK: p.LastCheckOK,
		LastCheckMs: p.LastCheckMs,
		CreatedAt:   p.CreatedAt.Unix(),
		UpdatedAt:   p.UpdatedAt.Unix(),
	}
	if p.Host != nil {
		r.Host = *p.Host
	}
	if p.Username != nil {
		r.Username = *p.Username
	}
	if p.APIURL != nil {
		r.APIURL = *p.APIURL
	}
	if p.LastUsedAt != nil {
		r.LastUsedAt = p.LastUsedAt.Unix()
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

func validatePaymentProxyScheme(s string) bool {
	switch s {
	case "http", "https", "socks5", "socks5h":
		return true
	}
	return false
}

func parsePaymentProxyLine(line, country, remark string, idx int) (*dto.PaymentProxyCreateReq, error) {
	// 优先尝试 URL 形式
	if strings.Contains(line, "://") {
		u, err := url.Parse(line)
		if err != nil || u == nil || u.Host == "" {
			return nil, errors.New("invalid url")
		}
		host := u.Hostname()
		portNum, err := strconv.Atoi(u.Port())
		if err != nil || portNum <= 0 {
			return nil, errors.New("invalid port")
		}
		req := &dto.PaymentProxyCreateReq{
			Name:    fmt.Sprintf("Payment %02d - %s://%s:%d", idx, u.Scheme, host, portNum),
			Scheme:  strings.ToLower(u.Scheme),
			Host:    host,
			Port:    portNum,
			Country: country,
			Remark:  remark,
		}
		if u.User != nil {
			req.Username = u.User.Username()
			if pw, ok := u.User.Password(); ok {
				req.Password = pw
			}
		}
		return req, nil
	}

	// 形如 host:port:user:pass
	parts := strings.SplitN(line, ":", 4)
	if len(parts) < 2 {
		return nil, errors.New("bad format")
	}
	portNum, err := strconv.Atoi(parts[1])
	if err != nil || portNum <= 0 {
		return nil, errors.New("invalid port")
	}
	req := &dto.PaymentProxyCreateReq{
		Name:    fmt.Sprintf("Payment %02d - %s:%d", idx, parts[0], portNum),
		Scheme:  "http",
		Host:    parts[0],
		Port:    portNum,
		Country: country,
		Remark:  remark,
	}
	if len(parts) >= 3 {
		req.Username = parts[2]
	}
	if len(parts) >= 4 {
		req.Password = parts[3]
	}
	return req, nil
}

// fetchProxyAPI 调用动态拨号 API 取代理；约定首行为代理本身。
func fetchProxyAPI(ctx context.Context, apiURL string, timeout time.Duration) (string, error) {
	cli := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	r, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "", errors.New("api_url returned empty")
}

// normalizeProxyEndpoint 把 host:port:user:pass 等格式归一为 scheme://user:pass@host:port。
func normalizeProxyEndpoint(raw, scheme string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	if strings.Contains(raw, "@") {
		return scheme + "://" + raw
	}
	parts := strings.Split(raw, ":")
	if len(parts) == 4 {
		return fmt.Sprintf("%s://%s:%s@%s:%s",
			scheme,
			url.QueryEscape(parts[2]),
			url.QueryEscape(parts[3]),
			parts[0], parts[1],
		)
	}
	if len(parts) == 2 {
		return fmt.Sprintf("%s://%s:%s", scheme, parts[0], parts[1])
	}
	return scheme + "://" + raw
}
