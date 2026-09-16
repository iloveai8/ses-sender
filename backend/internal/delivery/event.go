package delivery

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"
)

// ProcessSESEvent SES 事件回写（=Python process_ses_event 逐条对等；P9/P10 验收事件链）。
// 事件源：SQS Worker（SNS 包装拆包后调用）。语义：
//   - eventType 统一大写比较；messageId 匹配明细，查不到忽略
//   - Pending 明细收到任意事件先改 Success（事件即"已发出"的证据）
//   - OPEN/CLICK 计数累加 + 首次时间；状态类字段覆盖写（天然幂等）
func ProcessSESEvent(ctx context.Context, db *sql.DB, evt map[string]any) {
	typ := strVal(evt["eventType"])
	if typ == "" {
		typ = strVal(evt["notificationType"])
	}
	mail, _ := evt["mail"].(map[string]any)
	if typ == "" || mail == nil {
		return
	}
	msgID := strVal(mail["messageId"])
	if msgID == "" {
		return
	}

	// 定位明细
	var detailID int
	var sendStatus string
	err := db.QueryRowContext(ctx,
		"SELECT id, send_status FROM sending_job_details WHERE message_id = ? LIMIT 1", msgID).
		Scan(&detailID, &sendStatus)
	if err == sql.ErrNoRows {
		return // 查不到忽略（Python 语义）
	}
	if err != nil {
		slog.Warn("[Event] 查明细失败", "messageId", msgID, "err", err)
		return
	}
	// Pending → Success（收到事件即已发出）
	if sendStatus == "Pending" {
		if _, err := db.ExecContext(ctx,
			"UPDATE sending_job_details SET send_status='Success' WHERE id = ?", detailID); err != nil {
			slog.Warn("[Event] Pending 修正失败", "err", err)
		}
	}

	ts := eventTime(evt)
	switch strings.ToUpper(typ) {
	case "DELIVERY":
		exec(ctx, db, "UPDATE sending_job_details SET delivery_status='Delivery', delivery_time=? WHERE id=?", ts, detailID)
	case "BOUNCE":
		b, _ := evt["bounce"].(map[string]any)
		bType, bSub, bMsg := strVal(b["bounceType"]), strVal(b["bounceSubType"]), ""
		if rs, ok := b["bouncedRecipients"].([]any); ok && len(rs) > 0 {
			if r0, ok := rs[0].(map[string]any); ok {
				bMsg = strVal(r0["diagnosticCode"])
			}
		}
		exec(ctx, db, "UPDATE sending_job_details SET delivery_status='Bounce', bounce_type=?, bounce_subtype=?, bounce_message=?, delivery_time=? WHERE id=?",
			nullIfEmpty(bType), nullIfEmpty(bSub), nullIfEmpty(bMsg), ts, detailID)
	case "COMPLAINT":
		exec(ctx, db, "UPDATE sending_job_details SET complaint_time=? WHERE id=?", ts, detailID)
	case "OPEN":
		exec(ctx, db, "UPDATE sending_job_details SET open_count=open_count+1, first_open_time=COALESCE(first_open_time, ?) WHERE id=?", ts, detailID)
	case "CLICK":
		exec(ctx, db, "UPDATE sending_job_details SET click_count=click_count+1, first_click_time=COALESCE(first_click_time, ?) WHERE id=?", ts, detailID)
	case "REJECT":
		exec(ctx, db, "UPDATE sending_job_details SET delivery_status='Reject' WHERE id=?", detailID)
	case "SEND":
		exec(ctx, db, "UPDATE sending_job_details SET delivery_status=COALESCE(delivery_status,'Sent') WHERE id=?", detailID)
	}
}

// eventTime 取事件时间戳（delivery/bounce/complaint/open/click 各层；解析失败回落当前时间）
func eventTime(evt map[string]any) time.Time {
	for _, key := range []string{"delivery", "bounce", "complaint", "open", "click", "mail"} {
		if m, ok := evt[key].(map[string]any); ok {
			if ts := strVal(m["timestamp"]); ts != "" {
				for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.999Z0700", "2006-01-02T15:04:05"} {
					if t, err := time.Parse(layout, ts); err == nil {
						return t.UTC()
					}
				}
			}
		}
	}
	return time.Now().UTC()
}

func strVal(v any) string {
	s, _ := v.(string)
	return s
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func exec(ctx context.Context, db *sql.DB, q string, args ...any) {
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		slog.Warn("[Event] 落库失败", "q", q[:40], "err", err)
	}
}
