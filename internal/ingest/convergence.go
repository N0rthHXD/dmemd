package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// explicitUpdate reads evidence, never a model reason_code or receive order.
// A bare different value is not proof that the previous assertion was replaced.
func explicitUpdate(src source.Source, evidence []source.Evidence) bool {
	if src.Redacted {
		return false
	}
	for _, item := range evidence {
		if item.Redacted || item.SourceID != src.ID || !hasVerifiedEvidence(src, []source.Evidence{item}) {
			continue
		}
		quote := strings.ToLower(item.Quote)
		if strings.ContainsAny(quote, "?？") {
			continue
		}
		uncertain := false
		for _, marker := range []string{"如果", "假如", "可能", "考虑", "打算", "是否", "没有更正", "不更正", "没有改", "不改", "没有换", "不换", "未改", "未换", "尚未", "不要", "别", "if ", "might ", "consider", "not changed", "not replace", "don't ", "do not ", "never "} {
			if strings.Contains(quote, marker) {
				uncertain = true
				break
			}
		}
		if uncertain {
			continue
		}
		for _, marker := range []string{"更正", "纠正", "改期", "改为", "改成", "换成", "换用", "改用", "替换", "调整为", "变更为", "搬到", "不再", "以后请", "correction", "corrected", "rescheduled", "changed to", "switched to", "replace", "instead of"} {
			if strings.Contains(quote, marker) {
				return true
			}
		}
	}
	return false
}

// convergenceEligible is shared by the model-free planner and the transaction
// guard. Metadata must agree exactly; a model cannot assert away scope or
// cardinality uncertainty by choosing a target ID.
func convergenceEligible(plan *mergePlan, action plannedAction, group memory.FactGroup, members []memory.Memory) bool {
	proposal := action.candidate.FactGroup
	for _, other := range plan.candidates {
		if proposal != nil && other.CandidateID != action.candidate.CandidateID && other.FactGroup != nil &&
			other.FactGroup.Subject == proposal.Subject && other.FactGroup.Predicate == proposal.Predicate && other.FactGroup.Scope == proposal.Scope {
			return false
		}
	}
	if action.decision.TargetGroupID != "" && action.decision.TargetGroupID != group.ID {
		return false
	}
	for _, id := range action.decision.TargetMemoryIDs {
		item, ok := plan.related[id]
		if !ok || item.FactGroupID != group.ID || item.ScopeKey != plan.source.ScopeKey {
			return false
		}
	}
	if !plan.complete || proposal == nil || group.Cardinality != memory.CardinalitySingle ||
		proposal.Cardinality != memory.CardinalitySingle || strings.TrimSpace(group.Subject) == "" ||
		strings.TrimSpace(group.Predicate) == "" || group.Subject != strings.TrimSpace(proposal.Subject) ||
		group.Predicate != strings.TrimSpace(proposal.Predicate) || group.Scope != strings.TrimSpace(proposal.Scope) ||
		!explicitUpdate(plan.source, action.evidence) {
		return false
	}
	from, to, fromBasis, toBasis := actionRange(action)
	if _, err := memory.NormalizeTimeRange(from, to, fromBasis, toBasis); err != nil {
		return false
	}
	found := false
	for _, old := range convergenceMembers(action, members) {
		if old.State != memory.StateActive || old.AssertionStatus == memory.AssertionRejected {
			continue
		}
		if old.FactGroupID != group.ID || old.ScopeKey != plan.source.ScopeKey {
			return false
		}
		if normalizeComparable(old.Content) == normalizeComparable(decisionContent(action)) {
			return false
		}
		// Two explicitly bounded overlapping alternatives require adjudication.
		if from != nil && to != nil && old.ValidFrom != nil && old.ValidTo != nil &&
			memory.Overlaps(from, to, old.ValidFrom, old.ValidTo) {
			return false
		}
		disjoint := !memory.Overlaps(from, to, old.ValidFrom, old.ValidTo)
		later := action.candidate.ObservedAt != nil && action.candidate.ObservedAt.After(old.ObservedAt)
		if from != nil && (old.ValidFrom == nil || from.After(*old.ValidFrom)) {
			later = true
		}
		if !disjoint && (!later || (from != nil && old.ValidFrom != nil && from.Before(*old.ValidFrom))) {
			return false
		}
		found = true
	}
	return found
}

func planConvergence(plan *mergePlan, candidate Candidate) (plannedAction, bool) {
	action := plannedAction{candidate: candidate, evidence: candidate.Evidence,
		decision: Decision{CandidateID: candidate.CandidateID, Action: ActionAdd}}
	if candidate.FactGroup == nil {
		return action, false
	}
	group := matchingGroup(plan.groups, *candidate.FactGroup)
	if group == nil {
		return action, false
	}
	members := []memory.Memory{}
	for _, item := range plan.related {
		if item.FactGroupID == group.ID {
			members = append(members, item)
		}
	}
	action.decision.TargetGroupID = group.ID
	action.autoConverge = true
	return action, convergenceEligible(plan, action, *group, members)
}

// tryConvergence writes a successor, the complete conflict, all predecessor
// revisions, supersedes edges and the resolution audit in the ingestion tx.
func tryConvergence(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) (bool, error) {
	proposal := action.candidate.FactGroup
	if proposal == nil {
		return false, nil
	}
	group, err := tx.FindFactGroup(ctx, strings.TrimSpace(proposal.Subject), strings.TrimSpace(proposal.Predicate), strings.TrimSpace(proposal.Scope))
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) || strings.TrimSpace(proposal.Subject) == "" || strings.TrimSpace(proposal.Predicate) == "" {
			return false, nil
		}
		return false, err
	}
	members, err := tx.ListFactGroupMemories(ctx, group.ID)
	if err != nil {
		return false, err
	}
	if !convergenceEligible(state.plan, action, group, members) {
		return false, nil
	}
	from, _, _, _ := actionRange(action)
	oldMembers := convergenceMembers(action, members)
	// Never close a conflict that also contains an unverified, unrelated member.
	ids := make([]string, 0, len(oldMembers))
	known := map[string]bool{}
	for _, old := range oldMembers {
		ids = append(ids, old.ID)
		known[old.ID] = true
	}
	open, err := tx.ListOpenConflictsForMemories(ctx, ids)
	if err != nil {
		return false, err
	}
	for _, conflict := range open {
		if conflict.FactGroupID != group.ID {
			return false, nil
		}
		for _, member := range conflict.Members {
			if !known[member.MemoryID] {
				return false, nil
			}
		}
	}
	created, err := createMemory(ctx, tx, writer, state, action, memory.AssertionAccepted, group.ID,
		decisionContent(action), action.evidence, "ingest:auto_converge")
	if err != nil {
		return false, err
	}
	recordCreated(state, created)
	_, err = recordConflict(ctx, tx, state, group.ID, memory.ConflictContradictoryValue,
		append([]memory.Memory{created}, oldMembers...))
	if err != nil {
		return false, err
	}
	for _, old := range oldMembers {
		evidence, err := tx.ListRevisionEvidence(ctx, old.ID, old.Version)
		if err != nil {
			return false, err
		}
		status, to, basis := memory.AssertionRejected, old.ValidTo, old.ValidToBasis
		// A dated forward transition preserves history. Corrections and
		// rescheduling with unknown bounds reject, rather than inventing dates.
		if old.AssertionStatus == memory.AssertionAccepted && old.Kind != memory.KindEvent && old.Phase != memory.PhaseScheduled &&
			from != nil && old.ValidTo == nil && (old.ValidFrom == nil || from.After(*old.ValidFrom)) {
			status, to, basis = old.AssertionStatus, from, memory.BasisInferred
		}
		if old.AssertionStatus == memory.AssertionAccepted && old.ValidTo != nil && from != nil && !old.ValidTo.After(*from) &&
			old.Kind != memory.KindEvent && old.Phase != memory.PhaseScheduled {
			status = old.AssertionStatus
		}
		revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
			MemoryID: old.ID, Kind: old.Kind, Title: old.Title, Content: old.Content,
			ObservedAt: old.ObservedAt, ValidFrom: old.ValidFrom, ValidTo: to,
			ValidFromBasis: old.ValidFromBasis, ValidToBasis: basis, AssertionStatus: status,
			FactGroupID: old.FactGroupID, ScopeKey: old.ScopeKey, Topics: old.RawTopics,
			Phase: old.Phase, Durability: old.Durability, Evidence: evidence,
			Operation: memory.OperationResolve, Reason: "ingest:auto_converge",
		})
		if err != nil {
			return false, err
		}
		recordCreated(state, revised)
		if err := insertRelation(ctx, tx, created, revised); err != nil {
			return false, err
		}
	}
	conflicts, err := tx.ListOpenConflictsForMemories(ctx, []string{created.ID})
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(map[string]any{"action": "supersede", "member_ids": []string{created.ID}, "reason": "ingest:auto_converge:explicit_update"})
	if err != nil {
		return false, err
	}
	for _, conflict := range conflicts {
		if _, err := memory.CompleteConflictResolution(ctx, tx, conflict, string(payload), time.Now().UTC()); err != nil {
			return false, err
		}
	}
	state.decisions = append(state.decisions, mergeDecision{Action: "auto_converge", MemoryID: created.ID})
	return true, nil
}

// Ignore closed historical segments when a current alternative exists. When
// intervals are already disjoint, keep the latest predecessor to audit the
// replacement instead of requiring an unnecessary decision model call.
func convergenceMembers(action plannedAction, members []memory.Memory) []memory.Memory {
	from, _, _, _ := actionRange(action)
	current := []memory.Memory{}
	var latestClosed *memory.Memory
	for _, item := range members {
		if item.State != memory.StateActive || item.AssertionStatus == memory.AssertionRejected {
			continue
		}
		if from != nil && item.ValidTo != nil && !item.ValidTo.After(*from) {
			if latestClosed == nil || item.ValidTo.After(*latestClosed.ValidTo) {
				copy := item
				latestClosed = &copy
			}
			continue
		}
		current = append(current, item)
	}
	if len(current) == 0 && latestClosed != nil {
		current = append(current, *latestClosed)
	}
	return current
}

// Stable candidate ordering also makes multiple updates in one source reproducible.
func sortedCandidates(plan *mergePlan) []Candidate {
	items := make([]Candidate, 0, len(plan.candidates))
	for _, candidate := range plan.candidates {
		items = append(items, candidate)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CandidateID < items[j].CandidateID })
	return items
}
