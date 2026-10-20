package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate applies every pending embedded migration in order. Each migration is
// one transaction: a failure rolls back that file and records nothing, while
// already applied versions remain valid and are skipped on retry. The database
// is only marked ready after every migration has committed.
func (s *Store) Migrate(ctx context.Context) error {
	s.migrate.Lock()
	defer s.migrate.Unlock()

	if err := applyMigrations(ctx, s.writer, embeddedMigrations); err != nil {
		s.ready.Store(false)
		return err
	}
	s.ready.Store(true)
	return nil
}

func applyMigrations(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER NOT NULL PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("创建 schema_migrations: %w", err)
	}

	migrations, err := loadMigrations(fsys)
	if err != nil {
		return err
	}

	applied := map[int]string{}
	rows, err := db.QueryContext(ctx, `SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("读取迁移记录: %w", err)
	}
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("读取迁移记录: %w", err)
		}
		applied[version] = name
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("读取迁移记录: %w", err)
	}
	_ = rows.Close()

	known := make(map[int]migration, len(migrations))
	for _, item := range migrations {
		known[item.version] = item
	}
	for version, name := range applied {
		item, ok := known[version]
		if !ok {
			return fmt.Errorf("数据库包含当前程序未知的迁移 %d (%s)", version, name)
		}
		if item.name != name {
			return fmt.Errorf("迁移 %d 的名称不一致：数据库为 %q，程序为 %q", version, name, item.name)
		}
	}

	for _, item := range migrations {
		if _, ok := applied[item.version]; ok {
			continue
		}
		if err := applyOneMigration(ctx, db, item); err != nil {
			return err
		}
	}
	return nil
}

func applyOneMigration(ctx context.Context, db *sql.DB, item migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始迁移 %d: %w", item.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, item.sql); err != nil {
		return fmt.Errorf("执行迁移 %d (%s): %w", item.version, item.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
		item.version, item.name, formatTime(time.Now()),
	); err != nil {
		return fmt.Errorf("记录迁移 %d (%s): %w", item.version, item.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交迁移 %d (%s): %w", item.version, item.name, err)
	}
	return nil
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录: %w", err)
	}
	items := make([]migration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		separator := strings.IndexByte(name, '_')
		if separator <= 0 {
			return nil, fmt.Errorf("迁移文件名 %q 缺少版本前缀", name)
		}
		version, err := strconv.Atoi(name[:separator])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("迁移文件名 %q 的版本前缀无效", name)
		}
		body, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("读取迁移 %s: %w", name, err)
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			return nil, fmt.Errorf("迁移 %s 为空", name)
		}
		items = append(items, migration{version: version, name: name, sql: string(body)})
	}
	if len(items) == 0 {
		return nil, errors.New("没有可用的数据库迁移")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].version < items[j].version })
	for i := 1; i < len(items); i++ {
		if items[i-1].version == items[i].version {
			return nil, fmt.Errorf("迁移版本 %d 重复", items[i].version)
		}
	}
	return items, nil
}
