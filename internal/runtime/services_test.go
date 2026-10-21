package runtime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestEmbeddingAssemblyIndexesAndSearchesSemantically(t *testing.T) {
	chat := testutil.NewFakeModel(t)
	chat.EnqueueContent(`{"candidates":[{"content":"用户偏好清淡饮食。"}]}`)
	embeddingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0,0]}]}`)
	}))
	defer embeddingServer.Close()

	store := testutil.NewStore(t)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	cfg.Model.Name = "extract"
	cfg.Model.BaseURL = chat.URL()
	cfg.Model.Timeout = Duration(time.Second)
	cfg.Embedding.Enabled = true
	cfg.Embedding.BaseURL = embeddingServer.URL
	cfg.Embedding.Model = "embedding-test"
	cfg.Embedding.Dimensions = 3
	cfg.Embedding.Timeout = Duration(time.Second)

	stack, err := NewStack(cfg, discardLogger(), store)
	if err != nil {
		t.Fatal(err)
	}
	if stack.EmbeddingWorker == nil {
		t.Fatal("embedding worker missing from stack")
	}
	receipt, err := stack.Services.Ingest.Submit(context.Background(), ingest.Submission{Content: []byte(`"用户偏好清淡饮食。"`)})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := stack.Worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ingest processed = %v, err = %v", processed, err)
	}
	job, err := stack.Services.Ingest.GetJob(context.Background(), receipt.JobID)
	if err != nil || job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 1 {
		t.Fatalf("job = %+v, err = %v", job, err)
	}
	processed, err = stack.EmbeddingWorker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("embedding processed = %v, err = %v", processed, err)
	}

	result, err := stack.Services.Search.Search(context.Background(), retrieval.Request{
		Query: "吃饭有什么忌口", Mode: retrieval.ModeHybrid, TopK: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Retrieval.Mode != retrieval.ModeHybrid || len(result.Warnings) != 0 {
		t.Fatalf("retrieval = %+v", result)
	}
	if len(result.Items) != 1 || result.Items[0].Memory.ID != job.MemoryIDs[0] {
		t.Fatalf("items = %+v", result.Items)
	}
	if result.Items[0].MatchedTerms == nil || len(result.Items[0].MatchedTerms) != 0 {
		t.Fatalf("semantic hit fabricated matched_terms: %#v", result.Items[0].MatchedTerms)
	}

	var documents int
	if err := testutil.OpenInspector(t, store.Path()).QueryRow(`SELECT count(*) FROM memory_embeddings`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if documents != 1 {
		t.Fatalf("embedding rows = %d", documents)
	}
}

func TestNewStackRejectsIncompleteEmbeddingConfig(t *testing.T) {
	store := testutil.NewStore(t)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	cfg.Embedding.Enabled = true
	if _, err := NewStack(cfg, discardLogger(), store); err == nil ||
		!strings.Contains(err.Error(), "embedding") {
		t.Fatalf("NewStack error = %v", err)
	}
}
