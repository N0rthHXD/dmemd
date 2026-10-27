package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/model"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type processorFixture struct {
	store     *sqlite.Store
	clock     time.Time
	memories  *memory.Service
	search    *retrieval.Service
	service   *ingest.Service
	processor *ingest.Processor
	fake      *testutil.FakeModel
}

func newProcessorFixture(t *testing.T) *processorFixture {
	t.Helper()
	store := testutil.NewStore(t)
	fake := testutil.NewFakeModel(t)
	clock := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	f := &processorFixture{store: store, clock: clock, fake: fake}
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

func TestProcessorPersistsScopeTopicsAndClassifyTask(t *testing.T) {
	f := newProcessorFixture(t)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"content": "项目使用 SQLite 保存记忆。",
		"topics":  []string{"Go", "sqlite", "go"},
	}})
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"add"}]}`)
	_, err := f.service.Submit(context.Background(), ingest.Submission{
		AppID:    "desktop",
		ScopeKey: " project:dmemd ",
		Content:  json.RawMessage(`"项目使用 SQLite 保存记忆。"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	job := f.claim(t)
	result := f.process(t, job)
	if len(result.MemoryIDs) != 1 {
		t.Fatalf("result = %+v", result)
	}
	item, err := f.memories.Get(context.Background(), result.MemoryIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if item.ScopeKey != "project:dmemd" {
		t.Fatalf("scope_key = %q", item.ScopeKey)
	}
	if len(item.Topics) != 2 || item.Topics[0] != "go" || item.Topics[1] != "sqlite" {
		t.Fatalf("topics = %#v", item.Topics)
	}
	governanceJob, err := f.store.GetGovernanceJobByTarget(context.Background(), task.KindClassify, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if governanceJob.Status != task.StatusQueued || governanceJob.Revision != 1 {
		t.Fatalf("classify job = %+v", governanceJob)
	}
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

func (f *processorFixture) memory(t *testing.T, id string) memory.Memory {
	t.Helper()
	item, err := f.memories.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func (f *processorFixture) countMemories(t *testing.T, states ...memory.State) int {
	t.Helper()
	items, err := f.memories.List(context.Background(), memory.ListOptions{States: states, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func (f *processorFixture) createMemory(t *testing.T, input memory.CreateInput) memory.Memory {
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

func TestAddUpdateMergeDuplicateAndZeroCandidates(t *testing.T) {
	f := newProcessorFixture(t)
	ctx := context.Background()

	f.fake.EnqueueCandidates([]any{map[string]any{
		"action": "add", "kind": "preference", "title": "回答语言",
		"fact_group": map[string]any{"subject": "用户", "predicate": "回答语言", "cardinality": "single"},
		"content":    "用户希望默认使用中文回答。",
		"evidence":   []any{map[string]any{"message_id": "m1", "quote": "用户希望默认使用中文回答。"}},
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
		"kind": "preference", "title": "回答语言",
		"fact_group": map[string]any{"subject": "用户", "predicate": "回答语言", "cardinality": "single"},
		"content":    "用户希望默认使用英文回答。",
		"evidence":   []any{map[string]any{"message_id": "m2", "quote": "以后请默认使用英文回答"}},
	}})
	secondReceipt := f.submit(t, `{"messages":[{"id":"m2","content":"回答 语言 英文 以后请默认使用英文回答。"}]}`)
	secondJob := f.claim(t)
	result = f.process(t, secondJob)
	if len(result.MemoryIDs) != 2 || len(result.PendingMemoryIDs) != 0 || len(result.ConflictIDs) != 0 {
		t.Fatalf("update result = %+v", result)
	}
	oldID := firstID
	for _, id := range result.MemoryIDs {
		if id != oldID {
			firstID = id
		}
	}
	updated := f.memory(t, firstID)
	previous := f.memory(t, oldID)
	if updated.Version != 1 || updated.Content != "用户希望默认使用英文回答。" ||
		previous.AssertionStatus != memory.AssertionAccepted || previous.ValidTo == nil {
		t.Fatalf("updated = %+v previous=%+v", updated, previous)
	}
	if current, err := f.memories.GetEvidence(ctx, firstID, 0); err != nil || len(current) != 1 || current[0].SourceID != secondReceipt.SourceID {
		t.Fatalf("updated evidence = %+v err=%v", current, err)
	}
	if old, err := f.memories.GetEvidence(ctx, oldID, 1); err != nil || len(old) != 1 || old[0].SourceID != firstReceipt.SourceID {
		t.Fatalf("old evidence = %+v err=%v", old, err)
	}
	versions, err := f.memories.ListVersions(ctx, firstID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if versions[0].Reason != "ingest:auto_converge" {
		t.Fatalf("update reason = %q", versions[0].Reason)
	}

	requests := f.fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d", len(requests))
	}
	if item := f.memory(t, unrelated.ID); item.Version != 1 {
		t.Fatalf("unrelated memory changed: %+v", item)
	}

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"title": "回答语言", "content": "用户希望默认使用英文回答。",
		"evidence": []any{map[string]any{"message_id": "m3", "quote": "英文回答"}},
	}})
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"merge_evidence","target_memory_ids":["` + firstID + `"]}]}`)
	f.submit(t, `{"messages":[{"id":"m3","content":"回答 语言 英文 再次确认默认英文回答。"}]}`)
	thirdJob := f.claim(t)
	result = f.process(t, thirdJob)
	if len(result.MemoryIDs) != 1 || result.MemoryIDs[0] != firstID {
		t.Fatalf("merge result = %+v", result)
	}
	merged := f.memory(t, firstID)
	if merged.Version != 2 {
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
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"merge_evidence","target_memory_ids":["` + firstID + `"]}]}`)
	f.submit(t, `{"messages":[{"id":"m4","content":"回答 语言 英文 再次确认默认英文回答。"}]}`)
	fourthJob := f.claim(t)
	result = f.process(t, fourthJob)
	if len(result.MemoryIDs) != 1 || result.MemoryIDs[0] != firstID {
		t.Fatalf("duplicate add result = %+v", result)
	}
	if count := f.countMemories(t, memory.StateActive); count != 3 {
		t.Fatalf("active memories = %d, want old rejected + successor + unrelated", count)
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
	f.createMemory(t, memory.CreateInput{Kind: memory.KindFact, Title: "旧说明", Content: "不需要记忆的旧说明"})
	f.fake.EnqueueCandidates([]any{map[string]any{"content": "不需要记忆"}})
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"skip","reason_code":"not_durable"}]}`)
	f.submit(t, `"不需要记忆"`)
	skipJob := f.claim(t)
	result = f.process(t, skipJob)
	if result.Status != ingest.JobSucceeded || len(result.MemoryIDs) != 0 {
		t.Fatalf("skip result = %+v", result)
	}
}

func TestOlderContentAndIndependentEvents(t *testing.T) {
	f := newProcessorFixture(t)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"content": "当前偏好是中文。", "kind": "preference", "observed_at": "2026-10-20T02:00:00Z",
	}})
	receipt := f.submit(t, `"当前偏好是中文。"`)
	job := f.claim(t)
	result := f.process(t, job)
	targetID := result.MemoryIDs[0]

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "preference", "content": "很久以前偏好是英文。", "observed_at": "2020-01-01T00:00:00Z",
	}})
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"separate","content":"很久以前偏好是英文。"}]}`)
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

func TestModelRunsOutsideWriteTransaction(t *testing.T) {
	f := newProcessorFixture(t)
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

func TestResultTransactionRollbackLeavesNoHalfMemory(t *testing.T) {
	f := newProcessorFixture(t)
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

func TestArchivedAndPurgedTargetsAfterContextSelection(t *testing.T) {
	t.Run("archived target is skipped", func(t *testing.T) {
		f := newProcessorFixture(t)
		f.fake.EnqueueCandidates([]any{map[string]any{"content": "原始事实", "kind": "fact"}})
		f.submit(t, `"原始事实"`)
		result := f.process(t, f.claim(t))
		targetID := result.MemoryIDs[0]

		f.advance(time.Minute)
		f.fake.Enqueue(blockedResponse(`{"candidates":[{"content":"原始事实 候选计算后的更新"}]}`))
		f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"merge_evidence","target_memory_ids":["` + targetID + `"]}]}`)
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
		f := newProcessorFixture(t)
		f.fake.EnqueueCandidates([]any{map[string]any{"content": "将被清除的事实"}})
		f.submit(t, `"将被清除的事实"`)
		result := f.process(t, f.claim(t))
		targetID := result.MemoryIDs[0]

		f.advance(time.Minute)
		f.fake.Enqueue(blockedResponse(`{"candidates":[{"content":"清除后重新形成的事实"}]}`))
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

func TestContextExcludesIrrelevantSourcesAndMemories(t *testing.T) {
	f := newProcessorFixture(t)
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
			ID       string `json:"id"`
			Segments []struct {
				Text string `json:"text"`
			} `json:"segments"`
		} `json:"current_source"`
		Related []struct {
			Title string `json:"title"`
		} `json:"related_memories"`
	}
	if err := json.Unmarshal([]byte(message.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	containsText := false
	for _, segment := range payload.CurrentSource.Segments {
		if strings.Contains(segment.Text, "用户希望默认使用中文回答") {
			containsText = true
		}
	}
	if payload.CurrentSource.ID != receipt.SourceID || !containsText {
		t.Fatalf("current source = %+v", payload.CurrentSource)
	}
	if len(payload.Related) != 0 {
		t.Fatalf("zero-candidate extraction should not receive related context: %+v", payload.Related)
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

func TestUpdateWithUnchangedContentMergesEvidence(t *testing.T) {
	f := newProcessorFixture(t)
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

func TestProcessorPersistsTimingsUsageAndRetention(t *testing.T) {
	f := newProcessorFixture(t)
	f.fake.EnqueueRaw(200, modelCompletion(
		`{"candidates":[{"kind":"preference","content":"用户偏好中文","fact_group":{"subject":"用户","predicate":"语言","cardinality":"single"}}]}`,
		11, 5))
	receipt, err := f.service.Submit(context.Background(), ingest.Submission{Content: json.RawMessage(`"用户偏好中文"`)})
	if err != nil {
		t.Fatal(err)
	}
	if result := f.process(t, f.claim(t)); len(result.MemoryIDs) != 1 {
		t.Fatalf("result = %+v", result)
	}
	job := f.job(t, receipt.JobID)
	if job.ModelUsage.Calls != 1 || job.ModelUsage.InputTokens != 11 || job.ModelUsage.OutputTokens != 5 {
		t.Fatalf("model usage = %+v", job.ModelUsage)
	}
	if job.Retention.Retained != 1 || job.Retention.ByReason[string(memory.RetentionExplicitIntent)] != 1 {
		t.Fatalf("retention = %+v", job.Retention)
	}
	if job.Timings.TotalMillis < 0 || job.Timings.ExtractionMillis < 0 || job.Timings.CommitMillis < 0 {
		t.Fatalf("timings = %+v", job.Timings)
	}
}

func modelCompletion(content string, inputTokens, outputTokens int) string {
	body, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-test",
		"choices": []any{map[string]any{
			"index":   0,
			"message": map[string]string{"role": "assistant", "content": content},
		}},
		"usage": map[string]int{"prompt_tokens": inputTokens, "completion_tokens": outputTokens},
	})
	return string(body)
}

func TestMultipleCompleteIdentitiesSkipAvoidableDecisionCall(t *testing.T) {
	f := newProcessorFixture(t)
	f.fake.EnqueueCandidates([]any{
		map[string]any{
			"kind": "fact", "content": "用户常住上海",
			"fact_group": map[string]any{"subject": "用户", "predicate": "常住城市", "cardinality": "single"},
		},
		map[string]any{
			"kind": "fact", "content": "用户使用 Go",
			"fact_group": map[string]any{"subject": "用户", "predicate": "主要编程语言", "cardinality": "single"},
		},
	})
	if _, err := f.service.Submit(context.Background(), ingest.Submission{Content: json.RawMessage(`"用户常住上海。用户使用 Go。"`)}); err != nil {
		t.Fatal(err)
	}
	result := f.process(t, f.claim(t))
	if len(result.MemoryIDs) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if f.fake.RequestCount() != 1 {
		t.Fatalf("decision model was called for a fully deterministic batch: %d", f.fake.RequestCount())
	}
}
