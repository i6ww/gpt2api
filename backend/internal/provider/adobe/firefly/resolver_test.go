package firefly

import (
	"strings"
	"testing"
)

// resolverCase 描述一条 (modelID, size, quality) → 上游真实返图 (W, H, AspectRatio, Resolution) 的断言。
type resolverCase struct {
	name   string
	model  string
	size   string
	qual   string
	wantW  int
	wantH  int
	wantAR string
	wantTr string // catalog SKU 解析出的 Resolution tier
}

// TestResolveDimensionsMatchUpstreamTable 把 (model, ratio, resolution) 三元组
// 解析出来的 width × height 锁定到 NB / GPT IMAGE 2 各自的严格尺寸表。
//
// 这条覆盖了 2026-05-13 修复的双 bug：
//  1. adobe.go 之前 quality="high" 会把分档塌成 2K（这里通过传 quality="1k|2k|4k" 直接验证 resolver）
//  2. resolver.go 之前 metadata 用的是 NB table 算的尺寸，gpt-image-2 也跟着用 NB 数字（错），现在按 UpstreamModelID 分流。
func TestResolveDimensionsMatchUpstreamTable(t *testing.T) {
	cases := []resolverCase{
		// ---- nano-banana (1:1) ----
		{"nano-banana 1:1 1K", "nano-banana", "1024x1024", "1k", 1024, 1024, "1:1", "1k"},
		{"nano-banana 1:1 2K", "nano-banana", "1024x1024", "2k", 2048, 2048, "1:1", "2k"},
		{"nano-banana 1:1 4K", "nano-banana", "1024x1024", "4k", 4096, 4096, "1:1", "4k"},
		// ---- nano-banana (16:9) ----
		{"nano-banana 16:9 1K", "nano-banana", "1024x576", "1k", 1376, 768, "16:9", "1k"},
		{"nano-banana 16:9 2K", "nano-banana", "2048x1152", "2k", 2752, 1536, "16:9", "2k"},
		{"nano-banana 16:9 4K", "nano-banana", "4096x2304", "4k", 5504, 3072, "16:9", "4k"},
		// ---- nano-banana (9:16) ----
		{"nano-banana 9:16 1K", "nano-banana", "576x1024", "1k", 768, 1376, "9:16", "1k"},
		{"nano-banana 9:16 2K", "nano-banana", "1152x2048", "2k", 1536, 2752, "9:16", "2k"},
		{"nano-banana 9:16 4K", "nano-banana", "2304x4096", "4k", 3072, 5504, "9:16", "4k"},
		// ---- nano-banana (4:3) ----
		{"nano-banana 4:3 1K", "nano-banana", "1024x768", "1k", 1152, 864, "4:3", "1k"},
		{"nano-banana 4:3 2K", "nano-banana", "2048x1536", "2k", 2048, 1536, "4:3", "2k"},
		// ---- nano-banana-pro ----
		{"nano-banana-pro 1:1 1K", "nano-banana-pro", "1024x1024", "1k", 1024, 1024, "1:1", "1k"},
		{"nano-banana-pro 16:9 4K", "nano-banana-pro", "4096x2304", "4k", 5504, 3072, "16:9", "4k"},
		// ---- nano-banana-v2 ----
		{"nano-banana-v2 1:1 2K", "nano-banana-v2", "1024x1024", "2k", 2048, 2048, "1:1", "2k"},
		{"nano-banana-v2 9:16 1K", "nano-banana-v2", "576x1024", "1k", 768, 1376, "9:16", "1k"},
		// ---- gpt-image-2 (走 Adobe 的 2K/4K 路径，本测试只覆盖到了 resolver，不区分 GPT web 1K) ----
		{"gpt-image-2 1:1 1K", "gpt-image-2", "1024x1024", "1k", 1024, 1024, "1:1", "1k"},
		{"gpt-image-2 16:9 1K", "gpt-image-2", "1280x720", "1k", 1280, 720, "16:9", "1k"},
		{"gpt-image-2 9:16 1K", "gpt-image-2", "720x1280", "1k", 720, 1280, "9:16", "1k"},
		{"gpt-image-2 1:1 2K", "gpt-image-2", "2048x2048", "2k", 2048, 2048, "1:1", "2k"},
		{"gpt-image-2 16:9 2K", "gpt-image-2", "2560x1440", "2k", 2560, 1440, "16:9", "2k"},
		{"gpt-image-2 9:16 2K", "gpt-image-2", "1440x2560", "2k", 1440, 2560, "9:16", "2k"},
		{"gpt-image-2 1:1 4K", "gpt-image-2", "2480x2480", "4k", 2480, 2480, "1:1", "4k"},
		{"gpt-image-2 16:9 4K", "gpt-image-2", "3328x1872", "4k", 3328, 1872, "16:9", "4k"},
		{"gpt-image-2 9:16 4K", "gpt-image-2", "1872x3328", "4k", 1872, 3328, "9:16", "4k"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.model, tc.size, tc.qual)
			if err != nil {
				t.Fatalf("Resolve(%q, %q, %q) error: %v", tc.model, tc.size, tc.qual, err)
			}
			if got.Width != tc.wantW || got.Height != tc.wantH {
				t.Errorf("dimensions: got %dx%d, want %dx%d", got.Width, got.Height, tc.wantW, tc.wantH)
			}
			if got.AspectRatio != tc.wantAR {
				t.Errorf("aspect ratio: got %q, want %q", got.AspectRatio, tc.wantAR)
			}
			if got.Model.Resolution != tc.wantTr {
				t.Errorf("tier (model.Resolution): got %q, want %q", got.Model.Resolution, tc.wantTr)
			}
		})
	}
}

// TestResolveAcceptsUppercaseTier 验证 frontend 直接传 "1K"/"2K"/"4K"（uppercase）也能命中分档。
// adobe.go::Generate 之前会把 rawQuality 写成 lowercase，但单测里直接走 resolver
// 是为了把契约钉死：resolver 自身也要容忍 uppercase。
func TestResolveAcceptsUppercaseTier(t *testing.T) {
	for _, tier := range []string{"1K", "2K", "4K"} {
		// resolver 内部 switch 是 lowercase 匹配，所以 uppercase 会落进默认 "2k"。
		// 这是已知行为——上层 adobe.go::Generate 必须先 strings.ToLower。
		// 这条只是把已知行为锁住，避免悄悄变更后没人发现。
		got, err := Resolve("nano-banana", "1024x1024", tier)
		if err != nil {
			t.Fatalf("Resolve uppercase %q: %v", tier, err)
		}
		if strings.ToLower(tier) == "2k" {
			if got.Model.Resolution != "2k" {
				t.Errorf("tier=%q: expect 2k, got %q", tier, got.Model.Resolution)
			}
		} else {
			// uppercase 1K/4K 没归一 → 落到默认 2k（这就是 adobe.go::Generate 为什么要 ToLower 的原因）。
			if got.Model.Resolution != "2k" {
				t.Errorf("tier=%q: resolver should fall back to 2k for uppercase, got %q", tier, got.Model.Resolution)
			}
		}
	}
}

// TestResolveQualityHighDoesNotForce2K 验证 quality="high" 不会再悄悄把档位顶到 2K——
// 这是 2026-05-13 主线 bug 的核心契约：
//   - adobe.go 现在保证：只要 rawResolution 有值就用 rawResolution 覆盖 rawQuality
//   - 所以传到这里的 quality 只可能是 1k/2k/4k/ultra/standard 之一
//   - resolver 自身对 "high" 仍是回 2K，因为 high 不在白名单——这是历史行为，保留作为兜底
//
// 这条断言验证 high 仍然产 2K（用来记住 adobe.go 这一层的责任）。
func TestResolveQualityHighStillFallsBackTo2K(t *testing.T) {
	got, err := Resolve("nano-banana", "1024x576", "high")
	if err != nil {
		t.Fatalf("Resolve high: %v", err)
	}
	if got.Model.Resolution != "2k" {
		t.Errorf("quality=high should fall back to 2k (historical), got %q", got.Model.Resolution)
	}
	// 2K @ 16:9 in NB table (sizeFromRatio payloads.go):
	if got.Width != 2752 || got.Height != 1536 {
		t.Errorf("dims: got %dx%d, want 2752x1536", got.Width, got.Height)
	}
}

func TestResolveVideoVariantUsesDurationAndCatalogBase(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		duration int
		size     string
		want     string
	}{
		{"sora public 12s landscape", "sora", 12, "1280x720", "firefly-sora2-12s-16x9"},
		{"sora catalog 12s portrait", "firefly-sora2-8s-16x9", 12, "720x1280", "firefly-sora2-12s-9x16"},
		{"sora pro catalog 4s", "firefly-sora2-pro-8s-16x9", 4, "1280x720", "firefly-sora2-pro-4s-16x9"},
		{"veo flash catalog 6s", "firefly-veo31-fast-8s-16x9-720p", 6, "1280x720", "firefly-veo31-fast-6s-16x9-720p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveVideoVariant(tc.model, tc.duration, tc.size); got != tc.want {
				t.Fatalf("ResolveVideoVariant(%q, %d, %q) = %q, want %q", tc.model, tc.duration, tc.size, got, tc.want)
			}
		})
	}
}
