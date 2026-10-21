package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestOpenConfiguresWriterAndEveryReadConnection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if got := store.writer.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer MaxOpenConnections = %d, want 1", got)
	}
	if got := store.reader.Stats().MaxOpenConnections; got != 4 {
		t.Fatalf("reader MaxOpenConnections = %d, want 4", got)
	}
	if err := verifyConnectionSettings(ctx, store.writer, 5*time.Second, false); err != nil {
		t.Fatalf("writer settings: %v", err)
	}

	connections := make([]*sql.Conn, 4)
	for i := range connections {
		conn, err := store.reader.Conn(ctx)
		if err != nil {
			t.Fatalf("reader conn %d: %v", i, err)
		}
		connections[i] = conn
		defer conn.Close()
		if err := verifyConnectionSettings(ctx, conn, 5*time.Second, true); err != nil {
			t.Fatalf("reader conn %d settings: %v", i, err)
		}
	}
	// A read-pool connection must not be able to mutate the database.
	if _, err := connections[0].ExecContext(ctx, "CREATE TABLE reader_write_attempt(id INTEGER)"); err == nil {
		t.Fatal("read pool accepted a write")
	}

	// Replace a pooled connection and verify the DSN settings are still applied.
	if err := connections[0].Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.reader.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := verifyConnectionSettings(ctx, replacement, 5*time.Second, true); err != nil {
		t.Fatalf("replacement connection settings: %v", err)
	}
}

func TestDatabaseAndSidecarPermissions(t *testing.T) {
	store := newTestStore(t)
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		return tx.InsertSource(ctx, sourceForPermissions())
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPermissions(); err != nil {
		t.Fatalf("CheckPermissions: %v", err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("database mode = %04o, want 0600", mode)
	}
	info, err = os.Stat(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("database directory mode = %04o, want 0700", mode)
	}

	walPath := store.Path() + "-wal"
	if _, err := os.Stat(walPath); err != nil {
		t.Fatalf("WAL sidecar missing after write: %v", err)
	}
	if err := os.Chmod(walPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPermissions(); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("CheckPermissions with 0644 WAL = %v, want error", err)
	}
	if err := os.Chmod(walPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPermissions(); err != nil {
		t.Fatalf("CheckPermissions after restoring WAL: %v", err)
	}
}

func TestReadyRequiresMigrationAndWorkingPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dmemd.db")
	store, err := Open(context.Background(), Config{Path: path, BusyTimeout: 5 * time.Second, MaxReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(context.Background()); err == nil {
		t.Fatal("Ready succeeded before migrations")
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("Ready after migration: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(context.Background()); err == nil {
		t.Fatal("Ready succeeded after Close")
	}
}

func TestReopenUsesExistingMigrationsAndData(t *testing.T) {
	store := newTestStore(t)
	path := store.Path()
	created := seedSource(t, store, "source-reopen")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), Config{Path: path, BusyTimeout: 5 * time.Second, MaxReadConnections: 4})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat Migrate: %v", err)
	}
	loaded, err := reopened.GetSource(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetSource after reopen: %v", err)
	}
	if string(loaded.Content) != string(created.Content) {
		t.Fatalf("content = %s, want %s", loaded.Content, created.Content)
	}
	var count int
	if err := reopened.writer.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("schema_migrations count = %d, want 3", count)
	}
}

func TestOpenRejectsEmptyPathAndCreatesPrivateDirectory(t *testing.T) {
	if _, err := Open(context.Background(), Config{}); err == nil {
		t.Fatal("Open accepted an empty path")
	}

	path := filepath.Join(t.TempDir(), "nested", "state", "dmemd.db")
	store, err := Open(context.Background(), Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPermissions(); err != nil {
		t.Fatalf("CheckPermissions: %v", err)
	}
}

func TestOpenAcceptsURISpecialCharactersInPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a #1 中文", "dmemd.db")
	store, err := Open(context.Background(), Config{Path: path})
	if err != nil {
		t.Fatalf("Open with URI characters: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate with URI characters: %v", err)
	}
	if err := store.CheckPermissions(); err != nil {
		t.Fatalf("CheckPermissions: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file missing: %v", err)
	}
}

func sourceForPermissions() source.Source {
	return source.Source{ID: "source-permissions", Content: []byte(`"content"`), CreatedAt: testTime(0)}
}
