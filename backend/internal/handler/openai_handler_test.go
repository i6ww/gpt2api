package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBindImageReqMultipartFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	if err := w.WriteField("prompt", "edit background"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("model", "gpt-image-2"); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("image", "ref.png")
	if err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}
	if _, err := part.Write(png); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())

	req, err := bindImageReq(c)
	if err != nil {
		t.Fatalf("bindImageReq: %v", err)
	}
	if req.Prompt != "edit background" {
		t.Fatalf("prompt = %q", req.Prompt)
	}
	if req.Image == "" {
		t.Fatal("expected image data URL")
	}
	if !strings.HasPrefix(req.Image, "data:image/") {
		t.Fatalf("image = %q, want data:image/ prefix", req.Image[:min(len(req.Image), 32)])
	}
	if len(req.Images) != 1 || req.Images[0] != req.Image {
		t.Fatalf("images = %#v", req.Images)
	}
}

func TestBindImageReqMultipartURLStillWorks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	_ = w.WriteField("prompt", "test")
	_ = w.WriteField("image", "https://example.com/a.png")
	_ = w.Close()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())

	req, err := bindImageReq(c)
	if err != nil {
		t.Fatalf("bindImageReq: %v", err)
	}
	if req.Image != "https://example.com/a.png" {
		t.Fatalf("image = %q", req.Image)
	}
}

func TestShouldAsyncImageRequestDefaultsOpenAIImageAliasesToSync(t *testing.T) {
	if shouldAsyncImageRequest(&imageReq{Model: "nano-banana"}) {
		t.Fatal("nano-banana should default to sync for OpenAI image compatibility")
	}
	if shouldAsyncImageRequest(&imageReq{Model: "gpt-image-2"}) {
		t.Fatal("gpt-image-2 should default to sync for OpenAI image compatibility")
	}
	async := true
	if !shouldAsyncImageRequest(&imageReq{Model: "gpt-image-2", Async: &async}) {
		t.Fatal("explicit async=true should be respected")
	}
	async = false
	if shouldAsyncImageRequest(&imageReq{Model: "gpt-image-2", Async: &async}) {
		t.Fatal("explicit async=false should be respected")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
