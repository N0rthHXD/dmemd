package retrieval

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

// Embedding the interfaces makes unexpected backend calls fail immediately.
type queryRecordingBackend struct {
	Backend
	queries []Query
}

func (b *queryRecordingBackend) Search(_ context.Context, query Query) ([]Hit, error) {
	b.queries = append(b.queries, query)
	return []Hit{}, nil
}

func (*queryRecordingBackend) OpenConflictsForMemories(context.Context, []string) ([]memory.Conflict, error) {
	return []memory.Conflict{}, nil
}

func (*queryRecordingBackend) LoadSearchCandidates(context.Context, []string) (map[string]Candidate, error) {
	return map[string]Candidate{}, nil
}

func (*queryRecordingBackend) LoadSearchAssemblies(context.Context, []VersionKey) (map[VersionKey]Assembly, error) {
	return map[VersionKey]Assembly{}, nil
}

type queryRecordingArtifacts struct {
	queries []artifact.SearchQuery
}

func (a *queryRecordingArtifacts) Search(_ context.Context, query artifact.SearchQuery) ([]artifact.SearchHit, error) {
	a.queries = append(a.queries, query)
	return []artifact.SearchHit{}, nil
}

type queryRecordingEncoder struct {
	QueryEncoder
	queries []string
}

func (e *queryRecordingEncoder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	e.queries = append(e.queries, text)
	// A dimension mismatch exercises hybrid fallback without a vector backend.
	return nil, nil
}

func (*queryRecordingEncoder) Dimensions() int { return 3 }

func TestEveryTextQueryUsesSameFilteredAnyTerms(t *testing.T) {
	for _, mode := range []Mode{ModeKeyword, ModeHybrid} {
		t.Run(string(mode), func(t *testing.T) {
			backend := &queryRecordingBackend{}
			artifacts := &queryRecordingArtifacts{}
			encoder := &queryRecordingEncoder{}
			service := NewService(backend, nil, WithArtifacts(artifacts), WithSemantic(encoder))
			query := "我对什么过敏？花生 过敏"
			wantTerms := []string{"过敏", "花生"}
			wantExpression := `("过敏") OR ("花生")`
			ctx := context.Background()
			if _, err := service.Recall(ctx, RecallRequest{Query: query, Mode: mode}); err != nil {
				t.Fatal(err)
			}
			if len(backend.queries) != 2 {
				t.Fatalf("atomic and conflict query count = %d, want 2", len(backend.queries))
			}
			if _, err := service.RelatedContext(ctx, query, 10); err != nil {
				t.Fatal(err)
			}
			if len(backend.queries) != 3 {
				t.Fatalf("related context did not query backend: %+v", backend.queries)
			}
			for _, plan := range backend.queries {
				if !reflect.DeepEqual(GroupTexts(plan.Groups), wantTerms) || plan.Expression != wantExpression {
					t.Fatalf("keyword query plan = %+v", plan)
				}
			}
			if len(artifacts.queries) != 3 {
				t.Fatalf("derived query count = %d, want summary, experience and skill", len(artifacts.queries))
			}
			for _, plan := range artifacts.queries {
				if plan.Expression != wantExpression {
					t.Fatalf("derived expression = %q, want %q", plan.Expression, wantExpression)
				}
			}
			for _, text := range encoder.queries {
				if text != query {
					t.Fatalf("semantic query must retain original text: %q", text)
				}
			}
		})
	}
}

func TestQueriesWithoutEffectiveTermsNeverAccessBackends(t *testing.T) {
	for _, mode := range []Mode{ModeKeyword, ModeHybrid} {
		for _, query := range []string{"", "，。！？ * ()", "我 对 什么", "what is the"} {
			t.Run(string(mode)+"/"+query, func(t *testing.T) {
				// Nil embedded interfaces panic if any backend or model is used.
				service := NewService(nil, nil, WithArtifacts(&struct{ ArtifactSearcher }{}),
					WithSemantic(&struct{ QueryEncoder }{}))
				ctx := context.Background()
				result, err := service.Search(ctx, Request{Query: query, Mode: mode})
				if err != nil || result.Items == nil || len(result.Items) != 0 || len(result.Conflicts) != 0 {
					t.Fatalf("empty search = %+v, %v", result, err)
				}
				recall, err := service.Recall(ctx, RecallRequest{Query: query, Mode: mode})
				if err != nil || recall.Memories == nil || recall.Summaries == nil || recall.Experiences == nil || recall.Skills == nil ||
					len(recall.Memories)+len(recall.Summaries)+len(recall.Experiences)+len(recall.Skills)+len(recall.Conflicts)+len(recall.Warnings) != 0 {
					t.Fatalf("empty recall = %+v, %v", recall, err)
				}
				related, err := service.RelatedContext(ctx, query, 10)
				if err != nil || related.Memories == nil || len(related.Memories) != 0 || !related.Complete {
					t.Fatalf("empty related context = %+v, %v", related, err)
				}
			})
		}
	}
}

func TestRelatedQueryFiltersBeforeApplyingTermLimit(t *testing.T) {
	backend := &queryRecordingBackend{}
	service := NewService(backend, nil)
	if _, err := service.RelatedContext(context.Background(), englishQueryStopwords+" 过敏 "+strings.Repeat("过敏 ", 40), 10); err != nil {
		t.Fatal(err)
	}
	if len(backend.queries) != 1 || !reflect.DeepEqual(GroupTexts(backend.queries[0].Groups), []string{"过敏"}) {
		t.Fatalf("stopwords or duplicates exhausted the term budget: %+v", backend.queries)
	}
}
