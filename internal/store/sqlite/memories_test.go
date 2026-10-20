package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestMemoryRevisionAndEvidenceRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sourceID := "source-roundtrip"
	seedSource(t, store, sourceID)
	base := testTime(0)
	memoryID := "memory-roundtrip"

	v1 := memory.Memory{
		ID: memoryID, Kind: memory.KindFact, Title: "First title", Content: "first content",
		State: memory.StateActive, Version: 1, ObservedAt: base, CreatedAt: base, UpdatedAt: base,
		CreatedByApp: "app-one", OriginSourceID: sourceID,
	}
	evidence1 := source.Evidence{
		ID: "evidence-1", SourceID: sourceID, MessageID: "msg-1", Quote: "first content",
		Location:  &source.Location{Field: "messages.0.content", Start: 0, End: 5},
		ModelName: "test-model", CreatedAt: base,
	}
	revision1 := memory.Revision{
		MemoryID: memoryID, Version: 1, Operation: memory.OperationCreate,
		Reason: "initial", Snapshot: v1, CreatedAt: base,
	}
	v2 := v1
	v2.Title = "Second title"
	v2.Content = "second content"
	v2.Version = 2
	v2.UpdatedAt = base.Add(time.Minute)
	evidence2 := source.Evidence{
		ID: "evidence-2", SourceID: sourceID, Quote: "second content", CreatedAt: base.Add(time.Minute),
	}
	revision2 := memory.Revision{
		MemoryID: memoryID, Version: 2, Operation: memory.OperationUpdate,
		Reason: "correction", Snapshot: v2, CreatedAt: base.Add(time.Minute),
	}

	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertMemory(ctx, v1); err != nil {
			return err
		}
		if err := tx.InsertEvidence(ctx, evidence1); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, revision1); err != nil {
			return err
		}
		if err := tx.LinkRevisionEvidence(ctx, memoryID, 1, []string{evidence1.ID}); err != nil {
			return err
		}
		if err := tx.UpdateMemory(ctx, v2); err != nil {
			return err
		}
		if err := tx.InsertEvidence(ctx, evidence2); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, revision2); err != nil {
			return err
		}
		return tx.LinkRevisionEvidence(ctx, memoryID, 2, []string{evidence2.ID})
	}); err != nil {
		t.Fatal(err)
	}

	current, err := store.GetMemory(ctx, memoryID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 2 || current.Content != "second content" || current.CreatedByApp != "app-one" {
		t.Fatalf("current = %+v", current)
	}

	first, err := getRevisionForTest(store, memoryID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot.Content != "first content" || first.Operation != memory.OperationCreate {
		t.Fatalf("revision 1 = %+v", first)
	}
	if len(first.Evidence) != 1 || first.Evidence[0].ID != evidence1.ID {
		t.Fatalf("revision 1 evidence = %+v", first.Evidence)
	}
	if first.Evidence[0].Location == nil || first.Evidence[0].Location.Start != 0 || first.Evidence[0].Location.End != 5 {
		t.Fatalf("location = %+v", first.Evidence[0].Location)
	}
	if first.Evidence[0].MessageID != "msg-1" || first.Evidence[0].ModelName != "test-model" {
		t.Fatalf("evidence metadata = %+v", first.Evidence[0])
	}

	second, err := getRevisionForTest(store, memoryID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Evidence) != 1 || second.Evidence[0].ID != evidence2.ID {
		t.Fatalf("revision 2 evidence = %+v", second.Evidence)
	}

	versions, err := listRevisionsForTest(store, memoryID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("versions = %+v", versions)
	}
	limited, err := listRevisionsForTest(store, memoryID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("default revision limit returned %d", len(limited))
	}

	if _, err := getRevisionForTest(store, memoryID, 99); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing revision error = %v", err)
	}
	if _, err := store.GetMemory(ctx, "missing"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing memory error = %v", err)
	}
}

func TestListMemoriesDefaultsFiltersAndOrdering(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sourceID := "source-list"
	seedSource(t, store, sourceID)
	base := testTime(0)

	for i := 0; i < 55; i++ {
		seedMemory(t, store, sourceID, memory.Memory{
			ID:           "bulk-" + twoDigits(i),
			Kind:         memory.KindFact,
			Title:        "bulk",
			Content:      "bulk",
			State:        memory.StateActive,
			Version:      1,
			ObservedAt:   base,
			CreatedAt:    base,
			UpdatedAt:    base.Add(time.Duration(i) * time.Millisecond),
			CreatedByApp: "bulk-app",
		})
	}
	items, err := store.ListMemories(ctx, memory.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 50 {
		t.Fatalf("default list length = %d, want 50", len(items))
	}
	if items[0].ID != "bulk-54" || items[49].ID != "bulk-05" {
		t.Fatalf("default order first=%s last=%s", items[0].ID, items[49].ID)
	}

	all, err := store.ListMemories(ctx, memory.ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 55 {
		t.Fatalf("explicit limit length = %d, want 55", len(all))
	}

	// Tie on updated_at must fall back to id ascending.
	seedMemory(t, store, sourceID, memory.Memory{
		ID: "tie-bbb", Kind: memory.KindPreference, Title: "tie", Content: "tie",
		State: memory.StateArchived, Version: 1, ObservedAt: base, CreatedAt: base,
		UpdatedAt: base.Add(time.Hour), CreatedByApp: "other-app",
	})
	seedMemory(t, store, sourceID, memory.Memory{
		ID: "tie-aaa", Kind: memory.KindPreference, Title: "tie", Content: "tie",
		State: memory.StateActive, Version: 1, ObservedAt: base, CreatedAt: base,
		UpdatedAt: base.Add(time.Hour), CreatedByApp: "other-app",
	})
	seedMemory(t, store, sourceID, memory.Memory{
		ID: "tombstone", Kind: memory.KindEvent, Title: "tombstone", Content: "tombstone",
		State: memory.StateDeleted, Version: 2, ObservedAt: base, CreatedAt: base,
		UpdatedAt: base.Add(2 * time.Hour), CreatedByApp: "other-app",
	})

	ties, err := store.ListMemories(ctx, memory.ListOptions{Kinds: []memory.Kind{memory.KindPreference}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ties) != 2 || ties[0].ID != "tie-aaa" || ties[1].ID != "tie-bbb" {
		t.Fatalf("tie order = %+v", ties)
	}

	active, err := store.ListMemories(ctx, memory.ListOptions{States: []memory.State{memory.StateActive}, CreatedByApp: "other-app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != "tie-aaa" {
		t.Fatalf("active filter = %+v", active)
	}

	after := base.Add(30 * time.Minute)
	byTime, err := store.ListMemories(ctx, memory.ListOptions{UpdatedAfter: &after, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTime) != 3 {
		t.Fatalf("updated_after filter length = %d, want 3", len(byTime))
	}

	before := base.Add(30 * time.Minute)
	byBefore, err := store.ListMemories(ctx, memory.ListOptions{UpdatedBefore: &before, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(byBefore) != 55 {
		t.Fatalf("updated_before filter length = %d, want 55", len(byBefore))
	}

	tombstone, err := store.GetMemory(ctx, "tombstone")
	if err != nil {
		t.Fatal(err)
	}
	if tombstone.State != memory.StateDeleted {
		t.Fatalf("tombstone state = %s", tombstone.State)
	}

	none, err := store.ListMemories(ctx, memory.ListOptions{Kinds: []memory.Kind{memory.KindProcedure}, CreatedByApp: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("empty filter returned %d items", len(none))
	}
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string([]byte{byte('0' + value/10), byte('0' + value%10)})
}

func getRevisionForTest(store *Store, memoryID string, version int) (memory.Revision, error) {
	var result memory.Revision
	err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		var err error
		result, err = tx.GetRevision(ctx, memoryID, version)
		return err
	})
	return result, err
}

func listRevisionsForTest(store *Store, memoryID string, limit int) ([]memory.Revision, error) {
	var result []memory.Revision
	err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		var err error
		result, err = tx.ListRevisions(ctx, memoryID, limit)
		return err
	})
	return result, err
}
