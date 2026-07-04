// Package service 邀请中心：返佣计算 + 列表查询 + 总览。
//
// 触发点：管理员充值（AdminUserService.AdjustPoints/Create）会调用 OnRecharge，
//        若被邀请人有 inviter，则按 invite.commission_rate_bp 给邀请人返佣，
//        并在 invite_reward_log 写一条记录（source_log_id 唯一约束保证幂等）。
package service

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/logger"
)

// SettingInviteCommissionRateBP 邀请返佣比例 key（基点，10000=100%）。
const SettingInviteCommissionRateBP = "invite.commission_rate_bp"

// defaultInviteCommissionBP 默认 10% 返佣。
const defaultInviteCommissionBP int64 = 1000

// InviteService 邀请中心服务。
type InviteService struct {
	invite *repo.InviteRepo
	user   *repo.UserRepo
	cfg    *SystemConfigService
	bill   *BillingService
}

// NewInviteService 构造。
func NewInviteService(invite *repo.InviteRepo, user *repo.UserRepo, cfg *SystemConfigService, bill *BillingService) *InviteService {
	return &InviteService{invite: invite, user: user, cfg: cfg, bill: bill}
}

// commissionRateBP 当前返佣基点，回退到默认 1000 (=10%)。范围 1..10000。
func (s *InviteService) commissionRateBP(ctx context.Context) int64 {
	if s.cfg == nil {
		return defaultInviteCommissionBP
	}
	v := s.cfg.GetInt(ctx, SettingInviteCommissionRateBP, defaultInviteCommissionBP)
	if v <= 0 {
		return 0
	}
	if v > 10000 {
		v = 10000
	}
	return v
}

// GetSummary 邀请中心总览。
func (s *InviteService) GetSummary(ctx context.Context, uid uint64) (*dto.InviteSummaryResp, error) {
	u, err := s.user.GetByID(ctx, uid)
	if err != nil {
		return nil, errcode.UserNotFound
	}
	row, err := s.invite.Summary(ctx, uid)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	rateBP := s.commissionRateBP(ctx)
	resp := &dto.InviteSummaryResp{
		InviteCode:        u.InviteCode,
		InviteeCount:      row.InviteeCount,
		TotalRewardPoints: row.TotalRewardPoints,
		RewardCount:       row.RewardCount,
		CommissionRateBP:  int(rateBP),
		CommissionRate:    float64(rateBP) / 100.0,
	}
	return resp, nil
}

// ListInvitees 分页查询。pageSize 默认 10、上限 100。
func (s *InviteService) ListInvitees(ctx context.Context, uid uint64, page, pageSize int) (*dto.InviteeListResp, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}
	rows, total, err := s.invite.ListInvitees(ctx, uid, page, pageSize)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.InviteeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &dto.InviteeRow{
			UserID:          r.UserID,
			Account:         maskAccountLabel(r.Email, r.Phone, r.Username, r.UUID),
			Status:          r.Status,
			TotalRecharge:   r.TotalRecharge,
			RewardToInviter: r.RewardToInviter,
			BoundAt:         r.BoundAt,
		})
	}
	return &dto.InviteeListResp{List: out, Total: total, Page: page, PageSize: pageSize}, nil
}

// OnRecharge 处理一次"对被邀请人 invitee 的充值"，必要时给邀请人返佣。
//
// 调用方应在 wallet.Adjust 成功之后调用，并把生成的 wallet_log.id 作为 sourceLogID 传入。
// 任何错误都只记日志，不向调用方返回 —— 避免因为返佣失败影响充值主流程。
func (s *InviteService) OnRecharge(ctx context.Context, inviteeID, sourceLogID uint64, rechargePoints int64) {
	if s == nil || s.invite == nil || s.bill == nil {
		return
	}
	if rechargePoints <= 0 || sourceLogID == 0 {
		return
	}

	inviterID, err := s.invite.GetInviterID(ctx, inviteeID)
	if err != nil {
		logger.FromCtx(ctx).Warn("invite.lookup_inviter", zap.Uint64("invitee", inviteeID), zap.Error(err))
		return
	}
	if inviterID == 0 {
		return
	}

	rateBP := s.commissionRateBP(ctx)
	if rateBP <= 0 {
		return
	}

	// 返佣点数 = recharge * rateBP / 10000；至少 1 点。
	reward := rechargePoints * rateBP / 10000
	if reward <= 0 {
		// 充值数额过小，按基点比例算不到 1，最少也要给 1（让用户能看到"我已经获得过 X 笔返佣"）
		// 不过为了避免极端 case 下被薅，1 点也只在 rechargePoints>0 且 rateBP>0 时发。
		reward = 1
	}

	bizID := fmt.Sprintf("invite:%d:%d", inviteeID, sourceLogID)
	remark := fmt.Sprintf("邀请返佣 %.2f%%（用户 #%d 充值 %d）", float64(rateBP)/100.0, inviteeID, rechargePoints)

	// 1) 直接给邀请人加点（独立事务）。如果失败，不写 invite_reward_log，下次该 source 还能重试。
	logRow, err := s.bill.IncomeForInvite(ctx, inviterID, model.BizInvite, bizID, reward, remark)
	if err != nil {
		logger.FromCtx(ctx).Warn("invite.grant_failed",
			zap.Uint64("inviter", inviterID), zap.Uint64("invitee", inviteeID),
			zap.Uint64("source_log", sourceLogID), zap.Int64("reward", reward), zap.Error(err))
		return
	}

	// 2) 落 invite_reward_log；source_log_id 唯一约束保证幂等：
	//    同一笔 wallet_log.id（充值流水）只可能写一条。
	rec := &model.InviteRewardLog{
		InviterID:      inviterID,
		InviteeID:      inviteeID,
		SourceLogID:    sourceLogID,
		RechargePoints: rechargePoints,
		RewardPoints:   reward,
		RateBP:         int(rateBP),
		WalletLogID:    logRow.ID,
	}
	if err := s.invite.InsertReward(ctx, rec); err != nil {
		if errors.Is(err, repo.ErrRewardDuplicated) {
			// 极少见：上一笔 invite_reward_log 已经写入但本笔又来到——直接忽略。
			// 此时邀请人会多拿一次返佣（重复 Income 已完成），但实务上 wallet_log 是
			// 串行 commit，重入到这条路径需要 source_log_id 重用，几乎不可能发生。
			logger.FromCtx(ctx).Info("invite.reward_dup", zap.Uint64("source_log", sourceLogID))
			return
		}
		logger.FromCtx(ctx).Warn("invite.reward_log_insert",
			zap.Uint64("inviter", inviterID), zap.Uint64("invitee", inviteeID), zap.Error(err))
		return
	}

	logger.FromCtx(ctx).Info("invite.rewarded",
		zap.Uint64("inviter", inviterID),
		zap.Uint64("invitee", inviteeID),
		zap.Int64("reward", reward),
		zap.Int64("rate_bp", rateBP),
		zap.Uint64("source_log", sourceLogID),
	)
}

// maskAccountLabel 选一个最能代表用户的字段并脱敏。
//
//	优先级：email > phone > username > uuid 前 8 位
//	邮箱：保留首 + 末 1 位 + 域名（短账号则补 *）
//	手机：保留前 3 后 4
//	用户名：保留首末各 1 字符（短于 4 时全保留再加 ***）
func maskAccountLabel(email, phone, username, uuid string) string {
	switch {
	case email != "":
		return maskInviteEmail(email)
	case phone != "":
		return maskInvitePhone(phone)
	case username != "":
		return maskInviteName(username)
	case uuid != "":
		if len(uuid) >= 8 {
			return "user-" + uuid[:8]
		}
		return "user-" + uuid
	default:
		return "—"
	}
}

func maskInviteEmail(s string) string {
	at := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '@' {
			at = i
			break
		}
	}
	if at <= 0 {
		return maskInviteName(s)
	}
	local := s[:at]
	domain := s[at:]
	switch {
	case len(local) <= 1:
		return local + "***" + domain
	case len(local) == 2:
		return string(local[0]) + "*" + domain
	default:
		return string(local[0]) + "***" + string(local[len(local)-1]) + domain
	}
}

func maskInvitePhone(s string) string {
	r := []rune(s)
	n := len(r)
	if n <= 4 {
		return s
	}
	if n <= 7 {
		return string(r[:1]) + "****" + string(r[n-1:])
	}
	return string(r[:3]) + "****" + string(r[n-4:])
}

func maskInviteName(s string) string {
	r := []rune(s)
	n := len(r)
	switch {
	case n <= 1:
		return s + "***"
	case n == 2:
		return string(r[0]) + "*"
	default:
		return string(r[0]) + "***" + string(r[n-1])
	}
}
