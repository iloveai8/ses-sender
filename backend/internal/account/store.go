package account

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// User 账户实体（hashed_password 仅服务端内部使用，永不序列化）
type User struct {
	ID             int
	Username       string
	DisplayName    string
	SenderName     *string
	Email          string
	ContactEmail   *string
	IsAdmin        bool
	IsActive       bool
	DailySendLimit int
	HashedPassword string
}

// UserOut 对外形状（/auth/me 与 /admin/users 用；字段顺序=golden 契约，禁改）
type UserOut struct {
	ID             int     `json:"id"`
	Username       string  `json:"username"`
	DisplayName    string  `json:"display_name"`
	SenderName     *string `json:"sender_name"`
	Email          string  `json:"email"`
	ContactEmail   *string `json:"contact_email"`
	IsAdmin        bool    `json:"is_admin"`
	IsActive       bool    `json:"is_active"`
	DailySendLimit int     `json:"daily_send_limit"`
}

func (u *User) Out() UserOut {
	return UserOut{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
		SenderName: u.SenderName, Email: u.Email, ContactEmail: u.ContactEmail,
		IsAdmin: u.IsAdmin, IsActive: u.IsActive, DailySendLimit: u.DailySendLimit,
	}
}

// Store 用户数据访问（暂为手写预处理语句；sqlc 工具链接入后迁移为生成物——语义不变）
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const userCols = `id, username, display_name, sender_name, COALESCE(email,''), contact_email,
	is_admin, is_active, COALESCE(daily_send_limit,1000), hashed_password`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.SenderName, &u.Email, &u.ContactEmail,
		&u.IsAdmin, &u.IsActive, &u.DailySendLimit, &u.HashedPassword); err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetByUsername(ctx context.Context, username string) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE username = ?", username))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (s *Store) GetByID(ctx context.Context, id int) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// List 全量用户（按 id 升序，与 Python 版一致）
func (s *Store) List(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateParams 建用户参数（必填 username/display_name/password/email；其余有默认）
type CreateParams struct {
	Username, DisplayName, Password, Email string
	ContactEmail                           *string
	IsAdmin                                bool
	DailySendLimit                         int
	SenderName                             *string
}

// Create 建用户；用户名重复返回 dup=true（调用方落 400 用户名已存在）
func (s *Store) Create(ctx context.Context, p CreateParams) (*User, bool, error) {
	hash, err := HashPassword(p.Password)
	if err != nil {
		return nil, false, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, display_name, hashed_password, email, contact_email, is_admin, is_active, daily_send_limit, sender_name, created_at)
		 VALUES (?,?,?,?,?,?,TRUE,?,?,?)`,
		p.Username, p.DisplayName, hash, p.Email, p.ContactEmail, p.IsAdmin, p.DailySendLimit, p.SenderName, time.Now().UTC())
	if err != nil {
		if isDuplicateKey(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	u, err := s.GetByID(ctx, int(id))
	return u, false, err
}

// UpdateProfile 按需更新（nil 字段跳过——Python UserUpdate 全可选语义）
func (s *Store) UpdateProfile(ctx context.Context, id int, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	set := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+1)
	for k, v := range fields {
		set = append(set, k+" = ?")
		args = append(args, v)
	}
	args = append(args, id)
	_, err := s.db.ExecContext(ctx, "UPDATE users SET "+strings.Join(set, ", ")+" WHERE id = ?", args...)
	return err
}

// GetUnsubConfig 用户退订页配置原始 JSON（空/损坏由调用方兜 {}）
func (s *Store) GetUnsubConfig(ctx context.Context, userID int) (string, error) {
	var cfg sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT unsub_config FROM users WHERE id = ?", userID).Scan(&cfg)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return cfg.String, err
}

// SetUnsubConfig 原样存 JSON（Python 语义：任意 dict 原文保存）
func (s *Store) SetUnsubConfig(ctx context.Context, userID int, raw []byte) error {
	_, err := s.db.ExecContext(ctx, "UPDATE users SET unsub_config = ? WHERE id = ?", string(raw), userID)
	return err
}

// isDuplicateKey MySQL 1062 唯一键冲突
func isDuplicateKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Error 1062")
}

// EnsureDefaultAdmin 启动种子：无 admin 用户则创建（admin/admin123，与 Python 版一致）。
// 顺带是跨语言 bcrypt 兼容的自然验证点：若库里的 admin 是 Python 建的，这里直接跳过。
func EnsureDefaultAdmin(ctx context.Context, db *sql.DB) error {
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE username='admin'").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := HashPassword("admin123")
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO users (username, display_name, hashed_password, email, is_admin, is_active, daily_send_limit, created_at)
		 VALUES ('admin', '管理员', ?, '', TRUE, TRUE, 1000, ?)`, hash, time.Now().UTC())
	return err
}
