package governance_test

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/aggregation"
	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func TestInvalidateDependentsPropagatesThroughEveryLayer(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	src := source.Source{ID: "src-invalidation", ScopeKey: "scope-a",
		Content: []byte(`{"text":"two observations"}`), CreatedAt: now}
	seedSourceObservation(t, store, src, []observation.Observation{
		seedObservation("obs-a", src.ID, "scope-a", "事实一", observation.StatusObserved, now),
		seedObservation("obs-b", src.ID, "scope-a", "事实二", observation.StatusObserved, now.Add(time.Second)),
	})

	bucket := artifact.L1BucketForTime(now, artifact.DefaultL1BucketSize)
	l1Target := aggregation.L1Target("scope-a", bucket)
	l2Target := aggregation.L2Target("scope-a")
	l3Target := aggregation.L3Target("scope-a")
	seedDerived(t, store, l1Target, "l1-1", 1, []artifact.DependencyRef{
		{Type: artifact.DependencyObservation, ID: "obs-a", Required: true},
		{Type: artifact.DependencyObservation, ID: "obs-b", Required: true},
	})
	seedDerived(t, store, l2Target, "l2-1", 1, []artifact.DependencyRef{
		{Type: artifact.DependencyDerived, ID: "l1-1", Version: 1, Required: true},
	})
	seedDerived(t, store, l3Target, "l3-1", 1, []artifact.DependencyRef{
		{Type: artifact.DependencyDerived, ID: "l2-1", Version: 1, Required: true},
	})

	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.UpdateObservationStatus(ctx, "obs-a", observation.StatusRetracted, now); err != nil {
			return err
		}
		return governance.InvalidateDependents(ctx, tx, artifact.DependencyRef{
			Type: artifact.DependencyObservation,
			ID:   "obs-a",
		}, governance.ReasonSourceRedacted)
	}); err != nil {
		t.Fatal(err)
	}

	for _, item := range []struct {
		id   string
		kind artifact.Kind
	}{{"l1-1", artifact.KindLocalSummary}, {"l2-1", artifact.KindTopicSummary}, {"l3-1", artifact.KindScopeSummary}} {
		loaded, err := store.GetArtifact(ctx, item.id)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.State != artifact.StateStale {
			t.Fatalf("%s state = %s, want stale", item.id, loaded.State)
		}
		job, err := store.GetGovernanceJobByTarget(ctx, jobKindForTest(t, item.kind), loaded.TargetKey)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != task.StatusQueued {
			t.Fatalf("%s job status = %s, want queued", item.id, job.Status)
		}
	}
	if docs := countArtifactSearchDocs(t, store); docs != 0 {
		t.Fatalf("default derived index rows = %d, want 0", docs)
	}

	// Removing the remaining observation leaves every layer without legal
	// input. Each target must be disabled and its queued rebuild cancelled.
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.UpdateObservationStatus(ctx, "obs-b", observation.StatusRetracted, now); err != nil {
			return err
		}
		return governance.InvalidateDependents(ctx, tx, artifact.DependencyRef{
			Type: artifact.DependencyObservation,
			ID:   "obs-b",
		}, governance.ReasonSourceRedacted)
	}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id   string
		kind artifact.Kind
	}{{"l1-1", artifact.KindLocalSummary}, {"l2-1", artifact.KindTopicSummary}, {"l3-1", artifact.KindScopeSummary}} {
		loaded, err := store.GetArtifact(ctx, item.id)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.State != artifact.StateDisabled {
			t.Fatalf("%s state = %s, want disabled", item.id, loaded.State)
		}
		job, err := store.GetGovernanceJobByTarget(ctx, jobKindForTest(t, item.kind), loaded.TargetKey)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != task.StatusCancelled {
			t.Fatalf("%s job status = %s, want cancelled", item.id, job.Status)
		}
	}
}

func TestObservationDeletionDeletesOnlyUnsupportedMemory(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	memories := newMemoryService(t, store, now)
	lifecycle := governance.NewObservationLifecycleService(
		sqlite.GovernanceStore{Store: store}, memories,
		governance.WithObservationLifecycleClock(func() time.Time { return now }),
	)
	src := source.Source{ID: "src-lifecycle", ScopeKey: "scope-a",
		Content: []byte(`{"text":"observations"}`), CreatedAt: now}
	seedSourceObservation(t, store, src, []observation.Observation{
		seedObservation("obs-only", src.ID, "scope-a", "只有一条来源", observation.StatusObserved, now),
		seedObservation("obs-shared", src.ID, "scope-a", "共享来源", observation.StatusObserved, now),
		seedObservation("obs-other", src.ID, "scope-a", "另一条来源", observation.StatusObserved, now.Add(time.Second)),
	})

	onlyMemory := seedMemory(t, store, memories, "scope-a", "只有一条来源", []string{"obs-only"})
	sharedMemory := seedMemory(t, store, memories, "scope-a", "共享来源", []string{"obs-shared", "obs-other"})
	l1Target := aggregation.L1TargetForTime("scope-a", now, artifact.DefaultL1BucketSize)
	seedDerived(t, store, l1Target, "l1-lifecycle", 1, []artifact.DependencyRef{
		{Type: artifact.DependencyObservation, ID: "obs-only", Required: true},
	})
	l4Target := artifact.Target{
		Layer: artifact.LayerL4, Kind: artifact.KindGuidance, ScopeKey: "scope-a", Key: "guidance",
	}
	seedDerived(t, store, l4Target, "l4-memory", 1, []artifact.DependencyRef{
		{Type: artifact.DependencyMemory, ID: onlyMemory.ID, Version: onlyMemory.Version, Required: true},
	})

	result, err := lifecycle.Delete(ctx, "obs-only")
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationID != "obs-only" || len(result.MemoryIDs) != 1 || result.MemoryIDs[0] != onlyMemory.ID {
		t.Fatalf("deletion result = %+v", result)
	}
	deleted, err := memories.Get(ctx, onlyMemory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.State != "deleted" {
		t.Fatalf("unsupported memory state = %s, want deleted", deleted.State)
	}
	retained, err := memories.Get(ctx, sharedMemory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.State != "active" {
		t.Fatalf("shared memory state = %s, want active", retained.State)
	}
	withdrawn, err := store.GetObservation(ctx, "obs-only")
	if err != nil {
		t.Fatal(err)
	}
	if withdrawn.Status != observation.StatusDeleted {
		t.Fatalf("observation status = %s, want deleted", withdrawn.Status)
	}
	l1, err := store.GetArtifact(ctx, "l1-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if l1.State != artifact.StateStale {
		t.Fatalf("L1 state = %s, want stale because obs-other remains", l1.State)
	}
	l4, err := store.GetArtifact(ctx, "l4-memory")
	if err != nil {
		t.Fatal(err)
	}
	if l4.State != artifact.StateStale {
		t.Fatalf("L4 dependent state = %s, want stale after memory deletion", l4.State)
	}
}

func TestSourceRedactionRetractsObservationsAndInvalidatesDerived(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	memories := newMemoryService(t, store, now)
	sources := source.NewService(store, sqlite.SourceStore{Store: store},
		source.WithClock(func() time.Time { return now }),
		source.WithRedactionPropagator(governance.NewRedactionPropagator(memories)),
	)
	src := source.Source{ID: "src-redact", ScopeKey: "scope-redact",
		Content: []byte(`{"messages":[{"id":"m1","role":"user","content":"需要撤回的内容"}]}`), CreatedAt: now}
	evidence, err := source.EvidenceForSegments(src, []string{"seg-0001"}, now)
	if err != nil {
		t.Fatal(err)
	}
	item := seedObservation("obs-redact", src.ID, "scope-redact", "需要撤回的内容", observation.StatusObserved, now)
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
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
	redactedMemory := seedMemory(t, store, memories, "scope-redact", "需要撤回的内容", []string{"obs-redact"})
	l1Target := aggregation.L1TargetForTime("scope-redact", now, artifact.DefaultL1BucketSize)
	seedDerived(t, store, l1Target, "l1-redact", 1, []artifact.DependencyRef{
		{Type: artifact.DependencyObservation, ID: "obs-redact", Required: true},
	})

	result, err := sources.Redact(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Redacted || result.RetractedObservations != 1 {
		t.Fatalf("redaction result = %+v", result)
	}
	if len(result.InvalidatedDerived) != 1 || result.InvalidatedDerived[0] != "l1-redact" {
		t.Fatalf("invalidated derived = %+v", result.InvalidatedDerived)
	}
	loadedSource, err := store.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loadedSource.Redacted || len(loadedSource.Content) != 0 {
		t.Fatalf("source after redaction = %+v", loadedSource)
	}
	loadedObservation, err := store.GetObservation(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedObservation.Status != observation.StatusRetracted {
		t.Fatalf("observation status = %s, want retracted", loadedObservation.Status)
	}
	loadedEvidence, err := store.ListObservationEvidence(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loadedEvidence) != 1 || !loadedEvidence[0].Redacted || loadedEvidence[0].Quote != "" {
		t.Fatalf("evidence after redaction = %+v", loadedEvidence)
	}
	memoryItem, err := memories.Get(ctx, redactedMemory.ID)
	if err != nil {
		t.Fatal(err)
	}
	if memoryItem.State != "deleted" {
		t.Fatalf("memory state = %s, want deleted", memoryItem.State)
	}
	l1, err := store.GetArtifact(ctx, "l1-redact")
	if err != nil {
		t.Fatal(err)
	}
	if l1.State != artifact.StateDisabled {
		t.Fatalf("L1 state = %s, want disabled", l1.State)
	}
}

func TestForgetRemovesObservationsAndDisablesDependentDerived(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	memories := newMemoryService(t, store, now)
	src := source.Source{ID: "src-forget", ScopeKey: "scope-forget",
		Content: []byte(`{"text":"forget me"}`), CreatedAt: now}
	item := seedObservation("obs-forget", src.ID, "scope-forget", "forget me", observation.StatusObserved, now)
	seedSourceObservation(t, store, src, []observation.Observation{item})
	seedMemory(t, store, memories, "scope-forget", "forget me", []string{"obs-forget"})
	l1Target := aggregation.L1TargetForTime("scope-forget", now, artifact.DefaultL1BucketSize)
	seedDerived(t, store, l1Target, "l1-forget", 1, []artifact.DependencyRef{
		{Type: artifact.DependencyObservation, ID: "obs-forget", Required: true},
	})

	forget := governance.NewForgetService(sqlite.GovernanceStore{Store: store}, memories,
		governance.WithForgetClock(func() time.Time { return now }))
	result, err := forget.Forget(ctx, governance.ForgetRequest{SourceIDs: []string{src.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SourceIDs) != 1 || result.SourceIDs[0] != src.ID {
		t.Fatalf("forget result = %+v", result)
	}
	if _, err := store.GetObservation(ctx, "obs-forget"); err == nil {
		t.Fatal("observation survived permanent forget")
	}
	if _, err := store.GetSource(ctx, src.ID); err == nil {
		t.Fatal("source survived permanent forget")
	}
	l1, err := store.GetArtifact(ctx, "l1-forget")
	if err != nil {
		t.Fatal(err)
	}
	if l1.State != artifact.StateDisabled {
		t.Fatalf("dependent L1 state = %s, want disabled", l1.State)
	}
}

func TestSourceRedactionCancelsQueuedAggregationWithoutArtifact(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)
	memories := newMemoryService(t, store, now)
	sources := source.NewService(store, sqlite.SourceStore{Store: store},
		source.WithClock(func() time.Time { return now }),
		source.WithRedactionPropagator(governance.NewRedactionPropagator(memories)),
	)
	src := source.Source{ID: "src-job", ScopeKey: "scope-job",
		Content: []byte(`{"text":"queued job"}`), CreatedAt: now}
	item := seedObservation("obs-job", src.ID, "scope-job", "queued job", observation.StatusObserved, now)
	seedSourceObservation(t, store, src, []observation.Observation{item})
	l1 := artifact.Target{
		Layer: artifact.LayerL1, Kind: artifact.KindLocalSummary, ScopeKey: "scope-job",
		Bucket: artifact.L1BucketForTime(now, artifact.DefaultL1BucketSize),
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind: task.KindAggregateL1, TargetKey: l1.TargetKey(),
			InputHash: "queued-before-redaction", Now: now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := sources.Redact(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	job, err := store.GetGovernanceJobByTarget(ctx, task.KindAggregateL1, l1.TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != task.StatusCancelled {
		t.Fatalf("queued L1 job status = %s, want cancelled", job.Status)
	}
}

func seedSourceObservation(t *testing.T, store *sqlite.Store, src source.Source,
	items []observation.Observation) {
	t.Helper()
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		for _, item := range items {
			if item.SourceID == "" {
				item.SourceID = src.ID
			}
			if err := tx.InsertObservation(ctx, item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func seedObservation(id, sourceID, scopeKey, content string,
	status observation.Status, now time.Time) observation.Observation {
	return observation.Observation{
		ID: id, SourceID: sourceID, ScopeKey: scopeKey, Kind: observation.KindFact,
		Content: content, ContentMode: observation.ContentExtractive,
		Status: status, ExtractionVersion: "extraction-v6", CreatedAt: now, UpdatedAt: now,
	}
}

func newMemoryService(t *testing.T, store *sqlite.Store, now time.Time) *memory.Service {
	t.Helper()
	return memory.NewService(store, sqlite.MemoryStore{Store: store},
		testutil.DocumentBuilder{}, memory.WithClock(func() time.Time { return now }))
}

func seedMemory(t *testing.T, store *sqlite.Store, memories *memory.Service,
	scopeKey, content string, observationIDs []string) memory.Memory {
	t.Helper()
	var created memory.Memory
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = memories.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindFact, Title: content, Content: content,
			AssertionStatus: memory.AssertionAccepted, ScopeKey: scopeKey,
		})
		if err != nil {
			return err
		}
		return tx.InsertMemoryObservationRefs(ctx, created.ID, created.Version, observationIDs)
	}); err != nil {
		t.Fatal(err)
	}
	return created
}

func seedDerived(t *testing.T, store *sqlite.Store, target artifact.Target, id string,
	version int, deps []artifact.DependencyRef) {
	t.Helper()
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.CreateArtifact(ctx, artifact.DerivedMemory{
			ID: id, Layer: target.LayerOrDefault(), Kind: target.Kind, TargetKey: target.TargetKey(),
			ScopeKey: target.ScopeKey, Topic: target.Topic, CurrentVersion: version,
			State: artifact.StateActive, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.InsertArtifactRevision(ctx, artifact.Revision{
			ArtifactID: id, Version: version, Title: id, Content: id,
			Payload: []byte("{}"), CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.LinkDerivedDependencies(ctx, id, version, deps); err != nil {
			return err
		}
		return tx.UpsertArtifactSearchDocument(ctx, artifact.SearchDocument{
			ArtifactID: id, Version: version, TitleTerms: id, ContentTerms: id,
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func countArtifactSearchDocs(t *testing.T, store *sqlite.Store) int {
	t.Helper()
	db := testutil.OpenInspector(t, store.Path())
	defer db.Close()
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM artifact_search_docs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func jobKindForTest(t *testing.T, kind artifact.Kind) task.Kind {
	t.Helper()
	switch kind {
	case artifact.KindLocalSummary:
		return task.KindAggregateL1
	case artifact.KindTopicSummary:
		return task.KindAggregateL2
	case artifact.KindScopeSummary:
		return task.KindAggregateL3
	default:
		return task.KindBuildGuidance
	}
}
