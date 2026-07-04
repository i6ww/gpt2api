// Package service 模型计费表（开发期内置；后续从 model 表读取并缓存）。
package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/kleinai/backend/internal/provider"
)

// DefaultPriceTable 默认计费（与 migrations/seed 对齐）。
//
// 单位：点 *100。例：400 = 4 点 / 张图。
var DefaultPriceTable = map[string]int64{
	"gpt-4o-mini":                100,
	"gpt-image-2":                0,
	"vid-v1":                     1500, // 4 秒视频
	"vid-i2v":                    2000,
	"grok-imagine-video":         2000,
	"grok-imagine-video-6s-free": 0, // 免额度 Imagine Pipeline 通道，本站也免费
}

// DefaultImageVariantTable 默认图片分档价（点 ×100）。仅作为 billing.model_prices
// 没存档时的兜底，UI 已经把 1K/2K/4K 选项直接暴露给用户。Adobe 2K/4K 实际 Firefly
// quota 消耗约为 1K 的 4×/16×，先按 4× 阶梯定，admin 后台可改。
var DefaultImageVariantTable = map[string]map[string]int64{
	"gpt-image-2": {
		"1k": 400,
		"2k": 1500,
		"4k": 3000,
	},
	"nano-banana": {
		"1k": 800,
		"2k": 1500,
		"4k": 3000,
	},
	"nano-banana-v2": {
		"1k": 800,
		"2k": 1500,
		"4k": 3000,
	},
	"nano-banana-pro": {
		"1k": 1500,
		"2k": 3000,
		"4k": 6000,
	},
}

// DefaultVideoVariantTable 默认视频分档价（点 ×100）。Grok web 链路 20s / 30s 是用
// extension 链拼出来的，每段约 ~100s 推理，所以阶梯递增。admin 后台可改。
var DefaultVideoVariantTable = map[string]map[string]int64{
	"grok-imagine-video": {
		"6":  1500,
		"10": 2500,
		"20": 5000,
		"30": 7500,
	},
	"grok-imagine-video-6s-free": {
		"6": 0, // pipeline 通道固定 6s，且 creditCost=0
	},
	"vid-v1": {
		"6":  1500,
		"10": 2500,
		"20": 5000,
		"30": 7500,
	},
	"vid-i2v": {
		"6":  2000,
		"10": 3000,
		"20": 6000,
		"30": 9000,
	},
}

const (
	// VideoPricingModeScaled：legacy 计费——base × (duration / 6)。20s/30s 任务
	// 在没设 video_pricing map 时会按线性倍率扣分。
	VideoPricingModeScaled = "scaled"
	// VideoPricingModeFlat：不管 duration 一律 base 价。
	VideoPricingModeFlat = "flat"
	// VideoPricingModeVariant：优先按 video_pricing map[duration→price] 取价；
	// map 里没该 duration 时再退到 scaled 倍率。
	VideoPricingModeVariant = "variant"
)

// ChatPrice 单位：points*100 / 每 1M tokens。
//
// 历史上本结构按「每 1K tokens」计价，但平台点值 1 点 = 1 元、内部最小整数单位
// = 0.01 元，导致按 1K 计的最小可计费价高达 10 元/1M，无法表达便宜模型的
// 「成本+小幅加价」。2026-06 起改为按 1M tokens 计价，精度 0.01 元/1M，可精确
// 设置低价模型的毛利。原有（全部 enabled=false 的）文字模型已等比换算。
type ChatPrice struct {
	InputPerM  int64
	OutputPerM int64
}

// DefaultChatPriceFn returns default token prices in points*100 per 1M tokens.
// 数值为旧「每 1K」默认值 ×1000，保持原有计费等价（仅作未配置模型的兜底）。
func DefaultChatPriceFn(modelCode string) ChatPrice {
	switch modelCode {
	case "gpt-4o-mini":
		return ChatPrice{InputPerM: 100000, OutputPerM: 300000}
	case "gpt-5.4":
		return ChatPrice{InputPerM: 200000, OutputPerM: 600000}
	case "gpt-5.4-mini":
		return ChatPrice{InputPerM: 100000, OutputPerM: 300000}
	case "gpt-5.3-codex":
		return ChatPrice{InputPerM: 150000, OutputPerM: 450000}
	case "grok-4.20-fast":
		return ChatPrice{InputPerM: 100000, OutputPerM: 300000}
	case "grok-4.20-auto":
		return ChatPrice{InputPerM: 150000, OutputPerM: 450000}
	case "grok-4.20-expert":
		return ChatPrice{InputPerM: 200000, OutputPerM: 600000}
	case "grok-4.20-heavy":
		return ChatPrice{InputPerM: 400000, OutputPerM: 1200000}
	case "grok-4.3-beta":
		return ChatPrice{InputPerM: 300000, OutputPerM: 900000}
	default:
		return ChatPrice{InputPerM: 100000, OutputPerM: 300000}
	}
}

// DefaultPriceFn 实现 PriceFunc。
//
// 取价顺序：
//  1. 图片任务且模型在 DefaultImageVariantTable 里：按 params.resolution 选 1k/2k/4k 分档
//  2. 视频任务且模型在 DefaultVideoVariantTable 里：按模型支持的 params.duration 分档
//  3. 否则退到 DefaultPriceTable 的 flat 单价，再按 scaled 倍率给视频拉伸
func DefaultPriceFn(modelCode string, kind provider.Kind, params map[string]any) int64 {
	if kind == provider.KindImage {
		if variants, ok := DefaultImageVariantTable[modelCode]; ok {
			if v := applyImagePrice(0, kind, modelCode, params, variants); v > 0 {
				return v
			}
		}
	}
	if kind == provider.KindVideo {
		if variants, ok := DefaultVideoVariantTable[modelCode]; ok {
			if v := applyVideoPriceWithVariantsForModel(0, kind, modelCode, params, VideoPricingModeVariant, variants); v > 0 {
				return v
			}
		}
	}
	if v, ok := DefaultPriceTable[modelCode]; ok {
		return applyVideoPriceForModel(v, kind, modelCode, params, VideoPricingModeScaled)
	}
	switch kind {
	case provider.KindImage:
		return 400
	case provider.KindVideo:
		// 没列入计费表的 video 模型也走 scaled 倍率，避免 30s 任务按 6s 收钱。
		return applyVideoPrice(1500, kind, params, VideoPricingModeScaled)
	}
	return 0
}

func ConfigPriceFn(cfg *SystemConfigService) PriceFunc {
	return func(modelCode string, kind provider.Kind, params map[string]any) int64 {
		if cfg != nil {
			raw := cfg.GetString(context.Background(), "billing.model_prices", "")
			if raw != "" {
				var rows []struct {
					ModelCode        string           `json:"model_code"`
					UnitPoints       int64            `json:"unit_points"`
					VideoPricingMode string           `json:"video_pricing_mode"`
					ImagePricing     map[string]int64 `json:"image_pricing"`
					VideoPricing     map[string]int64 `json:"video_pricing"`
					Enabled          *bool            `json:"enabled"`
				}
				if err := json.Unmarshal([]byte(raw), &rows); err == nil {
					for _, row := range rows {
						if row.ModelCode != modelCode {
							continue
						}
						if row.Enabled != nil && !*row.Enabled {
							continue
						}
						switch kind {
						case provider.KindImage:
							if v := applyImagePrice(row.UnitPoints, kind, modelCode, params, row.ImagePricing); v > 0 {
								return v
							}
							return row.UnitPoints
						case provider.KindVideo:
							return applyVideoPriceWithVariantsForModel(row.UnitPoints, kind, modelCode, params, row.VideoPricingMode, row.VideoPricing)
						}
						return row.UnitPoints
					}
				}
				var prices map[string]int64
				if err := json.Unmarshal([]byte(raw), &prices); err == nil {
					if v, ok := prices[modelCode]; ok {
						return applyVideoPriceForModel(v, kind, modelCode, params, VideoPricingModeScaled)
					}
				}
			}
		}
		return DefaultPriceFn(modelCode, kind, params)
	}
}

func ConfigChatPriceFn(cfg *SystemConfigService) func(modelCode string) ChatPrice {
	return func(modelCode string) ChatPrice {
		def := DefaultChatPriceFn(modelCode)
		if cfg == nil {
			return def
		}
		raw := cfg.GetString(context.Background(), "billing.model_prices", "")
		if raw == "" {
			return def
		}
		var rows []struct {
			ModelCode        string `json:"model_code"`
			Kind             string `json:"kind"`
			UnitPoints       *int64 `json:"unit_points"`
			InputUnitPoints  *int64 `json:"input_unit_points"`
			OutputUnitPoints *int64 `json:"output_unit_points"`
			Enabled          *bool  `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(raw), &rows); err == nil {
			for _, row := range rows {
				if row.ModelCode != modelCode {
					continue
				}
				if row.Enabled != nil && !*row.Enabled {
					continue
				}
				if row.InputUnitPoints != nil || row.OutputUnitPoints != nil {
					if row.InputUnitPoints != nil {
						def.InputPerM = *row.InputUnitPoints
					}
					if row.OutputUnitPoints != nil {
						def.OutputPerM = *row.OutputUnitPoints
					}
					return def
				}
				if row.Kind == "text" && row.UnitPoints != nil {
					return ChatPrice{InputPerM: *row.UnitPoints, OutputPerM: *row.UnitPoints}
				}
			}
		}
		return def
	}
}

func ChatCost(price ChatPrice, promptTokens, completionTokens int) int64 {
	if price.InputPerM <= 0 && price.OutputPerM <= 0 {
		return 0
	}
	const perMillion = 1_000_000
	in := (int64(promptTokens)*price.InputPerM + perMillion - 1) / perMillion
	out := (int64(completionTokens)*price.OutputPerM + perMillion - 1) / perMillion
	total := in + out
	if total <= 0 {
		return 1
	}
	return total
}

// NormalizeVideoDurationForModel 必须和各 provider 实际支持档位保持一致。
// Sora 是 4/8/12，VEO 是 4/6/8，Grok 是 6/10/20/30。
func NormalizeVideoDurationForModel(modelCode string, sec int) int {
	durations := videoDurationBucketsForModel(modelCode)
	for _, v := range durations {
		if sec <= v {
			return v
		}
	}
	return durations[len(durations)-1]
}

func videoDurationBucketsForModel(modelCode string) []int {
	full := strings.ToLower(strings.TrimSpace(modelCode))
	// 官方 xAI grok-imagine-video（带 xai/ 前缀）：上游只接受 1-15 秒。
	// 必须在剥掉前缀之前判断，否则会和网页版 grok-imagine-video（6/10/20/30
	// 走扩展链）混淆，把网页版的长视频档位误砍到 15。
	if strings.HasPrefix(full, "xai/grok-imagine") {
		return []int{6, 10, 15}
	}
	code := full
	if idx := strings.LastIndex(code, "@"); idx >= 0 {
		code = code[:idx]
	}
	if idx := strings.LastIndex(code, "/"); idx >= 0 {
		code = code[idx+1:]
	}
	switch {
	case code == "sora", code == "sora2", strings.HasPrefix(code, "sora2-"), strings.HasPrefix(code, "sora-2"),
		strings.HasPrefix(code, "firefly-sora2-"):
		return []int{4, 8, 12}
	case strings.HasPrefix(code, "veo3.1"), strings.HasPrefix(code, "veo-3.1"), strings.HasPrefix(code, "veo31"),
		strings.HasPrefix(code, "firefly-veo31-"):
		return []int{4, 6, 8}
	default:
		return []int{6, 10, 20, 30}
	}
}

// normalizeBillingVideoDuration 保留旧签名给 Grok / legacy 调用，默认使用 6/10/20/30。
func normalizeBillingVideoDuration(sec int) int {
	return NormalizeVideoDurationForModel("", sec)
}

// normalizeImageTier 把图片分档归一成 "1k|2k|4k"。
//
// 优先级：
//  1. resolution / size_tier：这是前端显式档位，最高优先级；
//  2. size：OpenAI 兼容 API 常只传 size + quality。按长边推断档位（与 firefly
//     inferGPTImageTier 一致），避免 gpt-image-2 的 1024×1536（1K 2:3）因面积
//     >1.5M 被误按 2K 收费；
//  3. quality：作为最后兜底（low/standard/high/ultra）。
//
// size 推断阈值（长边）：≤1600=1k, ≤2800=2k, >2800=4k。
func normalizeImageTier(params map[string]any) string {
	for _, key := range []string{"resolution", "size_tier"} {
		raw, _ := params[key].(string)
		raw = strings.ToLower(strings.TrimSpace(raw))
		switch raw {
		case "1", "1k", "low", "standard":
			return "1k"
		case "2", "2k", "high":
			return "2k"
		case "4", "4k", "ultra":
			return "4k"
		}
	}
	if size, _ := params["size"].(string); size != "" {
		size = strings.ToLower(strings.TrimSpace(size))
		switch size {
		case "auto", "自动", "自动跟随", "reference", "ref", "follow":
			// gpt-image-2 的 AUTO 会在 Adobe fallback/catalog 中解析到 2K auto 变体；
			// 这里只选档位，价格仍严格使用后台 billing.model_prices 配置的原始内部点数。
			return "2k"
		}
		if i := strings.Index(size, "x"); i > 0 {
			w, errW := strconv.Atoi(strings.TrimSpace(size[:i]))
			h, errH := strconv.Atoi(strings.TrimSpace(size[i+1:]))
			if errW == nil && errH == nil && w > 0 && h > 0 {
				if tier := tierFromLongEdge(w, h); tier != "" {
					return tier
				}
			}
		}
	}
	raw, _ := params["quality"].(string)
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "1", "1k", "low", "standard":
		return "1k"
	case "2", "2k", "high":
		return "2k"
	case "4", "4k", "ultra":
		return "4k"
	}
	return ""
}

// TierFromPixels 按输出长边推断 1k/2k/4k，与 firefly.inferGPTImageTier 阈值一致。
// 不能用总像素面积：gpt-image-2 的 1K 2:3（1024×1536）面积 >1.5M，按面积会误成 2K。
func TierFromPixels(width, height int) string {
	return tierFromLongEdge(width, height)
}

func tierFromLongEdge(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	long := width
	if height > long {
		long = height
	}
	switch {
	case long <= 1600:
		return "1k"
	case long <= 2800:
		return "2k"
	default:
		return "4k"
	}
}

func isGPTImage2Model(modelCode string) bool {
	modelCode = strings.TrimSpace(modelCode)
	if idx := strings.LastIndex(modelCode, "@"); idx >= 0 {
		modelCode = modelCode[:idx]
	}
	if idx := strings.LastIndex(modelCode, "/"); idx >= 0 {
		modelCode = modelCode[idx+1:]
	}
	return modelCode == "gpt-image-2"
}

func hasExplicitImageResolutionParam(params map[string]any) bool {
	for _, key := range []string{"resolution", "size_tier"} {
		raw, _ := params[key].(string)
		raw = strings.ToUpper(strings.TrimSpace(raw))
		switch raw {
		case "1", "1K", "2", "2K", "4", "4K":
			return true
		}
	}
	return false
}

// gptImage2TierFromParams 对齐 firefly.inferGPTImageTier：gpt-image-2 的 quality=high
// 等信号代表 4K，而不是 OpenAI 兼容层里通用的 2K。
func gptImage2TierFromParams(params map[string]any) string {
	raw, _ := params["quality"].(string)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1k", "standard", "std", "low", "fast", "quick", "preview", "hd-fast":
		return "1k"
	case "2k", "hd", "medium", "med", "hd-std", "hd-high":
		return "2k"
	case "4k", "ultra", "high", "fine", "max":
		return "4k"
	}
	if size, _ := params["size"].(string); size != "" {
		size = strings.ToLower(strings.TrimSpace(size))
		if i := strings.Index(size, "x"); i > 0 {
			w, errW := strconv.Atoi(strings.TrimSpace(size[:i]))
			h, errH := strconv.Atoi(strings.TrimSpace(size[i+1:]))
			if errW == nil && errH == nil && w > 0 && h > 0 {
				long := w
				if h > long {
					long = h
				}
				switch {
				case long <= 1600:
					return "1k"
				case long <= 2800:
					return "2k"
				default:
					return "4k"
				}
			}
		}
	}
	return ""
}

func imageTierRank(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "1k":
		return 1
	case "2k":
		return 2
	case "4k":
		return 3
	default:
		return 0
	}
}

// normalizeImageTierForModel 在 normalizeImageTier 基础上补 gpt-image-2 专用 quality 语义。
func normalizeImageTierForModel(modelCode string, params map[string]any) string {
	tier := normalizeImageTier(params)
	if !isGPTImage2Model(modelCode) || hasExplicitImageResolutionParam(params) {
		return tier
	}
	gptTier := gptImage2TierFromParams(params)
	if gptTier == "" {
		return tier
	}
	if tier == "" || (tier == "2k" && imageTierRank(gptTier) > imageTierRank(tier)) {
		return gptTier
	}
	return tier
}

// ImageBillingParams 返回用于计价的 params 副本；若实际输出档位高于请求推断档位，
// 注入 resolution 让结算与生成结果一致。
func ImageBillingParams(modelCode string, params map[string]any, outputWidth, outputHeight int) map[string]any {
	billing := cloneStringAnyMap(params)
	paramTier := normalizeImageTierForModel(modelCode, billing)
	outTier := TierFromPixels(outputWidth, outputHeight)
	if imageTierRank(outTier) > imageTierRank(paramTier) {
		billing["resolution"] = strings.ToUpper(outTier)
	}
	return billing
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// MaxOutputImageTier 从生成结果里取最高输出档位。
func MaxOutputImageTier(widths, heights []int) string {
	best := ""
	for i := range widths {
		if i >= len(heights) {
			break
		}
		tier := TierFromPixels(widths[i], heights[i])
		if imageTierRank(tier) > imageTierRank(best) {
			best = tier
		}
	}
	return best
}

// applyImagePrice 用 image_pricing map（如果存档了）按 1k/2k/4k 分档取价；
// 没命中分档时退回 base。返回 0 表示「无分档可用」，给上层留 fallback 路径。
func applyImagePrice(base int64, kind provider.Kind, modelCode string, params map[string]any, variants map[string]int64) int64 {
	if kind != provider.KindImage || len(variants) == 0 {
		return base
	}
	tier := normalizeImageTierForModel(modelCode, params)
	if tier == "" {
		return base
	}
	if v, ok := variants[tier]; ok && v > 0 {
		return v
	}
	return base
}

func videoDurationFromParams(params map[string]any) (int, bool) {
	if v, ok := params["duration"].(float64); ok && v > 0 {
		return int(v), true
	}
	if v, ok := params["duration"].(int); ok && v > 0 {
		return v, true
	}
	if v, ok := params["duration"].(string); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

func applyVideoPrice(base int64, kind provider.Kind, params map[string]any, mode string) int64 {
	return applyVideoPriceWithVariantsForModel(base, kind, "", params, mode, nil)
}

// applyVideoPriceWithVariants 视频计费总入口：
//  1. variant 模式：从 variants map[duration→price] 直接拿
//  2. flat 模式：原样返回 base
//  3. scaled 模式（默认）：base × (duration / 6)
//
// duration 先经 NormalizeVideoDurationForModel 拍到模型自己的合法档位。
func applyVideoPriceWithVariants(base int64, kind provider.Kind, params map[string]any, mode string, variants map[string]int64) int64 {
	return applyVideoPriceWithVariantsForModel(base, kind, "", params, mode, variants)
}

func applyVideoPriceForModel(base int64, kind provider.Kind, modelCode string, params map[string]any, mode string) int64 {
	return applyVideoPriceWithVariantsForModel(base, kind, modelCode, params, mode, nil)
}

func applyVideoPriceWithVariantsForModel(base int64, kind provider.Kind, modelCode string, params map[string]any, mode string, variants map[string]int64) int64 {
	if kind != provider.KindVideo {
		return base
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	dur, hasDur := videoDurationFromParams(params)
	if hasDur {
		dur = NormalizeVideoDurationForModel(modelCode, dur)
	}
	// 1) variant 模式或显式 variants：先查 map
	if (mode == VideoPricingModeVariant || len(variants) > 0) && hasDur {
		if v, ok := variants[strconv.Itoa(dur)]; ok && v > 0 {
			return v
		}
	}
	if mode == VideoPricingModeFlat {
		return base
	}
	if !hasDur {
		return base
	}
	if dur <= 6 {
		return base
	}
	factor := float64(dur) / 6
	return int64(float64(base) * factor)
}
