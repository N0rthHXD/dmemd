package retrieval

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
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
	artifacts    ArtifactSearcher
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
		if encoder == nil {
			service.encoder = nil
			return
		}
		value := reflect.ValueOf(encoder)
		if value.Kind() == reflect.Ptr && value.IsNil() {
			service.encoder = nil
			return
		}
		service.encoder = encoder
	}
}

// WithArtifacts installs the derived-layer search capability. A nil value
// preserves atomic-only assembly, while the unified recall still returns empty
// derived partitions as required by the contract.
func WithArtifacts(searcher ArtifactSearcher) Option {
	return func(service *Service) {
		if searcher != nil {
			service.artifacts = searcher
		}
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

// Search runs one Top-K query. Input without effective terms (empty,
// punctuation-only or stopword-only) returns empty without querying any backend.
func (s *Service) Search(ctx context.Context, request Request) (Result, error) {
	mode, err := normalizeMode(request.Mode)
	if err != nil {
		return Result{}, err
	}
	topK, err := normalizeTopK(request.TopK)
	if err != nil {
		return Result{}, err
	}
	groups := queryGroups(request.Query)
	filters, filtersOK := sanitizeFilters(request.Filters)
	if filters.AsOf == nil {
		asOf := time.Now().UTC()
		filters.AsOf = &asOf
	}
	if len(groups) == 0 || !filtersOK {
		return Result{
			Items:     []Item{},
			Retrieval: Retrieval{Mode: mode},
			Conflicts: []memory.ConflictSummary{},
		}, nil
	}
	var result Result
	if mode == ModeHybrid {
		result, err = s.searchHybrid(ctx, groups, request, filters, topK)
	} else {
		result, err = s.searchKeyword(ctx, groups, request, filters, topK, nil, true)
	}
	if err != nil {
		return Result{}, err
	}
	return s.decorateConflicts(ctx, result, groups, filters)
}

// RelatedContext selects bounded consistency context for one candidate fact.
// It deliberately bypasses the public current-time filter so historical,
// pending and archived assertions can be judged.
func (s *Service) RelatedContext(ctx context.Context, text string, limit int) (ContextResult, error) {
	if limit <= 0 {
		limit = DefaultRelatedLimit
	}
	groups := queryGroups(text)
	if len(groups) > MaxRelatedQueryGroups {
		groups = groups[:MaxRelatedQueryGroups]
	}
	if len(groups) == 0 {
		return ContextResult{Memories: []memory.Memory{}, Complete: true}, nil
	}
	contextFilters := Filters{
		States:          []memory.State{memory.StateActive, memory.StateArchived},
		AssertionStatus: []memory.AssertionStatus{memory.AssertionAccepted, memory.AssertionPending},
	}
	result, err := s.searchKeyword(ctx, groups, Request{}, contextFilters, limit+1, nil, false)
	if err != nil {
		return ContextResult{}, err
	}
	items := make([]memory.Memory, 0, len(result.Items))
	seen := make(map[string]struct{}, len(result.Items))
	for _, item := range result.Items {
		items = append(items, item.Memory)
		seen[item.Memory.ID] = struct{}{}
	}
	warnings := []string{}
	if s.encoder != nil {
		// Semantic recall only supplements keyword context. A failure keeps the
		// keyword context and records a stable degradation warning.
		hits, warning, err := s.semanticCandidates(ctx, text, contextFilters, limit)
		if err != nil {
			return ContextResult{}, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if len(hits) > 0 {
			ids := make([]string, 0, len(hits))
			for _, hit := range hits {
				ids = append(ids, hit.MemoryID)
			}
			candidates, err := s.backend.LoadSearchCandidates(ctx, ids)
			if err != nil {
				return ContextResult{}, err
			}
			for _, hit := range hits {
				candidate, ok := candidates[hit.MemoryID]
				if !ok || candidate.Memory.State == memory.StateDeleted {
					continue
				}
				if _, duplicate := seen[candidate.Memory.ID]; duplicate {
					continue
				}
				seen[candidate.Memory.ID] = struct{}{}
				items = append(items, candidate.Memory)
			}
		}
	}
	complete := len(items) <= limit
	if len(items) > limit {
		items = items[:limit]
	}
	return ContextResult{Memories: items, Complete: complete, Warnings: warnings}, nil
}

// FactTimeline returns the bounded timeline of one fact group.
func (s *Service) FactTimeline(ctx context.Context, groupID string, limit int) (ContextResult, error) {
	if limit <= 0 {
		limit = 50
	}
	items, err := s.backend.FactTimeline(ctx, groupID, limit)
	if err != nil {
		return ContextResult{}, err
	}
	complete := len(items) <= limit
	if len(items) > limit {
		items = items[:limit]
	}
	return ContextResult{Memories: items, Complete: complete}, nil
}

// OpenConflictsForMemories exposes read-pool conflict discovery to ingest.
func (s *Service) OpenConflictsForMemories(ctx context.Context, memoryIDs []string) ([]memory.Conflict, error) {
	return s.backend.OpenConflictsForMemories(ctx, memoryIDs)
}

// MemoriesByIDs loads exact current snapshots for conflict-member context.
func (s *Service) MemoriesByIDs(ctx context.Context, ids []string) ([]memory.Memory, error) {
	candidates, err := s.backend.LoadSearchCandidates(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]memory.Memory, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.Memory)
	}
	return result, nil
}

// CatalogGeneration returns the semantic catalog version used for optimistic
// checks around a model call.
func (s *Service) CatalogGeneration(ctx context.Context) (int64, error) {
	return s.backend.CatalogGeneration(ctx)
}

// decorateConflicts runs an independent conflict discovery path over accepted
// and pending members, attaches conflict references to returned items and emits
// stable warnings. Pending text retains its assertion status in ordinary items.
func (s *Service) decorateConflicts(ctx context.Context, result Result, groups []Group, filters Filters) (Result, error) {
	if result.Conflicts == nil {
		result.Conflicts = []memory.ConflictSummary{}
	}
	for index := range result.Items {
		if result.Items[index].Memory.AssertionStatus == memory.AssertionPending {
			result.Warnings = dedupeWarnings(append(result.Warnings, WarningFactConflict))
		}
		if result.Items[index].ConflictRefs == nil {
			result.Items[index].ConflictRefs = []memory.ConflictRef{}
		}
	}
	if filters.AsOf == nil {
		return result, nil
	}
	asOf := *filters.AsOf
	expression, err := Compile(groups)
	if err != nil {
		return Result{}, err
	}
	discoveryFilters := filters
	discoveryFilters.AssertionStatus = []memory.AssertionStatus{
		memory.AssertionAccepted,
		memory.AssertionPending,
	}
	hits, err := s.backend.Search(ctx, Query{
		Groups:     groups,
		Expression: expression,
		Filters:    discoveryFilters,
		TopK:       CandidateLimitMax,
	})
	if err != nil {
		return Result{}, err
	}
	ids := make([]string, 0, len(hits)+len(result.Items))
	for _, hit := range hits {
		ids = append(ids, hit.MemoryID)
	}
	for _, item := range result.Items {
		ids = append(ids, item.Memory.ID)
	}
	conflicts, err := s.backend.OpenConflictsForMemories(ctx, ids)
	if err != nil {
		return Result{}, err
	}
	relevant, candidateByID, truncated, err := s.relevantConflicts(ctx, conflicts, asOf)
	if err != nil {
		return Result{}, err
	}
	if len(relevant) == 0 {
		return result, nil
	}
	byMemory := make(map[string][]memory.ConflictRef)
	summaries := make([]memory.ConflictSummary, 0, len(relevant))
	for _, conflict := range relevant {
		hasAccepted := false
		memberIDs := make([]string, 0, len(conflict.Members))
		for _, member := range conflict.Members {
			memberIDs = append(memberIDs, member.MemoryID)
			if candidate, ok := candidateByID[member.MemoryID]; ok && candidate.Memory.AssertionStatus == memory.AssertionAccepted {
				hasAccepted = true
			}
			byMemory[member.MemoryID] = append(byMemory[member.MemoryID], memory.ConflictRef{
				ID:         conflict.ID,
				ReasonCode: conflict.ReasonCode,
			})
		}
		summaries = append(summaries, memory.ConflictSummary{
			ID:          conflict.ID,
			ReasonCode:  conflict.ReasonCode,
			MemberIDs:   memberIDs,
			HasAccepted: hasAccepted,
			FactGroupID: conflict.FactGroupID,
		})
	}
	for index := range result.Items {
		if refs, ok := byMemory[result.Items[index].Memory.ID]; ok {
			result.Items[index].ConflictRefs = refs
		}
	}
	result.Conflicts = summaries
	result.Warnings = dedupeWarnings(append(result.Warnings, WarningFactConflict))
	if truncated {
		result.Warnings = dedupeWarnings(append(result.Warnings, WarningConflictResultsTruncated))
	}
	return result, nil
}

func (s *Service) relevantConflicts(ctx context.Context, conflicts []memory.Conflict, asOf time.Time) ([]memory.Conflict, map[string]Candidate, bool, error) {
	if len(conflicts) == 0 {
		return nil, nil, false, nil
	}
	ids := make([]string, 0, len(conflicts)*2)
	for _, conflict := range conflicts {
		for _, member := range conflict.Members {
			ids = append(ids, member.MemoryID)
		}
	}
	candidates, err := s.backend.LoadSearchCandidates(ctx, ids)
	if err != nil {
		return nil, nil, false, err
	}

	relevant := make([]memory.Conflict, 0, len(conflicts))
	for _, conflict := range conflicts {
		relates := false
		for _, member := range conflict.Members {
			candidate, ok := candidates[member.MemoryID]
			if !ok {
				continue
			}
			item := candidate.Memory
			if item.AssertionStatus != memory.AssertionAccepted && item.AssertionStatus != memory.AssertionPending {
				continue
			}
			if item.State != memory.StateDeleted && memory.Contains(item.ValidFrom, item.ValidTo, asOf) {
				relates = true
				break
			}
		}
		if relates {
			relevant = append(relevant, conflict)
		}
	}
	truncated := len(relevant) > 20
	if truncated {
		relevant = relevant[:20]
	}
	return relevant, candidates, truncated, nil
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
	if len(groups) == 0 {
		return result, nil
	}
	expression, err := Compile(groups)
	if err != nil {
		return Result{}, err
	}
	limit := topK
	if optimize {
		// Reranking needs a wider candidate pool. top_k still bounds the
		// response, not the recall range.
		limit = candidateLimit(topK)
	}
	hits, err := s.backend.Search(ctx, Query{
		Groups:     groups,
		Expression: expression,
		Filters:    filters,
		TopK:       limit,
	})
	if err != nil {
		return Result{}, err
	}
	var assembled []Item
	if optimize {
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
	}
	sort.SliceStable(selected, func(i, j int) bool {
		li, lj := selected[i].candidate.Memory.AssertionStatus, selected[j].candidate.Memory.AssertionStatus
		if li != lj {
			return li == memory.AssertionAccepted
		}
		return selected[i].keywordRank < selected[j].keywordRank
	})
	if len(selected) > limit {
		selected = selected[:limit]
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
		li, lj := selected[i].candidate.Memory.AssertionStatus, selected[j].candidate.Memory.AssertionStatus
		if li != lj {
			return li == memory.AssertionAccepted
		}
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
	// Confirmed assertions rank ahead of pending alternatives. Rejected
	// assertions are never recalled.
	output.AssertionStatus = []memory.AssertionStatus{memory.AssertionAccepted, memory.AssertionPending}

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
	if len(filters.AssertionStatus) > 0 && !containsAssertion(filters.AssertionStatus, item.AssertionStatus) {
		return false
	}
	if filters.ScopeKey != "" && item.ScopeKey != filters.ScopeKey && item.ScopeKey != "user" {
		return false
	}
	if filters.AsOf != nil && !memory.Contains(item.ValidFrom, item.ValidTo, *filters.AsOf) {
		return false
	}
	return true
}

func containsAssertion(statuses []memory.AssertionStatus, status memory.AssertionStatus) bool {
	for _, candidate := range statuses {
		if candidate == status {
			return true
		}
	}
	return false
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
