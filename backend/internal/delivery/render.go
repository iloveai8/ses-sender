package delivery

import (
	"encoding/json"
	"fmt"
	"strings"
)

// renderPage 渲染退订选择页（模板=金标准反推，逐字节保真）
func renderPage(cfg pageCfg, email, source, token string) string {
	logo := logoHTML(cfg.Logo)
	var reasons strings.Builder
	for _, r := range cfg.Reasons {
		reasons.WriteString(fmt.Sprintf(
			"<div class=\"reason\" onclick=\"selectReason(this,'%s')\"><input type=\"radio\" name=\"reason\" value=\"%s\"><label>%s</label></div>\n",
			r.Value, r.Value, r.Label))
	}
	return strings.NewReplacer(
		"__T__", cfg.Title,
		"__SUB__", cfg.Subtitle,
		"__SUCCESS__", cfg.Success,
		"__BTN__", cfg.ButtonText,
		"__EMAIL__", email,
		"__SRC__", source,
		"__COLOR__", cfg.Color,
		"__TOKEN__", token,
		"__REASONS__", reasons.String(),
		"__LOGO1__", logo,
		"__LOGO2__", logo,
	).Replace(pageTemplateHTML)
}

// renderAlready 渲染"已退订"页
func renderAlready(cfg pageCfg, email, source string) string {
	return strings.NewReplacer(
		"__T__", cfg.Title,
		"__EMAIL__", email,
		"__SRC__", source,
		"__LOGO__", logoHTML(cfg.Logo),
	).Replace(alreadyPageHTML)
}

func logoHTML(logo string) string {
	if logo == "" {
		return ""
	}
	return fmt.Sprintf("<img src=%q alt=\"Logo\" style=\"max-height:48px;margin-bottom:16px;\">", logo)
}

// ── 页面配置 JSON 读取小件 ──

type jsonKeys struct{ m map[string]json.RawMessage }

func newJSONKeys(raw string) *jsonKeys {
	j := &jsonKeys{m: map[string]json.RawMessage{}}
	_ = json.Unmarshal([]byte(raw), &j.m)
	return j
}

func (j *jsonKeys) str(key string) string {
	v, ok := j.m[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

func (j *jsonKeys) reasons() []pageReason {
	v, ok := j.m["reasons"]
	if !ok {
		return nil
	}
	var rs []pageReason
	if json.Unmarshal(v, &rs) == nil && len(rs) > 0 {
		return rs
	}
	return nil
}
