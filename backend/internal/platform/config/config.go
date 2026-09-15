// Package config 装载运行配置：内置默认值 → config.yaml → 环境变量 → 命令行 flag（高者优先）。
// 环境变量沿用 Python 版全部变量名（AWS_REGION/SENDER_*/SECRET_KEY/...），生产 taskdef 与
// SSM 注入方式零改动；密钥只经环境变量进入，yaml 模板（config.example.yaml）不含真实值。
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config 全量运行配置（字段与 config.example.yaml 一一对应）
type Config struct {
	Server    Server   `mapstructure:"server"`
	Database  Database `mapstructure:"database"`
	SecretKey string   `mapstructure:"secret_key"` // JWT 签名 + 退订 HMAC 密钥（生产走 env 注入）
	AWS       AWS      `mapstructure:"aws"`
	Sender    Sender   `mapstructure:"sender"`
	Unsub     Unsub    `mapstructure:"unsubscribe"`
	Bedrock   Bedrock  `mapstructure:"bedrock"`
}

type Server struct {
	Port int    `mapstructure:"port"` // 默认 8000，容器恒 8000
	Mode string `mapstructure:"mode"` // all | api | worker
}

type Database struct {
	URL string `mapstructure:"url"` // 兼容 mysql+pymysql:// 前缀（Python 习惯），DSN() 做翻译
}

type AWS struct {
	Region string `mapstructure:"region"`
	SES    struct {
		ConfigurationSet string `mapstructure:"configuration_set"` // 空=不附配置集（无回执）
	} `mapstructure:"ses"`
	SQS struct {
		QueueURL string `mapstructure:"queue_url"` // 空=不启动 SQS 事件轮询
	} `mapstructure:"sqs"`
}

type Sender struct {
	Enabled              bool `mapstructure:"enabled"` // 引擎开关（对应 ENABLE_SENDER）
	Concurrency          int  `mapstructure:"concurrency"`
	MessageRate          int  `mapstructure:"message_rate"` // 0=自动跟随 SES 配额
	SlidingWindowSeconds int  `mapstructure:"sliding_window_seconds"`
	SlidingWindowRate    int  `mapstructure:"sliding_window_rate"`
}

type Unsub struct {
	BaseURL string `mapstructure:"base_url"` // 空=退订链接替换为 "#"
}

type Bedrock struct {
	ModelID string `mapstructure:"model_id"`
	Region  string `mapstructure:"region"`
}

// Load 装载配置。path 为空时在当前目录查找 config.yaml（不存在则仅用默认值+环境变量）。
func Load(path string) (*Config, error) {
	v := viper.New()

	// 内置默认值（与 Python core/config.py 默认对齐）
	v.SetDefault("server.port", 8000)
	v.SetDefault("server.mode", "all")
	v.SetDefault("aws.region", "us-east-2")
	v.SetDefault("sender.enabled", true)
	v.SetDefault("sender.concurrency", 2)
	v.SetDefault("sender.message_rate", 0)
	v.SetDefault("sender.sliding_window_seconds", 0)
	v.SetDefault("sender.sliding_window_rate", 0)
	v.SetDefault("bedrock.model_id", "global.anthropic.claude-opus-4-6-v1")
	v.SetDefault("bedrock.region", "us-east-1")
	v.SetDefault("secret_key", "ses-sender-secret-key-change-in-production")

	// 环境变量绑定（沿用 Python 版变量名：生产 taskdef/SSM 零改动）
	for key, env := range map[string]string{
		"server.port":                   "SERVER_PORT",
		"database.url":                  "DATABASE_URL",
		"secret_key":                    "SECRET_KEY",
		"aws.region":                    "AWS_REGION",
		"aws.ses.configuration_set":     "SES_CONFIGURATION_SET",
		"aws.sqs.queue_url":             "SQS_QUEUE_URL",
		"sender.enabled":                "ENABLE_SENDER",
		"sender.concurrency":            "SENDER_CONCURRENCY",
		"sender.message_rate":           "SENDER_MESSAGE_RATE",
		"sender.sliding_window_seconds": "SENDER_SLIDING_WINDOW_SECONDS",
		"sender.sliding_window_rate":    "SENDER_SLIDING_WINDOW_RATE",
		"unsubscribe.base_url":          "UNSUBSCRIBE_BASE_URL",
		"bedrock.model_id":              "BEDROCK_MODEL_ID",
		"bedrock.region":                "BEDROCK_REGION",
	} {
		_ = v.BindEnv(key, env)
	}

	// yaml 文件（可选），三种方式按优先级：
	// ① --config 显式路径
	// ② APP_ENV 约定：config/config.{APP_ENV}.yaml（多环境部署用，如 APP_ENV=test）
	// ③ 通用回落：config/config.yaml（config/ 目录优先，其次当前目录）
	// 全部未命中 = 仅默认值+环境变量（容器/CI 形态，合法）
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("读取配置文件失败: %w", err)
		}
	} else {
		v.SetConfigType("yaml")
		v.AddConfigPath("config")
		v.AddConfigPath(".")
		names := []string{"config"}
		if env := strings.TrimSpace(os.Getenv("APP_ENV")); env != "" {
			names = append([]string{"config." + env}, names...)
		}
		for _, name := range names {
			v.SetConfigName(name)
			err := v.ReadInConfig()
			if err == nil {
				break
			}
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return nil, fmt.Errorf("解析配置文件失败: %w", err)
			}
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("配置反序列化失败: %w", err)
	}
	return &cfg, nil
}

// DSN 把连接串翻译为 go-sql-driver 的 DSN。
// 兼容两种写法：mysql+pymysql://user:pass@host:port/db（Python 习惯，SSM 里存量格式）
//
//	与 user:pass@tcp(host:port)/db（Go 原生）；统一补 utf8mb4/parseTime/UTC。
func (d Database) DSN() string {
	u := d.URL
	if strings.HasPrefix(u, "mysql+pymysql://") {
		u = strings.TrimPrefix(u, "mysql+pymysql://")
	}
	if sep := strings.IndexAny(u, "?"); sep >= 0 { // 已带参数：仅追加必需项
		u = u + "&charset=utf8mb4&parseTime=true&loc=UTC"
		return u
	}
	return u + "?charset=utf8mb4&parseTime=true&loc=UTC"
}
