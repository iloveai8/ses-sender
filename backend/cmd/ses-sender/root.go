package main

import (
	"os"

	"github.com/spf13/cobra"
)

// cfgPath 配置文件路径（--config；空=查当前目录 config.yaml，见 platform/config）
var cfgPath string

// rootCmd 根命令：全局参数挂这里；裸执行打印帮助
var rootCmd = &cobra.Command{
	Use:   "ses-sender",
	Short: "SES 批量邮件平台后端",
	Long:  "ses-sender 后端服务。子命令：serve（服务）/ healthcheck（容器探针）；规划中：migrate / admin",
}

func execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", "", "配置文件路径（默认查当前目录 config.yaml）")
}
