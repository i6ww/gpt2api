package adobe

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestFitVideoReferenceImageCoverMatchesTargetSize(t *testing.T) {
	src := testJPEG(t, 2048, 1152)
	out, mime, changed, err := fitVideoReferenceImage(src, 1280, 720, "cover")
	if err != nil {
		t.Fatalf("fitVideoReferenceImage cover: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	assertImageSize(t, out, 1280, 720)
}

func TestFitVideoReferenceImageContainMatchesTargetSize(t *testing.T) {
	src := testJPEG(t, 1200, 1200)
	out, _, changed, err := fitVideoReferenceImage(src, 720, 1280, "contain")
	if err != nil {
		t.Fatalf("fitVideoReferenceImage contain: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	assertImageSize(t, out, 720, 1280)
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func assertImageSize(t *testing.T, data []byte, width, height int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.Width != width || cfg.Height != height {
		t.Fatalf("size = %dx%d, want %dx%d", cfg.Width, cfg.Height, width, height)
	}
}
