package pic2api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/pkg/outbound"
)

const (
	defaultBaseURL = "https://pic2api.com"
	defaultTimeout = 6 * time.Minute
)

type Provider struct {
	client     *http.Client
	defaultURL string
	name       string
}

func New(defaultBase string) *Provider {
	if strings.TrimSpace(defaultBase) == "" {
		defaultBase = defaultBaseURL
	}
	return &Provider{
		client: &http.Client{Timeout: defaultTimeout},
		defaultURL: strings.TrimRight(strings.TrimSpace(defaultBase), "/"),
		name: "pic2api",
	}
}

func (p *Provider) Name() string { return p.name }

type imageReq struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	N              int            `json:"n,omitempty"`
	Size           string         `json:"size,omitempty"`
	Quality        string         `json:"quality,omitempty"`
	Style          string         `json:"style,omitempty"`
	ResponseFormat string         `json:"response_format,omitempty"`
	Image          string         `json:"image,omitempty"`
	Images         []string       `json:"images,omitempty"`
	RefAssets      []string       `json:"ref_assets,omitempty"`
	ImageURLs      []string       `json:"image_urls,omitempty"`
	Operation      string         `json:"operation,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
}

type imageResp struct {
	Data  []map[string]any `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

func logUpstream(ctx context.Context, req *provider.Request, entry provider.UpstreamLogEntry) {
	if req == nil || req.UpstreamLog == nil {
		return
	}
	if entry.Provider == "" {
		entry.Provider = "pic2api"
	}
	req.UpstreamLog(ctx, entry)
}

func (p *Provider) Generate(ctx context.Context, req *provider.Request) (*provider.Result, error) {
	if req.Kind != provider.KindImage {
		return nil, fmt.Errorf("pic2api provider only supports image kind, got %s", req.Kind)
	}
	if strings.TrimSpace(req.Credential) == "" {
		return nil, fmt.Errorf("pic2api provider missing credential")
	}

	base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if base == "" {
		base = p.defaultURL
	}
	endpoint := base + "/v1/images/generations"
	fallbackEndpoint := ""
	stage := "images.generations"

	count := req.Count
	if count <= 0 {
		count = 1
	}

	body := imageReq{
		Model:          strings.TrimSpace(req.ModelCode),
		Prompt:         strings.TrimSpace(req.Prompt),
		N:              count,
		Size:           pic2apiImageSize(req.Params, "1024x1024"),
		Quality:        firstStringParam(req.Params, "quality"),
		Style:          firstStringParam(req.Params, "style"),
		ResponseFormat: firstStringParam(req.Params, "response_format"),
		Params:         cloneParams(req.Params),
	}
	if body.ResponseFormat == "" {
		body.ResponseFormat = "url"
	}
	if len(req.RefAssets) > 0 {
		body.Image = strings.TrimSpace(req.RefAssets[0])
		if len(req.RefAssets) > 1 {
			body.Images = append([]string(nil), req.RefAssets...)
			body.RefAssets = append([]string(nil), req.RefAssets...)
			body.ImageURLs = append([]string(nil), req.RefAssets...)
		}
		if strings.EqualFold(firstStringParam(req.Params, "operation"), "edit") || req.Mode == provider.ModeI2I {
			body.Operation = "edit"
			stage = "images.generations.edit"
			// Pic2API currently accepts edit-style image references on the
			// generations endpoint and may not expose /v1/images/edits.
			endpoint = base + "/v1/images/generations"
			fallbackEndpoint = base + "/v1/images/edits"
		}
	}
	if body.Params != nil && len(body.Params) == 0 {
		body.Params = nil
	}

	payload, _ := json.Marshal(body)
	start := time.Now()
	client, err := p.httpClient(req.ProxyURL)
	if err != nil {
		return nil, err
	}
	resp, raw, err := p.doImageRequest(ctx, client, endpoint, req.Credential, payload, req, stage, start)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		if fallbackEndpoint != "" && fallbackEndpoint != endpoint {
			resp, raw, err = p.doImageRequest(ctx, client, fallbackEndpoint, req.Credential, payload, req, stage+".fallback", start)
			if err != nil {
				return nil, err
			}
		}
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("pic2api %d: %s", resp.StatusCode, snippet(raw, 240))
	}

	var out imageResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("pic2api decode: %w (raw=%s)", err, snippet(raw, 240))
	}
	if out.Error != nil && strings.TrimSpace(out.Error.Message) != "" {
		return nil, fmt.Errorf("pic2api: %s", out.Error.Message)
	}

	width, height := parseSize(body.Size)
	assets := extractCompatImageAssets(raw, width, height)
	if len(assets) == 0 {
		return nil, fmt.Errorf("pic2api returned 0 image (raw=%s)", snippet(raw, 240))
	}
	return &provider.Result{
		TaskID:  req.TaskID,
		Assets:  assets,
		Latency: time.Since(start),
	}, nil
}

func (p *Provider) doImageRequest(ctx context.Context, client *http.Client, endpoint, credential string, payload []byte, req *provider.Request, stage string, start time.Time) (*http.Response, []byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credential))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "kleinai/1.0")

	resp, err := client.Do(httpReq)
	if err != nil {
		logUpstream(ctx, req, provider.UpstreamLogEntry{
			Stage:          stage,
			Method:         http.MethodPost,
			URL:            endpoint,
			RequestExcerpt: snippet(payload, 600),
			Error:          err.Error(),
		})
		return nil, nil, fmt.Errorf("pic2api http: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	logUpstream(ctx, req, provider.UpstreamLogEntry{
		Stage:           stage,
		Method:          http.MethodPost,
		URL:             endpoint,
		StatusCode:      resp.StatusCode,
		DurationMs:      time.Since(start).Milliseconds(),
		RequestExcerpt:  snippet(payload, 600),
		ResponseExcerpt: snippet(raw, 600),
	})
	return resp, raw, nil
}

func (p *Provider) httpClient(proxyURL string) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return p.client, nil
	}
	client, err := outbound.NewClient(outbound.Options{
		ProxyURL: proxyURL,
		Timeout:  defaultTimeout,
		Mode:     outbound.ModeUTLS,
		Profile:  outbound.ProfileChrome,
	})
	if err == nil {
		return client, nil
	}
	if proxyURL == "" {
		return p.client, nil
	}
	return nil, err
}

func firstStringParam(p map[string]any, keys ...string) string {
	for _, key := range keys {
		if p == nil {
			continue
		}
		if v, ok := p[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func pic2apiImageSize(params map[string]any, def string) string {
	raw := strings.TrimSpace(firstStringParam(params, "size", "image_size"))
	if looksLikePixelSize(raw) {
		return raw
	}
	ratio := strings.TrimSpace(firstStringParam(params, "ratio", "aspect_ratio"))
	tier := strings.ToUpper(strings.TrimSpace(firstStringParam(params, "resolution")))
	sizes := map[string]map[string]string{
		"1K": {
			"1:1":  "1024x1024",
			"3:2":  "1216x832",
			"2:3":  "832x1216",
			"4:3":  "1152x864",
			"3:4":  "864x1152",
			"5:4":  "1120x896",
			"4:5":  "896x1120",
			"16:9": "1344x768",
			"9:16": "768x1344",
			"21:9": "1536x640",
		},
		"2K": {
			"1:1":  "1248x1248",
			"3:2":  "1536x1024",
			"2:3":  "1024x1536",
			"4:3":  "1440x1088",
			"3:4":  "1088x1440",
			"5:4":  "1392x1120",
			"4:5":  "1120x1392",
			"16:9": "1664x928",
			"9:16": "928x1664",
			"21:9": "1904x816",
		},
		"4K": {
			"1:1":  "2480x2480",
			"3:2":  "3056x2032",
			"2:3":  "2032x3056",
			"4:3":  "2880x2160",
			"3:4":  "2160x2880",
			"5:4":  "2784x2224",
			"4:5":  "2224x2784",
			"16:9": "3312x1872",
			"9:16": "1872x3312",
			"21:9": "3808x1632",
		},
	}
	if byRatio, ok := sizes[tier]; ok {
		if size := byRatio[ratio]; size != "" {
			return size
		}
		if size := byRatio["1:1"]; size != "" {
			return size
		}
	}
	if looksLikePixelSize(raw) {
		return raw
	}
	return def
}

func looksLikePixelSize(size string) bool {
	if size == "" {
		return false
	}
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return false
	}
	var w, h int
	_, errW := fmt.Sscanf(parts[0], "%d", &w)
	_, errH := fmt.Sscanf(parts[1], "%d", &h)
	return errW == nil && errH == nil && w > 0 && h > 0
}

func cloneParams(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func parseSize(size string) (int, int) {
	if size == "" {
		return 1024, 1024
	}
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return 1024, 1024
	}
	var w, h int
	fmt.Sscanf(parts[0], "%d", &w)
	fmt.Sscanf(parts[1], "%d", &h)
	if w <= 0 {
		w = 1024
	}
	if h <= 0 {
		h = 1024
	}
	return w, h
}

func snippet(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	r := []rune(string(b))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "...(truncated)"
}

func extractCompatImageAssets(raw []byte, width, height int) []provider.Asset {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	vals := make([]string, 0, 4)
	var walk func(parentKey string, v any)
	walk = func(parentKey string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				walk(key, child)
			}
		case []any:
			for _, child := range x {
				walk(parentKey, child)
			}
		case string:
			if asset := compatImageAssetValue(parentKey, x); asset != "" {
				if _, ok := seen[asset]; ok {
					return
				}
				seen[asset] = struct{}{}
				vals = append(vals, asset)
			}
		}
	}
	walk("", payload)
	if len(vals) == 0 {
		return nil
	}
	assets := make([]provider.Asset, 0, len(vals))
	for _, v := range vals {
		mime := "image/png"
		if strings.HasPrefix(v, "data:image/") {
			if semi := strings.Index(v[len("data:"):], ";"); semi > 0 {
				mime = v[len("data:") : len("data:")+semi]
			}
		}
		assets = append(assets, provider.Asset{
			URL:    v,
			Width:  width,
			Height: height,
			Mime:   mime,
		})
	}
	return assets
}

func compatImageAssetValue(parentKey, raw string) string {
	key := strings.ToLower(strings.TrimSpace(parentKey))
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "data:image/") {
		return v
	}
	if inline := extractInlineImageURL(v); inline != "" {
		switch key {
		case "content", "text", "message", "output", "result":
			return inline
		}
	}
	if looksLikeHTTPURL(v) {
		switch key {
		case "url", "image", "image_url", "imageurl", "images", "data", "output", "result", "src", "href", "download_url", "media_url", "oss_url":
			return v
		}
	}
	if key == "b64_json" || key == "image_b64" || key == "result" {
		if looksLikeBase64Image(v) {
			return "data:image/png;base64," + v
		}
	}
	return ""
}

func looksLikeHTTPURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func looksLikeBase64Image(v string) bool {
	if len(v) < 32 || strings.ContainsAny(v, " \t\r\n") {
		return false
	}
	if _, err := base64.StdEncoding.DecodeString(v); err == nil {
		return true
	}
	if _, err := base64.RawStdEncoding.DecodeString(v); err == nil {
		return true
	}
	return false
}

var inlineImageURLRe = regexp.MustCompile(`https?://[^\s<>()\]"]+\.(?:png|jpe?g|webp|gif|bmp)(?:\?[^\s<>()\]"]*)?`)

func extractInlineImageURL(v string) string {
	if m := inlineImageURLRe.FindString(v); m != "" {
		return m
	}
	return ""
}
