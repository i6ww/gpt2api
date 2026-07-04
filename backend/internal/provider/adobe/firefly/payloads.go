package firefly

import (
	"fmt"
	"strings"
	"time"
)

// SizeSpec 上游接受的 width / height 对（top-level "size"）。
type SizeSpec struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ReferenceBlob 上游 referenceBlobs[] 数组里的元素。
type ReferenceBlob struct {
	ID              string `json:"id"`
	Usage           string `json:"usage"`
	PromptReference int    `json:"promptReference,omitempty"`
}

// autoPlaceholderSize Nano Banana AUTO 模式上游 top-level "size" 期望的占位（方形 tier 占位）。
// 1K=1024², 2K=2048², 4K=4096²。
func autoPlaceholderSize(resolution string) SizeSpec {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "1k":
		return SizeSpec{1024, 1024}
	case "4k":
		return SizeSpec{4096, 4096}
	default:
		return SizeSpec{2048, 2048}
	}
}

func nanoBananaSubmitSize(params *ResolvedParams) SizeSpec {
	if params != nil && params.Model.UpstreamModelID == "gemini-flash" && params.Model.UpstreamModelVersion == "nano-banana-3" {
		return SizeSpec{1024, 1024}
	}
	if params == nil {
		return SizeSpec{1024, 1024}
	}
	return autoPlaceholderSize(params.Model.Resolution)
}

// sizeFromRatio NB 家族（nano-banana / pro / v2 + gemini alias）的精确像素表。
//
// 重要：NB 与 GPT IMAGE 2 的尺寸白名单不同。GPT IMAGE 2 走 sizeForGPTImage 单独路径。
// 把 GPT 的尺寸代入这里（如 2K 9:16 = 1440x2560）会让 NB 模型 422。每个 tier
// 严格守恒为 ~(N*1024)² 面积（1MP / 4MP / 16MP），那也是 Adobe 真实计费的面积。
func sizeFromRatio(ratio, resolution string) SizeSpec {
	type dims = SizeSpec
	tables := map[string]map[string]dims{
		"1K": {
			// 2026-05-13 校准：上游 NB 1K @ 16:9 实际返 1376x768（= 2K 2752x1536 的一半），
			// 不是历史 1360x768；以此为基准把 9:16 / 2:3 / 3:2 也校到 2K 表的一半（multiple of 16）。
			"1:1":  {1024, 1024},
			"2:3":  {848, 1264},
			"3:2":  {1264, 848},
			"3:4":  {864, 1152},
			"4:3":  {1152, 864},
			"4:5":  {928, 1152},
			"5:4":  {1152, 928},
			"9:16": {768, 1376},
			"16:9": {1376, 768},
			"21:9": {1584, 672},
			"1:4":  {512, 2048},
			"4:1":  {2048, 512},
			"1:8":  {368, 2944},
			"8:1":  {2944, 368},
		},
		"2K": {
			"1:1":  {2048, 2048},
			"2:3":  {1696, 2528},
			"3:2":  {2528, 1696},
			"3:4":  {1536, 2048},
			"4:3":  {2048, 1536},
			"4:5":  {1856, 2304},
			"5:4":  {2304, 1856},
			"9:16": {1536, 2752},
			"16:9": {2752, 1536},
			"21:9": {3168, 1344},
			"1:4":  {1024, 4096},
			"4:1":  {4096, 1024},
			"1:8":  {736, 5888},
			"8:1":  {5888, 736},
		},
		"4K": {
			"1:1":  {4096, 4096},
			"2:3":  {3392, 5056},
			"3:2":  {5056, 3392},
			"3:4":  {3584, 4784},
			"4:3":  {4784, 3584},
			"4:5":  {3712, 4608},
			"5:4":  {4608, 3712},
			"9:16": {3072, 5504},
			"16:9": {5504, 3072},
			"21:9": {6336, 2688},
			"1:4":  {2048, 8192},
			"4:1":  {8192, 2048},
			"1:8":  {1472, 11776},
			"8:1":  {11776, 1472},
		},
	}
	level := "2K"
	switch resolution {
	case "1k":
		level = "1K"
	case "4k":
		level = "4K"
	}
	if t, ok := tables[level]; ok {
		if s, ok := t[ratio]; ok {
			return s
		}
	}
	return tables["2K"]["16:9"]
}

// videoSize 视频 modelSpecificPayload 期望的像素。
//
//	720p:  1280x720 / 720x1280
//	1080p: 1920x1080 / 1080x1920
func videoSize(aspectRatio, resolution string) SizeSpec {
	res := resolution
	if res == "" {
		res = "720p"
	}
	if res == "1080p" {
		if aspectRatio == "16:9" {
			return SizeSpec{1920, 1080}
		}
		return SizeSpec{1080, 1920}
	}
	if aspectRatio == "16:9" {
		return SizeSpec{1280, 720}
	}
	return SizeSpec{720, 1280}
}

// VideoSize returns the exact canvas required by video models for reference images.
func VideoSize(aspectRatio, resolution string) SizeSpec {
	return videoSize(aspectRatio, resolution)
}

// ImagePayload 上游 image / video generate-async 请求体。
type ImagePayload map[string]interface{}

// Adobe Firefly 上游 prompt submit 实测（2026-06-15）：
//   - gpt-image-2: 1024 / 2000 / 4000 / 6000 均 HTTP 200
//   - nano-banana: 4000 HTTP 200
//
// 先取跨模型已验证的 4000，避免过长提示词在 NB 家族引入新的 422 风险。
const maxUpstreamPromptRunes = 4000

func clampPromptForUpstream(prompt string) string {
	return firstRunes(prompt, maxUpstreamPromptRunes)
}

// SizeForGPTImage gpt-image v2 精确像素表（10 比例 × 3 tier）。
// 与 Nano Banana 不同——上游强制 W%16==0 && H%16==0，否则 400。
func SizeForGPTImage(ratio, tier string) SizeSpec {
	t := strings.ToLower(strings.TrimSpace(tier))
	switch t {
	case "hd-fast", "fast", "quick", "preview", "standard", "std", "low":
		t = "1k"
	case "hd", "hd-std", "hd-high":
		t = "2k"
	case "ultra":
		t = "4k"
	}
	if t == "" {
		t = "2k"
	}
	type dims = SizeSpec
	table := map[string]map[string]dims{
		"1k": {
			"1:1":  {1024, 1024},
			"3:2":  {1536, 1024},
			"2:3":  {1024, 1536},
			"4:3":  {1152, 864},
			"3:4":  {864, 1152},
			"5:4":  {1120, 896},
			"4:5":  {896, 1120},
			"16:9": {1280, 720},
			"9:16": {720, 1280},
			"21:9": {1456, 624},
		},
		"2k": {
			"1:1":  {2048, 2048},
			"3:2":  {2496, 1664},
			"2:3":  {1664, 2496},
			"4:3":  {2304, 1728},
			"3:4":  {1728, 2304},
			"5:4":  {2240, 1792},
			"4:5":  {1792, 2240},
			"16:9": {2560, 1440},
			"9:16": {1440, 2560},
			"21:9": {3024, 1296},
		},
		"4k": {
			"1:1":  {2480, 2480},
			"3:2":  {3056, 2032},
			"2:3":  {2032, 3056},
			"4:3":  {2880, 2160},
			"3:4":  {2160, 2880},
			"5:4":  {2784, 2224},
			"4:5":  {2224, 2784},
			"16:9": {3328, 1872},
			"9:16": {1872, 3328},
			"21:9": {3808, 1632},
		},
	}
	if m, ok := table[t]; ok {
		if d, ok := m[ratio]; ok {
			return d
		}
	}
	return SizeSpec{1024, 1024}
}

func sizeForGPTImage(ratio, tier string) SizeSpec { return SizeForGPTImage(ratio, tier) }

// IsGPTImageCatalogSize 判断 (w,h) 是否命中 gpt-image 白名单像素表（10 比例 × 3 档）。
// 命中说明该尺寸本就是旧逻辑能产出的标准尺寸，自定义透传必须跳过以保持原行为。
func IsGPTImageCatalogSize(w, h int) bool {
	for _, ratio := range []string{"1:1", "3:2", "2:3", "4:3", "3:4", "5:4", "4:5", "16:9", "9:16", "21:9"} {
		for _, tier := range []string{"1k", "2k", "4k"} {
			s := SizeForGPTImage(ratio, tier)
			if s.Width == w && s.Height == h {
				return true
			}
		}
	}
	return false
}

// ParseSizeWH 导出 parseSize：从 "WxH" 解析像素宽高。
func ParseSizeWH(size string) (int, int, error) { return parseSize(size) }

// gptImageDetailLevel 把客户端显式提交的画质级别映射成上游 detailLevel：
//
//	low → 1，high → 3，original → 5；
//	未提交 / 其它值（medium / auto / 1k / 2k / 4k …）→ 3（默认中等）。
//
// 兼容 OpenAI 的 quality(low/medium/high/auto) 与 detail(low/high/original/auto)。
// 只影响请求 Adobe 时的画质，与后台 billing 价格无关。
func gptImageDetailLevel(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low":
		return 1
	case "original":
		return 5
	default:
		return 3
	}
}

// buildGPTImagePayloadCandidates gpt-image v2 专用 payload 构造。
//
// 与通用 firefly/nano-banana 形态不同：
//   - 不发 groundSearch
//   - modelSpecificPayload 装 "WxH" 字符串而不是 SizeSpec
//   - referenceBlobs[].usage="subject"
//   - generationSettings.detailLevel 控制档位
func buildGPTImagePayloadCandidates(params *ResolvedParams, prompt string, referenceImageIDs []string) []ImagePayload {
	prompt = clampPromptForUpstream(prompt)
	size := sizeForGPTImage(params.AspectRatio, params.Model.Resolution)
	// 自定义尺寸透传：用户给了非白名单的字面像素 WxH 时，原样发给上游。
	if params.ExplicitWidth > 0 && params.ExplicitHeight > 0 {
		size = SizeSpec{Width: params.ExplicitWidth, Height: params.ExplicitHeight}
	}
	seed := int(time.Now().UnixMilli() % 999999)
	detailLevel := gptImageDetailLevel(params.DetailLevelHint)

	sizeStr := fmt.Sprintf("%dx%d", size.Width, size.Height)

	base := ImagePayload{
		"modelId":      "gpt-image",
		"modelVersion": "2",
		"n":            1,
		"prompt":       prompt,
		"seeds":        []int{seed},
		"output":       map[string]interface{}{"storeInputs": true},
		"generationMetadata": map[string]string{
			"module":    "text2image",
			"submodule": "ff-image-generate",
		},
		"modelSpecificPayload": map[string]interface{}{
			"size": sizeStr,
		},
		"generationSettings": map[string]interface{}{
			"detailLevel": detailLevel,
		},
	}

	if len(referenceImageIDs) == 0 {
		c1 := copyPayload(base)
		c1["referenceBlobs"] = []ReferenceBlob{}

		c2 := copyPayload(base)
		delete(c2, "modelSpecificPayload")
		c2["size"] = size
		c2["referenceBlobs"] = []ReferenceBlob{}

		c3 := copyPayload(c2)
		delete(c3, "referenceBlobs")

		return []ImagePayload{c1, c2, c3}
	}

	// gpt-image v2 i2i 仍然 module="text2image"，只是带 referenceBlobs[].usage="subject"。
	c1 := copyPayload(base)
	c1["modelSpecificPayload"] = map[string]interface{}{"size": "auto"}
	c1["generationSettings"] = map[string]interface{}{"detailLevel": detailLevel}
	blobs1 := make([]ReferenceBlob, 0, len(referenceImageIDs))
	for _, id := range referenceImageIDs {
		blobs1 = append(blobs1, ReferenceBlob{ID: id, Usage: "subject"})
	}
	c1["referenceBlobs"] = blobs1

	c2 := copyPayload(base)
	blobs2 := make([]ReferenceBlob, 0, len(referenceImageIDs))
	for _, id := range referenceImageIDs {
		blobs2 = append(blobs2, ReferenceBlob{ID: id, Usage: "subject"})
	}
	c2["referenceBlobs"] = blobs2

	return []ImagePayload{c1, c2}
}

// buildAutoNanoBananaPayload NB AUTO 模式（上游让输出比例跟随首张参考图）。
//
// 经验值（autoprobe 2026-04-26）：
//   - 不发 aspectRatio / imageSize
//   - 顶层 size 是方形 tier 占位（1024² / 2048² / 4096²）
//   - module="text2image"（不是 image2image，Adobe 合流了）
//   - referenceBlobs[].usage="general"（不是 GPT 用的 subject）
func buildAutoNanoBananaPayload(params *ResolvedParams, prompt string, referenceImageIDs []string) []ImagePayload {
	prompt = clampPromptForUpstream(prompt)
	placeholder := nanoBananaSubmitSize(params)
	seed := int(time.Now().UnixMilli() % 999999)

	base := ImagePayload{
		"modelId":      params.Model.UpstreamModelID,
		"modelVersion": params.Model.UpstreamModelVersion,
		"n":            1,
		"prompt":       prompt,
		"size":         placeholder,
		"seeds":        []int{seed},
		"groundSearch": false,
		"output":       map[string]interface{}{"storeInputs": true},
		"generationMetadata": map[string]string{
			"module":    "text2image",
			"submodule": "ff-image-generate",
		},
		"modelSpecificPayload": map[string]interface{}{
			"parameters": map[string]interface{}{"addWatermark": false},
		},
	}

	if len(referenceImageIDs) == 0 {
		base["referenceBlobs"] = []ReferenceBlob{}
		return []ImagePayload{base}
	}

	blobs := make([]ReferenceBlob, 0, len(referenceImageIDs))
	for _, id := range referenceImageIDs {
		blobs = append(blobs, ReferenceBlob{ID: id, Usage: "general"})
	}
	base["referenceBlobs"] = blobs
	return []ImagePayload{base}
}

// BuildImagePayloadCandidates 构造图片生成请求体的候选列表（按优先级）。
// 调用方挨个发请求，第一个 200 OK 的就用。
func BuildImagePayloadCandidates(params *ResolvedParams, prompt string, referenceImageIDs []string) []ImagePayload {
	prompt = clampPromptForUpstream(prompt)
	if params.Model.UpstreamModelID == "gpt-image" {
		return buildGPTImagePayloadCandidates(params, prompt, referenceImageIDs)
	}
	if params.AspectRatio == "auto" {
		return buildAutoNanoBananaPayload(params, prompt, referenceImageIDs)
	}
	size := nanoBananaSubmitSize(params)
	seed := int(time.Now().UnixMilli() % 999999)

	// 显式 aspect 模式：top-level size 是方形 tier 占位；输出比例由
	// modelSpecificPayload.aspectRatio 控制。
	// 严禁加 skipCai / contentClass / 任何多余字段，否则上游 422 "Invalid Usage"。
	base := ImagePayload{
		"modelId":      params.Model.UpstreamModelID,
		"modelVersion": params.Model.UpstreamModelVersion,
		"n":            1,
		"prompt":       prompt,
		"size":         size,
		"seeds":        []int{seed},
		"groundSearch": false,
		"output":       map[string]interface{}{"storeInputs": true},
		"generationMetadata": map[string]string{
			"module":    "text2image",
			"submodule": "ff-image-generate",
		},
		"modelSpecificPayload": map[string]interface{}{
			"aspectRatio": params.AspectRatio,
			"parameters":  map[string]interface{}{"addWatermark": false},
		},
	}

	if len(referenceImageIDs) == 0 {
		base["referenceBlobs"] = []ReferenceBlob{}
		return []ImagePayload{base}
	}

	blobs := make([]ReferenceBlob, 0, len(referenceImageIDs))
	for _, id := range referenceImageIDs {
		blobs = append(blobs, ReferenceBlob{ID: id, Usage: "general"})
	}

	// c1: 显式 aspect + text2image（多档比例命名模型，必须优先）。
	c1 := copyPayload(base)
	c1["generationMetadata"] = map[string]string{
		"module":    "text2image",
		"submodule": "ff-image-generate",
	}
	c1["referenceBlobs"] = blobs

	// c2: 官方浏览器 HAR 的形态（不发 aspectRatio + 方形 tier size）。
	c2 := copyPayload(base)
	c2["modelSpecificPayload"] = map[string]interface{}{
		"parameters": map[string]interface{}{"addWatermark": false},
	}
	c2["generationMetadata"] = map[string]string{
		"module":    "text2image",
		"submodule": "ff-image-generate",
	}
	c2["referenceBlobs"] = blobs

	// c3: legacy image2image fallback。
	c3 := copyPayload(base)
	c3["generationMetadata"] = map[string]string{
		"module":    "image2image",
		"submodule": "ff-image-generate",
	}
	c3["referenceBlobs"] = blobs

	candidates := []ImagePayload{c1, c2, c3}
	if len(blobs) > 1 {
		firstBlob := []ReferenceBlob{blobs[0]}
		// 部分 NB 账号拒绝多 ref + 显式 aspect 的 i2i，退化为只发首张参考图。
		c4 := copyPayload(c2)
		c4["referenceBlobs"] = firstBlob
		c5 := copyPayload(c3)
		c5["referenceBlobs"] = firstBlob
		candidates = append(candidates, c4, c5)
	}

	return candidates
}

// sora2DefaultNegativePrompt Firefly 浏览器默认 SORA-2 negativePrompt（HAR 2026-05-07）。
const sora2DefaultNegativePrompt = "cartoon, vector art, & bad aesthetics & poor aesthetic"

// soraModelVersionFor 区分 sora-2 / sora-2-pro（仅 modelVersion，老 `model` 字段被去掉了）。
func soraModelVersionFor(upstreamModel string) string {
	if strings.Contains(strings.ToLower(upstreamModel), "sora2-pro") ||
		strings.Contains(strings.ToLower(upstreamModel), "sora-2-pro") {
		return "sora-2-pro"
	}
	return "sora-2"
}

// BuildVideoPayload POST /v2/3p-videos/generate-async 的请求体。
//
// 2026-05-07 起上游严格校验：任何 unknown field 都返回
//
//	422 {"message":"Unsupported field(s): [...]"}
//
// 所以这里只发严格白名单字段，不要随便加。
func BuildVideoPayload(params *ResolvedParams, prompt string, referenceImageIDs []string) (ImagePayload, error) {
	prompt = clampPromptForUpstream(prompt)
	seed := int(time.Now().UnixMilli() % 999999)
	engine := params.Engine
	aspectRatio := params.AspectRatio
	duration := params.Duration
	resolution := params.VideoResolution

	if engine == "veo31-fast" || engine == "veo31-standard" || engine == "veo31-lite" {
		// modelVersion 跟 Adobe schema：
		//   veo3.1 (普通)    → "3.1-generate"      element/asset 最多 3 张参考图
		//   veo3.1-fast      → "3.1-fast-generate" frame 首尾帧最多 2 张
		//   veo3.1-lite      → "3.1-lite-generate" frame 首尾帧最多 2 张
		modelVersion := "3.1-generate"
		switch engine {
		case "veo31-fast":
			modelVersion = "3.1-fast-generate"
		case "veo31-lite":
			modelVersion = "3.1-lite-generate"
		}

		// 经裸调上游实测（普号 #2699 + Adobe2api2 模板）+ HAR 抓包确认：
		// veo3.1-* 严格白名单 11 个字段，**不接受 modelSpecificPayload**（旧版我们多发了
		// modelSpecificPayload 导致 quota_exhausted 误报）。duration / generateAudio /
		// negativePrompt 全部是顶层字段。
		//
		// generateAudio 默认 true：VEO 3.1 系列原生带音频是其卖点，运营预期就是开启。
		// 用户显式 params.GenerateAudio=false 时（前端"静音"开关）才关闭。
		// 注意：之前裸调测试用 false 是为了避免噪音变量；实际生产 audio=true 也能正常
		// 出图（普号有 firefly_credits 给视频用），只是单次消耗稍多。
		generateAudio := true
		if params.GenerateAudioSet && !params.GenerateAudio {
			generateAudio = false
		}
		payload := ImagePayload{
			"n":              1,
			"seeds":          []int{seed},
			"modelId":        "veo",
			"modelVersion":   modelVersion,
			"output":         map[string]interface{}{"storeInputs": true},
			"prompt":         prompt,
			"size":           videoSize(aspectRatio, resolution),
			"duration":       duration,
			"generateAudio":  generateAudio,
			"negativePrompt": "",
			"referenceBlobs": []interface{}{},
			"generationMetadata": map[string]string{
				"module":    "text2video",
				"submodule": "ff-video-generate",
			},
		}

		if len(referenceImageIDs) > 0 {
			blobs := []interface{}{}
			switch {
			case engine == "veo31-standard" && params.ReferenceMode == "image":
				// veo3.1-ref：3 张 asset 类型参考图
				if len(referenceImageIDs) > 3 {
					return nil, fmt.Errorf("veo31-ref supports at most 3 reference images")
				}
				for _, id := range referenceImageIDs {
					blobs = append(blobs, map[string]interface{}{"id": id, "usage": "asset"})
				}
			case engine == "veo31-fast" || engine == "veo31-lite":
				// fast / lite 只接受 frame 类型，最多 2 张（首尾帧）
				// 字段对齐 Adobe2api2：usage="frame" + promptReference=1/2 表示首/尾帧。
				if len(referenceImageIDs) > 2 {
					return nil, fmt.Errorf("%s supports at most 2 frame images", engine)
				}
				for idx, id := range referenceImageIDs {
					blobs = append(blobs, map[string]interface{}{
						"id": id, "usage": "frame", "promptReference": idx + 1,
					})
				}
			default:
				// veo3.1-standard 默认 general 模式：最多 2 张（HAR 抓包实测此 usage 能跑）
				if len(referenceImageIDs) > 2 {
					return nil, fmt.Errorf("veo31 supports at most 2 reference images")
				}
				for idx, id := range referenceImageIDs {
					blobs = append(blobs, map[string]interface{}{
						"id": id, "usage": "general", "promptReference": idx + 1,
					})
				}
			}
			payload["referenceBlobs"] = blobs
		}

		return payload, nil
	}

	// SORA-2 — payload 字段（HAR 抓包 + 实测都 OK）：
	//   modelId / modelVersion / size / seeds / referenceBlobs / prompt /
	//   negativePrompt / duration / generateAudio / generationMetadata / output
	// generateAudio 默认 true（sora-2 实测出来的 mp4 自带音轨；用户显式 false 才静音）。
	soraAudio := true
	if params.GenerateAudioSet && !params.GenerateAudio {
		soraAudio = false
	}
	payload := ImagePayload{
		"size":           videoSize(aspectRatio, resolution),
		"seeds":          []int{seed},
		"referenceBlobs": []interface{}{},
		"prompt":         prompt,
		"negativePrompt": sora2DefaultNegativePrompt,
		"duration":       duration,
		"generateAudio":  soraAudio,
		"generationMetadata": map[string]string{
			"module":    "text2video",
			"submodule": "ff-video-generate",
		},
		"modelId":      "sora",
		"modelVersion": soraModelVersionFor(params.Model.UpstreamModel),
		"output":       map[string]interface{}{"storeInputs": true},
	}

	if len(referenceImageIDs) > 0 {
		if len(referenceImageIDs) > 1 {
			return nil, fmt.Errorf("sora2 supports at most 1 reference image")
		}
		payload["referenceBlobs"] = []interface{}{
			map[string]interface{}{
				"id":              referenceImageIDs[0],
				"usage":           "general",
				"promptReference": 1,
			},
		}
	}

	return payload, nil
}

func copyPayload(src ImagePayload) ImagePayload {
	dst := make(ImagePayload, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
