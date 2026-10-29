package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

var errInjectedIngestFailure = errors.New("injected ingest transaction failure")

type failingIngestStore struct {
	sqlite.IngestStore
}

func (f failingIngestStore) WithTx(ctx context.Context, fn func(context.Context, ingest.Tx) error) error {
	return f.IngestStore.Store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return errInjectedIngestFailure
	})
}

func TestSubmitPersistsSourceAndQueuedJobTogether(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	clock := time.Date(2026, 10, 20, 2, 0, 0, 123, time.UTC)
	service := ingest.NewService(sqlite.IngestStore{Store: store}, store, ingest.WithClock(func() time.Time { return clock }))

	type submission struct {
		name string
		body ingest.Submission
		want string
	}
	for _, tc := range []submission{
		{name: "text", body: ingest.Submission{Content: json.RawMessage(`"以后请默认使用中文回答。"`)}, want: `"以后请默认使用中文回答。"`},
		{name: "conversation", body: ingest.Submission{AppID: "desktop", ExternalID: "conv-1", Content: json.RawMessage(`{"messages":[{"role":"user","content":"中文"}]}`)}, want: `{"messages":[{"role":"user","content":"中文"}]}`},
		{name: "structured", body: ingest.Submission{Content: json.RawMessage(`[{"kind":"note","value":3}]`)}, want: `[{"kind":"note","value":3}]`},
		{name: "missing", body: ingest.Submission{}, want: `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt, err := service.Submit(ctx, tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.SourceID == "" || receipt.JobID == "" || receipt.Status != ingest.JobQueued {
				t.Fatalf("receipt = %+v", receipt)
			}
			src, err := store.GetSource(ctx, receipt.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			if string(src.Content) != tc.want || !src.CreatedAt.Equal(clock) || src.AppID != tc.body.AppID {
				t.Fatalf("source = %+v content=%s", src, src.Content)
			}
			job, err := service.GetJob(ctx, receipt.JobID)
			if err != nil {
				t.Fatal(err)
			}
			if job.SourceID != receipt.SourceID || job.Status != ingest.JobQueued || job.Attempts != 0 ||
				job.ObservationIDs == nil || len(job.ObservationIDs) != 0 || !job.CreatedAt.Equal(clock) {
				t.Fatalf("job = %+v", job)
			}
		})
	}

	first, err := service.Submit(ctx, ingest.Submission{Content: json.RawMessage(`"重复"`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Submit(ctx, ingest.Submission{Content: json.RawMessage(`"重复"`)})
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceID == second.SourceID || first.JobID == second.JobID {
		t.Fatal("repeated submissions reused identifiers")
	}
}

func TestSubmitRejectsInvalidJSONWithoutPartialRows(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	service := ingest.NewService(sqlite.IngestStore{Store: store}, store)
	if _, err := service.Submit(ctx, ingest.Submission{Content: json.RawMessage(`{`)}); !errors.Is(err, ingest.ErrInvalidContent) {
		t.Fatalf("error = %v", err)
	}
	db := testutil.OpenInspector(t, store.Path())
	for _, table := range []string{"sources", "ingest_jobs"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows = %d", table, count)
		}
	}
}

func TestSubmitNormalizesScopeKey(t *testing.T) {
	store := testutil.NewStore(t)
	service := ingest.NewService(sqlite.IngestStore{Store: store}, store)
	receipt, err := service.Submit(context.Background(), ingest.Submission{
		ScopeKey: " project:dmemd ",
		Content:  json.RawMessage(`"带作用域的内容"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := store.GetSource(context.Background(), receipt.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if src.ScopeKey != "project:dmemd" {
		t.Fatalf("scope_key = %q", src.ScopeKey)
	}
}

func TestSubmitRollsBackSourceWhenJobOrCommitFails(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	service := ingest.NewService(failingIngestStore{IngestStore: sqlite.IngestStore{Store: store}}, store)
	_, err := service.Submit(ctx, ingest.Submission{Content: json.RawMessage(`"不会落库"`)})
	if !errors.Is(err, errInjectedIngestFailure) {
		t.Fatalf("error = %v", err)
	}
	db := testutil.OpenInspector(t, store.Path())
	for _, table := range []string{"sources", "ingest_jobs"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows = %d", table, count)
		}
	}
}

func TestGetJobMissingUsesUnifiedError(t *testing.T) {
	store := testutil.NewStore(t)
	service := ingest.NewService(sqlite.IngestStore{Store: store}, store)
	if _, err := service.GetJob(context.Background(), "missing"); !errors.Is(err, ingest.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}
