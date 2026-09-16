// Package awsx AWS 服务客户端封装（SES 模板操作先行；发信/事件随域接入）。
package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// SESTemplates SES v2 模板操作（模板 CRUD 双写用；错误文案由调用方包装为契约格式）
type SESTemplates struct{ client *sesv2.Client }

// NewSESTemplates 凭证走默认链（本地 ~/.aws / 云端任务角色），不发网络请求
func NewSESTemplates(ctx context.Context, region string) (*SESTemplates, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("加载 AWS 配置: %w", err)
	}
	return &SESTemplates{client: sesv2.NewFromConfig(cfg)}, nil
}

// Create 建模板（与 Python 参数一致：Subject/Html/Text）
func (s *SESTemplates) Create(ctx context.Context, sesName, subject, html, text string) error {
	_, err := s.client.CreateEmailTemplate(ctx, &sesv2.CreateEmailTemplateInput{
		TemplateName: aws.String(sesName),
		TemplateContent: &types.EmailTemplateContent{
			Subject: aws.String(subject), Html: aws.String(html), Text: aws.String(text),
		},
	})
	return err
}

// Update 更新模板
func (s *SESTemplates) Update(ctx context.Context, sesName, subject, html, text string) error {
	_, err := s.client.UpdateEmailTemplate(ctx, &sesv2.UpdateEmailTemplateInput{
		TemplateName: aws.String(sesName),
		TemplateContent: &types.EmailTemplateContent{
			Subject: aws.String(subject), Html: aws.String(html), Text: aws.String(text),
		},
	})
	return err
}

// Delete 删模板（调用方 best-effort：失败不阻断本地删除——Python 语义）
func (s *SESTemplates) Delete(ctx context.Context, sesName string) error {
	_, err := s.client.DeleteEmailTemplate(ctx, &sesv2.DeleteEmailTemplateInput{
		TemplateName: aws.String(sesName),
	})
	return err
}
