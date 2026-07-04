package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/service"
)

func publicOriginFromGin(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := c.GetHeader("X-Forwarded-Host")
	if host == "" {
		host = c.Request.Host
	}
	return service.RequestPublicOrigin(scheme, host)
}
