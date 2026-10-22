package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/model"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type ingestStack struct {
	path      string
	store     *sqlite.Store
	clock     time.Time
	fake      *testutil.FakeModel
	memories  *memory.Service
	search    *retrieval.Service
	sources   *source.Service
	service   *ingest.Service
	processor *ingest.Processor
	worker    *ingest.Worker
}

var errInjectedCommit = errors.New("injected commit failure")

type interruptingStore struct {
	sqlite.IngestStore
}

func (f interruptingStore) WithTx(ctx context.Context, fn func(context.Context, ingest.Tx) error) error {
	return f.IngestStore.Store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return errInjectedCommit
	})
}

func newIngestStack(t *testing.T) *ingestStack {
	t.Helper()
	fake := testutil.NewFakeModel(t)
	stack := &ingestStack{
		path:  filepath.Join(t.TempDir(), "dmemd.db"),
		clock: time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC),
		fake:  fake,
	}
	stack.open(t)
	return stack
}

func (f *ingestStack) open(t *testing.T) {
	t.Helper()
	store := testutil.NewStoreAt(t, f.path)
	now := func() time.Time { return f.clock }
	memories := memory.NewService(store, sqlite.MemoryStore{Store: store}, retrieval.NewDocumentBuilder(), memory.WithClock(now))
	search := retrieval.NewService(store, retrieval.NewDocumentBuilder())
	provider, err := model.NewProvider(model.Config{Name: "test-model", BaseURL: f.fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.store = store
	f.memories = memories
	f.search = search
	f.sources = source.NewService(store, sqlite.SourceStore{Store: store}, source.WithClock(now))
	f.service = ingest.NewService(sqlite.IngestStore{Store: store}, store, ingest.WithClock(now))
	f.processor = ingest.NewProcessor(
		sqlite.IngestStore{Store: store},
		store,
		search,
		memories,
		provider,
		ingest.WithModelName("test-model"),
		ingest.WithProcessorClock(now),
	)
	f.worker = ingest.NewWorker(
		sqlite.IngestStore{Store: store},
		store,
		f.processor,
		ingest.WithWorkerClock(time.Now),
		ingest.WithRetryDelays(0, 0, 0),
		ingest.WithPollInterval(time.Millisecond),
	)
}

func (f *ingestStack) restart(t *testing.T) {
	t.Helper()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t)
}

func (f *ingestStack) advance(delta time.Duration) { f.clock = f.clock.Add(delta) }

func (f *ingestStack) submit(t *testing.T, content string) ingest.Receipt {
	t.Helper()
	receipt, err := f.service.Submit(context.Background(), ingest.Submission{
		AppID: "desktop-assistant", Content: json.RawMessage(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func (f *ingestStack) claim(t *testing.T) ingest.Job {
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

func (f *ingestStack) job(t *testing.T, id string) ingest.Job {
	t.Helper()
	job, err := f.service.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (f *ingestStack) processOne(t *testing.T) bool {
	t.Helper()
	processed, err := f.worker.ProcessOne(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return processed
}

func (f *ingestStack) searchMemory(t *testing.T, query string) retrieval.Result {
	t.Helper()
	result, err := f.search.Search(context.Background(), retrieval.Request{Query: query})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestEndToEndIngestPipeline(t *testing.T) {
	f := newIngestStack(t)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"action": "add", "kind": "preference", "title": "回答语言",
		"content":  "用户希望默认使用中文回答。",
		"evidence": []any{map[string]any{"message_id": "m1", "quote": "默认使用中文回答"}},
	}})
	receipt := f.submit(t, `{"messages":[{"id":"m1","content":"以后请默认使用中文回答。"}]}`)
	if !f.processOne(t) {
		t.Fatal("queued job was not processed")
	}
	job := f.job(t, receipt.JobID)
	if job.Status != ingest.JobSucceeded || job.Attempts != 1 || len(job.MemoryIDs) != 1 {
		t.Fatalf("job = %+v", job)
	}
	created, err := f.memories.Get(context.Background(), job.MemoryIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if created.Content != "用户希望默认使用中文回答。" || created.State != memory.StateActive {
		t.Fatalf("memory = %+v", created)
	}
	result := f.searchMemory(t, "中文回答")
	if len(result.Items) != 1 || result.Items[0].Memory.ID != created.ID {
		t.Fatalf("search = %+v", result)
	}
	if len(result.Items[0].EvidenceRefs) != 1 || result.Items[0].EvidenceRefs[0].SourceID != receipt.SourceID {
		t.Fatalf("evidence refs = %+v", result.Items[0].EvidenceRefs)
	}
}

func TestRedactionBeforeResultCommitWritesNothing(t *testing.T) {
	f := newIngestStack(t)
	candidate := `{"candidates":[{"action":"add","kind":"fact","content":"在途模型结果不应落库。","evidence":[{"message_id":"m1","quote":"在途模型结果"}]}]}`
	f.fake.Enqueue(testutil.FakeResponse{Block: true, Content: candidate})
	receipt := f.submit(t, `{"messages":[{"id":"m1","content":"在途模型结果不应落库。"}]}`)
	done := make(chan error, 1)
	go func() {
		processed, err := f.worker.ProcessOne(context.Background())
		if err == nil && !processed {
			err = errors.New("no job processed")
		}
		done <- err
	}()
	if err := f.fake.WaitStarted(context.Background()); err != nil {
		t.Fatal(err)
	}
	redaction, err := f.sources.Redact(context.Background(), receipt.SourceID)
	if err != nil {
		f.fake.Release()
		t.Fatal(err)
	}
	if redaction.CancelledJobs != 1 {
		f.fake.Release()
		t.Fatalf("cancelled jobs = %d", redaction.CancelledJobs)
	}
	f.fake.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	job := f.job(t, receipt.JobID)
	if job.Status != ingest.JobCancelled || len(job.MemoryIDs) != 0 {
		t.Fatalf("job = %+v", job)
	}
	db := testutil.OpenInspector(t, f.store.Path())
	for _, table := range []string{"memories", "memory_revisions", "revision_evidence", "evidence", "memory_search_docs"} {
		if count := integrationCount(t, db, `SELECT COUNT(*) FROM `+table); count != 0 {
			t.Fatalf("%s rows = %d", table, count)
		}
	}
	if count := integrationCount(t, db, `SELECT COUNT(*) FROM sources WHERE id = ? AND redacted = 1 AND content IS NULL`, receipt.SourceID); count != 1 {
		t.Fatalf("source redacted rows = %d", count)
	}
	if f.fake.RequestCount() != 1 {
		t.Fatalf("model requests = %d", f.fake.RequestCount())
	}
}

func TestResultCommitBeforeRedactionKeepsBodyAndClearsFragments(t *testing.T) {
	f := newIngestStack(t)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "title": "保留正文", "content": "已形成的记忆正文继续保留。",
		"evidence": []any{map[string]any{"message_id": "m1", "quote": "已形成的记忆正文"}},
	}})
	receipt := f.submit(t, `{"messages":[{"id":"m1","content":"已形成的记忆正文继续保留。"}]}`)
	if !f.processOne(t) {
		t.Fatal("job not processed")
	}
	job := f.job(t, receipt.JobID)
	if job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 1 {
		t.Fatalf("job = %+v", job)
	}
	memoryID := job.MemoryIDs[0]

	redaction, err := f.sources.Redact(context.Background(), receipt.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if redaction.CancelledJobs != 0 {
		t.Fatalf("cancelled jobs = %d", redaction.CancelledJobs)
	}
	current, err := f.memories.Get(context.Background(), memoryID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Content != "已形成的记忆正文继续保留。" {
		t.Fatalf("memory body = %q", current.Content)
	}
	evidence, err := f.memories.GetEvidence(context.Background(), memoryID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || !evidence[0].Redacted || evidence[0].Quote != "" || evidence[0].Location != nil {
		t.Fatalf("evidence after redaction = %+v", evidence)
	}
	result := f.searchMemory(t, "记忆正文")
	if len(result.Items) != 1 || result.Items[0].Memory.ID != memoryID {
		t.Fatalf("search after redaction = %+v", result)
	}
	if len(result.Items[0].EvidenceRefs) != 1 || result.Items[0].EvidenceRefs[0].SourceID != receipt.SourceID {
		t.Fatalf("evidence refs after redaction = %+v", result.Items[0].EvidenceRefs)
	}
	if strings.Contains(result.Items[0].Snippet, "已清除") {
		t.Fatalf("search snippet leaked redacted source text: %q", result.Items[0].Snippet)
	}
	db := testutil.OpenInspector(t, f.store.Path())
	if count := integrationCount(t, db, `SELECT COUNT(*) FROM evidence WHERE quote IS NOT NULL`); count != 0 {
		t.Fatalf("evidence quote copies remain: %d", count)
	}
	if count := integrationCount(t, db, `SELECT COUNT(*) FROM sources WHERE id = ? AND content IS NULL`, receipt.SourceID); count != 1 {
		t.Fatalf("source content remains")
	}
	if after := f.job(t, receipt.JobID); after.Status != ingest.JobSucceeded || len(after.MemoryIDs) != 1 {
		t.Fatalf("terminal job changed by redaction: %+v", after)
	}
}

func TestRestartRecoveryAndNoDuplicateSuccess(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()

	// Received but not yet claimed.
	f.fake.EnqueueContent(`{"candidates":[{"content":"接收后重启仍可处理。"}]}`)
	queued := f.submit(t, `"接收后重启仍可处理。"`)
	f.restart(t)
	if err := f.worker.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.processOne(t) {
		t.Fatal("queued job lost after restart")
	}
	queuedJob := f.job(t, queued.JobID)
	if queuedJob.Status != ingest.JobSucceeded || queuedJob.Attempts != 1 || len(queuedJob.MemoryIDs) != 1 {
		t.Fatalf("queued job after restart = %+v", queuedJob)
	}

	// Claimed to running before the crash.
	f.advance(time.Millisecond)
	f.fake.EnqueueContent(`{"candidates":[{"content":"记忆乙：运行中重启保留尝试次数。"}]}`)
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"add"}]}`)
	runningReceipt := f.submit(t, `"运行中重启保留尝试次数。"`)
	runningJob := f.claim(t)
	if runningJob.Attempts != 1 || runningJob.Status != ingest.JobRunning {
		t.Fatalf("claimed job = %+v", runningJob)
	}
	f.restart(t)
	if err := f.worker.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	requeued := f.job(t, runningReceipt.JobID)
	if requeued.Status != ingest.JobQueued || requeued.Attempts != 1 {
		t.Fatalf("requeued job = %+v", requeued)
	}
	if !f.processOne(t) {
		t.Fatal("recovered job not processed")
	}
	runningDone := f.job(t, runningReceipt.JobID)
	if runningDone.Status != ingest.JobSucceeded || runningDone.Attempts != 2 || len(runningDone.MemoryIDs) != 1 {
		t.Fatalf("running job after recovery = %+v", runningDone)
	}

	// Model returned, but the result transaction was interrupted before commit.
	f.advance(time.Millisecond)
	beforeFailureRequests := f.fake.RequestCount()
	f.fake.EnqueueContent(`{"candidates":[{"content":"记忆丙：结果事务中断后由重启补做。"}]}`)
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"add"}]}`)
	interruptedReceipt := f.submit(t, `"结果事务中断后由重启补做。"`)
	interruptedJob := f.claim(t)
	failingProcessor := ingest.NewProcessor(
		interruptingStore{IngestStore: sqlite.IngestStore{Store: f.store}},
		f.store, f.search, f.memories, mustIngestProvider(t, f.fake.URL()),
		ingest.WithModelName("test-model"),
		ingest.WithProcessorClock(func() time.Time { return f.clock }),
	)
	if _, err := failingProcessor.Process(ctx, interruptedJob); !errors.Is(err, errInjectedCommit) {
		t.Fatalf("interrupted error = %v", err)
	}
	if f.fake.RequestCount() != beforeFailureRequests+2 {
		t.Fatalf("model request count after interruption = %d", f.fake.RequestCount())
	}
	if count := integrationCount(t, testutil.OpenInspector(t, f.store.Path()), `SELECT COUNT(*) FROM memories`); count != 2 {
		t.Fatalf("memories after interrupted commit = %d", count)
	}
	f.restart(t)
	if err := f.worker.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if state := f.job(t, interruptedReceipt.JobID); state.Status != ingest.JobQueued || state.Attempts != 1 {
		t.Fatalf("interrupted job after restart = %+v", state)
	}
	f.fake.EnqueueContent(`{"candidates":[{"content":"记忆丙：结果事务中断后由重启补做。"}]}`)
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"add"}]}`)
	if !f.processOne(t) {
		t.Fatal("interrupted job not reprocessed")
	}
	interruptedDone := f.job(t, interruptedReceipt.JobID)
	if interruptedDone.Status != ingest.JobSucceeded || interruptedDone.Attempts != 2 || len(interruptedDone.MemoryIDs) != 1 {
		t.Fatalf("interrupted job after retry = %+v", interruptedDone)
	}

	// Successful result committed before the crash: never run it again.
	successID := interruptedDone.MemoryIDs[0]
	successMemory, err := f.memories.Get(ctx, successID)
	if err != nil {
		t.Fatal(err)
	}
	successVersions, err := f.memories.ListVersions(ctx, successID, 0)
	if err != nil {
		t.Fatal(err)
	}
	beforeRequests := f.fake.RequestCount()
	f.restart(t)
	if err := f.worker.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if f.processOne(t) {
		t.Fatal("succeeded job was claimed after restart")
	}
	if f.fake.RequestCount() != beforeRequests {
		t.Fatalf("model called again after successful restart: %d -> %d", beforeRequests, f.fake.RequestCount())
	}
	afterMemory, err := f.memories.Get(ctx, successID)
	if err != nil {
		t.Fatal(err)
	}
	afterVersions, err := f.memories.ListVersions(ctx, successID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if afterMemory.Version != successMemory.Version || len(afterVersions) != len(successVersions) {
		t.Fatalf("succeeded memory changed after restart: before=%+v after=%+v", successMemory, afterMemory)
	}
	if finalJob := f.job(t, interruptedReceipt.JobID); finalJob.Status != ingest.JobSucceeded || len(finalJob.MemoryIDs) != 1 {
		t.Fatalf("succeeded job changed after restart = %+v", finalJob)
	}
}

func TestModelOutageKeepsSubmissionQueryAndRecovery(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()
	f.fake.EnqueueCandidates([]any{map[string]any{"content": "模型不可用前的记忆。"}})
	first := f.submit(t, `"模型不可用前的记忆。"`)
	if !f.processOne(t) {
		t.Fatal("initial job not processed")
	}
	firstJob := f.job(t, first.JobID)
	existingID := firstJob.MemoryIDs[0]

	for i := 0; i < 3; i++ {
		f.fake.EnqueueStatus(503)
	}
	f.advance(time.Millisecond)
	outage := f.submit(t, `"模型不可用期间仍可接收。"`)
	deadline := time.Now().Add(3 * time.Second)
	for {
		job := f.job(t, outage.JobID)
		if job.Status == ingest.JobWaiting && job.Attempts == 3 {
			break
		}
		f.processOne(t)
		if time.Now().After(deadline) {
			t.Fatalf("outage job did not wait: %+v", job)
		}
	}
	if count := integrationCount(t, testutil.OpenInspector(t, f.store.Path()), `SELECT COUNT(*) FROM sources WHERE id = ?`, outage.SourceID); count != 1 {
		t.Fatalf("outage source rows = %d", count)
	}
	if _, err := f.memories.Get(ctx, existingID); err != nil {
		t.Fatalf("existing memory unavailable during model outage: %v", err)
	}
	if result := f.searchMemory(t, "模型不可用"); len(result.Items) != 1 || result.Items[0].Memory.ID != existingID {
		t.Fatalf("search during model outage = %+v", result)
	}

	// The same persisted task continues after the provider recovers; the user
	// does not resubmit the source.
	f.fake.EnqueueCandidates([]any{map[string]any{"content": "模型恢复后的记忆。"}})
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"add"}]}`)
	recoveredDeadline := time.Now().Add(3 * time.Second)
	for {
		job := f.job(t, outage.JobID)
		if job.Status == ingest.JobSucceeded {
			if len(job.MemoryIDs) != 1 {
				t.Fatalf("recovered outage job = %+v", job)
			}
			break
		}
		f.processOne(t)
		if time.Now().After(recoveredDeadline) {
			t.Fatalf("outage job did not recover: %+v", job)
		}
	}
}

func TestPartialAndZeroCandidateResults(t *testing.T) {
	f := newIngestStack(t)
	partial := `{"candidates":[
		{"action":"add","kind":"preference","content":"可用的部分候选。"},
		{"action":"update","kind":"unknown","content":""},
		{"action":"add","content":"另一个可用候选。","evidence":[{"quote":7}]}
	]}`
	f.fake.EnqueueContent(partial)
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"add"},{"candidate_id":"c2","action":"add"}]}`)
	receipt := f.submit(t, `"部分可解析输入"`)
	if !f.processOne(t) {
		t.Fatal("partial job not processed")
	}
	job := f.job(t, receipt.JobID)
	if job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 2 {
		t.Fatalf("partial job = %+v", job)
	}
	items, err := f.memories.List(context.Background(), memory.ListOptions{States: []memory.State{memory.StateActive}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("partial memories = %d", len(items))
	}

	f.fake.EnqueueContent(`{"candidates":[]}`)
	zero := f.submit(t, `"没有候选"`)
	if !f.processOne(t) {
		t.Fatal("zero job not processed")
	}
	zeroJob := f.job(t, zero.JobID)
	if zeroJob.Status != ingest.JobSucceeded || zeroJob.MemoryIDs == nil || len(zeroJob.MemoryIDs) != 0 {
		t.Fatalf("zero job = %+v", zeroJob)
	}
}

func mustIngestProvider(t *testing.T, baseURL string) *model.Provider {
	t.Helper()
	provider, err := model.NewProvider(model.Config{Name: "test-model", BaseURL: baseURL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}
