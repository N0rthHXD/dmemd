package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestObservationSearchIndexAndLayerRecall(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	src := seedSource(t, store, "source-observation-search")
	now := testTime(0)
	item := observation.Observation{
		ID: "observation-search", SourceID: src.ID, ScopeKey: "project:dmemd",
		Kind: observation.KindFact, Content: "项目使用 SQLite 保存记忆。",
		ContentMode: observation.ContentExtractive, OriginRole: "user",
		Status: observation.StatusObserved, ExtractionVersion: "extraction-v6",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertObservation(ctx, item); err != nil {
			return err
		}
		evidence := source.Evidence{
			ID: "evidence-search", SourceID: src.ID, SegmentID: "seg-0001",
			Quote: "项目使用 SQLite 保存记忆。", CreatedAt: now,
		}
		if err := tx.InsertEvidence(ctx, evidence); err != nil {
			return err
		}
		return tx.InsertObservationEvidence(ctx, item.ID, evidence.ID)
	}); err != nil {
		t.Fatal(err)
	}

	service := retrieval.NewService(store, retrieval.NewDocumentBuilder())
	result, err := service.Recall(ctx, retrieval.RecallRequest{
		Query: "SQLite", ScopeKey: "project:dmemd",
		Layers: []retrieval.Layer{retrieval.LayerObservations}, LayersSpecified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 1 || result.Observations[0].Observation.ID != item.ID {
		t.Fatalf("observations = %+v", result.Observations)
	}
	if len(result.Observations[0].EvidenceRefs) != 1 ||
		result.Observations[0].EvidenceRefs[0].ID != "evidence-search" {
		t.Fatalf("evidence refs = %+v", result.Observations[0].EvidenceRefs)
	}

	result, err = service.Recall(ctx, retrieval.RecallRequest{
		Query: "SQLite", ScopeKey: "project:dmemd",
		Layers: []retrieval.Layer{retrieval.LayerMemories}, LayersSpecified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 0 {
		t.Fatalf("observation leaked into memory-only recall: %+v", result.Observations)
	}
}

func TestListObservationsFiltersAndBounds(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	src := seedSource(t, store, "source-observation-list")
	now := testTime(0)
	items := []observation.Observation{
		{
			ID: "observation-a", SourceID: src.ID, ScopeKey: "project:a",
			Kind: observation.KindFact, Content: "A", ContentMode: observation.ContentExtractive,
			Status: observation.StatusObserved, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "observation-b", SourceID: src.ID, ScopeKey: "project:b",
			Kind: observation.KindPreference, Content: "B", ContentMode: observation.ContentNormalized,
			Status: observation.StatusPending, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
		},
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		for _, item := range items {
			if err := tx.InsertObservation(ctx, item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListObservations(ctx, observation.ListOptions{SourceID: src.ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "observation-b" {
		t.Fatalf("bounded = %+v", got)
	}
	got, err = store.ListObservations(ctx, observation.ListOptions{
		ScopeKey: "project:a", Kind: observation.KindFact, Status: observation.StatusObserved, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "observation-a" {
		t.Fatalf("filtered = %+v", got)
	}
}
