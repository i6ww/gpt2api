package middleware

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/pkg/errcode"
	"github.com/kleinai/backend/pkg/ratelimit"
	"github.com/kleinai/backend/pkg/response"
)

// RateLimitIP IP 限流。
func RateLimitIP(limiter *ratelimit.Limiter, ratePerMin int) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := "ratelimit:ip:" + c.ClientIP()
		applyLimit(c, limiter, key, ratePerMin)
	}
}

// RateLimitUser 已登录用户限流（依赖 AuthJWT 在前）。
func RateLimitUser(limiter *ratelimit.Limiter, ratePerMin int) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := UID(c)
		if uid == 0 {
			c.Next()
			return
		}
		key := "ratelimit:user:" + strconv.FormatUint(uid, 10)
		applyLimit(c, limiter, key, ratePerMin)
	}
}

// RateLimitAPIKey OpenAI 兼容 API Key 限流（依赖 AuthAPIKey 在前）。
func RateLimitAPIKey(limiter *ratelimit.Limiter, ratePerMin int) gin.HandlerFunc {
	return RateLimitAPIKeyNamed(limiter, "default", ratePerMin)
}

// RateLimitAPIKeyNamed OpenAI API Key 分桶限流。
// bucket 用于把创建任务和查询任务状态拆开计数，避免高频轮询挤占创建额度。
func RateLimitAPIKeyNamed(limiter *ratelimit.Limiter, bucket string, ratePerMin int) gin.HandlerFunc {
	return RateLimitAPIKeyDynamic(limiter, bucket, func(*gin.Context) int { return ratePerMin })
}

// RateLimitAPIKeyDynamic 与 RateLimitAPIKeyNamed 相同，但每个请求动态读取限流值。
func RateLimitAPIKeyDynamic(limiter *ratelimit.Limiter, bucket string, rateFn func(*gin.Context) int) gin.HandlerFunc {
	return func(c *gin.Context) {
		k := APIKeyFromCtx(c)
		if k == nil {
			c.Next()
			return
		}
		if bucket == "" {
			bucket = "default"
		}
		ratePerMin := 0
		if rateFn != nil {
			ratePerMin = rateFn(c)
		}
		key := "ratelimit:apikey:" + bucket + ":" + strconv.FormatUint(k.ID, 10)
		applyLimit(c, limiter, key, ratePerMin)
	}
}

func applyLimit(c *gin.Context, limiter *ratelimit.Limiter, key string, ratePerMin int) {
	if ratePerMin <= 0 {
		c.Next()
		return
	}
	res, err := limiter.Allow(c.Request.Context(), key, ratePerMin)
	if err != nil {
		// 限流出错不阻断业务
		c.Next()
		return
	}
	c.Header("X-RateLimit-Limit", strconv.Itoa(ratePerMin))
	c.Header("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
	c.Header("X-RateLimit-Reset", strconv.Itoa(int(res.ResetAfter.Seconds())))
	if res.Allowed <= 0 {
		c.Header("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(res.RetryAfter)))
		response.Fail(c, errcode.RateLimited)
		c.Abort()
		return
	}
	c.Next()
}
