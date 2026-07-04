package grok

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

func TestNormalizeImageForGrokUploadUsesDefaultMaxEdge(t *testing.T) {
	t.Setenv("KLEIN_GROK_UPLOAD_SCALE_PERCENT", "100")
	t.Setenv("KLEIN_GROK_UPLOAD_MAX_EDGE", "1280")
	t.Setenv("KLEIN_GROK_UPLOAD_MIN_LONG_EDGE", "720")

	raw := encodeTestPNG(t, 768, 1365)
	normalized, ok := normalizeImageForGrokUpload(raw)
	if !ok {
		t.Fatal("expected normalizeImageForGrokUpload to succeed")
	}
	img, _, err := image.Decode(bytes.NewReader(normalized))
	if err != nil {
		t.Fatalf("decode normalized image: %v", err)
	}
	if got := img.Bounds().Dx(); got != 768 {
		t.Fatalf("expected width 768, got %d", got)
	}
	if got := img.Bounds().Dy(); got != 1365 {
		t.Fatalf("expected height 1365, got %d", got)
	}
	if len(normalized) < 2 || normalized[0] != 0xff || normalized[1] != 0xd8 {
		t.Fatal("expected jpeg output after normalization")
	}
}

func TestResolveGrokUploadSizeRespectsPercentBeforeMaxEdge(t *testing.T) {
	t.Setenv("KLEIN_GROK_UPLOAD_SCALE_PERCENT", "99")
	t.Setenv("KLEIN_GROK_UPLOAD_MAX_EDGE", "0")
	t.Setenv("KLEIN_GROK_UPLOAD_MIN_LONG_EDGE", "720")

	width, height := resolveGrokUploadSize(768, 1365)
	if width != 760 || height != 1351 {
		t.Fatalf("expected 760x1351, got %dx%d", width, height)
	}
}

func TestResolveGrokUploadSizeKeepsLongEdgeAtLeast720WhenPossible(t *testing.T) {
	t.Setenv("KLEIN_GROK_UPLOAD_SCALE_PERCENT", "50")
	t.Setenv("KLEIN_GROK_UPLOAD_MAX_EDGE", "0")
	t.Setenv("KLEIN_GROK_UPLOAD_MIN_LONG_EDGE", "720")

	width, height := resolveGrokUploadSize(1000, 500)
	if width != 720 || height != 360 {
		t.Fatalf("expected 720x360, got %dx%d", width, height)
	}
}

func TestCollectGrokStreamDoesNotTreatAssetIDAsVideoPostID(t *testing.T) {
	stream := `data: {"assetId":"asset-only-id","text":"done"}`
	_, _, _, postID, _, err := collectGrokStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("collectGrokStream() error = %v", err)
	}
	if postID != "" {
		t.Fatalf("expected empty video post id for asset-only payload, got %q", postID)
	}
}

func TestCollectGrokStreamKeepsExplicitVideoPostID(t *testing.T) {
	stream := `data: {"assetId":"asset-only-id","videoPostId":"post-123","thumbnailUrl":"https://example.com/thumb.jpg"}`
	_, _, _, postID, _, err := collectGrokStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("collectGrokStream() error = %v", err)
	}
	if postID != "post-123" {
		t.Fatalf("expected explicit post id, got %q", postID)
	}
}

func TestFirstVideoURLPrefersHigherQualityCandidate(t *testing.T) {
	obj := map[string]any{
		"items": []any{
			map[string]any{"videoUrl": "https://assets.example.com/generated_video_400.mp4"},
			map[string]any{"videoUrl": "https://assets.example.com/generated_video_1080.mp4"},
		},
	}
	got := firstVideoURL(obj)
	if got != "https://assets.example.com/generated_video_1080.mp4" {
		t.Fatalf("expected highest-quality url, got %q", got)
	}
}

func TestFirstVideoURLAvoidsPreviewVariant(t *testing.T) {
	obj := map[string]any{
		"videoUrl": []any{
			"https://assets.example.com/generated_video_preview.mp4",
			"https://assets.example.com/generated_video_master.mp4",
		},
	}
	got := firstVideoURL(obj)
	if got != "https://assets.example.com/generated_video_master.mp4" {
		t.Fatalf("expected master url, got %q", got)
	}
}

func TestNormalizeVideoDurationSupportsExtensionBuckets(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 6},
		{1, 6},
		{6, 6},
		{7, 10},
		{10, 10},
		{11, 20},
		{15, 20},
		{20, 20},
		{21, 30},
		{30, 30},
		{45, 30},
	}
	for _, tc := range cases {
		if got := normalizeVideoDuration(tc.in); got != tc.want {
			t.Errorf("normalizeVideoDuration(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestBuildVideoExtensionPayloadMatchesHARShape 把 HAR 抓包确认的字段拍死，
// 防止以后改 chatPayload 默认值时把 extension 链路一起改坏。
// 参考 grok.com.har 中第二条 conversations/new 的 postData.text。
func TestBuildVideoExtensionPayloadMatchesHARShape(t *testing.T) {
	c := NewWebClient("")
	p := c.buildVideoExtensionPayload(extensionPayloadArgs{
		extendPostID:   "post-A",
		originalPostID: "root-X",
		startTime:      10.031667,
		length:         10,
		aspectRatio:    "9:16",
		resolution:     "720p",
	})
	if p["temporary"] != true {
		t.Fatalf("temporary = %v, want true", p["temporary"])
	}
	if p["modelName"] != "imagine-video-gen" {
		t.Fatalf("modelName = %v, want imagine-video-gen", p["modelName"])
	}
	if p["message"] != "--mode=normal" {
		t.Fatalf("message = %v, want --mode=normal", p["message"])
	}
	if p["enableSideBySide"] != true {
		t.Fatalf("enableSideBySide = %v, want true", p["enableSideBySide"])
	}
	rm := p["responseMetadata"].(map[string]any)
	if _, ok := rm["experiments"]; !ok {
		t.Fatal("responseMetadata.experiments missing")
	}
	cfg := rm["modelConfigOverride"].(map[string]any)["modelMap"].(map[string]any)["videoGenModelConfig"].(map[string]any)
	if cfg["isVideoExtension"] != true {
		t.Fatalf("isVideoExtension = %v", cfg["isVideoExtension"])
	}
	if cfg["videoExtensionStartTime"].(float64) != 10.031667 {
		t.Fatalf("videoExtensionStartTime = %v", cfg["videoExtensionStartTime"])
	}
	if cfg["extendPostId"] != "post-A" || cfg["parentPostId"] != "post-A" {
		t.Fatalf("extendPostId/parentPostId mismatch: %+v", cfg)
	}
	if cfg["originalPostId"] != "root-X" {
		t.Fatalf("originalPostId = %v", cfg["originalPostId"])
	}
	if cfg["stitchWithExtendPostId"] != true {
		t.Fatalf("stitchWithExtendPostId = %v", cfg["stitchWithExtendPostId"])
	}
	if cfg["originalRefType"] != "ORIGINAL_REF_TYPE_VIDEO_EXTENSION" {
		t.Fatalf("originalRefType = %v", cfg["originalRefType"])
	}
	if cfg["mode"] != "normal" {
		t.Fatalf("mode = %v", cfg["mode"])
	}
	if cfg["videoLength"].(int) != 10 {
		t.Fatalf("videoLength = %v", cfg["videoLength"])
	}
	if cfg["aspectRatio"] != "9:16" {
		t.Fatalf("aspectRatio = %v", cfg["aspectRatio"])
	}
	if cfg["resolutionName"] != "720p" {
		t.Fatalf("resolutionName = %v", cfg["resolutionName"])
	}
	if cfg["isVideoEdit"] != false {
		t.Fatalf("isVideoEdit = %v", cfg["isVideoEdit"])
	}
}

// TestBuildVideoConversationPayloadMatchesHARShape 把首段视频请求的形态拍死，对齐
// 2026-06-14.har 第 46 条 conversations/new（modelName=imagine-video-gen + 极简 body）。
// 同时确保不再出现聊天时代字段（这些字段曾导致 anti-bot 403）。
func TestBuildVideoConversationPayloadMatchesHARShape(t *testing.T) {
	c := NewWebClient("")

	// t2v：纯文生视频，无参考图。
	t.Run("t2v", func(t *testing.T) {
		p := c.buildVideoConversationPayload(videoConversationArgs{
			message:      "小兔子和乌龟赛跑 --mode=custom",
			parentPostID: "video-post-1",
			videoLength:  6,
			aspectRatio:  "16:9",
			resolution:   "720p",
		})
		assertVideoEnvelope(t, p, "小兔子和乌龟赛跑 --mode=custom")
		fa := p["fileAttachments"].([]any)
		if len(fa) != 0 {
			t.Fatalf("t2v fileAttachments should be empty, got %+v", fa)
		}
		cfg := videoCfg(t, p)
		if cfg["parentPostId"] != "video-post-1" || cfg["videoLength"].(int) != 6 {
			t.Fatalf("cfg mismatch: %+v", cfg)
		}
		if cfg["aspectRatio"] != "16:9" || cfg["resolutionName"] != "720p" {
			t.Fatalf("cfg ratio/res mismatch: %+v", cfg)
		}
		if _, ok := cfg["isReferenceToVideo"]; ok {
			t.Fatalf("t2v should not set isReferenceToVideo: %+v", cfg)
		}
	})

	// i2v 单图：fileAttachments 与 parentPostId 同指图片 post。
	t.Run("i2v_single", func(t *testing.T) {
		p := c.buildVideoConversationPayload(videoConversationArgs{
			message:         "https://assets.grok.com/x/content  动画 --mode=custom",
			parentPostID:    "img-post-9",
			videoLength:     10,
			aspectRatio:     "9:16",
			resolution:      "720p",
			refs:            []uploadedVideoRef{{assetURL: "https://assets.grok.com/x/content"}},
			fileAttachments: []any{},
		})
		fa := p["fileAttachments"].([]any)
		if len(fa) != 1 || fa[0] != "img-post-9" {
			t.Fatalf("i2v single fileAttachments should be [img-post-9], got %+v", fa)
		}
		cfg := videoCfg(t, p)
		if cfg["parentPostId"] != "img-post-9" {
			t.Fatalf("cfg parentPostId mismatch: %+v", cfg)
		}
		if _, ok := cfg["imageReferences"]; ok {
			t.Fatalf("single ref should not set imageReferences: %+v", cfg)
		}
	})

	// i2v 多图：保留 isReferenceToVideo + imageReferences，沿用传入的 mentions 附件。
	t.Run("i2v_multi", func(t *testing.T) {
		p := c.buildVideoConversationPayload(videoConversationArgs{
			message:      "@a @b 动画 --mode=custom",
			parentPostID: "vid-post-2",
			videoLength:  10,
			refs: []uploadedVideoRef{
				{fileID: "a", assetURL: "https://assets.grok.com/a/content"},
				{fileID: "b", assetURL: "https://assets.grok.com/b/content"},
			},
			fileAttachments: []any{"a", "b"},
		})
		fa := p["fileAttachments"].([]any)
		if len(fa) != 2 {
			t.Fatalf("multi ref fileAttachments should keep mentions, got %+v", fa)
		}
		cfg := videoCfg(t, p)
		if cfg["isReferenceToVideo"] != true {
			t.Fatalf("multi ref should set isReferenceToVideo: %+v", cfg)
		}
		refsOut := cfg["imageReferences"].([]string)
		if len(refsOut) != 2 {
			t.Fatalf("imageReferences mismatch: %+v", refsOut)
		}
	})
}

func assertVideoEnvelope(t *testing.T, p map[string]any, wantMessage string) {
	t.Helper()
	if p["temporary"] != true {
		t.Fatalf("temporary = %v, want true", p["temporary"])
	}
	if p["modelName"] != "imagine-video-gen" {
		t.Fatalf("modelName = %v, want imagine-video-gen", p["modelName"])
	}
	if p["message"] != wantMessage {
		t.Fatalf("message = %v, want %v", p["message"], wantMessage)
	}
	if p["enableSideBySide"] != true {
		t.Fatalf("enableSideBySide = %v, want true", p["enableSideBySide"])
	}
	// 绝不能再出现聊天时代字段。
	for _, banned := range []string{
		"deviceEnvInfo", "enableImageGeneration", "enableImageStreaming",
		"imageGenerationCount", "isAsyncChat", "modelMode", "toolOverrides",
		"returnImageBytes", "enable420", "imageAttachments",
	} {
		if _, ok := p[banned]; ok {
			t.Fatalf("video payload must not contain chat-era field %q", banned)
		}
	}
	rm := p["responseMetadata"].(map[string]any)
	if _, ok := rm["experiments"]; !ok {
		t.Fatal("responseMetadata.experiments missing")
	}
	if _, ok := rm["requestModelDetails"]; ok {
		t.Fatal("responseMetadata must not contain requestModelDetails")
	}
}

func videoCfg(t *testing.T, p map[string]any) map[string]any {
	t.Helper()
	rm := p["responseMetadata"].(map[string]any)
	return rm["modelConfigOverride"].(map[string]any)["modelMap"].(map[string]any)["videoGenModelConfig"].(map[string]any)
}

func TestMediaPostGetRetryDelayExtends404Window(t *testing.T) {
	if got := mediaPostGetRetryDelay(1, 404); got != 4*time.Second {
		t.Fatalf("expected first 404 retry delay 4s, got %s", got)
	}
	if got := mediaPostGetRetryDelay(7, 404); got != 18*time.Second {
		t.Fatalf("expected capped 404 retry delay 18s, got %s", got)
	}
	if got := mediaPostGetRetryDelay(3, 500); got != 12*time.Second {
		t.Fatalf("expected generic retry delay 12s, got %s", got)
	}
}

func encodeTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.NRGBA{
				R: uint8((x * 255) / maxInt(width, 1)),
				G: uint8((y * 255) / maxInt(height, 1)),
				B: 120,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
