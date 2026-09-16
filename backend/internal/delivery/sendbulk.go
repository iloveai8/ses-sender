package delivery

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
)

// SendBulkResult POST /send-bulk 响应（字段顺序=契约）
type SendBulkResult struct {
	Status              string `json:"status"`
	BatchID             string `json:"batch_id"`
	Source              string `json:"source"`
	TotalContacts       int    `json:"total_contacts"`
	ActiveContacts      int    `json:"active_contacts"`
	SkippedUnsubscribed int    `json:"skipped_unsubscribed"`
	Message             string `json:"message"`
}

// SendBulkParams 建单参数
type SendBulkParams struct {
	UserID      int
	SourceEmail string
	TemplateID  int
	GroupID     int
}

// Sender 依赖面（依赖注入——campaign 域提供实现；delivery 不 import campaign 防环）
type Sender interface {
	// QuotaCheck 每日配额校验：返回 429 detail（空=通过）
	QuotaCheck(ctx context.Context, userID, contactCount int) string
	// TplCheck 模板归属：返回名称 + 404 detail（空=通过）
	TplCheck(ctx context.Context, userID, templateID int) (string, string)
	// GroupCheck 客群归属：返回名称 + 404/空客群 detail（空=通过）
	GroupCheck(ctx context.Context, userID, groupID int) (string, string)
	// Contacts 枚举客群联系人
	Contacts(ctx context.Context, groupID int) []ContactView
	// UserDailyLimit 用户日配额
	UserDailyLimit(ctx context.Context, userID int) int
}

// ContactView 联系人视图（delivery 只关心这三个字段）
type ContactView struct {
	Email      string
	Name       string
	Attributes map[string]string
}

// SendBulk 建单（四过滤：退订→去重→[黑名单在引擎发送瞬间拦截]→配额；需求 D1/D2）
// 引擎在跑则只写 queued 由 Scanner 拾取；无引擎时留 queued（不做旧版后台线程——Go 版简化，Python 版双路径不再保留）
func SendBulk(ctx context.Context, db *sql.DB, s Sender, p SendBulkParams) (*SendBulkResult, string) {
	if p.SourceEmail == "" {
		return nil, "您尚未配置发送邮箱，请联系管理员"
	}
	tplName, err404 := s.TplCheck(ctx, p.UserID, p.TemplateID)
	if err404 != "" {
		return nil, err404
	}
	grpName, err404 := s.GroupCheck(ctx, p.UserID, p.GroupID)
	if err404 != "" {
		return nil, err404
	}
	contacts := s.Contacts(ctx, p.GroupID)
	if len(contacts) == 0 {
		return nil, "客群中没有联系人"
	}
	quotaErr := s.QuotaCheck(ctx, p.UserID, len(contacts))
	if quotaErr != "" {
		return nil, quotaErr
	}

	// 退订过滤（按 source_email 全量取集合）
	unsub := map[string]struct{}{}
	rows, err := db.QueryContext(ctx,
		"SELECT LOWER(email) FROM unsubscribe_list WHERE source_email = ?", p.SourceEmail)
	if err == nil {
		for rows.Next() {
			var e string
			if rows.Scan(&e) == nil {
				unsub[e] = struct{}{}
			}
		}
		rows.Close()
	}

	// 同邮箱去重 + 退订过滤
	seen := map[string]struct{}{}
	var active, skipped []ContactView
	for _, c := range contacts {
		if _, ok := unsub[c.Email]; ok {
			skipped = append(skipped, c)
			continue
		}
		if _, ok := seen[c.Email]; ok {
			continue
		}
		seen[c.Email] = struct{}{}
		active = append(active, c)
	}

	batchID := genBatchID()
	total := len(active) + len(skipped)

	// 单事务：批次 + 明细
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err.Error()
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO sending_jobs
		(user_id, batch_id, template_name, template_id, group_name, group_id, source_email,
		 total_contacts, sent_count, total_batches, status, configuration_set, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,0,'queued','',UTC_TIMESTAMP())`,
		p.UserID, batchID, tplName, p.TemplateID, grpName, p.GroupID, p.SourceEmail,
		total, 0)
	if err != nil {
		return nil, err.Error()
	}
	jobID, _ := res.LastInsertId()
	for _, c := range active {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO sending_job_details (job_id, batch_id, recipient, send_status, created_at) VALUES (?,?,?,'Pending',UTC_TIMESTAMP())",
			jobID, batchID, c.Email); err != nil {
			return nil, err.Error()
		}
	}
	for _, c := range skipped {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO sending_job_details (job_id, batch_id, recipient, send_status, created_at) VALUES (?,?,?,'Unsubscribed',UTC_TIMESTAMP())",
			jobID, batchID, c.Email); err != nil {
			return nil, err.Error()
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err.Error()
	}
	slog.Info("[SendBulk] 建单", "batch", batchID, "total", total, "active", len(active), "skipped", len(skipped))

	return &SendBulkResult{
		Status: "queued", BatchID: batchID, Source: p.SourceEmail,
		TotalContacts: total, ActiveContacts: len(active), SkippedUnsubscribed: len(skipped),
		Message: "发送任务已创建，正在后台执行",
	}, ""
}

// genBatchID batch-{12hex}
func genBatchID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "batch-" + hex.EncodeToString(b)
}
