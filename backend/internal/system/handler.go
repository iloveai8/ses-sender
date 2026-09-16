package system

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ses-sender/internal/httpx"
	"ses-sender/internal/platform/config"
)

// Handler system 域 HTTP 适配层
type Handler struct {
	store  *Store
	bl     *BlacklistCache
	bedDef config.Bedrock // 默认值注入源（BEDROCK_MODEL_ID/REGION env）
}

func NewHandler(store *Store, bl *BlacklistCache, bedDef config.Bedrock) *Handler {
	return &Handler{store: store, bl: bl, bedDef: bedDef}
}

// ── settings ──

// SettingsGet GET /admin/settings：键序=golden 锚定（白名单序-秘密键+has_* 尾缀）
// 毛边照抄：sso_has_* 在 pop 之后计算 → 恒 false（Python 原版行为）
func (h *Handler) SettingsGet(c *gin.Context) {
	m, err := h.store.LoadSettings(c.Request.Context())
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if m["ai_provider"] == "" {
		m["ai_provider"] = "bedrock"
	}
	if m["bedrock_model_id"] == "" {
		m["bedrock_model_id"] = h.bedDef.ModelID
	}
	if m["bedrock_region"] == "" {
		m["bedrock_region"] = h.bedDef.Region
	}
	if m["bedrock_auth_mode"] == "" {
		m["bedrock_auth_mode"] = "iam_role"
	}
	if m["image_storage_mode"] == "" {
		m["image_storage_mode"] = "local"
	}
	hasAKSK := m["bedrock_secret_key"] != ""
	hasAPIKey := m["bedrock_api_key"] != ""
	hasOpenAI := m["openai_api_key"] != ""
	hasS3SK := m["image_s3_secret_key"] != ""

	out := httpx.Obj{}
	for _, k := range settingKeys { // 白名单序，跳过 6 个秘密键（pop 语义）
		if secretKeys[k] {
			continue
		}
		out = out.Set(k, m[k])
	}
	out = out.Set("bedrock_has_ak_sk", hasAKSK).
		Set("bedrock_has_api_key", hasAPIKey).
		Set("openai_has_key", hasOpenAI).
		Set("sso_has_github_secret", false). // 毛边：Python 在 pop 后计算，恒 false
		Set("sso_has_google_secret", false). // 同上
		Set("image_has_s3_secret", hasS3SK)
	httpx.WriteJSON(c, http.StatusOK, out)
}

// SettingsPut PUT /admin/settings（白名单过滤/秘密空串跳过/__CLEAR__ 清空）
func (h *Handler) SettingsPut(c *gin.Context) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		body = map[string]any{}
	}
	data := map[string]string{}
	for k, v := range body {
		if s, ok := v.(string); ok {
			data[k] = s
		}
	}
	if err := h.store.SaveSettings(c.Request.Context(), data); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "配置已保存"})
}

// ── ai-models ──

// AIMModelsGet GET /admin/ai-models：ai_models 键的 JSON 整包（空/损坏 → []）
func (h *Handler) AIMModelsGet(c *gin.Context) {
	raw, err := h.store.GetRaw(c.Request.Context(), "ai_models")
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	out := []byte(strings.TrimSpace(raw))
	if len(out) == 0 || !json.Valid(out) {
		out = []byte("[]")
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", out)
}

// AIMModelsPut PUT /admin/ai-models {models:[...]}：整包存储
func (h *Handler) AIMModelsPut(c *gin.Context) {
	raw, _ := io.ReadAll(c.Request.Body)
	var body struct {
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Models) == 0 {
		writeErr(c, httpx.New(http.StatusBadRequest, "models 不能为空"))
		return
	}
	if err := h.store.PutRaw(c.Request.Context(), "ai_models", string(body.Models)); err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "模型列表已保存"})
}

// aiModelAvailable GET /ai-models/available 的条目（字段顺序=契约）
type aiModelAvailable struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderName string `json:"provider_name"`
	ProviderType string `json:"provider_type"`
}

// AIAvailable GET /ai-models/available：展平 providers×models
func (h *Handler) AIAvailable(c *gin.Context) {
	raw, _ := h.store.GetRaw(c.Request.Context(), "ai_models")
	out := []aiModelAvailable{}
	if s := strings.TrimSpace(raw); s != "" {
		var providers []struct {
			Name   string `json:"name"`
			Type   string `json:"type"`
			Models []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.Unmarshal([]byte(s), &providers) == nil {
			for _, p := range providers {
				for _, m := range p.Models {
					out = append(out, aiModelAvailable{ID: m.ID, Name: m.Name, ProviderName: p.Name, ProviderType: p.Type})
				}
			}
		}
	}
	if out == nil {
		out = []aiModelAvailable{}
	}
	httpx.WriteJSON(c, http.StatusOK, out)
}

// ── blacklist ──

// BlacklistList GET /admin/blacklist?search=&page=1&page_size=20(1..200)
func (h *Handler) BlacklistList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	pg, err := h.store.ListBlacklist(c.Request.Context(), c.Query("search"), page, size)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	httpx.WriteJSON(c, http.StatusOK, pg)
}

// BlacklistAdd POST {email, reason=""}
func (h *Handler) BlacklistAdd(c *gin.Context) {
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	email := strings.ToLower(str(body["email"]))
	if email == "" {
		writeErr(c, httpx.New(http.StatusBadRequest, "邮箱不能为空"))
		return
	}
	reason := str(body["reason"])
	dup, err := h.store.AddBlacklist(c.Request.Context(), email, reason)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if dup {
		writeErr(c, httpx.New(http.StatusBadRequest, email+" 已在黑名单中"))
		return
	}
	h.bl.Add(email)
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "已添加 " + email + " 到黑名单"})
}

// BlacklistDelete DELETE /:id
func (h *Handler) BlacklistDelete(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	email, ok, err := h.store.DeleteBlacklistByID(c.Request.Context(), id)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	if !ok {
		writeErr(c, httpx.New(http.StatusNotFound, "记录不存在"))
		return
	}
	h.bl.Remove(email)
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "已从黑名单移除 " + email})
}

// BlacklistBatchDelete POST {ids:[]}
func (h *Handler) BlacklistBatchDelete(c *gin.Context) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	_ = c.ShouldBindJSON(&body)
	if len(body.IDs) == 0 {
		writeErr(c, httpx.New(http.StatusBadRequest, "未选择记录"))
		return
	}
	n, err := h.store.DeleteBlacklistByIDs(c.Request.Context(), body.IDs)
	if err != nil {
		writeErr(c, httpx.New(http.StatusInternalServerError, err.Error()))
		return
	}
	h.bl.ReloadAll(c.Request.Context()) // 批量删后全量重载（Python 语义）
	httpx.WriteJSON(c, http.StatusOK, gin.H{"message": "已删除 " + strconv.FormatInt(n, 10) + " 条记录"})
}

// BlacklistCount GET /count（读内存缓存）
func (h *Handler) BlacklistCount(c *gin.Context) {
	httpx.WriteJSON(c, http.StatusOK, gin.H{"count": h.bl.Count()})
}

// ── 小工具 ──

func str(v any) string {
	s, _ := v.(string)
	return s
}

func writeErr(c *gin.Context, e *httpx.Error) {
	httpx.WriteJSON(c, e.Status, gin.H{"detail": e.Detail})
}
