// Package adobe Adobe Firefly 生成 Provider。
//
// 复用 internal/provider/adobe/firefly 子包做实际 HTTP 通信，
// 自己负责：
//   - 把 provider.Request → firefly.Resolve(...) → 选 image / video / chat 分支
//   - 拉取 RefAssets（URL / data: / 本地缓存）并 UploadImage 拿到 referenceBlobs ID
//   - 把 firefly.GenerateResult → provider.Result（含 width/height/duration meta）
//   - 收集 firefly.LogHook 转发到 provider.UpstreamLogger
package adobe

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/provider/adobe/firefly"
	"github.com/kleinai/backend/pkg/outbound"
)

const (
	imageGenTimeout = 5 * time.Minute
	videoGenTimeout = 15 * time.Minute
	refDLTimeout    = 60 * time.Second
)

// directRefClient 下载参考图专用：不走任何代理，直连。
//
// 参考图要么是本站自己的对象存储（腾讯 COS / CDN，服务器直连即可），要么是用户
// 给的公网图床——都不需要、也不应该走 Adobe 账号的海外代理。历史故障：参考图下载
// 走账号代理后，被某个挂掉/过载的代理节点 CONNECT 403 / TLS handshake EOF 拖垮，
// 导致整批「图生图 / 带参考图」任务失败（错误被脱敏成"当前选项暂不可用…更换尺寸"，
// 极具误导性）。改为直连下载，仅当直连传输层失败时才回退到账号代理。
var directRefClient = &http.Client{
	Timeout:   refDLTimeout,
	Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: true},
}

// Provider 实现 provider.Provider，使用 firefly.Client 与 Adobe Firefly 通信。
type Provider struct {
	defaultClient *http.Client
}

// New 构造一个 Adobe Provider。defaultClient 仅用于不带 proxy 的常规调用；
// 带 proxy 时每次请求 outbound.NewClient 现造（与 pic2api 一致）。
func New() *Provider {
	return &Provider{
		defaultClient: &http.Client{Timeout: videoGenTimeout + 1*time.Minute},
	}
}

// Name 返回 provider 标识。
func (p *Provider) Name() string { return "adobe" }

// Generate 同步发起一次生成。
//
// Kind 路由：
//   - KindImage / KindChat 走 firefly.GenerateImage
//   - KindVideo 走 firefly.GenerateVideo
//
// 这里 KindChat 复用 Image 路径：上游 chat/completions 兼容层会把 image
// 模型当 chat 调进来，返回一张图当 message content 即可。
func (p *Provider) Generate(ctx context.Context, req *provider.Request) (*provider.Result, error) {
	if req == nil {
		return nil, errors.New("adobe: nil request")
	}
	token := strings.TrimSpace(req.Credential)
	if token == "" {
		return nil, errors.New("adobe: empty credential")
	}

	// 前端 / OpenAI 客户端传过来通常是 {ratio: "1:1", resolution: "1K", quality: "high"}，
	// 而 firefly.Resolve 只看 size + quality。这里把 ratio 翻译成 size 让 Resolve
	// 命中正确的 aspect alias，并把 frontend 的 resolution "1K/2K/4K" 映射成 quality
	// "1k/2k/4k" 让 ResolvePublicAlias 正确挑分档 SKU。
	//
	// **历史 bug 注释**：之前是 `if rawQuality == "" && rawResolution != ""` ——
	// 前端默认把 quality 写成 "high" / "draft"，导致 rawQuality 永远非空，
	// rawResolution 进不来。ResolvePublicAlias 的 switch quality 又只认
	// "1k/2k/4k/standard/ultra"，"high" 不在内 → 全部塌成 2K（与用户选的
	// 1K / 2K / 4K 完全脱钩）。这里改成 resolution 显式优先：只要
	// rawResolution 有值，rawQuality 一律按 resolution 覆盖；rawQuality 仅在
	// 没 rawResolution 时（旧 OpenAI 协议入口）才作为兜底分档信号。
	rawSize, rawQuality := adobeResolveInputs(req.Params)
	explicitAspect := hasExplicitAspectInput(req.Params)
	if rawSize == "" && (req.Kind == provider.KindImage || req.Kind == provider.KindChat) && len(req.RefAssets) > 0 {
		refClient, err := p.httpClient(req.ProxyURL, refDLTimeout)
		if err != nil {
			return nil, fmt.Errorf("adobe: build ref http client: %w", err)
		}
		if aspect := inferAspectFromFirstRef(ctx, refClient, req.RefAssets); aspect != "" {
			rawSize = sizeFromRatio(aspect, rawQuality)
		}
	}

	modelID := strings.TrimSpace(req.ModelCode)
	if req.Kind == provider.KindVideo {
		if duration := intParam(req.Params, "duration"); duration > 0 {
			if variant := firefly.ResolveVideoVariant(modelID, duration, rawSize, rawQuality); variant != "" {
				modelID = variant
			}
		}
	}
	resolved, err := firefly.Resolve(modelID, rawSize, rawQuality)
	if err != nil {
		return nil, fmt.Errorf("adobe: resolve model: %w", err)
	}

	// 客户端显式提交的画质级别（detail 优先，其次 quality）透传给 gpt-image-2 的
	// 上游 detailLevel 映射（low→1/high→3/original→5，其它/空→3）。与计费无关。
	resolved.DetailLevelHint = detailLevelHintFromParams(req.Params)

	// gpt-image 自定义尺寸透传：用户显式给了非白名单的字面像素 WxH 时，原样发给上游。
	// 严格门控以不影响既有 ratio/tier 行为，详见 explicitGPTImagePixelSize。
	if w, h, ok := explicitGPTImagePixelSize(req.Params, resolved); ok {
		resolved.ExplicitWidth = w
		resolved.ExplicitHeight = h
		resolved.Width = w
		resolved.Height = h
	}

	httpClient, err := p.httpClient(req.ProxyURL, requestTimeout(resolved.Model.Type))
	if err != nil {
		return nil, fmt.Errorf("adobe: build http client: %w", err)
	}
	client := firefly.NewClient(firefly.ClientConfig{
		HTTPClient: httpClient,
		ProxyURL:   req.ProxyURL,
		SubmitMode: req.AdobeSubmitMode,
	})

	logHook := makeLogHook(ctx, req)
	logCtx := firefly.WithLogHook(ctx, logHook)

	refOpts := adobeRefUploadOptions{}
	if req.Kind == provider.KindVideo && len(req.RefAssets) > 0 {
		target := firefly.VideoSize(resolved.AspectRatio, resolved.VideoResolution)
		refOpts = adobeRefUploadOptions{
			targetWidth:  target.Width,
			targetHeight: target.Height,
			fit:          strParam(req.Params, "reference_fit"),
			taskID:       req.TaskID,
			upstreamLog:  req.UpstreamLog,
		}
	}
	refIDs, err := p.uploadReferenceImages(logCtx, client, httpClient, token, req.RefAssets, refOpts)
	if err != nil {
		return nil, sanitizeErr(err)
	}

	prompt := strings.TrimSpace(req.Prompt)

	switch resolved.Model.Type {
	case firefly.ModelTypeVideo:
		return p.generateVideo(logCtx, client, token, resolved, prompt, refIDs, req)
	case firefly.ModelTypeImage:
		return p.generateImage(logCtx, client, token, resolved, prompt, refIDs, req, explicitAspect)
	}
	return nil, fmt.Errorf("adobe: unsupported model type %s", resolved.Model.Type)
}

// detailLevelHintFromParams 取客户端显式提交的画质级别：detail 字段优先，其次 quality。
// 返回原始字符串，具体 1/3/5 映射在 firefly.gptImageDetailLevel 内做。
func detailLevelHintFromParams(params map[string]any) string {
	if v := strings.TrimSpace(strParam(params, "detail")); v != "" {
		return v
	}
	return strings.TrimSpace(strParam(params, "quality"))
}

func adobeResolveInputs(params map[string]any) (string, string) {
	rawSize := strParam(params, "size")
	rawQuality := strParam(params, "quality")
	rawRatio := strParam(params, "ratio")
	if rawRatio == "" {
		rawRatio = strParam(params, "aspect_ratio")
	}
	rawResolution := strParam(params, "resolution")
	if rawResolution == "" {
		rawResolution = strParam(params, "size_tier")
	}
	if rawRatio == "" && isAspectRatioInput(rawSize) {
		rawRatio = rawSize
	}
	if rawRatio != "" && (rawSize == "" || isAspectRatioInput(rawSize)) {
		rawSize = sizeFromRatio(rawRatio, rawResolution)
	}
	if rawResolution != "" {
		rawQuality = strings.ToLower(rawResolution)
	}
	return rawSize, rawQuality
}

// explicitGPTImagePixelSize 判定是否启用 gpt-image 自定义尺寸透传，并返回对齐后的 (w,h)。
//
// 前置门控（任一不满足都保持旧行为）：
//   - 模型是 gpt-image；
//   - 用户没有用 ratio / aspect_ratio（保护前端比例请求路径）；
//   - 原始 size 是字面像素 "WxH"（不是 "16:9" 这类比例，也不是 "auto"）。
//
// 核心判定：区分两类「字面像素」请求，只对后者透传，确保不改历史行为——
//
//	(a) 标准比例的基准尺寸 + tier：如 768x1024 + 2k。比例是 10 种标准比例之一，
//	    且面积不超过该 (比例,tier) 的白名单尺寸。历史语义是「按这个比例出 tier 档」，
//	    会被放大到白名单尺寸（768x1024 -> 1728x2304）。**保持原样，不透传。**
//	(b) 真·自定义尺寸：如 3840x2160 / 2880x2880 / 1344x496。要么比例不在 10 种标准
//	    之内，要么面积已超过该档白名单尺寸。说明用户想要精确像素。**透传。**
//
// 透传时把宽高对齐到 16 的倍数（上游硬性要求 W%16==0 && H%16==0），并 clamp 到安全范围。
func explicitGPTImagePixelSize(params map[string]any, resolved *firefly.ResolvedParams) (int, int, bool) {
	if resolved == nil || resolved.Model.UpstreamModelID != "gpt-image" {
		return 0, 0, false
	}
	if strings.TrimSpace(strParam(params, "ratio")) != "" || strings.TrimSpace(strParam(params, "aspect_ratio")) != "" {
		return 0, 0, false
	}
	raw := strings.TrimSpace(strParam(params, "size"))
	if raw == "" || isAspectRatioInput(raw) || isAutoSizeInput(raw) {
		return 0, 0, false
	}
	w, h, err := firefly.ParseSizeWH(raw)
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}

	// 精确透传：只要 API 显式给了字面像素 WxH（非比例、非 auto），就原样发给上游、
	// 返回精确尺寸（宽高对齐到 16 的倍数、clamp 到 [256,4096]）。
	//
	// 不再因「标准比例且未超档」而回退到白名单放大——用户通过 API 直传像素即表示想要
	// 精确尺寸（如 2048x1152 就出 2048x1152，而不是放大到 16:9@2K 的 2560x1440）。
	// 前端走 ratio / aspect_ratio 参数，已在上面拦截，此路径只影响 API 直传字面尺寸。
	return alignTo16Clamp(w), alignTo16Clamp(h), true
}

// alignTo16Clamp 把像素值四舍五入到最近的 16 的倍数，并约束到 [256, 4096]。
func alignTo16Clamp(v int) int {
	v = ((v + 8) / 16) * 16
	if v < 256 {
		v = 256
	}
	if v > 4096 {
		v = 4096
	}
	return v
}

func hasExplicitAspectInput(params map[string]any) bool {
	if params == nil {
		return false
	}
	if isAspectRatioInput(strParam(params, "ratio")) || isAspectRatioInput(strParam(params, "aspect_ratio")) {
		return true
	}
	size := strings.TrimSpace(strParam(params, "size"))
	if size == "" || isAutoSizeInput(size) {
		return false
	}
	return isAspectRatioInput(size) || strings.Contains(strings.ToLower(size), "x")
}

func isAutoSizeInput(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "auto", "自动", "自动跟随", "reference", "ref", "follow":
		return true
	default:
		return false
	}
}

func preferExplicitGPTImageSize(candidates []firefly.ImagePayload) []firefly.ImagePayload {
	if len(candidates) < 2 {
		return candidates
	}
	out := make([]firefly.ImagePayload, 0, len(candidates))
	auto := make([]firefly.ImagePayload, 0, 1)
	for _, candidate := range candidates {
		if isGPTImageAutoSizePayload(candidate) {
			auto = append(auto, candidate)
			continue
		}
		out = append(out, candidate)
	}
	return append(out, auto...)
}

func isGPTImageAutoSizePayload(payload firefly.ImagePayload) bool {
	raw, ok := payload["modelSpecificPayload"].(map[string]interface{})
	if !ok {
		return false
	}
	size, _ := raw["size"].(string)
	return strings.EqualFold(strings.TrimSpace(size), "auto")
}

func (p *Provider) generateImage(ctx context.Context, c *firefly.Client, token string, resolved *firefly.ResolvedParams, prompt string, refIDs []string, req *provider.Request, explicitAspect bool) (*provider.Result, error) {
	candidates := firefly.BuildImagePayloadCandidates(resolved, prompt, refIDs)
	if explicitAspect && resolved.Model.UpstreamModelID == "gpt-image" {
		candidates = preferExplicitGPTImageSize(candidates)
	}
	if len(candidates) == 0 {
		return nil, errors.New("adobe: empty image payload")
	}

	start := time.Now()
	var lastErr error
	progressCb := p.pollProgressCallback(ctx, req)
	for i, payload := range candidates {
		out, err := c.GenerateImage(ctx, token, payload, imageGenTimeout, "", progressCb)
		if err == nil {
			width := resolved.Width
			height := resolved.Height
			if width == 0 || height == 0 {
				width, height = 1024, 1024
			}
			return &provider.Result{
				TaskID: req.TaskID,
				Assets: []provider.Asset{{
					URL:    out.PresignedURL,
					Width:  width,
					Height: height,
					Mime:   "image/png",
					Meta: map[string]any{
						"model":         resolved.Model.ID,
						"aspect_ratio":  resolved.AspectRatio,
						"resolution":    resolved.Model.Resolution,
						"reference_ids": refIDs,
					},
				}},
				Latency: time.Since(start),
			}, nil
		}
		lastErr = err
		if i == len(candidates)-1 {
			break
		}
		// 仅在上游回 "Unsupported field" 422 时切下一个候选。
		var reqErr *firefly.AdobeRequestError
		if errors.As(err, &reqErr) && strings.Contains(strings.ToLower(reqErr.Message), "unsupported field") {
			continue
		}
		break
	}
	return nil, sanitizeErr(lastErr)
}

func (p *Provider) generateVideo(ctx context.Context, c *firefly.Client, token string, resolved *firefly.ResolvedParams, prompt string, refIDs []string, req *provider.Request) (*provider.Result, error) {
	payload, err := firefly.BuildVideoPayload(resolved, prompt, refIDs)
	if err != nil {
		return nil, fmt.Errorf("adobe: build video payload: %w", err)
	}

	start := time.Now()
	out, err := c.GenerateVideo(ctx, token, payload, videoGenTimeout, "", p.pollProgressCallback(ctx, req))
	if err != nil {
		return nil, sanitizeErr(err)
	}

	width, height := resolved.Width, resolved.Height
	if width == 0 || height == 0 {
		if strings.Contains(resolved.AspectRatio, "9:16") {
			width, height = 1080, 1920
		} else {
			width, height = 1920, 1080
		}
	}

	return &provider.Result{
		TaskID: req.TaskID,
		Assets: []provider.Asset{{
			URL:        out.PresignedURL,
			Width:      width,
			Height:     height,
			DurationMs: resolved.Duration * 1000,
			Mime:       "video/mp4",
			Meta: map[string]any{
				"model":            resolved.Model.ID,
				"aspect_ratio":     resolved.AspectRatio,
				"duration_seconds": resolved.Duration,
				"resolution":       resolved.VideoResolution,
				"reference_ids":    refIDs,
			},
		}},
		Latency: time.Since(start),
	}, nil
}

type adobeRefUploadOptions struct {
	targetWidth  int
	targetHeight int
	fit          string
	taskID       string
	upstreamLog  provider.UpstreamLogger
}

func (p *Provider) uploadReferenceImages(ctx context.Context, c *firefly.Client, httpClient *http.Client, token string, refs []string, opts adobeRefUploadOptions) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(refs))
	for i, ref := range refs {
		data, mime, err := readRefImage(ctx, httpClient, ref)
		if err != nil {
			return nil, fmt.Errorf("adobe: read ref[%d]: %w", i, err)
		}
		if opts.targetWidth > 0 && opts.targetHeight > 0 {
			origW, origH := imageConfigSize(data)
			normalized, normalizedMime, changed, err := fitVideoReferenceImage(data, opts.targetWidth, opts.targetHeight, opts.fit)
			if err != nil {
				return nil, fmt.Errorf("adobe: normalize video ref[%d]: %w", i, err)
			}
			if changed {
				data = normalized
				mime = normalizedMime
			}
			if opts.upstreamLog != nil {
				opts.upstreamLog(ctx, provider.UpstreamLogEntry{
					Provider: "adobe",
					Stage:    "video.ref_normalize",
					Meta: map[string]any{
						"ref_index":     i + 1,
						"source_width":  origW,
						"source_height": origH,
						"target_width":  opts.targetWidth,
						"target_height": opts.targetHeight,
						"fit":           normalizeVideoReferenceFit(opts.fit),
						"changed":       changed,
					},
				})
			}
		}
		id, err := c.UploadImage(ctx, token, data, mime)
		if err != nil {
			return nil, fmt.Errorf("adobe: upload ref[%d]: %w", i, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func imageConfigSize(data []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func normalizeVideoReferenceFit(fit string) string {
	switch strings.ToLower(strings.TrimSpace(fit)) {
	case "contain", "stretch":
		return strings.ToLower(strings.TrimSpace(fit))
	default:
		return "cover"
	}
}

func fitVideoReferenceImage(data []byte, targetWidth, targetHeight int, fit string) ([]byte, string, bool, error) {
	if targetWidth <= 0 || targetHeight <= 0 {
		return data, "", false, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, err
	}
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW == targetWidth && srcH == targetHeight {
		return data, "", false, nil
	}

	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	switch normalizeVideoReferenceFit(fit) {
	case "contain":
		fillRGBA(dst, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		drawContainNearest(dst, src)
	case "stretch":
		drawScaledNearest(dst, src, src.Bounds())
	default:
		crop := coverCropRect(src.Bounds(), targetWidth, targetHeight)
		drawScaledNearest(dst, src, crop)
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 92}); err != nil {
		return nil, "", false, err
	}
	return out.Bytes(), "image/jpeg", true, nil
}

func coverCropRect(bounds image.Rectangle, targetWidth, targetHeight int) image.Rectangle {
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW <= 0 || srcH <= 0 || targetWidth <= 0 || targetHeight <= 0 {
		return bounds
	}
	if srcW*targetHeight > srcH*targetWidth {
		cropW := srcH * targetWidth / targetHeight
		if cropW <= 0 || cropW > srcW {
			cropW = srcW
		}
		x0 := bounds.Min.X + (srcW-cropW)/2
		return image.Rect(x0, bounds.Min.Y, x0+cropW, bounds.Max.Y)
	}
	cropH := srcW * targetHeight / targetWidth
	if cropH <= 0 || cropH > srcH {
		cropH = srcH
	}
	y0 := bounds.Min.Y + (srcH-cropH)/2
	return image.Rect(bounds.Min.X, y0, bounds.Max.X, y0+cropH)
}

func drawContainNearest(dst *image.RGBA, src image.Image) {
	if dst == nil || src == nil {
		return
	}
	db := dst.Bounds()
	sb := src.Bounds()
	dstW, dstH := db.Dx(), db.Dy()
	srcW, srcH := sb.Dx(), sb.Dy()
	if dstW <= 0 || dstH <= 0 || srcW <= 0 || srcH <= 0 {
		return
	}
	scaledW, scaledH := dstW, dstH
	if srcW*dstH > srcH*dstW {
		scaledH = srcH * dstW / srcW
	} else {
		scaledW = srcW * dstH / srcH
	}
	if scaledW <= 0 {
		scaledW = 1
	}
	if scaledH <= 0 {
		scaledH = 1
	}
	x0 := db.Min.X + (dstW-scaledW)/2
	y0 := db.Min.Y + (dstH-scaledH)/2
	tmp := image.NewRGBA(image.Rect(0, 0, scaledW, scaledH))
	drawScaledNearest(tmp, src, sb)
	for y := 0; y < scaledH; y++ {
		for x := 0; x < scaledW; x++ {
			dst.Set(x0+x, y0+y, tmp.At(x, y))
		}
	}
}

func drawScaledNearest(dst *image.RGBA, src image.Image, srcRect image.Rectangle) {
	if dst == nil || src == nil {
		return
	}
	db := dst.Bounds()
	dstW, dstH := db.Dx(), db.Dy()
	srcW, srcH := srcRect.Dx(), srcRect.Dy()
	if dstW <= 0 || dstH <= 0 || srcW <= 0 || srcH <= 0 {
		return
	}
	for y := 0; y < dstH; y++ {
		srcY := srcRect.Min.Y + y*srcH/dstH
		for x := 0; x < dstW; x++ {
			srcX := srcRect.Min.X + x*srcW/dstW
			dst.Set(db.Min.X+x, db.Min.Y+y, src.At(srcX, srcY))
		}
	}
}

func fillRGBA(dst *image.RGBA, c color.Color) {
	if dst == nil {
		return
	}
	b := dst.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x, y, c)
		}
	}
}

// readRefImage 解析单个 ref：data URL / 本地 cached / http(s)。
// 与 internal/provider/gpt/gpt.go 的 readRefImage 行为对齐。
func readRefImage(ctx context.Context, client *http.Client, ref string) ([]byte, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, "", errors.New("empty reference image")
	}
	if strings.HasPrefix(ref, "data:") {
		header, data, ok := strings.Cut(ref, ",")
		if !ok {
			return nil, "", errors.New("invalid data url image")
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, "", err
		}
		mime := strings.TrimPrefix(strings.Split(strings.TrimPrefix(header, "data:"), ";")[0], "data:")
		if mime == "" {
			mime = http.DetectContentType(raw)
		}
		return raw, mime, nil
	}
	if strings.HasPrefix(ref, "/api/v1/gen/cached/") {
		rel := strings.TrimPrefix(ref, "/api/v1/gen/cached/")
		if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
			return nil, "", errors.New("invalid cached reference image")
		}
		root := strings.TrimSpace(os.Getenv("KLEIN_STORAGE_ROOT"))
		if root == "" {
			root = "/app/storage/public"
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, "", fmt.Errorf("read cached reference image: %w", err)
		}
		if len(raw) == 0 {
			return nil, "", errors.New("empty cached reference image")
		}
		return raw, http.DetectContentType(raw), nil
	}
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, "", errors.New("reference image must be data/http url")
	}
	dlCtx, cancel := context.WithTimeout(ctx, refDLTimeout)
	defer cancel()
	newReq := func() (*http.Request, error) {
		return http.NewRequestWithContext(dlCtx, http.MethodGet, ref, nil)
	}
	httpReq, err := newReq()
	if err != nil {
		return nil, "", err
	}
	// 先直连下载（不走代理）。仅当直连「传输层」失败（DNS / 连接 / TLS）时才回退到
	// 账号代理：拿到任意 HTTP 响应（含 403/404）都不回退——那是 URL/签名问题，换代理
	// 没用，回退只会平白多花一次 refDLTimeout。
	resp, err := directRefClient.Do(httpReq)
	if err != nil && client != nil && client != directRefClient {
		if req2, rerr := newReq(); rerr == nil {
			resp, err = client.Do(req2)
		}
	}
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("reference image download %d", resp.StatusCode)
	}
	mime := resp.Header.Get("Content-Type")
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = mime[:idx]
	}
	if mime == "" || !strings.HasPrefix(mime, "image/") {
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
}

func inferAspectFromFirstRef(ctx context.Context, client *http.Client, refs []string) string {
	for _, ref := range refs {
		raw, _, err := readRefImage(ctx, client, ref)
		if err != nil || len(raw) == 0 {
			continue
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			continue
		}
		if aspect := aspectFromDimensions(cfg.Width, cfg.Height); aspect != "" {
			return aspect
		}
	}
	return ""
}

func aspectFromDimensions(w, h int) string {
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
	bestDelta := absLogRatio(ratio, best.value)
	for _, item := range items[1:] {
		delta := absLogRatio(ratio, item.value)
		if delta < bestDelta {
			best = item
			bestDelta = delta
		}
	}
	return best.label
}

func absLogRatio(a, b float64) float64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	v := a / b
	if v < 1 {
		v = 1 / v
	}
	return v - 1
}

func (p *Provider) httpClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		if timeout > 0 && p.defaultClient.Timeout < timeout {
			return &http.Client{Timeout: timeout}, nil
		}
		return p.defaultClient, nil
	}
	client, err := outbound.NewClient(outbound.Options{
		ProxyURL: proxyURL,
		Timeout:  timeout,
		Mode:     outbound.ModeUTLS,
		// 对齐 Firefly Web 的桌面 Chrome 请求头，避免头/握手指纹不一致触发 408。
		Profile: outbound.ProfileChrome,
	})
	if err == nil {
		return client, nil
	}
	return nil, err
}

func requestTimeout(t firefly.ModelType) time.Duration {
	if t == firefly.ModelTypeVideo {
		return videoGenTimeout + 1*time.Minute
	}
	return imageGenTimeout + 1*time.Minute
}

func (p *Provider) pollProgressCallback(ctx context.Context, req *provider.Request) firefly.ProgressCallback {
	if req == nil || req.OnPollProgress == nil {
		return nil
	}
	return func(update firefly.ProgressUpdate) {
		req.OnPollProgress(ctx, update.TaskProgress, update.RetryAfter)
	}
}

func makeLogHook(ctx context.Context, req *provider.Request) firefly.LogHook {
	if req == nil || req.UpstreamLog == nil {
		return nil
	}
	return func(entry firefly.LogEntry) {
		meta := map[string]any{}
		if entry.TaskStatus != "" {
			meta["task_status"] = entry.TaskStatus
		}
		if entry.Progress > 0 {
			meta["progress"] = entry.Progress
		}
		if len(entry.Headers) > 0 {
			meta["headers"] = entry.Headers
		}
		req.UpstreamLog(ctx, provider.UpstreamLogEntry{
			Provider:        "adobe",
			Stage:           entry.Phase,
			Method:          entry.Method,
			URL:             entry.URL,
			StatusCode:      entry.StatusCode,
			DurationMs:      entry.DurationMs,
			RequestExcerpt:  entry.RequestBody,
			ResponseExcerpt: entry.ResponseBody,
			Error:           entry.Error,
			Meta:            meta,
		})
	}
}

func strParam(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	v, ok := p[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func intParam(p map[string]any, key string) int {
	if p == nil {
		return 0
	}
	switch v := p[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return 0
}

// sizeFromRatio 把前端给的 ratio + resolution 翻译成 firefly 认识的 "WxH" 字符串。
// 主要用于 nano-banana 系列（GPT IMAGE 2 自己一套 aspect alias，由 firefly.ResolvePublicAlias 兜底）。
//
//   - ratio       "1:1" / "16:9" / "9:16" / "3:2" / "2:3" / "4:3" / "3:4" / "4:5" / "5:4" 等
//   - resolution  "1K" / "2K" / "4K"（不区分大小写，空值默认 2K）
//
// 返回值若 ratio 解析失败则空字符串，让 firefly.Resolve 走它自己的默认值。
func sizeFromRatio(ratio, resolution string) string {
	ratio = strings.TrimSpace(ratio)
	if ratio == "" {
		return ""
	}
	res := strings.ToUpper(strings.TrimSpace(resolution))
	if res == "" {
		res = "2K"
	}
	// 基准边长：1K = 1024、2K = 2048、4K = 4096。
	var base int
	switch res {
	case "1K", "1":
		base = 1024
	case "4K", "4":
		base = 4096
	default:
		base = 2048
	}
	parts := strings.SplitN(ratio, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	wn, errW := parseRatioPart(parts[0])
	hn, errH := parseRatioPart(parts[1])
	if errW != nil || errH != nil || wn <= 0 || hn <= 0 {
		return ""
	}
	var w, h int
	if wn >= hn {
		w = base
		h = base * hn / wn
	} else {
		h = base
		w = base * wn / hn
	}
	w = roundTo16(w)
	h = roundTo16(h)
	if w <= 0 || h <= 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", w, h)
}

func isAspectRatioInput(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || strings.Contains(strings.ToLower(v), "x") {
		return false
	}
	parts := strings.SplitN(v, ":", 2)
	if len(parts) != 2 {
		return false
	}
	wn, errW := parseRatioPart(parts[0])
	hn, errH := parseRatioPart(parts[1])
	return errW == nil && errH == nil && wn > 0 && hn > 0
}

func parseRatioPart(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty ratio part")
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

func roundTo16(n int) int {
	if n <= 0 {
		return 0
	}
	return ((n + 8) / 16) * 16
}

// sanitizeErr 用 SanitizeErrorMessage 抹掉内部实现细节再返回。
//
// 这里直接换 Error() 字符串（包一层 sanitized wrapper），让上层 errors.As 还能
// 识别 firefly.AuthError / QuotaExhaustedError 等子类型决定 cooldown / retry。
func sanitizeErr(err error) error {
	if err == nil {
		return nil
	}
	return &sanitizedError{
		inner: err,
		msg:   firefly.SanitizeErrorMessage(err.Error()),
	}
}

type sanitizedError struct {
	inner error
	msg   string
}

func (e *sanitizedError) Error() string { return e.msg }
func (e *sanitizedError) Unwrap() error { return e.inner }
