package aggregation_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/aggregation"
	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type recordingProvider struct {
	requests []aggregation.Request
}

func (p *recordingProvider) Aggregate(_ context.Context, request aggregation.Request) (aggregation.Result, error) {
	p.requests = append(p.requests, request)
	return aggregation.Result{Content: "聚合正文 " + string(request.Target.Layer)}, nil
}

func TestL1L2L3AggregationPipeline(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	src := source.Source{ID: "source-l1", ScopeKey: "project:dmemd", Content: []byte(`"ignored"`), CreatedAt: now}
	observations := []observation.Observation{
		{ID: "obs-1", SourceID: src.ID, ScopeKey: src.ScopeKey, Kind: observation.KindFact,
			Content: "项目使用 SQLite。", ContentMode: observation.ContentExtractive,
			Status: observation.StatusObserved, CreatedAt: now, UpdatedAt: now},
		{ID: "obs-2", SourceID: src.ID, ScopeKey: src.ScopeKey, Kind: observation.KindPreference,
			Content: "偏好本地优先。", ContentMode: observation.ContentExtractive,
			Status: observation.StatusPending, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)},
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		for _, item := range observations {
			if err := tx.InsertObservation(ctx, item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	provider := &recordingProvider{}
	artifacts := artifact.NewService(store, nil)
	storeAdapter := sqlite.AggregationStore{Store: store}
	options := []aggregation.Option{
		aggregation.WithClock(func() time.Time { return now.Add(time.Hour) }),
		aggregation.WithDebounce(0),
	}
	l1 := aggregation.NewL1AggregationHandler(storeAdapter, artifacts, provider, options...)
	l2 := aggregation.NewL2AggregationHandler(storeAdapter, artifacts, provider, options...)
	l3 := aggregation.NewL3AggregationHandler(storeAdapter, artifacts, provider, options...)
	worker := governance.NewWorker(sqlite.GovernanceStore{Store: store}, map[task.Kind]governance.Handler{
		task.KindAggregateL1: l1,
		task.KindAggregateL2: l2,
		task.KindAggregateL3: l3,
	}, governance.WithWorkerClock(func() time.Time { return now.Add(time.Hour) }))

	trigger := aggregation.NewTrigger(aggregation.TriggerConfig{
		Now:      func() time.Time { return now },
		Debounce: 0,
	})
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		for _, item := range observations {
			if err := trigger.ObservationGoverned(ctx, tx, item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 6; attempt++ {
		processed, err := worker.ProcessOne(ctx)
		if err != nil {
			t.Fatalf("process aggregation job: %v", err)
		}
		if !processed {
			break
		}
	}
	if len(provider.requests) != 3 {
		t.Fatalf("aggregation calls = %d, want 3", len(provider.requests))
	}
	for index, layer := range []artifact.Layer{artifact.LayerL1, artifact.LayerL2, artifact.LayerL3} {
		if provider.requests[index].Target.LayerOrDefault() != layer {
			t.Fatalf("request %d target layer = %s, want %s", index,
				provider.requests[index].Target.LayerOrDefault(), layer)
		}
	}

	bucket := artifact.L1BucketForTime(now, artifact.DefaultL1BucketSize)
	l1Target := aggregation.L1Target(src.ScopeKey, bucket)
	l1Item, err := store.GetArtifactByTarget(ctx, l1Target.Kind, l1Target.TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if l1Item.State != artifact.StateActive || l1Item.CurrentVersion != 1 {
		t.Fatalf("L1 artifact = %+v", l1Item)
	}
	if len(provider.requests[0].Inputs) != 2 || provider.requests[0].InputCount != 2 {
		t.Fatalf("L1 inputs = %+v count=%d", provider.requests[0].Inputs, provider.requests[0].InputCount)
	}
	dependents, err := store.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyObservation, ID: "obs-1",
	})
	if err != nil || len(dependents) != 1 || dependents[0].ID != l1Item.ID {
		t.Fatalf("L1 observation dependents = %+v err=%v", dependents, err)
	}

	l2Target := aggregation.L2Target(src.ScopeKey)
	l2Item, err := store.GetArtifactByTarget(ctx, l2Target.Kind, l2Target.TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if l2Item.State != artifact.StateActive || l2Item.CurrentVersion != 1 ||
		l2Item.Layer != artifact.LayerL2 {
		t.Fatalf("L2 artifact = %+v", l2Item)
	}
	if len(provider.requests[1].Inputs) != 1 ||
		provider.requests[1].Inputs[0].Type != artifact.DependencyDerived ||
		provider.requests[1].Inputs[0].ID != l1Item.ID ||
		!strings.Contains(strings.ToLower(provider.requests[1].Inputs[0].Content), "l1") {
		t.Fatalf("L2 inputs = %+v", provider.requests[1].Inputs)
	}
	dependents, err = store.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyDerived, ID: l1Item.ID, Version: l1Item.CurrentVersion,
	})
	if err != nil || len(dependents) != 1 || dependents[0].ID != l2Item.ID {
		t.Fatalf("L2 derived dependents = %+v err=%v", dependents, err)
	}

	l3Target := aggregation.L3Target(src.ScopeKey)
	l3Item, err := store.GetArtifactByTarget(ctx, l3Target.Kind, l3Target.TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if l3Item.State != artifact.StateActive || l3Item.CurrentVersion != 1 ||
		l3Item.Layer != artifact.LayerL3 {
		t.Fatalf("L3 artifact = %+v", l3Item)
	}
	if len(provider.requests[2].Inputs) != 1 ||
		provider.requests[2].Inputs[0].ID != l2Item.ID {
		t.Fatalf("L3 inputs = %+v", provider.requests[2].Inputs)
	}
}

func TestObservationTriggerMarksLocalAggregateStale(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	src := source.Source{ID: "source-stale", ScopeKey: "project:dmemd", Content: []byte(`"ignored"`), CreatedAt: now}
	first := observation.Observation{ID: "obs-1", SourceID: src.ID, ScopeKey: src.ScopeKey,
		Kind: observation.KindFact, Content: "第一条。", ContentMode: observation.ContentExtractive,
		Status: observation.StatusObserved, CreatedAt: now, UpdatedAt: now}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		return tx.InsertObservation(ctx, first)
	}); err != nil {
		t.Fatal(err)
	}
	trigger := aggregation.NewTrigger(aggregation.TriggerConfig{
		Now: func() time.Time { return now }, Debounce: 0,
	})
	provider := &recordingProvider{}
	artifacts := artifact.NewService(store, nil)
	handler := aggregation.NewL1AggregationHandler(sqlite.AggregationStore{Store: store}, artifacts, provider,
		aggregation.WithClock(func() time.Time { return now }), aggregation.WithDebounce(0))
	worker := governance.NewWorker(sqlite.GovernanceStore{Store: store}, map[task.Kind]governance.Handler{
		task.KindAggregateL1: handler,
	}, governance.WithWorkerClock(func() time.Time { return now }))
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		return trigger.ObservationGoverned(ctx, tx, first)
	}); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("process initial L1 job: processed=%v err=%v", processed, err)
	}

	second := first
	second.ID = "obs-2"
	second.Content = "第二条。"
	second.CreatedAt = now.Add(time.Minute)
	second.UpdatedAt = second.CreatedAt
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertObservation(ctx, second); err != nil {
			return err
		}
		return trigger.ObservationGoverned(ctx, tx, second)
	}); err != nil {
		t.Fatal(err)
	}
	bucket := artifact.L1BucketForTime(now, artifact.DefaultL1BucketSize)
	target := aggregation.L1Target(src.ScopeKey, bucket)
	item, err := store.GetArtifactByTarget(ctx, target.Kind, target.TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if item.State != artifact.StateStale {
		t.Fatalf("L1 state after new observation = %s, want stale", item.State)
	}
	job, err := store.GetGovernanceJobByTarget(ctx, task.KindAggregateL1, target.TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != task.StatusQueued {
		t.Fatalf("L1 job status = %s, want queued", job.Status)
	}
}
