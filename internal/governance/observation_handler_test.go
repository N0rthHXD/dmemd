package governance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type relationFunc func(context.Context, governance.RelationRequest) (governance.RelationResult, error)

func (f relationFunc) Relate(ctx context.Context, request governance.RelationRequest) (governance.RelationResult, error) {
	return f(ctx, request)
}

type handlerFixture struct {
	store     *sqlite.Store
	memories  *memory.Service
	now       time.Time
	clock     func() time.Time
	handlers  *governance.ObservationGovernanceHandler
	relations governance.RelationService
}

func newHandlerFixture(t *testing.T, provider governance.RelationProvider) *handlerFixture {
	t.Helper()
	store := testutil.NewStore(t)
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	memories := memory.NewService(store, sqlite.MemoryStore{Store: store}, testutil.DocumentBuilder{}, memory.WithClock(clock))
	materializer := governance.NewMaterializer(memories)
	handler := governance.NewObservationGovernanceHandler(
		store,
		sqlite.GovernanceStore{Store: store},
		materializer,
		provider,
		nil,
		governance.WithObservationGovernanceClock(clock),
	)
	return &handlerFixture{
		store: store, memories: memories, now: now, clock: clock, handlers: handler,
		relations: governance.NewRelationService(store),
	}
}

func (f *handlerFixture) insertSourceAndObservation(t *testing.T, content, scopeKey string) (source.Source, observation.Observation) {
	t.Helper()
	ctx := context.Background()
	src := source.Source{
		ID:        "src-" + scopeKey,
		ScopeKey:  scopeKey,
		Content:   []byte(`{"messages":[{"id":"m1","role":"user","content":"` + content + `"}]}`),
		CreatedAt: f.now,
	}
	view := source.BuildView(src.Content)
	segments, err := source.ResolveSegments(view, []string{"seg-0001"})
	if err != nil {
		t.Fatal(err)
	}
	evidence := source.EvidenceForResolvedSegments(src, segments, []string{"seg-0001"}, f.now)
	item := observation.Observation{
		ID:                "obs-" + scopeKey,
		SourceID:          src.ID,
		ScopeKey:          scopeKey,
		Kind:              observation.KindFact,
		Content:           content,
		ContentMode:       observation.ContentExtractive,
		OriginRole:        "user",
		Status:            observation.StatusObserved,
		ExtractionVersion: "extraction-v6",
		CreatedAt:         f.now,
		UpdatedAt:         f.now,
	}
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
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
	return src, item
}

func (f *handlerFixture) createCandidate(t *testing.T, content, scopeKey string) memory.Memory {
	t.Helper()
	ctx := context.Background()
	var created memory.Memory
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = f.memories.Create(ctx, tx, memory.CreateInput{
			Kind:     memory.KindFact,
			Title:    "候选记忆",
			Content:  content,
			ScopeKey: scopeKey,
		})
		if err != nil {
			return err
		}
		_, err = tx.CancelJob(ctx, task.KindClassifyObservation, task.MemoryKey(created.ID), f.now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return created
}

func (f *handlerFixture) cancelClassifyJobs(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	memories, err := f.memories.List(ctx, memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) == 0 {
		return
	}
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		for _, item := range memories {
			if _, err := tx.CancelJob(ctx, task.KindClassifyObservation, task.MemoryKey(item.ID), f.now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *handlerFixture) process(t *testing.T, observationID string) task.Job {
	t.Helper()
	ctx := context.Background()
	f.cancelClassifyJobs(t)
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:      task.KindGovernObservation,
			TargetKey: task.ObservationKey(observationID),
			InputHash: observationID,
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
	if claimed.Kind != task.KindGovernObservation {
		t.Fatalf("claimed kind = %s", claimed.Kind)
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

func TestObservationGovernanceCreatesIndependentMemory(t *testing.T) {
	f := newHandlerFixture(t, nil)
	_, item := f.insertSourceAndObservation(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
	job := f.process(t, item.ID)
	if job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 {
		t.Fatalf("memories = %+v", memories)
	}
	created := memories[0]
	if created.Content != item.Content || created.ScopeKey != item.ScopeKey ||
		created.AssertionStatus != memory.AssertionAccepted || created.Version != 1 {
		t.Fatalf("created memory = %+v", created)
	}
	refs, err := f.store.ListMemoryObservationRefs(context.Background(), created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != item.ID {
		t.Fatalf("observation refs = %+v", refs)
	}
	relations, err := f.relations.ListForEndpoint(context.Background(), governance.EndpointObservation, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 0 {
		t.Fatalf("independent relation rows = %+v", relations)
	}
}

func TestObservationGovernanceUsesDuplicateRelationWithoutNewVersion(t *testing.T) {
	var captured governance.RelationRequest
	provider := relationFunc(func(_ context.Context, request governance.RelationRequest) (governance.RelationResult, error) {
		captured = request
		return governance.RelationResult{Relations: []governance.RelationProposal{{
			FromRef: "new", ToRef: "existing_1", Kind: governance.RelationDuplicate,
		}}}, nil
	})
	f := newHandlerFixture(t, provider)
	candidate := f.createCandidate(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
	_, item := f.insertSourceAndObservation(t, "项目用 SQLite 持久化数据。", "project:dmemd")
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	if captured.NewObservation.Ref != "new" || len(captured.Existing) != 1 ||
		captured.Existing[0].Ref != "existing_1" || captured.Existing[0].Content == "" {
		t.Fatalf("relation request = %+v", captured)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].ID != candidate.ID || memories[0].Version != candidate.Version {
		t.Fatalf("memories after duplicate = %+v", memories)
	}
	refs, err := f.store.ListMemoryObservationRefs(context.Background(), candidate.ID, candidate.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != item.ID {
		t.Fatalf("duplicate observation refs = %+v", refs)
	}
	relations, err := f.relations.ListBetween(context.Background(), governance.EndpointObservation, item.ID,
		governance.EndpointMemory, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].Kind != governance.RelationDuplicate ||
		relations[0].ToVersion != candidate.Version {
		t.Fatalf("duplicate relations = %+v", relations)
	}
}

func TestObservationGovernanceEnrichesCurrentMemory(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		return governance.RelationResult{Relations: []governance.RelationProposal{{
			FromRef: "new", ToRef: "existing_1", Kind: governance.RelationEnriches,
		}}}, nil
	})
	f := newHandlerFixture(t, provider)
	candidate := f.createCandidate(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
	_, item := f.insertSourceAndObservation(t, "记忆写入使用短事务。", "project:dmemd")
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	updated, err := f.memories.Get(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != candidate.Version+1 ||
		updated.Content != candidate.Content+"\n"+item.Content {
		t.Fatalf("enriched memory = %+v", updated)
	}
	refs, err := f.store.ListMemoryObservationRefs(context.Background(), updated.ID, updated.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != item.ID {
		t.Fatalf("enriched observation refs = %+v", refs)
	}
}

func TestObservationGovernanceSupersedesKeepsOldMemory(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		return governance.RelationResult{Relations: []governance.RelationProposal{{
			FromRef: "new", ToRef: "existing_1", Kind: governance.RelationSupersedes,
		}}}, nil
	})
	f := newHandlerFixture(t, provider)
	candidate := f.createCandidate(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
	_, item := f.insertSourceAndObservation(t, "项目改用 PostgreSQL 保存记忆。", "project:dmemd")
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 2 {
		t.Fatalf("memories after supersede = %+v", memories)
	}
	old, err := f.memories.Get(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Content != candidate.Content || old.Version != candidate.Version {
		t.Fatalf("superseded memory changed = %+v", old)
	}
	var successor memory.Memory
	for _, entry := range memories {
		if entry.ID != candidate.ID {
			successor = entry
		}
	}
	if successor.ID == "" || successor.Content != item.Content ||
		successor.AssertionStatus != memory.AssertionAccepted {
		t.Fatalf("successor = %+v", successor)
	}
	relations, err := f.relations.ListBetween(context.Background(), governance.EndpointObservation, item.ID,
		governance.EndpointMemory, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].Kind != governance.RelationSupersedes {
		t.Fatalf("supersede relations = %+v", relations)
	}
}

func TestObservationGovernanceSupportsAttachesWithoutRevision(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		return governance.RelationResult{Relations: []governance.RelationProposal{{
			FromRef: "new", ToRef: "existing_1", Kind: governance.RelationSupports,
		}}}, nil
	})
	f := newHandlerFixture(t, provider)
	candidate := f.createCandidate(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
	_, item := f.insertSourceAndObservation(t, "SQLite 是持久化方案。", "project:dmemd")
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	current, err := f.memories.Get(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Content != candidate.Content || current.Version != candidate.Version {
		t.Fatalf("support changed memory = %+v", current)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 {
		t.Fatalf("memories after support = %+v", memories)
	}
	refs, err := f.store.ListMemoryObservationRefs(context.Background(), candidate.ID, candidate.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != item.ID {
		t.Fatalf("support refs = %+v", refs)
	}
	relations, err := f.relations.ListBetween(context.Background(), governance.EndpointObservation, item.ID,
		governance.EndpointMemory, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].Kind != governance.RelationSupports {
		t.Fatalf("support relations = %+v", relations)
	}
}

func TestObservationGovernanceContradictionStaysPending(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		return governance.RelationResult{Relations: []governance.RelationProposal{{
			FromRef: "new", ToRef: "existing_1", Kind: governance.RelationContradicts,
		}}}, nil
	})
	f := newHandlerFixture(t, provider)
	candidate := f.createCandidate(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
	_, item := f.insertSourceAndObservation(t, "项目不再使用 SQLite。", "project:dmemd")
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 2 {
		t.Fatalf("memories after contradiction = %+v", memories)
	}
	var pending memory.Memory
	for _, entry := range memories {
		if entry.ID != candidate.ID {
			pending = entry
		}
	}
	if pending.ID == "" || pending.AssertionStatus != memory.AssertionPending {
		t.Fatalf("contradicting memory = %+v", pending)
	}
	base, err := f.memories.Get(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if base.Content != candidate.Content || base.Version != candidate.Version ||
		base.AssertionStatus != memory.AssertionAccepted {
		t.Fatalf("contradiction overwrote current memory: %+v", base)
	}
	relations, err := f.relations.ListBetween(context.Background(), governance.EndpointObservation, item.ID,
		governance.EndpointMemory, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].Kind != governance.RelationContradicts {
		t.Fatalf("contradiction relations = %+v", relations)
	}
}

func TestObservationGovernanceUnknownReferenceBecomesPending(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		return governance.RelationResult{Relations: []governance.RelationProposal{{
			FromRef: "new", ToRef: "existing_99", Kind: governance.RelationSupersedes,
		}}}, nil
	})
	f := newHandlerFixture(t, provider)
	candidate := f.createCandidate(t, "旧事实。", "project:dmemd")
	_, item := f.insertSourceAndObservation(t, "新事实。", "project:dmemd")
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 2 {
		t.Fatalf("memories = %+v", memories)
	}
	for _, entry := range memories {
		if entry.ID == candidate.ID {
			continue
		}
		if entry.AssertionStatus != memory.AssertionPending {
			t.Fatalf("unknown reference created accepted memory: %+v", entry)
		}
	}
	relations, err := f.relations.ListForEndpoint(context.Background(), governance.EndpointObservation, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 0 {
		t.Fatalf("unknown reference persisted relation: %+v", relations)
	}
}

func TestObservationGovernanceCancelsRedactedSource(t *testing.T) {
	f := newHandlerFixture(t, nil)
	src, item := f.insertSourceAndObservation(t, "敏感内容。", "project:dmemd")
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.RedactSource(ctx, src.ID, f.now)
	}); err != nil {
		t.Fatal(err)
	}
	job := f.process(t, item.ID)
	if job.Status != task.StatusCancelled {
		t.Fatalf("job = %+v", job)
	}
	memories, err := f.memories.List(context.Background(), memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 0 {
		t.Fatalf("redacted source materialized memories = %+v", memories)
	}
}

func TestObservationGovernanceMissingObservationCancels(t *testing.T) {
	f := newHandlerFixture(t, nil)
	job := f.process(t, "missing-observation")
	if job.Status != task.StatusCancelled {
		t.Fatalf("job = %+v", job)
	}
}

func TestObservationGovernanceRelationRequestUsesAliasesOnly(t *testing.T) {
	var captured governance.RelationRequest
	provider := relationFunc(func(_ context.Context, request governance.RelationRequest) (governance.RelationResult, error) {
		captured = request
		return governance.RelationResult{Relations: []governance.RelationProposal{{
			FromRef: "new", ToRef: "existing_1", Kind: governance.RelationSupports,
		}}}, nil
	})
	f := newHandlerFixture(t, provider)
	candidate := f.createCandidate(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
	src, item := f.insertSourceAndObservation(t, "SQLite 是项目的持久化方案。", "project:dmemd")
	evidence, err := f.store.ListObservationEvidence(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	if captured.NewObservation.Ref != "new" ||
		captured.NewObservation.Type != string(governance.EndpointObservation) ||
		captured.NewObservation.Content != item.Content ||
		captured.NewObservation.EvidenceHint == "" {
		t.Fatalf("new observation request = %+v", captured.NewObservation)
	}
	if len(captured.Existing) != 1 ||
		captured.Existing[0].Ref != "existing_1" ||
		captured.Existing[0].Type != string(governance.EndpointMemory) ||
		captured.Existing[0].Content != candidate.Content {
		t.Fatalf("existing candidates = %+v", captured.Existing)
	}
	encoded, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{candidate.ID, item.ID, src.ID} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("relation request leaked %q: %s", forbidden, encoded)
		}
	}
	for _, entry := range evidence {
		if strings.Contains(string(encoded), entry.ID) {
			t.Fatalf("relation request leaked evidence id %q: %s", entry.ID, encoded)
		}
	}
}

func TestObservationGovernanceDeduplicatesRelationProposals(t *testing.T) {
	provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
		proposal := governance.RelationProposal{
			FromRef: "new", ToRef: "existing_1", Kind: governance.RelationDuplicate,
		}
		return governance.RelationResult{Relations: []governance.RelationProposal{proposal, proposal}}, nil
	})
	f := newHandlerFixture(t, provider)
	candidate := f.createCandidate(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
	_, item := f.insertSourceAndObservation(t, "项目用 SQLite 持久化数据。", "project:dmemd")
	if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	relations, err := f.relations.ListBetween(context.Background(), governance.EndpointObservation, item.ID,
		governance.EndpointMemory, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].Kind != governance.RelationDuplicate {
		t.Fatalf("duplicate proposals were not normalized: %+v", relations)
	}
}

func TestObservationGovernanceAcceptsTargetlessSemanticOutcomes(t *testing.T) {
	for _, kind := range []governance.RelationKind{
		governance.RelationIndependent,
		governance.RelationSeparate,
	} {
		t.Run(string(kind), func(t *testing.T) {
			provider := relationFunc(func(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
				return governance.RelationResult{Relations: []governance.RelationProposal{{
					FromRef: "new", Kind: kind,
				}}}, nil
			})
			f := newHandlerFixture(t, provider)
			candidate := f.createCandidate(t, "项目使用 SQLite 保存记忆。", "project:dmemd")
			_, item := f.insertSourceAndObservation(t, "记忆写入使用短事务。", "project:dmemd")
			if job := f.process(t, item.ID); job.Status != task.StatusSucceeded {
				t.Fatalf("job = %+v", job)
			}
			memories, err := f.memories.List(context.Background(), memory.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(memories) != 2 {
				t.Fatalf("memories = %+v", memories)
			}
			var created memory.Memory
			for _, entry := range memories {
				if entry.ID != candidate.ID {
					created = entry
				}
			}
			if created.ID == "" || created.Content != item.Content ||
				created.AssertionStatus != memory.AssertionAccepted {
				t.Fatalf("targetless %s result = %+v", kind, created)
			}
			relations, err := f.relations.ListForEndpoint(context.Background(), governance.EndpointObservation, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(relations) != 0 {
				t.Fatalf("targetless %s persisted a relation: %+v", kind, relations)
			}
		})
	}
}
