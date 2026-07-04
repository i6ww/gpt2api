// Package service: 上游 API 管理。
//
// 本文件覆盖 Phase A:
//   - UpstreamChannelService：通道 / 路由 CRUD、缓存预热、首启 seed
//   - 与 Phase B 的 cost_recorder.go 共用同一份 channel 缓存（resolveChannel / refreshCache）
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/crypto"
	"github.com/kleinai/backend/pkg/errcode"
)

// LocalPoolChannelKey 本地号池通道的固定 key；启动 seed 保证存在且类型 = local_pool。
const LocalPoolChannelKey = "local.pool"

// 系统配置 key
const (
	SettingFXUSDToCNY     = "fx.usd_to_cny"
	SettingFXIDRToCNY     = "fx.idr_to_cny"
	SettingCostLogEnabled = "cost_log.enabled"
	SettingCostPointToCNY = "cost_log.point_to_cny" // 1 个销售点 = 多少 CNY；默认 0.01（1 点 ≈ 1 分）
)

// UpstreamChannelService 上游通道 / 路由管理。
//
// 缓存策略：channelByID / channelByKey / routesByModel 全量保留在内存，
// 任何 mutate 操作（Create/Update/Delete）后调用 invalidate() 异步刷新。
// CostRecorder 高并发读，避免每次落账走一次 DB。
type UpstreamChannelService struct {
	repo *repo.UpstreamChannelRepo
	aes  *crypto.AESGCM // 用于 api_key_enc 字段；可为 nil（启动时未注入 → 不允许写/读 api_key）

	mu            sync.RWMutex
	channelByID   map[uint64]*model.UpstreamChannel
	channelByKey  map[string]*model.UpstreamChannel
	routesByModel map[string][]*model.UpstreamModelRoute // key = model_code|variant_key
	loadedAt      time.Time
	ttl           time.Duration
}

// NewUpstreamChannelService 构造；aes 可选，未传则 api_key 字段不可读写。
func NewUpstreamChannelService(r *repo.UpstreamChannelRepo, aes *crypto.AESGCM) *UpstreamChannelService {
	return &UpstreamChannelService{
		repo:          r,
		aes:           aes,
		channelByID:   map[uint64]*model.UpstreamChannel{},
		channelByKey:  map[string]*model.UpstreamChannel{},
		routesByModel: map[string][]*model.UpstreamModelRoute{},
		ttl:           30 * time.Second,
	}
}

// === 缓存 ===

// reload 全量重读两张表到内存索引。
func (s *UpstreamChannelService) reload(ctx context.Context) error {
	chs, err := s.repo.AllChannels(ctx)
	if err != nil {
		return err
	}
	rts, err := s.repo.AllRoutes(ctx)
	if err != nil {
		return err
	}
	byID := make(map[uint64]*model.UpstreamChannel, len(chs))
	byKey := make(map[string]*model.UpstreamChannel, len(chs))
	for _, ch := range chs {
		byID[ch.ID] = ch
		byKey[ch.Key] = ch
	}
	byModel := make(map[string][]*model.UpstreamModelRoute, len(rts))
	for _, rt := range rts {
		if !rt.Enabled {
			continue
		}
		k := routeIndexKey(rt.ModelCode, rt.VariantKey)
		byModel[k] = append(byModel[k], rt)
	}
	s.mu.Lock()
	s.channelByID = byID
	s.channelByKey = byKey
	s.routesByModel = byModel
	s.loadedAt = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *UpstreamChannelService) invalidate() {
	s.mu.Lock()
	s.loadedAt = time.Time{}
	s.mu.Unlock()
}

func (s *UpstreamChannelService) ensureLoaded(ctx context.Context) {
	s.mu.RLock()
	fresh := time.Since(s.loadedAt) < s.ttl && len(s.channelByID) > 0
	s.mu.RUnlock()
	if fresh {
		return
	}
	_ = s.reload(ctx)
}

func routeIndexKey(modelCode, variantKey string) string {
	return strings.TrimSpace(modelCode) + "|" + strings.TrimSpace(variantKey)
}

// === 解析（CostRecorder 主要使用入口）===

// ResolveChannelForTask 给定 (model_code, variant_key) 找 enabled 优先级最高的通道。
// 命中返回 (channel, route, true)；未命中返回 (nil, nil, false)。
//
// CostRecorder 内部用这个解析；前端 admin UI 显示「这个 model 走哪个通道」也调它。
func (s *UpstreamChannelService) ResolveChannelForTask(ctx context.Context, modelCode, variantKey string) (*model.UpstreamChannel, *model.UpstreamModelRoute, bool) {
	s.ensureLoaded(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	// 1) 精确匹配
	if rts, ok := s.routesByModel[routeIndexKey(modelCode, variantKey)]; ok && len(rts) > 0 {
		rt := rts[0] // priority ASC, id ASC 已经在 reload 里隐含排序
		if ch, ok := s.channelByID[rt.UpstreamChannelID]; ok && ch.Enabled {
			return ch, rt, true
		}
	}
	// 2) variant 缺省 fallback：用空 variant_key 行
	if strings.TrimSpace(variantKey) != "" {
		if rts, ok := s.routesByModel[routeIndexKey(modelCode, "")]; ok && len(rts) > 0 {
			rt := rts[0]
			if ch, ok := s.channelByID[rt.UpstreamChannelID]; ok && ch.Enabled {
				return ch, rt, true
			}
		}
	}
	return nil, nil, false
}

// GetChannelByID 拿一个通道（缓存命中），admin handler 详情页用。
func (s *UpstreamChannelService) GetChannelByID(ctx context.Context, id uint64) (*model.UpstreamChannel, bool) {
	s.ensureLoaded(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.channelByID[id]
	return c, ok
}

// ResolveChannelsForTask 返回按 priority ASC 排序的所有 enabled 通道。
//
// runtime 调度：
//  1. 精确匹配 (model_code, variant_key)；
//  2. variant_key 非空时补一份 (model_code, "") 的 fallback；
//  3. 过滤掉 channel.Enabled=false 的；
//  4. 同 channel 多次出现按第一次出现保留（避免重复试）。
//
// 返回切片可能为空（无配置）；调用方需要 fallback 旧的"按 provider 选号"逻辑。
func (s *UpstreamChannelService) ResolveChannelsForTask(ctx context.Context, modelCode, variantKey string) []ResolvedChannel {
	s.ensureLoaded(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[uint64]struct{}, 4)
	out := make([]ResolvedChannel, 0, 4)
	appendFrom := func(idxKey string) {
		rts := s.routesByModel[idxKey]
		for _, rt := range rts {
			if _, dup := seen[rt.UpstreamChannelID]; dup {
				continue
			}
			ch, ok := s.channelByID[rt.UpstreamChannelID]
			if !ok || !ch.Enabled {
				continue
			}
			seen[rt.UpstreamChannelID] = struct{}{}
			out = append(out, ResolvedChannel{Channel: ch, Route: rt})
		}
	}
	appendFrom(routeIndexKey(modelCode, variantKey))
	if strings.TrimSpace(variantKey) != "" {
		appendFrom(routeIndexKey(modelCode, ""))
	}
	return out
}

// ResolvedChannel runtime 路由结果：通道 + 命中的路由（路由里带 cost_multiplier）。
type ResolvedChannel struct {
	Channel *model.UpstreamChannel
	Route   *model.UpstreamModelRoute
}

// GetDecryptedAPIKey 解密 external_api 通道的 api_key。无 key 或类型不对返回 ""。
func (s *UpstreamChannelService) GetDecryptedAPIKey(ch *model.UpstreamChannel) (string, error) {
	if s == nil || ch == nil {
		return "", nil
	}
	if ch.ChannelType != model.ChannelTypeExternalAPI {
		return "", nil
	}
	if len(ch.APIKeyEnc) == 0 {
		return "", nil
	}
	if s.aes == nil {
		return "", fmt.Errorf("aes not configured")
	}
	plain, err := s.aes.Decrypt(ch.APIKeyEnc)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// ChannelSupportsModel 检查 supported_models 是否包含给定 model_code。
// local_pool 永远返回 true（系统识别全部）；external_api 留空时按"无限制"。
func ChannelSupportsModel(ch *model.UpstreamChannel, modelCode string) bool {
	if ch == nil {
		return false
	}
	if ch.ChannelType == model.ChannelTypeLocalPool {
		return true
	}
	if ch.SupportedModels == nil || strings.TrimSpace(*ch.SupportedModels) == "" || strings.TrimSpace(*ch.SupportedModels) == "[]" {
		return true
	}
	var list []string
	if err := json.Unmarshal([]byte(*ch.SupportedModels), &list); err != nil {
		return false
	}
	mc := strings.TrimSpace(modelCode)
	for _, m := range list {
		if strings.EqualFold(strings.TrimSpace(m), mc) {
			return true
		}
	}
	return false
}

// === Channel CRUD ===

// CreateChannel 创建通道。
func (s *UpstreamChannelService) CreateChannel(ctx context.Context, req *dto.ChannelSaveReq) (*model.UpstreamChannel, error) {
	if req == nil {
		return nil, errcode.InvalidParam.WithMsg("missing body")
	}
	if strings.TrimSpace(req.Key) == "" {
		return nil, errcode.InvalidParam.WithMsg("key is required")
	}
	if strings.TrimSpace(req.Provider) == "" {
		return nil, errcode.InvalidParam.WithMsg("provider is required")
	}
	if !validBillingMode(req.BillingMode) {
		return nil, errcode.InvalidParam.WithMsg("invalid billing_mode")
	}
	unitPriceJSON, err := encodeJSONOrEmpty(req.UnitPrice)
	if err != nil {
		return nil, errcode.InvalidParam.Wrap(err)
	}
	capsJSON, err := encodeJSONOrEmpty(req.Capabilities)
	if err != nil {
		return nil, errcode.InvalidParam.Wrap(err)
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = "USD"
	}
	channelType := strings.TrimSpace(req.ChannelType)
	if channelType == "" {
		channelType = model.ChannelTypeExternalAPI
	}
	if channelType != model.ChannelTypeLocalPool && channelType != model.ChannelTypeExternalAPI {
		return nil, errcode.InvalidParam.WithMsg("invalid channel_type")
	}
	row := &model.UpstreamChannel{
		Key:                  strings.TrimSpace(req.Key),
		ChannelType:          channelType,
		Provider:             strings.TrimSpace(req.Provider),
		Route:                strings.TrimSpace(req.Route),
		BaseURL:              strings.TrimSpace(req.BaseURL),
		Label:                strings.TrimSpace(req.Label),
		Enabled:              enabled,
		BillingMode:          req.BillingMode,
		UnitPrice:            unitPriceJSON,
		Currency:             currency,
		Capabilities:         capsJSON,
		MonthlyFixedCost:     req.MonthlyFixedCost,
		ExpectedMonthlyCalls: req.ExpectedMonthlyCalls,
		FXToCNY:              req.FXToCNY,
	}
	if strings.TrimSpace(req.Notes) != "" {
		n := req.Notes
		row.Notes = &n
	}
	if len(req.SupportedModels) > 0 {
		raw, err := json.Marshal(req.SupportedModels)
		if err != nil {
			return nil, errcode.InvalidParam.Wrap(err)
		}
		s := string(raw)
		row.SupportedModels = &s
	}
	// API key 仅在 external_api 通道里有效；local_pool 行禁止设置。
	if channelType == model.ChannelTypeExternalAPI && req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" && *req.APIKey != dto.APIKeyClearSentinel {
		if s.aes == nil {
			return nil, errcode.InvalidParam.WithMsg("aes not configured; cannot store api_key")
		}
		enc, err := s.aes.Encrypt([]byte(strings.TrimSpace(*req.APIKey)))
		if err != nil {
			return nil, errcode.InvalidParam.Wrap(err)
		}
		row.APIKeyEnc = enc
	}
	if err := s.repo.CreateChannel(ctx, row); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	s.invalidate()
	return row, nil
}

// UpdateChannel 更新通道；req.Key 留空表示不改 key。
func (s *UpstreamChannelService) UpdateChannel(ctx context.Context, id uint64, req *dto.ChannelSaveReq) error {
	if req == nil {
		return errcode.InvalidParam.WithMsg("missing body")
	}
	cur, err := s.repo.GetChannelByID(ctx, id)
	if err != nil {
		return errcode.ResourceMissing.Wrap(err)
	}
	fields := map[string]any{}
	if strings.TrimSpace(req.Key) != "" && req.Key != cur.Key {
		fields["key"] = strings.TrimSpace(req.Key)
	}
	if strings.TrimSpace(req.Provider) != "" {
		fields["provider"] = strings.TrimSpace(req.Provider)
	}
	if req.Route != cur.Route {
		fields["route"] = strings.TrimSpace(req.Route)
	}
	if req.BaseURL != cur.BaseURL {
		fields["base_url"] = strings.TrimSpace(req.BaseURL)
	}
	if req.Label != cur.Label {
		fields["label"] = strings.TrimSpace(req.Label)
	}
	if req.Enabled != nil {
		fields["enabled"] = *req.Enabled
	}
	if strings.TrimSpace(req.BillingMode) != "" {
		if !validBillingMode(req.BillingMode) {
			return errcode.InvalidParam.WithMsg("invalid billing_mode")
		}
		fields["billing_mode"] = req.BillingMode
	}
	if req.UnitPrice != nil {
		raw, err := encodeJSONOrEmpty(req.UnitPrice)
		if err != nil {
			return errcode.InvalidParam.Wrap(err)
		}
		fields["unit_price"] = raw
	}
	if strings.TrimSpace(req.Currency) != "" {
		fields["currency"] = strings.ToUpper(strings.TrimSpace(req.Currency))
	}
	if req.Capabilities != nil {
		raw, err := encodeJSONOrEmpty(req.Capabilities)
		if err != nil {
			return errcode.InvalidParam.Wrap(err)
		}
		fields["capabilities"] = raw
	}
	if req.MonthlyFixedCost != cur.MonthlyFixedCost {
		fields["monthly_fixed_cost"] = req.MonthlyFixedCost
	}
	if req.ExpectedMonthlyCalls != cur.ExpectedMonthlyCalls {
		fields["expected_monthly_calls"] = req.ExpectedMonthlyCalls
	}
	if req.FXToCNY != cur.FXToCNY {
		fields["fx_to_cny"] = req.FXToCNY
	}
	if req.Notes != "" || (cur.Notes != nil && *cur.Notes != "") {
		// 让空字符串能清空
		fields["notes"] = req.Notes
	}
	// supported_models nil 表示"不改"；len==0 表示"清空成 []"。
	if req.SupportedModels != nil {
		raw, err := json.Marshal(req.SupportedModels)
		if err != nil {
			return errcode.InvalidParam.Wrap(err)
		}
		fields["supported_models"] = string(raw)
	}
	// api_key 处理（仅 external_api 通道允许）：
	//   nil           -> 不变
	//   == sentinel   -> 清空
	//   非空字符串    -> 覆盖（要求 aes 已注入）
	if req.APIKey != nil && cur.ChannelType == model.ChannelTypeExternalAPI {
		switch v := strings.TrimSpace(*req.APIKey); {
		case v == "" || v == dto.APIKeyClearSentinel:
			fields["api_key_enc"] = []byte(nil)
		default:
			if s.aes == nil {
				return errcode.InvalidParam.WithMsg("aes not configured; cannot store api_key")
			}
			enc, err := s.aes.Encrypt([]byte(v))
			if err != nil {
				return errcode.InvalidParam.Wrap(err)
			}
			fields["api_key_enc"] = enc
		}
	}
	if len(fields) == 0 {
		return nil
	}
	if err := s.repo.UpdateChannel(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	s.invalidate()
	return nil
}

// DeleteChannel 删除通道，连带删该通道下所有路由。
func (s *UpstreamChannelService) DeleteChannel(ctx context.Context, id uint64) error {
	if _, err := s.repo.DeleteRoutesByChannel(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	if err := s.repo.DeleteChannel(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	s.invalidate()
	return nil
}

// ListChannels 列表查询，DTO 形态返回（unit_price/capabilities 反序列化）。
func (s *UpstreamChannelService) ListChannels(ctx context.Context, req *dto.ChannelListReq) ([]*dto.ChannelDTO, int64, error) {
	f := repo.ChannelListFilter{
		Provider: req.Provider,
		Enabled:  req.Enabled,
		Keyword:  req.Keyword,
		Page:     req.Page,
		PageSize: req.PageSize,
	}
	rows, total, err := s.repo.ListChannels(ctx, f)
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.ChannelDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, channelToDTO(r))
	}
	return out, total, nil
}

// === Route CRUD ===

// CreateRoute 创建路由（model_code → channel）。
func (s *UpstreamChannelService) CreateRoute(ctx context.Context, req *dto.RouteSaveReq) (*model.UpstreamModelRoute, error) {
	if req == nil || strings.TrimSpace(req.ModelCode) == "" {
		return nil, errcode.InvalidParam.WithMsg("model_code required")
	}
	if req.UpstreamChannelID == 0 {
		return nil, errcode.InvalidParam.WithMsg("upstream_channel_id required")
	}
	if _, err := s.repo.GetChannelByID(ctx, req.UpstreamChannelID); err != nil {
		return nil, errcode.InvalidParam.WithMsg("channel not found")
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.Priority <= 0 {
		req.Priority = 1
	}
	if req.CostMultiplier <= 0 {
		req.CostMultiplier = 1.0
	}
	row := &model.UpstreamModelRoute{
		ModelCode:         strings.TrimSpace(req.ModelCode),
		VariantKey:        strings.TrimSpace(req.VariantKey),
		UpstreamChannelID: req.UpstreamChannelID,
		Priority:          req.Priority,
		Enabled:           enabled,
		CostMultiplier:    req.CostMultiplier,
	}
	if strings.TrimSpace(req.Notes) != "" {
		n := req.Notes
		row.Notes = &n
	}
	if err := s.repo.CreateRoute(ctx, row); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	s.invalidate()
	return row, nil
}

// UpdateRoute 更新一条路由。
func (s *UpstreamChannelService) UpdateRoute(ctx context.Context, id uint64, req *dto.RouteSaveReq) error {
	if req == nil {
		return errcode.InvalidParam.WithMsg("missing body")
	}
	cur, err := s.repo.GetRouteByID(ctx, id)
	if err != nil {
		return errcode.ResourceMissing.Wrap(err)
	}
	fields := map[string]any{}
	if strings.TrimSpace(req.ModelCode) != "" && req.ModelCode != cur.ModelCode {
		fields["model_code"] = strings.TrimSpace(req.ModelCode)
	}
	if req.VariantKey != cur.VariantKey {
		fields["variant_key"] = strings.TrimSpace(req.VariantKey)
	}
	if req.UpstreamChannelID > 0 && req.UpstreamChannelID != cur.UpstreamChannelID {
		if _, err := s.repo.GetChannelByID(ctx, req.UpstreamChannelID); err != nil {
			return errcode.InvalidParam.WithMsg("channel not found")
		}
		fields["upstream_channel_id"] = req.UpstreamChannelID
	}
	if req.Priority > 0 && req.Priority != cur.Priority {
		fields["priority"] = req.Priority
	}
	if req.Enabled != nil {
		fields["enabled"] = *req.Enabled
	}
	if req.CostMultiplier > 0 && req.CostMultiplier != cur.CostMultiplier {
		fields["cost_multiplier"] = req.CostMultiplier
	}
	if req.Notes != "" || (cur.Notes != nil && *cur.Notes != "") {
		fields["notes"] = req.Notes
	}
	if len(fields) == 0 {
		return nil
	}
	if err := s.repo.UpdateRoute(ctx, id, fields); err != nil {
		return errcode.DBError.Wrap(err)
	}
	s.invalidate()
	return nil
}

// DeleteRoute 删除一条路由。
func (s *UpstreamChannelService) DeleteRoute(ctx context.Context, id uint64) error {
	if err := s.repo.DeleteRoute(ctx, id); err != nil {
		return errcode.DBError.Wrap(err)
	}
	s.invalidate()
	return nil
}

// ListRoutes 列表查询。
func (s *UpstreamChannelService) ListRoutes(ctx context.Context, req *dto.RouteListReq) ([]*dto.RouteDTO, int64, error) {
	f := repo.RouteListFilter{
		ModelCode: req.ModelCode,
		ChannelID: req.ChannelID,
		Enabled:   req.Enabled,
		Page:      req.Page,
		PageSize:  req.PageSize,
	}
	if req.VariantKey != "" {
		v := req.VariantKey
		f.VariantKey = &v
	}
	rows, total, err := s.repo.ListRoutes(ctx, f)
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	s.ensureLoaded(ctx)
	out := make([]*dto.RouteDTO, 0, len(rows))
	for _, r := range rows {
		dtoRow := &dto.RouteDTO{
			ID:                r.ID,
			ModelCode:         r.ModelCode,
			VariantKey:        r.VariantKey,
			UpstreamChannelID: r.UpstreamChannelID,
			Priority:          r.Priority,
			Enabled:           r.Enabled,
			CostMultiplier:    r.CostMultiplier,
		}
		if r.Notes != nil {
			dtoRow.Notes = *r.Notes
		}
		s.mu.RLock()
		if ch, ok := s.channelByID[r.UpstreamChannelID]; ok {
			dtoRow.ChannelKey = ch.Key
			dtoRow.ChannelLabel = ch.Label
			dtoRow.ChannelProvider = ch.Provider
		}
		s.mu.RUnlock()
		out = append(out, dtoRow)
	}
	return out, total, nil
}

// === Seed ===

// SeedIfEmpty 启动时调。两件事：
//  1. 幂等保证 local.pool 通道存在（任何时候都需要——本地号池总通道）；
//  2. 若 channel 表此前完全空（首次启动），再额外植入 15 条外部 API + 内部细分通道模板，
//     运营之后可以删除或填 api_key 后启用。
//
// 这样后续部署不会重复种数据；只有首次 fresh DB 才看到模板。
func (s *UpstreamChannelService) SeedIfEmpty(ctx context.Context) error {
	if err := s.ensureLocalPoolChannel(ctx); err != nil {
		return err
	}
	cn, err := s.repo.CountChannels(ctx)
	if err != nil {
		return err
	}
	// > 1 表示除了 local.pool 还有别的；不再种模板，避免重复
	if cn > 1 {
		return nil
	}
	// 通道清单
	channels := []dto.ChannelSaveReq{
		// GPT web，免费但调度成本主要在号池；用 subscription 摊销 + monthly_fixed_cost = 平均一个号月维护成本
		{
			Key: "gpt.web.image_1k", Provider: "gpt", Route: "web", BaseURL: "https://chatgpt.com",
			Label: "GPT Web · gpt-image-2 1K（号池摊销）",
			BillingMode: model.BillingModeSubscription, Currency: "USD",
			UnitPrice:    map[string]any{},
			Capabilities: map[string]any{"kinds": []string{"image"}, "variants": []string{"1k"}},
			MonthlyFixedCost: 0, ExpectedMonthlyCalls: 1000, FXToCNY: 7.2,
			Notes: "ChatGPT web 路径出图；上游本身免费，但号池有维护成本（注册、刷新、代理）。如需精确，运营在此填 monthly_fixed_cost 平摊。",
		},
		{
			Key: "gpt.api.image_2", Provider: "gpt", Route: "api", BaseURL: "https://api.openai.com",
			Label: "GPT API · gpt-image-2 (官方)",
			BillingMode: model.BillingModePerCall, Currency: "USD",
			UnitPrice:    map[string]any{"micro_usd": 40000}, // $0.04
			Capabilities: map[string]any{"kinds": []string{"image"}},
			FXToCNY:      7.2,
			Notes:        "走 api.openai.com /v1/images/generations；标准价 0.04 USD/张（gpt-image-2-standard），HD/超大尺寸需运营手改。",
		},
		{
			Key: "gpt.api.chat_4o_mini", Provider: "gpt", Route: "api", BaseURL: "https://api.openai.com",
			Label: "GPT API · gpt-4o-mini",
			BillingMode: model.BillingModePerTokenIO, Currency: "USD",
			UnitPrice: map[string]any{
				"input_per_1k_micro_usd":  150,  // $0.00015
				"output_per_1k_micro_usd": 600,  // $0.0006
			},
			Capabilities: map[string]any{"kinds": []string{"chat"}},
			FXToCNY:      7.2,
		},
		// Adobe Firefly：每月固定 credit 包；这里假设 5000 credit / $9.99 ≈ $0.001998/credit
		{
			Key: "adobe.firefly.image_1k", Provider: "adobe", Route: "firefly", BaseURL: "https://firefly-api.adobe.io",
			Label: "Adobe Firefly · 1K（每次 1 credit）",
			BillingMode: model.BillingModePerCredit, Currency: "USD",
			UnitPrice: map[string]any{
				"credits_per_call":      1,
				"credits_per_month":     5000,
				"monthly_pack_micro_usd": 9990000, // $9.99
			},
			Capabilities: map[string]any{"kinds": []string{"image"}, "variants": []string{"1k"}},
			MonthlyFixedCost: 9990000, ExpectedMonthlyCalls: 5000, FXToCNY: 7.2,
		},
		{
			Key: "adobe.firefly.image_2k", Provider: "adobe", Route: "firefly", BaseURL: "https://firefly-api.adobe.io",
			Label: "Adobe Firefly · 2K（每次 4 credit）",
			BillingMode: model.BillingModePerCredit, Currency: "USD",
			UnitPrice: map[string]any{
				"credits_per_call":       4,
				"credits_per_month":      5000,
				"monthly_pack_micro_usd": 9990000,
			},
			Capabilities: map[string]any{"kinds": []string{"image"}, "variants": []string{"2k"}},
			MonthlyFixedCost: 9990000, ExpectedMonthlyCalls: 5000, FXToCNY: 7.2,
		},
		{
			Key: "adobe.firefly.image_4k", Provider: "adobe", Route: "firefly", BaseURL: "https://firefly-api.adobe.io",
			Label: "Adobe Firefly · 4K（每次 16 credit）",
			BillingMode: model.BillingModePerCredit, Currency: "USD",
			UnitPrice: map[string]any{
				"credits_per_call":       16,
				"credits_per_month":      5000,
				"monthly_pack_micro_usd": 9990000,
			},
			Capabilities: map[string]any{"kinds": []string{"image"}, "variants": []string{"4k"}},
			MonthlyFixedCost: 9990000, ExpectedMonthlyCalls: 5000, FXToCNY: 7.2,
		},
		// Grok web 链路（号池摊销）
		{
			Key: "grok.web.image", Provider: "grok", Route: "web", BaseURL: "https://grok.com",
			Label: "Grok Web · 图像",
			BillingMode: model.BillingModeSubscription, Currency: "USD",
			UnitPrice:    map[string]any{},
			Capabilities: map[string]any{"kinds": []string{"image"}},
			FXToCNY:      7.2,
			Notes:        "Grok web 路径出图；号池摊销（Plus 订阅 / 代理 / 验证码成本）。",
		},
		{
			Key: "grok.web.video", Provider: "grok", Route: "web", BaseURL: "https://grok.com",
			Label: "Grok Web · 视频",
			BillingMode: model.BillingModeSubscription, Currency: "USD",
			UnitPrice:    map[string]any{},
			Capabilities: map[string]any{"kinds": []string{"video"}, "variants": []string{"6", "10", "20", "30"}},
			MonthlyFixedCost: 9900000, ExpectedMonthlyCalls: 200, FXToCNY: 7.2,
			Notes: "Grok 视频 5/10/20/30 秒，号池摊销；单 Plus 月费 $9.9 ÷ 月生成 200 次 ≈ $0.05/次。",
		},
		// Adobe Creative Cloud 订阅（如果运营是用 Premium Plan 而不是 credit pack）
		{
			Key: "adobe.cc.premium", Provider: "adobe", Route: "firefly", BaseURL: "https://firefly-api.adobe.io",
			Label: "Adobe Creative Cloud Premium（订阅平摊）",
			BillingMode: model.BillingModeSubscription, Currency: "USD",
			UnitPrice:        map[string]any{},
			Capabilities:     map[string]any{"kinds": []string{"image"}, "variants": []string{"1k", "2k", "4k"}},
			MonthlyFixedCost: 59990000, ExpectedMonthlyCalls: 30000, FXToCNY: 7.2,
			Notes: "Adobe CC Premium $59.99/月，可摊到所有 firefly 调用上。需要在 admin UI 路由表把它指向具体 model_code。",
		},
		// pic2api gemini
		{
			Key: "pic2api.gemini.flash", Provider: "pic2api", Route: "api", BaseURL: "https://api.pic2api.com",
			Label: "pic2api · gemini-2.5-flash-image",
			BillingMode: model.BillingModePerCall, Currency: "USD",
			UnitPrice:    map[string]any{"micro_usd": 8000},
			Capabilities: map[string]any{"kinds": []string{"image"}},
			FXToCNY:      7.2,
		},
		{
			Key: "pic2api.gemini.pro", Provider: "pic2api", Route: "api", BaseURL: "https://api.pic2api.com",
			Label: "pic2api · gemini-2.5-pro-image",
			BillingMode: model.BillingModePerCall, Currency: "USD",
			UnitPrice:    map[string]any{"micro_usd": 20000},
			Capabilities: map[string]any{"kinds": []string{"image"}},
			FXToCNY:      7.2,
		},
		// Plus 升级 (Phase E 真正使用，先建好通道)
		{
			Key: "geelark.cloud_phone", Provider: "geelark", Route: "rent", BaseURL: "https://openapi.geelark.cn",
			Label: "GeeLark 云手机租用",
			BillingMode: model.BillingModePerCall, Currency: "USD",
			UnitPrice:    map[string]any{"micro_usd": 12000}, // $0.012 ≈ 一次开机使用费
			Capabilities: map[string]any{"kinds": []string{"register"}},
			FXToCNY:      7.2,
		},
		{
			Key: "smspool.herosms", Provider: "smspool", Route: "rent", BaseURL: "https://herosms.io",
			Label: "HeroSMS · 短信验证码",
			BillingMode: model.BillingModePerCall, Currency: "USD",
			UnitPrice:    map[string]any{"micro_usd": 35000},
			Capabilities: map[string]any{"kinds": []string{"register"}},
			FXToCNY:      7.2,
		},
		{
			Key: "captcha.capsolver", Provider: "capsolver", Route: "solve", BaseURL: "https://api.capsolver.com",
			Label: "Capsolver · 人机验证",
			BillingMode: model.BillingModePerCall, Currency: "USD",
			UnitPrice:    map[string]any{"micro_usd": 2000},
			Capabilities: map[string]any{"kinds": []string{"register"}},
			FXToCNY:      7.2,
		},
		{
			Key: "proxy.residential", Provider: "proxy", Route: "pool", BaseURL: "",
			Label: "代理池（住宅/数据中心）",
			BillingMode: model.BillingModePerUnit, Currency: "USD",
			UnitPrice:    map[string]any{"micro_usd_per_unit": 6500},
			Capabilities: map[string]any{"kinds": []string{"register", "image", "video", "chat"}},
			FXToCNY:      7.2,
			Notes:        "按 GB 计费 ≈ $6.5/GB；单次调用一般折算 0.1 MB，所以 cost_recorder 在 Phase E 才用上",
		},
	}
	for i := range channels {
		req := channels[i]
		if _, err := s.CreateChannel(ctx, &req); err != nil {
			return fmt.Errorf("seed channel %s: %w", req.Key, err)
		}
	}

	// 路由清单：(model_code, variant) → channel.key
	type seedRoute struct {
		ModelCode  string
		VariantKey string
		ChannelKey string
		Priority   int16
		Multiplier float64
		Notes      string
	}
	routes := []seedRoute{
		// gpt-image-2: 1K 走 web、2K/4K 走 adobe firefly
		{"gpt-image-2", "1k", "gpt.web.image_1k", 1, 1.0, "1K 走 web 链路"},
		{"gpt-image-2", "2k", "adobe.firefly.image_2k", 1, 1.0, "2K 走 Adobe Firefly"},
		{"gpt-image-2", "4k", "adobe.firefly.image_4k", 1, 1.0, "4K 走 Adobe Firefly"},
		// 备用：API 路径
		{"gpt-image-2", "1k", "gpt.api.image_2", 9, 1.0, "API 路径，备用"},
		// nano-banana 系列（adobe firefly 各档位）
		{"nano-banana", "1k", "adobe.firefly.image_1k", 1, 1.0, ""},
		{"nano-banana", "2k", "adobe.firefly.image_2k", 1, 1.0, ""},
		{"nano-banana", "4k", "adobe.firefly.image_4k", 1, 1.0, ""},
		{"nano-banana-v2", "1k", "adobe.firefly.image_1k", 1, 1.0, ""},
		{"nano-banana-v2", "2k", "adobe.firefly.image_2k", 1, 1.0, ""},
		{"nano-banana-v2", "4k", "adobe.firefly.image_4k", 1, 1.0, ""},
		{"nano-banana-pro", "1k", "adobe.firefly.image_1k", 1, 1.5, "Pro 模型预估 1.5×"},
		{"nano-banana-pro", "2k", "adobe.firefly.image_2k", 1, 1.5, ""},
		{"nano-banana-pro", "4k", "adobe.firefly.image_4k", 1, 1.5, ""},
		// Grok 视频
		{"grok-imagine-video", "6", "grok.web.video", 1, 1.0, ""},
		{"grok-imagine-video", "10", "grok.web.video", 1, 1.5, ""},
		{"grok-imagine-video", "20", "grok.web.video", 1, 3.0, ""},
		{"grok-imagine-video", "30", "grok.web.video", 1, 4.5, ""},
		{"vid-v1", "", "grok.web.video", 1, 1.0, ""},
		{"vid-i2v", "", "grok.web.video", 1, 1.3, ""},
		// Grok chat
		{"grok-4.20-fast", "", "grok.web.image", 1, 1.0, "复用 grok.web 通道"}, // 暂用 grok.web.image 通道，后续可以单独建 chat 通道
		{"grok-4.20-auto", "", "grok.web.image", 1, 1.0, ""},
		{"grok-4.20-expert", "", "grok.web.image", 1, 1.5, ""},
		{"grok-4.20-heavy", "", "grok.web.image", 1, 2.0, ""},
		// GPT chat
		{"gpt-4o-mini", "", "gpt.api.chat_4o_mini", 1, 1.0, ""},
		// gemini 兼容
		{"gemini-2.5-flash-image", "", "pic2api.gemini.flash", 1, 1.0, ""},
		{"gemini-2.5-pro-image", "", "pic2api.gemini.pro", 1, 1.0, ""},
	}
	for _, rt := range routes {
		// 通过 key 查 channel id
		ch, err := s.repo.GetChannelByKey(ctx, rt.ChannelKey)
		if err != nil {
			continue
		}
		notes := rt.Notes
		req := dto.RouteSaveReq{
			ModelCode:         rt.ModelCode,
			VariantKey:        rt.VariantKey,
			UpstreamChannelID: ch.ID,
			Priority:          rt.Priority,
			CostMultiplier:    rt.Multiplier,
			Notes:             notes,
		}
		if _, err := s.CreateRoute(ctx, &req); err != nil {
			return fmt.Errorf("seed route %s/%s: %w", rt.ModelCode, rt.VariantKey, err)
		}
	}
	return nil
}

// === helpers ===

func channelToDTO(c *model.UpstreamChannel) *dto.ChannelDTO {
	if c == nil {
		return nil
	}
	out := &dto.ChannelDTO{
		ID:                   c.ID,
		Key:                  c.Key,
		ChannelType:          c.ChannelType,
		Provider:             c.Provider,
		Route:                c.Route,
		BaseURL:              c.BaseURL,
		Label:                c.Label,
		Enabled:              c.Enabled,
		BillingMode:          c.BillingMode,
		Currency:             c.Currency,
		MonthlyFixedCost:     c.MonthlyFixedCost,
		ExpectedMonthlyCalls: c.ExpectedMonthlyCalls,
		FXToCNY:              c.FXToCNY,
		HasAPIKey:            len(c.APIKeyEnc) > 0,
		CreatedAt:            c.CreatedAt.Format(time.RFC3339),
		UpdatedAt:            c.UpdatedAt.Format(time.RFC3339),
	}
	if out.ChannelType == "" {
		// 旧数据兼容：迁移前的行没有 channel_type，按 external_api 处理
		out.ChannelType = model.ChannelTypeExternalAPI
	}
	if c.Notes != nil {
		out.Notes = *c.Notes
	}
	if c.UnitPrice != "" {
		_ = json.Unmarshal([]byte(c.UnitPrice), &out.UnitPrice)
	}
	if c.Capabilities != "" {
		_ = json.Unmarshal([]byte(c.Capabilities), &out.Capabilities)
	}
	if c.SupportedModels != nil && strings.TrimSpace(*c.SupportedModels) != "" {
		_ = json.Unmarshal([]byte(*c.SupportedModels), &out.SupportedModels)
	}
	if len(c.APIKeyEnc) > 0 {
		out.APIKeyMasked = "••••" // 不暴露任何信息，提示"已配"
	}
	return out
}

func encodeJSONOrEmpty(v map[string]any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "{}", nil
	}
	return string(raw), nil
}

// ensureLocalPoolChannel 幂等地保证 local.pool 通道存在；
// 已存在时不会覆盖运营改过的字段。
func (s *UpstreamChannelService) ensureLocalPoolChannel(ctx context.Context) error {
	if exists, err := s.repo.GetChannelByKey(ctx, LocalPoolChannelKey); err == nil && exists != nil {
		// 老库可能 channel_type 为空字符串 / external_api，强制改回 local_pool
		if exists.ChannelType != model.ChannelTypeLocalPool {
			if err := s.repo.UpdateChannel(ctx, exists.ID, map[string]any{"channel_type": model.ChannelTypeLocalPool}); err != nil {
				return err
			}
			s.invalidate()
		}
		return nil
	}
	enabled := true
	req := &dto.ChannelSaveReq{
		Key:                  LocalPoolChannelKey,
		ChannelType:          model.ChannelTypeLocalPool,
		Provider:             "*", // 占位；runtime 不读这个字段，按请求 model 反查
		Route:                "pool",
		Label:                "本地号池（自家 GPT / Grok / Adobe）",
		Enabled:              &enabled,
		BillingMode:          model.BillingModeSubscription,
		UnitPrice:            map[string]any{},
		Currency:             "USD",
		Capabilities:         map[string]any{"kinds": []string{"image", "video", "chat"}},
		MonthlyFixedCost:     0,
		ExpectedMonthlyCalls: 0,
		FXToCNY:              7.2,
		Notes:                "系统内置通道：runtime 看到这条路由时，会根据请求 model 反查到 pool_gpt / pool_grok / pool_adobe 三张本地号池表选号。\n如需估算成本，运营可在 monthly_fixed_cost 填月度号池维护费、expected_monthly_calls 填月度预估调用次数，CostRecorder 会把月费摊到每次调用上。",
	}
	if _, err := s.CreateChannel(ctx, req); err != nil {
		return err
	}
	return nil
}

func validBillingMode(s string) bool {
	switch s {
	case model.BillingModePerCall,
		model.BillingModePerUnit,
		model.BillingModePerTokenIO,
		model.BillingModePerCredit,
		model.BillingModeSubscription,
		model.BillingModeCustom:
		return true
	}
	return false
}
