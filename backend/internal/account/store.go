package account

import (
	"context"
	"database/sql"
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
