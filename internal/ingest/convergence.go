package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// Transition kinds are service-derived from verifiable source wording. A model
// action label or a bare different value never establishes one.
type transitionKind string

const (
	transitionNone       transitionKind = ""
	transitionChange     transitionKind = "change"
	transitionCorrection transitionKind = "correction"
	transitionReschedule transitionKind = "reschedule"
	transitionCancel     transitionKind = "cancel"
)

// Non-convergence reasons are fixed diagnostics. They never contain source text.
const (
	ConvergenceIdentityAmbiguous     = "identity_ambiguous"
	ConvergenceMissingUpdateEvidence = "missing_update_evidence"
	ConvergenceTimeUnknown           = "time_unknown"
	ConvergenceCardinalityUnknown    = "cardinality_unknown"
	ConvergenceContextTruncated      = "context_truncated"
)

const uncertainMarkers = "如果|假如|可能|考虑|打算|是否|没有更正|不更正|没有改|不改|没有换|不换|未改|未换|尚未|不要|别|if |might |consider|not changed|not replace|don't |do not |never "

// classifyTransition reads verified evidence quotes, never a model reason code
// or receive order. It distinguishes an explicit correction from a state change,
// reschedule and cancellation because they produce different history semantics.
func classifyTransition(src source.Source, evidence []source.Evidence) transitionKind {
	if src.Redacted {
		return transitionNone
	}
	view := source.BuildView(src.Content)
	for _, item := range evidence {
		if item.Redacted || item.SourceID != src.ID || strings.TrimSpace(item.Quote) == "" || !view.ContainsQuote(item.Quote) {
			continue
		}
		quote := strings.ToLower(item.Quote)
		if strings.ContainsAny(quote, "?？") || containsAny(quote, uncertainMarkers) {
			continue
		}
		switch {
		case containsAny(quote, "取消|cancelled|canceled|called off|不去了|不办了"):
			return transitionCancel
		case containsAny(quote, "改期|推迟|延期|提前到|另行安排|rescheduled|reschedule|postponed|moved to"):
			return transitionReschedule
		case containsAny(quote, "更正|纠正|说错|记错|写错|correction|corrected|mistyped|wrongly|mistake|actually"):
			return transitionCorrection
		case strings.Contains(quote, "不是") && strings.Contains(quote, "而是"):
			return transitionCorrection
		case containsAny(quote, "改为|改成|换成|换用|改用|调整为|变更为|搬到|不再|以后|changed to|switched to|replace|instead of|no longer"):
			return transitionChange
		}
	}
	return transitionNone
}

func containsAny(value, markers string) bool {
	for _, marker := range strings.Split(markers, "|") {
		if marker != "" && strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// evaluateConvergence is shared by the model-free planner and the transaction
// guard. Metadata must agree exactly; a model cannot assert away scope,
// cardinality or context uncertainty by choosing a target ID.
func evaluateConvergence(plan *mergePlan, action plannedAction, group memory.FactGroup, members []memory.Memory) (bool, string) {
	identity, ok := candidateGroupIdentity(plan.source.ScopeKey, action.candidate)
	if !ok {
		return false, ConvergenceIdentityAmbiguous
	}
	if !plan.candidateComplete(action.candidate.CandidateID) || !plan.groupComplete(group.ID) {
		return false, ConvergenceContextTruncated
	}
	if action.decision.TargetGroupID != "" && action.decision.TargetGroupID != group.ID {
		return false, ConvergenceIdentityAmbiguous
	}
	if group.ScopeKey != memory.NormalizeScopeKey(identity.ScopeKey) ||
		group.SubjectKey != memory.NormalizeIdentityText(identity.Subject) ||
		group.PredicateKey != memory.NormalizeIdentityText(identity.Predicate) {
		return false, ConvergenceIdentityAmbiguous
	}
	if identity.Scope != "" && group.SceneKey != memory.NormalizeIdentityText(identity.Scope) {
		return false, ConvergenceIdentityAmbiguous
	}
	if len(identity.Conditions) > 0 && group.ConditionsKey != memory.ConditionsKey(identity.Conditions) {
		return false, ConvergenceIdentityAmbiguous
	}
	for _, id := range action.decision.TargetMemoryIDs {
		item, ok := plan.related[id]
		if !ok || item.FactGroupID != group.ID || item.ScopeKey != plan.source.ScopeKey {
			return false, ConvergenceIdentityAmbiguous
		}
	}
	if group.Cardinality != memory.CardinalitySingle {
		return false, ConvergenceCardinalityUnknown
	}
	if identity.Cardinality != "" && identity.Cardinality != memory.CardinalityUnknown &&
		identity.Cardinality != memory.CardinalitySingle {
		return false, ConvergenceCardinalityUnknown
	}
	kind := classifyTransition(plan.source, action.evidence)
	if kind == transitionNone {
		return false, ConvergenceMissingUpdateEvidence
	}
	if !plan.transitionOrdered(action.candidate.CandidateID) {
		return false, ConvergenceIdentityAmbiguous
	}
	from, to, fromBasis, toBasis := transitionRange(plan.source, action, kind)
	if _, err := memory.NormalizeTimeRange(from, to, fromBasis, toBasis); err != nil {
		return false, ConvergenceTimeUnknown
	}
	explicitFrom, _, _, _ := actionRange(action)
	found := false
	for _, old := range convergenceMembers(plan.source, action, members) {
		if old.State != memory.StateActive || old.AssertionStatus == memory.AssertionRejected {
			continue
		}
		if old.FactGroupID != group.ID || old.ScopeKey != plan.source.ScopeKey {
			return false, ConvergenceIdentityAmbiguous
		}
		if normalizeComparable(old.Content) == normalizeComparable(decisionContent(action)) {
			// The same assertion repeated is an evidence addition, not a
			// competing current value.
			return false, ""
		}
		if !transitionCanFollow(kind, from, explicitFrom, action, old) {
			return false, ConvergenceTimeUnknown
		}
		found = true
	}
	if !found {
		return false, ""
	}
	return true, ""
}

func transitionCanFollow(kind transitionKind, from, explicitFrom *time.Time, action plannedAction, old memory.Memory) bool {
	if explicitFrom != nil && old.ValidFrom != nil && explicitFrom.Before(*old.ValidFrom) {
		return false
	}
	if kind == transitionCorrection || kind == transitionReschedule || kind == transitionCancel {
		// A correction does not need to invent a valid-time boundary, but it
		// still needs a source observation at least as new as the record.
		return observedNotOlder(action.candidate, old)
	}
	if from == nil {
		return false
	}
	if old.ValidFrom != nil && !from.After(*old.ValidFrom) && explicitFrom != nil {
		// Two simultaneously stated values are not a forward transition.
		return false
	}
	if old.ValidTo != nil && !from.Before(*old.ValidTo) {
		return true
	}
	if old.Kind == memory.KindEvent || old.Phase == memory.PhaseScheduled {
		return observedNotOlder(action.candidate, old)
	}
	return observedNotOlder(action.candidate, old) || explicitFrom != nil
}

func observedNotOlder(candidate Candidate, old memory.Memory) bool {
	if candidate.ObservedAt == nil {
		return false
	}
	if old.ObservedAt.IsZero() {
		return true
	}
	if candidate.ObservedAt.After(old.ObservedAt) {
		return true
	}
	// Several assertions from the same source share one receipt time. Their
	// order comes from verified evidence position, which transitionOrdered
	// checks separately, so equal time is not evidence of an older statement.
	if candidate.ObservedAt.Equal(old.ObservedAt) && sameCandidateSource(candidate, old) {
		return true
	}
	return false
}

func sameCandidateSource(candidate Candidate, old memory.Memory) bool {
	if old.OriginSourceID == "" {
		return false
	}
	for _, item := range candidate.SourceEvidence {
		if item.SourceID == old.OriginSourceID {
			return true
		}
	}
	return false
}

// transitionRange resolves the transition instant. A dated validity boundary
// wins. An explicit change/correction/reschedule wording may use the source
// observation time as an inferred transition; a correction never invents a
// validity start for the wrong assertion.
func transitionRange(src source.Source, action plannedAction, kind transitionKind) (from, to *time.Time, fromBasis, toBasis memory.TemporalBasis) {
	from, to, fromBasis, toBasis = actionRange(action)
	if from == nil && kind != transitionCorrection && kind != transitionCancel && action.candidate.ObservedAt != nil {
		value := action.candidate.ObservedAt.UTC()
		from = &value
		fromBasis = memory.BasisInferred
	}
	return from, to, fromBasis, toBasis
}

func convergenceRange(src source.Source, action plannedAction) (from, to *time.Time, fromBasis, toBasis memory.TemporalBasis) {
	return transitionRange(src, action, classifyTransition(src, action.evidence))
}

func planConvergence(plan *mergePlan, candidate Candidate) (plannedAction, bool, string) {
	action := plannedAction{candidate: candidate, evidence: candidate.SourceEvidence,
		decision: Decision{CandidateID: candidate.CandidateID, Action: ActionAdd}}
	if _, ok := candidateGroupIdentity(plan.source.ScopeKey, candidate); !ok {
		return action, false, ""
	}
	group, ambiguous := candidateGroup(plan.groups, plan.source.ScopeKey, candidate)
	if ambiguous {
		return action, false, ConvergenceIdentityAmbiguous
	}
	if group == nil {
		return action, false, ""
	}
	members := []memory.Memory{}
	for _, item := range plan.related {
		if item.FactGroupID == group.ID {
			members = append(members, item)
		}
	}
	action.decision.TargetGroupID = group.ID
	action.autoConverge = true
	ok, reason := evaluateConvergence(plan, action, *group, members)
	return action, ok, reason
}

// candidateGroup resolves the exact normalized identity, falling back only when
// a single compatible group exists and the model omitted optional scene or
// condition text. More than one candidate is ambiguous, never a merge target.
func samePhase(left memory.Phase, right Phase) bool {
	if right == "" || right == PhaseUnknown {
		return left == "" || left == memory.PhaseUnknown
	}
	return left == memory.Phase(right)
}

func candidateGroup(groups map[string]memory.FactGroup, sourceScope string, candidate Candidate) (*memory.FactGroup, bool) {
	identity, ok := candidateGroupIdentity(sourceScope, candidate)
	if !ok {
		return nil, false
	}
	matches := make([]memory.FactGroup, 0, 2)
	for _, group := range groups {
		if group.ScopeKey != memory.NormalizeScopeKey(identity.ScopeKey) ||
			group.SubjectKey != memory.NormalizeIdentityText(identity.Subject) ||
			group.PredicateKey != memory.NormalizeIdentityText(identity.Predicate) {
			continue
		}
		matches = append(matches, group)
		if sameFactGroupIdentity(memory.FactGroupIdentity{
			ScopeKey: group.ScopeKey, Subject: group.Subject, Predicate: group.Predicate,
			Scope: group.Scope, Conditions: group.Conditions,
		}, identity) {
			copied := group
			return &copied, false
		}
	}
	if len(matches) == 1 {
		if compatible := compatibleGroup(matches, identity); compatible != nil {
			return compatible, false
		}
	}
	if len(matches) > 1 {
		return nil, true
	}
	return nil, false
}

// planEvidenceMerge makes a repeated identical assertion deterministic. It
// only merges into an accepted current member of the same scoped identity and
// never changes the target's validity, content or assertion status.
func planEvidenceMerge(plan *mergePlan, candidate Candidate) (plannedAction, bool) {
	group, ambiguous := candidateGroup(plan.groups, plan.source.ScopeKey, candidate)
	if group == nil || ambiguous {
		return plannedAction{}, false
	}
	members := make([]memory.Memory, 0, len(plan.related))
	for _, item := range plan.related {
		if item.FactGroupID != group.ID || item.State != memory.StateActive ||
			item.AssertionStatus != memory.AssertionAccepted {
			continue
		}
		if normalizeComparable(item.Content) != normalizeComparable(candidate.Content) {
			continue
		}
		if item.Kind != candidate.Kind || !samePhase(item.Phase, candidate.Phase) {
			continue
		}
		if candidate.EventTime != nil && (item.EventTime == nil || !candidate.EventTime.Equal(*item.EventTime)) {
			continue
		}
		if candidate.ValidFrom != nil && (item.ValidFrom == nil || !candidate.ValidFrom.Equal(*item.ValidFrom)) {
			continue
		}
		if candidate.ValidTo != nil && (item.ValidTo == nil || !candidate.ValidTo.Equal(*item.ValidTo)) {
			continue
		}
		members = append(members, item)
	}
	if len(members) == 0 {
		return plannedAction{}, false
	}
	sort.SliceStable(members, func(i, j int) bool {
		if !members[i].UpdatedAt.Equal(members[j].UpdatedAt) {
			return members[i].UpdatedAt.After(members[j].UpdatedAt)
		}
		return members[i].ID < members[j].ID
	})
	target := members[0]
	return plannedAction{
		candidate: candidate,
		decision: Decision{
			CandidateID:     candidate.CandidateID,
			Action:          ActionMergeEvidence,
			TargetMemoryIDs: []string{target.ID},
		},
		evidence: candidate.SourceEvidence,
	}, true
}

// tryConvergence writes a successor, the complete conflict, all predecessor
// revisions, supersedes edges and the resolution audit in the ingestion tx.
func tryConvergence(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) (bool, string, error) {
	identity, ok := candidateGroupIdentity(state.plan.source.ScopeKey, action.candidate)
	if !ok {
		return false, ConvergenceIdentityAmbiguous, nil
	}
	group, err := tx.FindFactGroup(ctx, identity)
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			return false, "", nil
		}
		return false, "", err
	}
	members, err := tx.ListFactGroupMemories(ctx, group.ID)
	if err != nil {
		return false, "", err
	}
	if eligible, reason := evaluateConvergence(state.plan, action, group, members); !eligible {
		return false, reason, nil
	}
	kind := classifyTransition(state.plan.source, action.evidence)
	from, _, _, _ := transitionRange(state.plan.source, action, kind)
	oldMembers := convergenceMembers(state.plan.source, action, members)
	// Never close a conflict that also contains an unverified, unrelated member.
	ids := make([]string, 0, len(oldMembers))
	known := map[string]bool{}
	for _, old := range oldMembers {
		ids = append(ids, old.ID)
		known[old.ID] = true
	}
	open, err := tx.ListOpenConflictsForMemories(ctx, ids)
	if err != nil {
		return false, "", err
	}
	for _, conflict := range open {
		if conflict.FactGroupID != group.ID {
			return false, ConvergenceIdentityAmbiguous, nil
		}
		for _, member := range conflict.Members {
			if !known[member.MemoryID] {
				return false, ConvergenceIdentityAmbiguous, nil
			}
		}
	}
	successorFrom, successorTo, successorFromBasis, successorToBasis := actionRange(action)
	if successorFrom == nil && kind == transitionChange && from != nil {
		successorFrom, successorFromBasis = from, memory.BasisInferred
	}
	created, err := createMemoryWithRange(ctx, tx, writer, state, action, memory.AssertionAccepted, group.ID,
		decisionContent(action), action.evidence, "ingest:auto_converge",
		successorFrom, successorTo, successorFromBasis, successorToBasis)
	if err != nil {
		return false, "", err
	}
	recordCreated(state, created)
	// A dated state change with non-overlapping half-open segments is an
	// evolution, not a contradiction. Corrections and genuinely overlapping
	// explicit alternatives still create a resolved conflict audit.
	conflictRequired := kind == transitionCorrection
	if kind == transitionChange {
		for _, old := range oldMembers {
			if from == nil || (old.ValidTo != nil && from.Before(*old.ValidTo)) {
				conflictRequired = true
				break
			}
		}
	}
	if conflictRequired || len(open) > 0 {
		// An existing open conflict must receive the successor as a member
		// before it is closed; its detection versions stay immutable.
		if _, err := recordConflict(ctx, tx, state, group.ID, memory.ConflictContradictoryValue,
			append([]memory.Memory{created}, oldMembers...)); err != nil {
			return false, "", err
		}
	}
	actionCode := string(kind)
	if actionCode == "" {
		actionCode = "supersede"
	}
	for _, old := range oldMembers {
		evidence, err := tx.ListRevisionEvidence(ctx, old.ID, old.Version)
		if err != nil {
			return false, "", err
		}
		status := memory.AssertionRejected
		to := old.ValidTo
		basis := old.ValidToBasis
		phase := old.Phase
		if kind == transitionChange && old.Kind != memory.KindEvent && old.Phase != memory.PhaseScheduled &&
			old.AssertionStatus == memory.AssertionAccepted && from != nil {
			// A state change preserves the old value as a historical segment.
			// The boundary is inferred from the transition even when the
			// source did not state a calendar date. Several updates in one
			// source share a receipt time; the intermediate value then becomes
			// rejected history instead of a zero-length valid interval.
			if old.ValidFrom != nil && !from.After(*old.ValidFrom) {
				status = memory.AssertionRejected
			} else {
				if old.ValidTo == nil || from.Before(*old.ValidTo) {
					to, basis = from, memory.BasisInferred
				}
				status = old.AssertionStatus
			}
		}
		if old.Kind == memory.KindEvent || old.Phase == memory.PhaseScheduled {
			switch kind {
			case transitionReschedule:
				phase = memory.PhasePostponed
			case transitionCancel:
				phase = memory.PhaseCancelled
			}
		}
		revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
			MemoryID: old.ID, Kind: old.Kind, Title: old.Title, Content: old.Content,
			ObservedAt: old.ObservedAt, ObservedAtBasis: old.ObservedAtBasis,
			EventTime: old.EventTime, EventTimeBasis: old.EventTimeBasis, EventTimePrecision: old.EventTimePrecision,
			ValidFrom: old.ValidFrom, ValidTo: to,
			ValidFromBasis: old.ValidFromBasis, ValidToBasis: basis, Temporal: old.Temporal,
			AssertionStatus: status,
			FactGroupID:     old.FactGroupID, ScopeKey: old.ScopeKey, Topics: old.RawTopics,
			Phase: phase, Durability: old.Durability, Procedure: old.Procedure, Qualifiers: old.Qualifiers,
			Evidence:  evidence,
			Operation: memory.OperationResolve, Reason: "ingest:auto_converge",
		})
		if err != nil {
			return false, "", err
		}
		recordCreated(state, revised)
		if err := insertRelation(ctx, tx, created, revised); err != nil {
			return false, "", err
		}
		if err := tx.InsertEvolutionEvent(ctx, memory.EvolutionEvent{
			ID: idgen.NewUUID(), FactGroupID: group.ID,
			FromMemoryID: created.ID, FromVersion: created.Version,
			ToMemoryID: revised.ID, ToVersion: revised.Version,
			Action: actionCode, ReasonCode: string(kind), CreatedAt: time.Now().UTC(),
		}); err != nil {
			return false, "", err
		}
	}
	candidatesForClosure := append([]memory.Conflict(nil), open...)
	if conflictRequired {
		loaded, err := tx.ListOpenConflictsForMemories(ctx, []string{created.ID})
		if err != nil {
			return false, "", err
		}
		candidatesForClosure = loaded
	}
	payload, err := json.Marshal(map[string]any{
		"action": actionCode, "member_ids": []string{created.ID},
		"reason": "ingest:auto_converge:" + actionCode,
	})
	if err != nil {
		return false, "", err
	}
	seenConflicts := make(map[string]struct{}, len(candidatesForClosure))
	for _, conflict := range candidatesForClosure {
		if _, duplicate := seenConflicts[conflict.ID]; duplicate {
			continue
		}
		seenConflicts[conflict.ID] = struct{}{}
		if _, err := memory.CompleteConflictResolution(ctx, tx, conflict, string(payload), time.Now().UTC()); err != nil {
			return false, "", err
		}
		state.conflictIDs[conflict.ID] = struct{}{}
	}
	state.decisions = append(state.decisions, mergeDecision{Action: "auto_converge", MemoryID: created.ID})
	return true, "", nil
}

// Ignore closed historical segments when a current alternative exists. When
// intervals are already disjoint, keep the latest predecessor to audit the
// replacement instead of requiring an unnecessary decision model call.
func convergenceMembers(src source.Source, action plannedAction, members []memory.Memory) []memory.Memory {
	from, _, _, _ := convergenceRange(src, action)
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

// sortedCandidates orders the evolution chain by source position, not by
// candidate ID. IDs are service-assigned and must never substitute for source
// order.
func sortedCandidates(plan *mergePlan) []Candidate {
	items := make([]Candidate, 0, len(plan.candidates))
	for _, candidate := range plan.candidates {
		items = append(items, candidate)
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := candidateSourceOrder(plan.source, items[i]), candidateSourceOrder(plan.source, items[j])
		if left != right {
			return left < right
		}
		return items[i].CandidateID < items[j].CandidateID
	})
	return items
}

// candidateSourceOrder returns a stable position derived from verified evidence
// locations. It is not a candidate-ID sort and it never guesses an unknown
// quote into an earlier position.
func candidateSourceOrder(src source.Source, candidate Candidate) int {
	view := source.BuildView(src.Content)
	segmentIndex := make(map[string]int, len(view.Segments))
	for index, segment := range view.Segments {
		segmentIndex[segment.ID] = index
	}
	best := -1
	record := func(index, start int) {
		position := index*1_000_000_000 + start
		if index < 0 {
			position = start
		}
		if best < 0 || position < best {
			best = position
		}
	}
	for _, item := range candidate.SourceEvidence {
		if item.Location != nil {
			index := -1
			for i, segment := range view.Segments {
				if segment.Field == item.Location.Field {
					index = i
					break
				}
			}
			record(index, item.Location.Start)
			continue
		}
		if item.Quote == "" {
			continue
		}
		resolved, err := view.Resolve(source.EvidenceReference{Quote: item.Quote})
		if err == nil {
			record(segmentIndex[resolved.Segment.ID], resolved.Location.Start)
		}
	}
	if best < 0 {
		return int(^uint(0) >> 1)
	}
	return best
}
