package template

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ses-sender/internal/account"
	"ses-sender/internal/httpx"
	"ses-sender/internal/platform/awsx"
)

// Handler template 域 HTTP 适配层（/user/templates 与 /admin/templates 同构——按当前用户隔离，毛边照抄）
type Handler struct {
	store *Store
	ses   *awsx.SESTemplates
}

func NewHandler(store *Store, ses *awsx.SESTemplates) *Handler {
	return &Handler{store: store, ses: ses}
}

// List GET /user/templates（admin 版同构复用）
func (h *Handler) List(c *gin.Context) {
	items, err := h.store.List(c.Request.Context(), account.CurrentUser(c).ID)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, items)
}

// Create POST {name 必填, subject 必填, html_body=""}——先 SES 后 DB（Python 顺序）
func (h *Handler) Create(c *gin.Context) {
	raw, body := readJSON(c)
	var echo any
	if len(raw) > 0 && json.Valid(raw) {
		echo = json.RawMessage(raw)
	}
	var issues []httpx.ValidationIssue
	for _, f := range []string{"name", "subject"} {
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
	name, subject, html := str(body["name"]), str(body["subject"]), str(body["html_body"])
	text := html
	if text == "" {
		text = " " // Python：text_body = html_body or " "
	}
	sesName, err := GenSesName(account.CurrentUser(c).ID)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if err := h.ses.Create(c.Request.Context(), sesName, subject, html, text); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, fmt.Sprintf("SES 模版创建失败: %v", err)))
		return
	}
	if _, err := h.store.Create(c.Request.Context(), account.CurrentUser(c).ID, name, sesName, subject, html, text); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": fmt.Sprintf("模版「%s」创建成功", name)})
}

// Update PUT /:id {subject?, html_body?}
func (h *Handler) Update(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	_, body := readJSON(c)
	r, err := h.store.Update(c.Request.Context(), account.CurrentUser(c).ID, id,
		optStr(body["subject"]), optStr(body["html_body"]))
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if r == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "模版不存在"))
		return
	}
	if err := h.ses.Update(c.Request.Context(), r.SesName, r.Subject, r.HTMLBody, r.TextBody); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, fmt.Sprintf("SES 模版更新失败: %v", err)))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": fmt.Sprintf("模版「%s」已更新", r.Name)})
}

// Delete DELETE /:id（SES 删除失败静默——Python 语义）
func (h *Handler) Delete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	r, err := h.store.Get(c.Request.Context(), account.CurrentUser(c).ID, id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if r == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "模版不存在"))
		return
	}
	if _, err := h.store.Delete(c.Request.Context(), account.CurrentUser(c).ID, id); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	_ = h.ses.Delete(c.Request.Context(), r.SesName) // best-effort
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": fmt.Sprintf("模版「%s」已删除", r.Name)})
}

// ListAttachments GET /:id/attachments
func (h *Handler) ListAttachments(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	r, err := h.store.Get(c.Request.Context(), account.CurrentUser(c).ID, id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if r == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "模版不存在"))
		return
	}
	items, err := h.store.ListAttachments(c.Request.Context(), account.CurrentUser(c).ID, id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, items)
}

// DeleteAttachment DELETE /:id/attachments/:att_id
func (h *Handler) DeleteAttachment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	attID, _ := strconv.Atoi(c.Param("att_id"))
	r, err := h.store.Get(c.Request.Context(), account.CurrentUser(c).ID, id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if r == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "模版不存在"))
		return
	}
	ok, err := h.store.DeleteAttachment(c.Request.Context(), account.CurrentUser(c).ID, id, attID)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if !ok {
		writeErr(c, httpx.New(http.StatusNotFound, "附件不存在"))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "附件已删除"})
}

// ── 小工具（与 contact 包同构，域内自持避免跨域依赖）──

func readJSON(c *gin.Context) ([]byte, map[string]any) {
	raw, _ := io.ReadAll(c.Request.Body)
	body := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			body = map[string]any{}
		}
	}
	return raw, body
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func optStr(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func writeErr(c *gin.Context, e *httpx.Error) {
	httpx.WriteJSON(c, e.Status, gin.H{"detail": e.Detail})
}
