package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestMigrationsCreateCoreAndSearchSchema(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for _, table := range []string{
		"memories", "memory_revisions", "memory_observation_refs", "sources", "observations",
		"evidence", "observation_evidence", "revision_evidence",
		"ingest_jobs", "schema_migrations", "memory_search_docs", "memory_fts",
		"observation_search_docs", "observation_fts",
		"memory_embeddings", "memory_embedding_jobs", "memory_index_meta",
		"memory_relations", "memory_conflicts",
		"memory_conflict_members", "memory_conflict_events", "memory_catalog",
		"schema_meta", "governance_jobs", "artifacts", "artifact_revisions",
		"derived_dependencies", "artifact_search_docs", "artifact_fts",
		"derived_target_evaluations",
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
	if versions != 7 {
		t.Fatalf("migration count = %d, want 7", versions)
	}
	var contract string
	if err := store.writer.QueryRowContext(ctx,
		`SELECT value FROM schema_meta WHERE key = 'contract'`).Scan(&contract); err != nil {
		t.Fatal(err)
	}
	if contract != SchemaContract {
		t.Fatalf("schema contract = %q, want %q", contract, SchemaContract)
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

func TestMigrateRejectsIncompatibleInitializationContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-layout.db")
	db, err := sql.Open("sqlite", writerDSN(path, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (
		version INTEGER NOT NULL PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (1, '0001_core.sql', ?)`,
		formatTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(context.Background(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	err = store.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "初始化契约") {
		t.Fatalf("Migrate error = %v, want initialization-contract rejection", err)
	}
	var count int
	if err := store.writer.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("incompatible database was partially migrated: %d", count)
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

func TestPreReleaseSchemaUsesCurrentStructure(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for table, removed := range map[string][]string{
		"memories": {
			"phase", "durability", "observed_at", "observed_at_basis", "event_time",
			"event_time_basis", "event_time_precision", "valid_from", "valid_to",
			"valid_from_basis", "valid_to_basis", "temporal_json", "procedure_json",
			"qualifiers_json", "fact_group_id", "created_by_app", "topics_json",
			"raw_topics_json",
		},
		"memory_revisions": {
			"phase", "durability", "observed_at", "observed_at_basis", "event_time",
			"event_time_basis", "event_time_precision", "valid_from", "valid_to",
			"valid_from_basis", "valid_to_basis", "temporal_json", "procedure_json",
			"qualifiers_json", "fact_group_id", "created_by_app", "topics_json",
			"raw_topics_json", "updated_at",
		},
		"evidence": {"location_present", "model_name"},
	} {
		columns := tableColumns(t, store, table)
		for _, name := range removed {
			if _, exists := columns[name]; exists {
				t.Fatalf("%s still contains obsolete column %s", table, name)
			}
		}
	}
	for _, column := range []string{"segment_id", "quote", "location_field", "location_start", "location_end"} {
		if _, exists := tableColumns(t, store, "evidence")[column]; !exists {
			t.Fatalf("evidence is missing %s", column)
		}
	}

	for _, table := range []string{
		"artifact_memory_dependencies", "artifact_run_dependencies",
		"memory_fact_groups", "memory_evolution_events",
	} {
		if tableExists(t, store.writer, table) {
			t.Fatalf("legacy table %s still exists", table)
		}
	}
	if _, exists := tableColumns(t, store, "memory_conflicts")["fact_group_id"]; exists {
		t.Fatal("memory_conflicts still contains fact_group_id")
	}

	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertSource(ctx, source.Source{
			ID: "source-t02", Content: []byte(`{"text":"项目使用 SQLite。"}`), CreatedAt: testTime(0),
		}); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO observations(
			id, source_id, scope_key, kind, content, content_mode, origin_role,
			status, extraction_version, created_at, updated_at)
			VALUES ('observation-1', 'source-t02', 'project:dmemd', 'fact',
			'项目使用 SQLite。', 'extractive', 'user', 'observed', 'extraction-v6', ?, ?)`,
			formatTime(testTime(0)), formatTime(testTime(0))); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO evidence(
			id, source_id, segment_id, message_id, quote, location_field,
			location_start, location_end, redacted, created_at)
			VALUES ('evidence-1', 'source-t02', 'seg-0001', '', '项目使用 SQLite。',
			'text', 0, 9, 0, ?)`, formatTime(testTime(0))); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO observation_evidence(observation_id, evidence_id)
			VALUES ('observation-1', 'evidence-1')`); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO memory_relations(
			id, kind, from_type, from_id, from_version, to_type, to_id, to_version, created_at)
			VALUES ('relation-1', 'supports', 'observation', 'observation-1', 0,
			'memory', 'memory-1', 1, ?)`, formatTime(testTime(0))); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO derived_dependencies(
			derived_id, derived_version, input_type, input_id, input_version, required)
			VALUES ('derived-1', 1, 'observation', 'observation-1', 0, 1)`); err != nil {
			return err
		}
		if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind: task.KindAggregateL2, TargetKey: task.L2TargetKey("project:dmemd", "storage"),
			InputHash: "t02", Now: testTime(0),
		}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.writer.ExecContext(ctx, `INSERT INTO governance_jobs(
		id, kind, target_key, revision, status, attempts, consecutive_failures,
		last_error_code, wait_reason, input_hash, created_at, updated_at)
		VALUES ('legacy-kind', 'aggregate', 't', 1, 'queued', 0, 0, '', '', '', ?, ?)`,
		formatTime(testTime(0)), formatTime(testTime(0))); err == nil {
		t.Fatal("governance_jobs accepted the old aggregate kind")
	}
}

func tableColumns(t *testing.T, store *Store, table string) map[string]struct{} {
	t.Helper()
	rows, err := store.writer.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]struct{}{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
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
