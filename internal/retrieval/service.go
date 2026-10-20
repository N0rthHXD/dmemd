package retrieval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// Service implements keyword search, background related-memory selection and
// index maintenance over the same normalization rules used by the write path.
type Service struct {
	backend   Backend
	documents memory.DocumentBuilder
	logger    *slog.Logger
}

// Option customizes a Service. It is primarily useful for runtime logging.
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

// NewService assembles the retrieval service. The backend owns read and index
// maintenance capabilities; the document builder must be the same production
// builder injected into the memory write path.
func NewService(backend Backend, documents memory.DocumentBuilder, options ...Option) *Service {
	service := &Service{
		backend:   backend,
		documents: documents,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

var (
	_ SearchService   = (*Service)(nil)
	_ RelatedService  = (*Service)(nil)
	_ IndexMaintainer = (*Service)(nil)
)

// Search runs one Top-K keyword query. Empty or punctuation-only input is a
// valid empty result, not an error.
func (s *Service) Search(ctx context.Context, request Request) (Result, error) {
	return s.searchGroups(ctx, dedupeGroups(Groups(request.Query)), request)
}

// RelatedActive selects at most ten active memories for background extraction.
// It uses the same normalization and BM25 ordering as public search, match=any,
// and never returns archived or deleted memories.
func (s *Service) RelatedActive(ctx context.Context, text string, limit int) ([]memory.Memory, error) {
	groups := dedupeGroups(Groups(text))
	if len(groups) > MaxRelatedQueryGroups {
		groups = groups[:MaxRelatedQueryGroups]
	}
	if limit <= 0 || limit > DefaultRelatedLimit {
		limit = DefaultRelatedLimit
	}
	result, err := s.searchGroups(ctx, groups, Request{
		Match:   MatchAny,
		Filters: Filters{States: []memory.State{memory.StateActive}},
		TopK:    limit,
	})
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
// rebuilds FTS. The store commits all rows or rolls back; a failed rebuild is
// never reported as success.
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

func (s *Service) searchGroups(ctx context.Context, groups []Group, request Request) (Result, error) {
	result := Result{Items: []Item{}}
	if len(groups) == 0 {
		return result, nil
	}
	filters, ok := sanitizeFilters(request.Filters)
	if !ok {
		return result, nil
	}
	topK := request.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}
	match := normalizeMatch(request.Match)
	expression, err := Compile(groups, match)
	if err != nil {
		return Result{}, err
	}
	hits, err := s.backend.Search(ctx, Query{
		Match:      match,
		Groups:     groups,
		Expression: expression,
		Filters:    filters,
		TopK:       topK,
	})
	if err != nil {
		return Result{}, err
	}

	for _, hit := range hits {
		item, err := s.backend.GetMemory(ctx, hit.MemoryID)
		if errors.Is(err, memory.ErrNotFound) {
			// The index and snapshot changed between the query and assembly.
			// A one-shot search simply omits the stale hit.
			continue
		}
		if err != nil {
			return Result{}, fmt.Errorf("读取检索结果 %s: %w", hit.MemoryID, err)
		}
		if !matchesFilters(item, filters) {
			continue
		}
		evidence, err := s.backend.ListRevisionEvidence(ctx, item.ID, item.Version)
		if err != nil {
			return Result{}, fmt.Errorf("读取检索结果证据 %s@%d: %w", item.ID, item.Version, err)
		}
		matchedTerms := MatchedGroups(groups, item.Title, item.Content)
		if len(matchedTerms) == 0 {
			// FTS5 unicode61 applies its own diacritic folding, so a hit can
			// exist without an exact token match in the original text. Keep
			// matched_terms non-empty and query-readable for such hits.
			matchedTerms = GroupTexts(groups)
		}
		resultItem := Item{
			Memory:       item,
			Score:        -hit.BM25,
			Snippet:      Snippet(item.Content, matchedTerms),
			MatchedTerms: matchedTerms,
			EvidenceRefs: EvidenceRefsFrom(evidence),
		}
		if request.Explain {
			resultItem.Explanation = &Explanation{
				Ranking:      "bm25",
				FieldWeights: [2]int{FieldWeightTitle, FieldWeightContent},
			}
		}
		result.Items = append(result.Items, resultItem)
	}
	return result, nil
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
