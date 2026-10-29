package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
)

func TestListDerivedInputsByLayer(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	src := seedSource(t, store, "source-inputs")
	now := testTime(0)

	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		for _, item := range []observation.Observation{
			{ID: "obs-observed", SourceID: src.ID, ScopeKey: "scope-a",
				Kind: observation.KindFact, Content: "观察一", ContentMode: observation.ContentExtractive,
				Status: observation.StatusObserved, CreatedAt: now, UpdatedAt: now},
			{ID: "obs-pending", SourceID: src.ID, ScopeKey: "scope-a",
				Kind: observation.KindPreference, Content: "观察二", ContentMode: observation.ContentExtractive,
				Status: observation.StatusPending, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)},
			{ID: "obs-retracted", SourceID: src.ID, ScopeKey: "scope-a",
				Kind: observation.KindFact, Content: "已撤回", ContentMode: observation.ContentExtractive,
				Status: observation.StatusRetracted, CreatedAt: now, UpdatedAt: now},
			{ID: "obs-deleted", SourceID: src.ID, ScopeKey: "scope-a",
				Kind: observation.KindFact, Content: "已删除", ContentMode: observation.ContentExtractive,
				Status: observation.StatusDeleted, CreatedAt: now, UpdatedAt: now},
			{ID: "obs-other-scope", SourceID: src.ID, ScopeKey: "scope-b",
				Kind: observation.KindFact, Content: "其他范围", ContentMode: observation.ContentExtractive,
				Status: observation.StatusObserved, CreatedAt: now, UpdatedAt: now},
		} {
			if err := tx.InsertObservation(ctx, item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	target := artifact.Target{
		Layer: artifact.LayerL1, Kind: artifact.KindLocalSummary,
		ScopeKey: "scope-a", Bucket: "2026-10-29",
	}
	inputs, truncated, err := store.ListDerivedInputs(ctx, target, 10)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(inputs) != 2 {
		t.Fatalf("L1 inputs = %+v truncated=%v", inputs, truncated)
	}
	seen := map[string]bool{}
	for _, input := range inputs {
		seen[input.ID] = true
		if input.Type != artifact.DependencyObservation {
			t.Fatalf("input type = %s", input.Type)
		}
	}
	if !seen["obs-observed"] || !seen["obs-pending"] {
		t.Fatalf("L1 inputs = %+v", inputs)
	}
	page, truncated, err := store.ListDerivedInputs(ctx, target, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || !truncated {
		t.Fatalf("bounded L1 inputs = %+v truncated=%v", page, truncated)
	}

	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		for _, item := range []struct {
			id     string
			bucket string
			state  artifact.State
		}{
			{id: "l1-active", bucket: "bucket-a", state: artifact.StateActive},
			{id: "l1-stale", bucket: "bucket-b", state: artifact.StateStale},
		} {
			targetKey := artifact.Target{
				Layer: artifact.LayerL1, Kind: artifact.KindLocalSummary,
				ScopeKey: "scope-a", Bucket: item.bucket,
			}.TargetKey()
			if err := tx.CreateArtifact(ctx, artifact.DerivedMemory{
				ID: item.id, Layer: artifact.LayerL1, Kind: artifact.KindLocalSummary,
				TargetKey: targetKey, ScopeKey: "scope-a", Topic: "storage",
				CurrentVersion: 1, State: item.state, UpdatedAt: now,
			}); err != nil {
				return err
			}
			if err := tx.InsertArtifactRevision(ctx, artifact.Revision{
				ArtifactID: item.id, Version: 1, Title: "标题", Content: "内容",
				Payload: []byte("{}"), CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	l2 := artifact.Target{
		Layer: artifact.LayerL2, Kind: artifact.KindTopicSummary,
		ScopeKey: "scope-a", Topic: "storage",
	}
	inputs, truncated, err = store.ListDerivedInputs(ctx, l2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].ID != "l1-active" || inputs[0].Type != artifact.DependencyDerived ||
		inputs[0].Version != 1 {
		t.Fatalf("L2 inputs = %+v", inputs)
	}
	if !truncated {
		t.Fatal("stale L1 target did not mark the L2 selection truncated")
	}

	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.CreateArtifact(ctx, artifact.DerivedMemory{
			ID: "l2-active", Layer: artifact.LayerL2, Kind: artifact.KindTopicSummary,
			TargetKey: l2.TargetKey(), ScopeKey: l2.ScopeKey, Topic: l2.Topic,
			CurrentVersion: 1, State: artifact.StateActive, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return tx.InsertArtifactRevision(ctx, artifact.Revision{
			ArtifactID: "l2-active", Version: 1, Title: "存储", Content: "L2 内容",
			Payload: []byte("{}"), CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	l3 := artifact.Target{Layer: artifact.LayerL3, Kind: artifact.KindScopeSummary, ScopeKey: "scope-a"}
	inputs, truncated, err = store.ListDerivedInputs(ctx, l3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].ID != "l2-active" || inputs[0].Type != artifact.DependencyDerived {
		t.Fatalf("L3 inputs = %+v", inputs)
	}
	if truncated {
		t.Fatal("L3 selection was unexpectedly truncated")
	}

	seedMemory(t, store, src.ID, memory.Memory{
		ID: "memory-l4", Kind: memory.KindPreference, Title: "偏好", Content: "使用 SQLite",
		State: memory.StateActive, AssertionStatus: memory.AssertionAccepted, Version: 1,
		ScopeKey: "scope-a", CreatedAt: now, UpdatedAt: now,
	})
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.InsertExperienceRun(ctx, governance.ExperienceRun{
			ID: "experience-l4", SourceID: src.ID, ScopeKey: "scope-a",
			Goal: "验证存储", Conditions: []string{"本地数据库"},
			Actions: []string{"运行测试"}, Outcome: governance.OutcomeSuccess,
			State: governance.ExperienceRecorded, CreatedAt: now, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	l4 := artifact.Target{
		Layer: artifact.LayerL4, Kind: artifact.KindGuidance,
		ScopeKey: "scope-a", Key: "storage",
	}
	inputs, _, err = store.ListDerivedInputs(ctx, l4, 20)
	if err != nil {
		t.Fatal(err)
	}
	foundMemory, foundExperience := false, false
	for _, input := range inputs {
		switch {
		case input.Type == artifact.DependencyMemory && input.ID == "memory-l4":
			foundMemory = true
		case input.Type == artifact.DependencyExperience && input.ID == "experience-l4":
			foundExperience = true
			if input.Content == "" {
				t.Fatal("experience input has no readable content")
			}
		}
	}
	if !foundMemory || !foundExperience {
		t.Fatalf("L4 inputs = %+v", inputs)
	}
}

func TestListDependentsAndMarkStale(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	src := seedSource(t, store, "source-dependents")
	seedMemory(t, store, src.ID, memory.Memory{
		ID: "memory-1", Kind: memory.KindFact, Title: "标题", Content: "内容",
		State: memory.StateActive, AssertionStatus: memory.AssertionAccepted, Version: 1,
		ScopeKey: "scope-a", CreatedAt: testTime(0), UpdatedAt: testTime(0),
	})
	now := testTime(0)
	l2 := artifact.Target{
		Layer: artifact.LayerL2, Kind: artifact.KindTopicSummary,
		ScopeKey: "scope-a", Topic: "storage",
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.CreateArtifact(ctx, artifact.DerivedMemory{
			ID: "derived-1", Layer: artifact.LayerL2, Kind: artifact.KindTopicSummary,
			TargetKey: l2.TargetKey(), ScopeKey: l2.ScopeKey, Topic: l2.Topic,
			CurrentVersion: 1, State: artifact.StateActive, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertArtifactRevision(ctx, artifact.Revision{
			ArtifactID: "derived-1", Version: 1, Title: "标题", Content: "内容",
			Payload: []byte("{}"), CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.LinkDerivedDependencies(ctx, "derived-1", 1, []artifact.DependencyRef{
			{Type: artifact.DependencyObservation, ID: "obs-1", Required: true},
			{Type: artifact.DependencyObservation, ID: "obs-1", Required: true},
			{Type: artifact.DependencyMemory, ID: "memory-1", Version: 1, Required: true},
		}); err != nil {
			return err
		}
		return tx.UpsertArtifactSearchDocument(ctx, artifact.SearchDocument{
			ArtifactID: "derived-1", Version: 1,
			TitleTerms: "标题", ContentTerms: "内容",
		})
	}); err != nil {
		t.Fatal(err)
	}

	dependents, err := store.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyObservation, ID: "obs-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dependents) != 1 || dependents[0].ID != "derived-1" ||
		dependents[0].Version != 1 || dependents[0].Layer != artifact.LayerL2 ||
		dependents[0].Kind != artifact.KindTopicSummary || dependents[0].State != artifact.StateActive {
		t.Fatalf("dependents = %+v", dependents)
	}
	if err := store.MarkDerivedStale(ctx, "derived-1", "input_changed"); err != nil {
		t.Fatal(err)
	}
	item, err := store.GetArtifact(ctx, "derived-1")
	if err != nil {
		t.Fatal(err)
	}
	if item.State != artifact.StateStale || item.ReasonCode != "input_changed" {
		t.Fatalf("artifact after stale = %+v", item)
	}
	if err := store.MarkDerivedStale(ctx, "missing", "input_changed"); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("missing stale error = %v", err)
	}
	dependents, err = store.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyMemory, ID: "memory-1", Version: 1,
	})
	if err != nil || len(dependents) != 1 {
		t.Fatalf("memory dependents = %+v err=%v", dependents, err)
	}
	if dependents, err := store.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyMemory, ID: "memory-1", Version: 2,
	}); err != nil || len(dependents) != 0 {
		t.Fatalf("version-mismatched dependents = %+v err=%v", dependents, err)
	}
}
