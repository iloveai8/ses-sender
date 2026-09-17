package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// SESMailProvider SES v2 实现（MailProvider 第一步唯一实现；多渠道在此扩展）
type SESMailProvider struct {
	client    *sesv2.Client
	cfgSet    string
	unsubBase string
}

// NewSESMailProvider 构造（凭证走默认链：~/.aws 或任务角色）
func NewSESMailProvider(ctx context.Context, region, cfgSet, unsubBase string) (*SESMailProvider, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("加载 AWS 配置: %w", err)
	}
	return &SESMailProvider{
		client:    sesv2.NewFromConfig(cfg),
		cfgSet:    cfgSet,
		unsubBase: unsubBase,
	}, nil
}

// SendEmail 逐收件人 SES v2 send_email（渲染变量 → 内联 HTML → 发送）
func (p *SESMailProvider) SendEmail(ctx context.Context, task SendTask) (string, error) {
	name := task.Name
	if name == "" {
		name = "Customer"
	}
	subject := renderVars(task.SubjectTpl, task.Recipient, name, task.UnsubURL, task.Attrs)
	html := renderVars(task.HTMLTpl, task.Recipient, name, task.UnsubURL, task.Attrs)
	from := formatFrom(task.FromName, task.SourceEmail)

	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(from),
		Destination: &types.Destination{
			ToAddresses: []string{task.Recipient},
		},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{
					Data: aws.String(subject), Charset: aws.String("UTF-8"),
				},
				Body: &types.Body{
					Html: &types.Content{
						Data: aws.String(html), Charset: aws.String("UTF-8"),
					},
				},
			},
		},
		ReplyToAddresses: []string{task.ReplyTo},
	}

	if p.cfgSet != "" {
		input.ConfigurationSetName = aws.String(p.cfgSet)
	}
	if task.BatchID != "" {
		input.EmailTags = []types.MessageTag{
			{Name: aws.String("batch_id"), Value: aws.String(task.BatchID)},
		}
	}

	out, err := p.client.SendEmail(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.MessageId), nil
}

// GetQuota SES 配额（固定值——启动后不刷新，Python 语义）
func (p *SESMailProvider) GetQuota() int {
	return 14
}

// renderVars 模板变量替换
func renderVars(tpl, email, name, unsubURL string, attrs map[string]string) string {
	if tpl == "" {
		return ""
	}
	r := strings.ReplaceAll(tpl, "{{name}}", name)
	r = strings.ReplaceAll(r, "{{email}}", email)
	if unsubURL != "" {
		r = strings.ReplaceAll(r, "{{unsubscribe_url}}", unsubURL)
	} else {
		r = strings.ReplaceAll(r, "{{unsubscribe_url}}", "#")
	}
	for k, v := range attrs {
		r = strings.ReplaceAll(r, "{{"+k+"}}", v)
	}
	return r
}

// formatFrom 发件人格式化
func formatFrom(name, email string) string {
	if name == "" {
		return email
	}
	return "\"" + name + "\" <" + email + ">"
}
