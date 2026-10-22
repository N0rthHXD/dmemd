package retrieval

import (
	"context"
	"sort"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

type keywordOutcome struct {
	hits []Hit
	err  error
}

type semanticOutcome struct {
	hits    []VectorHit
	warning string
	err     error
}

func (s *Service) searchHybrid(ctx context.Context, groups []Group, request Request, filters Filters, topK int) (Result, error) {
	if s.encoder == nil {
		result, err := s.searchKeyword(ctx, groups, request, filters, topK, []string{WarningSemanticUnavailable}, false)
		return result, err
	}
	limit := candidateLimit(topK)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	keywordChannel := make(chan keywordOutcome, 1)
	semanticChannel := make(chan semanticOutcome, 1)

	go func() {
		expression, err := Compile(groups, normalizeMatch(request.Match))
		if err != nil {
			keywordChannel <- keywordOutcome{err: err}
			return
		}
		hits, err := s.backend.Search(runCtx, Query{
			Match:      normalizeMatch(request.Match),
			Groups:     groups,
			Expression: expression,
			Filters:    filters,
			TopK:       limit,
		})
		keywordChannel <- keywordOutcome{hits: hits, err: err}
	}()
	go func() {
		hits, warning, err := s.semanticCandidates(runCtx, request.Query, filters, limit)
		semanticChannel <- semanticOutcome{hits: hits, warning: warning, err: err}
	}()

	keywordResult := <-keywordChannel
	semanticResult := <-semanticChannel

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if keywordResult.err != nil {
		cancel()
		return Result{}, keywordResult.err
	}
	if semanticResult.err != nil {
		cancel()
		return Result{}, semanticResult.err
	}
	if semanticResult.warning != "" {
		// A degraded semantic path still uses the keyword path's original
		// scores, ordering and explanation; the already-fetched keyword
		// candidates are reused without a second query.
		items, err := s.assembleKeywordHits(ctx, keywordResult.hits, groups, filters, topK, request.Explain)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Items:     items,
			Retrieval: Retrieval{Mode: ModeKeyword},
			Warnings:  dedupeWarnings([]string{semanticResult.warning}),
		}, nil
	}
	return s.fuseHybrid(ctx, groups, keywordResult.hits, semanticResult.hits, filters, topK, request.Explain)
}

func (s *Service) semanticCandidates(ctx context.Context, query string, filters Filters, limit int) ([]VectorHit, string, error) {
	budgetCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	vector, err := s.encoder.EmbedQuery(budgetCtx, query)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if budgetCtx.Err() != nil {
			return nil, WarningSemanticTimeout, nil
		}
		return nil, WarningSemanticUnavailable, nil
	}
	if len(vector) != s.encoder.Dimensions() {
		return nil, WarningSemanticUnavailable, nil
	}

	hits, err := s.backend.SearchVectors(budgetCtx, VectorQuery{
		ProfileID:     s.encoder.ProfileID(),
		Dimensions:    s.encoder.Dimensions(),
		Query:         vector,
		Filters:       filters,
		Limit:         limit,
		MinSimilarity: s.encoder.MinSimilarity(),
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if budgetCtx.Err() != nil {
			return nil, WarningSemanticTimeout, nil
		}
		return nil, "", err
	}
	return hits, "", nil
}

func (s *Service) fuseHybrid(ctx context.Context, groups []Group, keywordHits []Hit, semanticHits []VectorHit, filters Filters, topK int, explain bool) (Result, error) {
	ids := make([]string, 0, len(keywordHits)+len(semanticHits))
	seenIDs := make(map[string]struct{}, len(keywordHits)+len(semanticHits))
	for _, hit := range keywordHits {
		if _, duplicate := seenIDs[hit.MemoryID]; duplicate {
			continue
		}
		seenIDs[hit.MemoryID] = struct{}{}
		ids = append(ids, hit.MemoryID)
	}
	for _, hit := range semanticHits {
		if _, duplicate := seenIDs[hit.MemoryID]; duplicate {
			continue
		}
		seenIDs[hit.MemoryID] = struct{}{}
		ids = append(ids, hit.MemoryID)
	}

	candidates, err := s.backend.LoadSearchCandidates(ctx, ids)
	if err != nil {
		return Result{}, err
	}

	ranked := make(map[string]*rankedCandidate, len(ids))
	keywordRank := 0
	for _, hit := range keywordHits {
		candidate, ok := candidates[hit.MemoryID]
		if !ok || !matchesFilters(candidate.Memory, filters) || candidate.Memory.State == memory.StateDeleted {
			continue
		}
		keywordRank++
		entry := ranked[hit.MemoryID]
		if entry == nil {
			entry = &rankedCandidate{candidate: candidate}
			ranked[hit.MemoryID] = entry
		}
		hitCopy := hit
		entry.keyword = &hitCopy
		entry.keywordRank = keywordRank
	}
	semanticRank := 0
	for _, hit := range semanticHits {
		candidate, ok := candidates[hit.MemoryID]
		if !ok || !matchesFilters(candidate.Memory, filters) || candidate.Memory.State == memory.StateDeleted {
			continue
		}
		if candidate.ContentHash == "" || candidate.ContentHash != hit.ContentHash {
			// The current snapshot changed after the vector scan. A stale
			// vector must not reinsert a result.
			continue
		}
		semanticRank++
		entry := ranked[hit.MemoryID]
		if entry == nil {
			entry = &rankedCandidate{candidate: candidate}
			ranked[hit.MemoryID] = entry
		}
		hitCopy := hit
		entry.semantic = &hitCopy
		entry.semanticRank = semanticRank
	}

	selected := make([]rankedCandidate, 0, len(ranked))
	for _, entry := range ranked {
		if entry.keywordRank > 0 {
			entry.score += reciprocalRank(entry.keywordRank)
		}
		if entry.semanticRank > 0 {
			entry.score += reciprocalRank(entry.semanticRank)
		}
		selected = append(selected, *entry)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].score != selected[j].score {
			return selected[i].score > selected[j].score
		}
		left, right := selected[i].candidate.Memory, selected[j].candidate.Memory
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return left.ID < right.ID
	})
	if len(selected) > topK {
		selected = selected[:topK]
	}

	items, err := s.buildHybridItems(ctx, selected, groups, explain)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Items:     items,
		Retrieval: Retrieval{Mode: ModeHybrid},
	}, nil
}

func reciprocalRank(rank int) float64 {
	return 1 / (RRFConstant + float64(rank))
}

func (s *Service) buildHybridItems(ctx context.Context, selected []rankedCandidate, groups []Group, explain bool) ([]Item, error) {
	keys := make([]VersionKey, 0, len(selected))
	for _, entry := range selected {
		keys = append(keys, VersionKey{MemoryID: entry.candidate.Memory.ID, Version: entry.candidate.Memory.Version})
	}
	assemblies, err := s.backend.LoadSearchAssemblies(ctx, keys)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(selected))
	for _, entry := range selected {
		key := VersionKey{MemoryID: entry.candidate.Memory.ID, Version: entry.candidate.Memory.Version}
		assembly, ok := assemblies[key]
		if !ok || !assembly.Current {
			continue
		}
		item := assembly.Memory
		matchedTerms := []string{}
		if entry.keywordRank > 0 {
			matchedTerms = MatchedGroups(groups, item.Title, item.Content)
			if len(matchedTerms) == 0 {
				matchedTerms = GroupTexts(groups)
			}
		}
		resultItem := Item{
			Memory:       item,
			Score:        entry.score,
			Snippet:      Snippet(item.Content, matchedTerms),
			MatchedTerms: matchedTerms,
			EvidenceRefs: EvidenceRefsFrom(assembly.Evidence),
		}
		if explain {
			explanation := &Explanation{Ranking: "rrf"}
			if entry.keyword != nil {
				explanation.Keyword = &KeywordExplanation{
					Rank: entry.keywordRank,
					BM25: entry.keyword.BM25,
				}
			}
			if entry.semantic != nil {
				explanation.Semantic = &SemanticExplanation{
					Rank:   entry.semanticRank,
					Cosine: entry.semantic.Cosine,
				}
			}
			resultItem.Explanation = explanation
		}
		items = append(items, resultItem)
	}
	return items, nil
}
