// Package app 负责 HTTP 装配：路由注册（按 Python 版 include 顺序）、中间件链、优雅停机。
// 骨架期仅含健康端点；各域路由随 P4-P8 接入。
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

	"ses-sender/internal/platform/config"
)

// Run 组合根入口：按配置装配组件并启动。
// 骨架期仅启动 HTTP；引擎/事件源/调度器随域推进在 all|worker 模式下接入。
func Run(cfg *config.Config) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// 健康端点（契约锚定：与 Python 版逐字节一致，GET / 探活 + healthcheck 子命令依赖）
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "SES Sender API is running"})
	})

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http listening", "addr", addr, "mode", cfg.Server.Mode)
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
