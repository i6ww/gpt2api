package repo

import (
	"context"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/kleinai/backend/internal/dto"
)

type DashboardRepo struct {
	db *gorm.DB

	cacheMu        sync.RWMutex
	cachedAt       time.Time
	cachedOverview *dto.AdminDashboardOverviewResp
}

func NewDashboardRepo(db *gorm.DB) *DashboardRepo { return &DashboardRepo{db: db} }

type dashboardGenerationAgg struct {
	GeneratedToday  int64
	GeneratedTotal  int64
	ImageToday      int64
	ImageTotal      int64
	VideoToday      int64
	VideoTotal      int64
	TextTokensToday int64
	TextTokensTotal int64
	CostToday       int64
	CostTotal       int64
	SuccessToday    int64
	FinishedToday   int64
}

type dashboardWalletAgg struct {
	SpendToday int64
	SpendTotal int64
}

type dashboardUserAgg struct {
	UsersTotal       int64
	UsersToday       int64
	ActiveUsersToday int64
}

type dashboardProviderRow struct {
	Provider       string
	Total          int64
	Enabled        int64
	Available      int64
	Broken         int64
	TestOK         int64
	QuotaRemaining float64
	QuotaTotal     float64
	SuccessCount   int64
	ErrorCount     int64
}

type dashboardRecentRow struct {
	TaskID     string
	CreatedAt  time.Time
	UserLabel  string
	Kind       string
	ModelCode  string
	Count      int
	Status     int8
	CostPoints int64
}

type dashboardTrendRow struct {
	Date           string
	GeneratedCount int64
	CostPoints     int64
}

func (r *DashboardRepo) Overview(ctx context.Context) (*dto.AdminDashboardOverviewResp, error) {
	const cacheTTL = 15 * time.Second

	r.cacheMu.RLock()
	if r.cachedOverview != nil && time.Since(r.cachedAt) < cacheTTL {
		cached := cloneDashboardOverview(r.cachedOverview)
		r.cacheMu.RUnlock()
		return cached, nil
	}
	r.cacheMu.RUnlock()

	now := time.Now()
	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endToday := startToday.Add(24 * time.Hour)
	startTrend := startToday.AddDate(0, 0, -6)

	var gen dashboardGenerationAgg
	genSQL := `SELECT
  COUNT(CASE WHEN created_at >= ? AND created_at < ? THEN 1 END) AS generated_today,
  COUNT(1) AS generated_total,
  COALESCE(SUM(CASE WHEN kind = 'image' AND created_at >= ? AND created_at < ? THEN count ELSE 0 END), 0) AS image_today,
  COALESCE(SUM(CASE WHEN kind = 'image' THEN count ELSE 0 END), 0) AS image_total,
  COALESCE(SUM(CASE WHEN kind = 'video' AND created_at >= ? AND created_at < ? THEN count ELSE 0 END), 0) AS video_today,
  COALESCE(SUM(CASE WHEN kind = 'video' THEN count ELSE 0 END), 0) AS video_total,
  COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? THEN CEIL(CHAR_LENGTH(prompt) / 4) ELSE 0 END), 0) AS text_tokens_today,
  COALESCE(SUM(CEIL(CHAR_LENGTH(prompt) / 4)), 0) AS text_tokens_total,
  COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? THEN cost_points ELSE 0 END), 0) AS cost_today,
  COALESCE(SUM(cost_points), 0) AS cost_total,
  COUNT(CASE WHEN created_at >= ? AND created_at < ? AND status = 2 THEN 1 END) AS success_today,
  COUNT(CASE WHEN created_at >= ? AND created_at < ? AND status IN (2,3,4) THEN 1 END) AS finished_today
FROM generation_task
WHERE deleted_at IS NULL`
	if err := r.db.WithContext(ctx).Raw(
		genSQL,
		startToday, endToday,
		startToday, endToday,
		startToday, endToday,
		startToday, endToday,
		startToday, endToday,
		startToday, endToday,
		startToday, endToday,
	).Scan(&gen).Error; err != nil {
		return nil, err
	}

	var wallet dashboardWalletAgg
	walletSQL := `SELECT
  COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? AND direction < 0 THEN ABS(points) ELSE 0 END), 0) AS spend_today,
  COALESCE(SUM(CASE WHEN direction < 0 THEN ABS(points) ELSE 0 END), 0) AS spend_total
FROM wallet_log`
	if err := r.db.WithContext(ctx).Raw(walletSQL, startToday, endToday).Scan(&wallet).Error; err != nil {
		return nil, err
	}

	var users dashboardUserAgg
	userSQL := `SELECT
  COUNT(1) AS users_total,
  COUNT(CASE WHEN created_at >= ? AND created_at < ? THEN 1 END) AS users_today,
  COUNT(CASE WHEN last_login_at >= ? AND last_login_at < ? THEN 1 END) AS active_users_today
FROM ` + "`user`" + `
WHERE deleted_at IS NULL`
	if err := r.db.WithContext(ctx).Raw(userSQL, startToday, endToday, startToday, endToday).Scan(&users).Error; err != nil {
		return nil, err
	}

	// 号池由 account 表迁到 provider-specific pool 表了，这里 UNION 各池表生成
	// dashboard 用的 provider 聚合。
	//   - status: 字符串 valid/cooldown/disabled/invalid → 通过 CASE 算 enabled/broken
	//   - last_test_status: pool_gpt/grok 有 int8 列；pool_adobe 用 status 推断
	//   - quota_remaining: Adobe/Grok/Google 用 credits；GPT 只有用量百分比，没有绝对余额
	//   - quota_total: Grok 有 quota_total；Adobe/Google 无总额度时用已探测余额兜底，
	//     避免前端把有 credits 的账号误显示为"未探测"
	//   - success_count / failure_count: Adobe/Google 没有 success_count，置 0
	var providers []*dashboardProviderRow
	providerSQL := `
SELECT provider, SUM(total) AS total, SUM(enabled) AS enabled, SUM(available) AS available,
       SUM(broken) AS broken, SUM(test_ok) AS test_ok,
       SUM(quota_remaining) AS quota_remaining, SUM(quota_total) AS quota_total,
       SUM(success_count) AS success_count, SUM(error_count) AS error_count
FROM (
  SELECT 'gpt' AS provider,
    COUNT(1) AS total,
    COALESCE(SUM(CASE WHEN status='valid' THEN 1 ELSE 0 END), 0) AS enabled,
    COALESCE(SUM(CASE WHEN status='valid' AND (cooldown_until IS NULL OR cooldown_until <= UTC_TIMESTAMP()) THEN 1 ELSE 0 END), 0) AS available,
    COALESCE(SUM(CASE WHEN status='cooldown' THEN 1 ELSE 0 END), 0) AS broken,
    COALESCE(SUM(CASE WHEN last_test_status = 1 THEN 1 ELSE 0 END), 0) AS test_ok,
    0.0 AS quota_remaining,
    0.0 AS quota_total,
    COALESCE(SUM(success_count), 0) AS success_count,
    COALESCE(SUM(failure_count), 0) AS error_count
  FROM pool_gpt WHERE deleted_at IS NULL
  UNION ALL
  SELECT 'grok' AS provider,
    COUNT(1) AS total,
    COALESCE(SUM(CASE WHEN status='valid' THEN 1 ELSE 0 END), 0) AS enabled,
    COALESCE(SUM(CASE WHEN status='valid' AND (cooldown_until IS NULL OR cooldown_until <= UTC_TIMESTAMP()) THEN 1 ELSE 0 END), 0) AS available,
    COALESCE(SUM(CASE WHEN status='cooldown' THEN 1 ELSE 0 END), 0) AS broken,
    COALESCE(SUM(CASE WHEN last_test_status = 1 THEN 1 ELSE 0 END), 0) AS test_ok,
    COALESCE(SUM(credits), 0) AS quota_remaining,
    COALESCE(SUM(quota_total), 0) AS quota_total,
    COALESCE(SUM(success_count), 0) AS success_count,
    COALESCE(SUM(failure_count), 0) AS error_count
  FROM pool_grok WHERE deleted_at IS NULL
  UNION ALL
  SELECT 'adobe' AS provider,
    COUNT(1) AS total,
    COALESCE(SUM(CASE WHEN status='valid' THEN 1 ELSE 0 END), 0) AS enabled,
    COALESCE(SUM(CASE WHEN status='valid' AND (cooldown_until IS NULL OR cooldown_until <= UTC_TIMESTAMP()) THEN 1 ELSE 0 END), 0) AS available,
    COALESCE(SUM(CASE WHEN status='cooldown' THEN 1 ELSE 0 END), 0) AS broken,
    COALESCE(SUM(CASE WHEN status='valid' THEN 1 ELSE 0 END), 0) AS test_ok,
    COALESCE(SUM(credits), 0) AS quota_remaining,
    COALESCE(SUM(CASE WHEN last_credits_check_at IS NOT NULL THEN GREATEST(credits, 0) ELSE 0 END), 0) AS quota_total,
    0 AS success_count,
    COALESCE(SUM(failure_count), 0) AS error_count
  FROM pool_adobe WHERE deleted_at IS NULL
  UNION ALL
  SELECT 'flowmusic' AS provider,
    COUNT(1) AS total,
    COALESCE(SUM(CASE WHEN status='valid' THEN 1 ELSE 0 END), 0) AS enabled,
    COALESCE(SUM(CASE WHEN status='valid' AND (cooldown_until IS NULL OR cooldown_until <= UTC_TIMESTAMP()) THEN 1 ELSE 0 END), 0) AS available,
    COALESCE(SUM(CASE WHEN status='cooldown' THEN 1 ELSE 0 END), 0) AS broken,
    COALESCE(SUM(CASE WHEN status='valid' THEN 1 ELSE 0 END), 0) AS test_ok,
    COALESCE(SUM(credits + tokens_remaining), 0) AS quota_remaining,
    COALESCE(SUM(CASE WHEN last_checked_at IS NOT NULL THEN GREATEST(credits + tokens_remaining, 0) ELSE 0 END), 0) AS quota_total,
    0 AS success_count,
    COALESCE(SUM(failure_count), 0) AS error_count
  FROM pool_google WHERE deleted_at IS NULL
) agg
GROUP BY provider
ORDER BY provider ASC`
	if err := r.db.WithContext(ctx).Raw(providerSQL).Scan(&providers).Error; err != nil {
		return nil, err
	}

	var recent []*dashboardRecentRow
	recentSQL := `SELECT
  t.task_id,
  t.created_at,
  COALESCE(NULLIF(u.username, ''), NULLIF(u.email, ''), NULLIF(u.phone, ''), CONCAT('用户 #', t.user_id)) AS user_label,
  t.kind,
  t.model_code,
  t.count,
  t.status,
  t.cost_points
FROM generation_task t
LEFT JOIN ` + "`user`" + ` u ON u.id = t.user_id
WHERE t.deleted_at IS NULL
ORDER BY t.id DESC
LIMIT 5`
	if err := r.db.WithContext(ctx).Raw(recentSQL).Scan(&recent).Error; err != nil {
		return nil, err
	}

	var trendRows []*dashboardTrendRow
	// 注意：用 DATE_FORMAT('%Y-%m-%d') 而不是 DATE()。前者 driver 直接返回字符串
	// "2026-05-13"；后者在 parseTime=True 下会被 driver 当成 time.Time 转成
	// "2026-05-13 00:00:00 +0000 +0800" 之类，与下面 trendMap 的 "2006-01-02"
	// key 完全对不上 —— 7 天趋势就会全是 0（前端图表一条空线）。
	trendSQL := `SELECT
  DATE_FORMAT(created_at, '%Y-%m-%d') AS date,
  COUNT(1) AS generated_count,
  COALESCE(SUM(cost_points), 0) AS cost_points
FROM generation_task
WHERE deleted_at IS NULL AND created_at >= ? AND created_at < ?
GROUP BY DATE_FORMAT(created_at, '%Y-%m-%d')
ORDER BY DATE_FORMAT(created_at, '%Y-%m-%d') ASC`
	if err := r.db.WithContext(ctx).Raw(trendSQL, startTrend, endToday).Scan(&trendRows).Error; err != nil {
		return nil, err
	}
	trendMap := map[string]*dashboardTrendRow{}
	for _, row := range trendRows {
		trendMap[row.Date] = row
	}

	resp := &dto.AdminDashboardOverviewResp{
		GeneratedToday:    gen.GeneratedToday,
		GeneratedTotal:    gen.GeneratedTotal,
		ImageToday:        gen.ImageToday,
		ImageTotal:        gen.ImageTotal,
		VideoToday:        gen.VideoToday,
		VideoTotal:        gen.VideoTotal,
		TextTokensToday:   gen.TextTokensToday,
		TextTokensTotal:   gen.TextTokensTotal,
		CostPointsToday:   gen.CostToday,
		CostPointsTotal:   gen.CostTotal,
		WalletSpendToday:  wallet.SpendToday,
		WalletSpendTotal:  wallet.SpendTotal,
		UsersTotal:        users.UsersTotal,
		UsersToday:        users.UsersToday,
		ActiveUsersToday:  users.ActiveUsersToday,
		AccountProviders:  make([]*dto.DashboardProviderRow, 0, len(providers)),
		RecentGenerations: make([]*dto.DashboardRecentTask, 0, len(recent)),
		Trend:             make([]*dto.DashboardTrendPoint, 0, 7),
	}
	if gen.FinishedToday > 0 {
		resp.SuccessRateToday = float64(gen.SuccessToday) / float64(gen.FinishedToday)
	}
	for _, p := range providers {
		quotaUsed := p.QuotaTotal - p.QuotaRemaining
		if quotaUsed < 0 {
			quotaUsed = 0
		}
		resp.AccountProviders = append(resp.AccountProviders, &dto.DashboardProviderRow{
			Provider:       p.Provider,
			Total:          p.Total,
			Enabled:        p.Enabled,
			Available:      p.Available,
			Broken:         p.Broken,
			TestOK:         p.TestOK,
			QuotaRemaining: p.QuotaRemaining,
			QuotaTotal:     p.QuotaTotal,
			QuotaUsed:      quotaUsed,
			SuccessCount:   p.SuccessCount,
			ErrorCount:     p.ErrorCount,
		})
	}
	for _, row := range recent {
		resp.RecentGenerations = append(resp.RecentGenerations, &dto.DashboardRecentTask{
			TaskID:     row.TaskID,
			CreatedAt:  row.CreatedAt.Unix(),
			UserLabel:  row.UserLabel,
			Kind:       row.Kind,
			ModelCode:  row.ModelCode,
			Count:      row.Count,
			Status:     row.Status,
			CostPoints: row.CostPoints,
		})
	}
	for i := 6; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		point := &dto.DashboardTrendPoint{Date: day}
		if row := trendMap[day]; row != nil {
			point.Generated = row.GeneratedCount
			point.CostPoints = row.CostPoints
		}
		resp.Trend = append(resp.Trend, point)
	}

	r.cacheMu.Lock()
	r.cachedAt = time.Now()
	r.cachedOverview = cloneDashboardOverview(resp)
	r.cacheMu.Unlock()

	return resp, nil
}

func cloneDashboardOverview(src *dto.AdminDashboardOverviewResp) *dto.AdminDashboardOverviewResp {
	if src == nil {
		return nil
	}
	dst := *src
	if len(src.AccountProviders) > 0 {
		dst.AccountProviders = make([]*dto.DashboardProviderRow, 0, len(src.AccountProviders))
		for _, row := range src.AccountProviders {
			if row == nil {
				continue
			}
			copied := *row
			dst.AccountProviders = append(dst.AccountProviders, &copied)
		}
	}
	if len(src.RecentGenerations) > 0 {
		dst.RecentGenerations = make([]*dto.DashboardRecentTask, 0, len(src.RecentGenerations))
		for _, row := range src.RecentGenerations {
			if row == nil {
				continue
			}
			copied := *row
			dst.RecentGenerations = append(dst.RecentGenerations, &copied)
		}
	}
	if len(src.Trend) > 0 {
		dst.Trend = make([]*dto.DashboardTrendPoint, 0, len(src.Trend))
		for _, row := range src.Trend {
			if row == nil {
				continue
			}
			copied := *row
			dst.Trend = append(dst.Trend, &copied)
		}
	}
	return &dst
}
