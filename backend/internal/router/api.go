// Package router api 服务路由组装。
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
	"github.com/kleinai/backend/pkg/jwtx"
)

// MountAPI 在 root 上挂载用户端 /api/v1 全部业务路由。
// 注意：未配置 DB 时（dev 降级）会跳过依赖 DB 的路由，仅保留 /ping。
func MountAPI(r *gin.Engine, deps *bootstrap.Deps) {
	v1 := r.Group("/api/v1")

	v1.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"pong": true})
	})

	if deps.DB == nil {
		return
	}

	userRepo := repo.NewUserRepo(deps.DB)
	apiKeyRepo := repo.NewAPIKeyRepo(deps.DB)
	walletRepo := repo.NewWalletRepo(deps.DB)
	accountRepo := repo.NewAccountRepo(deps.DB)
	genRepo := repo.NewGenerationRepo(deps.DB)
	sysCfgRepo := repo.NewSystemConfigRepo(deps.DB)
	proxyRepo := repo.NewProxyRepo(deps.DB)
	inviteRepo := repo.NewInviteRepo(deps.DB)
	clusterNodeRepo := repo.NewClusterNodeRepo(deps.DB)
	downloadLocRepo := repo.NewDownloadLocatorRepo(deps.DB)
	accountLeaseRepo := repo.NewAccountLeaseRepo(deps.DB)

	authSvc := service.NewAuthService(deps.DB, userRepo, deps.JWT)
	userSvc := service.NewUserService(userRepo)
	keySvc := service.NewAPIKeyService(apiKeyRepo)
	billingSvc := service.NewBillingService(deps.DB, walletRepo)
	cdkSvc := service.NewCDKService(deps.DB, billingSvc)
	sysCfgSvc := service.NewSystemConfigService(sysCfgRepo)
	proxySvc := service.NewProxyService(proxyRepo, deps.AES)
	inviteSvc := service.NewInviteService(inviteRepo, userRepo, sysCfgSvc, billingSvc)
	// 注册赠点：让 AuthService.Register 读取 billing.free_initial_points 并发放。
	authSvc.SetSignupGift(billingSvc, sysCfgSvc)

	upstreamChannelRepo := repo.NewUpstreamChannelRepo(deps.DB)
	taskCostLogRepo := repo.NewTaskCostLogRepo(deps.DB)
	upstreamChannelSvc := service.NewUpstreamChannelService(upstreamChannelRepo, deps.AES)
	costRecorder := service.NewCostRecorder(upstreamChannelSvc, sysCfgSvc, taskCostLogRepo)

	announcementRepo := repo.NewAnnouncementRepo(deps.DB)
	announcementSvc := service.NewAnnouncementService(announcementRepo)
	announcementH := handler.NewAnnouncementHandler(announcementSvc)

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

	authH := handler.NewAuthHandler(authSvc, userSvc)
	keyH := handler.NewAPIKeyHandler(keySvc)
	billH := handler.NewBillingHandler(billingSvc, cdkSvc, sysCfgSvc)
	genH := handler.NewGenerationHandler(genSvc, chatSvc, genRepo, accountRepo, sysCfgSvc, deps.AES)
	genH.SetClusterService(clusterSvc)
	inviteH := handler.NewInviteHandler(inviteSvc)

	v1.GET("/models", genH.Models)
	// 公开公告：用户端首页顶部滚动条用，未登录也要可见。
	v1.GET("/announcements", announcementH.ListActive)
	// 充值套餐 + 客服联系方式：billing 页未登录访问时（如游客逛价）也展示，
	// 内部已过滤掉 remark / 内部备注 / 支付密钥等敏感字段，无需鉴权。
	v1.GET("/recharge/products", billH.RechargeProducts)
	v1.GET("/gen/cached/*path", genH.CachedAsset)
	v1.GET("/gen/assets/:task_id/:seq", genH.Asset)
	// 重定向直链：storage.result_cache_driver=redirect 时，结果 URL 是签名短链，
	// 命中后 302 跳转到上游临时直链。公开访问（<img src> 直接用），靠 HMAC + 过期防猜。
	v1.GET("/m/:token", genH.RedirectMedia)
	v1.GET("/gen/media/:token", genH.RedirectMedia) // 兼容历史长链接
	// 公开匿名 + IP 限流的「下载失败汇报」端点；让浏览器在 302 到边缘节点失败时
	// 把对应 locator 标 tainted，下次 ResolveDownload 自动跳过该节点。
	{
		tainted := v1.Group("/gen/cached")
		if deps.Limiter != nil {
			tainted.Use(middleware.RateLimitIP(deps.Limiter, 60))
		}
		tainted.POST("/tainted", genH.ReportTaintedAsset)
	}

	auth := v1.Group("/auth")
	{
		// 注册 / 登录限流：每 IP 每分钟 30 次
		if deps.Limiter != nil {
			auth.Use(middleware.RateLimitIP(deps.Limiter, 30))
		}
		auth.POST("/register", authH.Register)
		auth.POST("/login", authH.Login)
		auth.POST("/refresh", authH.Refresh)
		auth.POST("/logout", authH.Logout)
	}

	// 需要登录的用户接口
	authed := v1.Group("/")
	authed.Use(middleware.AuthJWT(deps.JWT, jwtx.SubjectUser))
	{
		authed.GET("/users/me", authH.Me)
		authed.POST("/users/password", authH.ChangePassword)

		keys := authed.Group("/keys")
		{
			keys.GET("", keyH.List)
			keys.GET("/stats", keyH.Stats)
			keys.POST("", keyH.Create)
			keys.POST("/:id/toggle", keyH.Toggle)
			keys.DELETE("/:id", keyH.Delete)
		}

		bill := authed.Group("/billing")
		{
			bill.GET("/logs", billH.Logs)
			bill.POST("/cdk/redeem", billH.RedeemCDK)
		}

		gen := authed.Group("/gen")
		{
			gen.POST("/image", genH.CreateImage)
			gen.POST("/text", genH.CreateText)
			gen.POST("/video", genH.CreateVideo)
			gen.POST("/music", genH.CreateMusic)
			gen.GET("/tasks/:task_id", genH.Get)
			gen.GET("/history", genH.List)
			gen.DELETE("/history", genH.DeleteHistory)
		}

		invite := authed.Group("/invite")
		{
			invite.GET("/summary", inviteH.Summary)
			invite.GET("/invitees", inviteH.Invitees)
		}
	}
}
