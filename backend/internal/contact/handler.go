package contact

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ses-sender/internal/account"
	"ses-sender/internal/httpx"
)

// Handler contact 域 HTTP 适配层
type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// ListGroups GET /groups?search=&page=1&page_size=20
func (h *Handler) ListGroups(c *gin.Context) {
	page, pageSize := pageParams(c, 20)
	items, total, err := h.store.ListGroups(c.Request.Context(), account.CurrentUser(c).ID, c.Query("search"), page, pageSize)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	c.JSON(http.StatusOK, httpx.NewPage(items, total, page, pageSize))
}

// CreateGroup POST /groups {name, description?}
func (h *Handler) CreateGroup(c *gin.Context) {
	raw, body := readJSON(c)
	if _, ok := body["name"]; !ok {
		writeErr(c, missing("name", raw))
		return
	}
	g, err := h.store.CreateGroup(c.Request.Context(), account.CurrentUser(c).ID, str(body["name"]), optStr(body["description"]))
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	c.JSON(http.StatusOK, g)
}

// UpdateGroup PUT /groups/:id {name?, description?}
func (h *Handler) UpdateGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	_, body := readJSON(c)
	ok, err := h.store.UpdateGroup(c.Request.Context(), account.CurrentUser(c).ID, id,
		optStr(body["name"]), optStr(body["description"]))
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if !ok {
		writeErr(c, httpx.New(http.StatusNotFound, "客群不存在或无权操作"))
		return
	}
	g, err := h.store.GetGroup(c.Request.Context(), account.CurrentUser(c).ID, id)
	if err != nil || g == nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, "读取更新结果失败"))
		return
	}
	c.JSON(http.StatusOK, g)
}

// DeleteGroup DELETE /groups/:id（级联删联系人）
func (h *Handler) DeleteGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	ok, err := h.store.DeleteGroup(c.Request.Context(), account.CurrentUser(c).ID, id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if !ok {
		writeErr(c, httpx.New(http.StatusNotFound, "客群不存在或无权操作"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "客群已删除"})
}

// ListContacts GET /groups/:id/contacts?search=&page=&page_size=
func (h *Handler) ListContacts(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, err := h.store.GetGroup(c.Request.Context(), account.CurrentUser(c).ID, id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if g == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "客群不存在或无权操作"))
		return
	}
	page, pageSize := pageParams(c, 20)
	items, total, err := h.store.ListContacts(c.Request.Context(), id, c.Query("search"), page, pageSize)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	c.JSON(http.StatusOK, httpx.NewPage(items, total, page, pageSize))
}

// CreateContact POST /contacts {email 必填, name?, attributes?, group_id 必填}
// 422 按 ContactCreate 字段定义顺序报缺失（email, group_id——pydantic 行为）
func (h *Handler) CreateContact(c *gin.Context) {
	raw, body := readJSON(c)
	var echo any
	if len(raw) > 0 && json.Valid(raw) {
		echo = json.RawMessage(raw)
	}
	var issues []httpx.ValidationIssue
	for _, f := range []string{"email", "group_id"} {
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
	groupID := intOr(body["group_id"], 0)
	g, err := h.store.GetGroup(c.Request.Context(), account.CurrentUser(c).ID, groupID)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if g == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "客群不存在或无权操作"))
		return
	}
	out, err := h.store.CreateContact(c.Request.Context(), str(body["email"]),
		optStr(body["name"]), optStr(body["attributes"]), groupID)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	c.JSON(http.StatusOK, out)
}

// DeleteContact DELETE /contacts/:id
func (h *Handler) DeleteContact(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	ok, err := h.store.DeleteContact(c.Request.Context(), account.CurrentUser(c).ID, id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if !ok {
		writeErr(c, httpx.New(http.StatusNotFound, "联系人不存在"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "联系人已删除"})
}

// ── 小工具 ──

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
		size = 100 // Query(ge=1, le=100) 上界
	}
	return page, size
}

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

func missing(field string, raw []byte) *httpx.Error {
	var echo any
	if len(raw) > 0 && json.Valid(raw) {
		echo = json.RawMessage(raw)
	}
	return httpx.UnprocessableMissing(field, echo)
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

func intOr(v any, def int) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return def
}

func writeErr(c *gin.Context, e *httpx.Error) {
	c.JSON(e.Status, gin.H{"detail": e.Detail})
}
