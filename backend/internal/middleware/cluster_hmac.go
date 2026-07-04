package middleware

import (
	"bytes"
	"context"
	"io"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/response"
)

// 集群相关 ctx key。
const (
	CtxClusterNode   ctxKey = "kc:cluster:node"
	CtxClusterSecret ctxKey = "kc:cluster:secret"
)

// ClusterHMAC 校验 agent → 主控的请求 HMAC。
//   X-Klein-Node:   节点 id
//   X-Klein-Ts:     时间戳秒
//   X-Klein-Sig:    base64url(hmac_sha256(secret, ts + "\n" + METHOD + "\n" + path + "\n" + sha256_hex(body)))
//
// 同时会把 *model.ClusterNode 与明文 secret 放入 ctx。
func ClusterHMAC(svc *service.ClusterService) gin.HandlerFunc {
	return func(c *gin.Context) {
		nodeID := c.GetHeader("X-Klein-Node")
		ts := c.GetHeader("X-Klein-Ts")
		sig := c.GetHeader("X-Klein-Sig")

		// 读 body 并复位，让后续 ShouldBindJSON 还能用
		var body []byte
		if c.Request.Body != nil {
			b, err := io.ReadAll(c.Request.Body)
			if err != nil {
				response.Fail(c, errcode.InvalidParam.Wrap(err))
				return
			}
			body = b
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}

		// path 包含 query
		path := c.Request.URL.Path
		if c.Request.URL.RawQuery != "" {
			path = path + "?" + c.Request.URL.RawQuery
		}

		node, secret, err := svc.VerifyAgentRequest(c.Request.Context(), nodeID, ts, sig, c.Request.Method, path, body)
		if err != nil {
			response.Fail(c, errcode.Unauthorized.Wrap(err))
			return
		}
		ctx := context.WithValue(c.Request.Context(), CtxClusterNode, node)
		ctx = context.WithValue(ctx, CtxClusterSecret, secret)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// ClusterNodeFromCtx 拿当前请求的节点；handler 用。
func ClusterNodeFromCtx(c *gin.Context) *model.ClusterNode {
	v := c.Request.Context().Value(CtxClusterNode)
	if v == nil {
		return nil
	}
	if n, ok := v.(*model.ClusterNode); ok {
		return n
	}
	return nil
}
