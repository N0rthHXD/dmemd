package governance_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type governanceFixture struct {
	store    *sqlite.Store
	memories *memory.Service
	now      time.Time
}

func newGovernanceFixture(t *testing.T) *governanceFixture {
	t.Helper()
	store := testutil.NewStore(t)
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	memories := memory.NewService(
		store,
		sqlite.MemoryStore{Store: store},
		testutil.DocumentBuilder{},
		memory.WithGovernanceDebounce(0),
		memory.WithClock(func() time.Time { return now }),
	)
	return &governanceFixture{store: store, memories: memories, now: now}
}

func (f *governanceFixture) create(t *testing.T, input memory.CreateInput) memory.Memory {
	t.Helper()
	if input.ObservedAt.IsZero() {
		input.ObservedAt = f.now
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

func (f *governanceFixture) revise(t *testing.T, input memory.ReviseInput) memory.Memory {
	t.Helper()
	var revised memory.Memory
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		revised, err = f.memories.Revise(ctx, tx, input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return revised
}

func (f *governanceFixture) classifyJob(t *testing.T, memoryID string) task.Job {
	t.Helper()
	job, err := f.store.GetGovernanceJobByTarget(context.Background(), task.KindClassify, memoryID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func newGovernanceWorker(f *governanceFixture, suggest governance.TopicSuggester,
	options ...governance.WorkerOption) *governance.Worker {
	limiter := governance.NewLimiter(0, 0)
	classify := governance.NewClassifyHandler(
		f.store,
		sqlite.GovernanceStore{Store: f.store},
		f.memories,
		suggest,
		limiter,
	)
	reconcile := governance.NewReconcileHandler(sqlite.GovernanceStore{Store: f.store}, 0, 100)
	base := []governance.WorkerOption{
		governance.WithReconcileInterval(0),
		governance.WithRetryDelays(0, 0, 0),
		governance.WithPollInterval(time.Millisecond),
	}
	return governance.NewWorker(
		sqlite.GovernanceStore{Store: f.store},
		map[task.Kind]governance.Handler{
			task.KindClassify:  classify,
			task.KindReconcile: reconcile,
		},
		append(base, options...)...,
	)
}

func TestClassifyTaskAutomaticallyLabelsPersistedMemory(t *testing.T) {
	f := newGovernanceFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "项目数据库",
		Content:  "项目使用 SQLite 保存记忆。",
		ScopeKey: "project:dmemd",
	})
	job := f.classifyJob(t, created.ID)
	if job.Status != task.StatusQueued || job.Revision != 1 {
		t.Fatalf("initial classify job = %+v", job)
	}

	var calls atomic.Int32
	worker := newGovernanceWorker(f, func(context.Context, string, string, string) ([]string, error) {
		calls.Add(1)
		return []string{"Go", " sqlite ", "go"}, nil
	})
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("classification calls = %d", calls.Load())
	}
	current, err := f.memories.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 2 || current.ScopeKey != "project:dmemd" {
		t.Fatalf("classified memory = %+v", current)
	}
	if len(current.Topics) != 2 || current.Topics[0] != "go" || current.Topics[1] != "sqlite" {
		t.Fatalf("topics = %#v", current.Topics)
	}
	revision, err := f.memories.GetVersion(context.Background(), created.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if revision.Operation != memory.OperationClassify || len(revision.Snapshot.Topics) != 2 {
		t.Fatalf("classification revision = %+v", revision)
	}
	if job = f.classifyJob(t, created.ID); job.Status != task.StatusSucceeded || job.Attempts != 1 {
		t.Fatalf("finished classify job = %+v", job)
	}
}

func TestClassifyTaskKeepsExistingTopicsWithoutModelCall(t *testing.T) {
	f := newGovernanceFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindPreference,
		Title:    "回答语言",
		Content:  "默认使用中文回答。",
		ScopeKey: "user",
		Topics:   []string{"Language"},
	})
	worker := newGovernanceWorker(f, func(context.Context, string, string, string) ([]string, error) {
		t.Fatal("classifier called although extraction supplied topics")
		return nil, nil
	})
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	current, err := f.memories.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 1 || len(current.Topics) != 1 || current.Topics[0] != "language" {
		t.Fatalf("memory after deterministic classification = %+v", current)
	}
	if job := f.classifyJob(t, created.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
}

func TestRestoreRequeuesCancelledClassification(t *testing.T) {
	f := newGovernanceFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "可恢复记忆",
		Content:  "删除后恢复应重新分类。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"restore"},
	})
	worker := newGovernanceWorker(f, func(context.Context, string, string, string) ([]string, error) {
		t.Fatal("classifier called for an already labeled restore")
		return nil, nil
	})
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("classify processed=%v err=%v", processed, err)
	}
	if _, err := f.memories.Delete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	job := f.classifyJob(t, created.ID)
	if job.Status != task.StatusCancelled {
		t.Fatalf("job after delete = %+v", job)
	}
	restored, err := f.memories.Restore(context.Background(), memory.RestoreInput{
		MemoryID: created.ID,
		Version:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != memory.StateActive || restored.Version != 3 {
		t.Fatalf("restored memory = %+v", restored)
	}
	job = f.classifyJob(t, created.ID)
	if job.Status != task.StatusQueued || job.Revision <= 1 {
		t.Fatalf("job after restore = %+v", job)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("post-restore classify processed=%v err=%v", processed, err)
	}
	if job = f.classifyJob(t, created.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("post-restore job = %+v", job)
	}
}

func TestReconcileConvergesWithoutCreatingVersions(t *testing.T) {
	f := newGovernanceFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "已有分类",
		Content:  "主题已经存在。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
	})
	worker := newGovernanceWorker(f, func(context.Context, string, string, string) ([]string, error) {
		return []string{"go"}, nil
	})
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("classify processed=%v err=%v", processed, err)
	}
	before, err := f.memories.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	versionsBefore, err := f.memories.ListVersions(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}

	for tick := 0; tick < 2; tick++ {
		now := time.Now().UTC()
		if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
				Kind:          task.KindReconcile,
				TargetKey:     task.ReconcileKey,
				InputHash:     fmt.Sprintf("tick-%d", tick),
				NextAttemptAt: &now,
				Now:           now,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
			t.Fatalf("reconcile %d processed=%v err=%v", tick, processed, err)
		}
	}
	after, err := f.memories.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	versionsAfter, err := f.memories.ListVersions(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version || len(versionsAfter) != len(versionsBefore) {
		t.Fatalf("reconcile created a version: before=%d after=%d history=%d/%d",
			before.Version, after.Version, len(versionsBefore), len(versionsAfter))
	}
	if job := f.classifyJob(t, created.ID); job.Status != task.StatusSucceeded || job.Revision != 1 {
		t.Fatalf("classify job after reconcile = %+v", job)
	}
}

func TestContentRevisionReevaluatesClassification(t *testing.T) {
	f := newGovernanceFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "旧主题",
		Content:  "旧内容。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"old"},
	})
	var calls atomic.Int32
	worker := newGovernanceWorker(f, func(context.Context, string, string, string) ([]string, error) {
		calls.Add(1)
		return []string{"new"}, nil
	})
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("initial classify processed=%v err=%v", processed, err)
	}
	revised := f.revise(t, memory.ReviseInput{
		MemoryID:   created.ID,
		Kind:       memory.KindFact,
		Title:      "新主题",
		Content:    "新内容。",
		ObservedAt: f.now,
		Operation:  memory.OperationUpdate,
	})
	if revised.Version != 2 || len(revised.Topics) != 0 {
		t.Fatalf("revised memory = %+v", revised)
	}
	if job := f.classifyJob(t, created.ID); job.Revision != 2 || job.Status != task.StatusQueued {
		t.Fatalf("classify job after content change = %+v", job)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("reclassify processed=%v err=%v", processed, err)
	}
	current, err := f.memories.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 3 || len(current.Topics) != 1 || current.Topics[0] != "new" {
		t.Fatalf("reclassified memory = %+v", current)
	}
	if calls.Load() != 1 {
		t.Fatalf("classification calls = %d", calls.Load())
	}
}

func TestInFlightClassificationResultIsDiscardedAfterInputChange(t *testing.T) {
	f := newGovernanceFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "旧内容",
		Content:  "旧内容正文。",
		ScopeKey: "project:dmemd",
	})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	worker := newGovernanceWorker(f, func(context.Context, string, string, string) ([]string, error) {
		started <- struct{}{}
		<-release
		return []string{"stale"}, nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := worker.ProcessOne(context.Background())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("classification model was not called")
	}

	revised := f.revise(t, memory.ReviseInput{
		MemoryID:   created.ID,
		Kind:       memory.KindFact,
		Title:      "新内容",
		Content:    "新内容正文。",
		ObservedAt: f.now,
		Operation:  memory.OperationUpdate,
	})
	if revised.Version != 2 {
		t.Fatalf("revised version = %d", revised.Version)
	}
	job := f.classifyJob(t, created.ID)
	if job.Revision != 2 || job.Status != task.StatusQueued {
		t.Fatalf("job after input change = %+v", job)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not finish")
	}

	current, err := f.memories.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Content != "新内容正文。" || current.Version != 2 || len(current.Topics) != 0 {
		t.Fatalf("stale classification was committed: %+v", current)
	}
	job = f.classifyJob(t, created.ID)
	if job.Status != task.StatusQueued || job.Revision != 2 {
		t.Fatalf("job after stale result = %+v", job)
	}
}

func TestRepeatedProtocolFailureIsolatesTask(t *testing.T) {
	f := newGovernanceFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:    memory.KindFact,
		Title:   "分类协议失败",
		Content: "没有标签。",
	})
	var calls atomic.Int32
	worker := newGovernanceWorker(f, func(context.Context, string, string, string) ([]string, error) {
		calls.Add(1)
		return nil, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ingest.ErrProtocol}
	})
	for i := 0; i < 3; i++ {
		processed, err := worker.ProcessOne(context.Background())
		if err != nil || !processed {
			t.Fatalf("attempt %d processed=%v err=%v", i+1, processed, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("classification calls = %d", calls.Load())
	}
	job := f.classifyJob(t, created.ID)
	if job.Status != task.StatusFailed || job.ConsecutiveFailures != 3 {
		t.Fatalf("job = %+v", job)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || processed {
		t.Fatalf("failed job claimed again: processed=%v err=%v", processed, err)
	}
	if err := worker.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	job = f.classifyJob(t, created.ID)
	if job.Status != task.StatusQueued || job.ConsecutiveFailures != 0 {
		t.Fatalf("job after protocol recovery = %+v", job)
	}
}

func TestTransientClassificationFailureResumesAutomatically(t *testing.T) {
	f := newGovernanceFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:    memory.KindFact,
		Title:   "暂时失败",
		Content: "模型稍后恢复。",
	})
	var calls atomic.Int32
	worker := newGovernanceWorker(f, func(context.Context, string, string, string) ([]string, error) {
		if calls.Add(1) == 1 {
			return nil, &ingest.ProviderError{Operation: "response", Retryable: true, Err: errors.New("temporary")}
		}
		return []string{"recovered"}, nil
	})
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("first attempt processed=%v err=%v", processed, err)
	}
	job := f.classifyJob(t, created.ID)
	if job.Status != task.StatusWaiting || job.WaitReason != task.WaitTransient || job.NextAttemptAt == nil {
		t.Fatalf("waiting job = %+v", job)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("recovery attempt processed=%v err=%v", processed, err)
	}
	current, err := f.memories.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Topics) != 1 || current.Topics[0] != "recovered" {
		t.Fatalf("topics after recovery = %#v", current.Topics)
	}
	if job = f.classifyJob(t, created.ID); job.Status != task.StatusSucceeded || job.Attempts != 2 {
		t.Fatalf("job after recovery = %+v", job)
	}
}

func TestLimiterSpacesCallsAndResetsHourlyWindow(t *testing.T) {
	start := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	spacing := governance.NewLimiter(0, 2*time.Minute)
	if ok, _ := spacing.Acquire(start); !ok {
		t.Fatal("first spaced call rejected")
	}
	if ok, next := spacing.Acquire(start.Add(time.Minute)); ok || !next.Equal(start.Add(2*time.Minute)) {
		t.Fatalf("second spaced call = %v next=%v", ok, next)
	}
	if ok, _ := spacing.Acquire(start.Add(2 * time.Minute)); !ok {
		t.Fatal("call at spacing boundary rejected")
	}

	budget := governance.NewLimiter(2, 0)
	if ok, _ := budget.Acquire(start); !ok {
		t.Fatal("first budgeted call rejected")
	}
	if ok, _ := budget.Acquire(start.Add(time.Second)); !ok {
		t.Fatal("second budgeted call rejected")
	}
	if ok, next := budget.Acquire(start.Add(2 * time.Second)); ok || !next.Equal(start.Add(time.Hour)) {
		t.Fatalf("third budgeted call = %v next=%v", ok, next)
	}
	if ok, _ := budget.Acquire(start.Add(time.Hour)); !ok {
		t.Fatal("call after budget window rejected")
	}
}
