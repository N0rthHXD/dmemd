package retrieval

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// Service implements keyword search, hybrid search, background related-memory
// selection and index maintenance over the same normalization rules used by the
// write path.
type Service struct {
	backend      Backend
	documents    memory.DocumentBuilder
	logger       *slog.Logger
	encoder      QueryEncoder
	queryTimeout time.Duration
}

// Option customizes a Service. It is primarily useful for runtime logging and
// semantic assembly.
type Option func(*Service)

// WithLogger sets the structured logger used for index maintenance. The logger
// never receives memory title or content.
func WithLogger(logger *slog.Logger) Option {
	return func(service *Service) {
		if logger != nil {
			service.logger = logger
		}
	}
}

// WithSemantic installs the online query encoder. A nil encoder keeps the
// service keyword-only while preserving the hybrid degradation path.
func WithSemantic(encoder QueryEncoder) Option {
	return func(service *Service) {
		service.encoder = encoder
	}
}

// NewService assembles the retrieval service. The backend owns read and index
// maintenance capabilities; the document builder must be the same production
// builder injected into the memory write path.
func NewService(backend Backend, documents memory.DocumentBuilder, options ...Option) *Service {
	service := &Service{
		backend:      backend,
		documents:    documents,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		queryTimeout: DefaultQueryBudget,
	}
	for _, option := range options {
		option(service)
	}
	if service.queryTimeout <= 0 {
		service.queryTimeout = DefaultQueryBudget
	}
	return service
}

var (
	_ SearchService   = (*Service)(nil)
	_ RelatedService  = (*Service)(nil)
	_ IndexMaintainer = (*Service)(nil)
)

// Search runs one Top-K query. Empty or punctuation-only input is a valid empty
// result and never calls the model.
func (s *Service) Search(ctx context.Context, request Request) (Result, error) {
	mode, err := normalizeMode(request.Mode)
	if err != nil {
		return Result{}, err
	}
	topK, err := normalizeTopK(request.TopK)
	if err != nil {
		return Result{}, err
	}
	groups := dedupeGroups(Groups(request.Query))
	filters, filtersOK := sanitizeFilters(request.Filters)
	if len(groups) == 0 || !filtersOK {
		return Result{
			Items:     []Item{},
			Retrieval: Retrieval{Mode: mode},
		}, nil
	}
	match := normalizeMatch(request.Match)
	if mode == ModeKeyword && match == MatchAny {
		groups = selectQueryTerms(groups)
	}
	if mode == ModeHybrid {
		return s.searchHybrid(ctx, groups, request, filters, topK)
	}
	return s.searchKeyword(ctx, groups, request, filters, topK, nil, true)
}

// RelatedActive selects at most ten active memories for background extraction.
// It always uses the keyword path, match=any, and never returns archived or
// deleted memories.
func (s *Service) RelatedActive(ctx context.Context, text string, limit int) ([]memory.Memory, error) {
	groups := dedupeGroups(Groups(text))
	if len(groups) > MaxRelatedQueryGroups {
		groups = groups[:MaxRelatedQueryGroups]
	}
	if limit <= 0 || limit > DefaultRelatedLimit {
		limit = DefaultRelatedLimit
	}
	if len(groups) == 0 {
		return []memory.Memory{}, nil
	}
	result, err := s.searchKeyword(ctx, groups, Request{Match: MatchAny}, Filters{
		States: []memory.State{memory.StateActive},
	}, limit, []string{}, false)
	if err != nil {
		return nil, err
	}
	items := make([]memory.Memory, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, item.Memory)
	}
	return items, nil
}

// RebuildIndex regenerates search documents from current business snapshots and
// rebuilds FTS. With semantic retrieval enabled, unchanged documents reuse
// valid vectors and missing or mismatched vectors receive durable background
// tasks. The store commits all rows or rolls back; a failed rebuild is never
// reported as success.
func (s *Service) RebuildIndex(ctx context.Context) (RebuildResult, error) {
	started := time.Now()
	if s.documents == nil {
		err := ErrDocumentBuilderMissing
		s.logger.Warn("search index rebuild failed", "error", err)
		return RebuildResult{Duration: time.Since(started)}, err
	}
	indexed, err := s.backend.RebuildSearchDocuments(ctx, s.documents)
	if err != nil {
		s.logger.Warn("search index rebuild failed",
			"error", err,
			"duration_ms", time.Since(started).Milliseconds(),
		)
		return RebuildResult{Duration: time.Since(started)}, err
	}
	result := RebuildResult{Indexed: indexed, Duration: time.Since(started)}
	s.logger.Info("search index rebuilt",
		"indexed", result.Indexed,
		"duration_ms", result.Duration.Milliseconds(),
	)
	return result, nil
}

func (s *Service) searchKeyword(ctx context.Context, groups []Group, request Request, filters Filters, topK int, warnings []string, optimize bool) (Result, error) {
	result := Result{
		Items:     []Item{},
		Retrieval: Retrieval{Mode: ModeKeyword},
		Warnings:  dedupeWarnings(warnings),
	}
	match := normalizeMatch(request.Match)
	expression, err := Compile(groups, match)
	if err != nil {
		return Result{}, err
	}
	limit := topK
	if optimize && match == MatchAny {
		// Reranking needs a wider candidate pool. top_k still bounds the
		// response, not the recall range.
		limit = candidateLimit(topK)
	}
	hits, err := s.backend.Search(ctx, Query{
		Match:      match,
		Groups:     groups,
		Expression: expression,
		Filters:    filters,
		TopK:       limit,
	})
	if err != nil {
		return Result{}, err
	}
	var assembled []Item
	if optimize && match == MatchAny {
		assembled, err = s.assembleCoverageRerankedKeywordHits(ctx, hits, groups, filters, topK, request.Explain)
	} else {
		assembled, err = s.assembleKeywordHits(ctx, hits, groups, filters, topK, request.Explain)
	}
	if err != nil {
		return Result{}, err
	}
	result.Items = assembled
	return result, nil
}

type rankedCandidate struct {
	candidate    Candidate
	keyword      *Hit
	semantic     *VectorHit
	keywordRank  int
	semanticRank int
	score        float64
}

func (s *Service) assembleKeywordHits(ctx context.Context, hits []Hit, groups []Group, filters Filters, limit int, explain bool) ([]Item, error) {
	if len(hits) == 0 {
		return []Item{}, nil
	}
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.MemoryID)
	}
	candidates, err := s.backend.LoadSearchCandidates(ctx, ids)
	if err != nil {
		return nil, err
	}
	selected := make([]rankedCandidate, 0, limit)
	for _, hit := range hits {
		candidate, ok := candidates[hit.MemoryID]
		if !ok || !matchesFilters(candidate.Memory, filters) || candidate.Memory.State == memory.StateDeleted {
			continue
		}
		position := len(selected) + 1
		entry := rankedCandidate{candidate: candidate, keyword: &hit, keywordRank: position, score: -hit.BM25}
		selected = append(selected, entry)
		if len(selected) >= limit {
			break
		}
	}
	return s.buildKeywordItems(ctx, selected, groups, explain)
}

// keywordCoverageWeight bounds how much term coverage can move a candidate
// relative to its BM25 rank. Coverage is in [0,1]; the fixed weight keeps the
// fusion stable and avoids adding raw BM25 scores to a ratio.
const keywordCoverageWeight = 0.005

// assembleCoverageRerankedKeywordHits fuses each candidate's BM25 rank with
// its actual term coverage. It deliberately keeps every recalled candidate:
// a hard minimum-coverage filter hurts natural-language recall, so coverage
// may only move candidates within the BM25 candidate set. Candidates remain in
// BM25 order as the stable fallback for equal scores.
func (s *Service) assembleCoverageRerankedKeywordHits(ctx context.Context, hits []Hit, groups []Group, filters Filters, topK int, explain bool) ([]Item, error) {
	if len(hits) == 0 || len(groups) == 0 {
		return []Item{}, nil
	}
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.MemoryID)
	}
	candidates, err := s.backend.LoadSearchCandidates(ctx, ids)
	if err != nil {
		return nil, err
	}
	selected := make([]rankedCandidate, 0, len(hits))
	for index, hit := range hits {
		candidate, ok := candidates[hit.MemoryID]
		if !ok || !matchesFilters(candidate.Memory, filters) || candidate.Memory.State == memory.StateDeleted {
			continue
		}
		hitCopy := hit
		coverage := float64(matchedGroupCount(groups, candidate.Memory.Title, candidate.Memory.Content)) / float64(len(groups))
		selected = append(selected, rankedCandidate{
			candidate:   candidate,
			keyword:     &hitCopy,
			keywordRank: index + 1,
			score:       reciprocalRank(index+1) + keywordCoverageWeight*coverage,
		})
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].score != selected[j].score {
			return selected[i].score > selected[j].score
		}
		return selected[i].keywordRank < selected[j].keywordRank
	})
	if len(selected) > topK {
		selected = selected[:topK]
	}
	for i := range selected {
		selected[i].keywordRank = i + 1
	}
	return s.buildKeywordItems(ctx, selected, groups, explain)
}

func (s *Service) buildKeywordItems(ctx context.Context, selected []rankedCandidate, groups []Group, explain bool) ([]Item, error) {
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
			// A concurrent revision changed the snapshot after selection. The
			// contract allows returning fewer than Top-K rather than pairing a
			// stale snapshot with newer evidence.
			continue
		}
		item := assembly.Memory
		matchedTerms := MatchedGroups(groups, item.Title, item.Content)
		if len(matchedTerms) == 0 {
			// FTS5 unicode61 applies diacritic folding, so a hit can exist
			// without an exact token match. Keep the keyword contract readable.
			matchedTerms = GroupTexts(groups)
		}
		resultItem := Item{
			Memory:       item,
			Score:        entry.score,
			Snippet:      Snippet(item.Content, matchedTerms),
			MatchedTerms: matchedTerms,
			EvidenceRefs: EvidenceRefsFrom(assembly.Evidence),
		}
		if explain {
			explanation := &Explanation{
				Ranking:      "bm25",
				FieldWeights: [2]int{FieldWeightTitle, FieldWeightContent},
			}
			if entry.keyword != nil {
				explanation.Keyword = &KeywordExplanation{
					Rank: entry.keywordRank,
					BM25: entry.keyword.BM25,
				}
			}
			resultItem.Explanation = explanation
		}
		items = append(items, resultItem)
	}
	return items, nil
}

func normalizeMode(mode Mode) (Mode, error) {
	switch mode {
	case "", ModeKeyword:
		return ModeKeyword, nil
	case ModeHybrid:
		return ModeHybrid, nil
	default:
		return "", fmt.Errorf("%w: mode %q", ErrInvalidRequest, mode)
	}
}

func normalizeTopK(topK int) (int, error) {
	if topK <= 0 {
		return DefaultTopK, nil
	}
	if topK > MaxTopK {
		return 0, fmt.Errorf("%w: top_k %d 超过上限 %d", ErrInvalidRequest, topK, MaxTopK)
	}
	return topK, nil
}

func candidateLimit(topK int) int {
	limit := 5 * topK
	if limit < CandidateLimitMin {
		return CandidateLimitMin
	}
	if limit > CandidateLimitMax {
		return CandidateLimitMax
	}
	return limit
}

func dedupeWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(warnings))
	result := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		if warning == "" {
			continue
		}
		if _, duplicate := seen[warning]; duplicate {
			continue
		}
		seen[warning] = struct{}{}
		result = append(result, warning)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func sanitizeFilters(input Filters) (Filters, bool) {
	output := input
	output.Kinds = nil
	output.States = nil

	if len(input.Kinds) > 0 {
		seen := make(map[memory.Kind]struct{}, len(input.Kinds))
		for _, kind := range input.Kinds {
			if !validKind(kind) {
				continue
			}
			if _, duplicate := seen[kind]; duplicate {
				continue
			}
			seen[kind] = struct{}{}
			output.Kinds = append(output.Kinds, kind)
		}
		if len(output.Kinds) == 0 {
			return Filters{}, false
		}
	}

	if len(input.States) == 0 {
		output.States = []memory.State{memory.StateActive}
	} else {
		seen := make(map[memory.State]struct{}, len(input.States))
		for _, state := range input.States {
			if state != memory.StateActive && state != memory.StateArchived {
				continue
			}
			if _, duplicate := seen[state]; duplicate {
				continue
			}
			seen[state] = struct{}{}
			output.States = append(output.States, state)
		}
		if len(output.States) == 0 {
			return Filters{}, false
		}
	}
	return output, true
}

func matchesFilters(item memory.Memory, filters Filters) bool {
	if len(filters.States) > 0 && !containsState(filters.States, item.State) {
		return false
	}
	if len(filters.Kinds) > 0 && !containsKind(filters.Kinds, item.Kind) {
		return false
	}
	if filters.CreatedByApp != "" && item.CreatedByApp != filters.CreatedByApp {
		return false
	}
	if filters.UpdatedAfter != nil && item.UpdatedAt.Before(*filters.UpdatedAfter) {
		return false
	}
	if filters.UpdatedBefore != nil && item.UpdatedAt.After(*filters.UpdatedBefore) {
		return false
	}
	return true
}

func validKind(kind memory.Kind) bool {
	switch kind {
	case memory.KindFact, memory.KindPreference, memory.KindEvent, memory.KindProcedure:
		return true
	default:
		return false
	}
}

func containsState(states []memory.State, state memory.State) bool {
	for _, candidate := range states {
		if candidate == state {
			return true
		}
	}
	return false
}

func containsKind(kinds []memory.Kind, kind memory.Kind) bool {
	for _, candidate := range kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func dedupeGroups(groups []Group) []Group {
	seen := make(map[string]struct{}, len(groups))
	result := make([]Group, 0, len(groups))
	for _, group := range groups {
		if len(group.Tokens) == 0 {
			continue
		}
		if _, duplicate := seen[group.Text]; duplicate {
			continue
		}
		seen[group.Text] = struct{}{}
		result = append(result, group)
	}
	return result
}

// EvidenceRefsFrom is a small helper for tests and future callers that already
// hold a current revision's evidence.
func EvidenceRefsFrom(evidence []source.Evidence) []EvidenceRef {
	refs := make([]EvidenceRef, 0, len(evidence))
	for _, entry := range evidence {
		refs = append(refs, EvidenceRef{ID: entry.ID, SourceID: entry.SourceID})
	}
	return refs
}
