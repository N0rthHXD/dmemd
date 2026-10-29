package retrieval

import (
	"context"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

// populateFilterDiagnostics performs bounded, count-only relaxations of one
// filter at a time. It retains scope and update boundaries, never returns
// content, and cannot alter the actual result set. Temporal filtering is not
// part of the current contract; annotation filters will be added separately.
func (s *Service) populateFilterDiagnostics(ctx context.Context, groups []Group, filters Filters, diagnostics *SearchDiagnostics) {
	if s.backend == nil || len(groups) == 0 || diagnostics == nil {
		return
	}
	expression, err := Compile(groups)
	if err != nil {
		return
	}
	count := func(adjusted Filters) int {
		hits, err := s.backend.Search(ctx, Query{
			Groups:     groups,
			Expression: expression,
			Filters:    adjusted,
			TopK:       CandidateLimitMax,
		})
		if err != nil {
			return 0
		}
		return len(hits)
	}

	broad := filters
	broad.Kinds = nil
	broad.States = []memory.State{memory.StateActive, memory.StateArchived}
	broad.AssertionStatus = []memory.AssertionStatus{memory.AssertionAccepted, memory.AssertionPending}
	diagnostics.TermCandidates = count(broad)

	if len(filters.Kinds) > 0 {
		relaxed := filters
		relaxed.Kinds = nil
		diagnostics.KindFilterExcluded = nonNegative(count(relaxed) - diagnostics.KeywordCandidates)
	}
	relaxedStates := filters
	relaxedStates.States = []memory.State{memory.StateActive, memory.StateArchived}
	diagnostics.StateFilterExcluded = nonNegative(count(relaxedStates) - diagnostics.KeywordCandidates)
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
