package delivery

import (
	"context"
	"database/sql"

	"ses-sender/internal/campaign"
)

// dbConn 数据库连接接口（*sql.DB 满足）
type dbConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// CampaignSender campaign 域的 Sender 实现（delivery 不 import campaign 的编译期依赖
// 通过接口注入反转——这里是装配桥，放 delivery 包内避免环）
type CampaignSender struct {
	store *campaign.Store
}

func NewCampaignSender(store *campaign.Store) *CampaignSender {
	return &CampaignSender{store: store}
}

func (s *CampaignSender) QuotaCheck(ctx context.Context, userID, contactCount int) string {
	// Python 口径：remaining<=0 → 429 配额已用完；contacts>remaining → 429 超限
	limitN, sent, _ := s.store.TodayQuotaRaw(ctx, userID)
	remaining := limitN - sent
	if remaining <= 0 {
		return "今日发送配额已用完（限额 " + itoa(limitN) + " 封），请明天再试"
	}
	if contactCount > remaining {
		return "今日剩余配额 " + itoa(remaining) + " 封（限额 " + itoa(limitN) + "，已用 " + itoa(sent) +
			"），该客群有 " + itoa(contactCount) + " 个联系人，超出配额"
	}
	return ""
}

func (s *CampaignSender) TplCheck(ctx context.Context, userID, templateID int) (string, string) {
	name, ok := s.store.TplName(ctx, userID, templateID)
	if !ok {
		return "", "模版不存在"
	}
	return name, ""
}

func (s *CampaignSender) GroupCheck(ctx context.Context, userID, groupID int) (string, string) {
	name, ok := s.store.GroupName(ctx, userID, groupID)
	if !ok {
		return "", "客群不存在或无权操作"
	}
	return name, ""
}

func (s *CampaignSender) Contacts(ctx context.Context, groupID int) []ContactView {
	rows := s.store.ContactsRaw(groupID)
	out := make([]ContactView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ContactView{Email: r.Email, Name: r.Name, Attributes: r.Attrs})
	}
	return out
}

func (s *CampaignSender) UserDailyLimit(ctx context.Context, userID int) int {
	return s.store.UserDailyLimit(ctx, userID)
}

func itoa(n int) string {
	return strconvItoa(n)
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
