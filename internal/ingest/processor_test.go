package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/model"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type p5Fixture struct {
	store     *sqlite.Store
	clock     time.Time
	memories  *memory.Service
	search    *retrieval.Service
	service   *ingest.Service
	processor *ingest.Processor
	fake      *testutil.FakeModel
}

func newP5Fixture(t *testing.T) *p5Fixture {
	t.Helper()
	store := testutil.NewStore(t)
	fake := testutil.NewFakeModel(t)
	clock := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	f := &p5Fixture{store: store, clock: clock, fake: fake}
	now := func() time.Time { return f.clock }
	f.memories = memory.NewService(store, sqlite.MemoryStore{Store: store}, retrieval.NewDocumentBuilder(), memory.WithClock(now))
	f.search = retrieval.NewService(store, retrieval.NewDocumentBuilder())
	provider, err := model.NewProvider(model.Config{Name: "test-model", BaseURL: fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.service = ingest.NewService(sqlite.IngestStore{Store: store}, store, ingest.WithClock(now))
	f.processor = ingest.NewProcessor(
		sqlite.IngestStore{Store: store},
		store,
		f.search,
		f.memories,
		provider,
		ingest.WithModelName("test-model"),
		ingest.WithProcessorClock(now),
	)
	return f
}

func (f *p5Fixture) advance(delta time.Duration) { f.clock = f.clock.Add(delta) }

func (f *p5Fixture) submit(t *testing.T, content string) ingest.Receipt {
	t.Helper()
	receipt, err := f.service.Submit(context.Background(), ingest.Submission{AppID: "desktop", Content: json.RawMessage(content)})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func (f *p5Fixture) claim(t *testing.T) ingest.Job {
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

func (f *p5Fixture) process(t *testing.T, job ingest.Job) ingest.ProcessResult {
	t.Helper()
	result, err := f.processor.Process(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *p5Fixture) job(t *testing.T, id string) ingest.Job {
	t.Helper()
	job, err := f.service.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (f *p5Fixture) memory(t *testing.T, id string) memory.Memory {
	t.Helper()
	item, err := f.memories.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func (f *p5Fixture) countMemories(t *testing.T, states ...memory.State) int {
	t.Helper()
	items, err := f.memories.List(context.Background(), memory.ListOptions{States: states, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func (f *p5Fixture) createMemory(t *testing.T, input memory.CreateInput) memory.Memory {
	t.Helper()
	if input.ObservedAt.IsZero() {
		input.ObservedAt = f.clock
	}
	var created memory.Memory
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = f.memories.Create(ctx, tx, input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return created
}

func TestP5AddUpdateMergeDuplicateAndZeroCandidates(t *testing.T) {
	f := newP5Fixture(t)
	ctx := context.Background()

	f.fake.EnqueueCandidates([]any{map[string]any{
		"action": "add", "kind": "preference", "title": "回答语言",
		"content":  "用户希望默认使用中文回答。",
		"evidence": []any{map[string]any{"message_id": "m1", "quote": "用户希望默认使用中文回答。"}},
	}})
	firstReceipt := f.submit(t, `{"messages":[{"id":"m1","content":"用户希望默认使用中文回答。"}]}`)
	firstJob := f.claim(t)
	result := f.process(t, firstJob)
	if result.Status != ingest.JobSucceeded || len(result.MemoryIDs) != 1 {
		t.Fatalf("first result = %+v", result)
	}
	firstID := result.MemoryIDs[0]
	first := f.memory(t, firstID)
	if first.Kind != memory.KindPreference || first.Title != "回答语言" || first.State != memory.StateActive ||
		first.CreatedByApp != "desktop" || first.OriginSourceID != firstReceipt.SourceID || first.Version != 1 {
		t.Fatalf("first memory = %+v", first)
	}
	if !first.ObservedAt.Equal(f.clock) {
		t.Fatalf("observed_at fallback = %v, want %v", first.ObservedAt, f.clock)
	}
	if job := f.job(t, firstJob.ID); job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 1 || job.Error != "" {
		t.Fatalf("first job = %+v", job)
	}
	evidence, err := f.memories.GetEvidence(ctx, firstID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Quote != "用户希望默认使用中文回答。" || evidence[0].SourceID != firstReceipt.SourceID ||
		evidence[0].ModelName != "test-model" {
		t.Fatalf("first evidence = %+v", evidence)
	}

	unrelated := f.createMemory(t, memory.CreateInput{Kind: memory.KindFact, Title: "部署", Content: "打开终端运行服务。"})
	archived := f.createMemory(t, memory.CreateInput{Kind: memory.KindFact, Title: "旧回答", Content: "归档回答。"})
	if _, err := f.memories.UpdateState(ctx, archived.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"action": "update", "target_memory_id": firstID, "kind": "preference", "title": "回答语言",
		"content": "用户希望默认使用英文回答。", "reason": "用户明确更正",
		"evidence": []any{map[string]any{"message_id": "m2", "quote": "默认使用英文回答"}},
	}})
	secondReceipt := f.submit(t, `{"messages":[{"id":"m2","content":"回答 语言 英文 以后请默认使用英文回答。"}]}`)
	secondJob := f.claim(t)
	result = f.process(t, secondJob)
	if len(result.MemoryIDs) != 1 || result.MemoryIDs[0] != firstID {
		t.Fatalf("update result = %+v", result)
	}
	updated := f.memory(t, firstID)
	if updated.Version != 2 || updated.Content != "用户希望默认使用英文回答。" {
		t.Fatalf("updated = %+v", updated)
	}
	if current, err := f.memories.GetEvidence(ctx, firstID, 0); err != nil || len(current) != 1 || current[0].SourceID != secondReceipt.SourceID {
		t.Fatalf("updated evidence = %+v err=%v", current, err)
	}
	if old, err := f.memories.GetEvidence(ctx, firstID, 1); err != nil || len(old) != 1 || old[0].SourceID != firstReceipt.SourceID {
		t.Fatalf("old evidence = %+v err=%v", old, err)
	}
	versions, err := f.memories.ListVersions(ctx, firstID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if versions[0].Reason != "用户明确更正" {
		t.Fatalf("update reason = %q", versions[0].Reason)
	}

	requests := f.fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d", len(requests))
	}
	var payload struct {
		CurrentSource struct {
			ID string `json:"id"`
		} `json:"current_source"`
		RelatedMemories []struct {
			ID string `json:"id"`
		} `json:"related_memories"`
	}
	var message struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requests[1].Body, &message); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(message.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CurrentSource.ID != secondReceipt.SourceID {
		t.Fatalf("current source = %+v", payload.CurrentSource)
	}
	if len(payload.RelatedMemories) != 1 || payload.RelatedMemories[0].ID != firstID {
		t.Fatalf("related memories = %+v; unrelated=%s archived=%s", payload.RelatedMemories, unrelated.ID, archived.ID)
	}

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"action": "merge_evidence", "target_memory_id": firstID, "title": "ignored",
		"content": "ignored", "evidence": []any{map[string]any{"message_id": "m3", "quote": "英文回答"}},
	}})
	f.submit(t, `{"messages":[{"id":"m3","content":"回答 语言 英文 再次确认默认英文回答。"}]}`)
	thirdJob := f.claim(t)
	result = f.process(t, thirdJob)
	if len(result.MemoryIDs) != 1 || result.MemoryIDs[0] != firstID {
		t.Fatalf("merge result = %+v", result)
	}
	merged := f.memory(t, firstID)
	if merged.Version != 3 {
		t.Fatalf("merged version = %d", merged.Version)
	}
	if current, err := f.memories.GetEvidence(ctx, firstID, 0); err != nil || len(current) != 2 {
		t.Fatalf("merged evidence = %+v err=%v", current, err)
	}

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"action": "add", "kind": "preference", "title": "回答语言",
		"content":  "用户希望默认使用英文回答。",
		"evidence": []any{map[string]any{"message_id": "m4", "quote": "英文回答"}},
	}})
	f.submit(t, `{"messages":[{"id":"m4","content":"回答 语言 英文 再次确认默认英文回答。"}]}`)
	fourthJob := f.claim(t)
	result = f.process(t, fourthJob)
	if len(result.MemoryIDs) != 1 || result.MemoryIDs[0] != firstID {
		t.Fatalf("duplicate add result = %+v", result)
	}
	if count := f.countMemories(t, memory.StateActive); count != 2 {
		t.Fatalf("active memories = %d, want first + unrelated", count)
	}

	f.advance(time.Minute)
	f.fake.EnqueueContent(`{"candidates":[]}`)
	zeroReceipt := f.submit(t, `"没有可提取内容的说明"`)
	zeroJob := f.claim(t)
	result = f.process(t, zeroJob)
	if result.Status != ingest.JobSucceeded || len(result.MemoryIDs) != 0 {
		t.Fatalf("zero result = %+v", result)
	}
	if job := f.job(t, zeroJob.ID); job.Status != ingest.JobSucceeded || job.MemoryIDs == nil || len(job.MemoryIDs) != 0 {
		t.Fatalf("zero job = %+v", job)
	}
	if zeroReceipt.SourceID == "" {
		t.Fatal("zero submission missing source")
	}

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{"action": "skip"}})
	f.submit(t, `"不需要记忆"`)
	skipJob := f.claim(t)
	result = f.process(t, skipJob)
	if result.Status != ingest.JobSucceeded || len(result.MemoryIDs) != 0 {
		t.Fatalf("skip result = %+v", result)
	}
}

func TestP5OlderContentAndIndependentEvents(t *testing.T) {
	f := newP5Fixture(t)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"content": "当前偏好是中文。", "kind": "preference", "observed_at": "2026-10-20T02:00:00Z",
	}})
	receipt := f.submit(t, `"当前偏好是中文。"`)
	job := f.claim(t)
	result := f.process(t, job)
	targetID := result.MemoryIDs[0]

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"action": "update", "target_memory_id": targetID, "kind": "preference",
		"content": "很久以前偏好是英文。", "observed_at": "2020-01-01T00:00:00Z",
	}})
	f.submit(t, `"很久以前偏好是英文。"`)
	job = f.claim(t)
	result = f.process(t, job)
	if len(result.MemoryIDs) != 1 || result.MemoryIDs[0] == targetID {
		t.Fatalf("older result = %+v", result)
	}
	target := f.memory(t, targetID)
	if target.Version != 1 || target.Content != "当前偏好是中文。" {
		t.Fatalf("older content overwrote current target: %+v", target)
	}
	older := f.memory(t, result.MemoryIDs[0])
	if older.ID == targetID || older.ObservedAt.Year() != 2020 || older.Content != "很久以前偏好是英文。" {
		t.Fatalf("older independent memory = %+v", older)
	}
	if receipt.SourceID == "" {
		t.Fatal("missing source")
	}
}

func TestP5ModelRunsOutsideWriteTransaction(t *testing.T) {
	f := newP5Fixture(t)
	f.fake.Enqueue(ingestFakeBlockedCandidate("用户正在等待模型。"))
	receipt := f.submit(t, `"用户正在等待模型。"`)
	job := f.claim(t)

	done := make(chan error, 1)
	go func() {
		_, err := f.processor.Process(context.Background(), job)
		done <- err
	}()
	if err := f.fake.WaitStarted(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := f.service.Submit(context.Background(), ingest.Submission{Content: json.RawMessage(`"并发提交"`)})
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		f.fake.Release()
		t.Fatal("submission blocked while the model was running")
	}
	f.fake.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("processing did not finish")
	}
	processed := f.job(t, job.ID)
	if processed.Status != ingest.JobSucceeded || len(processed.MemoryIDs) != 1 {
		t.Fatalf("job = %+v", processed)
	}
	if receipt.SourceID == "" {
		t.Fatal("missing source")
	}
}

func TestP5ResultTransactionRollbackLeavesNoHalfMemory(t *testing.T) {
	f := newP5Fixture(t)
	f.fake.EnqueueCandidates([]any{map[string]any{"content": "事务失败时不应落库。"}})
	f.submit(t, `"事务失败时不应落库。"`)
	job := f.claim(t)

	failingProcessor := ingest.NewProcessor(
		failingProcessorStore{IngestStore: sqlite.IngestStore{Store: f.store}},
		f.store,
		f.search,
		f.memories,
		mustProvider(t, f.fake.URL()),
		ingest.WithModelName("test-model"),
		ingest.WithProcessorClock(func() time.Time { return f.clock }),
	)
	if _, err := failingProcessor.Process(context.Background(), job); !errors.Is(err, errInjectedIngestFailure) {
		t.Fatalf("error = %v", err)
	}
	if count := f.countMemories(t, memory.StateActive, memory.StateArchived); count != 0 {
		t.Fatalf("rolled-back memories = %d", count)
	}
	current := f.job(t, job.ID)
	if current.Status != ingest.JobRunning || len(current.MemoryIDs) != 0 {
		t.Fatalf("job after rollback = %+v", current)
	}

	f.fake.EnqueueCandidates([]any{map[string]any{"content": "事务失败时不应落库。"}})
	result := f.process(t, job)
	if len(result.MemoryIDs) != 1 {
		t.Fatalf("retry result = %+v", result)
	}
	done := f.job(t, job.ID)
	if done.Status != ingest.JobSucceeded || len(done.MemoryIDs) != 1 {
		t.Fatalf("job after retry = %+v", done)
	}
}

func TestP5ArchivedAndPurgedTargetsAfterContextSelection(t *testing.T) {
	t.Run("archived target is skipped", func(t *testing.T) {
		f := newP5Fixture(t)
		f.fake.EnqueueCandidates([]any{map[string]any{"content": "原始事实", "kind": "fact"}})
		f.submit(t, `"原始事实"`)
		result := f.process(t, f.claim(t))
		targetID := result.MemoryIDs[0]

		f.advance(time.Minute)
		f.fake.Enqueue(blockedResponse(`{"candidates":[{"action":"update","target_memory_id":"` + targetID + `","content":"候选计算后的更新"}]}`))
		f.submit(t, `"原始事实 更新"`)
		job := f.claim(t)
		done := make(chan error, 1)
		go func() { _, err := f.processor.Process(context.Background(), job); done <- err }()
		if err := f.fake.WaitStarted(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.memories.UpdateState(context.Background(), targetID, memory.StateArchived); err != nil {
			f.fake.Release()
			t.Fatal(err)
		}
		f.fake.Release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		target := f.memory(t, targetID)
		if target.State != memory.StateArchived || target.Version != 2 || target.Content != "原始事实" {
			t.Fatalf("archived target changed: %+v", target)
		}
		if count := f.countMemories(t, memory.StateActive); count != 0 {
			t.Fatalf("new active memory created for archived target: %d", count)
		}
		if jobResult := f.job(t, job.ID); jobResult.Status != ingest.JobSucceeded || len(jobResult.MemoryIDs) != 0 {
			t.Fatalf("job = %+v", jobResult)
		}
	})

	t.Run("purged target update adds a new memory", func(t *testing.T) {
		f := newP5Fixture(t)
		f.fake.EnqueueCandidates([]any{map[string]any{"content": "将被清除的事实"}})
		f.submit(t, `"将被清除的事实"`)
		result := f.process(t, f.claim(t))
		targetID := result.MemoryIDs[0]

		f.advance(time.Minute)
		f.fake.Enqueue(blockedResponse(`{"candidates":[{"action":"update","target_memory_id":"` + targetID + `","content":"清除后重新形成的事实"}]}`))
		f.submit(t, `"将被清除的事实 重新形成"`)
		job := f.claim(t)
		done := make(chan error, 1)
		go func() { _, err := f.processor.Process(context.Background(), job); done <- err }()
		if err := f.fake.WaitStarted(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := f.memories.Purge(context.Background(), targetID); err != nil {
			f.fake.Release()
			t.Fatal(err)
		}
		f.fake.Release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		jobResult := f.job(t, job.ID)
		if jobResult.Status != ingest.JobSucceeded || len(jobResult.MemoryIDs) != 1 || jobResult.MemoryIDs[0] == targetID {
			t.Fatalf("job = %+v", jobResult)
		}
		created := f.memory(t, jobResult.MemoryIDs[0])
		if created.Content != "清除后重新形成的事实" || created.Version != 1 {
			t.Fatalf("new memory = %+v", created)
		}
	})
}

func TestP5ContextExcludesIrrelevantSourcesAndMemories(t *testing.T) {
	f := newP5Fixture(t)
	f.createMemory(t, memory.CreateInput{Kind: memory.KindFact, Title: "回答语言", Content: "用户希望默认使用中文回答。"})
	f.createMemory(t, memory.CreateInput{Kind: memory.KindFact, Title: "部署步骤", Content: "打开终端运行服务。"})
	f.fake.EnqueueContent(`{"candidates":[]}`)
	receipt := f.submit(t, `{"messages":[{"content":"用户希望默认使用中文回答。"}]}`)
	job := f.claim(t)
	if _, err := f.processor.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	requests := f.fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	var message struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requests[0].Body, &message); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		CurrentSource struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
		} `json:"current_source"`
		Related []struct {
			Title string `json:"title"`
		} `json:"related_memories"`
	}
	if err := json.Unmarshal([]byte(message.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CurrentSource.ID != receipt.SourceID || !strings.Contains(string(payload.CurrentSource.Content), "用户希望默认使用中文回答") {
		t.Fatalf("current source = %+v", payload.CurrentSource)
	}
	if len(payload.Related) != 1 || payload.Related[0].Title != "回答语言" {
		t.Fatalf("related = %+v", payload.Related)
	}
}

type failingProcessorStore struct {
	sqlite.IngestStore
}

func (f failingProcessorStore) WithTx(ctx context.Context, fn func(context.Context, ingest.Tx) error) error {
	return f.IngestStore.Store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return errInjectedIngestFailure
	})
}

func mustProvider(t *testing.T, baseURL string) *model.Provider {
	t.Helper()
	provider, err := model.NewProvider(model.Config{Name: "test-model", BaseURL: baseURL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func ingestFakeBlockedCandidate(content string) testutil.FakeResponse {
	candidate, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": content}}})
	return testutil.FakeResponse{Block: true, Content: string(candidate)}
}

func blockedResponse(content string) testutil.FakeResponse {
	return testutil.FakeResponse{Block: true, Content: content}
}

func TestP5UpdateWithUnchangedContentMergesEvidence(t *testing.T) {
	f := newP5Fixture(t)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"content": "保持不变的正文。", "kind": "fact",
		"evidence": []any{map[string]any{"message_id": "m1", "quote": "保持不变的正文"}},
	}})
	f.submit(t, `{"messages":[{"id":"m1","content":"保持不变的正文。"}]}`)
	result := f.process(t, f.claim(t))
	targetID := result.MemoryIDs[0]

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"action": "update", "target_memory_id": targetID, "kind": "fact",
		"content":  "保持不变的正文。",
		"evidence": []any{map[string]any{"message_id": "m2", "quote": "再次确认保持不变"}},
	}})
	f.submit(t, `{"messages":[{"id":"m2","content":"再次确认保持不变 保持不变的正文。"}]}`)
	result = f.process(t, f.claim(t))
	if len(result.MemoryIDs) != 1 || result.MemoryIDs[0] != targetID {
		t.Fatalf("result = %+v", result)
	}
	updated := f.memory(t, targetID)
	if updated.Version != 2 || updated.Content != "保持不变的正文。" {
		t.Fatalf("updated = %+v", updated)
	}
	evidence, err := f.memories.GetEvidence(context.Background(), targetID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 {
		t.Fatalf("unchanged-content update dropped prior evidence: %+v", evidence)
	}
}
