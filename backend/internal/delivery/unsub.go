package delivery

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// UnsubHandler 退订端点（GET 页面 / POST one-click；无鉴权——印在邮件里的对外小窗）
type UnsubHandler struct {
	db     *sql.DB
	secret string
}

func NewUnsubHandler(db *sql.DB, secret string) *UnsubHandler {
	return &UnsubHandler{db: db, secret: secret}
}

// pageCfg 退订页配置（合并链：默认 → 系统级 unsub_page_* → 用户级 unsub_config）
type pageCfg struct {
	Title      string
	Subtitle   string
	Reasons    []pageReason
	Success    string
	Logo       string
	Color      string
	ButtonText string
}

type pageReason struct {
	Value string
	Label string
}

// defaultPageCfg 默认配置（=account 域 unsub-defaults 同源字面值；golden 锚定）
func defaultPageCfg() pageCfg {
	return pageCfg{
		Title:    "退订确认",
		Subtitle: "我们很遗憾看到您离开。请告诉我们退订原因，帮助我们改进服务。",
		Reasons: []pageReason{
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

// loadPageCfg 按 source_email 找用户级配置，叠加系统级 unsub_page_*（键映射 button_text→buttonText）
func (h *UnsubHandler) loadPageCfg(ctx context.Context, sourceEmail string) pageCfg {
	cfg := defaultPageCfg()
	// 系统级
	rows, err := h.db.QueryContext(ctx, "SELECT `key`, COALESCE(value,'') FROM system_settings WHERE `key` LIKE 'unsub_page_%'")
	if err == nil {
		m := map[string]string{}
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				m[k] = v
			}
		}
		rows.Close()
		applyCfg(&cfg, m)
	}
	// 用户级（按发件邮箱反查用户 unsub_config）
	var userCfg sql.NullString
	if err := h.db.QueryRowContext(ctx,
		"SELECT unsub_config FROM users WHERE email = ? AND unsub_config IS NOT NULL LIMIT 1",
		sourceEmail).Scan(&userCfg); err == nil && userCfg.Valid && strings.TrimSpace(userCfg.String) != "" {
		applyJSON(&cfg, userCfg.String)
	}
	return cfg
}

// applyCfg 键值覆盖（系统级键名 unsub_page_* → 配置字段）
func applyCfg(cfg *pageCfg, m map[string]string) {
	set := func(dst *string, key string) {
		if v, ok := m[key]; ok && v != "" {
			*dst = v
		}
	}
	set(&cfg.Title, "unsub_page_title")
	set(&cfg.Subtitle, "unsub_page_subtitle")
	set(&cfg.Success, "unsub_page_success")
	set(&cfg.Logo, "unsub_page_logo")
	set(&cfg.Color, "unsub_page_color")
	set(&cfg.ButtonText, "unsub_page_button_text")
	if v, ok := m["unsub_page_reasons"]; ok && v != "" {
		applyJSON(cfg, v) // reasons 是 JSON——复用 JSON 合并逻辑
	}
}

// applyJSON JSON 对象覆盖（title/subtitle/reasons/success/logo/color/buttonText——camelCase 键）
func applyJSON(cfg *pageCfg, raw string) {
	dec := newJSONKeys(raw)
	if v := dec.str("title"); v != "" {
		cfg.Title = v
	}
	if v := dec.str("subtitle"); v != "" {
		cfg.Subtitle = v
	}
	if v := dec.str("success"); v != "" {
		cfg.Success = v
	}
	if v := dec.str("logo"); v != "" {
		cfg.Logo = v
	}
	if v := dec.str("color"); v != "" {
		cfg.Color = v
	}
	if v := dec.str("buttonText"); v != "" {
		cfg.ButtonText = v
	}
	if rs := dec.reasons(); rs != nil {
		cfg.Reasons = rs
	}
}

// Get GET /unsubscribe?token= 三态：缺 token / 坏 token / 选择页（或已退订页）
func (h *UnsubHandler) Get(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte("<h2>Invalid link</h2>"))
		return
	}
	email, source, ok := VerifyUnsubToken(h.secret, token)
	if !ok {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte("<h2>Invalid or expired link</h2>"))
		return
	}
	cfg := h.loadPageCfg(c.Request.Context(), source)
	if h.isUnsubscribed(c.Request.Context(), email, source) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderAlready(cfg, email, source)))
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderPage(cfg, email, source, token)))
}

// Post POST /unsubscribe（RFC 8058 one-click）：token 取 query → form(token/List-Unsubscribe)；
// 成功幂等写库，响应纯文本 "ok"
func (h *UnsubHandler) Post(c *gin.Context) {
	token := c.Query("token")
	reason := "one-click"
	if token == "" {
		if err := c.Request.ParseForm(); err == nil {
			f := c.Request.PostForm
			if v := f.Get("token"); v != "" {
				token = v
			} else if v := f.Get("List-Unsubscribe"); v != "" {
				token = v
			}
			if v := f.Get("reason"); v != "" {
				reason = v
			}
		}
	}
	if token == "" {
		c.String(http.StatusBadRequest, "missing token")
		return
	}
	email, source, ok := VerifyUnsubToken(h.secret, token)
	if !ok {
		c.String(http.StatusBadRequest, "invalid token")
		return
	}
	if len(reason) > 64 {
		reason = reason[:64]
	}
	_, _ = h.db.ExecContext(c.Request.Context(),
		"INSERT IGNORE INTO unsubscribe_list (email, source_email, reason, unsubscribed_at) VALUES (?,?,?,UTC_TIMESTAMP())",
		email, source, reason)
	c.String(http.StatusOK, "ok")
}

func (h *UnsubHandler) isUnsubscribed(ctx context.Context, email, source string) bool {
	var n int
	_ = h.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM unsubscribe_list WHERE email = ? AND source_email = ?", email, source).Scan(&n)
	return n > 0
}
