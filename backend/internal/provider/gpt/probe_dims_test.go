package gpt

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// TestProbeImageDimsFromDataURLPNG 用一张内存里造的 31x17 PNG 验证 probeImageDimsFromDataURL
// 能正确读出真实的 width × height，不再使用 fallback。
//
// 这条契约是 2026-05-13 修：GPT web 路径之前用 hint size 直接当 metadata，
// 实际返图常和 hint 偏 200+ 像素（1024² → 1254², 1344x768 → 1672x941），
// 导致前端 / admin 显示的 width/height 与磁盘 PNG 对不上。
func TestProbeImageDimsFromDataURLPNG(t *testing.T) {
	const wantW, wantH = 31, 17
	img := image.NewRGBA(image.Rect(0, 0, wantW, wantH))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	w, h := probeImageDimsFromDataURL(dataURL, 1024, 1024)
	if w != wantW || h != wantH {
		t.Errorf("png: got %dx%d, want %dx%d", w, h, wantW, wantH)
	}
}

// TestProbeImageDimsFallback 验证非 dataURL / 截断 / 非图片格式都回 fallback。
func TestProbeImageDimsFallback(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"plain URL":      "https://example.com/foo.png",
		"truncated":      "data:image/png;base64,iVBOR", // 解出来不够 24 字节
		"not png":        "data:text/plain;base64," + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("hello world "), 10)),
		"invalid base64": "data:image/png;base64,!!!",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			w, h := probeImageDimsFromDataURL(in, 1280, 720)
			if w != 1280 || h != 720 {
				t.Errorf("%s: got %dx%d, want 1280x720 fallback", name, w, h)
			}
		})
	}
}

// TestProbeImageDimsRespectsIHDR 直接构造一个 IHDR 字节序列（手工 PNG header），
// 确认 probeImageDimsFromDataURL 用的是 big-endian 解码。
func TestProbeImageDimsRespectsIHDR(t *testing.T) {
	// PNG signature
	raw := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	// IHDR length (13)
	raw = append(raw, 0, 0, 0, 13)
	raw = append(raw, 'I', 'H', 'D', 'R')
	// width = 0x05DC = 1500, height = 0x0258 = 600（big-endian）
	wBE := make([]byte, 4)
	hBE := make([]byte, 4)
	binary.BigEndian.PutUint32(wBE, 1500)
	binary.BigEndian.PutUint32(hBE, 600)
	raw = append(raw, wBE...)
	raw = append(raw, hBE...)
	// 余下字段（bit depth、color type 等）不影响 probe，填 0 即可。
	raw = append(raw, 8, 6, 0, 0, 0)
	// IHDR CRC 也用占位 0（probe 不校验）。
	raw = append(raw, 0, 0, 0, 0)

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	w, h := probeImageDimsFromDataURL(dataURL, 0, 0)
	if w != 1500 || h != 600 {
		t.Errorf("got %dx%d, want 1500x600", w, h)
	}
}
