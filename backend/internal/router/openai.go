// Package router OpenAI 兼容服务路由。
package router

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/bootstrap"
	"github.com/kleinai/backend/internal/handler"
	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/provider/factory"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
)

// MountOpenAI 挂载 /v1（OpenAI 兼容）。
//
// 公开路由（无需鉴权）：
//
//	GET  /v1/health
//
// 受 API Key 保护：
//
//	GET  /v1/models
//	POST /v1/chat/completions
//	POST /v1/images/generations
//	POST /v1/images/edits
//	GET  /v1/images/generations/:task_id
//	POST /v1/video/generations
//	GET  /v1/video/generations/:task_id
//	POST /v1/music/generations
//	GET  /v1/music/generations/:task_id
func MountOpenAI(r *gin.Engine, deps *bootstrap.Deps) {
	v1 := r.Group("/v1")
	v1.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	if deps.DB == nil {
		// 降级：DB 未连，受 KEY 保护的路由不挂载
		return
	}

	apiKeyRepo := repo.NewAPIKeyRepo(deps.DB)
	walletRepo := repo.NewWalletRepo(deps.DB)
	accountRepo := repo.NewAccountRepo(deps.DB)
	genRepo := repo.NewGenerationRepo(deps.DB)
	sysCfgRepo := repo.NewSystemConfigRepo(deps.DB)
	proxyRepo := repo.NewProxyRepo(deps.DB)
	clusterNodeRepo := repo.NewClusterNodeRepo(deps.DB)
	downloadLocRepo := repo.NewDownloadLocatorRepo(deps.DB)
	accountLeaseRepo := repo.NewAccountLeaseRepo(deps.DB)

	keySvc := service.NewAPIKeyService(apiKeyRepo)
	billingSvc := service.NewBillingService(deps.DB, walletRepo)
	sysCfgSvc := service.NewSystemConfigService(sysCfgRepo)
	proxySvc := service.NewProxyService(proxyRepo, deps.AES)
	upstreamChannelRepo := repo.NewUpstreamChannelRepo(deps.DB)
	taskCostLogRepo := repo.NewTaskCostLogRepo(deps.DB)
	upstreamChannelSvc := service.NewUpstreamChannelService(upstreamChannelRepo, deps.AES)
	costRecorder := service.NewCostRecorder(upstreamChannelSvc, sysCfgSvc, taskCostLogRepo)
	pool := service.NewAccountPool(accountRepo, 30*time.Second)
	pool.SetLeaseRepo(accountLeaseRepo)
	providers := factory.Build()
	clusterSvc := service.NewClusterService(clusterNodeRepo, downloadLocRepo, sysCfgSvc, deps.AES)
	if len(deps.ClusterBootstrap) > 0 {
		clusterSvc.SetBootstrapSecret(deps.ClusterBootstrap)
	}
	genSvc := service.NewGenerationService(deps.DB, genRepo, pool, billingSvc, providers, service.ConfigPriceFn(sysCfgSvc), deps.AES, proxySvc, sysCfgSvc)
	genSvc.SetCostRecorder(costRecorder)
	genSvc.SetClusterService(clusterSvc)
	genSvc.SetWebhookService(service.NewWebhookService(deps.Cfg, deps.Redis, sysCfgSvc))
	chatSvc := service.NewChatService(deps.DB, genRepo, pool, billingSvc, sysCfgSvc, deps.AES, proxySvc)
	chatSvc.SetCostRecorder(costRecorder)
	openaiH := handler.NewOpenAIHandler(genSvc, chatSvc, genRepo, sysCfgSvc)

	guard := v1.Group("/")
	guard.Use(middleware.AuthAPIKey(keySvc))
	{
		read := guard.Group("/")
		create := guard.Group("/")
		if deps.Limiter != nil {
			read.Use(middleware.RateLimitAPIKeyDynamic(deps.Limiter, "read", func(c *gin.Context) int {
				return sysCfgSvc.OpenAIPollRatePerMinute(c.Request.Context())
			}))
			create.Use(middleware.RateLimitAPIKeyNamed(deps.Limiter, "create", deps.Cfg.RateLimit.APIKeyPerMinute))
		}

		read.GET("/models", openaiH.Models)
		create.POST("/chat/completions", openaiH.ChatCompletions)
		create.POST("/images/generations", openaiH.ImageGenerations)
		read.GET("/images/generations", openaiH.GetImageTaskQuery)
		read.GET("/images/generations/:task_id", openaiH.GetImageTask)
		create.POST("/images/edits", openaiH.ImageEdits)
		create.POST("/video/generations", openaiH.VideoGenerations)
		read.GET("/video/generations/:task_id", openaiH.GetVideoTask)
		// Backward-compatible alias kept for older clients.
		create.POST("/videos/generations", openaiH.VideoGenerations)
		read.GET("/videos/generations/:task_id", openaiH.GetVideoTask)
		create.POST("/music/generations", openaiH.MusicGenerations)
		read.GET("/music/generations/:task_id", openaiH.GetMusicTask)
	}

	gemini := r.Group("/v1beta")
	gemini.Use(geminiAPIKeyCompat(), middleware.AuthAPIKey(keySvc))
	if deps.Limiter != nil {
		gemini.Use(middleware.RateLimitAPIKeyNamed(deps.Limiter, "create", deps.Cfg.RateLimit.APIKeyPerMinute))
	}
	{
		gemini.GET("/models", openaiH.GeminiModels)
		gemini.POST("/models/:model", openaiH.GeminiRouteByAction)
	}
}

func geminiAPIKeyCompat() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			key := c.GetHeader("x-goog-api-key")
			if key == "" {
				key = c.Query("key")
			}
			if key != "" {
				c.Request.Header.Set("Authorization", "Bearer "+key)
			}
		}
		c.Next()
	}
}
