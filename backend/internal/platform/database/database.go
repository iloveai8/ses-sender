// Package database MySQL 连接与 goose 迁移引导（三态：全新库建表 / 存量 alembic 库标记基线 / goose 增量）。
package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"ses-sender/internal/platform/config"
)

// Open 建立连接池并探活（DSN 由 config.Database.DSN() 统一翻译，parseTime/UTC/utf8mb4 已附带）
func Open(cfg *config.Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库探活失败: %w", err)
	}
	return db, nil
}
