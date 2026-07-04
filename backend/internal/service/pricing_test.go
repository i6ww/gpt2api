package service

import (
	"testing"

	"github.com/kleinai/backend/internal/provider"
)

func TestDefaultPriceFnVideoUsesVariantTable(t *testing.T) {
	// vid-i2v 进了 DefaultVideoVariantTable，10s 直接拿 map 价 3000，不走 scaled。
	got := DefaultPriceFn("vid-i2v", provider.KindVideo, map[string]any{"duration": float64(10)})
	if got != 3000 {
		t.Fatalf("expected 3000 (variant), got %d", got)
	}
	if got := DefaultPriceFn("vid-i2v", provider.KindVideo, map[string]any{"duration": float64(20)}); got != 6000 {
		t.Fatalf("expected 6000 for 20s, got %d", got)
	}
	if got := DefaultPriceFn("vid-i2v", provider.KindVideo, map[string]any{"duration": float64(30)}); got != 9000 {
		t.Fatalf("expected 9000 for 30s, got %d", got)
	}
}

func TestDefaultPriceFnVideoScaledFallback(t *testing.T) {
	// 没进 DefaultVideoVariantTable 的 video 模型走 scaled：base × dur/6。
	got := DefaultPriceFn("some-other-video-model", provider.KindVideo, map[string]any{"duration": float64(30)})
	want := int64(1500 * 30 / 6)
	if got != want {
		t.Fatalf("expected scaled fallback %d, got %d", want, got)
	}
}

func TestApplyVideoPriceFlatMode(t *testing.T) {
	got := applyVideoPrice(100, provider.KindVideo, map[string]any{"duration": float64(30)}, VideoPricingModeFlat)
	if got != 100 {
		t.Fatalf("expected flat price 100 regardless of duration, got %d", got)
	}
}

func TestApplyVideoPriceVariantMode(t *testing.T) {
	variants := map[string]int64{"6": 1500, "10": 2500, "20": 5000, "30": 7500}
	cases := []struct {
		dur  int
		want int64
	}{
		{6, 1500},
		{10, 2500},
		{20, 5000},
		{30, 7500},
	}
	for _, tc := range cases {
		got := applyVideoPriceWithVariants(9999, provider.KindVideo, map[string]any{"duration": float64(tc.dur)}, VideoPricingModeVariant, variants)
		if got != tc.want {
			t.Errorf("dur=%d: got %d, want %d", tc.dur, got, tc.want)
		}
	}
	// missing map entry: variant 模式取不到时退到 scaled 倍率
	got := applyVideoPriceWithVariants(600, provider.KindVideo, map[string]any{"duration": float64(15)}, VideoPricingModeVariant, map[string]int64{"6": 1500})
	want := int64(600 * 20 / 6) // 15s 归一到 20s 档
	if got != want {
		t.Fatalf("variant miss should fall back to scaled, got %d want %d", got, want)
	}
}

func TestApplyImagePriceTierLookup(t *testing.T) {
	variants := map[string]int64{"1k": 400, "2k": 1500, "4k": 3000}
	cases := []struct {
		model  string
		params map[string]any
		want   int64
	}{
		{"nano-banana", map[string]any{"resolution": "1K"}, 400},
		{"nano-banana", map[string]any{"resolution": "2k"}, 1500},
		{"nano-banana", map[string]any{"resolution": "4K"}, 3000},
		{"nano-banana", map[string]any{"size_tier": "2K"}, 1500},
		{"nano-banana", map[string]any{"quality": "high"}, 1500},
		{"nano-banana", map[string]any{"size": "1024x1024"}, 400},
		{"nano-banana", map[string]any{"size": "2048x1152"}, 1500},
		{"nano-banana", map[string]any{"size": "4096x2304"}, 3000},
		{"gpt-image-2", map[string]any{"size": "1024x1536"}, 400},
		{"gpt-image-2", map[string]any{"size": "auto"}, 1500},
		{"gpt-image-2", map[string]any{"quality": "high"}, 3000},
		{"gpt-image-2", map[string]any{"size": "3840x2160"}, 3000},
		{"gpt-image-2", map[string]any{"resolution": "2K", "quality": "high"}, 1500},
	}
	for _, tc := range cases {
		got := applyImagePrice(9999, provider.KindImage, tc.model, tc.params, variants)
		if got != tc.want {
			t.Errorf("model=%s params=%v: got %d, want %d", tc.model, tc.params, got, tc.want)
		}
	}
}

func TestApplyImagePriceFallsBackWhenMissing(t *testing.T) {
	if got := applyImagePrice(123, provider.KindImage, "nano-banana", map[string]any{}, nil); got != 123 {
		t.Fatalf("nil variants must return base, got %d", got)
	}
	if got := applyImagePrice(123, provider.KindImage, "nano-banana", map[string]any{"resolution": "weird"}, map[string]int64{"1k": 50}); got != 123 {
		t.Fatalf("unmatched tier must return base, got %d", got)
	}
}

func TestImageBillingParamsUsesOutputTier(t *testing.T) {
	params := map[string]any{"ratio": "16:9"}
	got := ImageBillingParams("nano-banana", params, 5504, 3072)
	if got["resolution"] != "4K" {
		t.Fatalf("expected output tier to bump resolution to 4K, got %#v", got["resolution"])
	}
}

func TestTierFromPixels(t *testing.T) {
	cases := []struct {
		w, h int
		want string
	}{
		{1024, 1024, "1k"},
		{1024, 1536, "1k"},
		{2048, 1152, "2k"},
		{3840, 2160, "4k"},
		{5504, 3072, "4k"},
	}
	for _, tc := range cases {
		if got := TierFromPixels(tc.w, tc.h); got != tc.want {
			t.Errorf("%dx%d: got %q want %q", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestNormalizeBillingVideoDurationFourBuckets(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 6}, {1, 6}, {6, 6},
		{7, 10}, {10, 10},
		{11, 20}, {20, 20},
		{21, 30}, {30, 30}, {45, 30},
	}
	for _, tc := range cases {
		if got := normalizeBillingVideoDuration(tc.in); got != tc.want {
			t.Errorf("normalizeBillingVideoDuration(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeVideoDurationForModelUsesModelBuckets(t *testing.T) {
	cases := []struct {
		model string
		in    int
		want  int
	}{
		{"sora", 3, 4},
		{"sora", 7, 8},
		{"sora", 12, 12},
		{"sora", 20, 12},
		{"firefly-sora2-8s-16x9", 12, 12},
		{"veo3.1", 5, 6},
		{"veo3.1-flash", 7, 8},
		{"grok-imagine-video", 12, 20},
		{"xai/grok-imagine-video", 12, 15},
		{"xai/grok-imagine-video", 15, 15},
		{"xai/grok-imagine-video", 30, 15},
	}
	for _, tc := range cases {
		if got := NormalizeVideoDurationForModel(tc.model, tc.in); got != tc.want {
			t.Errorf("%s %ds: got %d, want %d", tc.model, tc.in, got, tc.want)
		}
	}
}

func TestApplyVideoPriceVariantModeSoraBuckets(t *testing.T) {
	variants := map[string]int64{"4": 5000, "8": 10000, "12": 15000}
	if got := applyVideoPriceWithVariantsForModel(10000, provider.KindVideo, "sora", map[string]any{"duration": float64(12)}, VideoPricingModeVariant, variants); got != 15000 {
		t.Fatalf("expected 12s Sora variant price, got %d", got)
	}
	if got := applyVideoPriceWithVariantsForModel(10000, provider.KindVideo, "sora", map[string]any{"duration": float64(7)}, VideoPricingModeVariant, variants); got != 10000 {
		t.Fatalf("expected 7s Sora to round to 8s price, got %d", got)
	}
}

func TestDefaultPriceFnGrokImagineFreeIsZero(t *testing.T) {
	// 免额度 Imagine Pipeline 通道：服务端 creditCost=0，本站也不收费。
	// 不管 duration 怎么传都必须算 0 点，否则用户会被错误扣点。
	cases := []map[string]any{
		nil,
		{"duration": float64(6)},
		{"duration": float64(10)}, // 用户即使传了 10s，pipeline 通道实际仍出 6s，照样 0 点
		{"duration": float64(30)},
	}
	for _, p := range cases {
		got := DefaultPriceFn("grok-imagine-video-6s-free", provider.KindVideo, p)
		if got != 0 {
			t.Errorf("params=%v: expected 0 points for free pipeline, got %d", p, got)
		}
	}
}

func TestDefaultPriceFnImageUsesVariantTable(t *testing.T) {
	cases := []struct {
		model string
		tier  string
		want  int64
	}{
		{"gpt-image-2", "1K", 400},
		{"gpt-image-2", "2K", 1500},
		{"gpt-image-2", "4K", 3000},
		{"nano-banana-pro", "1K", 1500},
		{"nano-banana-pro", "2K", 3000},
		{"nano-banana-pro", "4K", 6000},
	}
	for _, tc := range cases {
		got := DefaultPriceFn(tc.model, provider.KindImage, map[string]any{"resolution": tc.tier})
		if got != tc.want {
			t.Errorf("%s @ %s: got %d, want %d", tc.model, tc.tier, got, tc.want)
		}
	}
}
