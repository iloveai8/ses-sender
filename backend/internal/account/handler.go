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
	c.JSON(http.StatusOK, loginResponse{AccessToken: token, TokenType: "bearer", User: loginUserInfo{
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
	c.JSON(http.StatusOK, CurrentUser(c).Out())
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
	c.JSON(e.Status, gin.H{"detail": e.Detail})
}
