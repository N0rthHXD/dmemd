package retrieval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

// Recall runs the single unified recall entry over the selected layers. It
// never waits for generation and never adds atomic results to a request that
// explicitly selected only derived layers.
func (s *Service) Recall(ctx context.Context, request RecallRequest) (RecallResult, error) {
	mode, err := normalizeMode(request.Mode)
	if err != nil {
		return RecallResult{}, err
	}
	topK, err := normalizeTopK(request.TopK)
	if err != nil {
		return RecallResult{}, err
	}
	layers, err := normalizeLayers(request.Layers, request.LayersSpecified)
	if err != nil {
		return RecallResult{}, err
	}
	explicitAsOf := request.Filters.AsOf != nil
	scopeKey := strings.TrimSpace(request.ScopeKey)
	if scopeKey == "" {
		scopeKey = strings.TrimSpace(request.Filters.ScopeKey)
	}
	filtersInput := request.Filters
	filtersInput.ScopeKey = scopeKey
	filters, filtersOK := sanitizeFilters(filtersInput)
	if !filtersOK {
		result := emptyRecallResult()
		if request.Explain {
			result.Diagnostics = &SearchDiagnostics{}
		}
		return result, nil
	}
	if filters.AsOf == nil && filters.EventAt == nil {
		asOf := time.Now().UTC()
		filters.AsOf = &asOf
	}
	derivedAsOf := time.Now().UTC()
	if filters.AsOf != nil {
		derivedAsOf = *filters.AsOf
	}

	result := emptyRecallResult()
	if request.Explain {
		result.Diagnostics = &SearchDiagnostics{}
	}
	if layers[LayerAtomic] {
		searchRequest := Request{
			Query: request.Query,
			Mode:  mode,
			Filters: Filters{
				Kinds:         filters.Kinds,
				States:        filters.States,
				AsOf:          filters.AsOf,
				EventAt:       filters.EventAt,
				ScopeKey:      filters.ScopeKey,
				CreatedByApp:  filters.CreatedByApp,
				UpdatedAfter:  filters.UpdatedAfter,
				UpdatedBefore: filters.UpdatedBefore,
			},
			TopK:    topK,
			Explain: request.Explain,
		}
		atomic, err := s.Search(ctx, searchRequest)
		if err != nil {
			return RecallResult{}, err
		}
		result.Memories = atomic.Items
		result.Conflicts = atomic.Conflicts
		result.Warnings = append(result.Warnings, atomic.Warnings...)
		result.Diagnostics = atomic.Diagnostics
	}

	derivedRequested := layers[LayerSummary] || layers[LayerExperience] || layers[LayerSkill]
	if derivedRequested && explicitAsOf {
		// The first release only supports an atomic historical view. It never
		// fabricates a historical derived snapshot from current text.
		result.Warnings = append(result.Warnings, WarningDerivedAsOfUnsupported)
		derivedRequested = false
	}
	if derivedRequested {
		groups := queryGroups(request.Query)
		if len(groups) > 0 && s.artifacts != nil {
			expression, err := Compile(groups)
			if err != nil {
				return RecallResult{}, err
			}
			layersToKinds := []struct {
				layer Layer
				kinds []artifact.Kind
				into  *[]artifact.SearchHit
			}{
				{layer: LayerSummary, kinds: []artifact.Kind{artifact.KindTopicSummary, artifact.KindScopeSummary, artifact.KindProfile}, into: &result.Summaries},
				{layer: LayerExperience, kinds: []artifact.Kind{artifact.KindExperienceCase}, into: &result.Experiences},
				{layer: LayerSkill, kinds: []artifact.Kind{artifact.KindSkill}, into: &result.Skills},
			}
			for _, entry := range layersToKinds {
				if !layers[entry.layer] {
					continue
				}
				hits, err := s.artifacts.Search(ctx, artifact.SearchQuery{
					Expression: expression,
					ScopeKey:   scopeKey,
					Kinds:      entry.kinds,
					AsOf:       derivedAsOf,
					Limit:      topK,
				})
				if err != nil {
					// A derived-index failure must not take atomic recall down.
					result.Warnings = append(result.Warnings, WarningDerivedUnavailable)
					continue
				}
				*entry.into = hits
			}
		}
	}

	applyContextBudget(&result, request.MaxContextTokens, result.Diagnostics)
	if result.Diagnostics != nil {
		result.Diagnostics.ReturnedItems = len(result.Memories)
	}
	if explicitAsOf {
		for _, item := range result.Memories {
			if item.Memory.TemporalStatus() == memory.TemporalUnknown {
				result.Warnings = append(result.Warnings, WarningTemporalUncertain)
				break
			}
		}
	}
	result.Warnings = dedupeWarnings(result.Warnings)
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	return result, nil
}

func normalizeLayers(input []Layer, specified bool) (map[Layer]bool, error) {
	result := map[Layer]bool{}
	if !specified && len(input) == 0 {
		result[LayerAtomic] = true
		result[LayerSummary] = true
		result[LayerExperience] = true
		result[LayerSkill] = true
		return result, nil
	}
	if len(input) == 0 {
		return result, nil
	}
	for _, layer := range input {
		if !layer.Known() {
			return nil, fmt.Errorf("%w: layer %q", ErrInvalidRequest, layer)
		}
		result[layer] = true
	}
	return result, nil
}

func emptyRecallResult() RecallResult {
	return RecallResult{
		Memories:    []Item{},
		Summaries:   []artifact.SearchHit{},
		Experiences: []artifact.SearchHit{},
		Skills:      []artifact.SearchHit{},
		Conflicts:   []memory.ConflictSummary{},
		Warnings:    []string{},
	}
}

// applyContextBudget uses a deliberately conservative rune-based estimate.
// Callers can only see that truncation happened; the service never claims
// tokenizer-exact accounting for an arbitrary model.
func applyContextBudget(result *RecallResult, maxTokens int, diagnostics *SearchDiagnostics) {
	if maxTokens <= 0 {
		return
	}
	remaining := maxTokens
	truncated := 0
	fit := func(count int, estimate func(int) int) int {
		kept := 0
		for kept < count {
			cost := estimate(kept)
			if cost > remaining {
				truncated += count - kept
				break
			}
			remaining -= cost
			kept++
		}
		return kept
	}
	// Keep atomic and summary content first, then experiences and skills.
	result.Memories = result.Memories[:fit(len(result.Memories), func(i int) int {
		return estimateTokens(result.Memories[i].Memory.Title) + estimateTokens(result.Memories[i].Memory.Content)
	})]
	result.Summaries = result.Summaries[:fit(len(result.Summaries), func(i int) int {
		return estimateTokens(result.Summaries[i].Revision.Title) + estimateTokens(result.Summaries[i].Revision.Content)
	})]
	result.Experiences = result.Experiences[:fit(len(result.Experiences), func(i int) int {
		return estimateTokens(result.Experiences[i].Revision.Title) + estimateTokens(result.Experiences[i].Revision.Content)
	})]
	result.Skills = result.Skills[:fit(len(result.Skills), func(i int) int {
		return estimateTokens(result.Skills[i].Revision.Title) + estimateTokens(result.Skills[i].Revision.Content)
	})]
	if truncated > 0 {
		result.Warnings = append(result.Warnings, WarningContextBudgetTruncated)
		if diagnostics != nil {
			diagnostics.ContextBudgetTruncated += truncated
		}
	}
}

func estimateTokens(text string) int {
	runes := len([]rune(strings.TrimSpace(text)))
	if runes == 0 {
		return 0
	}
	return runes/2 + 1
}
