package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// sqsMaxReceives 毒消息阈值：同一条消息失败达此次数仍异常 → 删除（之前的失败留给可见性超时重投）
const sqsMaxReceives = 3

// StartSQSWorker 启动 SQS 事件轮询（SES→SNS→SQS 回执链路；长轮询 10条/20s）。
// 仅在配置了队列 URL 时由组合根调用（all/worker 模式）。
func StartSQSWorker(ctx context.Context, db *sql.DB, queueURL, region string) error {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return err
	}
	client := sqs.NewFromConfig(cfg)
	go func() {
		slog.Info("[SQS Worker] 启动", "queue", queueURL)
		for {
			if ctx.Err() != nil {
				return
			}
			pollSQS(ctx, client, db, queueURL)
		}
	}()
	return nil
}

func pollSQS(ctx context.Context, client *sqs.Client, db *sql.DB, queueURL string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[SQS Worker] 轮询 panic", "err", r)
		}
	}()
	out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(queueURL),
		MaxNumberOfMessages:   10,
		WaitTimeSeconds:       20,
		MessageAttributeNames: []string{"All"},
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
		},
	})
	if err != nil {
		slog.Warn("[SQS Worker] 拉取失败（5s 后重试）", "err", err)
		sleepCtx(ctx, 5)
		return
	}
	for _, msg := range out.Messages {
		handleSQSMessage(ctx, client, db, queueURL, msg)
	}
}

func handleSQSMessage(ctx context.Context, client *sqs.Client, db *sql.DB, queueURL string, msg types.Message) {
	procErr := func() error {
		var body map[string]any
		if err := json.Unmarshal([]byte(aws.ToString(msg.Body)), &body); err != nil {
			return err // 非 JSON：毒消息
		}
		// SNS 包装：Notification → Message 内层才是 SES 事件；订阅确认直接放行
		if t, _ := body["Type"].(string); t == "SubscriptionConfirmation" {
			return nil
		} else if t == "Notification" {
			inner, _ := body["Message"].(string)
			var evt map[string]any
			if err := json.Unmarshal([]byte(inner), &evt); err != nil {
				return err // 内层非 JSON：毒消息
			}
			body = evt
		}
		ProcessSESEvent(ctx, db, body)
		return nil
	}()

	if procErr == nil {
		_, _ = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
			QueueUrl: aws.String(queueURL), ReceiptHandle: msg.ReceiptHandle,
		})
		return
	}
	// 失败：按已投递次数区分瞬时错误（留队重投）与毒消息（删除）
	count := 1
	if msg.Attributes != nil {
		if c, ok := msg.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]; ok {
			if n, err := strconv.Atoi(c); err == nil {
				count = n
			}
		}
	}
	if count >= sqsMaxReceives {
		slog.Error("[SQS Worker] 毒消息删除", "receiveCount", count, "err", procErr,
			"body", aws.ToString(msg.Body)[:minInt(200, len(aws.ToString(msg.Body)))])
		_, _ = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
			QueueUrl: aws.String(queueURL), ReceiptHandle: msg.ReceiptHandle,
		})
	} else {
		slog.Warn("[SQS Worker] 处理失败留队重投", "count", count, "err", procErr)
	}
}

func sleepCtx(ctx context.Context, seconds int) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(seconds) * time.Second):
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
