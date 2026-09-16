// Package contact 受众域：客群/联系人的数据访问与 HTTP 适配。
package contact

import (
	"context"
	"database/sql"
	"strings"
)

// GroupOut 客群对外形状（字段顺序=golden 契约，禁改）
type GroupOut struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	UserID       int     `json:"user_id"`
	ContactCount int     `json:"contact_count"`
}

// ContactOut 联系人对外形状（字段顺序=golden 契约，禁改）
type ContactOut struct {
	ID         int     `json:"id"`
	Email      string  `json:"email"`
	Name       *string `json:"name"`
	Attributes *string `json:"attributes"`
	GroupID    int     `json:"group_id"`
}

// Store 客群/联系人数据访问（手写预处理语句；排序/搜索口径与 Python 版一致）
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ListGroups 客群分页（搜索仅匹配 name；id 倒序；contact_count 关联子查询）
func (s *Store) ListGroups(ctx context.Context, userID int, search string, page, pageSize int) ([]GroupOut, int, error) {
	where, args := "user_id = ?", []any{userID}
	if search != "" {
		where += " AND name LIKE ?"
		args = append(args, "%"+search+"%")
	}
	var total int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM contact_groups WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := "SELECT id, name, description, user_id, (SELECT COUNT(*) FROM contacts WHERE group_id = contact_groups.id) " +
		"FROM contact_groups WHERE " + where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []GroupOut{}
	for rows.Next() {
		var g GroupOut
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.UserID, &g.ContactCount); err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	return out, total, rows.Err()
}

// GetGroup 按归属取客群（他人客群视同不存在——Python 语义）
func (s *Store) GetGroup(ctx context.Context, userID, id int) (*GroupOut, error) {
	var g GroupOut
	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, description, user_id, (SELECT COUNT(*) FROM contacts WHERE group_id = contact_groups.id) "+
			"FROM contact_groups WHERE id = ? AND user_id = ?", id, userID).
		Scan(&g.ID, &g.Name, &g.Description, &g.UserID, &g.ContactCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// CreateGroup 建客群（contact_count=0）
func (s *Store) CreateGroup(ctx context.Context, userID int, name string, desc *string) (*GroupOut, error) {
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO contact_groups (name, description, user_id) VALUES (?,?,?)", name, desc, userID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	var g GroupOut
	if err := s.db.QueryRowContext(ctx,
		"SELECT id, name, description, user_id, 0 FROM contact_groups WHERE id = ?", id).
		Scan(&g.ID, &g.Name, &g.Description, &g.UserID, &g.ContactCount); err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateGroup 按需更新（nil 跳过）；不存在/无权返回 ok=false
func (s *Store) UpdateGroup(ctx context.Context, userID, id int, name *string, desc *string) (bool, error) {
	set, args := []string{}, []any{}
	if name != nil {
		set = append(set, "name = ?")
		args = append(args, *name)
	}
	if desc != nil {
		set = append(set, "description = ?")
		args = append(args, *desc)
	}
	if len(set) == 0 {
		_, err := s.GetGroup(ctx, userID, id)
		return err == nil, nil
	}
	args = append(args, id, userID)
	res, err := s.db.ExecContext(ctx,
		"UPDATE contact_groups SET "+strings.Join(set, ", ")+" WHERE id = ? AND user_id = ?", args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteGroup 删客群 + 级联删联系人（同事务——Python 级联语义）
func (s *Store) DeleteGroup(ctx context.Context, userID, id int) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM contact_groups WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM contacts WHERE group_id = ?", id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ListContacts 联系人分页（搜索 name OR email；id 倒序）
func (s *Store) ListContacts(ctx context.Context, groupID int, search string, page, pageSize int) ([]ContactOut, int, error) {
	where, args := "group_id = ?", []any{groupID}
	if search != "" {
		where += " AND (name LIKE ? OR email LIKE ?)"
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	var total int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM contacts WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, email, name, attributes, group_id FROM contacts WHERE "+where+" ORDER BY id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ContactOut{}
	for rows.Next() {
		var c ContactOut
		if err := rows.Scan(&c.ID, &c.Email, &c.Name, &c.Attributes, &c.GroupID); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// CreateContact 建联系人（归属校验由调用方完成）
func (s *Store) CreateContact(ctx context.Context, email string, name, attributes *string, groupID int) (*ContactOut, error) {
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO contacts (email, name, attributes, group_id) VALUES (?,?,?,?)", email, name, attributes, groupID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &ContactOut{ID: int(id), Email: email, Name: name, Attributes: attributes, GroupID: groupID}, nil
}

// DeleteContact 删联系人（按归属 join 校验；miss → ok=false → 404 联系人不存在）
func (s *Store) DeleteContact(ctx context.Context, userID, contactID int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM contacts WHERE id = ? AND group_id IN (SELECT id FROM contact_groups WHERE user_id = ?)`,
		contactID, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
