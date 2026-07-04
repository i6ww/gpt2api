package adobe

import (
	"testing"

	"github.com/kleinai/backend/internal/provider/adobe/firefly"
)

// resolveLikeProd 复刻生产路径：先 adobeResolveInputs 再 firefly.Resolve。
func resolveLikeProd(t *testing.T, modelID string, params map[string]any) *firefly.ResolvedParams {
	t.Helper()
	rawSize, rawQuality := adobeResolveInputs(params)
	r, err := firefly.Resolve(modelID, rawSize, rawQuality)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return r
}

func TestExplicitGPTImagePixelSize(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		wantOK bool
		wantW  int
		wantH  int
	}{
		// 真·自定义尺寸 → 透传（对齐 16）
		{"custom 4k uhd 16:9", map[string]any{"size": "3840x2160", "quality": "4k"}, true, 3840, 2160},
		{"custom 9:16 uhd", map[string]any{"size": "2160x3840", "quality": "4k"}, true, 2160, 3840},
		{"custom wide bar 2.71:1", map[string]any{"size": "1344x496", "quality": "2k"}, true, 1344, 496},
		{"custom 1.84:1", map[string]any{"size": "1120x608", "quality": "2k"}, true, 1120, 608},
		{"custom square 2880", map[string]any{"size": "2880x2880", "quality": "4k"}, true, 2880, 2880},
		{"custom 5:4 big", map[string]any{"size": "3200x2560", "quality": "4k"}, true, 3200, 2560},
		{"custom 3:4 big", map[string]any{"size": "2448x3264", "quality": "4k"}, true, 2448, 3264},
		{"non-16-multiple aligns", map[string]any{"size": "1345x497", "quality": "2k"}, true, 1344, 496},

		// 字面像素 WxH 一律精确透传（含标准比例基准尺寸）——不再放大到 tier 白名单。
		{"std 16:9 2k -> exact", map[string]any{"size": "2048x1152", "quality": "2k"}, true, 2048, 1152},
		{"base 3:4 -> exact", map[string]any{"size": "768x1024", "quality": "2k"}, true, 768, 1024},
		{"base 1:1 1k -> exact", map[string]any{"size": "1024x1024", "quality": "1k"}, true, 1024, 1024},
		{"std 1:1 2k -> exact", map[string]any{"size": "2048x2048", "quality": "2k"}, true, 2048, 2048},
		{"std 2:3 2k -> exact", map[string]any{"size": "1664x2496", "quality": "2k"}, true, 1664, 2496},

		// 比例请求 / 非字面像素 → 不透传（保护前端比例路径）
		{"ratio param set", map[string]any{"ratio": "16:9", "size": "3840x2160"}, false, 0, 0},
		{"aspect_ratio set", map[string]any{"aspect_ratio": "9:16", "size": "2160x3840"}, false, 0, 0},
		{"size is ratio token", map[string]any{"size": "16:9", "quality": "4k"}, false, 0, 0},
		{"auto size", map[string]any{"size": "auto"}, false, 0, 0},
		{"no size", map[string]any{"quality": "4k"}, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved := resolveLikeProd(t, "gpt-image-2", tc.params)
			w, h, ok := explicitGPTImagePixelSize(tc.params, resolved)
			if ok != tc.wantOK || (ok && (w != tc.wantW || h != tc.wantH)) {
				t.Fatalf("got (w=%d,h=%d,ok=%v) want (w=%d,h=%d,ok=%v)", w, h, ok, tc.wantW, tc.wantH, tc.wantOK)
			}
		})
	}
}

func TestExplicitSizeIgnoredForNonGPTImage(t *testing.T) {
	// nano-banana 模型不应触发透传（即便给了字面像素）。
	r := resolveLikeProd(t, "nano-banana", map[string]any{"size": "3840x2160", "quality": "4k"})
	if _, _, ok := explicitGPTImagePixelSize(map[string]any{"size": "3840x2160", "quality": "4k"}, r); ok {
		t.Fatalf("expected no passthrough for nano-banana")
	}
}
