package ingest_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/model"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type processorFixture struct {
	store        *sqlite.Store
	clock        time.Time
	observations *observation.Service
	service      *ingest.Service
	processor    *ingest.Processor
	fake         *testutil.FakeModel
}

func newProcessorFixture(t *testing.T) *processorFixture {
	t.Helper()
	store := testutil.NewStore(t)
	fake := testutil.NewFakeModel(t)
	clock := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	f := &processorFixture{store: store, clock: clock, fake: fake}
	now := func() time.Time { return f.clock }
	f.observations = observation.NewService(store)
	provider, err := model.NewProvider(model.Config{Name: "test-model", BaseURL: fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.service = ingest.NewService(sqlite.IngestStore{Store: store}, store, ingest.WithClock(now))
	f.processor = ingest.NewProcessor(
		sqlite.IngestStore{Store: store},
		store,
		provider,
		ingest.WithModelName("test-model"),
		ingest.WithProcessorClock(now),
	)
	return f
}

func (f *processorFixture) advance(delta time.Duration) { f.clock = f.clock.Add(delta) }

func (f *processorFixture) submit(t *testing.T, content string) ingest.Receipt {
	t.Helper()
	receipt, err := f.service.Submit(context.Background(), ingest.Submission{AppID: "desktop", Content: json.RawMessage(content)})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func (f *processorFixture) claim(t *testing.T) ingest.Job {
	t.Helper()
	var job ingest.Job
	var found bool
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		job, found, err = tx.ClaimNextJob(ctx, f.clock)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no due job")
	}
	return job
}

func (f *processorFixture) process(t *testing.T, job ingest.Job) ingest.ProcessResult {
	t.Helper()
	result, err := f.processor.Process(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *processorFixture) job(t *testing.T, id string) ingest.Job {
	t.Helper()
	job, err := f.service.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func ingestFakeBlockedDraft(content string) testutil.FakeResponse {
	candidate, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"span_ids": []string{"seg-0001"}, "content": content,
	}}})
	return testutil.FakeResponse{Block: true, Content: string(candidate)}
}
