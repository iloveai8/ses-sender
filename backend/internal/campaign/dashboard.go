package campaign

import (
	"context"
	"database/sql"
	"time"

	"ses-sender/internal/httpx"
)

// ── 用户 dashboard（需求 D8；口径全 UTC，字段顺序=golden 契约）──

// DashboardSummary 概览（键序=契约）
type DashboardSummary struct {
	TotalJobs      int `json:"total_jobs"`
	TotalEmails    int `json:"total_emails"`
	TodaySent      int `json:"today_sent"`
	MonthSent      int `json:"month_sent"`
	SuccessJobs    int `json:"success_jobs"`
	FailedJobs     int `json:"failed_jobs"`
	DailyLimit     int `json:"daily_limit"`
	DailyRemaining int `json:"daily_remaining"`
}

// DashboardDelivery 回执聚合（键序=契约）
type DashboardDelivery struct {
	Total            int  `json:"total"`
	Delivered        int  `json:"delivered"`
	Bounced          int  `json:"bounced"`
	Opened           int  `json:"opened"`
	Clicked          int  `json:"clicked"`
	Complained       int  `json:"complained"`
	ReceiptAvailable bool `json:"receipt_available"`
}

// TrendItem 近 7 天趋势（键序=契约 date,count）
type TrendItem struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// RecentJob 最近批次（键序=契约）
type RecentJob struct {
	BatchID       string  `json:"batch_id"`
	TemplateName  string  `json:"template_name"`
	GroupName     string  `json:"group_name"`
	TotalContacts int     `json:"total_contacts"`
	Status        string  `json:"status"`
	CreatedAt     *string `json:"created_at"`
}

// Dashboard 完整响应（键序=契约 summary,delivery,daily_trend,recent_jobs）
type Dashboard struct {
	Summary    DashboardSummary  `json:"summary"`
	Delivery   DashboardDelivery `json:"delivery"`
	DailyTrend []TrendItem       `json:"daily_trend"`
	RecentJobs []RecentJob       `json:"recent_jobs"`
}

// GetDashboard 用户看板（口径=Python get_user_dashboard：日/月界 UTC）
func (s *Store) GetDashboard(ctx context.Context, userID, dailyLimit int) (*Dashboard, error) {
	d := &Dashboard{}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(total_contacts),0),
		COALESCE(SUM(status='success'),0), COALESCE(SUM(status='failed'),0)
		FROM sending_jobs WHERE user_id = ?`, userID).
		Scan(&d.Summary.TotalJobs, &d.Summary.TotalEmails, &d.Summary.SuccessJobs, &d.Summary.FailedJobs); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	todayStart := now.Format("2006-01-02")
	monthStart := now.Format("2006-01") + "-01"
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(total_contacts),0) FROM sending_jobs WHERE user_id = ? AND created_at >= ?`,
		userID, todayStart).Scan(&d.Summary.TodaySent); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(total_contacts),0) FROM sending_jobs WHERE user_id = ? AND created_at >= ?`,
		userID, monthStart).Scan(&d.Summary.MonthSent); err != nil {
		return nil, err
	}
	d.Summary.DailyLimit = dailyLimit
	d.Summary.DailyRemaining = dailyLimit - d.Summary.TodaySent
	if d.Summary.DailyRemaining < 0 {
		d.Summary.DailyRemaining = 0
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(dd.delivery_status='Delivery'),0), COALESCE(SUM(dd.delivery_status='Bounce'),0),
		COALESCE(SUM(dd.open_count>0),0), COALESCE(SUM(dd.click_count>0),0), COALESCE(SUM(dd.complaint_time IS NOT NULL),0)
		FROM sending_job_details dd WHERE dd.batch_id IN (SELECT batch_id FROM sending_jobs WHERE user_id = ?)`, userID).
		Scan(&d.Delivery.Total, &d.Delivery.Delivered, &d.Delivery.Bounced,
			&d.Delivery.Opened, &d.Delivery.Clicked, &d.Delivery.Complained); err != nil {
		return nil, err
	}
	d.Delivery.ReceiptAvailable = d.Delivery.Delivered+d.Delivery.Bounced+
		d.Delivery.Complained+d.Delivery.Opened+d.Delivery.Clicked > 0
	d.DailyTrend = []TrendItem{}
	for i := 6; i >= 0; i-- {
		day := now.AddDate(0, 0, -i)
		var n int
		_ = s.db.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(total_contacts),0) FROM sending_jobs WHERE user_id = ? AND created_at >= ? AND created_at < ?`,
			userID, day.Format("2006-01-02"), day.AddDate(0, 0, 1).Format("2006-01-02")).Scan(&n)
		d.DailyTrend = append(d.DailyTrend, TrendItem{Date: day.Format("01-02"), Count: n})
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT batch_id, template_name, group_name, total_contacts, status, created_at
		 FROM sending_jobs WHERE user_id = ? ORDER BY id DESC LIMIT 5`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.RecentJobs = []RecentJob{}
	for rows.Next() {
		var r RecentJob
		var created sql.NullTime
		if err := rows.Scan(&r.BatchID, &r.TemplateName, &r.GroupName, &r.TotalContacts, &r.Status, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = nullTime(created)
		d.RecentJobs = append(d.RecentJobs, r)
	}
	return d, rows.Err()
}

// ── 管理员全局统计（需求 D9；success_rate=job 数级比率，用户按量降序）──

// AdminStatsSummary 全局概览（键序=契约）
type AdminStatsSummary struct {
	TotalUsers    int           `json:"total_users"`
	TotalJobs     int           `json:"total_jobs"`
	TotalContacts int           `json:"total_contacts"`
	SuccessRate   httpx.PyFloat `json:"success_rate"`
}

// AdminStatsUser 用户行（键序=契约）
type AdminStatsUser struct {
	UserID        int     `json:"user_id"`
	Username      string  `json:"username"`
	DisplayName   string  `json:"display_name"`
	SenderName    *string `json:"sender_name"`
	Email         string  `json:"email"`
	TotalJobs     int     `json:"total_jobs"`
	TotalContacts int     `json:"total_contacts"`
	SuccessCount  int     `json:"success_count"`
	FailedCount   int     `json:"failed_count"`
	FirstSend     *string `json:"first_send"`
	LastSend      *string `json:"last_send"`
}

// AdminStats 完整响应
type AdminStats struct {
	Summary AdminStatsSummary `json:"summary"`
	Users   []AdminStatsUser  `json:"users"`
}

// GetAdminStats 全局统计
func (s *Store) GetAdminStats(ctx context.Context) (*AdminStats, error) {
	out := &AdminStats{}
	var successJobs int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT user_id), COUNT(*),
		COALESCE(SUM(total_contacts),0), COALESCE(SUM(status='success'),0) FROM sending_jobs`).
		Scan(&out.Summary.TotalUsers, &out.Summary.TotalJobs, &out.Summary.TotalContacts, &successJobs); err != nil {
		return nil, err
	}
	rate := 0.0
	if out.Summary.TotalJobs > 0 {
		rate = float64(successJobs) / float64(out.Summary.TotalJobs) * 100
	}
	out.Summary.SuccessRate = httpx.Round1(rate)
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.username, u.display_name, u.sender_name, COALESCE(u.email,''),
		COUNT(j.id), COALESCE(SUM(j.total_contacts),0),
		COALESCE(SUM(j.status='success'),0), COALESCE(SUM(j.status='failed'),0),
		MIN(j.created_at), MAX(j.created_at)
		FROM users u LEFT JOIN sending_jobs j ON j.user_id = u.id
		GROUP BY u.id, u.username, u.display_name, u.sender_name, u.email
		ORDER BY COALESCE(SUM(j.total_contacts),0) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out.Users = []AdminStatsUser{}
	for rows.Next() {
		var u AdminStatsUser
		var sender sql.NullString
		var first, last sql.NullTime
		if err := rows.Scan(&u.UserID, &u.Username, &u.DisplayName, &sender, &u.Email,
			&u.TotalJobs, &u.TotalContacts, &u.SuccessCount, &u.FailedCount, &first, &last); err != nil {
			return nil, err
		}
		u.SenderName = nullStr(sender)
		u.FirstSend, u.LastSend = nullTime(first), nullTime(last)
		out.Users = append(out.Users, u)
	}
	return out, rows.Err()
}

// AdminJobRow 管理员批次列表行（键序=契约）
type AdminJobRow struct {
	ID               int     `json:"id"`
	BatchID          string  `json:"batch_id"`
	Username         string  `json:"username"`
	DisplayName      string  `json:"display_name"`
	TemplateName     string  `json:"template_name"`
	GroupName        string  `json:"group_name"`
	SourceEmail      string  `json:"source_email"`
	TotalContacts    int     `json:"total_contacts"`
	Status           string  `json:"status"`
	ConfigurationSet *string `json:"configuration_set"`
	CreatedAt        *string `json:"created_at"`
}

// ListAdminJobs 全部批次分页（含用户名，id 倒序）
func (s *Store) ListAdminJobs(ctx context.Context, page, pageSize int) ([]AdminJobRow, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sending_jobs").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT j.id, j.batch_id, COALESCE(u.username,''), COALESCE(u.display_name,''),
		j.template_name, j.group_name, j.source_email, j.total_contacts, j.status, j.configuration_set, j.created_at
		FROM sending_jobs j LEFT JOIN users u ON u.id = j.user_id
		ORDER BY j.id DESC LIMIT ? OFFSET ?`, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AdminJobRow{}
	for rows.Next() {
		var r AdminJobRow
		var cfgSet sql.NullString
		var created sql.NullTime
		if err := rows.Scan(&r.ID, &r.BatchID, &r.Username, &r.DisplayName, &r.TemplateName, &r.GroupName,
			&r.SourceEmail, &r.TotalContacts, &r.Status, &cfgSet, &created); err != nil {
			return nil, 0, err
		}
		r.ConfigurationSet = nullStr(cfgSet)
		r.CreatedAt = nullTime(created)
		out = append(out, r)
	}
	return out, total, rows.Err()
}
