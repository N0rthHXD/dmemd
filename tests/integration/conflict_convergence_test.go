package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestExplicitUpdatesConvergeWithoutDecisionModel(t *testing.T) {
	for _, test := range []struct {
		name, predicate, old, updated, quote string
		wantConflict                         bool
	}{
		{"dog food", "狗粮品牌", "狗粮使用A牌", "狗粮使用B牌", "狗粮换成B牌", false},
		{"glucose", "空腹血糖", "空腹血糖6.5", "空腹血糖5.6", "更正空腹血糖为5.6", true},
		{"rent", "租房预算", "租房预算3000元", "租房预算4000元", "租房预算改为4000元", false},
		{"CKA reschedule", "CKA考试日期", "CKA考试安排在下周", "CKA考试安排在下个月", "CKA考试改期到下个月", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newIngestStack(t)
			ctx := context.Background()
			group := map[string]any{"subject": "用户", "predicate": test.predicate, "cardinality": "single"}
			f.fake.EnqueueCandidates([]any{map[string]any{"kind": "fact", "content": test.old, "fact_group": group,
				"evidence": []any{map[string]any{"quote": test.old}}}})
			data, _ := json.Marshal(test.old)
			first := f.submit(t, string(data))
			f.processOne(t)
			oldID := f.job(t, first.JobID).MemoryIDs[0]
			f.advance(time.Minute)
			f.fake.EnqueueCandidates([]any{map[string]any{"kind": "fact", "content": test.updated, "fact_group": group,
				"evidence": []any{map[string]any{"quote": test.quote}}}})
			data, _ = json.Marshal(test.quote)
			second := f.submit(t, string(data))
			f.processOne(t)
			job := f.job(t, second.JobID)
			wantConflicts := 0
			if test.wantConflict {
				wantConflicts = 1
			}
			if job.Status != ingest.JobSucceeded || len(job.PendingMemoryIDs) != 0 || len(job.MemoryIDs) != 2 || len(job.ConflictIDs) != wantConflicts {
				t.Fatalf("convergence job=%+v", job)
			}
			if len(f.fake.Requests()) != 2 {
				t.Fatalf("explicit update called decision model: %d requests", len(f.fake.Requests()))
			}
			old, err := f.memories.Get(ctx, oldID)
			if err != nil {
				t.Fatalf("old=%+v err=%v", old, err)
			}
			if test.name == "glucose" || test.name == "CKA reschedule" {
				if old.AssertionStatus != memory.AssertionRejected {
					t.Fatalf("correction/reschedule must reject the old assertion: %+v", old)
				}
				if test.name == "glucose" && old.ValidTo != nil {
					t.Fatalf("correction invented a validity boundary: %+v", old)
				}
			} else if old.AssertionStatus != memory.AssertionAccepted || old.ValidTo == nil {
				t.Fatalf("state change must keep a closed historical segment: %+v", old)
			}
			if test.wantConflict {
				conflict, err := f.memories.GetConflict(ctx, job.ConflictIDs[0])
				if err != nil || conflict.Status != memory.ConflictResolved || len(conflict.Members) != 2 {
					t.Fatalf("conflict=%+v err=%v", conflict, err)
				}
				resolved := 0
				for _, event := range conflict.Events {
					if event.Action == "resolved" {
						resolved++
					}
				}
				if resolved != 1 {
					t.Fatalf("resolved audit count=%d", resolved)
				}
				for _, member := range conflict.Members {
					if member.MemoryID == oldID && (member.Version != 1 || member.CurrentVersion != 2) {
						t.Fatalf("detection version overwritten: %+v", member)
					}
				}
			}
			evolution, err := f.memories.Evolution(ctx, newIDFor(job.MemoryIDs, oldID), 10)
			if err != nil || len(evolution) != 1 || evolution[0].ToMemoryID != oldID {
				t.Fatalf("evolution audit=%+v err=%v", evolution, err)
			}
			result, err := f.search.Search(ctx, retrieval.Request{Query: test.predicate})
			if err != nil || len(result.Items) != 1 || result.Items[0].Memory.Content != test.updated || len(result.Conflicts) != 0 {
				t.Fatalf("recall=%+v err=%v", result, err)
			}
			relations, err := f.memories.Relations(ctx, result.Items[0].Memory.ID)
			if err != nil || len(relations) != 1 || relations[0].ToMemoryID != oldID || relations[0].Kind != memory.RelationSupersedes {
				t.Fatalf("relations=%+v err=%v", relations, err)
			}
		})
	}
}

func TestConflictExpansionKeepsCompleteUniqueMembersAndDetectionVersions(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()
	group := map[string]any{"subject": "用户", "predicate": "常住城市", "cardinality": "single"}
	f.fake.EnqueueCandidates([]any{map[string]any{"kind": "fact", "content": "用户常住上海", "fact_group": group}})
	first := f.submit(t, `"用户常住上海"`)
	f.processOne(t)
	oldID := f.job(t, first.JobID).MemoryIDs[0]
	var conflictID string
	for index, city := range []string{"北京", "广州"} {
		f.advance(time.Minute)
		f.fake.EnqueueCandidates([]any{map[string]any{"kind": "fact", "content": "用户常住" + city, "fact_group": group}})
		// Duplicate targets and a changed reason must still extend one conflict.
		reason := "contradictory_value"
		if index == 1 {
			reason = "uncertain_time"
		}
		f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"conflict","target_memory_ids":["` + oldID + `","` + oldID + `"],"reason_code":"` + reason + `"}]}`)
		data, _ := json.Marshal("用户常住" + city)
		receipt := f.submit(t, string(data))
		f.processOne(t)
		job := f.job(t, receipt.JobID)
		if len(job.ConflictIDs) != 1 {
			t.Fatalf("job=%+v", job)
		}
		if index == 0 {
			conflictID = job.ConflictIDs[0]
		} else if conflictID != job.ConflictIDs[0] {
			t.Fatalf("reason change split conflict: %+v", job)
		}
	}
	detail, err := f.memories.GetConflict(ctx, conflictID)
	if err != nil || len(detail.Members) != 3 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	seen := map[string]bool{}
	versions := []memory.ConflictMember{}
	for _, member := range detail.Members {
		if seen[member.MemoryID] {
			t.Fatalf("duplicate member=%+v", member)
		}
		seen[member.MemoryID] = true
		versions = append(versions, memory.ConflictMember{MemoryID: member.MemoryID, Version: member.CurrentVersion})
	}
	resolved, err := f.memories.Resolve(ctx, memory.ResolveInput{ConflictID: conflictID, Revision: detail.Revision,
		Action: memory.ResolutionChoose, MemberIDs: []string{oldID}, MemberVersions: versions})
	if err != nil || resolved.Status != memory.ConflictResolved || len(resolved.Members) != 3 {
		t.Fatalf("resolution=%+v err=%v", resolved, err)
	}
	for _, member := range resolved.Members {
		if member.Version != 1 {
			t.Fatalf("detection version changed: %+v", member)
		}
	}
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.UpsertConflictMember(ctx, conflictID, memory.ConflictMember{MemoryID: oldID, Version: 99})
	}); err == nil {
		t.Fatal("resolved conflict allowed membership mutation")
	}
}

func TestExplicitUpdateResolvesExistingPendingSet(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()
	group := map[string]any{"subject": "用户", "predicate": "狗粮品牌", "cardinality": "single"}
	f.fake.EnqueueCandidates([]any{map[string]any{"kind": "fact", "content": "狗粮使用A牌", "fact_group": group}})
	first := f.submit(t, `"狗粮使用A牌"`)
	f.processOne(t)
	oldID := f.job(t, first.JobID).MemoryIDs[0]
	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{"kind": "fact", "content": "狗粮使用B牌", "fact_group": group}})
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"conflict","target_memory_ids":["` + oldID + `"]}]}`)
	second := f.submit(t, `"狗粮使用B牌"`)
	f.processOne(t)
	previous := f.job(t, second.JobID)
	if len(previous.PendingMemoryIDs) != 1 || len(previous.ConflictIDs) != 1 {
		t.Fatalf("pending job=%+v", previous)
	}
	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{"kind": "fact", "content": "狗粮使用C牌", "fact_group": group,
		"evidence": []any{map[string]any{"quote": "狗粮换成C牌"}}}})
	third := f.submit(t, `"狗粮换成C牌"`)
	f.processOne(t)
	job := f.job(t, third.JobID)
	if job.Status != ingest.JobSucceeded || len(job.PendingMemoryIDs) != 0 || len(job.ConflictIDs) != 1 || job.ConflictIDs[0] != previous.ConflictIDs[0] {
		t.Fatalf("convergence=%+v", job)
	}
	conflict, err := f.memories.GetConflict(ctx, previous.ConflictIDs[0])
	if err != nil || conflict.Status != memory.ConflictResolved || len(conflict.Members) != 3 {
		t.Fatalf("resolved=%+v err=%v", conflict, err)
	}
	item, err := f.memories.Get(ctx, oldID)
	if err != nil || item.AssertionStatus != memory.AssertionAccepted || item.ValidTo == nil {
		t.Fatalf("changed predecessor=%+v err=%v", item, err)
	}
	pending, err := f.memories.Get(ctx, previous.PendingMemoryIDs[0])
	if err != nil || pending.AssertionStatus != memory.AssertionRejected {
		t.Fatalf("pending opponent=%+v err=%v", pending, err)
	}
	result, err := f.search.Search(ctx, retrieval.Request{Query: "狗粮"})
	if err != nil || len(result.Items) != 1 || result.Items[0].Memory.Content != "狗粮使用C牌" || len(result.Warnings) != 0 {
		t.Fatalf("recall=%+v err=%v", result, err)
	}
}

func newIDFor(ids []string, oldID string) string {
	for _, id := range ids {
		if id != oldID {
			return id
		}
	}
	return ""
}
