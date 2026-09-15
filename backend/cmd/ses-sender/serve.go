package main

import (
	"log/slog"

	"github.com/spf13/cobra"

	"ses-sender/internal/app"
	"ses-sender/internal/platform/config"
)

// serveMode / servePort 留空（""/0）表示"未显式指定"——回落 config.yaml，再回落内置默认值
var serveMode string
var servePort int

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "启动服务（组合根：装配依赖并运行）",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		if serveMode != "" {
			cfg.Server.Mode = serveMode
		}
		if servePort > 0 {
			cfg.Server.Port = servePort
		}
		slog.Info("starting ses-sender", "mode", cfg.Server.Mode, "port", cfg.Server.Port)
		return app.Run(cfg)
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveMode, "mode", "", "运行模式: all | api | worker（空=取配置/默认 all）")
	serveCmd.Flags().IntVar(&servePort, "port", 0, "HTTP 监听端口（0=取配置/默认 8000；容器恒 8000）")
	rootCmd.AddCommand(serveCmd)
}
