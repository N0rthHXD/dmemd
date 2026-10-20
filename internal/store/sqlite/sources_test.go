package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestSourceAndJobInsertAtomically(t *testing.T) {
	store := newTestStore(t)
	ingestStore := IngestStore{Store: store}
	ctx := context.Background()

	sourceID := idgen.NewUUID()
	jobID := idgen.NewUUID()
	err := ingestStore.WithTx(ctx, func(ctx context.Context, tx ingest.Tx) error {
		if err := tx.InsertSource(ctx, source.Source{
			ID:        sourceID,
			AppID:     "desktop-assistant",
			Content:   []byte(`{"messages":[]}`),
			CreatedAt: testTime(0),
		}); err != nil {
			return err
		}
		return tx.InsertJob(ctx, ingest.Job{
			ID:        jobID,
			SourceID:  sourceID,
			Status:    ingest.JobQueued,
			CreatedAt: testTime(0),
			UpdatedAt: testTime(0),
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	loadedSource, err := store.GetSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedSource.AppID != "desktop-assistant" || string(loadedSource.Content) != `{"messages":[]}` {
		t.Fatalf("source = %+v", loadedSource)
	}
	loadedJob, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedJob.Status != ingest.JobQueued || loadedJob.Attempts != 0 || loadedJob.SourceID != sourceID {
		t.Fatalf("job = %+v", loadedJob)
	}
	if loadedJob.MemoryIDs == nil || len(loadedJob.MemoryIDs) != 0 {
		t.Fatalf("memory_ids = %#v, want empty non-nil slice", loadedJob.MemoryIDs)
	}
}

func TestSourceAndJobRollbackTogether(t *testing.T) {
	store := newTestStore(t)
	ingestStore := IngestStore{Store: store}
	ctx := context.Background()
	failure := errors.New("injected failure")

	for _, failAt := range []string{"after_source", "after_job"} {
		t.Run(failAt, func(t *testing.T) {
			sourceID := idgen.NewUUID()
			jobID := idgen.NewUUID()
			err := ingestStore.WithTx(ctx, func(ctx context.Context, tx ingest.Tx) error {
				if err := tx.InsertSource(ctx, source.Source{
					ID:        sourceID,
					Content:   []byte(`"source"`),
					CreatedAt: testTime(0),
				}); err != nil {
					return err
				}
				if failAt == "after_source" {
					return failure
				}
				if err := tx.InsertJob(ctx, ingest.Job{
					ID:        jobID,
					SourceID:  sourceID,
					Status:    ingest.JobQueued,
					CreatedAt: testTime(0),
					UpdatedAt: testTime(0),
				}); err != nil {
					return err
				}
				return failure
			})
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if _, err := store.GetSource(ctx, sourceID); !errors.Is(err, source.ErrNotFound) {
				t.Fatalf("source survived rollback: %v", err)
			}
			if _, err := store.GetJob(ctx, jobID); !errors.Is(err, ingest.ErrNotFound) {
				t.Fatalf("job survived rollback: %v", err)
			}
		})
	}
}

func TestSourceStoresRawJSONShapesAndOptionalIdentifiers(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	shapes := []string{`"text"`, `{"role":"user"}`, `[1,2,3]`, `null`, `42`, `true`}
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			item := source.Source{
				ID:        idgen.NewUUID(),
				Content:   []byte(shape),
				CreatedAt: testTime(0),
			}
			if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
				return tx.InsertSource(ctx, item)
			}); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.GetSource(ctx, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if string(loaded.Content) != shape {
				t.Fatalf("content = %s, want %s", loaded.Content, shape)
			}
			if loaded.AppID != "" || loaded.ExternalID != "" {
				t.Fatalf("optional identifiers were not empty: %+v", loaded)
			}
		})
	}
}

func TestRepeatedSubmissionsCreateDistinctSourcesAndJobs(t *testing.T) {
	store := newTestStore(t)
	ingestStore := IngestStore{Store: store}
	ctx := context.Background()
	content := []byte(`"same submission"`)

	receipts := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		sourceID := idgen.NewUUID()
		jobID := idgen.NewUUID()
		if err := ingestStore.WithTx(ctx, func(ctx context.Context, tx ingest.Tx) error {
			if err := tx.InsertSource(ctx, source.Source{ID: sourceID, Content: content, CreatedAt: testTime(0)}); err != nil {
				return err
			}
			return tx.InsertJob(ctx, ingest.Job{
				ID: jobID, SourceID: sourceID, Status: ingest.JobQueued,
				CreatedAt: testTime(0), UpdatedAt: testTime(0),
			})
		}); err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, sourceID+"/"+jobID)
	}
	if receipts[0] == receipts[1] {
		t.Fatal("repeated submissions reused identifiers")
	}
}

func TestMissingSourceAndJobUseUnifiedNotFoundErrors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.GetSource(ctx, "missing"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("missing source error = %v", err)
	}
	if _, err := store.GetJob(ctx, "missing"); !errors.Is(err, ingest.ErrNotFound) {
		t.Fatalf("missing job error = %v", err)
	}
}

func TestSourceRedactionIsRepeatableAtStorageLevel(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	src := source.Source{
		ID: "source-redact", AppID: "app", Content: []byte(`{"text":"secret"}`), CreatedAt: testTime(0),
	}
	evidence := source.Evidence{
		ID: "evidence-redact", SourceID: src.ID, Quote: "secret",
		Location: &source.Location{Field: "text", Start: 0, End: 6}, CreatedAt: testTime(0),
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		return tx.InsertEvidence(ctx, evidence)
	}); err != nil {
		t.Fatal(err)
	}
	first := testTime(time.Minute)
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.RedactSource(ctx, src.ID, first); err != nil {
			return err
		}
		return tx.RedactEvidenceBySource(ctx, src.ID, first)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.RedactSource(ctx, src.ID, first.Add(time.Hour))
	}); err != nil {
		t.Fatalf("repeat redaction: %v", err)
	}
	loaded, err := store.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Redacted || len(loaded.Content) != 0 {
		t.Fatalf("source after redaction = %+v", loaded)
	}
	if loaded.RedactedAt == nil || !loaded.RedactedAt.Equal(first) {
		t.Fatalf("redacted_at = %v, want original %v", loaded.RedactedAt, first)
	}
	loadedEvidence, err := getEvidenceForTest(store, evidence.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loadedEvidence.Redacted || loadedEvidence.Quote != "" || loadedEvidence.Location != nil {
		t.Fatalf("evidence after redaction = %+v", loadedEvidence)
	}
}

func getEvidenceForTest(store *Store, id string) (source.Evidence, error) {
	var result source.Evidence
	err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		var err error
		result, err = tx.GetEvidence(ctx, id)
		return err
	})
	return result, err
}
