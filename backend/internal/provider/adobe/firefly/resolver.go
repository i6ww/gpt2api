package firefly

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ResolvedParams 把 (modelID, size, quality) 三元组解析出来的最终参数。
// 调用上游前要先 Resolve 一次。
type ResolvedParams struct {
	Model           ModelDef
	AspectRatio     string
	Width           int
	Height          int
	Duration        int
	Engine          string
	ReferenceMode   string
	VideoResolution string
	// GenerateAudio 仅用于视频模型。VEO 3.1 系列默认 true（带音频是卖点）；
	// SORA 默认不发该字段。用户/前端通过显式参数 generate_audio=false 关闭音频；
	// GenerateAudioSet 区分"未传值"和"显式 false"，未传值时走模型默认。
	GenerateAudio    bool
	GenerateAudioSet bool
	// ExplicitWidth/ExplicitHeight 仅 gpt-image 透传自定义尺寸时设置：用户给了
	// 字面像素 WxH 且不在白名单表内时，原样发给上游 modelSpecificPayload.size。
	// 0 表示未启用（走原有 ratio+tier 白名单逻辑，行为不变）。
	ExplicitWidth  int
	ExplicitHeight int
	// DetailLevelHint 客户端显式提交的画质级别原始字符串（quality / detail 字段），
	// 仅 gpt-image-2 用：映射成上游 generationSettings.detailLevel（low→1/high→3/original→5，
	// 其它/空→3）。与 billing 价格无关。
	DetailLevelHint string
}

// Resolve 把任意外部输入归一化为 ResolvedParams。
//   - modelID 空则用 DefaultModelID
//   - size="auto" / 自动 / reference 等触发 AUTO mode（NB 系列）
//   - quality 控制图片分辨率档（1k/2k/4k）
func Resolve(modelID string, size string, quality string) (*ResolvedParams, error) {
	if modelID == "" {
		modelID = DefaultModelID
	}

	resolved := ResolvePublicAlias(modelID, size, quality)
	// 如果 modelID 直接命中 catalog 但 size=auto，需要改写到 -auto 兄弟。
	if isAutoSize(size) {
		resolved = AutoVariantID(resolved)
	}
	def, ok := Catalog[resolved]
	if !ok {
		return nil, fmt.Errorf("unknown model: %s", modelID)
	}

	params := &ResolvedParams{
		Model:           def,
		AspectRatio:     def.AspectRatio,
		Duration:        def.Duration,
		Engine:          def.Engine,
		ReferenceMode:   def.ReferenceMode,
		VideoResolution: def.VideoResolution,
	}

	// 只在 catalog 自身是 -auto 时走 AUTO 分支（保护 GPT IMAGE 2 等不支持 AUTO 的模型）。
	if def.AspectRatio == "auto" {
		params.AspectRatio = "auto"
		return params, nil
	}

	// size 仅用来推断 aspect ratio；实际输出尺寸由「catalog SKU + NB/GPT 严格表」决定。
	// 之前直接把用户 size 的字面值塞进 params.Width/Height 会让 metadata 与上游真实
	// 返图（NB: sizeFromRatio / GPT IMAGE 2: SizeForGPTImage）对不上。
	if size != "" {
		if w, h, err := parseSize(size); err == nil {
			if ratio := inferAspectRatio(w, h); ratio != "" {
				params.AspectRatio = ratio
			}
		}
	}

	if params.AspectRatio == "" {
		params.AspectRatio = "1:1"
	}

	if def.Type == ModelTypeImage {
		res := resolveResolution(def.Resolution, quality)
		w, h := imageDimensions(def, res, params.AspectRatio)
		params.Width = w
		params.Height = h
	}

	return params, nil
}

// imageDimensions 按 NB / GPT IMAGE 2 各自的严格尺寸表算输出尺寸。
//
//   - NB 家族（nano-banana / pro / v2）：sizeFromRatio（payloads.go 的 NB table）
//   - GPT IMAGE 2：SizeForGPTImage（W%16==0 的 OpenAI 严格白名单）
//
// 这是上游真正返图的尺寸，必须用它做 metadata，否则前端 / admin 显示的
// width × height 与实际 PNG 不一致。
func imageDimensions(def ModelDef, resolution, aspect string) (int, int) {
	if def.UpstreamModelID == "gpt-image" {
		s := SizeForGPTImage(aspect, resolution)
		return s.Width, s.Height
	}
	s := sizeFromRatio(aspect, resolution)
	return s.Width, s.Height
}

func parseSize(size string) (int, int, error) {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid size: %s", size)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return w, h, nil
}

// inferAspectRatio 用 log 距离最近邻匹配，避免硬阈值导致中间值划错档。
func inferAspectRatio(w, h int) string {
	if w == 0 || h == 0 {
		return ""
	}
	type entry struct {
		name  string
		value float64
	}
	candidates := []entry{
		{"8:1", 8.0},
		{"4:1", 4.0},
		{"21:9", 21.0 / 9},
		{"16:9", 16.0 / 9},
		{"3:2", 3.0 / 2},
		{"4:3", 4.0 / 3},
		{"5:4", 5.0 / 4},
		{"1:1", 1.0},
		{"4:5", 4.0 / 5},
		{"3:4", 3.0 / 4},
		{"2:3", 2.0 / 3},
		{"9:16", 9.0 / 16},
		{"1:4", 1.0 / 4},
		{"1:8", 1.0 / 8},
	}
	ratio := float64(w) / float64(h)
	best := candidates[0].name
	bestDelta := math.Abs(math.Log(ratio / candidates[0].value))
	for _, c := range candidates[1:] {
		d := math.Abs(math.Log(ratio / c.value))
		if d < bestDelta {
			best = c.name
			bestDelta = d
		}
	}
	return best
}

func resolveResolution(modelRes, quality string) string {
	if modelRes != "" {
		return modelRes
	}
	switch strings.ToLower(quality) {
	case "4k", "ultra":
		return "4k"
	case "hd", "2k":
		return "2k"
	default:
		return "1k"
	}
}

// dimensionsForResolution 仅在 caller 没显式传 size 时回退用。委托给 sizeFromRatio
// 保证全工程只有一张像素表。
func dimensionsForResolution(res, aspectRatio string) (int, int) {
	if res == "hd" {
		switch aspectRatio {
		case "1:1":
			return 1024, 1024
		case "3:2":
			return 1536, 1024
		case "2:3":
			return 1024, 1536
		default:
			return 1024, 1024
		}
	}
	s := sizeFromRatio(aspectRatio, res)
	return s.Width, s.Height
}
