package governance

import (
	"sort"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// RuleReason is a fixed, content-free diagnostic code for a deterministic
// governance match. It is persisted only in logs and task diagnostics; model or
// source text never becomes a reason code.
type RuleReason string

const (
	RuleReasonSameSourceSpan RuleReason = "same_source_span"
	RuleReasonSameBody       RuleReason = "same_body"
	RuleReasonDisjointScope  RuleReason = "disjoint_scope"
	RuleReasonInactiveTarget RuleReason = "inactive_target"
	RuleReasonNoEvidence     RuleReason = "no_evidence"
	RuleReasonNoCandidates   RuleReason = "no_candidates"
	RuleReasonIdempotent     RuleReason = "idempotent"
	RuleReasonModelRequired  RuleReason = "model_required"
)

// RuleObservation is one earlier observation from the same source together with
// the deterministic identity material needed to compare spans and the canonical
// views that already reference it.
type RuleObservation struct {
	Observation observation.Observation
	SegmentIDs  []string
	MemoryIDs   []string
}

// RuleCandidate is one bounded same-scope canonical memory offered to the rule
// stage. Ref is only used when a rule match must fall through to the model.
type RuleCandidate struct {
	Ref            string
	Memory         memory.Memory
	ObservationIDs []string
}

// RuleInput contains only deterministic identity facts. The rule stage never
// calls a model and never rewrites content.
type RuleInput struct {
	Observation       observation.Observation
	Evidence          []source.Evidence
	ExistingMemoryIDs []string
	PriorObservations []RuleObservation
	Candidates        []RuleCandidate
	MemoriesByID      map[string]memory.Memory
}

// RuleDecision is one deterministic outcome. Skip means the observation has
// already been governed and the task can complete without materializing again.
type RuleDecision struct {
	Matched  bool
	Skip     bool
	Kind     RelationKind
	TargetID string
	Reason   RuleReason
}

// MatchRules applies the deterministic part of observation governance in fixed
// priority order. It intentionally handles only identity, exact body equality
// and explicit scope separation. Semantics such as support, enrichment,
// supersession and contradiction remain model questions.
func MatchRules(input RuleInput) RuleDecision {
	current := input.Observation

	// Rule 5: the same observation already participated in a committed
	// governance transaction. Relations and materialization commit atomically,
	// so any reference means this task is an idempotent retry.
	if len(input.ExistingMemoryIDs) > 0 {
		return RuleDecision{Matched: true, Skip: true, Reason: RuleReasonIdempotent}
	}

	currentSpans := evidenceSegmentIDs(input.Evidence)
	currentBody := normalizeBody(current.Content)
	inactive := func(id string) bool {
		item, ok := input.MemoriesByID[id]
		if !ok {
			return false
		}
		return item.State == memory.StateDeleted || item.AssertionStatus == memory.AssertionRejected
	}
	unresolved := func(reason RuleReason) RuleDecision {
		return RuleDecision{Matched: true, Kind: RelationUnresolved, Reason: reason}
	}

	// Rule 1: the same source expressed the same span set and body before.
	// This still detects a duplicate after the canonical view was enriched and
	// no longer equals the original observation body.
	if len(currentSpans) > 0 && currentBody != "" {
		for _, prior := range input.PriorObservations {
			if prior.Observation.ID == "" || prior.Observation.ID == current.ID {
				continue
			}
			if prior.Observation.Status == observation.StatusDeleted ||
				prior.Observation.Status == observation.StatusRetracted {
				continue
			}
			if normalizeBody(prior.Observation.Content) != currentBody {
				continue
			}
			if !sameSpanSet(currentSpans, prior.SegmentIDs) {
				continue
			}
			target := ""
			inactiveTarget := false
			for _, memoryID := range prior.MemoryIDs {
				if memoryID == "" {
					continue
				}
				item, ok := input.MemoriesByID[memoryID]
				if !ok {
					continue
				}
				if item.State == memory.StateDeleted || item.AssertionStatus == memory.AssertionRejected {
					inactiveTarget = true
					continue
				}
				target = memoryID
				break
			}
			if target == "" {
				if candidate := candidateForObservation(input.Candidates, prior.Observation.ID); candidate != nil {
					if candidate.Memory.State == memory.StateDeleted ||
						candidate.Memory.AssertionStatus == memory.AssertionRejected {
						inactiveTarget = true
					} else {
						target = candidate.Memory.ID
					}
				}
			}
			if target == "" {
				if inactiveTarget {
					return unresolved(RuleReasonInactiveTarget)
				}
				// The earlier observation has not produced a canonical view
				// yet. There is no safe identity to attach to, so fall through
				// to exact body matching and then the semantic model.
				continue
			}
			return RuleDecision{Matched: true, Kind: RelationDuplicate, TargetID: target,
				Reason: RuleReasonSameSourceSpan}
		}
	}

	// Rule 2: the same normalized body already exists in the same scope.
	if currentBody != "" {
		for _, candidate := range input.Candidates {
			if !sameScope(current.ScopeKey, candidate.Memory.ScopeKey) {
				continue
			}
			if normalizeBody(candidate.Memory.Content) != currentBody {
				continue
			}
			if inactive(candidate.Memory.ID) {
				return unresolved(RuleReasonInactiveTarget)
			}
			return RuleDecision{Matched: true, Kind: RelationDuplicate, TargetID: candidate.Memory.ID,
				Reason: RuleReasonSameBody}
		}
	}

	// Rule 3: every offered candidate has an explicitly different scope. The
	// two objects cannot share a current view without a scope decision.
	if allScopesDisjoint(current.ScopeKey, input.Candidates) {
		return RuleDecision{Matched: true, Kind: RelationSeparate, Reason: RuleReasonDisjointScope}
	}

	// No candidate exists, so the observation is independent without paying
	// for a semantic call.
	if len(input.Candidates) == 0 {
		return RuleDecision{Matched: true, Kind: RelationIndependent, Reason: RuleReasonNoCandidates}
	}

	// A pending observation without evidence cannot become an accepted view.
	// It is kept unresolved instead of asking the model to relate an
	// ungrounded statement.
	if len(currentSpans) == 0 {
		return unresolved(RuleReasonNoEvidence)
	}

	return RuleDecision{Matched: false, Reason: RuleReasonModelRequired}
}

// evidenceSegmentIDs returns the unique segment identifiers that ground one
// observation. The set, not the evidence row identity, defines the span set.
func evidenceSegmentIDs(items []source.Evidence) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.SegmentID)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func sameSpanSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	if len(left) == 0 {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameScope(left, right string) bool {
	return strings.TrimSpace(left) == strings.TrimSpace(right)
}

func candidateForObservation(candidates []RuleCandidate, observationID string) *RuleCandidate {
	for index := range candidates {
		for _, id := range candidates[index].ObservationIDs {
			if id == observationID {
				return &candidates[index]
			}
		}
	}
	return nil
}

// allScopesDisjoint reports whether every candidate has an explicit, different
// scope from the current observation. An empty scope is unknown and never
// treated as an explicit separation.
func allScopesDisjoint(current string, candidates []RuleCandidate) bool {
	current = strings.TrimSpace(current)
	if current == "" || len(candidates) == 0 {
		return false
	}
	for _, candidate := range candidates {
		scope := strings.TrimSpace(candidate.Memory.ScopeKey)
		if scope == "" || scope == current {
			return false
		}
	}
	return true
}
