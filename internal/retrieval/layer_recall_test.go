package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
)

type layeredRecallBackend struct {
	Backend
	observationHits []ObservationHit
	memoryIDs       []string
	artifactHits    map[artifact.Layer][]artifact.SearchHit
	artifactQueries []artifact.SearchQuery
}

func (b *layeredRecallBackend) Search(_ context.Context, _ Query) ([]Hit, error) {
	hits := make([]Hit, 0, len(b.memoryIDs))
	for _, id := range b.memoryIDs {
		hits = append(hits, Hit{MemoryID: id, BM25: -1})
	}
	return hits, nil
}

func (b *layeredRecallBackend) SearchObservations(_ context.Context, _ ObservationQuery) ([]ObservationHit, error) {
	return b.observationHits, nil
}

func (b *layeredRecallBackend) LoadSearchCandidates(_ context.Context, ids []string) (map[string]Candidate, error) {
	result := make(map[string]Candidate, len(ids))
	for _, id := range ids {
		result[id] = Candidate{Memory: memory.Memory{
			ID: id, Kind: memory.KindFact, Title: "记忆", Content: "记忆内容",
			State: memory.StateActive, AssertionStatus: memory.AssertionAccepted,
			Version: 1, ScopeKey: "project:dmemd", CreatedAt: testClock(), UpdatedAt: testClock(),
		}}
	}
	return result, nil
}

func (b *layeredRecallBackend) LoadSearchAssemblies(_ context.Context, keys []VersionKey) (map[VersionKey]Assembly, error) {
	result := make(map[VersionKey]Assembly, len(keys))
	for _, key := range keys {
		result[key] = Assembly{
			Memory: memory.Memory{
				ID: key.MemoryID, Kind: memory.KindFact, Title: "记忆", Content: "记忆内容",
				State: memory.StateActive, AssertionStatus: memory.AssertionAccepted,
				Version: key.Version, ScopeKey: "project:dmemd", CreatedAt: testClock(), UpdatedAt: testClock(),
			},
			Current: true,
		}
	}
	return result, nil
}

func (b *layeredRecallBackend) OpenConflictsForMemories(context.Context, []string) ([]memory.Conflict, error) {
	return []memory.Conflict{}, nil
}

func (b *layeredRecallBackend) RebuildSearchDocuments(context.Context, memory.DocumentBuilder) (int, error) {
	return 0, nil
}

type layeredRecallArtifacts struct {
	hits    map[artifact.Layer][]artifact.SearchHit
	queries []artifact.SearchQuery
}

func (a *layeredRecallArtifacts) Search(_ context.Context, query artifact.SearchQuery) ([]artifact.SearchHit, error) {
	a.queries = append(a.queries, query)
	if len(query.Layers) == 0 {
		return nil, nil
	}
	var result []artifact.SearchHit
	for _, hit := range a.hits[query.Layers[0]] {
		for _, state := range query.States {
			if hit.DerivedMemory.State == state {
				result = append(result, hit)
				break
			}
		}
	}
	return result, nil
}

func testClock() time.Time {
	return time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
}

func derivedHit(layer artifact.Layer, kind artifact.Kind, id string, state artifact.State) artifact.SearchHit {
	return artifact.SearchHit{
		DerivedMemory: artifact.DerivedMemory{
			ID: id, Layer: layer, Kind: kind, TargetKey: id, ScopeKey: "project:dmemd",
			State: state, CurrentVersion: 1, UpdatedAt: testClock(),
		},
		Revision: artifact.Revision{
			ArtifactID: id, Version: 1, Title: "标题", Content: "聚合正文",
			CreatedAt: testClock(),
		},
		Dependencies: []artifact.DependencyRef{{
			Type: artifact.DependencyObservation, ID: "observation-1", Required: true,
		}},
		Score: 1,
	}
}

func TestRecallSelectsLayersByQueryMode(t *testing.T) {
	backend := &layeredRecallBackend{
		observationHits: []ObservationHit{{
			Observation: observation.Observation{
				ID: "observation-1", SourceID: "source-1", Kind: observation.KindFact,
				Content: "项目使用 SQLite。", ContentMode: observation.ContentExtractive,
				Status: observation.StatusObserved, CreatedAt: testClock(), UpdatedAt: testClock(),
			},
			Score: 1,
		}},
		memoryIDs: []string{"memory-1"},
	}
	artifacts := &layeredRecallArtifacts{hits: map[artifact.Layer][]artifact.SearchHit{
		artifact.LayerL1: {derivedHit(artifact.LayerL1, artifact.KindLocalSummary, "l1", artifact.StateActive)},
		artifact.LayerL2: {derivedHit(artifact.LayerL2, artifact.KindTopicSummary, "l2", artifact.StateActive)},
		artifact.LayerL3: {derivedHit(artifact.LayerL3, artifact.KindScopeSummary, "l3", artifact.StateActive)},
		artifact.LayerL4: {derivedHit(artifact.LayerL4, artifact.KindSkill, "l4", artifact.StateActive)},
	}}
	service := NewService(backend, nil, WithArtifacts(artifacts))

	detail, err := service.Recall(context.Background(), RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd", QueryMode: QueryModeDetail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Observations) != 1 || len(detail.Memories) != 1 ||
		len(detail.L1)+len(detail.L2)+len(detail.L3)+len(detail.L4) != 0 {
		t.Fatalf("detail recall = %+v", detail)
	}

	overview, err := service.Recall(context.Background(), RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd", QueryMode: QueryModeOverview,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.L1) != 1 || len(overview.L2) != 1 || len(overview.L3) != 1 ||
		len(overview.L4) != 0 || len(overview.Memories) != 0 || len(overview.Observations) != 0 {
		t.Fatalf("overview recall = %+v", overview)
	}
	if len(overview.L1[0].Dependencies) != 1 || overview.L1[0].Dependencies[0].Type != artifact.DependencyObservation {
		t.Fatalf("drill-down dependencies = %+v", overview.L1[0].Dependencies)
	}

	guidance, err := service.Recall(context.Background(), RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd", QueryMode: QueryModeGuidance,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(guidance.L4) != 1 || guidance.L1 == nil || len(guidance.L1) != 0 {
		t.Fatalf("guidance recall = %+v", guidance)
	}

	all, err := service.Recall(context.Background(), RecallRequest{Query: "项目", ScopeKey: "project:dmemd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Observations) != 1 || len(all.Memories) != 1 ||
		len(all.L1) != 1 || len(all.L2) != 1 || len(all.L3) != 1 || len(all.L4) != 1 {
		t.Fatalf("default recall = %+v", all)
	}
}

func TestRecallStaleHighLayerFallsBackAndMarksExplicitStale(t *testing.T) {
	backend := &layeredRecallBackend{}
	artifacts := &layeredRecallArtifacts{hits: map[artifact.Layer][]artifact.SearchHit{
		artifact.LayerL1: {derivedHit(artifact.LayerL1, artifact.KindLocalSummary, "l1", artifact.StateActive)},
		artifact.LayerL2: {derivedHit(artifact.LayerL2, artifact.KindTopicSummary, "l2", artifact.StateStale)},
	}}
	service := NewService(backend, nil, WithArtifacts(artifacts))

	fallback, err := service.Recall(context.Background(), RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
		Layers: []Layer{LayerL2}, LayersSpecified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fallback.L2) != 0 || len(fallback.L1) != 1 {
		t.Fatalf("stale fallback = %+v", fallback)
	}
	if !containsWarning(fallback.Warnings, WarningDerivedStaleFallback) {
		t.Fatalf("fallback warning missing: %+v", fallback.Warnings)
	}

	explicit, err := service.Recall(context.Background(), RecallRequest{
		Query: "项目", ScopeKey: "project:dmemd",
		Layers: []Layer{LayerL2}, LayersSpecified: true,
		DerivedStates: []artifact.State{artifact.StateStale},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit.L2) != 1 || explicit.L2[0].DerivedMemory.State != artifact.StateStale {
		t.Fatalf("explicit stale = %+v", explicit)
	}
	if !containsWarning(explicit.Warnings, WarningStaleIncluded) {
		t.Fatalf("stale warning missing: %+v", explicit.Warnings)
	}
}

func containsWarning(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
