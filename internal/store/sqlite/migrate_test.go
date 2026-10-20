package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestMigrationsCreateCoreAndSearchSchema(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for _, table := range []string{
		"memories", "memory_revisions", "sources", "evidence", "revision_evidence",
		"ingest_jobs", "schema_migrations", "memory_search_docs", "memory_fts",
	} {
		var name string
		err := store.writer.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	var versions int
	if err := store.writer.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 2 {
		t.Fatalf("migration count = %d, want 2", versions)
	}

	rows, err := store.writer.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation")
	}
}

func TestMigrationFailureRollsBackAndCanBeRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	db, err := sql.Open("sqlite", writerDSN(path, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	broken := fstest.MapFS{
		"migrations/0001_ok.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE migration_probe(id INTEGER PRIMARY KEY);`)},
		"migrations/0002_bad.sql": &fstest.MapFile{Data: []byte(
			"CREATE TABLE should_rollback(id INTEGER PRIMARY KEY);\n" +
				"CREATE TRIGGER should_rollback_trigger AFTER INSERT ON should_rollback BEGIN SELECT 1; END;\n" +
				"CREATE TABLE broken (")},
	}
	if err := applyMigrations(context.Background(), db, broken); err == nil {
		t.Fatal("applyMigrations succeeded with invalid SQL")
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("applied migrations after failure = %d, want 1", count)
	}
	if tableExists(t, db, "migration_probe") != true {
		t.Fatal("first migration was not committed")
	}
	if tableExists(t, db, "should_rollback") {
		t.Fatal("failed migration left a partial table")
	}
	if schemaObjectExists(t, db, "trigger", "should_rollback_trigger") {
		t.Fatal("failed migration left a partial trigger")
	}

	repaired := fstest.MapFS{
		"migrations/0001_ok.sql":   &fstest.MapFile{Data: []byte(`CREATE TABLE migration_probe(id INTEGER PRIMARY KEY);`)},
		"migrations/0002_good.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE recovered(id INTEGER PRIMARY KEY);`)},
	}
	if err := applyMigrations(context.Background(), db, repaired); err != nil {
		t.Fatalf("retry after repair: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("applied migrations after retry = %d, want 2", count)
	}
	if !tableExists(t, db, "recovered") {
		t.Fatal("repaired migration was not applied")
	}
}

func TestMigrationDetectsUnknownOrRenamedVersion(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.writer.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (999, '999_future.sql', ?)`, formatTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err == nil {
		t.Fatal("Migrate accepted an unknown applied version")
	}
	if err := store.Ready(context.Background()); err == nil {
		t.Fatal("Ready succeeded after a failed migration check")
	}
}

func TestRevisionVersionIsUnique(t *testing.T) {
	store := newTestStore(t)
	src := seedSource(t, store, "source-revision")
	item := memory.Memory{
		ID:             idgen.NewUUID(),
		Kind:           memory.KindFact,
		Title:          "title",
		Content:        "content",
		State:          memory.StateActive,
		Version:        1,
		ObservedAt:     testTime(0),
		CreatedAt:      testTime(0),
		UpdatedAt:      testTime(0),
		OriginSourceID: src.ID,
	}
	revision := memory.Revision{
		MemoryID:  item.ID,
		Version:   1,
		Operation: memory.OperationCreate,
		Snapshot:  item,
		CreatedAt: testTime(0),
	}
	err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertMemory(ctx, item); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, revision); err != nil {
			return err
		}
		return tx.InsertRevision(ctx, revision)
	})
	if err == nil {
		t.Fatal("duplicate (memory_id, version) was accepted")
	}
	var count int
	if err := store.writer.QueryRow(`SELECT count(*) FROM memory_revisions WHERE memory_id = ?`, item.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("revision rows after rollback = %d, want 0", count)
	}
}

func TestSourceIdentifiersAreNotUnique(t *testing.T) {
	store := newTestStore(t)
	first := source.Source{ID: idgen.NewUUID(), AppID: "same-app", ExternalID: "same-external", Content: []byte(`"one"`), CreatedAt: testTime(0)}
	second := source.Source{ID: idgen.NewUUID(), AppID: "same-app", ExternalID: "same-external", Content: []byte(`"two"`), CreatedAt: testTime(time.Second)}
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertSource(ctx, first); err != nil {
			return err
		}
		return tx.InsertSource(ctx, second)
	}); err != nil {
		t.Fatalf("duplicate app_id/external_id rejected: %v", err)
	}
	var count int
	if err := store.writer.QueryRow(`SELECT count(*) FROM sources WHERE app_id = ? AND external_id = ?`,
		"same-app", "same-external").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("source count = %d, want 2", count)
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	return schemaObjectExists(t, db, "table", name)
}

func schemaObjectExists(t *testing.T, db *sql.DB, kind, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, kind, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count > 0
}
