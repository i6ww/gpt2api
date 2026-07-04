package router

import (
	"context"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kleinai/backend/internal/bootstrap"
	"github.com/kleinai/backend/internal/handler"
	"github.com/kleinai/backend/internal/middleware"
	"github.com/kleinai/backend/internal/provider/factory"
	"github.com/kleinai/backend/internal/provider/flowmusic"
	"github.com/kleinai/backend/internal/regkit/dispatcher"
	adobedispatch "github.com/kleinai/backend/internal/regkit/dispatcher/adobe"
	gptdispatch "github.com/kleinai/backend/internal/regkit/dispatcher/gpt"
	grokdispatch "github.com/kleinai/backend/internal/regkit/dispatcher/grok"
	upgradeplus "github.com/kleinai/backend/internal/regkit/dispatcher/upgrade_plus"
	"github.com/kleinai/backend/internal/regkit/mailbox"
	"github.com/kleinai/backend/internal/regkit/proxypicker"
	"github.com/kleinai/backend/internal/regkit/smspool"
	"github.com/kleinai/backend/internal/regkit/workerpool"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/internal/service"
	"github.com/kleinai/backend/pkg/jwtx"
	"go.uber.org/zap"
)

// MountAdmin 挂载管理后台路由
func MountAdmin(r *gin.Engine, deps *bootstrap.Deps) *service.AccountPool {
	v1 := r.Group("/admin/api/v1")

	v1.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"pong": true, "scope": "admin"})
	})

	if deps.DB == nil || deps.AES == nil {
		return nil
	}

	adminRepo := repo.NewAdminRepo(deps.DB)
	userRepo := repo.NewUserRepo(deps.DB)
	accountRepo := repo.NewAccountRepo(deps.DB)
	walletRepo := repo.NewWalletRepo(deps.DB)
	generationRepo := repo.NewGenerationRepo(deps.DB)
	proxyRepo := repo.NewProxyRepo(deps.DB)
	sysCfgRepo := repo.NewSystemConfigRepo(deps.DB)
	promoRepo := repo.NewPromoRepo(deps.DB)
	dashboardRepo := repo.NewDashboardRepo(deps.DB)
	mailPoolRepo := repo.NewMailPoolRepo(deps.DB)
	phonePoolRepo := repo.NewPhonePoolRepo(deps.DB)
	upstreamChannelRepo := repo.NewUpstreamChannelRepo(deps.DB)
	taskCostLogRepo := repo.NewTaskCostLogRepo(deps.DB)
	poolAdobeRepo := repo.NewPoolAdobeRepo(deps.DB)
	poolGrokRepo := repo.NewPoolGrokRepo(deps.DB)
	poolXaiRepo := repo.NewPoolXAIRepo(deps.DB)
	poolGptRepo := repo.NewPoolGptRepo(deps.DB)
	poolGoogleRepo := repo.NewPoolGoogleRepo(deps.DB)
	registerTaskRepo := repo.NewRegisterTaskRepo(deps.DB)
	registerTaskLogRepo := repo.NewRegisterTaskLogRepo(deps.DB)
	cloudPhoneRepo := repo.NewCloudPhonePoolRepo(deps.DB)
	gopayWalletRepo := repo.NewGopayWalletPoolRepo(deps.DB)
	gopayBindingRepo := repo.NewGopayWalletBindingRepo(deps.DB)
	paymentProxyRepo := repo.NewPaymentProxyPoolRepo(deps.DB)

	accountLeaseRepo := repo.NewAccountLeaseRepo(deps.DB)
	pool := service.NewAccountPool(accountRepo, 30*time.Second)
	pool.SetLeaseRepo(accountLeaseRepo)

	adminAuth := service.NewAdminAuthService(adminRepo, deps.JWT)
	adminUserSvc := service.NewAdminUserService(userRepo, walletRepo)
	accountAdmin := service.NewAccountAdminService(accountRepo, pool, deps.AES)
	billingSvc := service.NewBillingService(deps.DB, walletRepo)
	inviteRepo := repo.NewInviteRepo(deps.DB)
	cdkSvc := service.NewCDKService(deps.DB, billingSvc)
	promoSvc := service.NewAdminPromoService(promoRepo)
	sysCfgSvc := service.NewSystemConfigService(sysCfgRepo)
	inviteSvc := service.NewInviteService(inviteRepo, userRepo, sysCfgSvc, billingSvc)
	adminUserSvc.SetInviteService(inviteSvc)
	proxySvc := service.NewProxyService(proxyRepo, deps.AES)
	openaiOAuth := service.NewOpenAIOAuthService(sysCfgSvc)
	accountTest := service.NewAccountTestService(accountRepo, proxySvc, sysCfgSvc, openaiOAuth, deps.AES)
	accountAdmin.SetTestService(accountTest)
	mailPoolSvc := service.NewMailPoolService(mailPoolRepo, deps.AES, sysCfgSvc)
	poolAdobeSvc := service.NewPoolAdobeService(poolAdobeRepo, deps.AES)
	poolGrokSvc := service.NewPoolGrokService(poolGrokRepo, deps.AES)
	poolXaiSvc := service.NewPoolXAIService(poolXaiRepo, deps.AES)
	poolGptSvc := service.NewPoolGptService(poolGptRepo, deps.AES).WithRedis(deps.Redis)
	flowMusicClient := flowmusic.NewClient(flowmusic.Config{
		BaseURL:                 os.Getenv("KLEIN_FLOWMUSIC_BASE_URL"),
		SupabaseBaseURL:         os.Getenv("KLEIN_FLOWMUSIC_SUPABASE_BASE_URL"),
		SupabaseAnonKey:         os.Getenv("KLEIN_FLOWMUSIC_SUPABASE_ANON_KEY"),
		GoogleOAuthTokenURL:     os.Getenv("KLEIN_FLOWMUSIC_GOOGLE_OAUTH_TOKEN_URL"),
		GoogleOAuthClientID:     os.Getenv("KLEIN_FLOWMUSIC_GOOGLE_OAUTH_CLIENT_ID"),
		GoogleOAuthClientSecret: os.Getenv("KLEIN_FLOWMUSIC_GOOGLE_OAUTH_CLIENT_SECRET"),
	})
	poolGoogleSvc := service.NewPoolGoogleService(poolGoogleRepo, deps.AES, flowMusicClient)
	registerTaskSvc := service.NewRegisterTaskService(registerTaskRepo, registerTaskLogRepo, mailPoolRepo)
	cloudPhoneSvc := service.NewCloudPhoneService(cloudPhoneRepo, deps.AES)
	gopayWalletSvc := service.NewGopayWalletService(gopayWalletRepo, gopayBindingRepo, cloudPhoneRepo, deps.AES, deps.DB)
	paymentProxySvc := service.NewPaymentProxyService(paymentProxyRepo, deps.AES)
	upstreamChannelSvc := service.NewUpstreamChannelService(upstreamChannelRepo, deps.AES)
	// 启动时若上游通道表为空，灌入默认 15 行清单（一次性）。
	// 已经手配过的环境会跳过；非阻塞，失败只打日志。
	go func() {
		seedCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := upstreamChannelSvc.SeedIfEmpty(seedCtx); err != nil {
			zap.L().Warn("upstream_channel.seed_failed", zap.Error(err))
		}
	}()
	costRecorder := service.NewCostRecorder(upstreamChannelSvc, sysCfgSvc, taskCostLogRepo)

	mailMgr := mailbox.NewManager(mailPoolRepo, deps.AES)
	smsMgr := smspool.NewManager(phonePoolRepo, sysCfgSvc)
	proxyPicker := proxypicker.NewPicker(proxySvc, proxyRepo, sysCfgSvc)
	regDeps := dispatcher.Deps{MailMgr: mailMgr, ProxyPicker: proxyPicker, SysCfg: sysCfgSvc, SMSMgr: smsMgr}

	registerTaskSvc.RegisterDispatcher("adobe", &adobedispatch.Dispatcher{Deps: regDeps, Pool: poolAdobeSvc})
	registerTaskSvc.RegisterDispatcher("grok", &grokdispatch.Dispatcher{Deps: regDeps, Pool: poolGrokSvc})
	registerTaskSvc.RegisterDispatcher("gpt", &gptdispatch.Dispatcher{Deps: regDeps, Pool: poolGptSvc})
	registerTaskSvc.RegisterDispatcher(upgradeplus.Provider, &upgradeplus.Dispatcher{
		Deps:         regDeps,
		PoolGpt:      poolGptSvc,
		Wallet:       gopayWalletSvc,
		Phone:        cloudPhoneSvc,
		PaymentProxy: paymentProxySvc,
	})

	// 号池注册并发 worker pool；并发数从 system_config 读取（默认 5，可在 UI 调整）。
	regConc := sysCfgSvc.RegisterConcurrency(context.Background())
	regPool := workerpool.New(regConc, registerTaskSvc.RunTask)
	regPool.Start()
	registerTaskSvc.SetSubmitter(regPool)
	// 配置保存后自动 resize：让前端调整并发数能在线生效（仅扩容立即生效）。
	sysCfgSvc.OnUpdate(func(values map[string]any) {
		if v, ok := values[service.SettingRegisterConcurrency]; ok {
			n := 0
			switch x := v.(type) {
			case float64:
				n = int(x)
			case int:
				n = x
			case int64:
				n = int(x)
			}
			if n > 0 {
				regPool.Resize(n)
			}
		}
	})
	if running, requeued, err := registerTaskSvc.RecoverPending(context.Background()); err == nil {
		if running > 0 || requeued > 0 {
			// 启动日志，确认状态恢复发生
			_ = running
			_ = requeued
		}
	}

	authH := handler.NewAdminAuthHandler(adminAuth, adminRepo)
	userH := handler.NewAdminUserHandler(adminUserSvc)
	accountH := handler.NewAdminAccountHandler(accountAdmin, pool)
	cdkH := handler.NewAdminCDKHandler(cdkSvc)
	billingH := handler.NewAdminBillingHandler(walletRepo)
	promoH := handler.NewAdminPromoHandler(promoSvc)
	proxyH := handler.NewAdminProxyHandler(proxySvc, accountTest)
	sysH := handler.NewAdminSystemHandler(sysCfgSvc)
	logH := handler.NewAdminLogHandler(generationRepo, accountRepo, deps.AES)
	announcementRepo := repo.NewAnnouncementRepo(deps.DB)
	announcementSvc := service.NewAnnouncementService(announcementRepo)
	announcementH := handler.NewAdminAnnouncementHandler(announcementSvc)
	// 集群相关 repo / 服务：admin 端预览也走 302 到边缘
	clusterNodeRepo := repo.NewClusterNodeRepo(deps.DB)
	downloadLocatorRepo := repo.NewDownloadLocatorRepo(deps.DB)
	clusterSvc := service.NewClusterService(clusterNodeRepo, downloadLocatorRepo, sysCfgSvc, deps.AES)
	if len(deps.ClusterBootstrap) > 0 {
		clusterSvc.SetBootstrapSecret(deps.ClusterBootstrap)
	}
	logH.SetClusterService(clusterSvc)
	// admin 端 lease/result 用的 GenerationService（与 api.go 各持一份；都只读 DB / 池）
	adminProviders := factory.Build()
	adminGenSvc := service.NewGenerationService(deps.DB, generationRepo, pool, billingSvc, adminProviders, service.ConfigPriceFn(sysCfgSvc), deps.AES, proxySvc, sysCfgSvc)
	adminGenSvc.SetCostRecorder(costRecorder)
	adminGenSvc.SetClusterService(clusterSvc)
	logH.SetGenerationService(adminGenSvc)
	clusterH := handler.NewAdminClusterHandler(clusterSvc, adminGenSvc, deps.Cfg.Cluster.ControlURL)
	// 集群守护：
	//   - 30s 回收 lease 过期任务
	//   - 5min GC 过期 locator
	//   - 60s 反向 /healthz 探活，连续 3 次失败的节点踢到 Maintenance
	service.NewClusterMaintenance(adminGenSvc, downloadLocatorRepo).
		WithNodes(clusterNodeRepo).
		Start(context.Background())
	// 主控自带 embedded agent：cluster.enabled=true 时主控进程自己 lease 任务并跑 runTask。
	// 与远端 agent 通过 ClaimBatch(SKIP LOCKED) + 不同 node_id 互斥。
	// 单机模式（无远端 agent）也能让 cluster 路径转起来，避免 dispatch 后没人接的死锁。
	service.NewEmbeddedAgent(adminGenSvc, clusterSvc).Start(context.Background())
	dashboardH := handler.NewAdminDashboardHandler(dashboardRepo)
	mailPoolH := handler.NewAdminMailPoolHandler(mailPoolSvc)
	adobeProxyPicker := service.AdobeProxyPickerFunc(proxySvc)
	poolAdobeH := handler.NewAdminPoolAdobeHandler(poolAdobeSvc, adobeProxyPicker)
	// 启动 Adobe 后台续期调度器：每 60s 扫一次，<12h 过期 → silent refresh；
	// 30min 没看 credits → 只刷 credits。可在 system_config (key=adobe.refresh) 调整阈值/并发。
	adobeRefreshSched := service.NewAdobeRefreshScheduler(poolAdobeSvc, sysCfgSvc, adobeProxyPicker, zap.L())
	adobeRefreshSched.Start(context.Background())
	// GROK 刷新与 Adobe 共用代理轮转策略（adobeProxyPicker），保证多账号刷新分散到不同出口 IP。
	poolGrokH := handler.NewAdminPoolGrokHandler(poolGrokSvc, adobeProxyPicker)
	poolXaiH := handler.NewAdminPoolXAIHandler(poolXaiSvc, adobeProxyPicker)
	// 官方 xAI API 续期调度器：默认每 60s 扫一次，<15min 过期 → refresh_token 续期。
	// 可在 system_config (key=xai.refresh) 调整阈值/间隔/启停。
	xaiRefreshSched := service.NewXAIRefreshScheduler(poolXaiSvc, sysCfgSvc, adobeProxyPicker, zap.L())
	xaiRefreshSched.Start(context.Background())
	poolGptH := handler.NewAdminPoolGptHandler(poolGptSvc, adobeProxyPicker)
	poolGoogleH := handler.NewAdminPoolGoogleHandler(poolGoogleSvc)
	// FlowMusic（歌曲）后台续期调度器：默认每 120s 扫一次，<12h 过期 → Supabase 续期。
	// 可在 system_config (key=flowmusic.refresh) 调整阈值/间隔/启停。
	googleRefreshSched := service.NewGoogleRefreshScheduler(poolGoogleSvc, sysCfgSvc, zap.L())
	googleRefreshSched.Start(context.Background())
	// 启动 GPT 后台续期调度器：每 120s 扫一次，<12h 过期 → silent refresh；
	// 30min 没看 quota → 只拉 wham/usage 增量。配置 system_config key=gpt.refresh。
	gptRefreshSched := service.NewGptRefreshScheduler(poolGptSvc, sysCfgSvc, adobeProxyPicker, zap.L())
	gptRefreshSched.Start(context.Background())
	registerTaskH := handler.NewAdminRegisterTaskHandler(registerTaskSvc)
	cloudPhoneH := handler.NewAdminCloudPhoneHandler(cloudPhoneSvc, sysCfgSvc)
	gopayWalletH := handler.NewAdminGopayWalletHandler(gopayWalletSvc, sysCfgSvc)
	paymentProxyH := handler.NewAdminPaymentProxyHandler(paymentProxySvc)
	upstreamH := handler.NewAdminUpstreamHandler(upstreamChannelSvc, costRecorder, taskCostLogRepo)

	auth := v1.Group("/auth")
	if deps.Limiter != nil {
		auth.Use(middleware.RateLimitIP(deps.Limiter, 30))
	}
	auth.POST("/login", authH.Login)

	// 资源直连：和用户端 /api/v1/gen/cached/*、/api/v1/gen/assets/:task_id/:seq 形态对齐，
	// admin 这边只是多了 /admin 前缀，便于审计 + 让 admin-web nginx 可以不开放用户后端。
	// 这两条路径无需登录鉴权（图片要走 <img src=> 直接加载，URL 里没法带 token），
	// 但因为只有 admin 后台前端会构造这种链接，外部猜不到具体路径。
	v1.GET("/gen/cached/*path", logH.GenCachedAsset)
	v1.GET("/gen/assets/:task_id/:seq", logH.GenAsset)

	authed := v1.Group("/")
	authed.Use(middleware.AuthJWT(deps.JWT, jwtx.SubjectAdmin))
	{
		authed.GET("/auth/me", authH.Me)
		authed.POST("/auth/password", authH.ChangePassword)
		authed.GET("/dashboard/overview", dashboardH.Overview)

		users := authed.Group("/users")
		{
			users.GET("", userH.List)
			users.POST("", userH.Create)
			users.PUT("/:id", userH.Update)
			users.POST("/:id/points", userH.AdjustPoints)
		}

		acc := authed.Group("/accounts")
		{
			acc.GET("", accountH.List)
			acc.POST("", accountH.Create)
			acc.POST("/import", accountH.BatchImport)
			acc.POST("/batch-delete", accountH.BatchDelete)
			acc.POST("/purge", accountH.Purge)
			acc.POST("/batch-refresh", accountH.BatchRefresh)
			acc.POST("/batch-probe", accountH.BatchProbeQuota)
			acc.GET("/stats", accountH.PoolStats)
			acc.PUT("/:id", accountH.Update)
			acc.DELETE("/:id", accountH.Delete)
			acc.POST("/:id/test", accountH.Test)
			acc.POST("/:id/models", accountH.SyncModels)
			acc.POST("/:id/refresh", accountH.RefreshOAuth)
			acc.GET("/:id/secrets", accountH.Secrets)
		}

		proxies := authed.Group("/proxies")
		{
			proxies.GET("", proxyH.List)
			proxies.POST("", proxyH.Create)
			proxies.POST("/import", proxyH.Import)
			proxies.POST("/batch-delete", proxyH.BatchDelete)
			proxies.POST("/batch-test", proxyH.BatchTest)
			proxies.PUT("/:id", proxyH.Update)
			proxies.DELETE("/:id", proxyH.Delete)
			proxies.POST("/:id/test", proxyH.Test)
		}

		sys := authed.Group("/system")
		{
			sys.GET("/settings", sysH.GetSettings)
			sys.PUT("/settings", sysH.UpdateSettings)
			sys.GET("/cache", sysH.CacheStats)
			sys.DELETE("/cache", sysH.CleanCache)
		}

		cdk := authed.Group("/cdk")
		{
			cdk.GET("/batches", cdkH.ListBatches)
			cdk.POST("/batches", cdkH.CreateBatch)
			cdk.GET("/batches/:id", cdkH.GetBatch)
			cdk.POST("/batches/:id/toggle", cdkH.ToggleBatch)
			cdk.POST("/batches/:id/append", cdkH.AppendBatch)
			cdk.GET("/batches/:id/codes", cdkH.ListCodes)
			cdk.GET("/batches/:id/export", cdkH.ExportBatch)
			cdk.POST("/codes/:id/revoke", cdkH.RevokeCode)
		}

		billing := authed.Group("/billing")
		{
			billing.GET("/wallet-logs", billingH.WalletLogs)
			billing.GET("/wallet-logs/summary", billingH.WalletSummary)
		}

		promo := authed.Group("/promo")
		{
			promo.GET("/codes", promoH.List)
			promo.POST("/codes", promoH.Create)
			promo.PUT("/codes/:id", promoH.Update)
			promo.DELETE("/codes/:id", promoH.Delete)
		}

		logs := authed.Group("/logs")
		{
			logs.GET("/generations", logH.GenerationLogs)
			logs.POST("/generations/cleanup-stuck", logH.CleanupStuckGenerations)
			logs.GET("/generations/:task_id/upstream", logH.GenerationUpstreamLogs)
			logs.DELETE("/generations", logH.PurgeGenerationLogs)
		}

		// 系统公告 CRUD（admin 后台维护，对外用户端首页顶部滚动条展示）。
		announcements := authed.Group("/announcements")
		{
			announcements.GET("", announcementH.List)
			announcements.POST("", announcementH.Create)
			announcements.PUT("/:id", announcementH.Update)
			announcements.DELETE("/:id", announcementH.Delete)
		}

		mailPool := authed.Group("/mail-pool")
		{
			mailPool.GET("", mailPoolH.List)
			mailPool.GET("/stats", mailPoolH.Stats)
			mailPool.POST("/import", mailPoolH.Import)
			mailPool.POST("/cf-generate", mailPoolH.CFGenerate)
			mailPool.POST("/batch-delete", mailPoolH.BatchDelete)
			mailPool.POST("/delete-by-status", mailPoolH.DeleteByStatus)
			mailPool.POST("/truncate", mailPoolH.Truncate)
			mailPool.POST("/reset", mailPoolH.Reset)
			mailPool.PUT("/:id", mailPoolH.Update)
			mailPool.DELETE("/:id", mailPoolH.Delete)
		}

		pools := authed.Group("/pools")
		{
			google := pools.Group("/google")
			{
				google.GET("", poolGoogleH.List)
				google.GET("/stats", poolGoogleH.Stats)
				google.POST("", poolGoogleH.Create)
				google.POST("/import", poolGoogleH.Import)
				google.POST("/batch-delete", poolGoogleH.BatchDelete)
				google.POST("/refresh-all", poolGoogleH.RefreshAll)
				google.POST("/batch-refresh", poolGoogleH.BatchRefresh)
				google.PUT("/:id", poolGoogleH.Update)
				google.DELETE("/:id", poolGoogleH.Delete)
				google.POST("/:id/refresh", poolGoogleH.Refresh)
			}

			adobe := pools.Group("/adobe")
			{
				adobe.GET("", poolAdobeH.List)
				adobe.GET("/stats", poolAdobeH.Stats)
				adobe.POST("", poolAdobeH.Create)
				adobe.POST("/import", poolAdobeH.Import)
				adobe.GET("/export", poolAdobeH.Export)
				adobe.POST("/batch-delete", poolAdobeH.BatchDelete)
				adobe.POST("/purge", poolAdobeH.Purge)
				adobe.POST("/refresh-all", poolAdobeH.RefreshAll)
				adobe.POST("/batch-refresh", poolAdobeH.BatchRefresh)
				adobe.PUT("/:id", poolAdobeH.Update)
				adobe.DELETE("/:id", poolAdobeH.Delete)
				adobe.POST("/:id/refresh", poolAdobeH.Refresh)
			}

			grok := pools.Group("/grok")
			{
				grok.GET("", poolGrokH.List)
				grok.GET("/stats", poolGrokH.Stats)
				grok.POST("", poolGrokH.Create)
				grok.POST("/import", poolGrokH.Import)
				grok.POST("/batch-delete", poolGrokH.BatchDelete)
				grok.POST("/expire-overdue", poolGrokH.ExpireOverdue)
				grok.POST("/purge", poolGrokH.Purge)
				grok.POST("/batch-refresh", poolGrokH.BatchRefresh)
				grok.GET("/batch-refresh/status", poolGrokH.BatchRefreshStatus)
				grok.POST("/batch-refresh/cancel", poolGrokH.BatchRefreshCancel)
				grok.POST("/:id/refresh", poolGrokH.Refresh)
				grok.PUT("/:id", poolGrokH.Update)
				grok.DELETE("/:id", poolGrokH.Delete)
			}

			xaiPool := pools.Group("/xai")
			{
				xaiPool.GET("", poolXaiH.List)
				xaiPool.GET("/stats", poolXaiH.Stats)
				xaiPool.POST("", poolXaiH.Create)
				xaiPool.POST("/import", poolXaiH.Import)
				xaiPool.POST("/batch-delete", poolXaiH.BatchDelete)
				xaiPool.POST("/purge", poolXaiH.Purge)
				xaiPool.POST("/batch-refresh", poolXaiH.BatchRefresh)
				xaiPool.POST("/billing/refresh-all", poolXaiH.RefreshBillingAll)
				xaiPool.POST("/:id/refresh", poolXaiH.Refresh)
				xaiPool.POST("/:id/billing", poolXaiH.RefreshBilling)
				xaiPool.PUT("/:id", poolXaiH.Update)
				xaiPool.DELETE("/:id", poolXaiH.Delete)
			}

			gpt := pools.Group("/gpt")
			{
				gpt.GET("", poolGptH.List)
				gpt.GET("/stats", poolGptH.Stats)
				gpt.POST("", poolGptH.Create)
				gpt.POST("/import", poolGptH.Import)
				gpt.POST("/batch-delete", poolGptH.BatchDelete)
				gpt.POST("/batch-refresh", poolGptH.BatchRefresh)
				gpt.POST("/refresh-all", poolGptH.RefreshAll)
				gpt.POST("/purge", poolGptH.Purge)
				gpt.GET("/export", poolGptH.Export)
				gpt.POST("/export", poolGptH.Export) // selected 时支持 POST body
				gpt.GET("/:id", poolGptH.Detail)
				gpt.PUT("/:id", poolGptH.Update)
				gpt.DELETE("/:id", poolGptH.Delete)
				gpt.POST("/:id/refresh", poolGptH.Refresh)
			}
		}

		registerTasks := authed.Group("/register-tasks")
		{
			registerTasks.GET("", registerTaskH.List)
			registerTasks.GET("/stats", registerTaskH.Stats)
			registerTasks.GET("/logs", registerTaskH.Logs)
			registerTasks.DELETE("/logs", registerTaskH.LogsPurge)
			registerTasks.POST("", registerTaskH.Create)
			registerTasks.DELETE("", registerTaskH.Purge)
			registerTasks.GET("/:id", registerTaskH.Get)
			registerTasks.POST("/:id/cancel", registerTaskH.Cancel)
			registerTasks.DELETE("/:id", registerTaskH.Delete)
		}

		// ── Plus 升级资源池 ──
		// /cloud-phones (GeeLark 云手机) / /gopay-wallets (GoPay 钱包+绑定) / /payment-proxies (印尼支付代理)
		cloudPhones := authed.Group("/cloud-phones")
		{
			cloudPhones.GET("", cloudPhoneH.List)
			cloudPhones.GET("/stats", cloudPhoneH.Stats)
			cloudPhones.POST("", cloudPhoneH.Create)
			cloudPhones.POST("/import", cloudPhoneH.Import)
			cloudPhones.POST("/batch-delete", cloudPhoneH.BatchDelete)
			cloudPhones.PUT("/:id", cloudPhoneH.Update)
			cloudPhones.DELETE("/:id", cloudPhoneH.Delete)
			cloudPhones.POST("/:id/gopay-unlink-openai", cloudPhoneH.GopayUnlinkOpenAI)
		}

		gopayWallets := authed.Group("/gopay-wallets")
		{
			gopayWallets.GET("", gopayWalletH.List)
			gopayWallets.GET("/stats", gopayWalletH.Stats)
			gopayWallets.POST("", gopayWalletH.Create)
			gopayWallets.POST("/import", gopayWalletH.Import)
			gopayWallets.POST("/batch-delete", gopayWalletH.BatchDelete)
			gopayWallets.GET("/bindings", gopayWalletH.ListBindings)
			gopayWallets.POST("/bindings/:id/cancel", gopayWalletH.CancelBinding)
			gopayWallets.PUT("/:id", gopayWalletH.Update)
			gopayWallets.DELETE("/:id", gopayWalletH.Delete)
			gopayWallets.GET("/:id/secrets", gopayWalletH.Secrets)
		}

		paymentProxies := authed.Group("/payment-proxies")
		{
			paymentProxies.GET("", paymentProxyH.List)
			paymentProxies.GET("/stats", paymentProxyH.Stats)
			paymentProxies.POST("", paymentProxyH.Create)
			paymentProxies.POST("/import", paymentProxyH.Import)
			paymentProxies.POST("/batch-delete", paymentProxyH.BatchDelete)
			paymentProxies.PUT("/:id", paymentProxyH.Update)
			paymentProxies.DELETE("/:id", paymentProxyH.Delete)
			paymentProxies.POST("/:id/test", paymentProxyH.Test)
		}

		// ── 上游 API 管理 ──
		// /upstream/channels   通道增删改查（含 seed）
		// /upstream/routes     内部 model → channel 路由
		// /upstream/profit/*   利润报表
		// /upstream/logs       成本日志明细
		upstream := authed.Group("/upstream")
		{
			channels := upstream.Group("/channels")
			{
				channels.GET("", upstreamH.ListChannels)
				channels.POST("", upstreamH.CreateChannel)
				channels.POST("/seed", upstreamH.SeedChannels)
				channels.PUT("/:id", upstreamH.UpdateChannel)
				channels.DELETE("/:id", upstreamH.DeleteChannel)
			}
			routes := upstream.Group("/routes")
			{
				routes.GET("", upstreamH.ListRoutes)
				routes.POST("", upstreamH.CreateRoute)
				routes.PUT("/:id", upstreamH.UpdateRoute)
				routes.DELETE("/:id", upstreamH.DeleteRoute)
			}
			upstream.GET("/profit/overview", upstreamH.ProfitOverview)
			upstream.GET("/profit/daily", upstreamH.ProfitDaily)
			upstream.GET("/logs", upstreamH.CostLogs)
		}

		// ── 集群节点管理（仅管理类，agent 内部接口在 /cluster 同前缀但使用 HMAC） ──
		clusterAdmin := authed.Group("/cluster")
		{
			clusterAdmin.GET("/overview", clusterH.Overview)
			clusterAdmin.GET("/nodes", clusterH.ListNodes)
			clusterAdmin.POST("/nodes", clusterH.UpsertNode)
			clusterAdmin.POST("/nodes/:id/status", clusterH.UpdateNodeStatus)
			clusterAdmin.POST("/nodes/:id/revoke", clusterH.RevokeNode)
			clusterAdmin.POST("/nodes/:id/bootstrap", clusterH.ReissueBootstrap)
			clusterAdmin.DELETE("/nodes/:id", clusterH.DeleteNode)
			clusterAdmin.POST("/locator/tainted", clusterH.TaintLocator)
		}
	}

	// ── agent → 主控 内部接口（不走 admin JWT，走 ClusterHMAC） ──
	clusterIngress := v1.Group("/cluster")
	{
		// handshake 不走 HMAC（首次拿不到 secret），仅 bootstrap token 校验
		clusterIngress.POST("/handshake", clusterH.Handshake)

		hmacGated := clusterIngress.Group("")
		hmacGated.Use(middleware.ClusterHMAC(clusterSvc))
		{
			hmacGated.POST("/heartbeat", clusterH.Heartbeat)
			hmacGated.POST("/lease", clusterH.Lease)
			hmacGated.POST("/result", clusterH.Result)
		}
	}

	return pool
}
