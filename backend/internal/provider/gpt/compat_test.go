package gpt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
)

func TestShouldUseNativeImage2DefaultsToOpenAI(t *testing.T) {
	if !shouldUseNativeImage2(&provider.Request{ModelCode: "gpt-image-2"}) {
		t.Fatal("expected empty base url to use native gpt-image-2 flow")
	}
}

func TestShouldUseNativeImage2DisablesNativeFlowForGatewayBase(t *testing.T) {
	req := &provider.Request{
		ModelCode: "gpt-image-2",
		BaseURL:   "https://pic2api.com",
		Account:   &model.Account{},
	}
	if shouldUseNativeImage2(req) {
		t.Fatal("expected compatibility gateway base url to use generic images endpoint")
	}
}

func TestExtractCompatImageAssetsAcceptsStringArrayData(t *testing.T) {
	raw := []byte(`{"created":1746338000,"data":["https://ossdown.com/api/v1/media/6946d.69f911f7.demo.png"]}`)
	assets := extractCompatImageAssets(raw, 1024, 1024)
	if len(assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(assets))
	}
	if assets[0].URL != "https://ossdown.com/api/v1/media/6946d.69f911f7.demo.png" {
		t.Fatalf("unexpected asset url: %s", assets[0].URL)
	}
}

func TestExtractCompatImageAssetsAcceptsRootImageField(t *testing.T) {
	raw := []byte(`{"success":true,"image":"https://ossdown.com/api/v1/media/6946d.69f911f7.demo.png"}`)
	assets := extractCompatImageAssets(raw, 768, 768)
	if len(assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(assets))
	}
	if assets[0].Width != 768 || assets[0].Height != 768 {
		t.Fatalf("unexpected asset size: %dx%d", assets[0].Width, assets[0].Height)
	}
}

func TestCompatImageAssetValueAcceptsBase64Result(t *testing.T) {
	got := compatImageAssetValue("result", strings.Repeat("QUJDRA", 8))
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("expected base64 result to be converted to data url, got %q", got)
	}
}

func TestCompatImageAssetValueAcceptsMarkdownImageContent(t *testing.T) {
	got := compatImageAssetValue("content", "![image](https://ossdown.com/api/v1/media/demo.png)")
	if got != "https://ossdown.com/api/v1/media/demo.png" {
		t.Fatalf("unexpected markdown image url: %q", got)
	}
}

func TestExtractCompatImageAssetsAcceptsChatChoicesContent(t *testing.T) {
	raw := []byte(`{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"![image](https://ossdown.com/api/v1/media/6946f.69f91441.7Yhb15_mRvP14t8d9YM19Wyoud6554MnMDn-5eY6OZA.png)","role":"assistant"}}]}`)
	assets := extractCompatImageAssets(raw, 1024, 1024)
	if len(assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(assets))
	}
	if assets[0].URL != "https://ossdown.com/api/v1/media/6946f.69f91441.7Yhb15_mRvP14t8d9YM19Wyoud6554MnMDn-5eY6OZA.png" {
		t.Fatalf("unexpected asset url: %s", assets[0].URL)
	}
}

func TestCompatImageRequestCarriesReferenceImages(t *testing.T) {
	refs := []string{
		"https://example.com/ref-1.png",
		"https://example.com/ref-2.png",
	}
	body := imgReq{
		Model:          "gemini-3.1-flash-image-preview",
		Prompt:         "edit these two images",
		N:              1,
		Size:           "1024x1024",
		ResponseFormat: "url",
		Operation:      "edit",
		Image:          refs[0],
		Images:         append([]string(nil), refs...),
		RefAssets:      append([]string(nil), refs...),
		ImageURLs:      append([]string(nil), refs...),
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal imgReq: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"operation":"edit"`, `"image":"https://example.com/ref-1.png"`, `"images":["https://example.com/ref-1.png","https://example.com/ref-2.png"]`, `"ref_assets":["https://example.com/ref-1.png","https://example.com/ref-2.png"]`, `"image_urls":["https://example.com/ref-1.png","https://example.com/ref-2.png"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected marshaled body to contain %s, got %s", want, text)
		}
	}
}
