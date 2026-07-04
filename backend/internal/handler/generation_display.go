package handler

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func generationDisplayAttrs(kind, paramsJSON string, resultMeta *string, width, height *int) (string, string) {
	meta := parseDisplayJSON(resultMeta)
	params := parseDisplayJSONString(paramsJSON)
	isImage := strings.EqualFold(strings.TrimSpace(kind), "image")

	resolution := normalizeDisplayResolution(firstDisplayString(meta, "resolution", "size_tier"))
	if resolution == "" && isImage {
		resolution = normalizeDisplayResolution(firstDisplayString(meta, "quality"))
	}
	if resolution == "" {
		resolution = normalizeDisplayResolution(firstDisplayString(params, "resolution", "size_tier"))
	}
	if resolution == "" && isImage {
		resolution = normalizeDisplayResolution(firstDisplayString(params, "quality"))
	}
	if resolution == "" && isImage && width != nil && height != nil {
		resolution = inferDisplayResolution(*width, *height)
	}

	// 比例只认用户在请求里显式传入的值（params 的 ratio/aspect_ratio/size）。
	// 如果用户没传，不要从结果尺寸或上游 meta 倒推具体比例，统一显示 auto，
	// 避免出现“明明没指定却显示成 16:9/9:16”的误导。
	aspect := normalizeDisplayAspect(firstDisplayString(params, "ratio", "aspect_ratio", "size"))
	if aspect == "" {
		aspect = "auto"
	}
	return resolution, aspect
}

func parseDisplayJSON(raw *string) map[string]any {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	return parseDisplayJSONString(*raw)
}

func parseDisplayJSONString(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func firstDisplayString(m map[string]any, keys ...string) string {
	if len(m) == 0 {
		return ""
	}
	for _, key := range keys {
		if v, ok := m[key]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func normalizeDisplayResolution(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "1k", "standard", "std", "low", "draft":
		return "1K"
	case "2", "2k", "hd", "high":
		return "2K"
	case "4", "4k", "ultra":
		return "4K"
	default:
		return strings.ToUpper(strings.TrimSpace(v))
	}
}

func inferDisplayResolution(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	long := w
	if h > long {
		long = h
	}
	switch {
	case long <= 1600:
		return "1K"
	case long <= 2800:
		return "2K"
	default:
		return "4K"
	}
}

func normalizeDisplayAspect(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.Contains(v, ":") {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) != 2 {
			return ""
		}
		w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
		h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
		if errW == nil && errH == nil && w > 0 && h > 0 {
			return fmt.Sprintf("%d:%d", w, h)
		}
		return ""
	}
	lower := strings.ToLower(v)
	if strings.Contains(lower, "x") {
		parts := strings.SplitN(lower, "x", 2)
		if len(parts) != 2 {
			return ""
		}
		w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
		h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
		if errW != nil || errH != nil || w <= 0 || h <= 0 {
			return ""
		}
		if w <= 32 && h <= 32 {
			return fmt.Sprintf("%d:%d", w, h)
		}
		return inferDisplayAspect(w, h)
	}
	return ""
}

func inferDisplayAspect(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	type candidate struct {
		label string
		value float64
	}
	items := []candidate{
		{"21:9", 21.0 / 9.0},
		{"16:9", 16.0 / 9.0},
		{"3:2", 3.0 / 2.0},
		{"4:3", 4.0 / 3.0},
		{"5:4", 5.0 / 4.0},
		{"1:1", 1.0},
		{"4:5", 4.0 / 5.0},
		{"3:4", 3.0 / 4.0},
		{"2:3", 2.0 / 3.0},
		{"9:16", 9.0 / 16.0},
	}
	ratio := float64(w) / float64(h)
	best := items[0]
	bestDelta := math.Abs(math.Log(ratio / best.value))
	for _, item := range items[1:] {
		delta := math.Abs(math.Log(ratio / item.value))
		if delta < bestDelta {
			best = item
			bestDelta = delta
		}
	}
	return best.label
}
