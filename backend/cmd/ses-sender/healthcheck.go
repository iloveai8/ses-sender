package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// healthcheckCmd 容器健康探针（ECS taskdef healthCheck 调用，替代 Python 版的 python -c urllib）。
//
// ★ 纪律（ADR-6，cobra 单二进制方案的代价）：本命令执行路径【零初始化】——
//
//	不读配置、不连数据库、不 import 任何业务包；只做一次 GET。
//	违反本纪律 = 探针与应用同生共死，失去体检意义。
var hcPort int

var healthcheckCmd = &cobra.Command{
	Use:   "healthcheck",
	Short: "容器健康探针：GET 本地健康端点，通=exit 0，不通=exit 1",
	Run: func(cmd *cobra.Command, args []string) {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/", hcPort))
		if err != nil {
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	},
}

func init() {
	healthcheckCmd.Flags().IntVar(&hcPort, "port", 8000, "健康端点端口（容器恒 8000）")
	rootCmd.AddCommand(healthcheckCmd)
}
