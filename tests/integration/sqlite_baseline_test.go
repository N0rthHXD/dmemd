package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSQLiteDependencyBaseline is the dependency baseline probe. It uses a real temporary
// SQLite file through the pinned driver and verifies the capabilities the
// architecture depends on: version reporting, WAL, foreign keys and FTS5.
func TestSQLiteDependencyBaseline(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "baseline.db")
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	var sqliteVersion string
	if err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&sqliteVersion); err != nil {
		t.Fatalf("sqlite_version: %v", err)
	}

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode=WAL: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	if _, err := db.ExecContext(ctx, "CREATE TABLE parent(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id))"); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO child(id, parent_id) VALUES(1, 99)"); err == nil {
		t.Fatal("foreign key violation was accepted")
	}

	if _, err := db.ExecContext(ctx, "CREATE VIRTUAL TABLE baseline_fts USING fts5(title, content)"); err != nil {
		t.Fatalf("create FTS5 table: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO baseline_fts(title, content) VALUES(?, ?)", "memory baseline", "local memory service"); err != nil {
		t.Fatalf("insert FTS5 row: %v", err)
	}
	var matches int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM baseline_fts WHERE baseline_fts MATCH ?", "memory").Scan(&matches); err != nil {
		t.Fatalf("FTS5 query: %v", err)
	}
	if matches != 1 {
		t.Fatalf("FTS5 matches = %d, want 1", matches)
	}

	t.Logf("sqlite_version=%s journal_mode=%s foreign_keys=%d fts5=ok", sqliteVersion, journalMode, foreignKeys)
}
