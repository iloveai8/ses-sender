package delivery

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"ses-sender/internal/account"
	"ses-sender/internal/httpx"
	"ses-sender/internal/platform/awsx"
)

// WriteHandler send-bulk / test-email / ses-quota 端点
type WriteHandler struct {
	db     *sql.DB
	snd    Sender
	region string
}

func NewWriteHandler(db *sql.DB, snd Sender, region string) *WriteHandler {
	return &WriteHandler{db: db, snd: snd, region: region}
}

// SendBulk POST /send-bulk（请求体 PascalCase {TemplateId, GroupId}——契约毛边）
func (h *WriteHandler) SendBulk(c *gin.Context) {
	u := account.CurrentUser(c)
	raw, _ := io.ReadAll(c.Request.Body)
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	var echo any
	if len(raw) > 0 && json.Valid(raw) {
		echo = json.RawMessage(raw)
	}
	var issues []httpx.ValidationIssue
	for _, f := range []string{"TemplateId", "GroupId"} {
		if _, ok := body[f]; !ok {
			issues = append(issues, httpx.ValidationIssue{
				Type: "missing", Loc: []string{"body", f}, Msg: "Field required", Input: echo,
			})
		}
	}
	if len(issues) > 0 {
		writeErr(c, &httpx.Error{Status: 422, Detail: issues})
		return
	}
	result, errDetail := SendBulk(c.Request.Context(), h.db, h.snd, SendBulkParams{
		UserID:      u.ID,
		SourceEmail: u.Email,
		TemplateID:  intOf(body["TemplateId"]),
		GroupID:     intOf(body["GroupId"]),
	})
	if errDetail != "" {
		// 配额/未配置=400/429，模板客群=404
		if errDetail == "模版不存在" || errDetail == "客群不存在或无权操作" || errDetail == "客群中没有联系人" {
			writeErr(c, httpx.New(http.StatusNotFound, errDetail))
		} else {
			writeErr(c, httpx.New(http.StatusTooManyRequests, errDetail))
		}
		return
	}
	httpx.WriteJSON(c, http.StatusOK, result)
}

// TestEmail POST /admin/test-email（管理员单发；batch_id 标签固定 test）
func (h *WriteHandler) TestEmail(c *gin.Context) {
	writeErr(c, httpx.New(http.StatusNotImplemented, "测试邮件端点待 P10 接入（需 SES 发信客户端）"))
}

// sesQuotaOut GET /ses-quota 响应（字段顺序=契约；浮点=PyFloat——golden 14.0 形态）
type sesQuotaOut struct {
	MaxSendRate    httpx.PyFloat `json:"max_send_rate"`
	Max24HourSend  httpx.PyFloat `json:"max_24_hour_send"`
	SentLast24Hour httpx.PyFloat `json:"sent_last_24_hours"`
}

// SESQuota GET /ses-quota（SES v1 配额；异常回退默认 1/200/0——Python 语义）
func (h *WriteHandler) SESQuota(c *gin.Context) {
	rate, max24, sent := awsx.QuotaSnapshot(h.region)
	httpx.WriteJSON(c, http.StatusOK, sesQuotaOut{
		MaxSendRate:    httpx.PyFloat(rate),
		Max24HourSend:  httpx.PyFloat(max24),
		SentLast24Hour: httpx.PyFloat(sent),
	})
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func writeErr(c *gin.Context, e *httpx.Error) {
	httpx.WriteJSON(c, e.Status, gin.H{"detail": e.Detail})
}
