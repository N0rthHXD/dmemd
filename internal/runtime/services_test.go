package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/aggregation"
	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func TestProductionServicesUseSharedNormalization(t *testing.T) {
	store := testutil.NewStore(t)
	memories := NewMemoryService(store)
	search := NewSearchService(store, discardLogger())
	ctx := context.Background()

	var created memory.Memory
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = memories.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindFact, Title: "AI记忆", Content: "记忆服务保留上下文。",
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
	fake.EnqueueContent(`{"items":[{"span_ids":["seg-0001"],"content":"生产组装形成的记忆。"}]}`)
	ingestService := NewIngestService(store)
	observations := observation.NewService(store)
	workerCfg := DefaultConfig().Worker
	provider, err := NewExtractionProvider(ModelConfig{Name: "test-model", BaseURL: fake.URL(), Timeout: Duration(time.Second)}, workerCfg)
	if err != nil {
		t.Fatal(err)
	}
	processor := NewExtractionService(store, provider, "test-model", workerCfg, discardLogger())
	worker, err := NewExtractionWorker(store, processor, workerCfg, discardLogger())
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
	if job.Status != ingest.JobSucceeded || job.Attempts != 1 || len(job.ObservationIDs) != 1 {
		t.Fatalf("job = %+v", job)
	}
	item, err := observations.Get(context.Background(), job.ObservationIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if item.Content != "生产组装形成的记忆。" || item.ContentMode != observation.ContentNormalized ||
		item.Status != observation.StatusObserved || item.SourceID != receipt.SourceID {
		t.Fatalf("observation = %+v", item)
	}
	evidence, err := observations.Evidence(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Quote != "生产组装形成的记忆。" || evidence[0].SourceID != receipt.SourceID {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestProductionIngestAndGovernanceAssembly(t *testing.T) {
	store := testutil.NewStore(t)
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"items":[{"span_ids":["seg-0001"],"content":"项目使用 SQLite 保存记忆。"}]}`)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	cfg.Model.Name = "test-model"
	cfg.Model.BaseURL = fake.URL()
	cfg.Model.Timeout = Duration(5 * time.Second)
	cfg.Governance.Debounce = 0
	cfg.Governance.MinCallInterval = 0
	stack, err := NewStack(cfg, discardLogger(), store)
	if err != nil {
		t.Fatal(err)
	}
	if stack.ExtractionWorker == nil || stack.GovernanceWorker == nil || stack.AggregationWorker == nil {
		t.Fatalf("stage workers missing: extraction=%v governance=%v aggregation=%v",
			stack.ExtractionWorker, stack.GovernanceWorker, stack.AggregationWorker)
	}
	receipt, err := stack.Services.Ingest.Submit(context.Background(), ingest.Submission{
		ScopeKey: "project:dmemd",
		Content:  []byte(`"项目使用 SQLite 保存记忆。"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := stack.ExtractionWorker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("ingest processed=%v err=%v", processed, err)
	}
	job, err := stack.Services.Ingest.GetJob(context.Background(), receipt.JobID)
	if err != nil || job.Status != ingest.JobSucceeded || len(job.ObservationIDs) != 1 {
		t.Fatalf("ingest job = %+v err=%v", job, err)
	}
	governanceJob, err := store.GetGovernanceJobByTarget(context.Background(),
		task.KindGovernObservation, task.ObservationKey(job.ObservationIDs[0]))
	if err != nil {
		t.Fatal(err)
	}
	if governanceJob.Status != task.StatusQueued {
		t.Fatalf("governance job = %+v", governanceJob)
	}
}

func TestObservationGovernanceAssemblyMaterializesCurrentMemory(t *testing.T) {
	store := testutil.NewStore(t)
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"items":[{"span_ids":["seg-0001"],"content":"项目使用 SQLite 保存记忆。"}]}`)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	cfg.Model.Name = "test-model"
	cfg.Model.BaseURL = fake.URL()
	cfg.Model.Timeout = Duration(5 * time.Second)
	cfg.Governance.Debounce = 0
	cfg.Governance.MinCallInterval = 0
	stack, err := NewStack(cfg, discardLogger(), store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := stack.Services.Ingest.Submit(context.Background(), ingest.Submission{
		ScopeKey: "project:dmemd",
		Content:  []byte(`"项目使用 SQLite 保存记忆。"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := stack.ExtractionWorker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("ingest processed=%v err=%v", processed, err)
	}
	job, err := stack.Services.Ingest.GetJob(context.Background(), receipt.JobID)
	if err != nil || len(job.ObservationIDs) != 1 {
		t.Fatalf("ingest job = %+v err=%v", job, err)
	}
	if processed, err := stack.GovernanceWorker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("governance processed=%v err=%v", processed, err)
	}
	items, err := stack.Services.Memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Content != "项目使用 SQLite 保存记忆。" ||
		items[0].AssertionStatus != memory.AssertionAccepted {
		t.Fatalf("materialized memories = %+v", items)
	}
	refs, err := store.ListMemoryObservationRefs(context.Background(), items[0].ID, items[0].Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != job.ObservationIDs[0] {
		t.Fatalf("observation refs = %+v", refs)
	}
}

func TestIngestWorkerRejectsMultipleWorkers(t *testing.T) {
	store := testutil.NewStore(t)
	cfg := DefaultConfig().Worker
	processor := NewExtractionService(store, nil, "test", cfg, discardLogger())
	cfg.Workers = 2
	if _, err := NewExtractionWorker(store, processor, cfg, discardLogger()); err == nil {
		t.Fatal("multiple workers accepted")
	}
}

func TestStageWorkersClaimOnlyTheirOwnTaskKinds(t *testing.T) {
	store := testutil.NewStore(t)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	stack, err := NewStack(cfg, discardLogger(), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	target := aggregation.L1TargetForTime("project:dmemd", now, 24*time.Hour)
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:      task.KindAggregateL1,
			TargetKey: target.TargetKey(),
			InputHash: "stage-isolation-aggregation",
			Now:       now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if processed, err := stack.GovernanceWorker.ProcessOne(ctx); err != nil || processed {
		t.Fatalf("governance worker claimed aggregation job: processed=%v err=%v", processed, err)
	}
	if processed, err := stack.AggregationWorker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("aggregation worker did not claim its job: processed=%v err=%v", processed, err)
	}

	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:      task.KindClassifyObservation,
			TargetKey: task.MemoryKey("missing-memory"),
			InputHash: "stage-isolation-governance",
			Now:       now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if processed, err := stack.AggregationWorker.ProcessOne(ctx); err != nil || processed {
		t.Fatalf("aggregation worker claimed governance job: processed=%v err=%v", processed, err)
	}
	if processed, err := stack.GovernanceWorker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("governance worker did not claim its job: processed=%v err=%v", processed, err)
	}
}

func TestEmbeddingAssemblyIndexesAndSearchesSemantically(t *testing.T) {
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
	cfg.Model.BaseURL = "http://127.0.0.1:1"
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
	ctx := context.Background()
	memories := NewMemoryService(store)
	var created memory.Memory
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = memories.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindPreference, Title: "饮食偏好", Content: "用户偏好清淡饮食。",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	processed, err := stack.EmbeddingWorker.ProcessOne(ctx)
	if err != nil || !processed {
		t.Fatalf("embedding processed = %v, err = %v", processed, err)
	}

	searchService, ok := stack.Services.Search.(*retrieval.Service)
	if !ok {
		t.Fatalf("search service type = %T", stack.Services.Search)
	}
	result, err := searchService.Search(ctx, retrieval.Request{
		Query: "吃饭有什么忌口", Mode: retrieval.ModeHybrid, TopK: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Retrieval.Mode != retrieval.ModeHybrid || len(result.Warnings) != 0 {
		t.Fatalf("retrieval = %+v", result)
	}
	if len(result.Items) != 1 || result.Items[0].Memory.ID != created.ID {
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
	status, err := stack.Services.Embedding.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || !status.Ready || status.ReasonCode != embedding.StatusReady ||
		status.Documents != 1 || status.CurrentVectors != 1 || !status.ConfigFingerprintMatch {
		t.Fatalf("embedding status = %+v", status)
	}
	probe := stack.Services.Embedding.Probe(ctx)
	if !probe.Attempted || !probe.Success || probe.Code != "ok" || probe.Dimensions != 3 {
		t.Fatalf("embedding probe = %+v", probe)
	}
}

func TestProductionExperienceAndSkillAssembly(t *testing.T) {
	store := testutil.NewStore(t)
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"problem":"验证修改","actions":["运行 go test"],"lessons":["先测试"],"content":"问题：验证修改。\n行动：运行 go test。"}`)
	fake.EnqueueContent(`{"name":"验证工作流","description":"修改后的验证","content":"## 步骤\n1. 运行 go test。","steps":["运行 go test"]}`)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	cfg.Model.Name = "test-model"
	cfg.Model.BaseURL = fake.URL()
	cfg.Model.Timeout = Duration(5 * time.Second)
	cfg.Governance.Debounce = 0
	cfg.Governance.MinCallInterval = 0
	stack, err := NewStack(cfg, discardLogger(), store)
	if err != nil {
		t.Fatal(err)
	}
	drainStackWorkers(t, stack, 20)

	receipt, err := stack.Services.Experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID:    "run-1",
		ScopeKey: "project:dmemd",
		Goal:     "验证修改",
		Actions:  []string{"运行 go test"},
		Outcome:  governance.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	drainStackWorkers(t, stack, 20)
	caseArtifact, err := store.GetArtifactByTarget(context.Background(), artifact.KindExperienceCase,
		task.ExperienceKey(receipt.ID))
	if err != nil {
		t.Fatal(err)
	}
	if caseArtifact.State != artifact.StateActive || caseArtifact.CurrentVersion != 1 {
		t.Fatalf("experience case = %+v", caseArtifact)
	}
	skillArtifact, err := store.GetArtifactByTarget(context.Background(), artifact.KindSkill,
		task.SkillKey("project:dmemd", ""))
	if !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("skill artifact should not exist without a procedure: %+v err=%v", skillArtifact, err)
	}
	targets, err := stack.Services.Derived.ListTargets(context.Background(), artifact.TargetQuery{
		ScopeKey: "project:dmemd", Kind: artifact.KindSkill,
		Status: artifact.TargetStatusNoEligibleInput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Items) != 1 || targets.Items[0].ReasonCode != "no_procedure_input" {
		t.Fatalf("skill target evaluation = %+v", targets)
	}
	if fake.RequestCount() != 1 {
		t.Fatalf("model calls = %d, want 1 (no procedure)", fake.RequestCount())
	}
	requests := fake.Requests()
	if !strings.Contains(string(requests[0].Body), "application-reported result") {
		t.Fatalf("model prompt = %s", requests[0].Body)
	}

	recall, err := stack.Services.Search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "验证", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDerivedKind(recall.L4, artifact.KindExperienceCase) ||
		hasDerivedKind(recall.L4, artifact.KindSkill) {
		t.Fatalf("recall = %+v", recall)
	}
	if _, err := stack.Services.Experiences.Retract(context.Background(), receipt.ID); err != nil {
		t.Fatal(err)
	}
	if skill, err := store.GetArtifactByTarget(context.Background(), artifact.KindSkill,
		task.SkillKey("project:dmemd", "")); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("retract created an empty skill artifact: %+v err=%v", skill, err)
	}
	drainStackWorkers(t, stack, 20)
	after, err := stack.Services.Search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "验证", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.L4) != 0 {
		t.Fatalf("revoked experience remained recallable: %+v", after)
	}
}

func TestGovernanceDisabledKeepsAtomicServicesAvailable(t *testing.T) {
	store := testutil.NewStore(t)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	disabled := false
	cfg.Governance.Enabled = &disabled
	stack, err := NewStack(cfg, discardLogger(), store)
	if err != nil {
		t.Fatal(err)
	}
	if stack.GovernanceWorker != nil {
		t.Fatal("governance worker started while disabled")
	}
	if stack.AggregationWorker == nil {
		t.Fatal("aggregation worker missing while governance is disabled")
	}
	if stack.Services.Search == nil || stack.Services.Memories == nil || stack.Services.Forget == nil ||
		stack.Services.Embedding == nil {
		t.Fatal("atomic, management or embedding diagnostics services missing while governance is disabled")
	}
	embeddingStatus, err := stack.Services.Embedding.Status(context.Background())
	if err != nil || embeddingStatus.Enabled || embeddingStatus.ReasonCode != embedding.StatusDisabled {
		t.Fatalf("disabled embedding status = %+v, %v", embeddingStatus, err)
	}
	result, err := stack.Services.Search.Recall(context.Background(), retrieval.RecallRequest{Query: "任意内容"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 0 || len(result.Memories) != 0 ||
		len(result.L1)+len(result.L2)+len(result.L3)+len(result.L4) != 0 {
		t.Fatalf("empty recall = %+v", result)
	}
}

// drainStackWorkers alternates the governance and aggregation executors until
// both are idle. The stores are shared, but each worker only claims its own task
// kinds.
func drainStackWorkers(t *testing.T, stack *Stack, iterations int) {
	t.Helper()
	for i := 0; i < iterations; i++ {
		governanceProcessed := false
		if stack.GovernanceWorker != nil {
			processed, err := stack.GovernanceWorker.ProcessOne(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			governanceProcessed = processed
		}
		aggregationProcessed := false
		if stack.AggregationWorker != nil {
			processed, err := stack.AggregationWorker.ProcessOne(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			aggregationProcessed = processed
		}
		if !governanceProcessed && !aggregationProcessed {
			return
		}
	}
	t.Fatalf("background workers did not become idle after %d iterations", iterations)
}

func TestAggregationDisabledKeepsOtherStagesAvailable(t *testing.T) {
	store := testutil.NewStore(t)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	cfg.Aggregation.Enabled = false
	stack, err := NewStack(cfg, discardLogger(), store)
	if err != nil {
		t.Fatal(err)
	}
	if stack.AggregationWorker != nil {
		t.Fatal("aggregation worker started while disabled")
	}
	if stack.ExtractionWorker == nil || stack.GovernanceWorker == nil {
		t.Fatal("disabling aggregation stopped extraction or governance")
	}
	if stack.Services.Search == nil || stack.Services.Memories == nil || stack.Services.Derived == nil ||
		stack.Services.Observations == nil || stack.Services.Relations == nil || stack.Services.Jobs == nil {
		t.Fatal("disabling aggregation removed read services")
	}
}

func hasDerivedKind(items []retrieval.DerivedItem, kind artifact.Kind) bool {
	for _, item := range items {
		if item.DerivedMemory.Kind == kind {
			return true
		}
	}
	return false
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
