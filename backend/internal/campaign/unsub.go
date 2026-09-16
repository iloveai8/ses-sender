package campaign

import (
	"context"
	"database/sql"
	"strings"
)

// UnsubItem 退订记录行（键序=契约 id,email,source_email,reason,unsubscribed_at）
type UnsubItem struct {
	ID             int     `json:"id"`
	Email          string  `json:"email"`
	SourceEmail    string  `json:"source_email"`
	Reason         string  `json:"reason"`
	UnsubscribedAt *string `json:"unsubscribed_at"`
}

// ListUnsub 退订列表（admin 全量；非 admin 限本人发件邮箱；email LIKE 搜索；id 倒序）
func (s *Store) ListUnsub(ctx context.Context, userID int, admin bool, userEmail, search string, page, pageSize int) ([]UnsubItem, int, error) {
	where, args := []string{"1=1"}, []any{}
	if !admin {
		if userEmail == "" {
			return []UnsubItem{}, 0, nil // Python：无发件邮箱 → 空集
		}
		where = append(where, "source_email = ?")
		args = append(args, userEmail)
	}
	if search != "" {
		where = append(where, "email LIKE ?")
		args = append(args, "%"+search+"%")
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM unsubscribe_list WHERE "+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, source_email, COALESCE(reason,''), unsubscribed_at
		 FROM unsubscribe_list WHERE `+w+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []UnsubItem{}
	for rows.Next() {
		var it UnsubItem
		var at sql.NullTime
		if err := rows.Scan(&it.ID, &it.Email, &it.SourceEmail, &it.Reason, &at); err != nil {
			return nil, 0, err
		}
		it.UnsubscribedAt = nullTime(at)
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// GetUnsub 取单条（权限判定用）
func (s *Store) GetUnsub(ctx context.Context, id int) (*UnsubItem, error) {
	var it UnsubItem
	var at sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, source_email, COALESCE(reason,''), unsubscribed_at FROM unsubscribe_list WHERE id = ?`, id).
		Scan(&it.ID, &it.Email, &it.SourceEmail, &it.Reason, &at)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	it.UnsubscribedAt = nullTime(at)
	return &it, nil
}

// DeleteUnsub 恢复（删退订记录）
func (s *Store) DeleteUnsub(ctx context.Context, id int) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM unsubscribe_list WHERE id = ?", id)
	return err
}

// DeleteUnsubByIDs 批量恢复（权限内的 ids；返回恢复数）
func (s *Store) DeleteUnsubByIDs(ctx context.Context, ids []int64, admin bool, userEmail string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	for _, v := range ids {
		args = append(args, v)
	}
	q := "DELETE FROM unsubscribe_list WHERE id IN (" + ph + ")"
	if !admin {
		q += " AND source_email = ?"
		args = append(args, userEmail)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UnsubEmailSet 建单退订过滤：某发件邮箱的全部退订集合（P9 send_bulk 消费）
func (s *Store) UnsubEmailSet(ctx context.Context, sourceEmail string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT email FROM unsubscribe_list WHERE source_email = ?", sourceEmail)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out[strings.ToLower(e)] = struct{}{}
	}
	return out, rows.Err()
}
