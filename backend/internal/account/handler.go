package account

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ses-sender/internal/httpx"
)

// loginResponse 字段顺序=golden 契约（access_token, token_type, user）。
// 注意契约毛边：login 的 user 对象比 /auth/me 少 contact_email/is_active 两个字段（Python 两端点本就不同）
type loginResponse struct {
	AccessToken string        `json:"access_token"`
	TokenType   string        `json:"token_type"`
	User        loginUserInfo `json:"user"`
}

// loginUserInfo login 响应内的用户形状（7 字段，勿与 UserOut 混用）
type loginUserInfo struct {
	ID             int     `json:"id"`
	Username       string  `json:"username"`
	DisplayName    string  `json:"display_name"`
	SenderName     *string `json:"sender_name"`
	Email          string  `json:"email"`
	IsAdmin        bool    `json:"is_admin"`
	DailySendLimit int     `json:"daily_send_limit"`
}

// Handler account 域 HTTP 适配层
type Handler struct {
	store  *Store
	tokens *TokenSigner
	rl     *RateLimiter
}

func NewHandler(store *Store, tokens *TokenSigner, rl *RateLimiter) *Handler {
	return &Handler{store: store, tokens: tokens, rl: rl}
}

// Login POST /auth/login（需求 A1/A2：文案矩阵 + 限速 + 422 形状均锚定 golden）
func (h *Handler) Login(c *gin.Context) {
	ip := ClientIP(c.Request)
	if remain := h.rl.IsLocked(ip); remain > 0 {
		writeErr(c, httpx.New(http.StatusTooManyRequests,
			lockMessage(remain)))
		return
	}

	// 手工绑定：原样字节回显请求体（pydantic 422 的 input 字段——map 重排序会破契约，用 RawMessage 透传）
	raw, _ := io.ReadAll(c.Request.Body)
	var body map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			body = map[string]any{}
		}
	}
	for _, field := range []string{"username", "password"} {
		if _, ok := body[field]; !ok {
			var echo any
			if len(raw) > 0 && json.Valid(raw) {
				echo = json.RawMessage(raw)
			}
			writeErr(c, httpx.UnprocessableMissing(field, echo))
			return
		}
	}
	username, _ := body["username"].(string)
	password, _ := body["password"].(string)

	user, err := h.store.GetByUsername(c.Request.Context(), username)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if user == nil || !VerifyPassword(user.HashedPassword, password) {
		locked := h.rl.RecordFailure(ip)
		detail := "用户名或密码错误"
		if locked > 0 {
			detail = "失败次数过多，已临时锁定，请 10 分钟后再试"
		}
		writeErr(c, httpx.New(http.StatusUnauthorized, detail))
		return
	}
	if !user.IsActive {
		writeErr(c, httpx.New(http.StatusForbidden, "账户已被禁用"))
		return
	}

	h.rl.RecordSuccess(ip)
	token, err := h.tokens.Create(user.Username)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, "令牌签发失败"))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, loginResponse{AccessToken: token, TokenType: "bearer", User: loginUserInfo{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
		SenderName: user.SenderName, Email: user.Email,
		IsAdmin: user.IsAdmin, DailySendLimit: user.DailySendLimit,
	}})
}

// Authenticate 认证中间件：Authorization Bearer → ctx 注入 *User（文案矩阵见 httpx）
func (h *Handler) Authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			c.Header("WWW-Authenticate", "Bearer")
			writeErr(c, httpx.ErrNotAuthenticated())
			c.Abort()
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		sub, err := h.tokens.ParseSub(token)
		if err != nil {
			writeErr(c, httpx.ErrInvalidAuth())
			c.Abort()
			return
		}
		user, err := h.store.GetByUsername(c.Request.Context(), sub)
		if err != nil {
			writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
			c.Abort()
			return
		}
		if user == nil || !user.IsActive {
			writeErr(c, httpx.ErrUserDisabled())
			c.Abort()
			return
		}
		c.Set("user", user)
		c.Next()
	}
}

// RequireAdmin admin 门槛（叠加在 Authenticate 之后）
func (h *Handler) RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !CurrentUser(c).IsAdmin {
			writeErr(c, httpx.ErrRequireAdmin())
			c.Abort()
			return
		}
		c.Next()
	}
}

// Me GET /auth/me
func (h *Handler) Me(c *gin.Context) {
	httpx.WriteJSON(c, http.StatusOK, CurrentUser(c).Out())
}

// CurrentUser 从 ctx 取认证用户（未认证时返回零值，仅限已过 Authenticate 的路由使用）
func CurrentUser(c *gin.Context) *User {
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*User); ok {
			return u
		}
	}
	return &User{}
}

// lockMessage 锁定提示文案（分钟数与 Python max(1, remain//60) 口径一致）
func lockMessage(remainSeconds int) string {
	mins := remainSeconds / 60
	if mins < 1 {
		mins = 1
	}
	return "登录失败次数过多，请 " + strconv.Itoa(mins) + " 分钟后再试"
}

// writeErr 统一错误出口：{"detail": ...}（全项目唯一渲染点）
func writeErr(c *gin.Context, e *httpx.Error) {
	httpx.WriteJSON(c, e.Status, gin.H{"detail": e.Detail})
}

// ── 用户管理（admin 专属）──────────────────────────────

// AdminUsersList GET /admin/users
func (h *Handler) AdminUsersList(c *gin.Context) {
	users, err := h.store.List(c.Request.Context())
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	out := make([]UserOut, 0, len(users)) // 空列表序列化为 []（Python 行为），不用 nil
	for _, u := range users {
		out = append(out, u.Out())
	}
	httpx.WriteJSON(c, http.StatusOK, out)
}

// AdminUsersCreate POST /admin/users（422 按 UserCreate 字段定义顺序报全部缺失——pydantic 行为）
func (h *Handler) AdminUsersCreate(c *gin.Context) {
	raw, _ := io.ReadAll(c.Request.Body)
	var body map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			body = map[string]any{}
		}
	}
	var echo any
	if len(raw) > 0 && json.Valid(raw) {
		echo = json.RawMessage(raw)
	}
	var issues []httpx.ValidationIssue
	for _, f := range []string{"username", "display_name", "password", "email"} {
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

	p := CreateParams{
		Username:       str(body["username"]),
		DisplayName:    str(body["display_name"]),
		Password:       str(body["password"]),
		Email:          str(body["email"]),
		ContactEmail:   optStr(body["contact_email"]),
		IsAdmin:        boolOr(body["is_admin"], false),
		DailySendLimit: intOr(body["daily_send_limit"], 1000),
		SenderName:     optStr(body["sender_name"]),
	}
	if p.SenderName != nil && len(*p.SenderName) > 255 {
		writeErr(c, &httpx.Error{Status: 422, Detail: []httpx.ValidationIssue{{
			Type: "string_too_long", Loc: []string{"body", "sender_name"},
			Msg: "String should have at most 255 characters", Input: echo,
		}}})
		return
	}
	user, dup, err := h.store.Create(c.Request.Context(), p)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if dup {
		writeErr(c, httpx.New(http.StatusBadRequest, "用户名已存在"))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, user.Out())
}

// AdminUsersUpdate PUT /admin/users/:user_id（全字段可选；不存在 404 用户不存在）
func (h *Handler) AdminUsersUpdate(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("user_id"))
	if err != nil {
		writeErr(c, httpx.ErrNotFound())
		return
	}
	user, err := h.store.GetByID(c.Request.Context(), id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if user == nil {
		writeErr(c, httpx.New(http.StatusNotFound, "用户不存在"))
		return
	}
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		body = map[string]any{}
	}
	fields := map[string]any{}
	if v, ok := body["display_name"]; ok {
		fields["display_name"] = str(v)
	}
	if v, ok := body["email"]; ok {
		fields["email"] = str(v)
	}
	if v, ok := body["contact_email"]; ok {
		fields["contact_email"] = str(v)
	}
	if v, ok := body["password"]; ok && str(v) != "" {
		hash, err := HashPassword(str(v))
		if err == nil {
			fields["hashed_password"] = hash
		}
	}
	if v, ok := body["is_active"]; ok {
		fields["is_active"] = boolOr(v, true)
	}
	if v, ok := body["daily_send_limit"]; ok {
		fields["daily_send_limit"] = intOr(v, 1000)
	}
	if v, ok := body["sender_name"]; ok {
		s := str(v)
		fields["sender_name"] = &s
	}
	if err := h.store.UpdateProfile(c.Request.Context(), id, fields); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	updated, _ := h.store.GetByID(c.Request.Context(), id)
	httpx.WriteJSON(c, http.StatusOK, updated.Out())
}

// ── 个人设置 ─────────────────────────────────────────

// unsubReason 退订原因项（字段顺序=契约 value,label）
type unsubReason struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// unsubPageConfig 退订页配置（字段顺序=golden 契约，禁改）
type unsubPageConfig struct {
	Title      string        `json:"title"`
	Subtitle   string        `json:"subtitle"`
	Reasons    []unsubReason `json:"reasons"`
	Success    string        `json:"success"`
	Logo       string        `json:"logo"`
	Color      string        `json:"color"`
	ButtonText string        `json:"buttonText"`
}

// defaultUnsubConfig 系统默认（字面值=golden 锚定，与 Python settings 默认一致）
func defaultUnsubConfig() unsubPageConfig {
	return unsubPageConfig{
		Title:    "退订确认",
		Subtitle: "我们很遗憾看到您离开。请告诉我们退订原因，帮助我们改进服务。",
		Reasons: []unsubReason{
			{"too_frequent", "收到邮件太频繁"},
			{"not_relevant", "内容与我无关"},
			{"never_subscribed", "我从未订阅过"},
			{"prefer_other", "我更喜欢其他渠道获取信息"},
			{"other", "其他原因"},
		},
		Success:    "退订成功",
		Logo:       "",
		Color:      "#667eea",
		ButtonText: "确认退订",
	}
}

// UnsubDefaults GET /user/unsub-defaults：三级合并 默认→系统级 unsub_page_*→用户级（=Python val 链）
func (h *Handler) UnsubDefaults(c *gin.Context) {
	cfg := defaultUnsubConfig()
	// 系统级
	sys := map[string]string{}
	if rows, err := h.store.db.QueryContext(c.Request.Context(),
		"SELECT `key`, COALESCE(value,'') FROM system_settings WHERE `key` LIKE 'unsub_page_%'"); err == nil {
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil && v != "" {
				sys[k] = v
			}
		}
		rows.Close()
	}
	userRaw, _ := h.store.GetUnsubConfig(c.Request.Context(), CurrentUser(c).ID)
	var user map[string]json.RawMessage
	if userRaw != "" {
		_ = json.Unmarshal([]byte(userRaw), &user)
	}

	set := func(dst *string, userKey, sysKey string) {
		if user != nil {
			var v string
			if raw, ok := user[userKey]; ok && json.Unmarshal(raw, &v) == nil && v != "" {
				*dst = v
				return
			}
		}
		if v := sys[sysKey]; v != "" {
			*dst = v
		}
	}
	set(&cfg.Title, "title", "unsub_page_title")
	set(&cfg.Subtitle, "subtitle", "unsub_page_subtitle")
	set(&cfg.Success, "success", "unsub_page_success")
	set(&cfg.Logo, "logo", "unsub_page_logo")
	set(&cfg.Color, "color", "unsub_page_color")
	set(&cfg.ButtonText, "buttonText", "unsub_page_button_text")
	// reasons：用户级(list)→系统级(JSON 串)→默认
	if user != nil {
		var rs []unsubReason
		if raw, ok := user["reasons"]; ok && json.Unmarshal(raw, &rs) == nil && len(rs) > 0 {
			cfg.Reasons = rs
		}
	} else if v := sys["unsub_page_reasons"]; v != "" {
		var rs []unsubReason
		if json.Unmarshal([]byte(v), &rs) == nil && len(rs) > 0 {
			cfg.Reasons = rs
		}
	}
	httpx.WriteJSON(c, http.StatusOK, cfg)
}

// UnsubConfigGet GET /user/unsub-config：原始 JSON 透传；空/损坏兜 {}
func (h *Handler) UnsubConfigGet(c *gin.Context) {
	raw, _ := h.store.GetUnsubConfig(c.Request.Context(), CurrentUser(c).ID)
	out := []byte(raw)
	if json.Valid(out) == false || len(out) == 0 {
		out = []byte("{}")
	}
	c.Data(http.StatusOK, "application/json", out)
}

// UnsubConfigPut PUT /user/unsub-config：任意 JSON 原样保存
func (h *Handler) UnsubConfigPut(c *gin.Context) {
	raw, _ := io.ReadAll(c.Request.Body)
	if !json.Valid(raw) {
		raw = []byte("{}")
	}
	if err := h.store.SetUnsubConfig(c.Request.Context(), CurrentUser(c).ID, raw); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "退订页面配置已保存"})
}

// ContactEmailPut PUT /user/contact-email
func (h *Handler) ContactEmailPut(c *gin.Context) {
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	email := str(body["contact_email"])
	if email == "" {
		writeErr(c, httpx.New(http.StatusBadRequest, "收件邮箱不能为空"))
		return
	}
	if err := h.store.UpdateProfile(c.Request.Context(), CurrentUser(c).ID, map[string]any{"contact_email": email}); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "收件邮箱已更新"})
}

// ── JSON 取值小工具 ──────────────────────────────────

func str(v any) string {
	s, _ := v.(string)
	return s
}

func optStr(v any) *string {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func boolOr(v any, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func intOr(v any, def int) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return def
}
