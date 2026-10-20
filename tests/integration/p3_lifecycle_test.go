package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func TestP3LifecycleVersionEvidenceRedactionAndPurge(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	clock := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	memories := memory.NewService(store, sqlite.MemoryStore{Store: store}, testutil.DocumentBuilder{}, memory.WithClock(now))
	sources := source.NewService(store, sqlite.SourceStore{Store: store}, source.WithClock(now))

	src := source.Source{
		ID: "source-p3", AppID: "desktop-assistant",
		Content:   []byte(`{"messages":[{"id":"m1","content":"默认中文回答"}]}`),
		CreatedAt: clock,
	}
	evidence := source.NewEvidence(src, "m1", "默认中文回答", "test-model", clock)
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		if err := tx.InsertEvidence(ctx, evidence); err != nil {
			return err
		}
		for _, job := range []ingest.Job{
			{ID: "job-p3-queued", SourceID: src.ID, Status: ingest.JobQueued, CreatedAt: clock, UpdatedAt: clock},
			{ID: "job-p3-running", SourceID: src.ID, Status: ingest.JobRunning, Attempts: 1, CreatedAt: clock, UpdatedAt: clock},
		} {
			if err := tx.InsertJob(ctx, job); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var created memory.Memory
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = memories.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindPreference, Title: "回答语言", Content: "默认中文回答",
			ObservedAt: clock, CreatedByApp: src.AppID, OriginSourceID: src.ID,
			Evidence: []source.Evidence{evidence}, Reason: "initial",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Minute)
	var revised memory.Memory
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		revised, err = memories.Revise(ctx, tx, memory.ReviseInput{
			MemoryID: created.ID, Kind: memory.KindPreference, Title: "回答语言",
			Content: "始终中文回答", ObservedAt: clock, Operation: memory.OperationUpdate,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if revised.Version != 2 {
		t.Fatalf("revised version = %d", revised.Version)
	}
	if currentEvidence, err := memories.GetEvidence(ctx, created.ID, 0); err != nil {
		t.Fatal(err)
	} else if len(currentEvidence) != 0 {
		t.Fatalf("corrected conclusion inherited evidence: %+v", currentEvidence)
	}

	clock = clock.Add(time.Minute)
	archived, err := memories.UpdateState(ctx, created.ID, memory.StateArchived)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	deleted, err := memories.Delete(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Version != 3 || deleted.Version != 4 || deleted.State != memory.StateDeleted {
		t.Fatalf("lifecycle versions: archived=%d deleted=%d state=%s", archived.Version, deleted.Version, deleted.State)
	}
	clock = clock.Add(time.Minute)
	restored, err := memories.Restore(ctx, memory.RestoreInput{MemoryID: created.ID, Version: 1, Reason: "restore"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version != 5 || restored.State != memory.StateActive || restored.Content != "默认中文回答" {
		t.Fatalf("restored = %+v", restored)
	}
	versions, err := memories.ListVersions(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 5 {
		t.Fatalf("versions = %d, want 5", len(versions))
	}

	clock = clock.Add(time.Minute)
	redaction, err := sources.Redact(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if redaction.CancelledJobs != 2 {
		t.Fatalf("cancelled jobs = %d", redaction.CancelledJobs)
	}
	redactedSource, err := sources.Get(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !redactedSource.Redacted || len(redactedSource.Content) != 0 {
		t.Fatalf("source after redaction = %+v", redactedSource)
	}
	for _, version := range []int{1, 5} {
		items, err := memories.GetEvidence(ctx, created.ID, version)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || !items[0].Redacted || items[0].Quote != "" || items[0].Location != nil {
			t.Fatalf("version %d evidence after redaction = %+v", version, items)
		}
	}
	current, err := memories.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Content != "默认中文回答" {
		t.Fatalf("memory body after redaction = %q", current.Content)
	}
	for _, jobID := range []string{"job-p3-queued", "job-p3-running"} {
		job, err := store.GetJob(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != ingest.JobCancelled || job.Error != source.RedactedJobReason {
			t.Fatalf("%s after redaction = %+v", jobID, job)
		}
	}

	if err := memories.Purge(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := memories.Get(ctx, created.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("purged memory error = %v", err)
	}
	if _, err := memories.GetVersion(ctx, created.ID, 1); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("purged revision error = %v", err)
	}
	db := testutil.OpenInspector(t, store.Path())
	if rows := integrationCount(t, db, `SELECT COUNT(*) FROM sources WHERE id = ?`, src.ID); rows != 1 {
		t.Fatalf("source rows after purge = %d", rows)
	}
	if rows := integrationCount(t, db, `SELECT COUNT(*) FROM memory_search_docs`); rows != 0 {
		t.Fatalf("search documents after purge = %d", rows)
	}
	if rows := integrationCount(t, db, `SELECT COUNT(*) FROM memories WHERE id = ?`, created.ID); rows != 0 {
		t.Fatalf("memory rows after purge = %d", rows)
	}
	if rows := integrationCount(t, db, `SELECT COUNT(*) FROM memory_revisions WHERE memory_id = ?`, created.ID); rows != 0 {
		t.Fatalf("revision rows after purge = %d", rows)
	}
	if rows := integrationCount(t, db, `SELECT COUNT(*) FROM revision_evidence`); rows != 0 {
		t.Fatalf("revision evidence links after purge = %d", rows)
	}
	assertNoForeignKeyViolations(t, db)
}

func integrationCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertNoForeignKeyViolations(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation detected")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
