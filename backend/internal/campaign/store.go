// Package campaign 发送营销域：批次/明细读侧、指标统计、定时任务、退订管理。
package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"ses-sender/internal/httpx"
)

// Store campaign 域数据访问
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ── 批次与明细 ──

// JobOut 批次对外形状（字段顺序=Python SendingJobOut 契约，禁改）
type JobOut struct {
	ID               int     `json:"id"`
	BatchID          string  `json:"batch_id"`
	TemplateName     string  `json:"template_name"`
	GroupName        string  `json:"group_name"`
	SourceEmail      string  `json:"source_email"`
	ReplyTo          *string `json:"reply_to"`
	TotalContacts    int     `json:"total_contacts"`
	SentCount        int     `json:"sent_count"`
	TotalBatches     int     `json:"total_batches"`
	Status           string  `json:"status"`
	ErrorMessage     *string `json:"error_message"`
	ConfigurationSet *string `json:"configuration_set"`
	CreatedAt        *string `json:"created_at"`
	FinishedAt       *string `json:"finished_at"`
}

const isoFmt = "2006-01-02T15:04:05"

func nullTime(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(isoFmt)
	return &s
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

const jobCols = `id, batch_id, template_name, group_name, source_email, reply_to,
	total_contacts, sent_count, total_batches, status, error_message, configuration_set, created_at, finished_at`

func scanJob(sc interface{ Scan(...any) error }) (*JobOut, error) {
	var j JobOut
	var replyTo, errMsg, cfgSet sql.NullString
	var created, finished sql.NullTime
	if err := sc.Scan(&j.ID, &j.BatchID, &j.TemplateName, &j.GroupName, &j.SourceEmail, &replyTo,
		&j.TotalContacts, &j.SentCount, &j.TotalBatches, &j.Status, &errMsg, &cfgSet, &created, &finished); err != nil {
		return nil, err
	}
	j.ReplyTo, j.ErrorMessage, j.ConfigurationSet = nullStr(replyTo), nullStr(errMsg), nullStr(cfgSet)
	j.CreatedAt, j.FinishedAt = nullTime(created), nullTime(finished)
	return &j, nil
}

// ListJobs 用户批次分页（id 倒序）
func (s *Store) ListJobs(ctx context.Context, userID, page, pageSize int) ([]JobOut, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sending_jobs WHERE user_id = ?", userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+jobCols+" FROM sending_jobs WHERE user_id = ? ORDER BY id DESC LIMIT ? OFFSET ?",
		userID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []JobOut{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *j)
	}
	return out, total, rows.Err()
}

// GetJobByBatch 按 batch_id 取（admin 可跨用户）
func (s *Store) GetJobByBatch(ctx context.Context, batchID string, restrictUserID int) (*JobOut, error) {
	q := "SELECT " + jobCols + " FROM sending_jobs WHERE batch_id = ?"
	args := []any{batchID}
	if restrictUserID > 0 {
		q += " AND user_id = ?"
		args = append(args, restrictUserID)
	}
	j, err := scanJob(s.db.QueryRowContext(ctx, q, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// DetailOut 明细对外形状（字段顺序=Python SendingJobDetailOut 契约，禁改）
type DetailOut struct {
	ID             int     `json:"id"`
	BatchID        *string `json:"batch_id"`
	MessageID      *string `json:"message_id"`
	Recipient      string  `json:"recipient"`
	SendStatus     string  `json:"send_status"`
	SendError      *string `json:"send_error"`
	DeliveryStatus *string `json:"delivery_status"`
	DeliveryTime   *string `json:"delivery_time"`
	BounceType     *string `json:"bounce_type"`
	BounceSubtype  *string `json:"bounce_subtype"`
	BounceMessage  *string `json:"bounce_message"`
	OpenCount      int     `json:"open_count"`
	FirstOpenTime  *string `json:"first_open_time"`
	ClickCount     int     `json:"click_count"`
	FirstClickTime *string `json:"first_click_time"`
	ComplaintTime  *string `json:"complaint_time"`
	CreatedAt      *string `json:"created_at"`
}

const detailCols = `id, batch_id, message_id, recipient, send_status, send_error, delivery_status,
	delivery_time, bounce_type, bounce_subtype, bounce_message, open_count, first_open_time,
	click_count, first_click_time, complaint_time, created_at`

func scanDetail(sc interface{ Scan(...any) error }) (*DetailOut, error) {
	var d DetailOut
	var batch, msgID, sendErr, dStatus, bType, bSub, bMsg sql.NullString
	var dTime, fOpenT, fClickT, cTime, created sql.NullTime
	if err := sc.Scan(&d.ID, &batch, &msgID, &d.Recipient, &d.SendStatus, &sendErr, &dStatus,
		&dTime, &bType, &bSub, &bMsg, &d.OpenCount, &fOpenT, &d.ClickCount, &fClickT, &cTime, &created); err != nil {
		return nil, err
	}
	d.BatchID, d.MessageID, d.SendError = nullStr(batch), nullStr(msgID), nullStr(sendErr)
	d.DeliveryStatus, d.BounceType, d.BounceSubtype, d.BounceMessage = nullStr(dStatus), nullStr(bType), nullStr(bSub), nullStr(bMsg)
	d.DeliveryTime, d.FirstOpenTime, d.FirstClickTime, d.ComplaintTime, d.CreatedAt =
		nullTime(dTime), nullTime(fOpenT), nullTime(fClickT), nullTime(cTime), nullTime(created)
	return &d, nil
}

// ListDetailsByBatch 批次全量明细（id 升序）
func (s *Store) ListDetailsByBatch(ctx context.Context, batchID string) ([]DetailOut, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+detailCols+" FROM sending_job_details WHERE batch_id = ? ORDER BY id", batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DetailOut{}
	for rows.Next() {
		d, err := scanDetail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// EmailDetailRow 明细 + 批次信息（/email-details 行形状）
type EmailDetailRow struct {
	DetailOut
	TemplateName string `json:"template_name"`
	GroupName    string `json:"group_name"`
	SourceEmail  string `json:"source_email"`
}

// EmailDetailFilter /email-details 过滤器（空串=不过滤）
type EmailDetailFilter struct {
	Recipient, BatchID, SendStatus, DeliveryStatus string
}

// ListEmailDetails 分页明细（admin 全量；非 admin 限本人批次；id 倒序）
func (s *Store) ListEmailDetails(ctx context.Context, userID int, admin bool, f EmailDetailFilter, page, pageSize int) ([]EmailDetailRow, int, error) {
	where, args := []string{"1=1"}, []any{}
	if !admin {
		where = append(where, "j.user_id = ?")
		args = append(args, userID)
	}
	if f.Recipient != "" {
		where = append(where, "d.recipient LIKE ?")
		args = append(args, "%"+f.Recipient+"%")
	}
	if f.BatchID != "" {
		where = append(where, "d.batch_id = ?")
		args = append(args, f.BatchID)
	}
	if f.SendStatus != "" {
		where = append(where, "d.send_status = ?")
		args = append(args, f.SendStatus)
	}
	if f.DeliveryStatus != "" {
		where = append(where, "d.delivery_status = ?")
		args = append(args, f.DeliveryStatus)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sending_job_details d JOIN sending_jobs j ON j.batch_id = d.batch_id WHERE "+w, args...).
		Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.batch_id, d.message_id, d.recipient, d.send_status, d.send_error, d.delivery_status,
		   d.delivery_time, d.bounce_type, d.bounce_subtype, d.bounce_message, d.open_count, d.first_open_time,
		   d.click_count, d.first_click_time, d.complaint_time, d.created_at,
		   j.template_name, j.group_name, j.source_email
		 FROM sending_job_details d JOIN sending_jobs j ON j.batch_id = d.batch_id
		 WHERE `+w+` ORDER BY d.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []EmailDetailRow{}
	for rows.Next() {
		var r EmailDetailRow
		var batch, msgID, sendErr, dStatus, bType, bSub, bMsg sql.NullString
		var dTime, fOpenT, fClickT, cTime, created sql.NullTime
		if err := rows.Scan(&r.ID, &batch, &msgID, &r.Recipient, &r.SendStatus, &sendErr, &dStatus,
			&dTime, &bType, &bSub, &bMsg, &r.OpenCount, &fOpenT, &r.ClickCount, &fClickT, &cTime, &created,
			&r.TemplateName, &r.GroupName, &r.SourceEmail); err != nil {
			return nil, 0, err
		}
		r.BatchID, r.MessageID, r.SendError = nullStr(batch), nullStr(msgID), nullStr(sendErr)
		r.DeliveryStatus, r.BounceType, r.BounceSubtype, r.BounceMessage = nullStr(dStatus), nullStr(bType), nullStr(bSub), nullStr(bMsg)
		r.DeliveryTime, r.FirstOpenTime, r.FirstClickTime, r.ComplaintTime, r.CreatedAt =
			nullTime(dTime), nullTime(fOpenT), nullTime(fClickT), nullTime(cTime), nullTime(created)
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ── 指标统计 ──

// Metrics 批次指标（字段顺序=golden 契约，禁改）
type Metrics struct {
	Send             int           `json:"send"`
	Delivery         int           `json:"delivery"`
	Bounce           int           `json:"bounce"`
	Complaint        int           `json:"complaint"`
	Open             int           `json:"open"`
	Click            int           `json:"click"`
	Reject           int           `json:"reject"`
	DeliveryRate     httpx.PyFloat `json:"delivery_rate"`
	OpenRate         httpx.PyFloat `json:"open_rate"`
	BounceRate       httpx.PyFloat `json:"bounce_rate"`
	ReceiptAvailable bool          `json:"receipt_available"`
}

// GetBatchMetrics 批次指标（SQL COUNT 聚合，口径=Python：send=Success 行数等）
func (s *Store) GetBatchMetrics(ctx context.Context, batchID string) (*Metrics, bool, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sending_jobs WHERE batch_id = ?", batchID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if exists == 0 {
		return nil, false, nil
	}
	m := &Metrics{}
	row := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(send_status='Success'),0), COALESCE(SUM(delivery_status='Delivery'),0),
		COALESCE(SUM(delivery_status='Bounce'),0), COALESCE(SUM(complaint_time IS NOT NULL),0),
		COALESCE(SUM(open_count>0),0), COALESCE(SUM(click_count>0),0), COALESCE(SUM(delivery_status='Reject'),0)
		FROM sending_job_details WHERE batch_id = ?`, batchID)
	if err := row.Scan(&m.Send, &m.Delivery, &m.Bounce, &m.Complaint, &m.Open, &m.Click, &m.Reject); err != nil {
		return nil, false, err
	}
	return m, true, nil
}

// TodayQuota 日配额（口径=Python：SUM(total_contacts) >= UTC 零点）
func (s *Store) TodayQuota(ctx context.Context, userID, dailyLimit int) (sent, limit, remaining int) {
	limit = dailyLimit
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(total_contacts),0) FROM sending_jobs
		 WHERE user_id = ? AND created_at >= DATE(UTC_TIMESTAMP())`, userID).Scan(&sent); err != nil {
		sent = 0
	}
	remaining = limit - sent
	if remaining < 0 {
		remaining = 0
	}
	return
}

// AllTodayQuota 管理员：全员今日配额 map（user_id 字符串 → 已发数）
func (s *Store) AllTodayQuota(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, COALESCE(SUM(total_contacts),0) FROM sending_jobs
		 WHERE created_at >= DATE(UTC_TIMESTAMP()) GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var uid, n int
		if err := rows.Scan(&uid, &n); err != nil {
			return nil, err
		}
		out[strconv.Itoa(uid)] = n
	}
	return out, rows.Err()
}

// ── 供 delivery 域 Sender 适配器调用的原始查询 ──

// TodayQuotaRaw 配额三元组（limit, today_sent, remaining）
func (s *Store) TodayQuotaRaw(ctx context.Context, userID int) (int, int, int) {
	var limit sql.NullInt64
	var sent int
	_ = s.db.QueryRowContext(ctx, "SELECT daily_send_limit FROM users WHERE id = ?", userID).Scan(&limit)
	l := 1000
	if limit.Valid && limit.Int64 > 0 {
		l = int(limit.Int64)
	}
	_ = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(total_contacts),0) FROM sending_jobs
		 WHERE user_id = ? AND created_at >= DATE(UTC_TIMESTAMP())`, userID).Scan(&sent)
	rem := l - sent
	if rem < 0 {
		rem = 0
	}
	return l, sent, rem
}

// ContactRaw 联系人原始行
type ContactRaw struct {
	Email string
	Name  string
	Attrs map[string]string
}

// ContactsRaw 按客群枚举联系人（含 JSON 属性解析）
func (s *Store) ContactsRaw(groupID int) []ContactRaw {
	rows, err := s.db.Query(
		"SELECT email, COALESCE(name,''), COALESCE(attributes,'') FROM contacts WHERE group_id = ?", groupID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ContactRaw
	for rows.Next() {
		var email, name, attrsJSON string
		if rows.Scan(&email, &name, &attrsJSON) != nil {
			continue
		}
		attrs := map[string]string{}
		if attrsJSON != "" {
			_ = json.Unmarshal([]byte(attrsJSON), &attrs)
		}
		out = append(out, ContactRaw{Email: email, Name: name, Attrs: attrs})
	}
	return out
}

// UserDailyLimit 用户日配额（空/0 → 1000）
func (s *Store) UserDailyLimit(ctx context.Context, userID int) int {
	var limit sql.NullInt64
	_ = s.db.QueryRowContext(ctx, "SELECT daily_send_limit FROM users WHERE id = ?", userID).Scan(&limit)
	if limit.Valid && limit.Int64 > 0 {
		return int(limit.Int64)
	}
	return 1000
}
