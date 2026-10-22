package governance_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func (f *governanceFixture) newGroup(t *testing.T) memory.FactGroup {
	t.Helper()
	group, err := memory.NewFactGroup(memory.FactGroupInput{
		Subject: "项目数据库", Predicate: "使用", Scope: "project:dmemd", Cardinality: memory.CardinalitySingle,
	}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertFactGroup(ctx, group)
	}); err != nil {
		t.Fatal(err)
	}
	return group
}

func (f *governanceFixture) openConflict(t *testing.T, group memory.FactGroup,
	members ...memory.Memory) memory.Conflict {
	t.Helper()
	versioned := make([]memory.ConflictMember, 0, len(members))
	for _, member := range members {
		versioned = append(versioned, memory.ConflictMember{
			MemoryID: member.ID, Version: member.Version, CurrentVersion: member.Version,
		})
	}
	conflict := memory.Conflict{
		ID:          idgen.NewUUID(),
		FactGroupID: group.ID,
		Status:      memory.ConflictOpen,
		ReasonCode:  memory.ConflictContradictoryValue,
		Revision:    1,
		CreatedAt:   f.now,
		UpdatedAt:   f.now,
		Members:     versioned,
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertConflict(ctx, conflict); err != nil {
			return err
		}
		return memory.QueueConflictReview(ctx, tx, conflict, 0, f.now)
	}); err != nil {
		t.Fatal(err)
	}
	return conflict
}

func (f *governanceFixture) cancelClassify(t *testing.T, ids ...string) {
	t.Helper()
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		for _, id := range ids {
			if _, err := tx.CancelJob(ctx, task.KindClassify, task.MemoryKey(id), f.now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func newReviewWorker(f *governanceFixture) *governance.Worker {
	review := governance.NewConflictHandler(
		sqlite.GovernanceStore{Store: f.store},
		f.memories,
		governance.WithConflictClock(func() time.Time { return f.now }),
	)
	return governance.NewWorker(
		sqlite.GovernanceStore{Store: f.store},
		map[task.Kind]governance.Handler{task.KindReviewConflict: review},
		governance.WithReconcileInterval(0),
		governance.WithRetryDelays(0, 0, 0),
		governance.WithPollInterval(time.Millisecond),
	)
}

func TestConflictReviewClosesStaleConflictWithoutModel(t *testing.T) {
	f := newGovernanceFixture(t)
	group := f.newGroup(t)
	first := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "数据库", Content: "项目使用 SQLite。",
		ScopeKey: "project:dmemd", FactGroupID: group.ID, ObservedAt: f.now,
	})
	second := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "数据库", Content: "项目使用 PostgreSQL。",
		ScopeKey: "project:dmemd", FactGroupID: group.ID, ObservedAt: f.now,
	})
	f.cancelClassify(t, first.ID, second.ID)
	conflict := f.openConflict(t, group, first, second)

	// Archiving one side leaves exactly one accepted active member. The review
	// target advances in the same transaction as the lifecycle change.
	archived, err := f.memories.UpdateState(context.Background(), second.ID, memory.StateArchived)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Version != second.Version+1 {
		t.Fatalf("archived version = %d", archived.Version)
	}

	worker := newReviewWorker(f)
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	resolved, err := f.memories.GetConflict(context.Background(), conflict.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != memory.ConflictResolved {
		t.Fatalf("stale conflict stayed open: %+v", resolved)
	}
	current, err := f.memories.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AssertionStatus != memory.AssertionAccepted {
		t.Fatalf("surviving member status = %s", current.AssertionStatus)
	}
	other, err := f.memories.Get(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.State != memory.StateArchived || other.AssertionStatus != memory.AssertionRejected {
		t.Fatalf("archived member after review = %+v", other)
	}
}

func TestOpenConflictKeepsDependentSkillOutOfRecall(t *testing.T) {
	f := newGovernanceFixture(t)
	group := f.newGroup(t)
	first := f.create(t, memory.CreateInput{
		Kind: memory.KindProcedure, Title: "验证工作流", Content: "修改后运行 go test，并检查构建。",
		ScopeKey: "project:dmemd", Topics: []string{"go"}, FactGroupID: group.ID, ObservedAt: f.now,
	})
	second := f.create(t, memory.CreateInput{
		Kind: memory.KindProcedure, Title: "验证工作流", Content: "修改后只运行单元测试。",
		ScopeKey: "project:dmemd", Topics: []string{"go"}, FactGroupID: group.ID, ObservedAt: f.now,
	})
	f.cancelClassify(t, first.ID, second.ID)
	f.openConflict(t, group, first, second)

	artifacts := artifact.NewService(f.store, nil, artifact.WithClock(func() time.Time { return f.now }))
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := artifacts.Commit(ctx, tx, artifact.CommitInput{
			Kind: artifact.KindSkill, TargetKey: task.SkillKey("project:dmemd", "go"),
			ScopeKey: "project:dmemd", Topic: "go", Title: "验证工作流",
			Content: "修改后运行 go test。", Generator: "test", ProtocolVersion: "test-v1",
			Memories: []artifact.MemoryInput{{ID: first.ID, Version: first.Version}},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	recall := retrieval.NewService(f.store, testutil.DocumentBuilder{}, retrieval.WithArtifacts(artifacts))
	result, err := recall.Recall(context.Background(), retrieval.RecallRequest{
		Query: "验证工作流", ScopeKey: "project:dmemd",
		Layers: []retrieval.Layer{retrieval.LayerSkill}, LayersSpecified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 0 {
		t.Fatalf("skill backed by an open conflict entered recall: %+v", result.Skills)
	}
}

func TestConflictReviewResolvesKnownDisjointTimeline(t *testing.T) {
	f := newGovernanceFixture(t)
	group := f.newGroup(t)
	from1 := f.now.Add(-48 * time.Hour)
	to1 := f.now.Add(-24 * time.Hour)
	from2 := f.now.Add(-24 * time.Hour)
	to2 := f.now.Add(24 * time.Hour)
	first := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "数据库", Content: "项目使用 SQLite。",
		ScopeKey: "project:dmemd", FactGroupID: group.ID, ObservedAt: f.now,
		ValidFrom: &from1, ValidTo: &to1,
		ValidFromBasis: memory.BasisInferred, ValidToBasis: memory.BasisInferred,
	})
	second := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "数据库", Content: "项目使用 PostgreSQL。",
		ScopeKey: "project:dmemd", FactGroupID: group.ID, ObservedAt: f.now,
		ValidFrom: &from2, ValidTo: &to2,
		ValidFromBasis: memory.BasisInferred, ValidToBasis: memory.BasisInferred,
	})
	f.cancelClassify(t, first.ID, second.ID)
	conflict := f.openConflict(t, group, first, second)

	worker := newReviewWorker(f)
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	resolved, err := f.memories.GetConflict(context.Background(), conflict.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != memory.ConflictResolved {
		t.Fatalf("disjoint timeline stayed open: %+v", resolved)
	}
	if !strings.Contains(resolved.Resolution, "governance:auto_review:disjoint_timeline") {
		t.Fatalf("resolution reason = %s", resolved.Resolution)
	}
	for _, id := range []string{first.ID, second.ID} {
		item, err := f.memories.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if item.AssertionStatus != memory.AssertionAccepted {
			t.Fatalf("timeline member %s status = %s", id, item.AssertionStatus)
		}
	}
}

func TestConflictReviewLeavesComplexContradictionIsolated(t *testing.T) {
	f := newGovernanceFixture(t)
	group := f.newGroup(t)
	first := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "数据库", Content: "项目使用 SQLite。",
		ScopeKey: "project:dmemd", FactGroupID: group.ID, ObservedAt: f.now,
	})
	second := f.create(t, memory.CreateInput{
		Kind: memory.KindFact, Title: "数据库", Content: "项目使用 PostgreSQL。",
		ScopeKey: "project:dmemd", FactGroupID: group.ID, ObservedAt: f.now,
	})
	f.cancelClassify(t, first.ID, second.ID)
	conflict := f.openConflict(t, group, first, second)

	worker := newReviewWorker(f)
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	current, err := f.memories.GetConflict(context.Background(), conflict.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != memory.ConflictOpen || current.Revision != conflict.Revision {
		t.Fatalf("complex conflict changed: %+v", current)
	}
	job, err := f.store.GetGovernanceJobByTarget(context.Background(), task.KindReviewConflict,
		task.ConflictKey(conflict.ID))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != task.StatusSucceeded {
		t.Fatalf("review job = %+v", job)
	}
	// Requeueing the unchanged member set is a no-op, so an unresolved conflict
	// does not accumulate repeated work without new evidence.
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, changed, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:          task.KindReviewConflict,
			TargetKey:     task.ConflictKey(conflict.ID),
			InputHash:     memory.ConflictReviewInputHash(current),
			NextAttemptAt: &f.now,
			Now:           f.now,
		})
		if changed {
			t.Fatal("unchanged conflict requeued")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
