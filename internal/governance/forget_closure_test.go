package governance_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func (f *forgetFixture) create(t *testing.T, input memory.CreateInput) memory.Memory {
	t.Helper()
	var item memory.Memory
	err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		item, err = f.memories.Create(ctx, tx, input)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func (f *forgetFixture) group(t *testing.T, subject string) memory.FactGroup {
	t.Helper()
	group, err := memory.NewFactGroup(memory.FactGroupInput{
		Subject: subject, Predicate: "数据库", Scope: "project:dmemd", Cardinality: memory.CardinalitySingle,
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

func TestForgetSourceClosesOverEvidenceInAnyRevision(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "current-evidence", true: "historical-only-evidence"}[historical], func(t *testing.T) {
			f := newForgetFixture(t)
			src := f.createSource(t, `"项目使用 SQLite。"`)
			other := f.createSource(t, `"其他来源"`)
			item := f.create(t, memory.CreateInput{
				Kind: memory.KindFact, Content: "项目使用 SQLite。", ObservedAt: f.now,
				OriginSourceID: other.ID, ScopeKey: "project:dmemd",
				Evidence: []source.Evidence{source.NewEvidence(src, "", "项目使用 SQLite。", "test", f.now)},
			})
			unrelated := f.createMemory(t, other.ID)
			if historical {
				if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
					_, err := f.memories.Revise(ctx, tx, memory.ReviseInput{
						MemoryID: item.ID, Kind: item.Kind, Content: "项目改用 PostgreSQL。",
						ScopeKey: item.ScopeKey, ObservedAt: f.now, Operation: memory.OperationCorrect,
					})
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := f.forget.Forget(context.Background(), governance.ForgetRequest{SourceIDs: []string{src.ID}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.MemoryIDs, []string{item.ID}) {
				t.Fatalf("evidence closure = %+v", result)
			}
			if _, err := f.store.GetMemory(context.Background(), unrelated.ID); err != nil {
				t.Fatalf("unrelated memory removed: %v", err)
			}
			if _, err := f.store.GetSource(context.Background(), other.ID); err != nil {
				t.Fatalf("shared origin removed: %v", err)
			}
		})
	}
}

func TestForgetDuplicateClosureAndRetainedWarnings(t *testing.T) {
	f := newForgetFixture(t)
	group := f.group(t, "项目")
	otherGroup := f.group(t, "其他项目")
	start, end := f.now, f.now.Add(24*time.Hour)
	next := end.Add(24 * time.Hour)
	src := f.createSource(t, `"项目使用 SQLite。"`)
	base := memory.CreateInput{
		Kind: memory.KindFact, Content: "项目使用 SQLite。", ScopeKey: "project:dmemd",
		FactGroupID: group.ID, ObservedAt: f.now, ValidFrom: &start, ValidTo: &end,
		OriginSourceID: src.ID,
	}
	seed := f.create(t, base)
	base.OriginSourceID = ""
	duplicate := f.create(t, base)
	base.Content = "项目使用 sqlite！"
	formatted := f.create(t, base)
	deleted := f.create(t, base)
	if _, err := f.memories.Delete(context.Background(), deleted.ID); err != nil {
		t.Fatal(err)
	}
	var retained []string
	for _, change := range []func(*memory.CreateInput){
		func(in *memory.CreateInput) { in.Content = "项目不使用 SQLite。" },
		func(in *memory.CreateInput) { in.ScopeKey = "project:other" },
		func(in *memory.CreateInput) { in.ValidFrom, in.ValidTo = &end, &next },
		func(in *memory.CreateInput) { in.ValidFrom, in.ValidTo = nil, nil },
		func(in *memory.CreateInput) { in.Phase = memory.PhaseCancelled },
	} {
		input := base
		change(&input)
		retained = append(retained, f.create(t, input).ID)
	}
	base.FactGroupID = otherGroup.ID
	independent := f.create(t, base)
	base.FactGroupID = ""
	ungrouped := f.create(t, base)
	result, err := f.forget.Forget(context.Background(), governance.ForgetRequest{SourceIDs: []string{src.ID}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{seed.ID, duplicate.ID, formatted.ID, deleted.ID}
	sort.Strings(want)
	sort.Strings(retained)
	if !reflect.DeepEqual(result.MemoryIDs, want) || !reflect.DeepEqual(result.RetainedRelatedMemoryIDs, retained) ||
		!reflect.DeepEqual(result.Warnings, []string{"related_memories_retained"}) {
		t.Fatalf("closure = %+v; removed=%v retained=%v", result, want, retained)
	}
	for _, id := range want {
		if _, err := f.store.GetMemory(context.Background(), id); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("copy %s survived: %v", id, err)
		}
	}
	for _, id := range append(retained, independent.ID, ungrouped.ID) {
		if _, err := f.store.GetMemory(context.Background(), id); err != nil {
			t.Fatalf("independent fact %s removed: %v", id, err)
		}
	}
}

func TestForgetDuplicateClosureReachesFixedPoint(t *testing.T) {
	f := newForgetFixture(t)
	group := f.group(t, "项目")
	var items []memory.Memory
	for i := 0; i < 3; i++ {
		start, end := f.now.Add(time.Duration(i)*time.Hour), f.now.Add(time.Duration(i+2)*time.Hour)
		items = append(items, f.create(t, memory.CreateInput{
			Kind: memory.KindFact, Content: "项目使用 SQLite。", ScopeKey: "project:dmemd",
			FactGroupID: group.ID, ObservedAt: f.now, ValidFrom: &start, ValidTo: &end,
		}))
	}
	result, err := f.forget.Forget(context.Background(), governance.ForgetRequest{MemoryIDs: []string{items[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.MemoryIDs) != 3 || len(result.RetainedRelatedMemoryIDs) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("transitive closure = %+v", result)
	}
}

func TestForgetRemovesWholeConflictAndCancelsReview(t *testing.T) {
	for _, status := range []memory.ConflictStatus{memory.ConflictOpen, memory.ConflictResolved} {
		t.Run(string(status), func(t *testing.T) {
			f := newForgetFixture(t)
			var members []memory.ConflictMember
			for i := 0; i < 3; i++ {
				item := f.createMemory(t, "")
				members = append(members, memory.ConflictMember{MemoryID: item.ID, Version: 1})
			}
			conflict := memory.Conflict{
				ID: idgen.NewUUID(), Status: status, ReasonCode: memory.ConflictContradictoryValue,
				Revision: 1, CreatedAt: f.now, UpdatedAt: f.now, Members: members,
			}
			if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
				if err := tx.InsertConflict(ctx, conflict); err != nil {
					return err
				}
				_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
					Kind: task.KindReviewConflict, TargetKey: task.ConflictKey(conflict.ID), InputHash: "old", Now: f.now,
				})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.forget.Forget(context.Background(), governance.ForgetRequest{MemoryIDs: []string{members[0].MemoryID}}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.GetConflict(context.Background(), conflict.ID); !errors.Is(err, memory.ErrConflictNotFound) {
				t.Fatalf("dependent conflict survived with remaining members: %v", err)
			}
			job, err := f.store.GetGovernanceJobByTarget(context.Background(), task.KindReviewConflict, task.ConflictKey(conflict.ID))
			if err != nil || job.Status != task.StatusCancelled || job.Revision <= 1 {
				t.Fatalf("review not invalidated: %+v %v", job, err)
			}
			for _, member := range members[1:] {
				if _, err := f.store.GetMemory(context.Background(), member.MemoryID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestForgetHistoricalDependencyBumpsEveryArtifactTarget(t *testing.T) {
	f := newForgetFixture(t)
	item := f.createMemory(t, "")
	receipt, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		ScopeKey: "project:dmemd", Goal: "验证", Outcome: governance.OutcomeUnknown,
	})
	if err != nil {
		t.Fatal(err)
	}
	type affectedTarget struct {
		item   artifact.Artifact
		target artifact.Target
		job    task.Job
	}
	var affected []affectedTarget
	for _, kind := range []artifact.Kind{artifact.KindTopicSummary, artifact.KindProfile, artifact.KindExperienceCase, artifact.KindSkill} {
		t.Run(string(kind), func(t *testing.T) {
			target := artifact.Target{Kind: kind, ScopeKey: "project:dmemd", Topic: "historical"}
			if kind == artifact.KindProfile {
				target.Topic = ""
			}
			if kind == artifact.KindExperienceCase {
				target = artifact.ExperienceTarget(receipt.ID)
			}
			input := artifact.CommitInput{
				Kind: kind, TargetKey: target.TargetKey(), ScopeKey: "project:dmemd", Topic: target.Topic,
				ExperienceID: receipt.ID, Content: "旧派生内容", Memories: []artifact.MemoryInput{{ID: item.ID, Version: 1}},
			}
			var created artifact.Artifact
			var claimed task.Job
			if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
				var err error
				created, err = f.artifacts.Commit(ctx, tx, input)
				if err != nil {
					return err
				}
				input.Memories, input.Content = nil, "最新内容不再依赖该记忆"
				if _, err = f.artifacts.Commit(ctx, tx, input); err != nil {
					return err
				}
				jobKind := task.KindAggregate
				if kind == artifact.KindSkill {
					jobKind = task.KindBuildSkill
				}
				if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
					Kind: jobKind, TargetKey: target.TargetKey(), InputHash: "before-forget", Now: f.now,
				}); err != nil {
					return err
				}
				for {
					job, ok, err := tx.ClaimNextGovernanceJob(ctx, f.now)
					if err != nil {
						return err
					}
					if !ok {
						return errors.New("target was not claimable")
					}
					if job.Kind == jobKind && job.TargetKey == target.TargetKey() {
						claimed = job
						return nil
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			affected = append(affected, affectedTarget{created, target, claimed})
		})
	}
	if _, err := f.forget.Forget(context.Background(), governance.ForgetRequest{MemoryIDs: []string{item.ID}}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range affected {
		if _, err := f.store.GetArtifact(context.Background(), entry.item.ID); !errors.Is(err, artifact.ErrNotFound) {
			t.Errorf("historical %s survived: %v", entry.item.Kind, err)
		}
		jobKind := task.KindAggregate
		if entry.item.Kind == artifact.KindSkill {
			jobKind = task.KindBuildSkill
		}
		job, err := f.store.GetGovernanceJobByTarget(context.Background(), jobKind, entry.target.TargetKey())
		if err != nil || job.Status != task.StatusQueued || job.Revision <= entry.job.Revision {
			t.Errorf("historical target not requeued: %+v %v", job, err)
		}
		if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			ok, err := tx.FinishJob(ctx, entry.job.ID, entry.job.Revision, f.now)
			if ok {
				t.Error("late result completed a forgotten revision")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestForgetExpandedClosureRollsBackOnFailure(t *testing.T) {
	f := newForgetFixture(t)
	group := f.group(t, "项目")
	src := f.createSource(t, `"项目使用 SQLite。"`)
	input := memory.CreateInput{
		Kind: memory.KindFact, Content: "项目使用 SQLite。", ScopeKey: "project:dmemd",
		FactGroupID: group.ID, ObservedAt: f.now, OriginSourceID: src.ID,
	}
	seed := f.create(t, input)
	input.OriginSourceID = ""
	copy := f.create(t, input)
	summary := f.commitSummary(t, copy)
	service := governance.NewForgetService(sqlite.GovernanceStore{Store: f.store}, failingPurger{inner: f.memories})
	result, err := service.Forget(context.Background(), governance.ForgetRequest{SourceIDs: []string{src.ID}})
	if err == nil || len(result.MemoryIDs) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("failed forget = %+v, %v", result, err)
	}
	for _, id := range []string{seed.ID, copy.ID} {
		if _, err := f.store.GetMemory(context.Background(), id); err != nil {
			t.Fatalf("closure partially deleted %s: %v", id, err)
		}
	}
	if _, err := f.store.GetArtifact(context.Background(), summary.ID); err != nil {
		t.Fatalf("derived closure not rolled back: %v", err)
	}
	if _, err := f.store.GetSource(context.Background(), src.ID); err != nil {
		t.Fatalf("source not rolled back: %v", err)
	}
}
