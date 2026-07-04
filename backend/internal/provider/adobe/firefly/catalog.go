package firefly

import (
	"fmt"
	"strings"
)

// ModelType 区分图片/视频。
type ModelType string

const (
	ModelTypeImage ModelType = "image"
	ModelTypeVideo ModelType = "video"
)

// ModelDef 内部 catalog 条目。一个 (family, tier, aspect) 组合对应一条。
type ModelDef struct {
	ID                   string
	Type                 ModelType
	UpstreamModelID      string
	UpstreamModelVersion string
	UpstreamModel        string // 视频专用，例如 "openai:firefly:colligo:sora2-pro"
	Engine               string // sora2 / veo31-standard / veo31-fast
	ReferenceMode        string // "" / "image" / "frame"
	AspectRatio          string // "16:9", "9:16", "1:1", ...
	Resolution           string // "1k" / "2k" / "4k"
	Duration             int    // 视频秒数 4/6/8/12
	VideoResolution      string // "720p" / "1080p"
}

// DefaultModelID 当 modelID 为空时的兜底。
const DefaultModelID = "firefly-nano-banana-pro-2k-16x9"

// Catalog 全部内部 catalog 条目（init 里填充）。
var Catalog map[string]ModelDef

func init() {
	Catalog = make(map[string]ModelDef)
	registerImageModels()
	registerGPTImageModels()
	registerVideoModels()
}

// registerGPTImageModels 注册 GPT IMAGE 2 (gpt-image v2)，
// 上游通过 modelSpecificPayload.size="WxH" 接受 30 (W, H) 组合（10 比例 × 3 档）。
//
// Tier → detailLevel：
//   - "1k" → 1（~1Mpx）
//   - "2k" → 3（~4Mpx，默认）
//   - "4k" → 5（~8Mpx）
//
// 兼容 legacy hd-fast / hd-std / hd-high 别名（仅 1:1/3:2/2:3）。
func registerGPTImageModels() {
	aspects := []string{
		"1x1",
		"2x3", "3x2",
		"3x4", "4x3",
		"4x5", "5x4",
		"9x16", "16x9",
		"21x9",
	}
	for _, tier := range []string{"1k", "2k", "4k"} {
		for _, asp := range aspects {
			id := "firefly-gpt-image-2-" + tier + "-" + asp
			Catalog[id] = ModelDef{
				ID:                   id,
				Type:                 ModelTypeImage,
				UpstreamModelID:      "gpt-image",
				UpstreamModelVersion: "2",
				AspectRatio:          aspectFromShort(asp),
				Resolution:           tier,
			}
		}
	}

	legacy := map[string]string{
		"hd-fast": "1k",
		"hd-std":  "2k",
		"hd-high": "2k",
	}
	for old, neu := range legacy {
		for _, asp := range []string{"1x1", "3x2", "2x3"} {
			id := "firefly-gpt-image-2-" + old + "-" + asp
			Catalog[id] = ModelDef{
				ID:                   id,
				Type:                 ModelTypeImage,
				UpstreamModelID:      "gpt-image",
				UpstreamModelVersion: "2",
				AspectRatio:          aspectFromShort(asp),
				Resolution:           neu,
			}
		}
	}
}

func registerImageModels() {
	resolutions := []string{"1k", "2k", "4k"}

	// 三个家族支持的比例集合不同（按 Gemini 官方）：
	//
	//   gemini-2.5-flash-image-preview     → firefly-nano-banana,     nano-banana-2: 10 档
	//   gemini-3-pro-image-preview         → firefly-nano-banana-pro, nano-banana-2: 10 档
	//   gemini-3.1-flash-image-preview     → firefly-nano-banana2,    nano-banana-3: 14 档
	//     └─ 比另两者多: 1:4 / 4:1 / 1:8 / 8:1 四档极端条形
	commonAspects := []string{
		"1x1",
		"2x3", "3x2",
		"3x4", "4x3",
		"4x5", "5x4",
		"9x16", "16x9",
		"21x9",
	}
	extendedAspects := []string{
		"1x4", "4x1",
		"1x8", "8x1",
	}
	nanoBanana3Aspects := append(append([]string{}, commonAspects...), extendedAspects...)

	register := func(prefix, version string, aspects []string) {
		for _, res := range resolutions {
			for _, asp := range aspects {
				id := prefix + "-" + res + "-" + asp
				Catalog[id] = ModelDef{
					ID:                   id,
					Type:                 ModelTypeImage,
					UpstreamModelID:      "gemini-flash",
					UpstreamModelVersion: version,
					AspectRatio:          aspectFromShort(asp),
					Resolution:           res,
				}
			}
		}
	}

	register("firefly-nano-banana", "nano-banana-2", commonAspects)
	register("firefly-nano-banana-pro", "nano-banana-2", commonAspects)
	register("firefly-nano-banana2", "nano-banana-3", nanoBanana3Aspects)

	// AUTO mode: 不发 aspectRatio，上游让输出比例跟随首张参考图。
	// 计费仍按 tier。GPT IMAGE 2 不注册 -auto（上层 fallback 到 1:1）。
	registerAuto := func(prefix, version string) {
		for _, res := range resolutions {
			id := prefix + "-" + res + "-auto"
			Catalog[id] = ModelDef{
				ID:                   id,
				Type:                 ModelTypeImage,
				UpstreamModelID:      "gemini-flash",
				UpstreamModelVersion: version,
				AspectRatio:          "auto",
				Resolution:           res,
			}
		}
	}
	registerAuto("firefly-nano-banana", "nano-banana-2")
	registerAuto("firefly-nano-banana-pro", "nano-banana-2")
	registerAuto("firefly-nano-banana2", "nano-banana-3")
}

func registerVideoModels() {
	videoAspects := []string{"9x16", "16x9"}

	// sora2
	for _, dur := range []int{4, 8, 12} {
		for _, asp := range videoAspects {
			id := formatVideoID("firefly-sora2", dur, asp, "")
			Catalog[id] = ModelDef{
				ID:          id,
				Type:        ModelTypeVideo,
				Engine:      "sora2",
				AspectRatio: aspectFromShort(asp),
				Duration:    dur,
			}

			proID := formatVideoID("firefly-sora2-pro", dur, asp, "")
			Catalog[proID] = ModelDef{
				ID:            proID,
				Type:          ModelTypeVideo,
				Engine:        "sora2",
				UpstreamModel: "openai:firefly:colligo:sora2-pro",
				AspectRatio:   aspectFromShort(asp),
				Duration:      dur,
			}
		}
	}

	// veo31
	veoResolutions := []string{"1080p", "720p"}
	for _, dur := range []int{4, 6, 8} {
		for _, asp := range videoAspects {
			for _, vRes := range veoResolutions {
				id := formatVideoID("firefly-veo31", dur, asp, vRes)
				Catalog[id] = ModelDef{
					ID:              id,
					Type:            ModelTypeVideo,
					Engine:          "veo31-standard",
					AspectRatio:     aspectFromShort(asp),
					Duration:        dur,
					VideoResolution: vRes,
				}

				// veo31-ref 上游限制：只支持 8s + 16:9
				if dur == 8 && asp == "16x9" {
					refID := formatVideoID("firefly-veo31-ref", dur, asp, vRes)
					Catalog[refID] = ModelDef{
						ID:              refID,
						Type:            ModelTypeVideo,
						Engine:          "veo31-standard",
						ReferenceMode:   "image",
						AspectRatio:     aspectFromShort(asp),
						Duration:        dur,
						VideoResolution: vRes,
					}
				}

				fastID := formatVideoID("firefly-veo31-fast", dur, asp, vRes)
				Catalog[fastID] = ModelDef{
					ID:              fastID,
					Type:            ModelTypeVideo,
					Engine:          "veo31-fast",
					AspectRatio:     aspectFromShort(asp),
					Duration:        dur,
					VideoResolution: vRes,
				}

				liteID := formatVideoID("firefly-veo31-lite", dur, asp, vRes)
				Catalog[liteID] = ModelDef{
					ID:              liteID,
					Type:            ModelTypeVideo,
					Engine:          "veo31-lite",
					AspectRatio:     aspectFromShort(asp),
					Duration:        dur,
					VideoResolution: vRes,
				}
			}
		}
	}
}

func formatVideoID(prefix string, dur int, aspect, resolution string) string {
	id := prefix + "-" + itoa(dur) + "s-" + aspect
	if resolution != "" {
		id += "-" + resolution
	}
	return id
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// aspectFromShort "16x9" → "16:9"。
func aspectFromShort(s string) string {
	if strings.Contains(s, "x") {
		parts := strings.SplitN(s, "x", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0] + ":" + parts[1]
		}
	}
	return "1:1"
}

// PublicModel /v1/models 暴露给下游用户看的精简结构。
type PublicModel struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Type        string   `json:"type"` // "image" / "video"
	Description string   `json:"description,omitempty"`
	Sizes       []string `json:"sizes,omitempty"`
	Qualities   []string `json:"qualities,omitempty"`
	Durations   []int    `json:"durations,omitempty"`
	Resolutions []string `json:"resolutions,omitempty"`
}

var publicModels []PublicModel

func init() {
	// 对外暴露的 Adobe Firefly 模型清单：
	//   - Nano Banana 三件套（图像）
	//   - sora / VEO3.1 / VEO3.1-FLASH / VEO3.1-LITE 4 个视频模型
	//
	// gpt-image-2 由 GPT provider 自己接 ChatGPT Codex 路径，不在这里重复注册。
	// 之前注释「视频统一走 GROK」已废弃：grok 主通道仍保留，adobe 4 个模型作为新增并行通道。
	publicModels = []PublicModel{
		{
			ID:          "nano-banana-pro",
			DisplayName: "Nano Banana Pro",
			Type:        "image",
			Description: "High quality image generation (size=auto follows reference image)",
			Sizes:       []string{"auto", "1024x1024", "1024x576", "576x1024", "1024x768", "768x1024", "2048x2048", "2048x1152", "1152x2048", "4096x4096", "4096x2304", "2304x4096"},
			Qualities:   []string{"1k", "2k", "4k"},
		},
		{
			ID:          "nano-banana-v2",
			DisplayName: "Nano Banana V2",
			Type:        "image",
			Description: "Latest image generation model (size=auto follows reference image)",
			Sizes:       []string{"auto", "1024x1024", "1024x576", "576x1024", "2048x2048", "2048x1152", "1152x2048", "4096x4096", "4096x2304", "2304x4096"},
			Qualities:   []string{"1k", "2k", "4k"},
		},
		{
			ID:          "nano-banana",
			DisplayName: "Nano Banana",
			Type:        "image",
			Description: "Standard image generation model (size=auto follows reference image)",
			Sizes:       []string{"auto", "1024x1024", "1024x576", "576x1024", "2048x2048", "2048x1152", "1152x2048", "4096x4096", "4096x2304", "2304x4096"},
			Qualities:   []string{"1k", "2k", "4k"},
		},
		{
			ID:          "sora",
			DisplayName: "Sora 2",
			Type:        "video",
			Description: "OpenAI Sora 2 (Adobe Firefly 接口)：4/8/12 秒；最大 1 张参考图；仅支持 720P。",
			Sizes:       []string{"1280x720", "720x1280"},
			Durations:   []int{4, 8, 12},
			Resolutions: []string{"720p"},
		},
		{
			ID:          "veo3.1",
			DisplayName: "VEO 3.1",
			Type:        "video",
			Description: "Google VEO 3.1 (Adobe Firefly)：4/6/8 秒；带音频；最多 3 张 element/asset 参考图；支持 1080P。",
			Sizes:       []string{"1280x720", "720x1280", "1920x1080", "1080x1920"},
			Durations:   []int{4, 6, 8},
			Resolutions: []string{"720p", "1080p"},
		},
		{
			ID:          "veo3.1-flash",
			DisplayName: "VEO 3.1 Flash",
			Type:        "video",
			Description: "Google VEO 3.1 Fast (Adobe Firefly)：4/6/8 秒；带音频；首尾帧 2 张；支持 1080P。",
			Sizes:       []string{"1280x720", "720x1280", "1920x1080", "1080x1920"},
			Durations:   []int{4, 6, 8},
			Resolutions: []string{"720p", "1080p"},
		},
		{
			ID:          "veo3.1-lite",
			DisplayName: "VEO 3.1 Lite",
			Type:        "video",
			Description: "Google VEO 3.1 Lite (Adobe Firefly)：4/6/8 秒；带音频；首尾帧 2 张；支持 1080P。",
			Sizes:       []string{"1280x720", "720x1280", "1920x1080", "1080x1920"},
			Durations:   []int{4, 6, 8},
			Resolutions: []string{"720p", "1080p"},
		},
	}
}

// publicModelAliases 公开 ID → 默认 catalog ID。
//
// 当前对外暴露：
//   - Nano Banana 三件套（pro / v2 / 标准）固定走 Adobe Firefly。
//   - gpt-image-2 也保留 Adobe 别名，作为 2K / 4K 档的承接 provider：
//     1K 由 GenerationService 路由到 GPT web 路径，
//     2K / 4K 由 GenerationService.ImageProviderForModelWithParams 改写到 adobe，
//     然后 Adobe Provider 经 ResolvePublicAlias 命中 firefly-gpt-image-2-* 系列 catalog id。
//   - sora2 / veo3.1* 暂不开放（视频统一 GROK）。
//
// /v1/models 不会出现 gpt-image-2 重复行：openai_handler 用 billing.model_prices /
// defaultOpenAIModelItems 控制名单，firefly.publicModels（下方 init）也没有 gpt-image-2，
// 这里只是给 ResolvePublicAlias 用的内部映射表。
var publicModelAliases = map[string]string{
	"nano-banana-pro": "firefly-nano-banana-pro-2k-16x9",
	"nano-banana-v2":  "firefly-nano-banana2-2k-16x9",
	"nano-banana":     "firefly-nano-banana-2k-16x9",
	"gpt-image-2":     "firefly-gpt-image-2-2k-1x1",
	// 视频模型公开 alias → 默认 catalog id（后端 ResolvePublicAlias 会按 duration / aspect_ratio /
	// resolution 重新算出更具体的 catalog id；这里只是给 IsKnownAlias / 路由判断用的兜底入口）。
	"sora":         "firefly-sora2-8s-16x9",
	"veo3.1":       "firefly-veo31-8s-16x9-1080p",
	"veo3.1-flash": "firefly-veo31-fast-8s-16x9-1080p",
	"veo3.1-lite":  "firefly-veo31-lite-8s-16x9-1080p",
}

// ListPublicModels /v1/models 用。
func ListPublicModels() []PublicModel {
	return publicModels
}

// PublicAliases 返回所有公开 alias（用于路由表注册）。
func PublicAliases() []string {
	out := make([]string, 0, len(publicModelAliases))
	for k := range publicModelAliases {
		out = append(out, k)
	}
	return out
}

// IsKnownAlias 判断一个 modelCode 是否是 Adobe 公开 alias 或 catalog ID。
func IsKnownAlias(modelID string) bool {
	if modelID == "" {
		return false
	}
	if _, ok := Catalog[modelID]; ok {
		return true
	}
	if _, ok := publicModelAliases[modelID]; ok {
		return true
	}
	return false
}

// ResolvePublicAlias 把公开 alias + size + quality 映射到 internal catalog key。
func ResolvePublicAlias(modelID, size, quality string) string {
	if _, ok := Catalog[modelID]; ok {
		return modelID
	}
	base, ok := publicModelAliases[modelID]
	if !ok {
		return modelID
	}
	def := Catalog[base]
	if def.Type == ModelTypeImage {
		if isAutoSize(size) {
			autoRes := "2k"
			switch strings.ToLower(strings.TrimSpace(quality)) {
			case "4k", "ultra":
				autoRes = "4k"
			case "1k", "standard", "std", "low":
				autoRes = "1k"
			}
			switch modelID {
			case "nano-banana-pro":
				return "firefly-nano-banana-pro-" + autoRes + "-auto"
			case "nano-banana-v2":
				return "firefly-nano-banana2-" + autoRes + "-auto"
			case "nano-banana":
				return "firefly-nano-banana-" + autoRes + "-auto"
			}
			// gpt-image-2 fall through → 1:1 default.
		}

		asp := "16x9"
		if size != "" && !isAutoSize(size) {
			asp = inferAspectFromSize(size)
		}
		res := "2k"
		switch quality {
		case "4k", "ultra":
			res = "4k"
		case "1k", "standard":
			res = "1k"
		}
		switch {
		case modelID == "nano-banana-pro":
			return "firefly-nano-banana-pro-" + res + "-" + asp
		case modelID == "nano-banana-v2":
			return "firefly-nano-banana2-" + res + "-" + asp
		case modelID == "nano-banana":
			return "firefly-nano-banana-" + res + "-" + asp
		case modelID == "gpt-image-2":
			gptAsp := asp
			if isAutoSize(size) {
				gptAsp = "1x1"
			}
			switch gptAsp {
			case "1x1", "2x3", "3x2", "3x4", "4x3", "4x5", "5x4", "9x16", "16x9", "21x9":
				// supported, keep
			default:
				gptAsp = "1x1"
			}
			gptTier := inferGPTImageTier(quality, size)
			return "firefly-gpt-image-2-" + gptTier + "-" + gptAsp
		}
	}
	if def.Type == ModelTypeVideo {
		return base
	}
	return base
}

// AutoVariantID NB 系列尝试改写到 -auto 兄弟条目；GPT IMAGE 2 不支持 AUTO，返原值。
func AutoVariantID(catalogID string) string {
	if !strings.HasPrefix(catalogID, "firefly-nano-banana") {
		return catalogID
	}
	idx := strings.LastIndex(catalogID, "-")
	if idx <= 0 {
		return catalogID
	}
	candidate := catalogID[:idx] + "-auto"
	if _, ok := Catalog[candidate]; ok {
		return candidate
	}
	return catalogID
}

func isAutoSize(size string) bool {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "auto", "自动", "自动跟随", "reference", "ref", "follow":
		return true
	}
	return false
}

func inferAspectFromSize(size string) string {
	w, h, err := parseSize(size)
	if err != nil {
		return "16x9"
	}
	ratio := inferAspectRatio(w, h)
	switch ratio {
	case "1:1":
		return "1x1"
	case "16:9":
		return "16x9"
	case "9:16":
		return "9x16"
	case "4:3":
		return "4x3"
	case "3:4":
		return "3x4"
	case "3:2":
		return "3x2"
	case "2:3":
		return "2x3"
	case "4:5":
		return "4x5"
	case "5:4":
		return "5x4"
	case "21:9":
		return "21x9"
	default:
		return "16x9"
	}
}

// inferGPTImageTier 把 quality string + size 兜底成 gpt-image v2 的 tier。
func inferGPTImageTier(quality, size string) string {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "1k", "standard", "std", "low", "fast", "quick", "preview", "hd-fast":
		return "1k"
	case "2k", "hd", "medium", "med", "hd-std", "hd-high":
		return "2k"
	case "4k", "ultra", "high", "fine", "max":
		return "4k"
	}
	if size != "" {
		if w, h, err := parseSize(size); err == nil {
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
	return "2k"
}

// ResolveVideoVariant 公开 alias / catalog ID + duration + size 反查出更精确的视频 catalog ID。
func ResolveVideoVariant(publicModel string, duration int, size string, preferredResolution ...string) string {
	base, ok := publicModelAliases[publicModel]
	if !ok {
		if _, ok := Catalog[publicModel]; !ok {
			return ""
		}
		base = publicModel
	}
	def := Catalog[base]
	if def.Type != ModelTypeVideo {
		return ""
	}

	asp := "16x9"
	if size != "" {
		asp = inferAspectFromSize(size)
	}

	prefix := ""
	switch publicModel {
	case "sora", "sora2":
		prefix = "firefly-sora2"
	case "sora2-pro":
		prefix = "firefly-sora2-pro"
	case "veo3.1":
		prefix = "firefly-veo31"
	case "veo3.1-ref":
		prefix = "firefly-veo31-ref"
	case "veo3.1-fast", "veo3.1-flash":
		prefix = "firefly-veo31-fast"
	case "veo3.1-lite":
		prefix = "firefly-veo31-lite"
	}
	if prefix == "" {
		switch {
		case strings.HasPrefix(base, "firefly-sora2-pro-"):
			prefix = "firefly-sora2-pro"
		case strings.HasPrefix(base, "firefly-sora2-"):
			prefix = "firefly-sora2"
		case strings.HasPrefix(base, "firefly-veo31-ref-"):
			prefix = "firefly-veo31-ref"
		case strings.HasPrefix(base, "firefly-veo31-fast-"):
			prefix = "firefly-veo31-fast"
		case strings.HasPrefix(base, "firefly-veo31-lite-"):
			prefix = "firefly-veo31-lite"
		case strings.HasPrefix(base, "firefly-veo31-"):
			prefix = "firefly-veo31"
		default:
			return ""
		}
	}
	if duration <= 0 {
		return ""
	}

	candidate := fmt.Sprintf("%s-%ds-%s", prefix, duration, asp)
	if _, ok := Catalog[candidate]; ok {
		return candidate
	}
	if def.VideoResolution != "" {
		candidate = fmt.Sprintf("%s-%ds-%s-%s", prefix, duration, asp, def.VideoResolution)
		if _, ok := Catalog[candidate]; ok {
			return candidate
		}
	}
	if len(preferredResolution) > 0 {
		res := strings.ToLower(strings.TrimSpace(preferredResolution[0]))
		if res == "720" {
			res = "720p"
		}
		if res == "1080" || res == "hd" || res == "fhd" {
			res = "1080p"
		}
		if res == "720p" || res == "1080p" {
			candidate = fmt.Sprintf("%s-%ds-%s-%s", prefix, duration, asp, res)
			if _, ok := Catalog[candidate]; ok {
				return candidate
			}
		}
	}
	for _, res := range []string{"1080p", "720p"} {
		candidate = fmt.Sprintf("%s-%ds-%s-%s", prefix, duration, asp, res)
		if _, ok := Catalog[candidate]; ok {
			return candidate
		}
	}
	return ""
}

// ListModels 全量 catalog（admin / debug 用）。
func ListModels() []ModelDef {
	models := make([]ModelDef, 0, len(Catalog))
	for _, m := range Catalog {
		models = append(models, m)
	}
	return models
}
