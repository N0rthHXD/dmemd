//go:build legacy

package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestRepeatedIdenticalFactMergesEvidenceWithoutDecisionModel(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()
	group := map[string]any{"subject": "用户", "predicate": "回答语言", "cardinality": "single"}

	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "preference", "content": "用户希望默认使用英文回答。", "fact_group": group,
		"evidence": []any{map[string]any{"quote": "默认使用英文回答"}},
	}})
	first := f.submit(t, `"默认使用英文回答"`)
	f.processOne(t)
	firstID := f.job(t, first.JobID).MemoryIDs[0]

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "preference", "content": "用户希望默认使用英文回答。", "fact_group": group,
		"evidence": []any{map[string]any{"quote": "再次确认默认英文回答"}},
	}})
	second := f.submit(t, `"再次确认默认英文回答"`)
	f.processOne(t)
	job := f.job(t, second.JobID)
	if job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 1 || job.MemoryIDs[0] != firstID ||
		len(job.ConflictIDs) != 0 || len(job.PendingMemoryIDs) != 0 {
		t.Fatalf("job=%+v", job)
	}
	item, err := f.memories.Get(ctx, firstID)
	if err != nil || item.Version != 2 || item.AssertionStatus != memory.AssertionAccepted {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	evidence, err := f.memories.GetEvidence(ctx, firstID, 0)
	if err != nil || len(evidence) != 2 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	if len(f.fake.Requests()) != 2 {
		t.Fatalf("repeated fact called decision model: %d requests", len(f.fake.Requests()))
	}
}

func TestIndependentObservationsDoNotCreateConflict(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()
	group := map[string]any{"subject": "用户", "predicate": "空腹血糖", "cardinality": "single"}

	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "2026-10-01 空腹血糖 6.5", "fact_group": group,
		"observed_at": "2026-10-01T08:00:00Z", "observed_at_basis": "explicit",
		"evidence": []any{map[string]any{"quote": "2026-10-01 空腹血糖 6.5"}},
	}})
	first := f.submit(t, `"2026-10-01 空腹血糖 6.5"`)
	f.processOne(t)
	firstID := f.job(t, first.JobID).MemoryIDs[0]

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "2026-10-08 空腹血糖 5.4", "fact_group": group,
		"observed_at": "2026-10-08T08:00:00Z", "observed_at_basis": "explicit",
		"evidence": []any{map[string]any{"quote": "2026-10-08 空腹血糖 5.4"}},
	}})
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"add"}]}`)
	second := f.submit(t, `"2026-10-08 空腹血糖 5.4"`)
	f.processOne(t)
	job := f.job(t, second.JobID)
	if job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 1 ||
		len(job.ConflictIDs) != 0 || len(job.PendingMemoryIDs) != 0 {
		t.Fatalf("job=%+v", job)
	}
	secondID := job.MemoryIDs[0]
	for _, id := range []string{firstID, secondID} {
		item, err := f.memories.Get(ctx, id)
		if err != nil || item.AssertionStatus != memory.AssertionAccepted {
			t.Fatalf("observation %s = %+v err=%v", id, item, err)
		}
	}
	firstItem, _ := f.memories.Get(ctx, firstID)
	secondItem, _ := f.memories.Get(ctx, secondID)
	if firstItem.ObservedAt.Equal(secondItem.ObservedAt) {
		t.Fatalf("independent observation times collapsed: %v", firstItem.ObservedAt)
	}
}

func TestConvergenceReasonIsReportedWithoutSourceText(t *testing.T) {
	f := newIngestStack(t)
	group := map[string]any{"subject": "用户", "predicate": "常住城市", "cardinality": "single"}
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "用户常住上海", "fact_group": group,
		"evidence": []any{map[string]any{"quote": "用户常住上海"}},
	}})
	first := f.submit(t, `"用户常住上海"`)
	f.processOne(t)
	firstID := f.job(t, first.JobID).MemoryIDs[0]

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "用户常住北京", "fact_group": group,
		"evidence": []any{map[string]any{"quote": "用户常住北京"}},
	}})
	decision, _ := json.Marshal(map[string]any{"decisions": []any{map[string]any{
		"candidate_id": "c1", "action": "add",
	}}})
	f.fake.EnqueueContent(string(decision))
	second := f.submit(t, `"用户常住北京"`)
	f.processOne(t)
	job := f.job(t, second.JobID)
	if job.Status != ingest.JobSucceeded || len(job.Convergence) != 1 {
		t.Fatalf("job=%+v", job)
	}
	if job.Convergence[0].Code != ingest.ConvergenceMissingUpdateEvidence ||
		job.Convergence[0].CandidateID != "c1" || job.Convergence[0].GroupID == "" {
		t.Fatalf("diagnostic=%+v", job.Convergence[0])
	}
	if string(job.Convergence[0].Code) == "用户常住北京" || string(job.Convergence[0].CandidateID) == "用户常住北京" {
		t.Fatal("diagnostic contains source text")
	}
	_ = firstID
}

func TestFactIdentityDoesNotCrossExplicitScopes(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()
	group := map[string]any{"subject": "用户", "predicate": "常住城市", "cardinality": "single"}
	submitScope := func(content, scope string) ingest.Receipt {
		t.Helper()
		receipt, err := f.service.Submit(ctx, ingest.Submission{
			AppID: "desktop-assistant", ScopeKey: scope,
			Content: json.RawMessage(`"` + content + `"`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	}

	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "用户常住上海", "fact_group": group,
		"evidence": []any{map[string]any{"quote": "用户常住上海"}},
	}})
	first := submitScope("用户常住上海", "project:a")
	f.processOne(t)
	firstJob := f.job(t, first.JobID)
	firstItem, err := f.memories.Get(ctx, firstJob.MemoryIDs[0])
	if err != nil {
		t.Fatal(err)
	}

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "用户常住北京", "fact_group": group,
		"evidence": []any{map[string]any{"quote": "用户常住北京"}},
	}})
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"add"}]}`)
	second := submitScope("用户常住北京", "project:b")
	f.processOne(t)
	job := f.job(t, second.JobID)
	if job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 1 ||
		len(job.ConflictIDs) != 0 || len(job.PendingMemoryIDs) != 0 {
		t.Fatalf("cross-scope job=%+v", job)
	}
	secondItem, err := f.memories.Get(ctx, job.MemoryIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if secondItem.ScopeKey != "project:b" || secondItem.FactGroupID == firstItem.FactGroupID {
		t.Fatalf("cross-scope identity merged: first=%+v second=%+v", firstItem, secondItem)
	}
}

func TestSameSourceEvolutionChainUsesEvidenceOrder(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()
	group := map[string]any{"subject": "用户", "predicate": "狗粮品牌", "cardinality": "single"}

	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "狗粮使用A牌", "fact_group": group,
		"evidence": []any{map[string]any{"quote": "狗粮使用A牌"}},
	}})
	first := f.submit(t, `"狗粮使用A牌"`)
	f.processOne(t)
	oldID := f.job(t, first.JobID).MemoryIDs[0]

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{
		map[string]any{"kind": "fact", "content": "狗粮使用B牌", "fact_group": group,
			"evidence": []any{map[string]any{"quote": "狗粮换成B牌"}}},
		map[string]any{"kind": "fact", "content": "狗粮使用C牌", "fact_group": group,
			"evidence": []any{map[string]any{"quote": "后来狗粮换成C牌"}}},
	})
	second := f.submit(t, `"狗粮换成B牌。后来狗粮换成C牌。"`)
	f.processOne(t)
	job := f.job(t, second.JobID)
	if job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 3 ||
		len(job.PendingMemoryIDs) != 0 || len(job.ConflictIDs) != 0 {
		t.Fatalf("chain job=%+v", job)
	}
	old, err := f.memories.Get(ctx, oldID)
	if err != nil || old.AssertionStatus != memory.AssertionAccepted || old.ValidTo == nil {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	var current string
	for _, id := range job.MemoryIDs {
		item, err := f.memories.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.AssertionStatus == memory.AssertionAccepted && item.ValidTo == nil {
			current = item.Content
		}
	}
	if current != "狗粮使用C牌" {
		t.Fatalf("current=%q ids=%v", current, job.MemoryIDs)
	}
	if len(f.fake.Requests()) != 2 {
		t.Fatalf("same-source chain called decision model: %d", len(f.fake.Requests()))
	}
}

func TestEventRescheduleKeepsOldPlanAsHistory(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()
	group := map[string]any{"subject": "用户", "predicate": "CKA考试日期", "cardinality": "single"}

	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "event", "phase": "scheduled", "content": "CKA考试安排在2026-11-01",
		"event_time": "2026-11-01T02:00:00Z", "event_time_basis": "explicit",
		"fact_group": group,
		"evidence":   []any{map[string]any{"quote": "CKA考试安排在11月1日"}},
	}})
	first := f.submit(t, `"CKA考试安排在11月1日"`)
	f.processOne(t)
	oldID := f.job(t, first.JobID).MemoryIDs[0]

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "event", "phase": "scheduled", "content": "CKA考试安排在2026-12-01",
		"event_time": "2026-12-01T02:00:00Z", "event_time_basis": "explicit",
		"fact_group": group,
		"evidence":   []any{map[string]any{"quote": "CKA考试改期到12月1日"}},
	}})
	second := f.submit(t, `"CKA考试改期到12月1日"`)
	f.processOne(t)
	job := f.job(t, second.JobID)
	if job.Status != ingest.JobSucceeded || len(job.MemoryIDs) != 2 ||
		len(job.ConflictIDs) != 0 || len(job.PendingMemoryIDs) != 0 {
		t.Fatalf("reschedule job=%+v", job)
	}
	old, err := f.memories.Get(ctx, oldID)
	if err != nil || old.AssertionStatus != memory.AssertionRejected {
		t.Fatalf("old plan=%+v err=%v", old, err)
	}
	newID := ""
	for _, id := range job.MemoryIDs {
		if id != oldID {
			newID = id
		}
	}
	current, err := f.memories.Get(ctx, newID)
	if err != nil || current.AssertionStatus != memory.AssertionAccepted || current.Phase != memory.PhaseScheduled {
		t.Fatalf("current plan=%+v err=%v", current, err)
	}
}
