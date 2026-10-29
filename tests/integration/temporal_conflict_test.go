//go:build legacy

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestTemporalChangeAndAsOfRecall(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()

	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "用户常住上海。",
		"fact_group": map[string]any{
			"subject": "用户", "predicate": "常住城市", "cardinality": "single",
		},
		"evidence": []any{map[string]any{"message_id": "m1", "quote": "用户常住上海"}},
	}})
	first := f.submit(t, `{"messages":[{"id":"m1","content":"用户常住上海。"}]}`)
	if !f.processOne(t) {
		t.Fatal("first job not processed")
	}
	firstJob := f.job(t, first.JobID)
	if firstJob.Status != ingest.JobSucceeded || len(firstJob.MemoryIDs) != 1 {
		t.Fatalf("first job = %+v", firstJob)
	}
	oldID := firstJob.MemoryIDs[0]
	oldMemory, err := f.memories.Get(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if oldMemory.FactGroupID == "" || oldMemory.ValidFrom != nil || oldMemory.ValidTo != nil ||
		oldMemory.TemporalStatus() != "unknown" {
		t.Fatalf("initial memory = %+v", oldMemory)
	}
	groupBefore, err := f.store.GetFactGroup(ctx, oldMemory.FactGroupID)
	if err != nil {
		t.Fatal(err)
	}

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "用户常住北京。",
		"valid_from": "2026-06-15T02:00:00Z", "valid_from_basis": "inferred",
		"fact_group": map[string]any{
			"subject": "用户", "predicate": "常住城市", "cardinality": "single",
		},
		"evidence": []any{map[string]any{"message_id": "m2", "quote": "搬到北京"}},
	}})
	second := f.submit(t, `{"messages":[{"id":"m2","content":"用户 常住城市 搬到北京。"}]}`)
	if !f.processOne(t) {
		t.Fatal("second job not processed")
	}
	secondJob := f.job(t, second.JobID)
	if secondJob.Status != ingest.JobSucceeded || len(secondJob.MemoryIDs) != 2 {
		t.Fatalf("second job = %+v", secondJob)
	}
	transition := time.Date(2026, 6, 15, 2, 0, 0, 0, time.UTC)
	closed, err := f.memories.Get(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.ValidTo == nil || !closed.ValidTo.Equal(transition) || closed.ValidToBasis != memory.BasisInferred {
		t.Fatalf("closed interval = %+v", closed)
	}
	newID := ""
	for _, id := range secondJob.MemoryIDs {
		if id != oldID {
			newID = id
		}
	}
	if newID == "" {
		t.Fatal("change did not create a successor")
	}
	successor, err := f.memories.Get(ctx, newID)
	if err != nil {
		t.Fatal(err)
	}
	if successor.ValidFrom == nil || !successor.ValidFrom.Equal(transition) ||
		successor.FactGroupID != closed.FactGroupID {
		t.Fatalf("successor = %+v", successor)
	}
	relations, err := f.memories.Relations(ctx, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].Kind != memory.RelationSupersedes ||
		relations[0].ToMemoryID != oldID || relations[0].FromVersionChanged {
		t.Fatalf("relations = %+v", relations)
	}
	groupAfter, err := f.store.GetFactGroup(ctx, closed.FactGroupID)
	if err != nil {
		t.Fatal(err)
	}
	if groupAfter.Generation <= groupBefore.Generation {
		t.Fatalf("fact group generation did not advance: before=%d after=%d", groupBefore.Generation, groupAfter.Generation)
	}

	historical, err := f.search.Search(ctx, retrieval.Request{
		Query: "常住城市", Filters: retrieval.Filters{AsOf: timePtr(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(historical.Items) != 1 || historical.Items[0].Memory.ID != oldID {
		t.Fatalf("historical recall = %+v", historical.Items)
	}
	current, err := f.search.Search(ctx, retrieval.Request{
		Query: "常住城市", Filters: retrieval.Filters{AsOf: timePtr(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Items) != 1 || current.Items[0].Memory.ID != newID {
		t.Fatalf("current recall = %+v", current.Items)
	}
	if current.Items[0].Memory.TemporalStatus() != memory.TemporalUnknown {
		t.Fatalf("successor temporal status = %q", current.Items[0].Memory.TemporalStatus())
	}
}

func TestPendingConflictDiscoveryAndResolution(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()

	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "用户常住上海。",
		"fact_group": map[string]any{
			"subject": "用户", "predicate": "常住城市", "cardinality": "single",
		},
		"evidence": []any{map[string]any{"message_id": "m1", "quote": "用户常住上海"}},
	}})
	first := f.submit(t, `{"messages":[{"id":"m1","content":"用户常住上海。"}]}`)
	if !f.processOne(t) {
		t.Fatal("first job not processed")
	}
	firstJob := f.job(t, first.JobID)
	acceptedID := firstJob.MemoryIDs[0]

	f.advance(time.Minute)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "fact", "content": "用户常住北京。",
		"fact_group": map[string]any{
			"subject": "用户", "predicate": "常住城市", "cardinality": "single",
		},
		"evidence": []any{map[string]any{"message_id": "m2", "quote": "用户 常住城市 北京。"}},
	}})
	f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"conflict","target_memory_ids":["` + acceptedID + `"],"content":"用户常住北京。","reason_code":"contradictory_value","evidence_refs":[{"candidate_id":"c1","evidence_index":0}]}]}`)
	second := f.submit(t, `{"messages":[{"id":"m2","content":"用户 常住城市 北京。"}]}`)
	if !f.processOne(t) {
		t.Fatal("second job not processed")
	}
	secondJob := f.job(t, second.JobID)
	if secondJob.Status != ingest.JobSucceeded || len(secondJob.ConflictIDs) != 1 || len(secondJob.PendingMemoryIDs) != 1 {
		t.Fatalf("second job = %+v", secondJob)
	}
	conflictID := secondJob.ConflictIDs[0]
	reviewJob, err := f.store.GetGovernanceJobByTarget(ctx, task.KindReviewConflict, task.ConflictKey(conflictID))
	if err != nil {
		t.Fatalf("review job missing after conflict detection: %v", err)
	}
	if reviewJob.Status != task.StatusQueued && reviewJob.Status != task.StatusSucceeded {
		t.Fatalf("review job after conflict detection = %+v", reviewJob)
	}
	pendingID := secondJob.PendingMemoryIDs[0]

	conflicts, err := f.memories.ListConflicts(ctx, memory.ConflictListOptions{Status: memory.ConflictOpen, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].ID != conflictID {
		t.Fatalf("open conflicts = %+v", conflicts)
	}
	detail, err := f.memories.GetConflict(ctx, conflictID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Members) != 2 || detail.Revision != 1 {
		t.Fatalf("conflict detail = %+v", detail)
	}

	normal, err := f.search.Search(ctx, retrieval.Request{Query: "常住城市"})
	if err != nil {
		t.Fatal(err)
	}
	if len(normal.Items) != 2 || normal.Items[0].Memory.ID != acceptedID || normal.Items[1].Memory.ID != pendingID ||
		normal.Items[1].Memory.AssertionStatus != memory.AssertionPending || len(normal.Items[1].ConflictRefs) != 1 {
		t.Fatalf("ordinary recall must rank accepted before labelled pending: %+v", normal.Items)
	}
	if len(normal.Items[0].ConflictRefs) != 1 || normal.Items[0].ConflictRefs[0].ID != conflictID {
		t.Fatalf("item conflict refs = %+v", normal.Items[0].ConflictRefs)
	}
	if len(normal.Conflicts) != 1 || !normal.Conflicts[0].HasAccepted || normal.Conflicts[0].MemberIDs[0] == "" {
		t.Fatalf("conflict summary = %+v", normal.Conflicts)
	}
	if len(normal.Warnings) != 1 || normal.Warnings[0] != retrieval.WarningFactConflict {
		t.Fatalf("warnings = %+v", normal.Warnings)
	}
	for _, mode := range []retrieval.Mode{retrieval.ModeKeyword, retrieval.ModeHybrid} {
		acceptedFirst, err := f.search.Search(ctx, retrieval.Request{Query: "常住城市 北京", Mode: mode, TopK: 1})
		if err != nil || len(acceptedFirst.Items) != 1 || acceptedFirst.Items[0].Memory.ID != acceptedID {
			t.Fatalf("pending displaced accepted under Top-K mode=%s result=%+v err=%v", mode, acceptedFirst, err)
		}
		pendingOnly, err := f.search.Search(ctx, retrieval.Request{Query: "什么 北京 未命中的词", Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		if len(pendingOnly.Items) != 2 || pendingOnly.Items[0].Memory.ID != pendingID ||
			pendingOnly.Items[1].Memory.ID != acceptedID || pendingOnly.Items[1].Related == nil ||
			!contains(pendingOnly.Items[1].Related.ReasonCodes, retrieval.RelatedConflictMember) ||
			len(pendingOnly.Conflicts) != 1 || pendingOnly.Conflicts[0].ID != conflictID ||
			!contains(pendingOnly.Warnings, retrieval.WarningFactConflict) {
			t.Fatalf("pending-only any conflict discovery mode=%s = %+v", mode, pendingOnly)
		}
	}

	if _, err := f.memories.Resolve(ctx, memory.ResolveInput{
		ConflictID: conflictID, Revision: detail.Revision + 1, Action: memory.ResolutionRejectAll,
		MemberVersions: []memory.ConflictMember{
			{MemoryID: acceptedID, Version: conflictMemberVersion(detail, acceptedID)},
			{MemoryID: pendingID, Version: conflictMemberVersion(detail, pendingID)},
		},
	}); !errors.Is(err, memory.ErrConflictStale) {
		t.Fatalf("stale revision error = %v", err)
	}
	if _, err := f.memories.Resolve(ctx, memory.ResolveInput{
		ConflictID: conflictID, Revision: detail.Revision, Action: memory.ResolutionChoose,
		MemberIDs: []string{acceptedID},
	}); !errors.Is(err, memory.ErrInvalidResolution) {
		t.Fatalf("missing member versions error = %v", err)
	}

	conflict, err := f.memories.Resolve(ctx, memory.ResolveInput{
		ConflictID: conflictID,
		Revision:   detail.Revision,
		Action:     memory.ResolutionChoose,
		MemberIDs:  []string{acceptedID},
		MemberVersions: []memory.ConflictMember{
			{MemoryID: acceptedID, Version: conflictMemberVersion(detail, acceptedID)},
			{MemoryID: pendingID, Version: conflictMemberVersion(detail, pendingID)},
		},
		Reason: "用户确认常住上海",
	})
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Status != memory.ConflictResolved {
		t.Fatalf("resolved conflict = %+v", conflict)
	}
	rejected, err := f.memories.Get(ctx, pendingID)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.AssertionStatus != memory.AssertionRejected {
		t.Fatalf("pending opponent = %+v", rejected)
	}
	after, err := f.search.Search(ctx, retrieval.Request{Query: "常住城市"})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Conflicts) != 0 || len(after.Warnings) != 0 {
		t.Fatalf("resolved conflict still warned: %+v", after)
	}
	again, err := f.memories.Resolve(ctx, memory.ResolveInput{
		ConflictID: conflictID,
		Revision:   detail.Revision,
		Action:     memory.ResolutionChoose,
		MemberIDs:  []string{acceptedID},
		MemberVersions: []memory.ConflictMember{
			{MemoryID: acceptedID, Version: conflictMemberVersion(detail, acceptedID)},
			{MemoryID: pendingID, Version: conflictMemberVersion(detail, pendingID)},
		},
		Reason: "用户确认常住上海",
	})
	if err != nil || again.Status != memory.ConflictResolved {
		t.Fatalf("repeated resolution = %+v err=%v", again, err)
	}
}

func TestRestoreRejectsUnmarkedConflict(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()

	group, err := memory.NewFactGroup(memory.FactGroupInput{
		Subject: "用户", Predicate: "常住城市", Cardinality: memory.CardinalitySingle,
	}, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertFactGroup(ctx, group)
	}); err != nil {
		t.Fatal(err)
	}
	create := func(content string) memory.Memory {
		var created memory.Memory
		if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			var err error
			created, err = f.memories.Create(ctx, tx, memory.CreateInput{
				Kind: memory.KindFact, Title: content, Content: content,
				ObservedAt: f.clock, FactGroupID: group.ID,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return created
	}
	first := create("用户常住上海。")
	second := create("用户常住北京。")
	archived, err := f.memories.UpdateState(ctx, first.ID, memory.StateArchived)
	if err != nil {
		t.Fatal(err)
	}
	_ = second
	if _, err := f.memories.Restore(ctx, memory.RestoreInput{MemoryID: first.ID, Version: 1}); !errors.Is(err, memory.ErrRestoreConflict) {
		t.Fatalf("restore error = %v, want ErrRestoreConflict (archived=%+v)", err, archived)
	}
}

func timePtr(value time.Time) *time.Time { return &value }

func conflictMemberVersion(conflict memory.Conflict, memoryID string) int {
	for _, member := range conflict.Members {
		if member.MemoryID == memoryID {
			return member.Version
		}
	}
	return 0
}

func TestPendingOnlyConflictDiscovery(t *testing.T) {
	f := newIngestStack(t)
	ctx := context.Background()
	group, err := memory.NewFactGroup(memory.FactGroupInput{
		Subject: "用户", Predicate: "常住城市", Cardinality: memory.CardinalitySingle,
	}, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertFactGroup(ctx, group)
	}); err != nil {
		t.Fatal(err)
	}
	createPending := func(content string) memory.Memory {
		var created memory.Memory
		if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			var err error
			created, err = f.memories.Create(ctx, tx, memory.CreateInput{
				Kind: memory.KindFact, Title: content, Content: content, ObservedAt: f.clock,
				FactGroupID: group.ID, AssertionStatus: memory.AssertionPending,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return created
	}
	first := createPending("用户常住上海。")
	second := createPending("用户常住北京。")
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertConflict(ctx, memory.Conflict{
			ID: "conflict-pending-only", FactGroupID: group.ID,
			Status: memory.ConflictOpen, ReasonCode: memory.ConflictContradictoryValue,
			Revision: 1, CreatedAt: f.clock, UpdatedAt: f.clock,
			Members: []memory.ConflictMember{
				{MemoryID: first.ID, Version: first.Version},
				{MemoryID: second.ID, Version: second.Version},
			},
		})
	}); err != nil {
		t.Fatal(err)
	}
	result, err := f.search.Search(ctx, retrieval.Request{Query: "常住城市"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].Memory.AssertionStatus != memory.AssertionPending || result.Items[1].Memory.AssertionStatus != memory.AssertionPending {
		t.Fatalf("pending-only recall must expose labelled candidates: %+v", result.Items)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].ID != "conflict-pending-only" || result.Conflicts[0].HasAccepted {
		t.Fatalf("pending-only conflicts = %+v", result.Conflicts)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != retrieval.WarningFactConflict {
		t.Fatalf("pending-only warnings = %+v", result.Warnings)
	}
}
