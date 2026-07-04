package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
)

// GopayWalletService GoPay 钱包池管理。
//
// 这一层负责钱包 CRUD、导入导出、PIN 加解密、binding 列表/取消订阅；
// dispatcher 真正抢锁 / 完成 binding 写入由本服务的 Lease/CompleteSuccess
// 等高层方法承担。
type GopayWalletService struct {
	walletRepo  *repo.GopayWalletPoolRepo
	bindingRepo *repo.GopayWalletBindingRepo
	phoneRepo   *repo.CloudPhonePoolRepo // 用于反查手机号填充 resp
	aes         *crypto.AESGCM
	db          *gorm.DB
}

// NewGopayWalletService 构造。
func NewGopayWalletService(walletRepo *repo.GopayWalletPoolRepo, bindingRepo *repo.GopayWalletBindingRepo, phoneRepo *repo.CloudPhonePoolRepo, aes *crypto.AESGCM, db *gorm.DB) *GopayWalletService {
	return &GopayWalletService{
		walletRepo:  walletRepo,
		bindingRepo: bindingRepo,
		phoneRepo:   phoneRepo,
		aes:         aes,
		db:          db,
	}
}

// Create 新建钱包。
//
// 钱包不再独立保存手机号 / 国家码；要求 cloud_phone_id 必填，dispatcher 跑流程
// 时从 cloud_phone 拿手机号。这里同时做一次 cloud_phone 存在性校验，避免脏数据。
func (s *GopayWalletService) Create(ctx context.Context, adminID uint64, req *dto.GopayWalletCreateReq) (*model.GopayWalletPool, error) {
	pin := strings.TrimSpace(req.PIN)
	if pin == "" {
		return nil, errcode.InvalidParam.WithMsg("pin is required")
	}
	cpID := strings.TrimSpace(req.CloudPhoneID)
	if cpID == "" {
		return nil, errcode.InvalidParam.WithMsg("cloud_phone_id is required")
	}
	if s.phoneRepo != nil {
		if _, perr := s.phoneRepo.GetByID(ctx, cpID); perr != nil {
			if errors.Is(perr, repo.ErrNotFound) {
				return nil, errcode.InvalidParam.WithMsg("cloud_phone not found: " + cpID)
			}
			return nil, errcode.DBError.Wrap(perr)
		}
	}
	enc, err := s.aes.Encrypt([]byte(pin))
	if err != nil {
		return nil, errcode.Internal.Wrap(err)
	}

	w := &model.GopayWalletPool{
		PINEnc:       enc,
		CloudPhoneID: cpID,
		Status:       model.GopayWalletStatusAvailable,
		CreatedBy:    &adminID,
	}
	if v := strings.TrimSpace(req.Remark); v != "" {
		w.Remark = &v
	}

	if err := s.walletRepo.Create(ctx, w); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return w, nil
}

// Update 编辑钱包。
func (s *GopayWalletService) Update(ctx context.Context, id uint64, req *dto.GopayWalletUpdateReq) error {
	if _, err := s.walletRepo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}

	fields := map[string]any{}
	if req.PIN != nil {
		v := strings.TrimSpace(*req.PIN)
		if v != "" {
			enc, err := s.aes.Encrypt([]byte(v))
			if err != nil {
				return errcode.Internal.Wrap(err)
			}
			fields["pin_enc"] = enc
		}
	}
	if req.CloudPhoneID != nil {
		fields["cloud_phone_id"] = strings.TrimSpace(*req.CloudPhoneID)
	}
	if req.Status != nil {
		fields["status"] = *req.Status
	}
	if req.ActivePlusCount != nil {
		fields["active_plus_count"] = *req.ActivePlusCount
	}
	if req.Remark != nil {
		v := strings.TrimSpace(*req.Remark)
		if v == "" {
			fields["remark"] = nil
		} else {
			fields["remark"] = v
		}
	}

	if err := s.walletRepo.Update(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// Delete 软删除。
func (s *GopayWalletService) Delete(ctx context.Context, id uint64) error {
	if _, err := s.walletRepo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing
		}
		return errcode.DBError.Wrap(err)
	}
	if err := s.walletRepo.SoftDelete(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// BatchDelete 批量软删除。
func (s *GopayWalletService) BatchDelete(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, errcode.InvalidParam.WithMsg("ids is required")
	}
	n, err := s.walletRepo.SoftDeleteByIDs(ctx, ids)
	if err != nil {
		return 0, errcode.DBError.Wrap(err)
	}
	return n, nil
}

// List 分页列表（含 quota 过滤）。
//
// 列表 resp 需要展示手机号 / 云手机名，这两项来自 cloud_phone_pool。这里
// 先批量拉钱包，再按 cloud_phone_id 去重 fetch 一次 cloud_phone，最后填入。
func (s *GopayWalletService) List(ctx context.Context, req *dto.GopayWalletListReq, perWalletQuota int) ([]*dto.GopayWalletResp, int64, error) {
	items, total, err := s.walletRepo.List(ctx, repo.GopayWalletFilter{
		Status:         req.Status,
		CloudPhoneID:   req.CloudPhoneID,
		Keyword:        req.Keyword,
		Page:           req.Page,
		PageSize:       req.PageSize,
		HasAvailableOn: req.HasAvailableOn,
		Quota:          perWalletQuota,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	phoneMap := s.fetchPhonesByWallets(ctx, items)
	resp := make([]*dto.GopayWalletResp, 0, len(items))
	for _, it := range items {
		resp = append(resp, gopayWalletToResp(it, phoneMap[it.CloudPhoneID]))
	}
	return resp, total, nil
}

// fetchPhonesByWallets 把列表里出现过的 cloud_phone_id 批量加载，
// 返回 id → CloudPhonePool。失败的（不存在 / 已删）忽略；调用方按 nil 容错。
func (s *GopayWalletService) fetchPhonesByWallets(ctx context.Context, items []*model.GopayWalletPool) map[string]*model.CloudPhonePool {
	out := map[string]*model.CloudPhonePool{}
	if s.phoneRepo == nil || len(items) == 0 {
		return out
	}
	seen := map[string]struct{}{}
	for _, it := range items {
		if it.CloudPhoneID == "" {
			continue
		}
		if _, ok := seen[it.CloudPhoneID]; ok {
			continue
		}
		seen[it.CloudPhoneID] = struct{}{}
		p, err := s.phoneRepo.GetByID(ctx, it.CloudPhoneID)
		if err == nil && p != nil {
			out[it.CloudPhoneID] = p
		}
	}
	return out
}

// Stats 状态统计。
func (s *GopayWalletService) Stats(ctx context.Context) (map[string]int64, error) {
	out, err := s.walletRepo.Stats(ctx)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return out, nil
}

// GetByID 给 dispatcher 用。
func (s *GopayWalletService) GetByID(ctx context.Context, id uint64) (*model.GopayWalletPool, error) {
	w, err := s.walletRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return w, nil
}

// ResolvePIN 解密 PIN。dispatcher 调用。
func (s *GopayWalletService) ResolvePIN(w *model.GopayWalletPool) (string, error) {
	if len(w.PINEnc) == 0 {
		return "", nil
	}
	plain, err := s.aes.Decrypt(w.PINEnc)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// SecretsByID 编辑表单需要明文 PIN 时调用。
func (s *GopayWalletService) SecretsByID(ctx context.Context, id uint64) (*dto.GopayWalletSecretResp, error) {
	w, err := s.walletRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errcode.ResourceMissing
		}
		return nil, errcode.DBError.Wrap(err)
	}
	pin, err := s.ResolvePIN(w)
	if err != nil {
		return nil, errcode.Internal.Wrap(err)
	}
	return &dto.GopayWalletSecretResp{PIN: pin}, nil
}

// LeaseAvailable dispatcher 抢锁入口。
func (s *GopayWalletService) LeaseAvailable(ctx context.Context, perWalletQuota int) (*model.GopayWalletPool, error) {
	w, err := s.walletRepo.LeaseAvailable(ctx, perWalletQuota)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return w, nil
}

// Release dispatcher 释放（不做 success / failed 计数）。
func (s *GopayWalletService) Release(ctx context.Context, id uint64) error {
	return s.walletRepo.Release(ctx, id)
}

// MarkSuccess dispatcher 在成功收尾时调用。
func (s *GopayWalletService) MarkSuccess(ctx context.Context, id uint64, perWalletQuota int) error {
	return s.walletRepo.MarkSuccess(ctx, id, perWalletQuota)
}

// MarkFailed dispatcher 在失败收尾时调用。
func (s *GopayWalletService) MarkFailed(ctx context.Context, id uint64, reason string, cooldownMin int) error {
	return s.walletRepo.MarkFailed(ctx, id, reason, cooldownMin)
}

// MarkBanned dispatcher 在判定钱包永久不可用时调用。
func (s *GopayWalletService) MarkBanned(ctx context.Context, id uint64, reason string) error {
	return s.walletRepo.MarkBanned(ctx, id, reason)
}

// CreateBindingAndMarkSuccess dispatcher 在 GoPay 流程成功后一次性写：
//   - INSERT gopay_wallet_binding
//   - UPDATE wallet active_plus_count++ / total_success++ / 转 available 或 exhausted
//
// 全部走同一事务，保证 wallet 计数 / binding 始终一致。
func (s *GopayWalletService) CreateBindingAndMarkSuccess(
	ctx context.Context,
	binding *model.GopayWalletBinding,
	perWalletQuota int,
) error {
	if perWalletQuota <= 0 {
		perWalletQuota = 30
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(binding).Error; err != nil {
			return err
		}
		var w model.GopayWalletPool
		if err := tx.First(&w, binding.WalletID).Error; err != nil {
			return err
		}
		w.ActivePlusCount++
		w.TotalSuccess++
		updates := map[string]any{
			"active_plus_count": w.ActivePlusCount,
			"total_success":     w.TotalSuccess,
			"last_used_at":      binding.ChargedAt,
			"last_error":        nil,
			"cooldown_until":    nil,
		}
		if w.ActivePlusCount >= perWalletQuota {
			updates["status"] = model.GopayWalletStatusExhausted
		} else {
			updates["status"] = model.GopayWalletStatusAvailable
		}
		return tx.Model(&model.GopayWalletPool{}).
			Where("id = ?", binding.WalletID).Updates(updates).Error
	})
}

// CancelBinding 主动取消订阅 → binding=cancelled，wallet active_plus_count - 1。
func (s *GopayWalletService) CancelBinding(ctx context.Context, bindingID uint64, note string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b model.GopayWalletBinding
		if err := tx.First(&b, bindingID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errcode.ResourceMissing
			}
			return err
		}
		if b.Status != model.GopayBindingStatusActive {
			return errcode.InvalidParam.WithMsg(fmt.Sprintf("binding 状态非 active：%s", b.Status))
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"status":       model.GopayBindingStatusCancelled,
			"cancelled_at": now,
		}
		if note != "" {
			updates["note"] = truncate(note, 240)
		}
		if err := tx.Model(&model.GopayWalletBinding{}).
			Where("id = ?", bindingID).Updates(updates).Error; err != nil {
			return err
		}

		var w model.GopayWalletPool
		if err := tx.First(&w, b.WalletID).Error; err != nil {
			return err
		}
		if w.ActivePlusCount > 0 {
			w.ActivePlusCount--
		}
		walletUpdates := map[string]any{
			"active_plus_count": w.ActivePlusCount,
		}
		if w.Status == model.GopayWalletStatusExhausted {
			walletUpdates["status"] = model.GopayWalletStatusAvailable
		}
		return tx.Model(&model.GopayWalletPool{}).
			Where("id = ?", b.WalletID).Updates(walletUpdates).Error
	})
}

// ListBindings 绑定列表。
func (s *GopayWalletService) ListBindings(ctx context.Context, req *dto.GopayBindingListReq) ([]*dto.GopayBindingResp, int64, error) {
	items, total, err := s.bindingRepo.List(ctx, repo.GopayBindingFilter{
		WalletID:     req.WalletID,
		GptAccountID: req.GptAccountID,
		Status:       req.Status,
		Page:         req.Page,
		PageSize:     req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	resp := make([]*dto.GopayBindingResp, 0, len(items))
	for _, it := range items {
		resp = append(resp, gopayBindingToResp(it))
	}
	return resp, total, nil
}

// BatchImport 批量导入。
//
// text 一行一条：pin|cloud_phone_id[|remark]
//
// 比对旧格式：手机号字段已删（信息源在 cloud_phone_pool），导入更轻量。
func (s *GopayWalletService) BatchImport(ctx context.Context, adminID uint64, req *dto.GopayWalletImportReq) (*dto.GopayWalletImportResult, error) {
	imported := 0
	skipped := 0
	errs := []string{}

	apply := func(item *dto.GopayWalletCreateReq) {
		if _, err := s.Create(ctx, adminID, item); err != nil {
			skipped++
			errs = append(errs, fmt.Sprintf("%s: %v", item.CloudPhoneID, err))
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
			it := dto.GopayWalletCreateReq{
				PIN:          strings.TrimSpace(parts[0]),
				CloudPhoneID: strings.TrimSpace(parts[1]),
			}
			if len(parts) >= 3 {
				it.Remark = strings.TrimSpace(parts[2])
			}
			apply(&it)
		}
	}

	return &dto.GopayWalletImportResult{
		Imported: imported,
		Skipped:  skipped,
		Errors:   errs,
	}, nil
}

// gopayWalletToResp 把 wallet model 转 resp。phone 可为 nil（云手机被删/找不到时）。
//
// 手机号 / 国家码字段全部从 phone 里取，wallet 自身不再存。
func gopayWalletToResp(w *model.GopayWalletPool, phone *model.CloudPhonePool) *dto.GopayWalletResp {
	r := &dto.GopayWalletResp{
		ID:              w.ID,
		HasPIN:          len(w.PINEnc) > 0,
		CloudPhoneID:    w.CloudPhoneID,
		Status:          w.Status,
		ActivePlusCount: w.ActivePlusCount,
		TotalSuccess:    w.TotalSuccess,
		TotalFailed:     w.TotalFailed,
		CreatedAt:       w.CreatedAt.Unix(),
		UpdatedAt:       w.UpdatedAt.Unix(),
	}
	if phone != nil {
		r.CountryCode = phone.CountryCode
		r.PhoneNumber = phone.PhoneNumber
		r.PhoneMasked = maskPhone(phone.PhoneNumber)
		r.CloudPhoneName = phone.Name
	}
	if w.LastUsedAt != nil {
		r.LastUsedAt = w.LastUsedAt.Unix()
	}
	if w.LastError != nil {
		r.LastError = *w.LastError
	}
	if w.CooldownUntil != nil {
		r.CooldownUntil = w.CooldownUntil.Unix()
	}
	if w.Remark != nil {
		r.Remark = *w.Remark
	}
	return r
}

func gopayBindingToResp(b *model.GopayWalletBinding) *dto.GopayBindingResp {
	r := &dto.GopayBindingResp{
		ID:           b.ID,
		WalletID:     b.WalletID,
		GptAccountID: b.GptAccountID,
		AmountIDR:    b.AmountIDR,
		ChargedAt:    b.ChargedAt.Unix(),
		ExpiresAt:    b.ExpiresAt.Unix(),
		Status:       b.Status,
	}
	if b.CSID != nil {
		r.CSID = *b.CSID
	}
	if b.ChargeRef != nil {
		r.ChargeRef = *b.ChargeRef
	}
	if b.CancelledAt != nil {
		r.CancelledAt = b.CancelledAt.Unix()
	}
	if b.Note != nil {
		r.Note = *b.Note
	}
	return r
}

func normalizePhone(s string) string {
	out := strings.Builder{}
	for _, r := range strings.TrimSpace(s) {
		if r >= '0' && r <= '9' {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func maskPhone(p string) string {
	if len(p) < 7 {
		return p
	}
	return p[:3] + strings.Repeat("*", len(p)-6) + p[len(p)-3:]
}
