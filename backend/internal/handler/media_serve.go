package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// 签名媒体短链的对外前缀。新链统一用短前缀 /api/v1/m/，
// 旧前缀 /api/v1/gen/media/ 继续识别以兼容历史链接。
const (
	mediaShortPrefix  = "/api/v1/m/"
	mediaLegacyPrefix = "/api/v1/gen/media/"
)

// isInternalMediaPath 判断 result.URL 是否是我们签发的站内媒体短链。
func isInternalMediaPath(p string) bool {
	p = strings.TrimSpace(p)
	return strings.HasPrefix(p, mediaShortPrefix) || strings.HasPrefix(p, mediaLegacyPrefix)
}

// mediaStorageModeFromMeta 读出 result.meta 里记录的存储模式（redirect / proxy）。
// 取不到时默认 redirect，保持与历史行为一致。
func mediaStorageModeFromMeta(meta *string) string {
	if meta == nil || strings.TrimSpace(*meta) == "" {
		return "redirect"
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(*meta), &m); err != nil {
		return "redirect"
	}
	if v, ok := m["storage_mode"].(string); ok {
		if mode := strings.ToLower(strings.TrimSpace(v)); mode != "" {
			return mode
		}
	}
	return "redirect"
}

// serveSignedMedia 是签名媒体短链的统一出口：按 meta.storage_mode 决定行为。
//   - proxy   ：服务器流式拉取上游字节并转发（真实地址完全隐藏）。
//   - 其它（redirect）：302 跳转到上游真实直链（零带宽，但 Location 暴露真实地址）。
func serveSignedMedia(c *gin.Context, meta *string, thumb bool) {
	upstream := extractUpstreamURLFromMeta(meta, thumb)
	if upstream == "" {
		response.Fail(c, errcode.ResourceMissing.WithMsg("媒体链接已失效，请重新生成"))
		return
	}
	if mediaStorageModeFromMeta(meta) == "proxy" {
		streamUpstreamAsset(c, upstream)
		return
	}
	c.Redirect(http.StatusFound, upstream)
}

// streamUpstreamAsset 把上游资源边收边吐给客户端，全程只暴露我们自己的域名。
// 透传 Range（视频拖拽必需）与关键响应头，支持 200 / 206。
func streamUpstreamAsset(c *gin.Context, target string) {
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, target, nil)
	if err != nil {
		response.Fail(c, errcode.InvalidParam.Wrap(err))
		return
	}
	if rng := c.GetHeader("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; kleinai-media-proxy/1.0)")
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		response.Fail(c, errcode.GPTUnavailable.Wrap(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		response.Fail(c, errcode.GPTUnavailable.WithMsg("资源下载失败"))
		return
	}
	copyUpstreamHeader(c, resp, "Content-Type", "application/octet-stream")
	copyUpstreamHeader(c, resp, "Content-Length", "")
	copyUpstreamHeader(c, resp, "Content-Range", "")
	copyUpstreamHeader(c, resp, "Accept-Ranges", "bytes")
	copyUpstreamHeader(c, resp, "Last-Modified", "")
	copyUpstreamHeader(c, resp, "ETag", "")
	c.Header("Cache-Control", "public, max-age=3600")
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

func copyUpstreamHeader(c *gin.Context, resp *http.Response, key, fallback string) {
	if v := resp.Header.Get(key); v != "" {
		c.Header(key, v)
		return
	}
	if fallback != "" {
		c.Header(key, fallback)
	}
}
