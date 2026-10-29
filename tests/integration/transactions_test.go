//go:build legacy

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type crossTableIDs struct {
	sourceID string
	jobID    string
	memoryID string
	evidence string
}

func TestCrossTableTransactionAndIndexBoundary(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		store := testutil.NewStore(t)
		db := testutil.OpenInspector(t, store.Path())
		ids, err := writeCrossTable(context.Background(), store, nil)
		if err != nil {
			t.Fatalf("transaction: %v", err)
		}
		for _, table := range []string{"sources", "ingest_jobs", "memories", "memory_revisions", "evidence", "revision_evidence", "memory_search_docs"} {
			if got := countRows(t, db, table); got != 1 {
				t.Fatalf("%s rows = %d, want 1", table, got)
			}
		}
		if matches := ftsMatches(t, db, "crossprobe"); matches != 1 {
			t.Fatalf("FTS matches = %d, want 1", matches)
		}
		job, err := store.GetJob(context.Background(), ids.jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 1 || job.MemoryIDs[0] != ids.memoryID {
			t.Fatalf("job = %+v", job)
		}
		loaded, err := store.GetMemory(context.Background(), ids.memoryID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Content != "crossprobe content" || loaded.OriginSourceID != ids.sourceID {
			t.Fatalf("memory = %+v", loaded)
		}
	})

	for _, failAt := range []string{"source_job", "memory", "revision", "evidence", "link", "search_doc", "job_result"} {
		t.Run("rollback_"+failAt, func(t *testing.T) {
			store := testutil.NewStore(t)
			db := testutil.OpenInspector(t, store.Path())
			injector := &testutil.FaultInjector{FailAt: failAt}
			if _, err := writeCrossTable(context.Background(), store, injector); err == nil {
				t.Fatalf("transaction succeeded at failure point %s", failAt)
			}
			if !injector.Hit() {
				t.Fatalf("failure point %s was not reached", failAt)
			}
			for _, table := range []string{"sources", "ingest_jobs", "memories", "memory_revisions", "evidence", "revision_evidence", "memory_search_docs"} {
				if got := countRows(t, db, table); got != 0 {
					t.Fatalf("after %s rollback, %s rows = %d, want 0", failAt, table, got)
				}
			}
			if matches := ftsMatches(t, db, "crossprobe"); matches != 0 {
				t.Fatalf("after %s rollback, FTS matches = %d, want 0", failAt, matches)
			}
		})
	}
}

func writeCrossTable(ctx context.Context, store *sqlite.Store, injector *testutil.FaultInjector) (crossTableIDs, error) {
	now := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	ids := crossTableIDs{
		sourceID: idgen.NewUUID(),
		jobID:    idgen.NewUUID(),
		memoryID: idgen.NewUUID(),
		evidence: idgen.NewUUID(),
	}
	item := memory.Memory{
		ID: ids.memoryID, Kind: memory.KindFact, Title: "Cross probe",
		Content: "crossprobe content", State: memory.StateActive, Version: 1,
		ObservedAt: now, CreatedAt: now, UpdatedAt: now,
		CreatedByApp: "test-app", OriginSourceID: ids.sourceID,
	}
	revision := memory.Revision{
		MemoryID: ids.memoryID, Version: 1, Operation: memory.OperationCreate,
		Reason: "cross-table test", Snapshot: item, CreatedAt: now,
	}
	evidence := source.Evidence{
		ID: ids.evidence, SourceID: ids.sourceID, Quote: "crossprobe",
		CreatedAt: now,
	}
	job := ingest.Job{
		ID: ids.jobID, SourceID: ids.sourceID, Status: ingest.JobQueued,
		MemoryIDs: []string{}, CreatedAt: now, UpdatedAt: now,
	}
	document, err := (testutil.DocumentBuilder{}).BuildDocument(item)
	if err != nil {
		return ids, err
	}

	ingestStore := sqlite.IngestStore{Store: store}
	err = ingestStore.WithTx(ctx, func(ctx context.Context, tx ingest.Tx) error {
		if err := tx.InsertSource(ctx, source.Source{
			ID: ids.sourceID, AppID: "test-app", Content: []byte(`"cross table"`), CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.InsertJob(ctx, job); err != nil {
			return err
		}
		if injector != nil {
			if err := injector.Checkpoint("source_job"); err != nil {
				return err
			}
		}
		if err := tx.InsertMemory(ctx, item); err != nil {
			return err
		}
		if injector != nil {
			if err := injector.Checkpoint("memory"); err != nil {
				return err
			}
		}
		if err := tx.InsertRevision(ctx, revision); err != nil {
			return err
		}
		if injector != nil {
			if err := injector.Checkpoint("revision"); err != nil {
				return err
			}
		}
		if err := tx.InsertEvidence(ctx, evidence); err != nil {
			return err
		}
		if injector != nil {
			if err := injector.Checkpoint("evidence"); err != nil {
				return err
			}
		}
		if err := tx.LinkRevisionEvidence(ctx, ids.memoryID, 1, []string{ids.evidence}); err != nil {
			return err
		}
		if injector != nil {
			if err := injector.Checkpoint("link"); err != nil {
				return err
			}
		}
		if err := tx.UpsertSearchDocument(ctx, document); err != nil {
			return err
		}
		if injector != nil {
			if err := injector.Checkpoint("search_doc"); err != nil {
				return err
			}
		}
		job.Status = ingest.JobSucceeded
		job.MemoryIDs = []string{ids.memoryID}
		job.FinishedAt = &now
		job.UpdatedAt = now
		if err := tx.UpdateJob(ctx, job); err != nil {
			return err
		}
		if injector != nil {
			if err := injector.Checkpoint("job_result"); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func ftsMatches(t *testing.T, db *sql.DB, term string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM memory_fts WHERE memory_fts MATCH ?`, term).Scan(&count); err != nil {
		t.Fatalf("FTS match %s: %v", term, err)
	}
	return count
}

func TestCrossTableJobResultContainsOnlyMemoryIDs(t *testing.T) {
	store := testutil.NewStore(t)
	db := testutil.OpenInspector(t, store.Path())
	ids, err := writeCrossTable(context.Background(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT memory_ids FROM ingest_jobs WHERE id = ?`, ids.jobID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var decoded []string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0] != ids.memoryID {
		t.Fatalf("memory_ids = %v", decoded)
	}
	rows, err := db.Query(`PRAGMA table_info(ingest_jobs)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "response" || name == "raw_response" || name == "model_response" {
			t.Fatalf("ingest_jobs contains raw model response column %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
