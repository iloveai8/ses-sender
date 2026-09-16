package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ses-sender/internal/account"
	"ses-sender/internal/httpx"
)

// Handler campaign 域 HTTP 适配层
type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// ── dashboard / 配额 ──

// Dashboard GET /user/dashboard
func (h *Handler) Dashboard(c *gin.Context) {
	u := account.CurrentUser(c)
	d, err := h.store.GetDashboard(c.Request.Context(), u.ID, limit(u.DailySendLimit))
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, d)
}

// DailyQuota GET /user/daily-quota（键序=契约 daily_limit,today_sent,remaining）
func (h *Handler) DailyQuota(c *gin.Context) {
	u := account.CurrentUser(c)
	limitN := limit(u.DailySendLimit)
	sent, l, remain := h.store.TodayQuota(c.Request.Context(), u.ID, limitN)
	type quotaOut struct {
		DailyLimit int `json:"daily_limit"`
		TodaySent  int `json:"today_sent"`
		Remaining  int `json:"remaining"`
	}
	httpx.WriteJSON(c, http.StatusOK, quotaOut{DailyLimit: l, TodaySent: sent, Remaining: remain})
}

func limit(n int) int {
	if n <= 0 {
		return 1000
	}
	return n
}

// ── 批次列表/指标/明细/进度 ──

// ListJobs GET /sending-jobs
func (h *Handler) ListJobs(c *gin.Context) {
	page, pageSize := pageParams(c, 15)
	items, total, err := h.store.ListJobs(c.Request.Context(), account.CurrentUser(c).ID, page, pageSize)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, httpx.NewPage(items, total, page, pageSize))
}

// Metrics GET /sending-jobs/:batch_id/metrics（admin 可看任意批次）
func (h *Handler) Metrics(c *gin.Context) {
	if !h.ownBatch(c) {
		return
	}
	m, ok, err := h.store.GetBatchMetrics(c.Request.Context(), c.Param("batch_id"))
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if !ok {
		writeErr(c, httpx.New(http.StatusNotFound, "批次不存在"))
		return
	}
	m.DeliveryRate = ratio(m.Delivery, m.Send)
	m.OpenRate = ratio(m.Open, m.Delivery)
	m.BounceRate = ratio(m.Bounce, m.Send)
	m.ReceiptAvailable = m.Delivery+m.Bounce+m.Reject+m.Complaint+m.Open+m.Click > 0
	httpx.WriteJSON(c, http.StatusOK, m)
}

func ratio(a, b int) httpx.PyFloat {
	if b <= 0 {
		return httpx.Round1(0)
	}
	return httpx.Round1(float64(a) / float64(b) * 100)
}

// ownBatch 批次归属：admin 任意；否则限本人；不存在 404
func (h *Handler) ownBatch(c *gin.Context) bool {
	u := account.CurrentUser(c)
	restrict := 0
	if !u.IsAdmin {
		restrict = u.ID
	}
	j, err := h.store.GetJobByBatch(c.Request.Context(), c.Param("batch_id"), restrict)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return false
	}
	if j == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "批次不存在"))
		return false
	}
	return true
}

// Details GET /sending-jobs/:batch_id/details
func (h *Handler) Details(c *gin.Context) {
	if !h.ownBatch(c) {
		return
	}
	items, err := h.store.ListDetailsByBatch(c.Request.Context(), c.Param("batch_id"))
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, items)
}

// Progress GET /sending-jobs/:batch_id/progress（键序=契约）
func (h *Handler) Progress(c *gin.Context) {
	u := account.CurrentUser(c)
	restrict := 0
	if !u.IsAdmin {
		restrict = u.ID
	}
	j, err := h.store.GetJobByBatch(c.Request.Context(), c.Param("batch_id"), restrict)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if j == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "批次不存在"))
		return
	}
	prog := httpx.Round1(0)
	if j.TotalContacts > 0 {
		prog = httpx.Round1(float64(j.SentCount) / float64(j.TotalContacts) * 100)
	}
	type progressOut struct {
		BatchID       string        `json:"batch_id"`
		Status        string        `json:"status"`
		TotalContacts int           `json:"total_contacts"`
		SentCount     int           `json:"sent_count"`
		TotalBatches  int           `json:"total_batches"`
		Progress      httpx.PyFloat `json:"progress"`
		ErrorMessage  *string       `json:"error_message"`
		FinishedAt    *string       `json:"finished_at"`
	}
	httpx.WriteJSON(c, http.StatusOK, progressOut{
		BatchID: j.BatchID, Status: j.Status, TotalContacts: j.TotalContacts,
		SentCount: j.SentCount, TotalBatches: j.TotalBatches, Progress: prog,
		ErrorMessage: j.ErrorMessage, FinishedAt: j.FinishedAt,
	})
}

// EmailDetails GET /email-details
func (h *Handler) EmailDetails(c *gin.Context) {
	u := account.CurrentUser(c)
	page, pageSize := pageParams(c, 20)
	f := EmailDetailFilter{
		Recipient:      c.Query("recipient"),
		BatchID:        c.Query("batch_id"),
		SendStatus:     c.Query("send_status"),
		DeliveryStatus: c.Query("delivery_status"),
	}
	items, total, err := h.store.ListEmailDetails(c.Request.Context(), u.ID, u.IsAdmin, f, page, pageSize)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, httpx.NewPage(items, total, page, pageSize))
}

// ── admin 统计 ──

// AdminQuotas GET /admin/users/quotas（{uid: 今日已发}——单键 map 序无妨）
func (h *Handler) AdminQuotas(c *gin.Context) {
	m, err := h.store.AllTodayQuota(c.Request.Context())
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if m == nil {
		m = map[string]int{}
	}
	httpx.WriteJSON(c, http.StatusOK, m)
}

// AdminStats GET /admin/sending-stats
func (h *Handler) AdminStats(c *gin.Context) {
	d, err := h.store.GetAdminStats(c.Request.Context())
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, d)
}

// AdminJobs GET /admin/sending-jobs
func (h *Handler) AdminJobs(c *gin.Context) {
	page, pageSize := pageParams(c, 15)
	items, total, err := h.store.ListAdminJobs(c.Request.Context(), page, pageSize)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, httpx.NewPage(items, total, page, pageSize))
}

// ── 定时任务 ──

// ListScheduled GET /scheduled-jobs
func (h *Handler) ListScheduled(c *gin.Context) {
	items, err := h.store.ListScheduled(c.Request.Context(), account.CurrentUser(c).ID)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, items)
}

// CreateScheduled POST /scheduled-jobs（模板/客群归属 404；时间格式 400；算不出下次 400）
func (h *Handler) CreateScheduled(c *gin.Context) {
	u := account.CurrentUser(c)
	raw, _ := io.ReadAll(c.Request.Body)
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	typ := str(body["schedule_type"])
	st, err := parsePyTime(str(body["scheduled_time"]))
	if err != nil {
		writeErr(c, httpx.New(http.StatusBadRequest, "时间格式无效"))
		return
	}
	tplID := intOr(body["template_id"], 0)
	grpID := intOr(body["group_id"], 0)
	tplName, ok := h.store.TplName(c.Request.Context(), u.ID, tplID)
	if !ok {
		writeErr(c, httpx.New(http.StatusNotFound, "模版不存在"))
		return
	}
	grpName, ok := h.store.GroupName(c.Request.Context(), u.ID, grpID)
	if !ok {
		writeErr(c, httpx.New(http.StatusNotFound, "客群不存在或无权操作"))
		return
	}
	cronHour := intOr(body["cron_hour"], 9)
	cronMinute := intOr(body["cron_minute"], 0)
	dow, dom := optInt(body["day_of_week"]), optInt(body["day_of_month"])
	next := CalcNextRun(typ, st, cronHour, cronMinute, dow, dom, time.Now().UTC())
	if next == nil {
		writeErr(c, httpx.New(http.StatusBadRequest, "计算下次执行时间失败，请检查时间设置"))
		return
	}
	out, err := h.store.CreateScheduled(c.Request.Context(), CreateSchedParams{
		UserID: u.ID, TemplateID: tplID, GroupID: grpID, TemplateName: tplName, GroupName: grpName,
		ScheduleType: typ, ScheduledTime: st, CronHour: cronHour, CronMinute: cronMinute,
		DayOfWeek: dow, DayOfMonth: dom, NextRun: next,
	})
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, out)
}

// UpdateScheduled PUT /scheduled-jobs/:id（本增量实现 status/schedule 字段更新；404 任务不存在）
func (h *Handler) UpdateScheduled(c *gin.Context) {
	u := account.CurrentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	if _, err := h.store.GetScheduled(c.Request.Context(), u.ID, id); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	raw, _ := io.ReadAll(c.Request.Body)
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	sets := map[string]any{}
	if v, ok := body["status"].(string); ok {
		sets["status"] = v
	}
	if v, ok := body["cron_hour"]; ok {
		sets["cron_hour"] = intOr(v, 9)
	}
	if v, ok := body["cron_minute"]; ok {
		sets["cron_minute"] = intOr(v, 0)
	}
	out, err := h.store.UpdateSchedFields(c.Request.Context(), u.ID, id, sets)
	if err != nil || out == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "任务不存在"))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, out)
}

// DeleteScheduled DELETE /scheduled-jobs/:id
func (h *Handler) DeleteScheduled(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	ok, err := h.store.DeleteScheduled(c.Request.Context(), account.CurrentUser(c).ID, id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if !ok {
		writeErr(c, httpx.New(http.StatusNotFound, "任务不存在"))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "已删除"})
}

// ── 退订管理 ──

// ListUnsub GET /unsubscribe-list
func (h *Handler) ListUnsub(c *gin.Context) {
	u := account.CurrentUser(c)
	page, pageSize := pageParams(c, 20)
	items, total, err := h.store.ListUnsub(c.Request.Context(), u.ID, u.IsAdmin, u.Email, c.Query("search"), page, pageSize)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, httpx.NewPage(items, total, page, pageSize))
}

// RestoreUnsub DELETE /unsubscribe-list/:id（恢复；非本人记录 403）
func (h *Handler) RestoreUnsub(c *gin.Context) {
	u := account.CurrentUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	it, err := h.store.GetUnsub(c.Request.Context(), id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if it == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "记录不存在"))
		return
	}
	if !u.IsAdmin && it.SourceEmail != u.Email {
		writeErr(c, httpx.New(http.StatusForbidden, "无权操作"))
		return
	}
	if err := h.store.DeleteUnsub(c.Request.Context(), id); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "已恢复，该邮箱将重新接收邮件"})
}

// BatchRestoreUnsub POST /unsubscribe-list/batch-delete
func (h *Handler) BatchRestoreUnsub(c *gin.Context) {
	u := account.CurrentUser(c)
	var body struct {
		IDs []int64 `json:"ids"`
	}
	_ = c.ShouldBindJSON(&body)
	if len(body.IDs) == 0 {
		writeErr(c, httpx.New(http.StatusBadRequest, "未选择记录"))
		return
	}
	n, err := h.store.DeleteUnsubByIDs(c.Request.Context(), body.IDs, u.IsAdmin, u.Email)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "已恢复 " + strconv.FormatInt(n, 10) + " 条记录"})
}

// ── 小工具 ──

// parsePyTime 解析 Python fromisoformat 风格（去 Z/时区后缀→UTC naive）
func parsePyTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "Z")
	if strings.HasSuffix(s, "+00:00") {
		s = strings.TrimSuffix(s, "+00:00")
	}
	return time.Parse("2006-01-02T15:04:05", strings.Replace(s, " ", "T", 1))
}

func pageParams(c *gin.Context, defSize int) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", strconv.Itoa(defSize)))
	if size < 1 {
		size = defSize
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func intOr(v any, def int) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return def
}

func optInt(v any) *int {
	if f, ok := v.(float64); ok {
		i := int(f)
		return &i
	}
	return nil
}

func writeErr(c *gin.Context, e *httpx.Error) {
	httpx.WriteJSON(c, e.Status, gin.H{"detail": e.Detail})
}

// TplName / GroupName 模板与客群归属查询（定时任务建单校验）
func (s *Store) TplName(ctx context.Context, userID, id int) (string, bool) {
	var name string
	err := s.db.QueryRowContext(ctx,
		"SELECT name FROM email_templates WHERE id = ? AND user_id = ?", id, userID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", false
	}
	return name, err == nil
}

func (s *Store) GroupName(ctx context.Context, userID, id int) (string, bool) {
	var name string
	err := s.db.QueryRowContext(ctx,
		"SELECT name FROM contact_groups WHERE id = ? AND user_id = ?", id, userID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", false
	}
	return name, err == nil
}
