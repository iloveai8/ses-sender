package awsx

import (
	"context"
	"log/slog"
	"sync"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
)

var (
	quotaOnce  sync.Once
	quotaCache struct {
		MaxRate   int
		Max24Hour int
		Sent24Hr  int
	}
)

// FetchQuota 进程启动时一次性获取 SES v1 配额快照（失败回退默认 1/200/0——Python 语义；
// 引擎速率跟此快照，不刷新——照抄 Python get_send_quota 行为）
func FetchQuota(region string) {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		slog.Warn("[SES Quota] 配置加载失败，使用默认值", "err", err)
		quotaCache.MaxRate, quotaCache.Max24Hour, quotaCache.Sent24Hr = 1, 200, 0
		return
	}
	client := ses.NewFromConfig(cfg)
	out, err := client.GetSendQuota(context.Background(), &ses.GetSendQuotaInput{})
	if err != nil {
		slog.Warn("[SES Quota] 获取配额失败，使用默认值 max_rate=1", "err", err)
		quotaCache.MaxRate, quotaCache.Max24Hour, quotaCache.Sent24Hr = 1, 200, 0
		return
	}
	quotaCache.MaxRate = int(out.MaxSendRate)
	quotaCache.Max24Hour = int(out.Max24HourSend)
	quotaCache.Sent24Hr = int(out.SentLast24Hours)
	slog.Info("[SES Quota] 快照", "rate", quotaCache.MaxRate, "24h", quotaCache.Max24Hour, "sent", quotaCache.Sent24Hr)
}

// QuotaSnapshot 返回配额快照（首次调用时惰性获取）
func QuotaSnapshot(region string) (maxRate, max24h, sent24h int) {
	quotaOnce.Do(func() { FetchQuota(region) })
	return quotaCache.MaxRate, quotaCache.Max24Hour, quotaCache.Sent24Hr
}
