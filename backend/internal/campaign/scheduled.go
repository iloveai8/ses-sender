package campaign

import (
	"context"
	"database/sql"
	"time"
)

// ScheduledJobOut 定时任务对外形状（字段顺序=Python ScheduledJobOut 契约，禁改）
type ScheduledJobOut struct {
	ID            int     `json:"id"`
	UserID        int     `json:"user_id"`
	TemplateID    int     `json:"template_id"`
	GroupID       int     `json:"group_id"`
	TemplateName  string  `json:"template_name"`
	GroupName     string  `json:"group_name"`
	ScheduleType  string  `json:"schedule_type"`
	ScheduledTime *string `json:"scheduled_time"`
	CronHour      int     `json:"cron_hour"`
	CronMinute    int     `json:"cron_minute"`
	DayOfWeek     *int    `json:"day_of_week"`
	DayOfMonth    *int    `json:"day_of_month"`
	Status        string  `json:"status"`
	NextRunAt     *string `json:"next_run_at"`
	LastRunAt     *string `json:"last_run_at"`
	RunCount      int     `json:"run_count"`
	LastBatchID   *string `json:"last_batch_id"`
	ErrorMessage  *string `json:"error_message"`
	CreatedAt     *string `json:"created_at"`
}

const schedCols = `id, user_id, template_id, group_id, template_name, group_name, schedule_type,
	scheduled_time, cron_hour, cron_minute, day_of_week, day_of_month, status, next_run_at,
	last_run_at, run_count, last_batch_id, error_message, created_at`

func scanSched(sc interface{ Scan(...any) error }) (*ScheduledJobOut, error) {
	var j ScheduledJobOut
	var schedTime sql.NullTime
	var dow, dom sql.NullInt64
	var nextRun, lastRun sql.NullTime
	var lastBatch, errMsg sql.NullString
	var created sql.NullTime
	if err := sc.Scan(&j.ID, &j.UserID, &j.TemplateID, &j.GroupID, &j.TemplateName, &j.GroupName,
		&j.ScheduleType, &schedTime, &j.CronHour, &j.CronMinute, &dow, &dom, &j.Status,
		&nextRun, &lastRun, &j.RunCount, &lastBatch, &errMsg, &created); err != nil {
		return nil, err
	}
	j.ScheduledTime = nullTime(schedTime)
	if dow.Valid {
		v := int(dow.Int64)
		j.DayOfWeek = &v
	}
	if dom.Valid {
		v := int(dom.Int64)
		j.DayOfMonth = &v
	}
	j.NextRunAt, j.LastRunAt = nullTime(nextRun), nullTime(lastRun)
	j.LastBatchID, j.ErrorMessage = nullStr(lastBatch), nullStr(errMsg)
	j.CreatedAt = nullTime(created)
	return &j, nil
}

// ListScheduled 用户定时任务（全量 list，非分页——Python 口径）
func (s *Store) ListScheduled(ctx context.Context, userID int) ([]ScheduledJobOut, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+schedCols+" FROM scheduled_jobs WHERE user_id = ? ORDER BY id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduledJobOut{}
	for rows.Next() {
		j, err := scanSched(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// CreateSchedParams 建定时任务参数
type CreateSchedParams struct {
	UserID                  int
	TemplateID, GroupID     int
	TemplateName, GroupName string
	ScheduleType            string
	ScheduledTime           time.Time
	CronHour, CronMinute    int
	DayOfWeek, DayOfMonth   *int
	NextRun                 *time.Time
}

// CreateScheduled 建定时任务（next_run 由调用方算好传入）
func (s *Store) CreateScheduled(ctx context.Context, p CreateSchedParams) (*ScheduledJobOut, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO scheduled_jobs
		(user_id, template_id, group_id, template_name, group_name, schedule_type, scheduled_time,
		 cron_hour, cron_minute, day_of_week, day_of_month, status, next_run_at, run_count, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?, 'active', ?, 0, ?, ?)`,
		p.UserID, p.TemplateID, p.GroupID, p.TemplateName, p.GroupName, p.ScheduleType, p.ScheduledTime,
		p.CronHour, p.CronMinute, p.DayOfWeek, p.DayOfMonth, p.NextRun, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	j, err := scanSched(s.db.QueryRowContext(ctx, "SELECT "+schedCols+" FROM scheduled_jobs WHERE id = ?", id))
	return j, err
}

// GetScheduled 按归属取
func (s *Store) GetScheduled(ctx context.Context, userID, id int) (*ScheduledJobOut, error) {
	j, err := scanSched(s.db.QueryRowContext(ctx,
		"SELECT "+schedCols+" FROM scheduled_jobs WHERE id = ? AND user_id = ?", id, userID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// DeleteScheduled 删除
func (s *Store) DeleteScheduled(ctx context.Context, userID, id int) (bool, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM scheduled_jobs WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateSchedFields 更新提供的字段（status 回 active 时重算 next_run 由调用方置入）
func (s *Store) UpdateSchedFields(ctx context.Context, userID, id int, sets map[string]any) (*ScheduledJobOut, error) {
	if len(sets) > 0 {
		cols := make([]string, 0, len(sets))
		args := make([]any, 0, len(sets)+3)
		for k, v := range sets {
			cols = append(cols, k+" = ?")
			args = append(args, v)
		}
		args = append(args, time.Now().UTC(), id, userID)
		if _, err := s.db.ExecContext(ctx,
			"UPDATE scheduled_jobs SET "+joinCols(cols)+", updated_at = ? WHERE id = ? AND user_id = ?", args...); err != nil {
			return nil, err
		}
	}
	return s.GetScheduled(ctx, userID, id)
}

func joinCols(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}
