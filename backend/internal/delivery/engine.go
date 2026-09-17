package delivery

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ── Sender Engine（06-ENGINE-DESIGN.md 规格）──

// Engine 发送引擎：Scanner goroutine → buffered channel → Worker 池
type Engine struct {
	db        *sql.DB
	provider  MailProvider
	bl        BlacklistChecker
	taskCh    chan SendTask
	quit      chan struct{}
	wg        sync.WaitGroup
	rate      int // 每 Worker 每秒上限
	workers   int
	secret    string // 退订 HMAC 密钥
	unsubBase string // 退订链接前缀（空=模板里替换为 #）
}

// MailProvider 渠道接口（第一步唯一实现 SES；P10 后多渠道在此扩展）
type MailProvider interface {
	SendEmail(ctx context.Context, task SendTask) (messageID string, err error)
	GetQuota() (maxRate int)
}

// BlacklistChecker 黑名单接口（system 域 BlacklistCache 实现此接口）
type BlacklistChecker interface {
	IsBlacklisted(email string) bool
}

// SendTask 单封发送任务（=Python SendTask 精简——引擎路径只发 HTML）
type SendTask struct {
	JobID       int
	BatchID     string
	DetailID    int
	Recipient   string
	Name        string
	SubjectTpl  string
	HTMLTpl     string
	SourceEmail string
	FromName    string
	ReplyTo     string
	UnsubURL    string
	ConfigSet   string
	Attrs       map[string]string
}

// NewEngine 构造引擎；rate<=0 时自动 floor(quota/workers) 下限 1
func NewEngine(db *sql.DB, p MailProvider, bl BlacklistChecker, workers, rate int, secret, unsubBase string) *Engine {
	if workers < 1 {
		workers = 2
	}
	if rate <= 0 {
		rate = 7 // 默认每 Worker 每秒 7 封（=配额14/2Worker）
	}
	return &Engine{
		db: db, provider: p, bl: bl,
		taskCh: make(chan SendTask, workers*rate*4),
		quit:   make(chan struct{}),
		rate:   rate, workers: workers,
		secret: secret, unsubBase: unsubBase,
	}
}

// Start 启动 Scanner + Worker 池
func (e *Engine) Start(ctx context.Context) {
	e.wg.Add(1)
	go e.scanner(ctx)
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker(ctx, i)
	}
	slog.Info("[Engine] 启动", "workers", e.workers, "rate", e.rate, "queue", cap(e.taskCh))
}

// Stop 优雅停机：停 Scanner → 排空 channel → 等 Worker
func (e *Engine) Stop() {
	close(e.quit)
	e.wg.Wait()
	slog.Info("[Engine] 已停机")
}

// scanner 每 5 秒一轮：自愈 → 取 ≤5 个 queued → 入队（队满本轮中止）
func (e *Engine) scanner(ctx context.Context) {
	defer e.wg.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.quit:
			return
		case <-t.C:
			e.healStuck(ctx)
			e.enqueueQueued(ctx)
		}
	}
}

// healStuck 自愈：Pending+有回执→Success；真 Pending=0→终态判定
func (e *Engine) healStuck(ctx context.Context) {
	_, _ = e.db.ExecContext(ctx,
		`UPDATE sending_job_details d JOIN sending_jobs j ON j.batch_id = d.batch_id
		 SET d.send_status = 'Success'
		 WHERE j.status = 'sending' AND d.send_status = 'Pending' AND d.delivery_status IS NOT NULL`)

	rows, err := e.db.QueryContext(ctx, `SELECT j.batch_id,
		SUM(d.send_status='Pending' AND d.delivery_status IS NULL) AS true_pending,
		SUM(d.send_status='Failed') AS failed,
		COUNT(d.id) AS total
		FROM sending_jobs j JOIN sending_job_details d ON d.batch_id = j.batch_id
		WHERE j.status = 'sending' GROUP BY j.batch_id`)
	if err != nil {
		return
	}
	type agg struct {
		batch   string
		pending int64
		failed  int64
		total   int64
	}
	var aggs []agg
	for rows.Next() {
		var a agg
		if rows.Scan(&a.batch, &a.pending, &a.failed, &a.total) == nil {
			aggs = append(aggs, a)
		}
	}
	rows.Close()
	for _, a := range aggs {
		if a.pending == 0 {
			status := "success"
			var errMsg any
			if a.failed == a.total && a.total > 0 {
				status = "failed"
			} else if a.failed > 0 {
				status = "partial"
				errMsg = fmt.Sprintf("%d 封发送失败", a.failed)
			}
			_, _ = e.db.ExecContext(ctx,
				`UPDATE sending_jobs SET status=?, error_message=?, sent_count=?,
				 finished_at=UTC_TIMESTAMP() WHERE batch_id=? AND status='sending'`,
				status, errMsg, a.total-a.failed, a.batch)
			slog.Info("[Scanner] 终态", "batch", a.batch, "status", status)
		}
	}
}

// enqueueQueued 取 queued 批次置 sending → 逐明细入队（队满本轮中止）
func (e *Engine) enqueueQueued(ctx context.Context) {
	rows, err := e.db.QueryContext(ctx,
		`SELECT batch_id FROM sending_jobs WHERE status='queued' ORDER BY id LIMIT 5`)
	if err != nil {
		return
	}
	var batches []string
	for rows.Next() {
		var b string
		if rows.Scan(&b) == nil {
			batches = append(batches, b)
		}
	}
	rows.Close()

	for _, batch := range batches {
		if !e.enqueueBatch(ctx, batch) {
			return // 队满：本轮中止
		}
	}
}

func (e *Engine) enqueueBatch(ctx context.Context, batch string) bool {
	_, _ = e.db.ExecContext(ctx,
		"UPDATE sending_jobs SET status='sending' WHERE batch_id=? AND status='queued'", batch)

	rows, err := e.db.QueryContext(ctx, `SELECT d.id, d.recipient, c.name,
		t.subject, t.html_body, j.source_email,
		COALESCE(NULLIF(j.from_name,''), NULLIF(u.sender_name,''), SUBSTRING_INDEX(j.source_email,'@',1)),
		COALESCE(NULLIF(u.contact_email,''), j.source_email), COALESCE(j.configuration_set,'')
		FROM sending_job_details d
		JOIN sending_jobs j ON j.batch_id = d.batch_id
		LEFT JOIN email_templates t ON t.id = j.template_id
		LEFT JOIN contacts c ON c.email = d.recipient AND c.group_id = j.group_id
		JOIN users u ON u.id = j.user_id
		WHERE d.batch_id = ? AND d.send_status = 'Pending'`, batch)
	if err != nil {
		return true
	}
	type det struct {
		detailID         int
		recipient, name  string
		subject, html    string
		src, from, reply string
		cfg              string
	}
	var dets []det
	for rows.Next() {
		var d det
		var name, subject, html sql.NullString
		if rows.Scan(&d.detailID, &d.recipient, &name, &subject, &html, &d.src, &d.from, &d.reply, &d.cfg) == nil {
			d.name = "Customer"
			if name.Valid && name.String != "" {
				d.name = name.String
			}
			d.subject, d.html = subject.String, html.String
			dets = append(dets, d)
		}
	}
	rows.Close()

	for _, d := range dets {
		// 生成退订链接（每个收件人独立的 HMAC token）
		unsubURL := ""
		if e.unsubBase != "" {
			token := GenerateUnsubToken(e.secret, d.recipient, d.src)
			unsubURL = e.unsubBase + "/unsubscribe?token=" + token
		}
		select {
		case e.taskCh <- SendTask{
			JobID: 0, BatchID: batch, DetailID: d.detailID,
			Recipient: d.recipient, Name: d.name,
			SubjectTpl: d.subject, HTMLTpl: d.html,
			SourceEmail: d.src, FromName: d.from, ReplyTo: d.reply,
			UnsubURL: unsubURL, ConfigSet: d.cfg,
		}:
		default:
			return false // 队满：本轮中止
		}
	}
	return true
}

// worker 消费 taskCh，每秒限速 e.rate
func (e *Engine) worker(ctx context.Context, id int) {
	defer e.wg.Done()
	count := 0
	windowStart := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.quit:
			return
		case task, ok := <-e.taskCh:
			if !ok {
				return
			}
			// 每 Worker 每秒限速
			if count >= e.rate {
				elapsed := time.Since(windowStart)
				if elapsed < time.Second {
					time.Sleep(time.Second - elapsed)
				}
				count = 0
				windowStart = time.Now()
			}
			count++
			e.sendOne(ctx, id, task)
		}
	}
}

// sendOne 单封发送（黑名单拦截→防重复查→渲染→渠道→状态推进）
func (e *Engine) sendOne(ctx context.Context, workerID int, task SendTask) {
	if e.bl != nil && e.bl.IsBlacklisted(task.Recipient) {
		e.updateDetail(ctx, task, "Failed", "[Blacklisted] 邮箱在黑名单中", "")
		return
	}
	// 防重：明细仍为 Pending 才发
	var st string
	err := e.db.QueryRowContext(ctx,
		"SELECT send_status FROM sending_job_details WHERE id = ?", task.DetailID).Scan(&st)
	if err == sql.ErrNoRows || (err == nil && st != "Pending") {
		return
	}
	msgID, err := e.provider.SendEmail(ctx, task)
	if err != nil {
		short := extractErr(err)
		e.updateDetail(ctx, task, "Failed", short, "")
		if strings.Contains(err.Error(), "Throttling") || strings.Contains(err.Error(), "Rate exceeded") {
			time.Sleep(2 * time.Second)
		}
		return
	}
	e.updateDetail(ctx, task, "Success", "", msgID)
}

// updateDetail 更新同 batch 同 recipient 的全部 Pending 行；job.sent_count += affected
func (e *Engine) updateDetail(ctx context.Context, task SendTask, status, errMsg, msgID string) {
	res, err := e.db.ExecContext(ctx,
		`UPDATE sending_job_details SET send_status=?, send_error=?, message_id=?
		 WHERE batch_id=? AND recipient=? AND send_status='Pending'`,
		status, nullEmpty(errMsg), nullEmpty(msgID), task.BatchID, task.Recipient)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		_, _ = e.db.ExecContext(ctx,
			"UPDATE sending_jobs SET sent_count = sent_count + ? WHERE batch_id=?", n, task.BatchID)
	}
}

// extractErr SES 错误串格式化（[Code] Msg——boto3 风格）
func extractErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "api error "); i >= 0 {
		rest := s[i+len("api error "):]
		if j := strings.Index(rest, ": "); j > 0 {
			return "[" + rest[:j] + "] " + rest[j+2:]
		}
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
