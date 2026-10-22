package governance_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type artifactDocumentBuilder struct{}

func (artifactDocumentBuilder) BuildArtifactDocument(input artifact.DocumentInput) (artifact.SearchDocument, error) {
	return artifact.SearchDocument{
		ArtifactID:   input.ArtifactID,
		Version:      input.Version,
		TitleTerms:   strings.ToLower(input.Title),
		ContentTerms: strings.ToLower(input.Content),
	}, nil
}

type aggregateFunc func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error)

func (f aggregateFunc) Summarize(ctx context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
	return f(ctx, request)
}

type aggregateFixture struct {
	store     *sqlite.Store
	memories  *memory.Service
	artifacts *artifact.Service
	now       time.Time
}

func newAggregateFixture(t *testing.T) *aggregateFixture {
	t.Helper()
	store := testutil.NewStore(t)
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	artifacts := artifact.NewService(store, artifactDocumentBuilder{},
		artifact.WithClock(func() time.Time { return now }),
		artifact.WithDebounce(0),
	)
	memories := memory.NewService(
		store,
		sqlite.MemoryStore{Store: store},
		testutil.DocumentBuilder{},
		memory.WithGovernanceDebounce(0),
		memory.WithArtifactSync(artifacts),
		memory.WithClock(func() time.Time { return now }),
	)
	return &aggregateFixture{store: store, memories: memories, artifacts: artifacts, now: now}
}

func (f *aggregateFixture) create(t *testing.T, input memory.CreateInput) memory.Memory {
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

func (f *aggregateFixture) revise(t *testing.T, input memory.ReviseInput) memory.Memory {
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

func (f *aggregateFixture) aggregateWorker(t *testing.T, provider governance.AggregateProvider,
	options ...governance.AggregateOption) *governance.Worker {
	t.Helper()
	limiter := governance.NewLimiter(0, 0)
	classify := governance.NewClassifyHandler(
		f.store,
		sqlite.GovernanceStore{Store: f.store},
		f.memories,
		func(context.Context, string, string, string) ([]string, error) {
			t.Fatal("classifier called although the memory already has topics")
			return nil, nil
		},
		limiter,
		governance.WithClassifyClock(func() time.Time { return f.now }),
	)
	aggregate := governance.NewAggregateHandler(
		sqlite.GovernanceStore{Store: f.store},
		f.artifacts,
		provider,
		limiter,
		append([]governance.AggregateOption{
			governance.WithAggregateClock(func() time.Time { return f.now }),
		}, options...)...,
	)
	reconcile := governance.NewReconcileHandler(
		sqlite.GovernanceStore{Store: f.store}, 0, 100,
		governance.WithReconcileClock(func() time.Time { return f.now }),
		governance.WithArtifactReconcile(f.artifacts),
	)
	return governance.NewWorker(
		sqlite.GovernanceStore{Store: f.store},
		map[task.Kind]governance.Handler{
			task.KindClassify:  classify,
			task.KindAggregate: aggregate,
			task.KindReconcile: reconcile,
		},
		governance.WithReconcileInterval(0),
		governance.WithRetryDelays(0, 0, 0),
		governance.WithPollInterval(time.Millisecond),
		governance.WithWorkerClock(func() time.Time { return f.now }),
	)
}

func TestAggregateModelTimeoutDefersWithoutCommitting(t *testing.T) {
	f := newAggregateFixture(t)
	f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "数据库", Content: "项目使用 SQLite。",
		ScopeKey: "project:dmemd", Topics: []string{"go"}, ObservedAt: f.now,
	})
	provider := aggregateFunc(func(ctx context.Context, _ artifact.AggregateRequest) (artifact.AggregateResult, error) {
		<-ctx.Done()
		return artifact.AggregateResult{}, ctx.Err()
	})
	// Use a long retry delay so the timed-out target is not immediately retried
	// in the same test.
	worker := governance.NewWorker(
		sqlite.GovernanceStore{Store: f.store},
		map[task.Kind]governance.Handler{
			task.KindClassify: governance.NewClassifyHandler(
				f.store, sqlite.GovernanceStore{Store: f.store}, f.memories,
				func(context.Context, string, string, string) ([]string, error) {
					return []string{"go"}, nil
				},
				governance.NewLimiter(0, 0),
				governance.WithClassifyClock(func() time.Time { return f.now }),
			),
			task.KindAggregate: governance.NewAggregateHandler(
				sqlite.GovernanceStore{Store: f.store}, f.artifacts, provider,
				governance.NewLimiter(0, 0),
				governance.WithAggregateClock(func() time.Time { return f.now }),
				governance.WithAggregateTimeout(20*time.Millisecond),
			),
		},
		governance.WithReconcileInterval(0),
		governance.WithRetryDelays(time.Hour, time.Hour, time.Hour),
		governance.WithPollInterval(time.Millisecond),
		governance.WithWorkerClock(func() time.Time { return f.now }),
	)
	started := time.Now()
	processDue(t, worker)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timeout was not bounded: %v", elapsed)
	}
	job, err := f.store.GetGovernanceJobByTarget(context.Background(), task.KindAggregate,
		task.AggregateKey("project:dmemd", "go"))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != task.StatusWaiting || job.WaitReason != task.WaitTransient {
		t.Fatalf("timed-out aggregate job = %+v", job)
	}
	if _, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindTopicSummary,
		task.AggregateKey("project:dmemd", "go")); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("timed-out generation committed an artifact: %v", err)
	}
}

func processDue(t *testing.T, worker *governance.Worker) int {
	t.Helper()
	processed := 0
	for i := 0; i < 20; i++ {
		ok, err := worker.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		processed++
	}
	return processed
}

func (f *aggregateFixture) artifactByTarget(t *testing.T, kind artifact.Kind, targetKey string) artifact.Artifact {
	t.Helper()
	item, err := f.store.GetArtifactByTarget(context.Background(), kind, targetKey)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestAggregateCreatesActiveTopicSummaryAndProfile(t *testing.T) {
	f := newAggregateFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "项目数据库",
		Content:  "项目使用 SQLite 保存记忆。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go", "sqlite"},
	})
	var calls atomic.Int32
	worker := f.aggregateWorker(t, aggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
		calls.Add(1)
		if request.ScopeKey != "project:dmemd" || len(request.Memories) != 1 {
			t.Fatalf("aggregate request = %+v", request)
		}
		if request.Memories[0].ID != created.ID || request.Memories[0].Version != 1 {
			t.Fatalf("input memory = %+v", request.Memories[0])
		}
		return artifact.AggregateResult{Title: "摘要", Content: "项目使用 SQLite。"}, nil
	}))
	processDue(t, worker)

	summary := f.artifactByTarget(t, artifact.KindTopicSummary, task.AggregateKey("project:dmemd", "go"))
	if summary.State != artifact.StateActive || summary.CurrentVersion != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	detail, err := f.artifacts.Get(context.Background(), summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Revision == nil || detail.Revision.Content != "项目使用 SQLite。" {
		t.Fatalf("summary detail = %+v", detail)
	}
	if len(detail.Revision.MemoryRefs) != 1 || detail.Revision.MemoryRefs[0].MemoryID != created.ID ||
		detail.Revision.MemoryRefs[0].Version != 1 {
		t.Fatalf("summary sources = %+v", detail.Revision.MemoryRefs)
	}
	profile := f.artifactByTarget(t, artifact.KindProfile, task.ProfileKey("project:dmemd"))
	if profile.State != artifact.StateActive || profile.CurrentVersion != 1 {
		t.Fatalf("profile = %+v", profile)
	}
	if calls.Load() != 3 { // go, sqlite, profile
		t.Fatalf("model calls = %d", calls.Load())
	}
}

func TestAtomicChangeImmediatelyStalesSummaryAndRebuildsVersion(t *testing.T) {
	f := newAggregateFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "旧摘要",
		Content:  "旧内容。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
	})
	worker := f.aggregateWorker(t, aggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: "摘要:" + request.Topic + ":" + request.Memories[0].Content}, nil
	}))
	processDue(t, worker)
	targetKey := task.AggregateKey("project:dmemd", "go")
	before := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if before.State != artifact.StateActive {
		t.Fatalf("initial summary = %+v", before)
	}

	revised := f.revise(t, memory.ReviseInput{
		MemoryID:        created.ID,
		Kind:            memory.KindFact,
		Title:           "新摘要",
		Content:         "新内容。",
		ObservedAt:      f.now,
		ScopeKey:        "project:dmemd",
		Topics:          []string{"go"},
		Operation:       memory.OperationUpdate,
		AssertionStatus: memory.AssertionAccepted,
	})
	if revised.Version != 2 {
		t.Fatalf("revised version = %d", revised.Version)
	}
	stale := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if stale.State != artifact.StateStale {
		t.Fatalf("summary after atomic change = %+v", stale)
	}
	hits, err := f.artifacts.Search(context.Background(), artifact.SearchQuery{
		Expression: `"go"`, ScopeKey: "project:dmemd",
		Kinds: []artifact.Kind{artifact.KindTopicSummary}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("stale summary was still searchable: %+v", hits)
	}

	processDue(t, worker)
	after := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if after.State != artifact.StateActive || after.CurrentVersion != 2 {
		t.Fatalf("rebuilt summary = %+v", after)
	}
	detail, err := f.artifacts.Get(context.Background(), after.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Revision == nil || !strings.Contains(detail.Revision.Content, "新内容") {
		t.Fatalf("rebuilt content = %+v", detail.Revision)
	}
	if len(detail.Revision.MemoryRefs) != 1 || detail.Revision.MemoryRefs[0].Version != 2 {
		t.Fatalf("rebuilt sources = %+v", detail.Revision.MemoryRefs)
	}
	history, err := f.artifacts.GetVersion(context.Background(), after.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if history.Revision == nil || !strings.Contains(history.Revision.Content, "旧内容") ||
		len(history.Revision.MemoryRefs) != 1 || history.Revision.MemoryRefs[0].Version != 1 {
		t.Fatalf("immutable history = %+v", history.Revision)
	}
}

func TestArchiveDisablesSummaryAndRestoreRebuilds(t *testing.T) {
	f := newAggregateFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindPreference,
		Title:    "回答语言",
		Content:  "默认使用中文回答。",
		ScopeKey: "user",
		Topics:   []string{"language"},
	})
	worker := f.aggregateWorker(t, aggregateFunc(func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: "用户默认使用中文回答。"}, nil
	}))
	processDue(t, worker)
	targetKey := task.AggregateKey("user", "language")
	active := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if active.State != artifact.StateActive {
		t.Fatalf("initial = %+v", active)
	}
	if _, err := f.memories.UpdateState(context.Background(), created.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}
	if item := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey); item.State != artifact.StateStale {
		t.Fatalf("archive did not stale summary: %+v", item)
	}
	processDue(t, worker)
	disabled := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if disabled.State != artifact.StateDisabled {
		t.Fatalf("empty-input summary = %+v", disabled)
	}
	hits, err := f.artifacts.Search(context.Background(), artifact.SearchQuery{
		Expression: `"language"`, Kinds: []artifact.Kind{artifact.KindTopicSummary}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("disabled summary was searchable: %+v", hits)
	}

	if _, err := f.memories.UpdateState(context.Background(), created.ID, memory.StateActive); err != nil {
		t.Fatal(err)
	}
	processDue(t, worker)
	restored := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if restored.State != artifact.StateActive || restored.CurrentVersion != disabled.CurrentVersion+1 {
		t.Fatalf("restored summary = %+v", restored)
	}
}

func TestEvidenceOnlyRevisionKeepsSummaryActive(t *testing.T) {
	f := newAggregateFixture(t)
	src := source.Source{
		ID:        "source-evidence-only",
		Content:   []byte(`"项目使用 SQLite。"`),
		CreatedAt: f.now,
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertSource(ctx, src)
	}); err != nil {
		t.Fatal(err)
	}
	evidence := source.Evidence{
		ID: "evidence-one", SourceID: src.ID, MessageID: "m1", Quote: "项目使用 SQLite。", CreatedAt: f.now,
	}
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "项目数据库",
		Content:  "项目使用 SQLite。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
		Evidence: []source.Evidence{evidence},
	})
	worker := f.aggregateWorker(t, aggregateFunc(func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: "项目使用 SQLite。"}, nil
	}))
	processDue(t, worker)
	targetKey := task.AggregateKey("project:dmemd", "go")
	before := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if before.State != artifact.StateActive || before.CurrentVersion != 1 {
		t.Fatalf("initial summary = %+v", before)
	}
	f.revise(t, memory.ReviseInput{
		MemoryID:   created.ID,
		ObservedAt: f.now,
		Operation:  memory.OperationAddEvidence,
		Evidence: []source.Evidence{
			{ID: "evidence-two", SourceID: src.ID, MessageID: "m2", Quote: "补充证据。", CreatedAt: f.now},
		},
	})
	after := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if after.State != artifact.StateActive || after.CurrentVersion != 1 {
		t.Fatalf("evidence-only revision invalidated summary: %+v", after)
	}
}

func TestDeleteStalesSummaryAndDisablesWithoutInput(t *testing.T) {
	f := newAggregateFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "待删除",
		Content:  "删除后不再作为摘要依据。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
	})
	worker := f.aggregateWorker(t, aggregateFunc(func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: "项目摘要"}, nil
	}))
	processDue(t, worker)
	targetKey := task.AggregateKey("project:dmemd", "go")
	if item := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey); item.State != artifact.StateActive {
		t.Fatalf("initial summary = %+v", item)
	}
	if _, err := f.memories.Delete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if item := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey); item.State != artifact.StateStale {
		t.Fatalf("delete did not stale summary: %+v", item)
	}
	processDue(t, worker)
	if item := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey); item.State != artifact.StateDisabled {
		t.Fatalf("summary after delete = %+v", item)
	}
}

func TestPurgeRemovesArtifactsAndInFlightResultCannotRestore(t *testing.T) {
	f := newAggregateFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "待清除",
		Content:  "会被清除的内容。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
	})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	worker := f.aggregateWorker(t, aggregateFunc(func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return artifact.AggregateResult{Content: "迟到的摘要"}, nil
	}))
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 10; i++ {
			processed, err := worker.ProcessOne(context.Background())
			if err != nil {
				done <- err
				return
			}
			if !processed {
				break
			}
		}
		done <- nil
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("aggregate model was not called")
	}
	if err := f.memories.Purge(context.Background(), created.ID); err != nil {
		t.Fatal(err)
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
	item, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindTopicSummary, task.AggregateKey("project:dmemd", "go"))
	if err == nil && (item.State != artifact.StateDisabled || item.CurrentVersion != 0) {
		t.Fatalf("late result restored purged artifact: %+v", item)
	}
	if err != nil && !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("read purged artifact: %v", err)
	}
	hits, err := f.artifacts.Search(context.Background(), artifact.SearchQuery{
		Expression: `"go"`, ScopeKey: "project:dmemd",
		Kinds: []artifact.Kind{artifact.KindTopicSummary}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("purged content became searchable: %+v", hits)
	}
	_, err = f.memories.Get(context.Background(), created.ID)
	if !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("memory still exists after purge: %v", err)
	}
}

func TestReconcileRepairsStaleArtifactAfterCompletedJob(t *testing.T) {
	f := newAggregateFixture(t)
	f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "项目数据库",
		Content:  "项目使用 SQLite。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
	})
	worker := f.aggregateWorker(t, aggregateFunc(func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: "项目摘要"}, nil
	}))
	processDue(t, worker)
	targetKey := task.AggregateKey("project:dmemd", "go")
	before := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if before.State != artifact.StateActive || before.CurrentVersion != 1 {
		t.Fatalf("initial summary = %+v", before)
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		item, err := tx.GetArtifactByTarget(ctx, artifact.KindTopicSummary, targetKey)
		if err != nil {
			return err
		}
		item.State = artifact.StateStale
		item.UpdatedAt = f.now.Add(time.Minute)
		if err := tx.UpdateArtifact(ctx, item); err != nil {
			return err
		}
		return tx.DeleteArtifactSearchDocument(ctx, item.ID)
	}); err != nil {
		t.Fatal(err)
	}
	now := f.now
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:          task.KindReconcile,
			TargetKey:     task.ReconcileKey,
			InputHash:     "repair",
			NextAttemptAt: &now,
			Now:           now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	processDue(t, worker)
	after := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if after.State != artifact.StateActive || after.CurrentVersion != 2 {
		t.Fatalf("reconcile did not repair stale artifact: %+v", after)
	}
}

func TestReconcileStalesArtifactWhenDependencyExpiresWithoutWriteEvent(t *testing.T) {
	f := newAggregateFixture(t)
	from := f.now.Add(-2 * time.Hour)
	to := f.now.Add(2 * time.Hour)
	created := f.create(t, memory.CreateInput{
		Kind:           memory.KindFact,
		Title:          "临时项目事实",
		Content:        "该事实只在有限区间内有效。",
		ObservedAt:     f.now,
		ValidFrom:      &from,
		ValidTo:        &to,
		ValidFromBasis: memory.BasisExplicit,
		ValidToBasis:   memory.BasisExplicit,
		ScopeKey:       "project:dmemd",
		Topics:         []string{"go"},
	})
	worker := f.aggregateWorker(t, aggregateFunc(func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: "区间摘要"}, nil
	}))
	processDue(t, worker)
	targetKey := task.AggregateKey("project:dmemd", "go")
	if item := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey); item.State != artifact.StateActive {
		t.Fatalf("initial summary = %+v", item)
	}
	// Simulate wall-clock time passing without a memory write event by only
	// moving the persisted current validity boundary. No invalidation runs here.
	expiredTo := f.now.Add(-time.Minute)
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		current, err := tx.GetMemory(ctx, created.ID)
		if err != nil {
			return err
		}
		current.ValidTo = &expiredTo
		current.ValidToBasis = memory.BasisInferred
		return tx.UpdateMemory(ctx, current)
	}); err != nil {
		t.Fatal(err)
	}
	if item := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey); item.State != artifact.StateActive {
		t.Fatalf("direct validity change unexpectedly invalidated artifact: %+v", item)
	}

	now := f.now
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:          task.KindReconcile,
			TargetKey:     task.ReconcileKey,
			InputHash:     "expiry",
			NextAttemptAt: &now,
			Now:           now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	processDue(t, worker)
	expired := f.artifactByTarget(t, artifact.KindTopicSummary, targetKey)
	if expired.State != artifact.StateDisabled {
		t.Fatalf("expired summary was not disabled after reconcile: %+v", expired)
	}
}

func TestListAggregationGroupsFindsTopicAndProfileTargets(t *testing.T) {
	f := newAggregateFixture(t)
	f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "项目数据库",
		Content:  "项目使用 SQLite。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
	})
	groups, err := f.artifacts.ListAggregationGroups(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	foundTopic, foundProfile := false, false
	for _, group := range groups {
		if group.Target.Kind == artifact.KindTopicSummary && group.Target.Topic == "go" &&
			group.Target.ScopeKey == "project:dmemd" {
			foundTopic = true
		}
		if group.Target.Kind == artifact.KindProfile && group.Target.ScopeKey == "project:dmemd" {
			foundProfile = true
		}
	}
	if !foundTopic || !foundProfile {
		t.Fatalf("aggregation groups = %+v", groups)
	}
}

func TestInputLimitMarksPartialSummary(t *testing.T) {
	f := newAggregateFixture(t)
	for i := 0; i < 2; i++ {
		f.create(t, memory.CreateInput{
			Kind:     memory.KindFact,
			Title:    "输入",
			Content:  "同主题内容 " + string(rune('A'+i)),
			ScopeKey: "project:dmemd",
			Topics:   []string{"go"},
		})
	}
	var sawTruncated atomic.Bool
	worker := f.aggregateWorker(t, aggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
		if request.Kind == artifact.KindTopicSummary && request.Topic == "go" {
			if len(request.Memories) != 1 {
				t.Fatalf("bounded inputs = %+v", request.Memories)
			}
			sawTruncated.Store(request.Truncated)
		}
		return artifact.AggregateResult{Content: "局部摘要"}, nil
	}), governance.WithAggregateMaxInputs(1))
	processDue(t, worker)
	if !sawTruncated.Load() {
		t.Fatal("aggregate request did not declare truncation")
	}
	summary := f.artifactByTarget(t, artifact.KindTopicSummary, task.AggregateKey("project:dmemd", "go"))
	detail, err := f.artifacts.Get(context.Background(), summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Revision == nil || !detail.Revision.Truncated || detail.Revision.ReasonCode != "input_truncated" {
		t.Fatalf("truncated revision = %+v", detail.Revision)
	}
}
