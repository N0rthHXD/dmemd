package retrieval

import (
	"context"
	"fmt"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

// Recall runs the layer-aware recall entry point. The selected layers are
// independent partitions: each receives its own Top-K bound and no layer is
// appended to another layer's ranking. A high-level derived result that is
// stale is never presented as a current conclusion; recall falls back to the
// next lower layer and emits a stable warning instead of waiting for a model.
func (s *Service) Recall(ctx context.Context, request RecallRequest) (RecallResult, error) {
	mode, err := normalizeMode(request.Mode)
	if err != nil {
		return RecallResult{}, err
	}
	topK, err := normalizeTopK(request.TopK)
	if err != nil {
		return RecallResult{}, err
	}
	if !request.QueryMode.Known() {
		return RecallResult{}, fmt.Errorf("%w: query_mode %q", ErrInvalidRequest, request.QueryMode)
	}
	layers, err := normalizeLayers(request.Layers, request.LayersSpecified, request.QueryMode)
	if err != nil {
		return RecallResult{}, err
	}
	scopeKey := strings.TrimSpace(request.ScopeKey)
	if scopeKey == "" {
		scopeKey = strings.TrimSpace(request.Filters.ScopeKey)
	}
	filtersInput := request.Filters
	filtersInput.ScopeKey = scopeKey
	filters, filtersOK := sanitizeFilters(filtersInput)

	result := emptyRecallResult()
	result.QueryMode = request.QueryMode
	var diagnostics *SearchDiagnostics
	if request.Explain {
		diagnostics = &SearchDiagnostics{}
		result.Diagnostics = diagnostics
	}

	groups := queryGroups(request.Query)
	if len(groups) == 0 || !filtersOK {
		return result, nil
	}
	expression, err := Compile(groups)
	if err != nil {
		return RecallResult{}, err
	}

	selected := make(map[Layer]bool, len(layers))
	for layer := range layers {
		selected[layer] = true
	}

	if layers[LayerObservations] {
		observations, err := s.searchObservationLayer(ctx, expression, groups, filters, topK, request.Explain)
		if err != nil {
			result.Warnings = append(result.Warnings, WarningObservationsUnavailable)
		} else {
			result.Observations = observations
			if diagnostics != nil {
				diagnostics.ObservationCandidates += len(observations)
			}
		}
	}

	if layers[LayerMemories] {
		items, conflicts, warnings, memoryDiagnostics, err := s.searchMemoryLayer(
			ctx, request.Query, mode, filters, topK, request.Explain)
		if err != nil {
			return RecallResult{}, err
		}
		result.Memories = items
		result.Conflicts = conflicts
		result.Warnings = append(result.Warnings, warnings...)
		if diagnostics != nil {
			diagnostics.MemoryCandidates += len(items)
			mergeSearchDiagnostics(diagnostics, memoryDiagnostics)
		}
	}

	states := normalizeDerivedStates(request.DerivedStates)
	derivedRequested := false
	for _, layer := range []Layer{LayerL1, LayerL2, LayerL3, LayerL4} {
		if !layers[layer] {
			continue
		}
		derivedRequested = true
		items, err := s.searchDerivedLayer(ctx, expression, groups, layer, scopeKey, states, topK, request.Explain)
		if err != nil {
			result.Warnings = append(result.Warnings, WarningDerivedUnavailable)
			result.SetDerived(layer, nil)
			continue
		}
		result.SetDerived(layer, items)
		if diagnostics != nil {
			diagnostics.DerivedCandidates += len(items)
		}
	}
	if derivedRequested && hasDerivedState(states, artifact.StateStale) {
		for _, layer := range []Layer{LayerL1, LayerL2, LayerL3, LayerL4} {
			if len(result.Derived(layer)) > 0 {
				result.Warnings = append(result.Warnings, WarningStaleIncluded)
				break
			}
		}
	}

	// A stale high-level object is not a complete current conclusion. Fall back
	// to the next lower layer only when the current query still matches that
	// stale object; unrelated stale history must not widen ordinary recall.
	if derivedRequested && !hasDerivedState(states, artifact.StateStale) {
		for _, layer := range []Layer{LayerL2, LayerL3, LayerL4} {
			if !layers[layer] || len(result.Derived(layer)) > 0 {
				continue
			}
			// When the caller already selected the next lower layer, its
			// independent partition is the fallback; do not add a second search
			// or a warning for a layer the request already owns.
			if lower, ok := layer.Lower(); ok && layers[lower] {
				continue
			}
			stale, err := s.searchDerivedLayer(ctx, expression, groups, layer, scopeKey,
				[]artifact.State{artifact.StateStale}, 1, false)
			if err != nil || len(stale) == 0 {
				continue
			}
			result.Warnings = append(result.Warnings, WarningDerivedStaleFallback)
			s.fallbackRecall(ctx, layer, expression, groups, request.Query, mode,
				filters, topK, request.Explain, selected, &result, diagnostics)
		}
	}

	if request.QueryMode == QueryModeHistory || request.QueryMode == QueryModeConflict {
		relations, err := s.loadRelations(ctx, result.Observations, result.Memories)
		if err != nil {
			result.Warnings = append(result.Warnings, WarningRelationsUnavailable)
		} else {
			result.Relations = relations
		}
	}

	applyContextBudget(&result, request.MaxContextTokens, diagnostics)
	if diagnostics != nil {
		diagnostics.ReturnedItems = recallItemCount(result)
	}
	result.Warnings = dedupeWarnings(result.Warnings)
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	return result, nil
}

func normalizeLayers(input []Layer, specified bool, mode QueryMode) (map[Layer]bool, error) {
	result := map[Layer]bool{}
	if specified && len(input) == 0 {
		return result, nil
	}
	if len(input) == 0 {
		return mode.Layers(), nil
	}
	for _, layer := range input {
		if !layer.Known() {
			return nil, fmt.Errorf("%w: layer %q", ErrInvalidRequest, layer)
		}
		result[layer] = true
	}
	return result, nil
}

func normalizeDerivedStates(input []artifact.State) []artifact.State {
	if len(input) == 0 {
		return []artifact.State{artifact.StateActive}
	}
	seen := make(map[artifact.State]struct{}, len(input))
	result := make([]artifact.State, 0, len(input))
	for _, state := range input {
		if state != artifact.StateActive && state != artifact.StateStale {
			continue
		}
		if _, duplicate := seen[state]; duplicate {
			continue
		}
		seen[state] = struct{}{}
		result = append(result, state)
	}
	if len(result) == 0 {
		return []artifact.State{artifact.StateActive}
	}
	return result
}

func hasDerivedState(states []artifact.State, target artifact.State) bool {
	for _, state := range states {
		if state == target {
			return true
		}
	}
	return false
}

func (s *Service) searchMemoryLayer(ctx context.Context, query string, mode Mode, filters Filters,
	topK int, explain bool) ([]Item, []memory.ConflictSummary, []string, *SearchDiagnostics, error) {
	result, err := s.Search(ctx, Request{
		Query:   query,
		Mode:    mode,
		Filters: filters,
		TopK:    topK,
		Explain: explain,
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if result.Items == nil {
		result.Items = []Item{}
	}
	if result.Conflicts == nil {
		result.Conflicts = []memory.ConflictSummary{}
	}
	return result.Items, result.Conflicts, result.Warnings, result.Diagnostics, nil
}

func mergeSearchDiagnostics(target, source *SearchDiagnostics) {
	if target == nil || source == nil {
		return
	}
	target.KeywordCandidates += source.KeywordCandidates
	target.SemanticCandidates += source.SemanticCandidates
	target.SemanticExcluded += source.SemanticExcluded
	target.TermCandidates += source.TermCandidates
	target.KindFilterExcluded += source.KindFilterExcluded
	target.StateFilterExcluded += source.StateFilterExcluded
	target.RankingCandidates += source.RankingCandidates
	if source.SemanticDegradation != "" {
		target.SemanticDegradation = source.SemanticDegradation
	}
}

func (s *Service) searchObservationLayer(ctx context.Context, expression string, groups []Group,
	filters Filters, topK int, explain bool) ([]ObservationItem, error) {
	searcher, ok := s.backend.(ObservationSearcher)
	if !ok {
		return []ObservationItem{}, nil
	}
	hits, err := searcher.SearchObservations(ctx, ObservationQuery{
		Groups:     groups,
		Expression: expression,
		Filters:    filters,
		TopK:       topK,
	})
	if err != nil {
		return nil, err
	}
	items := make([]ObservationItem, 0, len(hits))
	for index, hit := range hits {
		matched := MatchedGroups(groups, "", hit.Observation.Content)
		if len(matched) == 0 {
			matched = GroupTexts(groups)
		}
		item := ObservationItem{
			Observation:  hit.Observation,
			Score:        hit.Score,
			Snippet:      Snippet(hit.Observation.Content, matched),
			MatchedTerms: matched,
			EvidenceRefs: hit.EvidenceRefs,
		}
		if item.EvidenceRefs == nil {
			item.EvidenceRefs = []EvidenceRef{}
		}
		if explain {
			item.Explanation = &Explanation{
				Ranking: "bm25",
				Keyword: &KeywordExplanation{Rank: index + 1, BM25: hit.Score},
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) searchDerivedLayer(ctx context.Context, expression string, groups []Group,
	layer Layer, scopeKey string, states []artifact.State, topK int, explain bool) ([]DerivedItem, error) {
	if s.artifacts == nil {
		return []DerivedItem{}, nil
	}
	hits, err := s.artifacts.Search(ctx, artifact.SearchQuery{
		Expression: expression,
		Terms:      GroupTexts(groups),
		ScopeKey:   scopeKey,
		Layers:     []artifact.Layer{layer.ArtifactLayer()},
		States:     states,
		Limit:      topK,
	})
	if err != nil {
		return nil, err
	}
	items := make([]DerivedItem, 0, len(hits))
	for index, hit := range hits {
		matched := MatchedGroups(groups, hit.Revision.Title, hit.Revision.Content)
		if len(matched) == 0 {
			matched = GroupTexts(groups)
		}
		dependencies := hit.Dependencies
		if dependencies == nil {
			dependencies = []artifact.DependencyRef{}
		}
		item := DerivedItem{
			DerivedMemory: hit.DerivedMemory,
			Revision:      hit.Revision,
			Score:         hit.Score,
			Snippet:       Snippet(hit.Revision.Content, matched),
			MatchedTerms:  matched,
			Dependencies:  dependencies,
		}
		if explain {
			item.Explanation = &Explanation{
				Ranking: "bm25",
				Keyword: &KeywordExplanation{Rank: index + 1, BM25: hit.Score},
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// fallbackRecall walks downward from a stale derived layer until it finds a
// usable lower-layer result. It never runs an aggregation model and never
// fabricates a current conclusion from stale text.
func (s *Service) fallbackRecall(ctx context.Context, from Layer, expression string, groups []Group,
	query string, mode Mode, filters Filters, topK int, explain bool, selected map[Layer]bool,
	result *RecallResult, diagnostics *SearchDiagnostics) {
	for attempts := 0; attempts < 4; attempts++ {
		lower, ok := from.Lower()
		if !ok {
			return
		}
		switch lower {
		case LayerObservations:
			if !selected[lower] {
				items, err := s.searchObservationLayer(ctx, expression, groups, filters, topK, explain)
				if err == nil {
					result.Observations = items
					selected[lower] = true
					if diagnostics != nil {
						diagnostics.ObservationCandidates += len(items)
					}
					if len(items) > 0 {
						return
					}
				}
			} else if len(result.Observations) > 0 {
				return
			}
			return
		case LayerMemories:
			if !selected[lower] {
				items, conflicts, warnings, _, err := s.searchMemoryLayer(ctx, query, mode, filters, topK, explain)
				if err == nil {
					result.Memories = items
					result.Conflicts = conflicts
					result.Warnings = append(result.Warnings, warnings...)
					selected[lower] = true
					if diagnostics != nil {
						diagnostics.MemoryCandidates += len(items)
					}
					if len(items) > 0 {
						return
					}
				}
			} else if len(result.Memories) > 0 {
				return
			}
			return
		default:
			if !selected[lower] {
				items, err := s.searchDerivedLayer(ctx, expression, groups, lower, filters.ScopeKey,
					[]artifact.State{artifact.StateActive}, topK, explain)
				if err == nil {
					result.SetDerived(lower, items)
					selected[lower] = true
					if diagnostics != nil {
						diagnostics.DerivedCandidates += len(items)
					}
					if len(items) > 0 {
						return
					}
				}
			} else if len(result.Derived(lower)) > 0 {
				return
			}
			stale, err := s.searchDerivedLayer(ctx, expression, groups, lower, filters.ScopeKey,
				[]artifact.State{artifact.StateStale}, 1, false)
			if err != nil || len(stale) == 0 {
				return
			}
			from = lower
		}
	}
}

func (s *Service) loadRelations(ctx context.Context, observations []ObservationItem,
	memories []Item) ([]governance.Relation, error) {
	reader, ok := s.backend.(RelationReader)
	if !ok {
		return []governance.Relation{}, nil
	}
	seen := make(map[string]struct{})
	result := []governance.Relation{}
	appendRelations := func(items []governance.Relation) {
		for _, item := range items {
			if item.ID == "" {
				continue
			}
			if _, duplicate := seen[item.ID]; duplicate {
				continue
			}
			seen[item.ID] = struct{}{}
			result = append(result, item)
		}
	}
	for _, item := range memories {
		relations, err := reader.ListGovernanceRelationsForEndpoint(ctx,
			governance.EndpointMemory, item.Memory.ID)
		if err != nil {
			return nil, err
		}
		appendRelations(relations)
	}
	for _, item := range observations {
		relations, err := reader.ListGovernanceRelationsForEndpoint(ctx,
			governance.EndpointObservation, item.Observation.ID)
		if err != nil {
			return nil, err
		}
		appendRelations(relations)
	}
	return result, nil
}

func emptyRecallResult() RecallResult {
	return RecallResult{
		Observations: []ObservationItem{},
		Memories:     []Item{},
		L1:           []DerivedItem{},
		L2:           []DerivedItem{},
		L3:           []DerivedItem{},
		L4:           []DerivedItem{},
		Relations:    []governance.Relation{},
		Conflicts:    []memory.ConflictSummary{},
		Warnings:     []string{},
	}
}

func recallItemCount(result RecallResult) int {
	total := len(result.Observations) + len(result.Memories)
	for _, layer := range []Layer{LayerL1, LayerL2, LayerL3, LayerL4} {
		total += len(result.Derived(layer))
	}
	return total
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
	// Keep raw evidence and the canonical view before higher abstractions.
	result.Observations = result.Observations[:fit(len(result.Observations), func(i int) int {
		return estimateTokens(result.Observations[i].Observation.Content)
	})]
	result.Memories = result.Memories[:fit(len(result.Memories), func(i int) int {
		return estimateTokens(result.Memories[i].Memory.Title) + estimateTokens(result.Memories[i].Memory.Content)
	})]
	for _, layer := range []Layer{LayerL1, LayerL2, LayerL3, LayerL4} {
		items := result.Derived(layer)
		items = items[:fit(len(items), func(i int) int {
			return estimateTokens(items[i].Revision.Title) + estimateTokens(items[i].Revision.Content)
		})]
		result.SetDerived(layer, items)
	}
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
