//go:build legacy

package capacity_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

const (
	defaultCapacityMemories = 10000
	defaultScopes           = 100
	topicsPerScope          = 10
	seedBatchSize           = 100
)

// TestCapacity10k is opt-in. It seeds 10,000 atomic memories, generates the
// corresponding summaries, profiles and skills, reads concurrently while the
// governance backlog drains, and then measures one permanent-forget transaction
// that fans out to the whole source. It records timings but does not treat a
// developer workstation as a performance SLA.
func TestCapacity10k(t *testing.T) {
	if os.Getenv("DMEMD_CAPACITY") != "1" {
		t.Skip("set DMEMD_CAPACITY=1 (make test-capacity) to run the 10k capacity scenario")
	}
	if testing.Short() {
		t.Skip("capacity scenario skipped in -short mode")
	}
	total := capacitySize(t)
	scopes := defaultScopes
	if total < scopes*10 {
		scopes = total / 10
	}
	if scopes < 10 {
		scopes = 10
	}
	perScope := (total + scopes - 1) / scopes

	store := testutil.NewStore(t)
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	artifacts := artifact.NewService(store, retrieval.NewArtifactDocumentBuilder(),
		artifact.WithClock(clock), artifact.WithDebounce(0))
	memories := memory.NewService(store, sqlite.MemoryStore{Store: store},
		retrieval.NewDocumentBuilder(),
		memory.WithGovernanceDebounce(0),
		memory.WithArtifactSync(artifacts),
		memory.WithClock(clock))
	recall := retrieval.NewService(store, retrieval.NewDocumentBuilder(),
		retrieval.WithArtifacts(artifacts))

	sourceID := idgen.NewUUID()
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertSource(ctx, source.Source{
			ID:        sourceID,
			AppID:     "capacity",
			ScopeKey:  "capacity",
			Content:   json.RawMessage(`"capacity fixture source"`),
			CreatedAt: now,
		})
	}); err != nil {
		t.Fatalf("insert source: %v", err)
	}

	summaryTargets := make(map[string]struct{})
	profileTargets := make(map[string]struct{})
	skillTargets := make(map[string]struct{})

	seedStarted := time.Now()
	for start := 0; start < total; start += seedBatchSize {
		end := start + seedBatchSize
		if end > total {
			end = total
		}
		if err := store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			for index := start; index < end; index++ {
				scopeIndex := index / perScope
				if scopeIndex >= scopes {
					scopeIndex = scopes - 1
				}
				scopeKey := fmt.Sprintf("project:capacity-%03d", scopeIndex)
				topicIndex := index % topicsPerScope
				topic := fmt.Sprintf("topic-%02d", topicIndex)
				kind := memory.KindFact
				if topicIndex == 0 {
					kind = memory.KindProcedure
				}
				title := fmt.Sprintf("容量记忆 %05d", index)
				content := fmt.Sprintf("容量测试记忆 %05d 使用 SQLite 保存 scope %s topic %s。",
					index, scopeKey, topic)
				if _, err := memories.Create(ctx, tx, memory.CreateInput{
					Kind:           kind,
					Title:          title,
					Content:        content,
					ObservedAt:     now,
					OriginSourceID: sourceID,
					CreatedByApp:   "capacity",
					ScopeKey:       scopeKey,
					Topics:         []string{topic},
				}); err != nil {
					return err
				}
				summaryTargets[scopeKey+"|"+topic] = struct{}{}
				profileTargets[scopeKey] = struct{}{}
				if kind == memory.KindProcedure {
					skillTargets[scopeKey+"|"+topic] = struct{}{}
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("seed memories %d..%d: %v", start, end, err)
		}
		if end%2000 == 0 || end == total {
			t.Logf("capacity seed: %d/%d memories, %s", end, total, time.Since(seedStarted).Round(time.Millisecond))
		}
	}
	seedDuration := time.Since(seedStarted)
	expectedArtifacts := len(summaryTargets) + len(profileTargets) + len(skillTargets)
	t.Logf("capacity seed complete: memories=%d scopes=%d expected_artifacts=%d duration=%s db_bytes=%d",
		total, len(profileTargets), expectedArtifacts, seedDuration.Round(time.Millisecond), databaseBytes(store))

	queuedBefore := jobCounts(t, store)
	t.Logf("capacity backlog before drain: %v", queuedBefore)

	classify := governance.NewClassifyHandler(store,
		sqlite.GovernanceStore{Store: store}, memories,
		func(context.Context, string, string, string) ([]string, error) { return nil, nil },
		governance.NewLimiter(0, 0),
		governance.WithClassifyClock(clock))
	aggregate := governance.NewAggregateHandler(
		sqlite.GovernanceStore{Store: store}, artifacts,
		capacitySummaryProvider{},
		governance.NewLimiter(0, 0),
		governance.WithAggregateClock(clock),
	)
	build := governance.NewSkillHandler(
		sqlite.GovernanceStore{Store: store}, artifacts,
		capacitySkillProvider{},
		governance.NewLimiter(0, 0),
		governance.WithSkillClock(clock),
	)
	worker := governance.NewWorker(
		sqlite.GovernanceStore{Store: store},
		map[task.Kind]governance.Handler{
			task.KindClassify:   classify,
			task.KindAggregate:  aggregate,
			task.KindBuildSkill: build,
		},
		governance.WithReconcileInterval(0),
		governance.WithRetryDelays(0, 0, 0),
		governance.WithPollInterval(time.Millisecond),
		governance.WithWorkerClock(clock),
	)

	stopReads := make(chan struct{})
	var readWG sync.WaitGroup
	var latencies []time.Duration
	var recallErr error
	readWG.Add(1)
	go func() {
		defer readWG.Done()
		rng := rand.New(rand.NewSource(20261022))
		for {
			select {
			case <-stopReads:
				return
			default:
			}
			scope := fmt.Sprintf("project:capacity-%03d", rng.Intn(scopes))
			started := time.Now()
			result, err := recall.Recall(context.Background(), retrieval.RecallRequest{
				Query: "容量", ScopeKey: scope, TopK: 10,
			})
			latencies = append(latencies, time.Since(started))
			if err != nil {
				recallErr = err
				return
			}
			if len(result.Memories) == 0 {
				recallErr = fmt.Errorf("concurrent recall for %s returned no atomic memory", scope)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	drainStarted := time.Now()
	processed := 0
	for {
		ok, err := worker.ProcessOne(context.Background())
		if err != nil {
			close(stopReads)
			readWG.Wait()
			t.Fatalf("drain governance backlog after %d tasks: %v", processed, err)
		}
		if !ok {
			break
		}
		processed++
		if processed > 200000 {
			close(stopReads)
			readWG.Wait()
			t.Fatal("governance backlog did not converge within 200000 tasks")
		}
		if processed%2000 == 0 {
			t.Logf("capacity drain: %d tasks, %s", processed, time.Since(drainStarted).Round(time.Millisecond))
		}
	}
	close(stopReads)
	readWG.Wait()
	if recallErr != nil {
		t.Fatalf("concurrent recall: %v", recallErr)
	}
	drainDuration := time.Since(drainStarted)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("capacity drain complete: tasks=%d duration=%s recalls=%d errors=0 p50=%s p95=%s max=%s",
		processed, drainDuration.Round(time.Millisecond), len(latencies),
		percentile(latencies, 0.50), percentile(latencies, 0.95), percentile(latencies, 1.0))

	after := jobCounts(t, store)
	t.Logf("capacity backlog after drain: %v", after)
	if after["queued"] != 0 || after["running"] != 0 || after["waiting"] != 0 || after["failed"] != 0 {
		t.Fatalf("governance backlog did not converge: %v", after)
	}
	if got := rowCount(t, store, "artifacts"); got != expectedArtifacts {
		t.Fatalf("artifacts = %d, want %d", got, expectedArtifacts)
	}
	if got := rowCount(t, store, "artifact_revisions"); got != expectedArtifacts {
		t.Fatalf("artifact revisions = %d, want %d", got, expectedArtifacts)
	}
	if got := rowCount(t, store, "artifact_search_docs"); got != expectedArtifacts {
		t.Fatalf("artifact search docs = %d, want %d", got, expectedArtifacts)
	}
	if got := rowCount(t, store, "memory_search_docs"); got != total {
		t.Fatalf("memory search docs = %d, want %d", got, total)
	}
	var heap runtime.MemStats
	runtime.ReadMemStats(&heap)
	t.Logf("capacity after drain: active_artifacts=%d db_bytes=%d heap_alloc_mb=%.1f",
		expectedArtifacts, databaseBytes(store), float64(heap.HeapAlloc)/(1<<20))

	summary, err := recall.Recall(context.Background(), retrieval.RecallRequest{
		Query: "容量", ScopeKey: "project:capacity-000", TopK: 20,
	})
	if err != nil {
		t.Fatalf("warm recall: %v", err)
	}
	if len(summary.Memories) == 0 || len(summary.Summaries) == 0 || len(summary.Skills) == 0 {
		t.Fatalf("layered recall after drain = memories:%d summaries:%d skills:%d",
			len(summary.Memories), len(summary.Summaries), len(summary.Skills))
	}

	forget := governance.NewForgetService(sqlite.GovernanceStore{Store: store}, memories,
		governance.WithForgetClock(clock))
	cleanupStarted := time.Now()
	result, err := forget.Forget(context.Background(), governance.ForgetRequest{
		SourceIDs: []string{sourceID},
	})
	cleanupDuration := time.Since(cleanupStarted)
	if err != nil {
		t.Fatalf("permanent forget: %v", err)
	}
	if len(result.MemoryIDs) != total {
		t.Fatalf("forget returned %d memories, want %d", len(result.MemoryIDs), total)
	}
	t.Logf("capacity forget complete: memories=%d sources=%d duration=%s db_bytes=%d",
		len(result.MemoryIDs), len(result.SourceIDs), cleanupDuration.Round(time.Millisecond), databaseBytes(store))

	for _, table := range []string{
		"memories", "memory_revisions", "memory_search_docs", "evidence",
		"artifacts", "artifact_revisions", "artifact_memory_dependencies",
		"artifact_search_docs", "artifact_fts", "sources", "ingest_jobs",
	} {
		if got := rowCount(t, store, table); got != 0 {
			t.Errorf("%s rows after forget = %d, want 0", table, got)
		}
	}
	remaining := jobCounts(t, store)
	t.Logf("capacity remaining governance targets after forget: %v", remaining)
	if remaining["running"] != 0 {
		t.Fatalf("governance task still running after forget: %v", remaining)
	}
	var integrity string
	if err := testutil.OpenInspector(t, store.Path()).QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q", integrity)
	}
	cleared, err := recall.Recall(context.Background(), retrieval.RecallRequest{
		Query: "容量", ScopeKey: "project:capacity-000", TopK: 20,
	})
	if err != nil {
		t.Fatalf("post-forget recall: %v", err)
	}
	if len(cleared.Memories) != 0 || len(cleared.Summaries) != 0 || len(cleared.Skills) != 0 {
		t.Fatalf("forgotten content remained recallable: %+v", cleared)
	}
	t.Logf("capacity scenario complete: seed=%s drain=%s forget=%s",
		seedDuration.Round(time.Millisecond), drainDuration.Round(time.Millisecond),
		cleanupDuration.Round(time.Millisecond))
}

type capacitySummaryProvider struct{}

func (capacitySummaryProvider) Summarize(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
	return artifact.AggregateResult{
		Title:   fmt.Sprintf("%s %s 摘要", request.ScopeKey, request.Topic),
		Content: fmt.Sprintf("作用域 %s 主题 %s 的容量摘要，覆盖 %d 条依据。", request.ScopeKey, request.Topic, len(request.Memories)),
	}, nil
}

type capacitySkillProvider struct{}

func (capacitySkillProvider) ComposeSkill(_ context.Context, request skill.Request) (skill.Draft, error) {
	return skill.Draft{
		Name:             "容量验证工作流",
		Description:      "仅用于容量测试的指导文本",
		Content:          "## 前置条件\n- 依赖已安装\n\n## 步骤\n1. 运行 go test\n\n## 停止条件\n- 测试失败时停止\n\n## 反例\n- 未安装依赖时不要执行",
		Preconditions:    []string{"依赖已安装"},
		Steps:            []string{"运行 go test"},
		StopConditions:   []string{"测试失败时停止"},
		CompletionChecks: []string{"测试通过"},
		Counterexamples:  []string{"未安装依赖时不要执行"},
	}, nil
}

func capacitySize(t *testing.T) int {
	t.Helper()
	value := os.Getenv("DMEMD_CAPACITY_MEMORIES")
	if value == "" {
		return defaultCapacityMemories
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		t.Fatalf("DMEMD_CAPACITY_MEMORIES=%q is not a positive integer", value)
	}
	return parsed
}

func jobCounts(t *testing.T, store *sqlite.Store) map[string]int {
	t.Helper()
	rows, err := testutil.OpenInspector(t, store.Path()).Query(
		`SELECT status, count(*) FROM governance_jobs GROUP BY status`)
	if err != nil {
		t.Fatalf("count governance jobs: %v", err)
	}
	defer rows.Close()
	result := map[string]int{"queued": 0, "running": 0, "waiting": 0, "succeeded": 0, "failed": 0, "cancelled": 0}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			t.Fatalf("scan governance job count: %v", err)
		}
		result[status] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate governance job counts: %v", err)
	}
	return result
}

func rowCount(t *testing.T, store *sqlite.Store, table string) int {
	t.Helper()
	var count int
	if err := testutil.OpenInspector(t, store.Path()).QueryRow(
		`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func percentile(values []time.Duration, ratio float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * ratio)
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index].Round(time.Microsecond)
}

func databaseBytes(store *sqlite.Store) int64 {
	total := int64(0)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(store.Path() + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}
