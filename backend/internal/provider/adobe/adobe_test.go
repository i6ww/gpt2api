package adobe

import (
	"strings"
	"testing"

	"github.com/kleinai/backend/internal/provider/adobe/firefly"
)

func TestAdobeResolveInputsAcceptsRatioInSize(t *testing.T) {
	size, quality := adobeResolveInputs(map[string]any{
		"size":    "3:4",
		"quality": "4k",
	})
	got, err := firefly.Resolve("nano-banana-v2", size, quality)
	if err != nil {
		t.Fatal(err)
	}
	if got.AspectRatio != "3:4" {
		t.Fatalf("aspect=%q want 3:4 (size=%q quality=%q)", got.AspectRatio, size, quality)
	}
	if !strings.Contains(got.Model.ID, "3x4") {
		t.Fatalf("model=%q want 3x4 variant", got.Model.ID)
	}
	if got.Width >= got.Height {
		t.Fatalf("dimensions=%dx%d want portrait", got.Width, got.Height)
	}
}

func TestAdobeResolveInputsRatioOverridesSizeRatioWithResolution(t *testing.T) {
	size, quality := adobeResolveInputs(map[string]any{
		"size":       "1:1",
		"ratio":      "9:16",
		"resolution": "1K",
	})
	got, err := firefly.Resolve("nano-banana-pro", size, quality)
	if err != nil {
		t.Fatal(err)
	}
	if got.AspectRatio != "9:16" {
		t.Fatalf("aspect=%q want 9:16 (size=%q quality=%q)", got.AspectRatio, size, quality)
	}
	if got.Model.Resolution != "1k" {
		t.Fatalf("resolution=%q want 1k", got.Model.Resolution)
	}
}

func TestAspectFromDimensions(t *testing.T) {
	tests := []struct {
		w, h int
		want string
	}{
		{1024, 1024, "1:1"},
		{1536, 2048, "3:4"},
		{2048, 1536, "4:3"},
		{720, 1280, "9:16"},
	}
	for _, tc := range tests {
		if got := aspectFromDimensions(tc.w, tc.h); got != tc.want {
			t.Fatalf("aspectFromDimensions(%d, %d)=%q want %q", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestPreferExplicitGPTImageSizeMovesAutoLast(t *testing.T) {
	candidates := []firefly.ImagePayload{
		{"modelSpecificPayload": map[string]interface{}{"size": "auto"}},
		{"modelSpecificPayload": map[string]interface{}{"size": "1728x2304"}},
	}
	got := preferExplicitGPTImageSize(candidates)
	first := got[0]["modelSpecificPayload"].(map[string]interface{})["size"]
	last := got[1]["modelSpecificPayload"].(map[string]interface{})["size"]
	if first != "1728x2304" || last != "auto" {
		t.Fatalf("unexpected order: first=%v last=%v", first, last)
	}
}

func TestHasExplicitAspectInput(t *testing.T) {
	if !hasExplicitAspectInput(map[string]any{"aspect_ratio": "3:4"}) {
		t.Fatal("aspect_ratio should be explicit")
	}
	if !hasExplicitAspectInput(map[string]any{"size": "1728x2304"}) {
		t.Fatal("WxH size should be explicit")
	}
	if hasExplicitAspectInput(map[string]any{"size": "auto"}) {
		t.Fatal("auto should not be explicit")
	}
}
