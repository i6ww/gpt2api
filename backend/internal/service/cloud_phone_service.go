package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/geelark"
)

// CloudPhoneService GeeLark 云手机管理。
//
// 这一层只管资源的 CRUD + token 加密；真正调 GeeLark API 的逻辑放在
// regkit/cloudphone 包，由 dispatcher 拉起 Plus 升级任务时调用。
type CloudPhoneService struct {
	repo *repo.CloudPhonePoolRepo
	aes  *crypto.AESGCM
}

// NewCloudPhoneService 构造。
func NewCloudPhoneService(r *repo.CloudPhonePoolRepo, aes *crypto.AESGCM) *CloudPhoneService {
	return &CloudPhoneService{repo: r, aes: aes}
}

// Create 新建云手机。
func (s *CloudPhoneService) Create(ctx context.Context, adminID uint64, req *dto.CloudPhoneCreateReq) (*model.CloudPhonePool, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return nil, errcode.InvalidParam.WithMsg("id is required")
	}

	enc, err := s.aes.Encrypt([]byte(strings.TrimSpace(req.GLToken)))
	if err != nil {
		return nil, errcode.Internal.Wrap(err)
	}

	preferAPI := int8(1)
	if req.PreferAPI != nil {
		preferAPI = *req.PreferAPI
	}

	cc := strings.TrimPrefix(strings.TrimSpace(req.CountryCode), "+")
	if cc == "" {
		cc = "62" // 默认印尼，跟 GoPay 流程主场景一致
	}
	p := &model.CloudPhonePool{
		ID:          id,
		Name:        strings.TrimSpace(req.Name),
		GLTokenEnc:  enc,
		PreferAPI:   preferAPI,
		CountryCode: cc,
		PhoneNumber: normalizePhone(req.PhoneNumber),
		Status:      model.CloudPhoneStatusOnline,
		CreatedBy:   &adminID,
	}
	if v := strings.TrimSpace(req.ADBAddr); v != "" {
		p.ADBAddr = &v
	}
	if v := strings.TrimSpace(req.Remark); v != "" {
		p.Remark = &v
	}

	if err := s.repo.Create(ctx, p); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return p, nil
}

// Update 编辑云手机。
func (s *CloudPhoneService) Update(ctx context.Context, id string, req *dto.CloudPhoneUpdateReq) error {
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
	if req.GLToken != nil {
		v := strings.TrimSpace(*req.GLToken)
		if v != "" {
			enc, err := s.aes.Encrypt([]byte(v))
			if err != nil {
				return errcode.Internal.Wrap(err)
			}
			fields["gl_token_enc"] = enc
		}
	}
	if req.ADBAddr != nil {
		v := strings.TrimSpace(*req.ADBAddr)
		if v == "" {
			fields["adb_addr"] = nil
		} else {
			fields["adb_addr"] = v
		}
	}
	if req.PreferAPI != nil {
		fields["prefer_api"] = *req.PreferAPI
	}
	if req.CountryCode != nil {
		v := strings.TrimPrefix(strings.TrimSpace(*req.CountryCode), "+")
		if v == "" {
			v = "62"
		}
		fields["country_code"] = v
	}
	if req.PhoneNumber != nil {
		fields["phone_number"] = normalizePhone(*req.PhoneNumber)
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
func (s *CloudPhoneService) Delete(ctx context.Context, id string) error {
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
func (s *CloudPhoneService) BatchDelete(ctx context.Context, ids []string) (int64, error) {
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
func (s *CloudPhoneService) List(ctx context.Context, req *dto.CloudPhoneListReq) ([]*dto.CloudPhoneResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.CloudPhoneFilter{
		Status:   req.Status,
		Keyword:  req.Keyword,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	resp := make([]*dto.CloudPhoneResp, 0, len(items))
	for _, it := range items {
		resp = append(resp, cloudPhoneToResp(it))
	}
	return resp, total, nil
}

// Stats 状态统计。
func (s *CloudPhoneService) Stats(ctx context.Context) (map[string]int64, error) {
	out, err := s.repo.Stats(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return out, nil
}

// ResolveToken 解密 GeeLark token；dispatcher / regkit/cloudphone 调用。
func (s *CloudPhoneService) ResolveToken(p *model.CloudPhonePool) (string, error) {
	if len(p.GLTokenEnc) == 0 {
		return "", nil
	}
	plain, err := s.aes.Decrypt(p.GLTokenEnc)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// GetByID 给 dispatcher 用：拿到 model 后再 ResolveToken 拼出 GeeLark Bearer。
func (s *CloudPhoneService) GetByID(ctx context.Context, id string) (*model.CloudPhonePool, error) {
	p, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// MarkCheck 记录探测结果（健康检查接口调用）。
func (s *CloudPhoneService) MarkCheck(ctx context.Context, id string, ok bool, errMsg string) error {
	if err := s.repo.MarkCheck(ctx, id, ok, errMsg); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// BatchImport 批量导入。
//
// text 格式（一行一条，分隔符 |，列从左到右）：
//
//	phone_id|gl_token[|adb_addr][|name][|country_code][|phone_number][|remark]
//
// 老格式（4 列以内）继续兼容；从第 5 列起按 country_code / phone_number / remark
// 顺序读取，缺省字段空字符串。
func (s *CloudPhoneService) BatchImport(ctx context.Context, adminID uint64, req *dto.CloudPhoneImportReq) (*dto.CloudPhoneImportResult, error) {
	imported := 0
	updated := 0
	skipped := 0
	errs := []string{}

	apply := func(item *dto.CloudPhoneCreateReq) {
		// 已存在则更新 token；否则新建
		exist, getErr := s.repo.GetByID(ctx, strings.TrimSpace(item.ID))
		if getErr != nil && !errors.Is(getErr, repo.ErrNotFound) {
			skipped++
			errs = append(errs, item.ID+": "+getErr.Error())
			return
		}
		if exist != nil {
			fields := map[string]any{}
			if item.GLToken != "" {
				enc, err := s.aes.Encrypt([]byte(strings.TrimSpace(item.GLToken)))
				if err != nil {
					skipped++
					errs = append(errs, item.ID+": encrypt failed")
					return
				}
				fields["gl_token_enc"] = enc
			}
			if v := strings.TrimSpace(item.ADBAddr); v != "" {
				fields["adb_addr"] = v
			}
			if v := strings.TrimSpace(item.Name); v != "" {
				fields["name"] = v
			}
			if v := strings.TrimPrefix(strings.TrimSpace(item.CountryCode), "+"); v != "" {
				fields["country_code"] = v
			}
			if v := normalizePhone(item.PhoneNumber); v != "" {
				fields["phone_number"] = v
			}
			if v := strings.TrimSpace(item.Remark); v != "" {
				fields["remark"] = v
			}
			if err := s.repo.Update(ctx, exist.ID, fields); err != nil {
				skipped++
				errs = append(errs, item.ID+": "+err.Error())
				return
			}
			updated++
			return
		}
		if _, err := s.Create(ctx, adminID, item); err != nil {
			skipped++
			errs = append(errs, item.ID+": "+err.Error())
			return
		}
		imported++
	}

	if len(req.Items) > 0 {
		for i := range req.Items {
			it := req.Items[i]
			apply(&it)
		}
	}

	if t := strings.TrimSpace(req.Text); t != "" {
		for _, raw := range strings.Split(strings.ReplaceAll(t, "\r\n", "\n"), "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Split(line, "|")
			if len(parts) < 2 {
				skipped++
				errs = append(errs, "bad line: "+line)
				continue
			}
			it := dto.CloudPhoneCreateReq{
				ID:      strings.TrimSpace(parts[0]),
				GLToken: strings.TrimSpace(parts[1]),
			}
			if len(parts) >= 3 {
				it.ADBAddr = strings.TrimSpace(parts[2])
			}
			if len(parts) >= 4 {
				it.Name = strings.TrimSpace(parts[3])
			}
			if len(parts) >= 5 {
				it.CountryCode = strings.TrimSpace(parts[4])
			}
			if len(parts) >= 6 {
				it.PhoneNumber = strings.TrimSpace(parts[5])
			}
			if len(parts) >= 7 {
				it.Remark = strings.TrimSpace(parts[6])
			}
			apply(&it)
		}
	}

	return &dto.CloudPhoneImportResult{
		Imported: imported,
		Updated:  updated,
		Skipped:  skipped,
		Errors:   errs,
	}, nil
}

// GopayUnlinkOpenAI 在云手机前台自动化操作 GoPay：Profil → 设置 → 已连接应用 →
// OpenAI →「Hapus」。依赖 GeeLark /shell/execute 与无障碍树（uiautomator dump）。
//
// geeLarkBase 传系统配置 `geelark.api_base`；空则使用 geelark 默认 OpenAPI 地址。
func (s *CloudPhoneService) GopayUnlinkOpenAI(ctx context.Context, phoneID string, geeLarkBase string, appPackage string) error {
	if strings.TrimSpace(phoneID) == "" {
		return errcode.InvalidParam.WithMsg("云手机 ID 为空")
	}
	p, err := s.GetByID(ctx, phoneID)
	if err != nil {
		return err
	}
	token, err := s.ResolveToken(p)
	if err != nil {
		return err
	}
	base := strings.TrimRight(strings.TrimSpace(geeLarkBase), "/")
	if base == "" {
		base = geelark.DefaultBaseURL
	}
	gl := geelark.New(geelark.Options{BaseURL: base, Timeout: 45 * time.Second})
	if err := gl.EnsureOnline(ctx, token, p.ID, geelark.PhoneStartWaitTimeout); err != nil {
		return errcode.Internal.Wrap(err)
	}
	opt := geelark.UnlinkOpenAIOptions{AppPackage: appPackage}
	if err := gl.UnlinkOpenAIInGopay(ctx, token, p.ID, opt); err != nil {
		return errcode.Internal.Wrap(err)
	}
	return nil
}

func cloudPhoneToResp(p *model.CloudPhonePool) *dto.CloudPhoneResp {
	r := &dto.CloudPhoneResp{
		ID:          p.ID,
		Name:        p.Name,
		HasGLToken:  len(p.GLTokenEnc) > 0,
		PreferAPI:   p.PreferAPI,
		CountryCode: p.CountryCode,
		PhoneNumber: p.PhoneNumber,
		PhoneMasked: maskPhone(p.PhoneNumber),
		Status:      p.Status,
		LastCheckOK: p.LastCheckOK,
		CreatedAt:   p.CreatedAt.Unix(),
		UpdatedAt:   p.UpdatedAt.Unix(),
	}
	if p.ADBAddr != nil {
		r.ADBAddr = *p.ADBAddr
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
