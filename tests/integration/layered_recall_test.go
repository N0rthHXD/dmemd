//go:build legacy

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
	httptransport "github.com/N0rthHXD/dmemd/internal/transport/http"
)

type recallFixture struct {
	store     *sqlite.Store
	memories  *memory.Service
	artifacts *artifact.Service
	search    *retrieval.Service
	now       time.Time
}

type recallAggregateFunc func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error)

func (f recallAggregateFunc) Summarize(ctx context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
	return f(ctx, request)
}

func newRecallFixture(t *testing.T) *recallFixture {
	t.Helper()
	store := testutil.NewStore(t)
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	artifacts := artifact.NewService(store, retrieval.NewArtifactDocumentBuilder(),
		artifact.WithClock(func() time.Time { return now }),
		artifact.WithDebounce(0),
	)
	memories := memory.NewService(store, sqlite.MemoryStore{Store: store}, retrieval.NewDocumentBuilder(),
		memory.WithGovernanceDebounce(0),
		memory.WithArtifactSync(artifacts),
		memory.WithClock(func() time.Time { return now }),
	)
	search := retrieval.NewService(store, retrieval.NewDocumentBuilder(), retrieval.WithArtifacts(artifacts))
	return &recallFixture{store: store, memories: memories, artifacts: artifacts, search: search, now: now}
}

func (f *recallFixture) create(t *testing.T, input memory.CreateInput) memory.Memory {
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

func (f *recallFixture) worker(t *testing.T) *governance.Worker {
	t.Helper()
	limiter := governance.NewLimiter(0, 0)
	classify := governance.NewClassifyHandler(f.store, sqlite.GovernanceStore{Store: f.store}, f.memories,
		func(context.Context, string, string, string) ([]string, error) { return nil, nil },
		limiter,
		governance.WithClassifyClock(func() time.Time { return f.now }),
	)
	aggregate := governance.NewAggregateHandler(sqlite.GovernanceStore{Store: f.store}, f.artifacts,
		recallAggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
			parts := make([]string, 0, len(request.Memories))
			for _, item := range request.Memories {
				parts = append(parts, item.Content)
			}
			return artifact.AggregateResult{
				Title:   request.Topic + " 摘要",
				Content: "项目摘要：" + strings.Join(parts, "；"),
			}, nil
		}),
		limiter,
		governance.WithAggregateClock(func() time.Time { return f.now }),
	)
	reconcile := governance.NewReconcileHandler(sqlite.GovernanceStore{Store: f.store}, 0, 100,
		governance.WithReconcileClock(func() time.Time { return f.now }),
		governance.WithArtifactReconcile(f.artifacts),
	)
	reviewConflict := governance.NewConflictHandler(sqlite.GovernanceStore{Store: f.store}, f.memories,
		governance.WithConflictClock(func() time.Time { return f.now }))
	return governance.NewWorker(
		sqlite.GovernanceStore{Store: f.store},
		map[task.Kind]governance.Handler{
			task.KindClassify:       classify,
			task.KindReviewConflict: reviewConflict,
			task.KindAggregate:      aggregate,
			task.KindReconcile:      reconcile,
		},
		governance.WithReconcileInterval(0),
		governance.WithRetryDelays(0, 0, 0),
		governance.WithPollInterval(time.Millisecond),
		governance.WithWorkerClock(func() time.Time { return f.now }),
	)
}

func (f *recallFixture) processAll(t *testing.T, worker *governance.Worker) {
	t.Helper()
	for i := 0; i < 20; i++ {
		processed, err := worker.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			return
		}
	}
	t.Fatal("governance did not quiesce")
}

func TestLayeredRecallDefaultLayersAndSelection(t *testing.T) {
	f := newRecallFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "项目数据库",
		Content:  "项目使用 SQLite 保存记忆。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
	})
	f.processAll(t, f.worker(t))
	if created.ID == "" {
		t.Fatal("memory was not created")
	}

	all, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Memories) != 1 || all.Memories[0].Memory.ID != created.ID {
		t.Fatalf("atomic partition = %+v", all.Memories)
	}
	if len(all.Summaries) == 0 {
		t.Fatalf("default recall did not include summaries: %+v", all)
	}
	if len(all.Experiences) != 0 || len(all.Skills) != 0 {
		t.Fatalf("unexpected experience/skill partitions: %+v", all)
	}

	atomic, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd", Layers: []retrieval.Layer{retrieval.LayerAtomic},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(atomic.Memories) != 1 || len(atomic.Summaries) != 0 {
		t.Fatalf("atomic-only recall = %+v", atomic)
	}

	summary, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd", Layers: []retrieval.Layer{retrieval.LayerSummary},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Memories) != 0 || len(summary.Summaries) == 0 {
		t.Fatalf("summary-only recall = %+v", summary)
	}

	filtered, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
		Filters: retrieval.Filters{Kinds: []memory.Kind{memory.KindPreference}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Memories) != 0 {
		t.Fatalf("kinds filter leaked into summaries: %+v", filtered.Memories)
	}
	if len(filtered.Summaries) == 0 {
		t.Fatalf("kinds filter incorrectly removed summaries: %+v", filtered)
	}

	other, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Memories) != 0 || len(other.Summaries) != 0 {
		t.Fatalf("scope isolation failed: %+v", other)
	}
}

func TestLayeredRecallUsesFilteredAnyForQuestionsAndKeywords(t *testing.T) {
	f := newRecallFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "饮食限制", Content: "花生过敏。", ScopeKey: "project:dmemd", Topics: []string{"饮食"},
	})
	f.processAll(t, f.worker(t))
	handler := httptransport.New(httptransport.Options{Services: httptransport.Services{Search: f.search}})
	for _, mode := range []retrieval.Mode{retrieval.ModeKeyword, retrieval.ModeHybrid} {
		var baseline map[string]any
		for _, query := range []string{"过敏", "我对什么过敏？", "我对什么过敏？火星殖民地"} {
			body := fmt.Sprintf(`{"query":%q,"mode":%q,"scope_key":"project:dmemd"}`, query, mode)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/memories/recall", strings.NewReader(body)))
			if response.Code != http.StatusOK {
				t.Fatalf("body=%s response=%d %s", body, response.Code, response.Body.String())
			}
			var decoded map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			memories := decoded["memories"].([]any)
			summaries := decoded["summaries"].([]any)
			if len(memories) != 1 || memories[0].(map[string]any)["memory"].(map[string]any)["id"] != created.ID || len(summaries) == 0 {
				t.Fatalf("partial any match missing for %s: %s", body, response.Body.String())
			}
			for _, field := range []string{"match", "requested_match", "effective_match", "match_relaxed"} {
				if strings.Contains(response.Body.String(), `"`+field+`"`) {
					t.Fatalf("matching mode field %s leaked: %s", field, response.Body.String())
				}
			}
			warnings := decoded["warnings"].([]any)
			if mode == retrieval.ModeKeyword && len(warnings) != 0 || mode == retrieval.ModeHybrid && !reflect.DeepEqual(warnings, []any{retrieval.WarningSemanticUnavailable}) {
				t.Fatalf("unexpected warnings for %s: %v", body, warnings)
			}
			// Stopwords and unmatched extra terms must not remove candidates.
			// Additional effective terms can change the keyword coverage score.
			for _, item := range memories {
				delete(item.(map[string]any), "score")
			}
			if baseline == nil {
				baseline = decoded
			} else if !reflect.DeepEqual(decoded, baseline) {
				t.Fatalf("recall differs for %s: %s", body, response.Body.String())
			}
		}
	}
}

func TestLayeredRecallStalesSummaryImmediatelyAndHistoricalQueryUsesAtomicOnly(t *testing.T) {
	f := newRecallFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "项目数据库",
		Content:  "项目使用 SQLite 保存记忆。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
	})
	f.processAll(t, f.worker(t))
	before, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Summaries) == 0 {
		t.Fatal("initial summary missing")
	}

	var revised memory.Memory
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		revised, err = f.memories.Revise(ctx, tx, memory.ReviseInput{
			MemoryID:        created.ID,
			Kind:            memory.KindFact,
			Title:           "项目数据库",
			Content:         "项目改用 PostgreSQL 保存记忆。",
			ObservedAt:      f.now,
			ScopeKey:        "project:dmemd",
			Topics:          []string{"go"},
			Operation:       memory.OperationUpdate,
			AssertionStatus: memory.AssertionAccepted,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if revised.Version != 2 {
		t.Fatalf("revised version = %d", revised.Version)
	}

	immediate, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(immediate.Memories) != 1 || len(immediate.Summaries) != 0 {
		t.Fatalf("stale summary remained in default recall: %+v", immediate)
	}

	historical, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
		Filters: retrieval.Filters{AsOf: &f.now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(historical.Memories) != 1 || len(historical.Summaries) != 0 {
		t.Fatalf("historical recall partitions = %+v", historical)
	}
	if !contains(historical.Warnings, retrieval.WarningDerivedAsOfUnsupported) {
		t.Fatalf("historical derived warning missing: %+v", historical.Warnings)
	}

	historicalAtomic, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd", Layers: []retrieval.Layer{retrieval.LayerAtomic},
		Filters: retrieval.Filters{AsOf: &f.now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if contains(historicalAtomic.Warnings, retrieval.WarningDerivedAsOfUnsupported) {
		t.Fatalf("atomic-only historical recall warned: %+v", historicalAtomic.Warnings)
	}

	f.processAll(t, f.worker(t))
	rebuilt, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilt.Summaries) == 0 {
		t.Fatalf("summary was not rebuilt: %+v", rebuilt)
	}
	for _, hit := range rebuilt.Summaries {
		if strings.Contains(hit.Revision.Content, "SQLite") {
			t.Fatalf("rebuilt summary still contains old atomic content: %+v", hit.Revision)
		}
	}
}

func TestDerivedRecallExcludesExpiredDependencyBeforeReconcile(t *testing.T) {
	f := newRecallFixture(t)
	wallNow := time.Now().UTC()
	from := f.now.Add(-2 * time.Hour)
	to := wallNow.Add(2 * time.Hour)
	created := f.create(t, memory.CreateInput{
		Kind:           memory.KindFact,
		Title:          "有限期事实",
		Content:        "项目只在有限期内使用 SQLite。",
		ObservedAt:     f.now,
		ValidFrom:      &from,
		ValidTo:        &to,
		ValidFromBasis: memory.BasisExplicit,
		ValidToBasis:   memory.BasisExplicit,
		ScopeKey:       "project:dmemd",
		Topics:         []string{"go"},
	})
	f.processAll(t, f.worker(t))
	initial, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Memories) != 1 || len(initial.Summaries) == 0 {
		t.Fatalf("initial recall = %+v", initial)
	}

	// Move only the persisted current boundary, as wall-clock time would. No
	// memory write event runs, so only the recall-time SQL time filter can keep
	// the expired dependency out of the active summary.
	expiredTo := time.Now().UTC().Add(-time.Minute)
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
	expired, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(expired.Memories) != 0 || len(expired.Summaries) != 0 {
		t.Fatalf("expired dependency leaked into recall: %+v", expired)
	}
	onlySummary, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd", Layers: []retrieval.Layer{retrieval.LayerSummary},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlySummary.Summaries) != 0 {
		t.Fatalf("expired summary surfaced in summary layer: %+v", onlySummary)
	}
}

func TestLayeredRecallArchiveRemovesSummaryFromDefaultRecall(t *testing.T) {
	f := newRecallFixture(t)
	created := f.create(t, memory.CreateInput{
		Kind:     memory.KindFact,
		Title:    "项目数据库",
		Content:  "项目使用 SQLite 保存记忆。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"go"},
	})
	f.processAll(t, f.worker(t))
	if _, err := f.memories.UpdateState(context.Background(), created.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}
	result, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Memories) != 0 || len(result.Summaries) != 0 {
		t.Fatalf("archived memory or stale summary surfaced: %+v", result)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
