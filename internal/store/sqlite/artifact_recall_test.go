package sqlite

import (
	"context"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

func TestSearchArtifactsFiltersLayerAndReturnsStaleWithDependencies(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := testTime(0)
	seedArtifact := func(item artifact.DerivedMemory, content string, dependencies []artifact.DependencyRef) {
		t.Helper()
		if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
			if err := tx.CreateArtifact(ctx, item); err != nil {
				return err
			}
			if err := tx.InsertArtifactRevision(ctx, artifact.Revision{
				ArtifactID: item.ID, Version: 1, Title: "聚合", Content: content,
				Payload: []byte("{}"), CreatedAt: now,
			}); err != nil {
				return err
			}
			if err := tx.LinkDerivedDependencies(ctx, item.ID, 1, dependencies); err != nil {
				return err
			}
			return tx.UpsertArtifactSearchDocument(ctx, artifact.SearchDocument{
				ArtifactID: item.ID, Version: 1,
				TitleTerms: retrieval.IndexText("聚合"), ContentTerms: retrieval.IndexText(content),
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	seedArtifact(artifact.DerivedMemory{
		ID: "l1", Layer: artifact.LayerL1, Kind: artifact.KindLocalSummary,
		TargetKey: "l1-target", ScopeKey: "project:dmemd", CurrentVersion: 1,
		State: artifact.StateActive, UpdatedAt: now,
	}, "项目使用 SQLite。", []artifact.DependencyRef{{
		Type: artifact.DependencyObservation, ID: "observation-1", Required: true,
	}})
	seedArtifact(artifact.DerivedMemory{
		ID: "l2", Layer: artifact.LayerL2, Kind: artifact.KindTopicSummary,
		TargetKey: "l2-target", ScopeKey: "project:dmemd", CurrentVersion: 1,
		State: artifact.StateStale, UpdatedAt: now,
	}, "项目存储聚合。", nil)

	groups := retrieval.Groups("SQLite")
	expression, err := retrieval.Compile(groups)
	if err != nil {
		t.Fatal(err)
	}
	l1, err := store.SearchArtifacts(ctx, artifact.SearchQuery{
		Expression: expression, Terms: retrieval.GroupTexts(groups),
		ScopeKey: "project:dmemd", Layers: []artifact.Layer{artifact.LayerL1},
		States: []artifact.State{artifact.StateActive}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(l1) != 1 || l1[0].DerivedMemory.ID != "l1" || len(l1[0].Dependencies) != 1 ||
		l1[0].Dependencies[0].Type != artifact.DependencyObservation {
		t.Fatalf("l1 search = %+v", l1)
	}

	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.DeleteArtifactSearchDocument(ctx, "l2")
	}); err != nil {
		t.Fatal(err)
	}
	staleGroups := retrieval.Groups("聚合")
	staleExpression, err := retrieval.Compile(staleGroups)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.SearchArtifacts(ctx, artifact.SearchQuery{
		Expression: staleExpression, Terms: retrieval.GroupTexts(staleGroups),
		ScopeKey: "project:dmemd", Layers: []artifact.Layer{artifact.LayerL2},
		States: []artifact.State{artifact.StateStale}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].DerivedMemory.ID != "l2" ||
		stale[0].DerivedMemory.State != artifact.StateStale {
		t.Fatalf("stale search = %+v", stale)
	}
}
