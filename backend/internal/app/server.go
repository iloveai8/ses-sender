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
	"ses-sender/internal/httpx"
	"ses-sender/internal/platform/config"
	"ses-sender/internal/platform/database"
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
		c.JSON(http.StatusOK, gin.H{"message": "SES Sender API is running"})
	})

	// 未匹配路由 → 404 JSON（gin 默认是 text/plain，契约要求 {"detail":"Not Found"}）
	r.NoRoute(func(c *gin.Context) {
		c.JSON(httpx.ErrNotFound().Status, gin.H{"detail": httpx.ErrNotFound().Detail})
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
