package database

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"
)

// alembicHead 当前 Python 侧迁移链头（双系统并存的地标；落后则告警人工介入）
const alembicHead = "e3f4a5b6c7d7"

// Bootstrap 启动迁移引导（三态，见 docs/v2/04-DATA-MODEL.md §4）：
//
//	全新空库            → goose up（应用基线 00001，一步建 11 表）
//	存量 alembic 库      → 校验 head 后，建 goose 表并把基线标记为已应用（零 DDL，生产表不动）
//	已有 goose 表        → goose up（增量，00002 起）
//
// 失败仅告警不阻塞启动（对齐 Python 版 alembic 检查失败不阻止启动的行为）
func Bootstrap(db *sql.DB, migrationsDir string) error {
	mustUse(db)
	if err := goose.SetDialect("mysql"); err != nil {
		return err
	}

	hasGoose, err := tableExists(db, "goose_db_version")
	if err != nil {
		return err
	}
	if hasGoose {
		return goose.Up(db, migrationsDir) // 常态：增量
	}

	hasAlembic, err := tableExists(db, "alembic_version")
	if err != nil {
		return err
	}
	if hasAlembic {
		var ver string
		if err := db.QueryRow("SELECT version_num FROM alembic_version LIMIT 1").Scan(&ver); err != nil {
			return fmt.Errorf("读取 alembic 版本: %w", err)
		}
		if ver != alembicHead {
			slog.Warn("alembic 版本与预期 head 不一致，跳过迁移引导（冻结期不允许，请人工介入）",
				"actual", ver, "expect", alembicHead)
			return nil
		}
		// 标记基线已应用：建 goose 表并写入 version=1（零 DDL，不动业务表）
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS goose_db_version (
			id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
			version_id BIGINT NOT NULL, is_applied BOOLEAN NOT NULL, tstamp TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
			return err
		}
		_, err := db.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, TRUE)")
		return err
	}

	return goose.Up(db, migrationsDir) // 全新库
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", name).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// mustUse goose 内部需要 driver 名；显式确保（重复调用无害）
func mustUse(db *sql.DB) {
	_ = goose.SetDialect("mysql")
}
