// Package service 兑换码（CDK） / 优惠码（Promo） 服务。
//
// 仅支持 reward_type=points 的最小实现：reward_value JSON 形如 {"points": 10000}（10000 = 100 点）。
package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/logger"
)

// CDKService 兑换码服务。
type CDKService struct {
	db      *gorm.DB
	billing *BillingService
}

// NewCDKService 构造。
func NewCDKService(db *gorm.DB, b *BillingService) *CDKService {
	return &CDKService{db: db, billing: b}
}

// Redeem 用户兑换 CDK。
func (s *CDKService) Redeem(ctx context.Context, userID uint64, code string) (int64, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return 0, errcode.InvalidParam
	}

	var grantedPoints int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c model.RedeemCode
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Where("code = ?", code).First(&c).Error; err != nil {
			return errcode.CDKInvalid
		}
		if c.Status != model.CDKStatusUnused {
			return errcode.CDKUsed
		}

		var batch model.RedeemCodeBatch
		if err := tx.Where("id = ?", c.BatchID).First(&batch).Error; err != nil {
			return errcode.CDKInvalid
		}
		now := time.Now().UTC()
		if batch.Status != model.PromoStatusEnabled {
			return errcode.CDKInvalid
		}
		if batch.ExpireAt != nil && now.After(*batch.ExpireAt) {
			return errcode.CDKInvalid.WithMsg("兑换码已过期")
		}

		// per_user_limit：同一用户在该批次最多兑换 N 次
		if batch.PerUserLimit > 0 {
			var used int64
			if err := tx.Model(&model.RedeemCode{}).
				Where("batch_id = ? AND used_by = ?", batch.ID, userID).
				Count(&used).Error; err != nil {
				return errcode.DBError.Wrap(err)
			}
			if int(used) >= batch.PerUserLimit {
				return errcode.CDKUsed.WithMsg("已达每用户兑换上限")
			}
		}

		// 解析 reward_value
		points, err := parsePointsReward(batch.RewardType, batch.RewardValue)
		if err != nil {
			return errcode.Internal.Wrap(err)
		}
		if points <= 0 {
			return errcode.Internal.WithMsg("invalid reward")
		}

		// 标记已使用
		if err := tx.Model(&model.RedeemCode{}).
			Where("id = ? AND status = ?", c.ID, model.CDKStatusUnused).
			Updates(map[string]any{
				"status":  model.CDKStatusUsed,
				"used_by": userID,
				"used_at": now,
			}).Error; err != nil {
			return errcode.DBError.Wrap(err)
		}
		// 更新 batch.used_qty
		if err := tx.Model(&model.RedeemCodeBatch{}).
			Where("id = ?", batch.ID).
			UpdateColumn("used_qty", gorm.Expr("used_qty + 1")).Error; err != nil {
			return errcode.DBError.Wrap(err)
		}
		grantedPoints = points
		return nil
	})
	if err != nil {
		return 0, err
	}

	// CDK 兑换走 GrantPoints（独立事务，幂等容易处理）
	bizID := fmt.Sprintf("cdk:%s", code)
	if err := s.billing.GrantPoints(ctx, userID, model.BizCDK, bizID, grantedPoints, "redeem code"); err != nil {
		logger.FromCtx(ctx).Error("cdk.grant_points", zap.String("code", code), zap.Error(err))
		return 0, err
	}
	return grantedPoints, nil
}

// GenerateBatch 管理后台生成 CDK 批次。
func (s *CDKService) GenerateBatch(ctx context.Context, adminID uint64, batchNo, name string, points int64, qty, perUserLimit int, expireAt *time.Time) (*model.RedeemCodeBatch, error) {
	if points <= 0 || qty <= 0 || qty > 100000 {
		return nil, errcode.InvalidParam
	}
	rewardJSON, _ := json.Marshal(map[string]any{"points": points})

	batch := &model.RedeemCodeBatch{
		BatchNo:      batchNo,
		Name:         name,
		RewardType:   "points",
		RewardValue:  string(rewardJSON),
		TotalQty:     qty,
		PerUserLimit: perUserLimit,
		ExpireAt:     expireAt,
		Status:       model.PromoStatusEnabled,
		CreatedBy:    &adminID,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(batch).Error; err != nil {
			return err
		}
		codes, err := buildUniqueCDKBatch(tx, batch.ID, qty)
		if err != nil {
			return err
		}
		return tx.CreateInBatches(codes, 500).Error
	})
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return batch, nil
}

// BatchListFilter 批次分页过滤。
type BatchListFilter struct {
	Keyword  string
	Status   *int
	Page     int
	PageSize int
}

// ListBatches 列出所有 CDK 批次（运营汇总用）。
//
// 列表里需要展示 used_qty / revoked_qty / remaining，这些字段一部分存在
// redeem_code_batch.used_qty（受 Redeem 时增量更新维护），revoked 数则要现算
// （单批次内 status=2 的总数）。批次条数通常很少（一年几十到几百级别），
// 直接 GROUP BY 取所有批次的吊销数后在内存里合并，避免每行 N+1。
func (s *CDKService) ListBatches(ctx context.Context, f BatchListFilter) ([]*model.RedeemCodeBatch, map[uint64]int, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}
	q := s.db.WithContext(ctx).Model(&model.RedeemCodeBatch{})
	if f.Status != nil {
		q = q.Where("status = ?", *f.Status)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("CAST(id AS CHAR) = ? OR batch_no LIKE ? OR name LIKE ?", kw, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, nil, 0, errcode.DBError.Wrap(err)
	}
	var rows []*model.RedeemCodeBatch
	if err := q.Order("id DESC").Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&rows).Error; err != nil {
		return nil, nil, 0, errcode.DBError.Wrap(err)
	}
	revoked := make(map[uint64]int, len(rows))
	if len(rows) > 0 {
		ids := make([]uint64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		type row struct {
			BatchID uint64
			Cnt     int
		}
		var counts []row
		if err := s.db.WithContext(ctx).
			Model(&model.RedeemCode{}).
			Select("batch_id, COUNT(*) AS cnt").
			Where("batch_id IN ? AND status = ?", ids, model.CDKStatusInvalid).
			Group("batch_id").
			Find(&counts).Error; err != nil {
			return nil, nil, 0, errcode.DBError.Wrap(err)
		}
		for _, c := range counts {
			revoked[c.BatchID] = c.Cnt
		}
	}
	return rows, revoked, total, nil
}

// GetBatch 取单个批次详情。
func (s *CDKService) GetBatch(ctx context.Context, id uint64) (*model.RedeemCodeBatch, int, error) {
	var batch model.RedeemCodeBatch
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&batch).Error; err != nil {
		return nil, 0, errcode.ResourceMissing
	}
	var revoked int64
	if err := s.db.WithContext(ctx).
		Model(&model.RedeemCode{}).
		Where("batch_id = ? AND status = ?", id, model.CDKStatusInvalid).
		Count(&revoked).Error; err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	return &batch, int(revoked), nil
}

// ToggleBatch 启用 / 停用整个批次。停用后批次中所有「未使用」的码都无法兑换，
// 但已使用记录保留（兑换历史里照常显示）。
func (s *CDKService) ToggleBatch(ctx context.Context, id uint64, status int8) error {
	if status != model.PromoStatusEnabled && status != model.PromoStatusDisabled {
		return errcode.InvalidParam
	}
	res := s.db.WithContext(ctx).Model(&model.RedeemCodeBatch{}).
		Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return errcode.DBError.Wrap(res.Error)
	}
	if res.RowsAffected == 0 {
		return errcode.ResourceMissing
	}
	return nil
}

// AppendBatch 给已有批次再追加生成 N 张未使用的码。
//
// 用于"运营临时加发"场景，免去重新建一个批次。新追加的码与老码使用同样的
// reward_value / expire_at / per_user_limit，受同一个 batch.status 控制。
func (s *CDKService) AppendBatch(ctx context.Context, id uint64, qty int) (int, *model.RedeemCodeBatch, error) {
	if qty <= 0 || qty > 100000 {
		return 0, nil, errcode.InvalidParam
	}
	var batch model.RedeemCodeBatch
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Where("id = ?", id).First(&batch).Error; err != nil {
			return errcode.ResourceMissing
		}
		codes, err := buildUniqueCDKBatch(tx, batch.ID, qty)
		if err != nil {
			return err
		}
		if err := tx.CreateInBatches(codes, 500).Error; err != nil {
			return err
		}
		return tx.Model(&model.RedeemCodeBatch{}).
			Where("id = ?", batch.ID).
			UpdateColumn("total_qty", gorm.Expr("total_qty + ?", qty)).Error
	})
	if err != nil {
		return 0, nil, err
	}
	batch.TotalQty += qty
	return qty, &batch, nil
}

// CodeListFilter 批次内单码分页过滤。
type CodeListFilter struct {
	BatchID  uint64
	Status   *int
	Keyword  string
	Page     int
	PageSize int
}

// ListCodes 列出某批次的单码（运营审计 / 客服查码用）。
func (s *CDKService) ListCodes(ctx context.Context, f CodeListFilter) ([]*model.RedeemCode, int64, error) {
	if f.BatchID == 0 {
		return nil, 0, errcode.InvalidParam
	}
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 500 {
		f.PageSize = 50
	}
	q := s.db.WithContext(ctx).Model(&model.RedeemCode{}).Where("batch_id = ?", f.BatchID)
	if f.Status != nil {
		q = q.Where("status = ?", *f.Status)
	}
	if kw := strings.TrimSpace(strings.ToUpper(f.Keyword)); kw != "" {
		q = q.Where("code LIKE ?", "%"+kw+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	var rows []*model.RedeemCode
	if err := q.Order("id ASC").Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&rows).Error; err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	return rows, total, nil
}

// RevokeCode 吊销单张码：仅对 status=unused 的码生效，已使用的码不受影响。
// 吊销后该码无法被用户 redeem，运营也能在列表里看到状态变化。
func (s *CDKService) RevokeCode(ctx context.Context, id uint64) error {
	res := s.db.WithContext(ctx).Model(&model.RedeemCode{}).
		Where("id = ? AND status = ?", id, model.CDKStatusUnused).
		Update("status", model.CDKStatusInvalid)
	if res.Error != nil {
		return errcode.DBError.Wrap(res.Error)
	}
	if res.RowsAffected == 0 {
		return errcode.CDKInvalid.WithMsg("码不存在或已被使用，无法吊销")
	}
	return nil
}

// ExportBatchCSV 把单个批次的所有单码导出为 CSV 字节流。
//
// 上限 100000，远大于常见运营批次。返回的 CSV 包含：code / status / used_by /
// used_at / created_at 五列，方便运营直接发给客户或导入 Excel。
func (s *CDKService) ExportBatchCSV(ctx context.Context, batchID uint64) ([]byte, *model.RedeemCodeBatch, error) {
	var batch model.RedeemCodeBatch
	if err := s.db.WithContext(ctx).Where("id = ?", batchID).First(&batch).Error; err != nil {
		return nil, nil, errcode.ResourceMissing
	}
	var codes []*model.RedeemCode
	if err := s.db.WithContext(ctx).
		Where("batch_id = ?", batchID).
		Order("id ASC").
		Limit(100001).
		Find(&codes).Error; err != nil {
		return nil, nil, errcode.DBError.Wrap(err)
	}
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // UTF-8 BOM，让 Excel 中文不乱码。
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"code", "status", "used_by_user_id", "used_at", "created_at"})
	for _, c := range codes {
		usedBy := ""
		if c.UsedBy != nil {
			usedBy = strconv.FormatUint(*c.UsedBy, 10)
		}
		usedAt := ""
		if c.UsedAt != nil {
			usedAt = c.UsedAt.UTC().Format(time.RFC3339)
		}
		_ = w.Write([]string{
			c.Code,
			cdkStatusLabel(c.Status),
			usedBy,
			usedAt,
			c.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, nil, errcode.Internal.Wrap(err)
	}
	return buf.Bytes(), &batch, nil
}

func cdkStatusLabel(v int8) string {
	switch v {
	case model.CDKStatusUsed:
		return "used"
	case model.CDKStatusInvalid:
		return "revoked"
	default:
		return "unused"
	}
}

// buildUniqueCDKBatch 生成 qty 张未冲突的 CDK 码。
//
// 同一个事务里调用：先全局 SELECT 已有 code 集合，再循环生成 → 在内存里
// 比对去重；冲突就再摇一次（256 次内仍冲突就报错给上层）。
// 实际冲突概率极低（16 位 base32 ≈ 32^16 ≈ 1.2e24 空间）。
func buildUniqueCDKBatch(tx *gorm.DB, batchID uint64, qty int) ([]*model.RedeemCode, error) {
	seen := make(map[string]struct{}, qty)
	out := make([]*model.RedeemCode, 0, qty)
	for i := 0; i < qty; i++ {
		var code string
		for attempt := 0; attempt < 256; attempt++ {
			c, err := generateCDKCode()
			if err != nil {
				return nil, errcode.Internal.Wrap(err)
			}
			if _, dup := seen[c]; dup {
				continue
			}
			var exists int64
			if err := tx.Model(&model.RedeemCode{}).Where("code = ?", c).Count(&exists).Error; err != nil {
				return nil, errcode.DBError.Wrap(err)
			}
			if exists == 0 {
				code = c
				break
			}
		}
		if code == "" {
			return nil, errcode.Internal.WithMsg("生成 CDK 时连续冲突，请重试")
		}
		seen[code] = struct{}{}
		out = append(out, &model.RedeemCode{BatchID: batchID, Code: code})
	}
	return out, nil
}

// === helpers ===

func parsePointsReward(rewardType, value string) (int64, error) {
	if rewardType != "points" {
		return 0, fmt.Errorf("unsupported reward_type: %s", rewardType)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(value), &v); err != nil {
		return 0, err
	}
	switch p := v["points"].(type) {
	case float64:
		return int64(p), nil
	case int64:
		return p, nil
	}
	return 0, fmt.Errorf("invalid points reward")
}

// generateCDKCode 生成 16 位 base32（避开易混字符）。
func generateCDKCode() (string, error) {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	b, err := crypto.RandomBytes(16)
	if err != nil {
		return "", err
	}
	out := make([]byte, 16)
	for i := 0; i < 16; i++ {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out), nil
}
