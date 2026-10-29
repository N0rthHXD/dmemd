package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestMatchRulesSameSourceSpanDuplicate(t *testing.T) {
	current := ruleObservation("new", "src", "project:dmemd", "项目使用 SQLite 保存记忆。")
	prior := ruleObservation("old", "src", "project:dmemd", "项目使用 SQLite 保存记忆。")
	target := memory.Memory{
		ID: "mem", ScopeKey: "project:dmemd", Content: "项目使用 SQLite 保存记忆。\n补充内容。",
		State: memory.StateActive, AssertionStatus: memory.AssertionAccepted, Version: 2,
	}
	decision := governance.MatchRules(governance.RuleInput{
		Observation: current,
		Evidence:    []source.Evidence{{SegmentID: "seg-0001"}},
		PriorObservations: []governance.RuleObservation{{
			Observation: prior,
			SegmentIDs:  []string{"seg-0001"},
			MemoryIDs:   []string{"mem"},
		}},
		MemoriesByID: map[string]memory.Memory{"mem": target},
	})
	if !decision.Matched || decision.Skip || decision.Kind != governance.RelationDuplicate ||
		decision.TargetID != "mem" || decision.Reason != governance.RuleReasonSameSourceSpan {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestMatchRulesSameBodyInScope(t *testing.T) {
	decision := governance.MatchRules(governance.RuleInput{
		Observation: ruleObservation("new", "src", "project:dmemd", "项目使用 SQLite 保存记忆。"),
		Candidates: []governance.RuleCandidate{{
			Ref: "existing_1",
			Memory: memory.Memory{
				ID: "mem", ScopeKey: "project:dmemd", Content: "项目使用 SQLite 保存记忆。",
				State: memory.StateActive, AssertionStatus: memory.AssertionAccepted, Version: 1,
			},
		}},
	})
	if !decision.Matched || decision.Kind != governance.RelationDuplicate ||
		decision.TargetID != "mem" || decision.Reason != governance.RuleReasonSameBody {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestMatchRulesExplicitScopeSeparation(t *testing.T) {
	decision := governance.MatchRules(governance.RuleInput{
		Observation: ruleObservation("new", "src", "project:a", "项目使用 SQLite。"),
		Candidates: []governance.RuleCandidate{{
			Ref: "existing_1",
			Memory: memory.Memory{
				ID: "mem", ScopeKey: "project:b", Content: "项目使用 PostgreSQL。",
				State: memory.StateActive, AssertionStatus: memory.AssertionAccepted, Version: 1,
			},
		}},
	})
	if !decision.Matched || decision.Kind != governance.RelationSeparate ||
		decision.Reason != governance.RuleReasonDisjointScope {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestMatchRulesInactiveTargetIsUnresolved(t *testing.T) {
	decision := governance.MatchRules(governance.RuleInput{
		Observation: ruleObservation("new", "src", "project:dmemd", "项目使用 SQLite 保存记忆。"),
		Candidates: []governance.RuleCandidate{{
			Ref: "existing_1",
			Memory: memory.Memory{
				ID: "mem", ScopeKey: "project:dmemd", Content: "项目使用 SQLite 保存记忆。",
				State: memory.StateDeleted, AssertionStatus: memory.AssertionAccepted, Version: 2,
			},
		}},
		MemoriesByID: map[string]memory.Memory{"mem": {
			ID: "mem", ScopeKey: "project:dmemd", Content: "项目使用 SQLite 保存记忆。",
			State: memory.StateDeleted, AssertionStatus: memory.AssertionAccepted, Version: 2,
		}},
	})
	if !decision.Matched || decision.Kind != governance.RelationUnresolved ||
		decision.Reason != governance.RuleReasonInactiveTarget {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestMatchRulesNoCandidatesIsIndependent(t *testing.T) {
	decision := governance.MatchRules(governance.RuleInput{
		Observation: ruleObservation("new", "src", "project:dmemd", "项目使用 SQLite。"),
	})
	if !decision.Matched || decision.Kind != governance.RelationIndependent ||
		decision.Reason != governance.RuleReasonNoCandidates {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestMatchRulesDefersAmbiguousBodyToModel(t *testing.T) {
	decision := governance.MatchRules(governance.RuleInput{
		Observation: ruleObservation("new", "src", "project:dmemd", "项目使用 SQLite 保存记忆。"),
		Evidence:    []source.Evidence{{SegmentID: "seg-0001"}},
		Candidates: []governance.RuleCandidate{{
			Ref: "existing_1",
			Memory: memory.Memory{
				ID: "mem", ScopeKey: "project:dmemd", Content: "项目使用 PostgreSQL 保存记忆。",
				State: memory.StateActive, AssertionStatus: memory.AssertionAccepted, Version: 1,
			},
		}},
	})
	if decision.Matched || decision.Reason != governance.RuleReasonModelRequired {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestObservationGovernanceDuplicatesIdenticalBodyWithoutModel(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		t.Fatal("relation model called for exact duplicate")
		return governance.RelationResult{}, nil
	})
	f := newHandlerFixture(t, provider)
	_, first := f.insertSourceWithObservation(t, "src-rule-1", "obs-rule-1",
		"项目使用 SQLite 保存记忆。", "project:dmemd")
	if job := f.process(t, first.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("first job = %+v", job)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 {
		t.Fatalf("memories = %+v", memories)
	}
	base := memories[0]

	_, second := f.insertSourceWithObservation(t, "src-rule-2", "obs-rule-2",
		"项目使用 SQLite 保存记忆。", "project:dmemd")
	if job := f.process(t, second.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("second job = %+v", job)
	}
	memories, err = f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].ID != base.ID || memories[0].Version != base.Version {
		t.Fatalf("memories after rule duplicate = %+v", memories)
	}
	refs, err := f.store.ListMemoryObservationRefs(context.Background(), base.ID, base.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0] != first.ID {
		t.Fatalf("duplicate observation refs = %+v", refs)
	}
	relations, err := f.relations.ListBetween(context.Background(), governance.EndpointObservation, second.ID,
		governance.EndpointMemory, base.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].Kind != governance.RelationDuplicate {
		t.Fatalf("duplicate relations = %+v", relations)
	}
}

func TestObservationGovernanceSameSourceSpanDuplicateAfterRevision(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		t.Fatal("relation model called for same-source span duplicate")
		return governance.RelationResult{}, nil
	})
	f := newHandlerFixture(t, provider)
	src := f.insertSourceOnly(t, "src-rule-span", "项目使用 SQLite 保存记忆。团队使用 Go 开发。", "project:dmemd")
	first := f.insertObservationFromSource(t, src, "obs-rule-span-1", "项目使用 SQLite 保存记忆。", "seg-0001")
	if job := f.process(t, first.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("first job = %+v", job)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 {
		t.Fatalf("memories = %+v", memories)
	}
	base := memories[0]
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := f.memories.Revise(ctx, tx, memory.ReviseInput{
			MemoryID:        base.ID,
			Kind:            base.Kind,
			Title:           base.Title,
			Content:         base.Content + "\n补充说明。",
			AssertionStatus: base.AssertionStatus,
			ScopeKey:        base.ScopeKey,
			Operation:       memory.OperationUpdate,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	revised, err := f.memories.Get(context.Background(), base.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revised.Version != base.Version+1 {
		t.Fatalf("revised = %+v", revised)
	}

	second := f.insertObservationFromSource(t, src, "obs-rule-span-2", "项目使用 SQLite 保存记忆。", "seg-0001")
	if job := f.process(t, second.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("second job = %+v", job)
	}
	current, err := f.memories.Get(context.Background(), base.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Content != revised.Content || current.Version != revised.Version {
		t.Fatalf("rule duplicate changed current memory: %+v", current)
	}
	refs, err := f.store.ListMemoryObservationRefs(context.Background(), current.ID, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != second.ID {
		t.Fatalf("current refs = %+v", refs)
	}
	relations, err := f.relations.ListBetween(context.Background(), governance.EndpointObservation, second.ID,
		governance.EndpointMemory, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].Kind != governance.RelationDuplicate {
		t.Fatalf("relations = %+v", relations)
	}
}

func TestObservationGovernanceIdempotentRetrySkipsModelAndMaterialization(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		t.Fatal("relation model called for idempotent retry")
		return governance.RelationResult{}, nil
	})
	f := newHandlerFixture(t, provider)
	_, item := f.insertSourceWithObservation(t, "src-idem", "obs-idem",
		"项目使用 SQLite 保存记忆。", "project:dmemd")
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("first job = %+v", job)
	}
	before, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job := f.reprocess(t, item.ID)
	if job.Status != task.StatusSucceeded {
		t.Fatalf("retry job = %+v", job)
	}
	after, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || len(after) != 1 || before[0].ID != after[0].ID || before[0].Version != after[0].Version {
		t.Fatalf("memories before=%+v after=%+v", before, after)
	}
}

func TestObservationGovernanceDuplicatePendingObservationDoesNotCreateMemory(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		t.Fatal("relation model called for exact pending duplicate")
		return governance.RelationResult{}, nil
	})
	f := newHandlerFixture(t, provider)
	candidate := f.createCandidate(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
	src := f.insertSourceOnly(t, "src-pending", "项目使用 SQLite 保存记忆。", "project:dmemd")
	item := f.insertPendingObservation(t, src, "obs-pending", "项目使用 SQLite 保存记忆。")
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	current, err := f.memories.Get(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Content != candidate.Content || current.Version != candidate.Version {
		t.Fatalf("duplicate pending observation changed memory = %+v", current)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 {
		t.Fatalf("pending duplicate created memories = %+v", memories)
	}
	refs, err := f.store.ListMemoryObservationRefs(context.Background(), candidate.ID, candidate.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != item.ID {
		t.Fatalf("pending duplicate refs = %+v", refs)
	}
}

func TestObservationGovernanceDiscardsStaleRevision(t *testing.T) {
	f := newHandlerFixture(t, nil)
	_, item := f.insertSourceWithObservation(t, "src-stale", "obs-stale",
		"项目使用 SQLite 保存记忆。", "project:dmemd")
	ctx := context.Background()
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:      task.KindGovernObservation,
			TargetKey: task.ObservationKey(item.ID),
			InputHash: "initial",
			Now:       f.now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var claimed task.Job
	var found bool
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		claimed, found, err = tx.ClaimNextGovernanceJob(ctx, f.now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no governance job claimed")
	}
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:      task.KindGovernObservation,
			TargetKey: task.ObservationKey(item.ID),
			InputHash: "newer",
			Now:       f.now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.handlers.Process(ctx, claimed); !errors.Is(err, governance.ErrRevisionChanged) {
		t.Fatalf("stale revision error = %v", err)
	}
	memories, err := f.memories.List(ctx, memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 0 {
		t.Fatalf("stale revision materialized memories = %+v", memories)
	}
}

func ruleObservation(id, sourceID, scopeKey, content string) observation.Observation {
	return observation.Observation{
		ID:       id,
		SourceID: sourceID,
		ScopeKey: scopeKey,
		Kind:     observation.KindFact,
		Content:  content,
		Status:   observation.StatusObserved,
	}
}

func (f *handlerFixture) insertSourceWithObservation(t *testing.T, sourceID, observationID, content, scopeKey string) (source.Source, observation.Observation) {
	t.Helper()
	src := f.insertSourceOnly(t, sourceID, content, scopeKey)
	return src, f.insertObservationFromSource(t, src, observationID, content, "seg-0001")
}

func (f *handlerFixture) insertSourceOnly(t *testing.T, sourceID, content, scopeKey string) source.Source {
	t.Helper()
	src := source.Source{
		ID:        sourceID,
		ScopeKey:  scopeKey,
		Content:   []byte(`{"messages":[{"id":"m1","role":"user","content":"` + content + `"}]}`),
		CreatedAt: f.now,
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertSource(ctx, src)
	}); err != nil {
		t.Fatal(err)
	}
	return src
}

func (f *handlerFixture) insertObservationFromSource(t *testing.T, src source.Source,
	observationID, content string, segmentIDs ...string) observation.Observation {
	t.Helper()
	view := source.BuildView(src.Content)
	segments, err := source.ResolveSegments(view, segmentIDs)
	if err != nil {
		t.Fatal(err)
	}
	evidence := source.EvidenceForResolvedSegments(src, segments, segmentIDs, f.now)
	item := observation.Observation{
		ID:                observationID,
		SourceID:          src.ID,
		ScopeKey:          src.ScopeKey,
		Kind:              observation.KindFact,
		Content:           content,
		ContentMode:       observation.ContentExtractive,
		OriginRole:        "user",
		Status:            observation.StatusObserved,
		ExtractionVersion: "extraction-v6",
		CreatedAt:         f.now,
		UpdatedAt:         f.now,
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertObservation(ctx, item); err != nil {
			return err
		}
		for _, entry := range evidence {
			if err := tx.InsertEvidence(ctx, entry); err != nil {
				return err
			}
			if err := tx.InsertObservationEvidence(ctx, item.ID, entry.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return item
}

func (f *handlerFixture) insertPendingObservation(t *testing.T, src source.Source, observationID, content string) observation.Observation {
	t.Helper()
	item := observation.Observation{
		ID:                observationID,
		SourceID:          src.ID,
		ScopeKey:          src.ScopeKey,
		Kind:              observation.KindFact,
		Content:           content,
		ContentMode:       observation.ContentNormalized,
		Status:            observation.StatusPending,
		ExtractionVersion: "extraction-v6",
		CreatedAt:         f.now,
		UpdatedAt:         f.now,
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertObservation(ctx, item)
	}); err != nil {
		t.Fatal(err)
	}
	return item
}

func (f *handlerFixture) reprocess(t *testing.T, observationID string) task.Job {
	t.Helper()
	ctx := context.Background()
	f.cancelClassifyJobs(t)
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:      task.KindGovernObservation,
			TargetKey: task.ObservationKey(observationID),
			InputHash: "reprocess:" + observationID,
			Now:       f.now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var claimed task.Job
	var found bool
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		claimed, found, err = tx.ClaimNextGovernanceJob(ctx, f.now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no governance job claimed for reprocess")
	}
	if err := f.handlers.Process(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	finished, err := f.store.GetGovernanceJob(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	return finished
}
