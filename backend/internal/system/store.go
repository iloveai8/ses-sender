// Package system 系统管理域：settings / ai-models / 黑名单（含内存缓存）。
package system

import (
	"context"
	"database/sql"
	"strings"
)

// settingKeys 36 键白名单（顺序=Python SETTING_KEYS，响应键序的依据）
var settingKeys = []string{
	"ai_provider", "bedrock_model_id", "bedrock_region", "bedrock_auth_mode",
	"bedrock_access_key", "bedrock_secret_key", "bedrock_api_key",
	"openai_api_base", "openai_api_key", "openai_model",
	"image_storage_mode", "image_s3_bucket", "image_s3_region", "image_s3_prefix",
	"image_s3_access_key", "image_s3_secret_key", "image_base_url",
	"unsub_page_title", "unsub_page_subtitle", "unsub_page_reasons",
	"unsub_page_success", "unsub_page_logo", "unsub_page_color", "unsub_page_button_text",
	"sso_github_enabled", "sso_github_client_id", "sso_github_client_secret",
	"sso_google_enabled", "sso_google_client_id", "sso_google_client_secret",
	"sso_saml_enabled", "sso_saml_idp_entity_id", "sso_saml_idp_sso_url",
	"sso_saml_idp_cert", "sso_saml_sp_entity_id",
}

var settingKeySet = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range settingKeys {
		m[k] = true
	}
	return m
}()

// secretKeys 秘密键：GET 脱敏（pop 后以 *_has_* 布尔呈现）；PUT 空串=跳过、__CLEAR__=清空
var secretKeys = map[string]bool{
	"bedrock_secret_key": true, "bedrock_api_key": true, "openai_api_key": true,
	"image_s3_secret_key": true, "sso_github_client_secret": true, "sso_google_client_secret": true,
}

// Store system 域数据访问
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// LoadSettings 全量设置（缺失键补 ""）——默认注入与脱敏由 handler 完成。
// 查全表后在白名单内过滤（表小，避免拼 36 个占位符）
func (s *Store) LoadSettings(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range settingKeys {
		out[k] = ""
	}
	rows, err := s.db.QueryContext(ctx, "SELECT `key`, COALESCE(value,'') FROM system_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if settingKeySet[k] {
			out[k] = v
		}
	}
	return out, rows.Err()
}

// SaveSettings 白名单写入：秘密键空串=跳过；"__CLEAR__"=清空；upsert
func (s *Store) SaveSettings(ctx context.Context, data map[string]string) error {
	for _, key := range settingKeys { // 按 SETTING_KEYS 顺序写（与 Python 一致，行为等价）
		val, ok := data[key]
		if !ok {
			continue
		}
		if secretKeys[key] && val == "" {
			continue
		}
		if val == "__CLEAR__" {
			val = ""
		}
		if _, err := s.db.ExecContext(ctx,
			"INSERT INTO system_settings (`key`, value) VALUES (?,?) ON DUPLICATE KEY UPDATE value = VALUES(value)",
			key, val); err != nil {
			return err
		}
	}
	return nil
}

// GetRaw 读任意原始设置值（ai_models 用；空库返回 ""）
func (s *Store) GetRaw(ctx context.Context, key string) (string, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT value FROM system_settings WHERE `key` = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v.String, err
}

// PutRaw 写原始设置值
func (s *Store) PutRaw(ctx context.Context, key, val string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO system_settings (`key`, value) VALUES (?,?) ON DUPLICATE KEY UPDATE value = VALUES(value)", key, val)
	return err
}

// ── 黑名单 ──

// BlacklistItem 黑名单行（字段顺序=契约 id,email,reason,created_by,created_at）
type BlacklistItem struct {
	ID        int     `json:"id"`
	Email     string  `json:"email"`
	Reason    string  `json:"reason"`
	CreatedBy string  `json:"created_by"`
	CreatedAt *string `json:"created_at"`
}

// BlacklistPage 分页（毛边：无 page_size 键——golden 锚定）
type BlacklistPage struct {
	Items      []BlacklistItem `json:"items"`
	Total      int             `json:"total"`
	PageNum    int             `json:"page"`
	TotalPages int             `json:"total_pages"`
}

// ListBlacklist 分页+搜索（email LIKE；id 倒序——Python 口径）
func (s *Store) ListBlacklist(ctx context.Context, search string, page, pageSize int) (BlacklistPage, error) {
	where, args := "1=1", []any{}
	if search != "" {
		where = "email LIKE ?"
		args = append(args, "%"+search+"%")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM email_blacklist WHERE "+where, args...).Scan(&total); err != nil {
		return BlacklistPage{}, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, COALESCE(reason,''), COALESCE(created_by,'admin'), created_at
		 FROM email_blacklist WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return BlacklistPage{}, err
	}
	defer rows.Close()
	items := []BlacklistItem{}
	for rows.Next() {
		var it BlacklistItem
		var created sql.NullTime
		if err := rows.Scan(&it.ID, &it.Email, &it.Reason, &it.CreatedBy, &created); err != nil {
			return BlacklistPage{}, err
		}
		if created.Valid {
			ts := created.Time.UTC().Format("2006-01-02T15:04:05")
			it.CreatedAt = &ts
		}
		items = append(items, it)
	}
	tp := 1
	if pageSize > 0 {
		if v := (total + pageSize - 1) / pageSize; v > 1 {
			tp = v
		}
	}
	return BlacklistPage{Items: items, Total: total, PageNum: page, TotalPages: tp}, rows.Err()
}

// GetBlacklistEmail 取邮箱（删除响应文案用；miss 返回 ""）
func (s *Store) GetBlacklistEmail(ctx context.Context, id int) (string, error) {
	var email string
	err := s.db.QueryRowContext(ctx, "SELECT email FROM email_blacklist WHERE id = ?", id).Scan(&email)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return email, err
}

// AddBlacklist 加黑名单（重复返回 dup=true——唯一键判定）
func (s *Store) AddBlacklist(ctx context.Context, email, reason string) (bool, error) {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO email_blacklist (email, reason, created_by) VALUES (?,?, 'admin')", email, reason)
	if err != nil {
		if strings.Contains(err.Error(), "Error 1062") {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// DeleteBlacklistByID 删除；返回被删邮箱（miss 返回 ok=false）
func (s *Store) DeleteBlacklistByID(ctx context.Context, id int) (string, bool, error) {
	email, err := s.GetBlacklistEmail(ctx, id)
	if err != nil || email == "" {
		return "", false, err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM email_blacklist WHERE id = ?", id); err != nil {
		return "", false, err
	}
	return email, true, nil
}

// DeleteBlacklistByIDs 批量删除（返回实际删除数）
func (s *Store) DeleteBlacklistByIDs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, v := range ids {
		args[i] = v
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM email_blacklist WHERE id IN ("+ph+")", args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LoadBlacklistAll 全量邮箱（内存缓存初始化/刷新用）
func (s *Store) LoadBlacklistAll(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT email FROM email_blacklist")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
