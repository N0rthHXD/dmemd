package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func TestProductionServicesUseSharedNormalization(t *testing.T) {
	store := testutil.NewStore(t)
	memories := NewMemoryService(store)
	search := NewSearchService(store, discardLogger())
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)

	var created memory.Memory
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = memories.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindFact, Title: "AI记忆", Content: "记忆服务保留上下文。",
			ObservedAt: now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	result, err := search.Search(ctx, retrieval.Request{Query: "记忆"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Memory.ID != created.ID {
		t.Fatalf("production search result = %+v", result.Items)
	}
}

func TestProductionIngestAssembly(t *testing.T) {
	store := testutil.NewStore(t)
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"candidates":[{"content":"生产组装形成的记忆。"}]}`)
	ingestService := NewIngestService(store)
	memories := NewMemoryService(store)
	search := NewSearchService(store, discardLogger())
	provider, err := NewModelProvider(ModelConfig{Name: "test-model", BaseURL: fake.URL(), Timeout: Duration(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	processor := NewIngestProcessor(store, memories, search, provider, "test-model", discardLogger())
	worker, err := NewIngestWorker(store, processor, DefaultConfig().Worker, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ingestService.Submit(context.Background(), ingest.Submission{Content: []byte(`"生产组装形成的记忆。"`)})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	job, err := ingestService.GetJob(context.Background(), receipt.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != ingest.JobSucceeded || job.Attempts != 1 || len(job.MemoryIDs) != 1 {
		t.Fatalf("job = %+v", job)
	}
	result, err := search.Search(context.Background(), retrieval.Request{Query: "生产组装"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Memory.ID != job.MemoryIDs[0] {
		t.Fatalf("search = %+v", result)
	}
}

func TestIngestWorkerRejectsMultipleWorkers(t *testing.T) {
	store := testutil.NewStore(t)
	processor := NewIngestProcessor(store, NewMemoryService(store), NewSearchService(store, discardLogger()), nil, "test", discardLogger())
	cfg := DefaultConfig().Worker
	cfg.Workers = 2
	if _, err := NewIngestWorker(store, processor, cfg, discardLogger()); err == nil {
		t.Fatal("multiple workers accepted")
	}
}
