// Package template 模板域：模板 CRUD（SES 双写）与附件。
package template

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// TemplateOut 模板对外形状（字段顺序=golden 契约，禁改）
type TemplateOut struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`
	Subject   string  `json:"subject"`
	HTMLBody  string  `json:"html_body"`
	CreatedAt *string `json:"created_at"` // isoformat 无 Z（JSONTime 语义，此处以字符串直出）
}

// AttachmentOut 附件元数据（字段顺序=契约 id,file_name,content_type,file_size,created_at）
type AttachmentOut struct {
	ID          int     `json:"id"`
	FileName    string  `json:"file_name"`
	ContentType string  `json:"content_type"`
	FileSize    int     `json:"file_size"`
	CreatedAt   *string `json:"created_at"`
}

// tplRow 内部行（含 ses_name 等不出网字段）
type tplRow struct {
	ID       int
	Name     string
	SesName  string
	Subject  string
	HTMLBody string
	TextBody string
	Created  sql.NullTime
}

func (r tplRow) out() TemplateOut {
	var ts *string
	if r.Created.Valid {
		s := r.Created.Time.UTC().Format("2006-01-02T15:04:05") // Python isoformat 等价（无 Z）
		ts = &s
	}
	return TemplateOut{ID: r.ID, Name: r.Name, Subject: r.Subject, HTMLBody: r.HTMLBody, CreatedAt: ts}
}

// Store 模板数据访问
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const tplCols = "id, name, ses_name, subject, html_body, COALESCE(text_body,''), created_at"

func scanTpl(row interface{ Scan(...any) error }) (*tplRow, error) {
	var r tplRow
	if err := row.Scan(&r.ID, &r.Name, &r.SesName, &r.Subject, &r.HTMLBody, &r.TextBody, &r.Created); err != nil {
		return nil, err
	}
	return &r, nil
}

// List 用户全部模板（id 倒序——golden 证据：admin-templates-list [id2, id1]）
func (s *Store) List(ctx context.Context, userID int) ([]TemplateOut, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+tplCols+" FROM email_templates WHERE user_id = ? ORDER BY id DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TemplateOut{}
	for rows.Next() {
		r, err := scanTpl(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r.out())
	}
	return out, rows.Err()
}

// Get 按归属取模板
func (s *Store) Get(ctx context.Context, userID, id int) (*tplRow, error) {
	r, err := scanTpl(s.db.QueryRowContext(ctx,
		"SELECT "+tplCols+" FROM email_templates WHERE id = ? AND user_id = ?", id, userID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// GenSesName 生成 SES 模板名：u{userID}_{12hex}（Python 同构）
func GenSesName(userID int) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("u%d_%s", userID, hex.EncodeToString(b)), nil
}

// Create 建模板（SES 调用由 handler 先行完成；此处只落库）
func (s *Store) Create(ctx context.Context, userID int, name, sesName, subject, html, text string) (*TemplateOut, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO email_templates (name, ses_name, subject, html_body, text_body, user_id, created_at)
		 VALUES (?,?,?,?,?,?,?)`, name, sesName, subject, html, text, userID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	r, err := scanTpl(s.db.QueryRowContext(ctx, "SELECT "+tplCols+" FROM email_templates WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	out := r.out()
	return &out, nil
}

// Update 更新主题/正文（text=html or " "——Python 语义）
func (s *Store) Update(ctx context.Context, userID, id int, subject, html *string) (*tplRow, error) {
	r, err := s.Get(ctx, userID, id)
	if err != nil || r == nil {
		return nil, err
	}
	if subject != nil {
		r.Subject = *subject
	}
	if html != nil {
		r.HTMLBody = *html
	}
	r.TextBody = r.HTMLBody
	if r.TextBody == "" {
		r.TextBody = " "
	}
	_, err = s.db.ExecContext(ctx,
		"UPDATE email_templates SET subject = ?, html_body = ?, text_body = ? WHERE id = ? AND user_id = ?",
		r.Subject, r.HTMLBody, r.TextBody, id, userID)
	return r, err
}

// Delete 删本地模板
func (s *Store) Delete(ctx context.Context, userID, id int) (bool, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM email_templates WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListAttachments 附件元数据列表
func (s *Store) ListAttachments(ctx context.Context, userID, tplID int) ([]AttachmentOut, error) {
	if _, err := s.Get(ctx, userID, tplID); err != nil || tplID == 0 {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, file_name, content_type, COALESCE(file_size,0), created_at
		 FROM template_attachments WHERE template_id = ? ORDER BY id`, tplID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AttachmentOut{}
	for rows.Next() {
		var a AttachmentOut
		var created sql.NullTime
		if err := rows.Scan(&a.ID, &a.FileName, &a.ContentType, &a.FileSize, &created); err != nil {
			return nil, err
		}
		if created.Valid {
			s := created.Time.UTC().Format("2006-01-02T15:04:05")
			a.CreatedAt = &s
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAttachment 删附件（模板归属 + 附件存在双重校验）
func (s *Store) DeleteAttachment(ctx context.Context, userID, tplID, attID int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM template_attachments WHERE id = ? AND template_id = ?
		 AND template_id IN (SELECT id FROM email_templates WHERE user_id = ?)`, attID, tplID, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
