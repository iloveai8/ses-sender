// Package app 负责 HTTP 装配：路由注册（按 Python 版 include 顺序）、中间件链、优雅停机。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"ses-sender/internal/account"
	"ses-sender/internal/campaign"
	"ses-sender/internal/contact"
	"ses-sender/internal/httpx"
	"ses-sender/internal/platform/awsx"
	"ses-sender/internal/platform/config"
	"ses-sender/internal/platform/database"
	"ses-sender/internal/system"
	"ses-sender/internal/template"
)

// Run 组合根入口：装配依赖并启动（引擎/事件源/调度器随域推进在 all|worker 模式接入）
func Run(cfg *config.Config) error {
	// ── 数据库（all/api 模式必需）──
	db, err := database.Open(cfg)
	if err != nil {
		return fmt.Errorf("数据库不可用: %w", err)
	}
	defer db.Close()
	if err := database.Bootstrap(db, "migrations"); err != nil {
		slog.Warn("迁移引导未完成（不阻塞启动，与 Python 版行为一致）", "err", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := account.EnsureDefaultAdmin(ctx, db); err != nil {
		slog.Warn("默认管理员种子失败", "err", err)
	}
	cancel()

	// ── account 域装配 ──
	store := account.NewStore(db)
	tokens := account.NewTokenSigner(cfg.SecretKey)
	rl := account.NewRateLimiter()
	acct := account.NewHandler(store, tokens, rl)

	// ── HTTP ──
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// 健康端点（契约锚定：与 Python 版逐字节一致）
	r.GET("/", func(c *gin.Context) {
		httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "SES Sender API is running"})
	})

	// 未匹配路由 → 404 JSON（gin 默认是 text/plain，契约要求 {"detail":"Not Found"}）
	r.NoRoute(func(c *gin.Context) {
		httpx.WriteJSON(c, httpx.ErrNotFound().Status, gin.H{"detail": httpx.ErrNotFound().Detail})
	})

	// ── account 路由（Python include 顺序第一位）──
	r.POST("/auth/login", acct.Login)
	auth := r.Group("", acct.Authenticate())
	{
		auth.GET("/auth/me", acct.Me)
	}
	admin := r.Group("", acct.Authenticate(), acct.RequireAdmin())
	{
		admin.GET("/admin/users", acct.AdminUsersList)
		admin.POST("/admin/users", acct.AdminUsersCreate)
		admin.PUT("/admin/users/:user_id", acct.AdminUsersUpdate)
	}
	user := r.Group("", acct.Authenticate())
	{
		user.GET("/user/unsub-config", acct.UnsubConfigGet)
		user.PUT("/user/unsub-config", acct.UnsubConfigPut)
		user.GET("/user/unsub-defaults", acct.UnsubDefaults)
		user.PUT("/user/contact-email", acct.ContactEmailPut)
	}

	// ── contact 路由（Python include 顺序第二位）──
	ct := contact.NewHandler(contact.NewStore(db))
	{
		user.GET("/groups", ct.ListGroups)
		user.POST("/groups", ct.CreateGroup)
		user.PUT("/groups/:id", ct.UpdateGroup)
		user.DELETE("/groups/:id", ct.DeleteGroup)
		user.GET("/groups/:id/contacts", ct.ListContacts)
		user.POST("/contacts", ct.CreateContact)
		user.DELETE("/contacts/:id", ct.DeleteContact)
	}

	// ── template 路由（Python include 顺序第三位；/admin 版同构复用——按当前用户隔离，毛边照抄）──
	sesTpl, err := awsx.NewSESTemplates(context.Background(), cfg.AWS.Region)
	if err != nil {
		return fmt.Errorf("SES 客户端初始化: %w", err)
	}
	tp := template.NewHandler(template.NewStore(db), sesTpl)
	{
		user.GET("/user/templates", tp.List)
		user.POST("/user/templates", tp.Create)
		user.PUT("/user/templates/:id", tp.Update)
		user.DELETE("/user/templates/:id", tp.Delete)
		user.GET("/user/templates/:id/attachments", tp.ListAttachments)
		user.DELETE("/user/templates/:id/attachments/:att_id", tp.DeleteAttachment)
		admin.GET("/admin/templates", tp.List)
		admin.POST("/admin/templates", tp.Create)
		admin.PUT("/admin/templates/:id", tp.Update)
		admin.DELETE("/admin/templates/:id", tp.Delete)
	}

	// ── system 路由（Python include 顺序第六位）──
	sysStore := system.NewStore(db)
	sysBL := system.NewBlacklistCache(sysStore)
	ctxBL, cancelBL := context.WithCancel(context.Background())
	defer cancelBL()
	sysBL.Start(ctxBL) // 同步加载+60s 刷新（Python 无条件启动，照抄）
	sysH := system.NewHandler(sysStore, sysBL, cfg.Bedrock)
	{
		admin.GET("/admin/settings", sysH.SettingsGet)
		admin.PUT("/admin/settings", sysH.SettingsPut)
		admin.GET("/admin/ai-models", sysH.AIMModelsGet)
		admin.PUT("/admin/ai-models", sysH.AIMModelsPut)
		user.GET("/ai-models/available", sysH.AIAvailable)
		admin.GET("/admin/blacklist", sysH.BlacklistList)
		admin.POST("/admin/blacklist", sysH.BlacklistAdd)
		admin.DELETE("/admin/blacklist/:id", sysH.BlacklistDelete)
		admin.POST("/admin/blacklist/batch-delete", sysH.BlacklistBatchDelete)
		admin.GET("/admin/blacklist/count", sysH.BlacklistCount)
	}

	// ── campaign 路由（Python include 顺序第五位）──
	cp := campaign.NewHandler(campaign.NewStore(db))
	{
		user.GET("/user/dashboard", cp.Dashboard)
		user.GET("/user/daily-quota", cp.DailyQuota)
		user.GET("/sending-jobs", cp.ListJobs)
		user.GET("/sending-jobs/:batch_id/metrics", cp.Metrics)
		user.GET("/sending-jobs/:batch_id/details", cp.Details)
		user.GET("/sending-jobs/:batch_id/progress", cp.Progress)
		user.GET("/email-details", cp.EmailDetails)
		user.GET("/scheduled-jobs", cp.ListScheduled)
		user.POST("/scheduled-jobs", cp.CreateScheduled)
		user.PUT("/scheduled-jobs/:id", cp.UpdateScheduled)
		user.DELETE("/scheduled-jobs/:id", cp.DeleteScheduled)
		user.GET("/unsubscribe-list", cp.ListUnsub)
		user.DELETE("/unsubscribe-list/:id", cp.RestoreUnsub)
		user.POST("/unsubscribe-list/batch-delete", cp.BatchRestoreUnsub)
		admin.GET("/admin/users/quotas", cp.AdminQuotas)
		admin.GET("/admin/sending-stats", cp.AdminStats)
		admin.GET("/admin/sending-jobs", cp.AdminJobs)
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http listening", "addr", srv.Addr, "mode", cfg.Server.Mode)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// 优雅停机（06-ENGINE-DESIGN §9）：SIGTERM → 25s 内排空退出（ECS stopTimeout 30s 内）
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-quit:
		slog.Info("shutting down (signal received)")
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
}
