package ingest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const (
	reasonAdd        = "ingest:add"
	reasonMerge      = "ingest:merge_evidence"
	reasonEnrich     = "ingest:enrich"
	reasonChange     = "ingest:change"
	reasonCorrect    = "ingest:correct"
	reasonSeparate   = "ingest:separate"
	reasonConflict   = "ingest:conflict"
	reasonUnresolved = "ingest:unresolved"
)

type mergeResult struct {
	Status           JobStatus
	MemoryIDs        []string
	ConflictIDs      []string
	PendingMemoryIDs []string
	Warnings         []string
	Decisions        []mergeDecision
	Convergence      []ConvergenceDiagnostic
}

type mergeDecision struct {
	Action   string
	MemoryID string
}

type plannedAction struct {
	candidate    Candidate
	decision     Decision
	evidence     []source.Evidence
	autoConverge bool
}

// mergePlan is built entirely outside the write transaction. It records the
// catalog generation and context snapshots used to validate the plan again when
// the transaction opens.
type mergePlan struct {
	source            source.Source
	candidates        map[string]Candidate
	actions           []plannedAction
	related           map[string]memory.Memory
	groups            map[string]memory.FactGroup
	conflicts         []memory.Conflict
	complete          bool
	warnings          []string
	catalogGeneration int64
	decided           map[string]struct{}

	// Context completeness is tracked per candidate and per fact group. A
	// truncated unrelated keyword result must not downgrade every safe update
	// in the source.
	incompleteCandidates map[string]struct{}
	incompleteGroups     map[string]struct{}
	convergenceReasons   map[string]ConvergenceDiagnostic
}

type applyState struct {
	plan        *mergePlan
	groupsByKey map[string]string
	memoryIDs   map[string]struct{}
	conflictIDs map[string]struct{}
	pendingIDs  map[string]struct{}
	warnings    []string
	decisions   []mergeDecision
	convergence map[string]ConvergenceDiagnostic
}

func (p *mergePlan) candidateComplete(candidateID string) bool {
	if _, bad := p.incompleteCandidates[candidateID]; bad {
		return false
	}
	if len(p.incompleteCandidates) == 0 && len(p.incompleteGroups) == 0 && !p.complete {
		return false
	}
	return true
}

func (p *mergePlan) groupComplete(groupID string) bool {
	if groupID == "" {
		return true
	}
	if _, bad := p.incompleteGroups[groupID]; bad {
		return false
	}
	if len(p.incompleteCandidates) == 0 && len(p.incompleteGroups) == 0 && !p.complete {
		return false
	}
	return true
}

func (p *mergePlan) markIncompleteCandidate(candidateID string) {
	if p.incompleteCandidates == nil {
		p.incompleteCandidates = make(map[string]struct{})
	}
	p.incompleteCandidates[candidateID] = struct{}{}
}

func (p *mergePlan) markIncompleteGroup(groupID string) {
	if groupID == "" {
		return
	}
	if p.incompleteGroups == nil {
		p.incompleteGroups = make(map[string]struct{})
	}
	p.incompleteGroups[groupID] = struct{}{}
}

func (p *mergePlan) recordConvergence(candidateID, groupID, code string) {
	if code == "" {
		return
	}
	if p.convergenceReasons == nil {
		p.convergenceReasons = make(map[string]ConvergenceDiagnostic)
	}
	if _, exists := p.convergenceReasons[candidateID]; exists {
		return
	}
	p.convergenceReasons[candidateID] = ConvergenceDiagnostic{
		CandidateID: candidateID, GroupID: groupID, Code: code,
	}
}

// transitionOrdered allows several mentions of one fact in a single source
// only when their verified evidence positions establish an order. Candidate ID
// order is never used as a substitute for source order.
func (p *mergePlan) transitionOrdered(candidateID string) bool {
	if len(p.candidates) <= 1 {
		return true
	}
	candidate, ok := p.candidates[candidateID]
	if !ok {
		return true
	}
	identity, ok := candidateGroupIdentity(p.source.ScopeKey, candidate)
	if !ok {
		return false
	}
	position := candidateSourceOrder(p.source, candidate)
	for _, other := range p.candidates {
		if other.CandidateID == candidateID {
			continue
		}
		otherIdentity, ok := candidateGroupIdentity(p.source.ScopeKey, other)
		if !ok || !sameFactGroupIdentity(identity, otherIdentity) {
			continue
		}
		otherPosition := candidateSourceOrder(p.source, other)
		if otherPosition == position && normalizeComparable(other.Content) != normalizeComparable(candidate.Content) {
			return false
		}
	}
	return true
}

func (p *mergePlan) decisionContextComplete(decision Decision) bool {
	if !p.candidateComplete(decision.CandidateID) {
		return false
	}
	if decision.TargetGroupID != "" && !p.groupComplete(decision.TargetGroupID) {
		return false
	}
	for _, id := range decision.TargetMemoryIDs {
		item, ok := p.related[id]
		if ok && item.FactGroupID != "" && !p.groupComplete(item.FactGroupID) {
			return false
		}
	}
	return true
}

func (p *mergePlan) convergenceDiagnostics() []ConvergenceDiagnostic {
	if len(p.convergenceReasons) == 0 {
		return []ConvergenceDiagnostic{}
	}
	result := make([]ConvergenceDiagnostic, 0, len(p.convergenceReasons))
	for _, item := range p.convergenceReasons {
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CandidateID != result[j].CandidateID {
			return result[i].CandidateID < result[j].CandidateID
		}
		return result[i].Code < result[j].Code
	})
	return result
}

func validateDecisions(batch []Candidate, result DecisionResult) ([]Decision, error) {
	seen := make(map[string]struct{}, len(batch))
	for _, candidate := range batch {
		seen[candidate.CandidateID] = struct{}{}
	}
	decisions := make(map[string]Decision, len(result.Decisions))
	for _, decision := range result.Decisions {
		if decision.CandidateID == "" {
			return nil, fmt.Errorf("%w: 决策缺少 candidate_id", ErrProtocol)
		}
		if _, expected := seen[decision.CandidateID]; !expected {
			return nil, fmt.Errorf("%w: 未知 candidate_id %s", ErrProtocol, decision.CandidateID)
		}
		if _, duplicate := decisions[decision.CandidateID]; duplicate {
			return nil, fmt.Errorf("%w: candidate_id %s 重复", ErrProtocol, decision.CandidateID)
		}
		action, err := normalizeAction(decision.Action)
		if err != nil {
			return nil, err
		}
		decision.Action = action
		if decision.TargetGroupID != "" && decision.FactGroup != nil {
			return nil, fmt.Errorf("%w: target_group_id 与 fact_group 不能同时提供", ErrProtocol)
		}
		switch action {
		case ActionEnrich, ActionCorrect, ActionMergeEvidence:
			if len(decision.TargetMemoryIDs) != 1 {
				return nil, fmt.Errorf("%w: %s 必须恰好有一个目标", ErrProtocol, action)
			}
			if decision.TargetGroupID != "" {
				return nil, fmt.Errorf("%w: %s 不接受 target_group_id", ErrProtocol, action)
			}
		case ActionChange:
			if len(decision.TargetMemoryIDs) != 1 {
				return nil, fmt.Errorf("%w: %s 必须恰好有一个目标", ErrProtocol, action)
			}
		case ActionAdd, ActionSeparate:
			if len(decision.TargetMemoryIDs) != 0 {
				return nil, fmt.Errorf("%w: %s 不接受 target_memory_ids", ErrProtocol, action)
			}
		case ActionConflict, ActionUnresolved:
			if len(decision.TargetMemoryIDs) > maxDecisionBatch {
				return nil, fmt.Errorf("%w: 目标过多", ErrProtocol)
			}
		case ActionSkip:
			if len(decision.TargetMemoryIDs) != 0 || decision.TargetGroupID != "" || decision.FactGroup != nil {
				return nil, fmt.Errorf("%w: skip 不接受目标或事实组", ErrProtocol)
			}
		}
		if err := validateDecisionTimes(decision); err != nil {
			return nil, err
		}
		decisions[decision.CandidateID] = decision
	}
	if len(decisions) != len(seen) {
		return nil, fmt.Errorf("%w: 缺失候选决策", ErrProtocol)
	}
	ordered := make([]Decision, 0, len(batch))
	for _, candidate := range batch {
		ordered = append(ordered, decisions[candidate.CandidateID])
	}
	return ordered, nil
}

func normalizeAction(action Action) (Action, error) {
	switch action {
	case ActionAdd, ActionMergeEvidence, ActionEnrich, ActionChange, ActionCorrect,
		ActionSeparate, ActionConflict, ActionUnresolved, ActionSkip:
		return action, nil
	default:
		return "", fmt.Errorf("%w: 未知动作 %q", ErrProtocol, action)
	}
}

func validateDecisionTimes(decision Decision) error {
	if decision.ValidFrom == nil && decision.ValidTo == nil {
		return nil
	}
	_, err := memory.NormalizeTimeRange(decision.ValidFrom, decision.ValidTo,
		decision.ValidFromBasis, decision.ValidToBasis)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return nil
}

func decisionEvidence(candidate Candidate, decision Decision, candidates map[string]Candidate) ([]source.Evidence, error) {
	if len(decision.EvidenceRefs) == 0 {
		return candidate.SourceEvidence, nil
	}
	result := make([]source.Evidence, 0, len(decision.EvidenceRefs))
	seen := make(map[string]struct{}, len(decision.EvidenceRefs))
	for _, ref := range decision.EvidenceRefs {
		referenced, ok := candidates[ref.CandidateID]
		if !ok {
			return nil, fmt.Errorf("%w: 证据引用越界", ErrProtocol)
		}
		if ref.EvidenceIndex < 0 || ref.EvidenceIndex >= len(referenced.Evidence) ||
			ref.EvidenceIndex >= len(referenced.SourceEvidence) {
			return nil, fmt.Errorf("%w: 证据索引越界", ErrProtocol)
		}
		item := referenced.SourceEvidence[ref.EvidenceIndex]
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, item)
	}
	if len(result) == 0 {
		return candidate.SourceEvidence, nil
	}
	return result, nil
}

// checkPlanState is the conservative generation/version guard. Any write that
// landed while the model was running invalidates the whole attempt, and the
// worker retries within its existing three-attempt budget.
func checkPlanState(ctx context.Context, tx Tx, plan *mergePlan) error {
	generation, err := tx.GetCatalogGeneration(ctx)
	if err != nil {
		return err
	}
	if generation != plan.catalogGeneration {
		return fmt.Errorf("%w: catalog %d != %d", ErrStateChanged, generation, plan.catalogGeneration)
	}
	for id, snapshot := range plan.related {
		current, err := tx.GetMemory(ctx, id)
		if errors.Is(err, memory.ErrNotFound) {
			return fmt.Errorf("%w: memory %s missing", ErrStateChanged, id)
		}
		if err != nil {
			return err
		}
		if current.Version != snapshot.Version || current.State != snapshot.State ||
			current.AssertionStatus != snapshot.AssertionStatus {
			return fmt.Errorf("%w: memory %s changed", ErrStateChanged, id)
		}
	}
	for id, snapshot := range plan.groups {
		current, err := tx.GetFactGroup(ctx, id)
		if errors.Is(err, memory.ErrNotFound) {
			return fmt.Errorf("%w: group %s missing", ErrStateChanged, id)
		}
		if err != nil {
			return err
		}
		if current.Generation != snapshot.Generation {
			return fmt.Errorf("%w: group %s changed", ErrStateChanged, id)
		}
	}
	for _, snapshot := range plan.conflicts {
		current, err := tx.GetConflict(ctx, snapshot.ID)
		if errors.Is(err, memory.ErrConflictNotFound) {
			return fmt.Errorf("%w: conflict %s missing", ErrStateChanged, snapshot.ID)
		}
		if err != nil {
			return err
		}
		if current.Status != memory.ConflictOpen || current.Revision != snapshot.Revision {
			return fmt.Errorf("%w: conflict %s changed", ErrStateChanged, snapshot.ID)
		}
	}
	return nil
}

func applyPlan(ctx context.Context, tx Tx, writer memory.Writer, plan *mergePlan) (mergeResult, error) {
	state := &applyState{
		plan:        plan,
		groupsByKey: make(map[string]string),
		memoryIDs:   make(map[string]struct{}),
		conflictIDs: make(map[string]struct{}),
		pendingIDs:  make(map[string]struct{}),
		warnings:    append([]string(nil), plan.warnings...),
		convergence: make(map[string]ConvergenceDiagnostic),
	}
	actions := append([]plannedAction(nil), plan.actions...)
	sort.SliceStable(actions, func(i, j int) bool {
		left := candidateSourceOrder(plan.source, actions[i].candidate)
		right := candidateSourceOrder(plan.source, actions[j].candidate)
		if left != right {
			return left < right
		}
		return actions[i].candidate.CandidateID < actions[j].candidate.CandidateID
	})
	for _, action := range actions {
		if err := applyOne(ctx, tx, writer, state, action); err != nil {
			return mergeResult{}, err
		}
	}
	result := mergeResult{
		MemoryIDs:        orderedKeys(state.memoryIDs),
		ConflictIDs:      orderedKeys(state.conflictIDs),
		PendingMemoryIDs: orderedKeys(state.pendingIDs),
		Warnings:         dedupeStrings(state.warnings),
		Decisions:        state.decisions,
		Convergence:      orderedConvergence(state.convergence, plan.convergenceDiagnostics()),
	}
	return result, nil
}

func applyOne(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	decision := action.decision
	// Model action labels are not replacement evidence. The same deterministic
	// gate applies even when the model calls an update a conflict or correction.
	if decision.Action != ActionSkip && decision.Action != ActionSeparate && decision.Action != ActionMergeEvidence {
		if converged, reason, err := tryConvergence(ctx, tx, writer, state, action); err != nil {
			return err
		} else if converged {
			return nil
		} else if action.autoConverge {
			if planReason, ok := state.plan.convergenceReasons[action.candidate.CandidateID]; ok {
				reason = planReason.Code
			}
			if reason == "" {
				reason = ConvergenceIdentityAmbiguous
			}
			state.convergence[action.candidate.CandidateID] = ConvergenceDiagnostic{
				CandidateID: action.candidate.CandidateID, Code: reason,
			}
			// A second action in this transaction can invalidate a preplanned
			// replacement. Never silently downgrade that plan to an accepted add.
			return applyUnresolved(ctx, tx, writer, state, action)
		}
	}
	switch decision.Action {
	case ActionSkip:
		state.decisions = append(state.decisions, mergeDecision{Action: "skip"})
		return nil
	case ActionAdd:
		return applyAdd(ctx, tx, writer, state, action)
	case ActionMergeEvidence:
		return applyMergeEvidence(ctx, tx, writer, state, action)
	case ActionEnrich:
		return applyEnrich(ctx, tx, writer, state, action)
	case ActionChange:
		return changeToConflict(ctx, tx, writer, state, action, memory.ConflictUncertainTime)
	case ActionCorrect:
		return correctToConflict(ctx, tx, writer, state, action, memory.ConflictAmbiguousScope)
	case ActionSeparate:
		return applySeparate(ctx, tx, writer, state, action)
	case ActionConflict:
		return applyConflict(ctx, tx, writer, state, action)
	case ActionUnresolved:
		return applyUnresolved(ctx, tx, writer, state, action)
	default:
		return fmt.Errorf("%w: 动作 %q 未实现", ErrProtocol, decision.Action)
	}
}

func applyAdd(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	groupID, err := resolveGroup(ctx, tx, state, action, false)
	if err != nil {
		return err
	}
	content := decisionContent(action)
	if duplicate := findSameContentMember(ctx, tx, groupID, action, content); duplicate != nil {
		currentEvidence, err := tx.ListRevisionEvidence(ctx, duplicate.ID, duplicate.Version)
		if err != nil {
			return err
		}
		incoming := unionEvidence(currentEvidence, action.evidence)
		if sameEvidence(currentEvidence, incoming) {
			state.decisions = append(state.decisions, mergeDecision{Action: "skip-duplicate"})
			return nil
		}
		revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
			MemoryID:           duplicate.ID,
			Kind:               duplicate.Kind,
			Title:              duplicate.Title,
			Content:            duplicate.Content,
			ObservedAt:         duplicate.ObservedAt,
			ObservedAtBasis:    duplicate.ObservedAtBasis,
			EventTime:          duplicate.EventTime,
			EventTimeBasis:     duplicate.EventTimeBasis,
			EventTimePrecision: duplicate.EventTimePrecision,
			ValidFrom:          duplicate.ValidFrom,
			ValidTo:            duplicate.ValidTo,
			ValidFromBasis:     duplicate.ValidFromBasis,
			ValidToBasis:       duplicate.ValidToBasis,
			Temporal:           duplicate.Temporal,
			AssertionStatus:    duplicate.AssertionStatus,
			FactGroupID:        duplicate.FactGroupID,
			ScopeKey:           duplicate.ScopeKey,
			Procedure:          duplicate.Procedure,
			Qualifiers:         duplicate.Qualifiers,
			Topics:             duplicate.Topics,
			Evidence:           incoming,
			Operation:          memory.OperationAddEvidence,
			Reason:             reasonMerge,
		})
		if err != nil {
			return err
		}
		recordCreated(state, revised)
		state.decisions = append(state.decisions, mergeDecision{Action: "merge-duplicate", MemoryID: revised.ID})
		return nil
	}
	conflicts, reason := findGroupConflicts(ctx, tx, groupID, action, content)
	status := memory.AssertionAccepted
	if len(conflicts) > 0 {
		status = memory.AssertionPending
	}
	created, err := createMemory(ctx, tx, writer, state, action, status, groupID, content,
		action.evidence, reasonAdd)
	if err != nil {
		return err
	}
	recordCreated(state, created)
	if len(conflicts) > 0 {
		members := append([]memory.Memory{created}, conflicts...)
		if _, err := recordConflict(ctx, tx, state, groupID, reason, members); err != nil {
			return err
		}
		state.warnings = append(state.warnings, WarningConflictDetected)
	}
	state.decisions = append(state.decisions, mergeDecision{Action: "add", MemoryID: created.ID})
	return nil
}

func applyMergeEvidence(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	target, err := currentTarget(ctx, tx, state.plan, action.decision.TargetMemoryIDs[0])
	if err != nil {
		return err
	}
	if target.State != memory.StateActive || target.AssertionStatus != memory.AssertionAccepted {
		state.decisions = append(state.decisions, mergeDecision{Action: "skip-inactive-target"})
		return nil
	}
	// Evidence-only merging must not change the target's valid time. A missing
	// candidate boundary is not an expansion when the assertion is identical.
	if action.decision.ValidFrom != nil || action.decision.ValidTo != nil ||
		normalizeComparable(decisionContent(action)) != normalizeComparable(target.Content) {
		return applyUnresolved(ctx, tx, writer, state, action)
	}
	currentEvidence, err := tx.ListRevisionEvidence(ctx, target.ID, target.Version)
	if err != nil {
		return err
	}
	incoming := unionEvidence(currentEvidence, action.evidence)
	if sameEvidence(currentEvidence, incoming) {
		state.decisions = append(state.decisions, mergeDecision{Action: "skip-duplicate-evidence"})
		return nil
	}
	revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
		MemoryID:           target.ID,
		Kind:               target.Kind,
		Title:              target.Title,
		Content:            target.Content,
		ObservedAt:         target.ObservedAt,
		ObservedAtBasis:    target.ObservedAtBasis,
		EventTime:          target.EventTime,
		EventTimeBasis:     target.EventTimeBasis,
		EventTimePrecision: target.EventTimePrecision,
		ValidFrom:          target.ValidFrom,
		ValidTo:            target.ValidTo,
		ValidFromBasis:     target.ValidFromBasis,
		ValidToBasis:       target.ValidToBasis,
		Temporal:           target.Temporal,
		AssertionStatus:    target.AssertionStatus,
		FactGroupID:        target.FactGroupID,
		ScopeKey:           target.ScopeKey,
		Procedure:          target.Procedure,
		Qualifiers:         target.Qualifiers,
		Topics:             target.Topics,
		Evidence:           incoming,
		Operation:          memory.OperationAddEvidence,
		Reason:             reasonMerge,
	})
	if err != nil {
		return err
	}
	recordCreated(state, revised)
	state.decisions = append(state.decisions, mergeDecision{Action: "merge_evidence", MemoryID: revised.ID})
	return nil
}

func applyEnrich(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	target, err := currentTarget(ctx, tx, state.plan, action.decision.TargetMemoryIDs[0])
	if err != nil {
		return err
	}
	if target.State != memory.StateActive || target.AssertionStatus != memory.AssertionAccepted {
		state.decisions = append(state.decisions, mergeDecision{Action: "skip-inactive-target"})
		return nil
	}
	content := strings.TrimSpace(action.decision.Content)
	if content == "" {
		content = action.candidate.Content
	}
	if expandsBounds(target, action) {
		return applyUnresolved(ctx, tx, writer, state, action)
	}
	currentEvidence, err := tx.ListRevisionEvidence(ctx, target.ID, target.Version)
	if err != nil {
		return err
	}
	from := target.ValidFrom
	to := target.ValidTo
	fromBasis := target.ValidFromBasis
	toBasis := target.ValidToBasis
	phase := target.Phase
	durability := target.Durability
	if action.candidate.Phase != "" && action.candidate.Phase != PhaseUnknown {
		phase = memory.Phase(action.candidate.Phase)
	}
	if action.candidate.Durability != "" && action.candidate.Durability != DurabilityUnknown {
		durability = memory.Durability(action.candidate.Durability)
	}
	if action.decision.ValidFrom != nil {
		from = memory.UTCPointer(action.decision.ValidFrom)
		fromBasis = normalizedBasis(action.decision.ValidFromBasis)
	}
	if action.decision.ValidTo != nil {
		to = memory.UTCPointer(action.decision.ValidTo)
		toBasis = normalizedBasis(action.decision.ValidToBasis)
	}
	status := target.AssertionStatus
	conflicts, reason := findGroupConflictsForRange(ctx, tx, target.FactGroupID, target.ID, from, to, content, nil)
	if len(conflicts) > 0 {
		status = memory.AssertionPending
	}
	eventTime, eventBasis, eventPrecision := target.EventTime, target.EventTimeBasis, target.EventTimePrecision
	if action.candidate.EventTime != nil {
		eventTime, eventBasis, eventPrecision = action.candidate.EventTime, action.candidate.EventTimeBasis, action.candidate.EventTimePrecision
	}
	temporalDetails := target.Temporal
	if normalizeComparable(content) == normalizeComparable(action.candidate.Content) {
		temporalDetails = action.candidate.Temporal
	}
	procedure := target.Procedure
	if action.candidate.Procedure != nil {
		procedure = action.candidate.Procedure
	}
	qualifiers := target.Qualifiers
	if action.candidate.Qualifiers != nil {
		qualifiers = action.candidate.Qualifiers
	}
	revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
		MemoryID:           target.ID,
		Kind:               target.Kind,
		Title:              target.Title,
		Content:            content,
		ObservedAt:         target.ObservedAt,
		ObservedAtBasis:    target.ObservedAtBasis,
		EventTime:          eventTime,
		EventTimeBasis:     eventBasis,
		EventTimePrecision: eventPrecision,
		ValidFrom:          from,
		ValidTo:            to,
		ValidFromBasis:     fromBasis,
		ValidToBasis:       toBasis,
		Temporal:           temporalDetails,
		AssertionStatus:    status,
		FactGroupID:        target.FactGroupID,
		ScopeKey:           target.ScopeKey,
		Procedure:          procedure,
		Qualifiers:         qualifiers,
		ReplaceStructured:  true,
		Topics:             reviseTopics(action),
		Evidence:           unionEvidence(currentEvidence, action.evidence),
		Phase:              phase,
		Durability:         durability,
		Operation:          memory.OperationUpdate,
		Reason:             reasonEnrich,
	})
	if err != nil {
		return err
	}
	recordCreated(state, revised)
	if len(conflicts) > 0 {
		members := append([]memory.Memory{revised}, conflicts...)
		if _, err := recordConflict(ctx, tx, state, target.FactGroupID, reason, members); err != nil {
			return err
		}
		state.warnings = append(state.warnings, WarningConflictDetected)
	}
	state.decisions = append(state.decisions, mergeDecision{Action: "enrich", MemoryID: revised.ID})
	return nil
}

func changeToConflict(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction, reason memory.ConflictReason) error {
	groupID, err := resolveGroup(ctx, tx, state, action, false)
	if err != nil {
		return err
	}
	if groupID == "" {
		for _, targetID := range action.decision.TargetMemoryIDs {
			target, err := currentTarget(ctx, tx, state.plan, targetID)
			if err != nil {
				return err
			}
			if target.FactGroupID != "" {
				groupID = target.FactGroupID
				break
			}
		}
	}
	created, err := createMemory(ctx, tx, writer, state, action, memory.AssertionPending, groupID,
		decisionContent(action), action.evidence, reasonChange)
	if err != nil {
		return err
	}
	recordCreated(state, created)
	members := []memory.Memory{created}
	for _, targetID := range action.decision.TargetMemoryIDs {
		target, err := currentTarget(ctx, tx, state.plan, targetID)
		if err != nil {
			return err
		}
		members = append(members, target)
	}
	if _, err := recordConflict(ctx, tx, state, groupID, reason, members); err != nil {
		return err
	}
	state.warnings = append(state.warnings, WarningConflictDetected)
	state.decisions = append(state.decisions, mergeDecision{Action: "conflict", MemoryID: created.ID})
	return nil
}

func correctToConflict(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction, reason memory.ConflictReason) error {
	target, err := currentTarget(ctx, tx, state.plan, action.decision.TargetMemoryIDs[0])
	if err != nil {
		return err
	}
	// A local correction that cannot be proven is persisted as a new pending
	// candidate, leaving the original conclusion untouched.
	created, err := createMemory(ctx, tx, writer, state, action, memory.AssertionPending,
		target.FactGroupID, decisionContent(action), action.evidence,
		reasonCorrect+":"+string(normalizeConflictReason(string(reason))))
	if err != nil {
		return err
	}
	recordCreated(state, created)
	if _, err := recordConflict(ctx, tx, state, target.FactGroupID, reason,
		[]memory.Memory{created, target}); err != nil {
		return err
	}
	state.warnings = append(state.warnings, WarningConflictDetected)
	state.decisions = append(state.decisions, mergeDecision{Action: "conflict", MemoryID: created.ID})
	return nil
}

func applySeparate(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	groupID, err := resolveGroup(ctx, tx, state, action, true)
	if err != nil {
		return err
	}
	content := decisionContent(action)
	conflicts, reason := findGroupConflicts(ctx, tx, groupID, action, content)
	status := memory.AssertionAccepted
	if len(conflicts) > 0 {
		status = memory.AssertionPending
	}
	created, err := createMemory(ctx, tx, writer, state, action, status, groupID, content,
		action.evidence, reasonSeparate)
	if err != nil {
		return err
	}
	recordCreated(state, created)
	if len(conflicts) > 0 {
		members := append([]memory.Memory{created}, conflicts...)
		if _, err := recordConflict(ctx, tx, state, groupID, reason, members); err != nil {
			return err
		}
		state.warnings = append(state.warnings, WarningConflictDetected)
	}
	state.decisions = append(state.decisions, mergeDecision{Action: "separate", MemoryID: created.ID})
	return nil
}

func applyConflict(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	reason := normalizeConflictReason(action.decision.ReasonCode)
	groupID, err := resolveGroup(ctx, tx, state, action, false)
	if err != nil {
		return err
	}
	targets, err := decisionTargets(ctx, tx, state.plan, action.decision.TargetMemoryIDs)
	if err != nil {
		return err
	}
	if groupID == "" {
		for _, target := range targets {
			if target.FactGroupID != "" {
				groupID = target.FactGroupID
				break
			}
		}
	}
	if len(targets) == 0 && groupID != "" {
		targets, _ = findGroupConflicts(ctx, tx, groupID, action, decisionContent(action))
	}
	created, err := createMemory(ctx, tx, writer, state, action, memory.AssertionPending, groupID,
		decisionContent(action), action.evidence, reasonConflict+":"+string(reason))
	if err != nil {
		return err
	}
	recordCreated(state, created)
	if len(targets) == 0 {
		if _, err := recordConflict(ctx, tx, state, groupID, reason, []memory.Memory{created}); err != nil {
			return err
		}
		state.decisions = append(state.decisions, mergeDecision{Action: "conflict-pending", MemoryID: created.ID})
		return nil
	}
	members := append([]memory.Memory{created}, targets...)
	if _, err := recordConflict(ctx, tx, state, groupID, reason, members); err != nil {
		return err
	}
	state.warnings = append(state.warnings, WarningConflictDetected)
	state.decisions = append(state.decisions, mergeDecision{Action: "conflict", MemoryID: created.ID})
	return nil
}

func applyUnresolved(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	reason := normalizeConflictReason(action.decision.ReasonCode)
	targets, err := decisionTargets(ctx, tx, state.plan, action.decision.TargetMemoryIDs)
	if err != nil {
		return err
	}
	groupID, err := resolveGroup(ctx, tx, state, action, false)
	if err != nil {
		return err
	}
	if groupID == "" {
		for _, target := range targets {
			if target.FactGroupID != "" {
				groupID = target.FactGroupID
				break
			}
		}
	}
	created, err := createMemory(ctx, tx, writer, state, action, memory.AssertionPending, groupID,
		decisionContent(action), action.evidence, reasonUnresolved+":"+string(reason))
	if err != nil {
		return err
	}
	recordCreated(state, created)
	members := append([]memory.Memory{created}, targets...)
	if conflictID, err := recordConflict(ctx, tx, state, groupID, reason, members); err != nil {
		return err
	} else if conflictID != "" {
		state.warnings = append(state.warnings, WarningConflictDetected)
	}
	state.decisions = append(state.decisions, mergeDecision{Action: "unresolved", MemoryID: created.ID})
	return nil
}

// actionRange resolves the validity range for a new record: the candidate
// supplies the extracted suggestion and the decision may override either bound.
func actionRange(action plannedAction) (from, to *time.Time, fromBasis, toBasis memory.TemporalBasis) {
	from, to = action.candidate.ValidFrom, action.candidate.ValidTo
	fromBasis, toBasis = action.candidate.ValidFromBasis, action.candidate.ValidToBasis
	if action.decision.ValidFrom != nil {
		from = action.decision.ValidFrom
		fromBasis = normalizedBasis(action.decision.ValidFromBasis)
	}
	if action.decision.ValidTo != nil {
		to = action.decision.ValidTo
		toBasis = normalizedBasis(action.decision.ValidToBasis)
	}
	return from, to, fromBasis, toBasis
}

func createMemory(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction,
	status memory.AssertionStatus, groupID, content string,
	evidence []source.Evidence, reason string) (memory.Memory, error) {
	from, to, fromBasis, toBasis := actionRange(action)
	return createMemoryWithRange(ctx, tx, writer, state, action, status, groupID, content, evidence, reason,
		from, to, fromBasis, toBasis)
}

func createMemoryWithRange(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction,
	status memory.AssertionStatus, groupID, content string,
	evidence []source.Evidence, reason string,
	from, to *time.Time, fromBasis, toBasis memory.TemporalBasis) (memory.Memory, error) {
	candidate := action.candidate
	title := candidate.Title
	created, err := writer.Create(ctx, tx, memory.CreateInput{
		Kind:               candidate.Kind,
		Phase:              memory.Phase(candidate.Phase),
		Durability:         memory.Durability(candidate.Durability),
		Title:              title,
		Content:            content,
		ObservedAt:         observedValue(candidate.ObservedAt),
		ObservedAtBasis:    candidate.ObservedAtBasis,
		EventTime:          candidate.EventTime,
		EventTimeBasis:     candidate.EventTimeBasis,
		EventTimePrecision: candidate.EventTimePrecision,
		ValidFrom:          from,
		ValidTo:            to,
		ValidFromBasis:     fromBasis,
		ValidToBasis:       toBasis,
		Temporal:           candidate.Temporal,
		AssertionStatus:    status,
		FactGroupID:        groupID,
		CreatedByApp:       state.plan.source.AppID,
		OriginSourceID:     state.plan.source.ID,
		ScopeKey:           state.plan.source.ScopeKey,
		Procedure:          candidate.Procedure,
		Qualifiers:         candidate.Qualifiers,
		Topics:             candidate.Topics,
		Evidence:           evidence,
		Reason:             reason,
	})
	if err != nil {
		return memory.Memory{}, err
	}
	if status == memory.AssertionPending {
		state.pendingIDs[created.ID] = struct{}{}
	}
	return created, nil
}

func recordCreated(state *applyState, item memory.Memory) {
	if item.ID != "" {
		state.memoryIDs[item.ID] = struct{}{}
	}
	if item.AssertionStatus == memory.AssertionPending {
		state.pendingIDs[item.ID] = struct{}{}
	} else {
		delete(state.pendingIDs, item.ID)
	}
}

func currentTarget(ctx context.Context, tx Tx, plan *mergePlan, id string) (memory.Memory, error) {
	if _, ok := plan.related[id]; !ok {
		return memory.Memory{}, fmt.Errorf("%w: 目标 %s 不在上下文", ErrProtocol, id)
	}
	item, err := tx.GetMemory(ctx, id)
	if errors.Is(err, memory.ErrNotFound) {
		return memory.Memory{}, fmt.Errorf("%w: 目标 %s 已消失", ErrStateChanged, id)
	}
	return item, err
}

func decisionTargets(ctx context.Context, tx Tx, plan *mergePlan, ids []string) ([]memory.Memory, error) {
	result := make([]memory.Memory, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		item, err := currentTarget(ctx, tx, plan, id)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func resolveGroup(ctx context.Context, tx Tx, state *applyState, action plannedAction, forceNew bool) (string, error) {
	decision := action.decision
	if decision.TargetGroupID != "" {
		group, ok := state.plan.groups[decision.TargetGroupID]
		if !ok {
			return "", fmt.Errorf("%w: 组 %s 不在上下文", ErrProtocol, decision.TargetGroupID)
		}
		return group.ID, nil
	}
	proposal := action.candidate.FactGroup
	if decision.FactGroup != nil {
		proposal = decision.FactGroup
	}
	if proposal == nil || (strings.TrimSpace(proposal.Subject) == "" && strings.TrimSpace(proposal.Predicate) == "") {
		return "", nil
	}
	identity := factGroupIdentity(state.plan.source.ScopeKey, *proposal, action.candidate.Qualifiers)
	key := memory.FactGroupKey(identity)
	keyString := strings.Join(key[:], "\x00")
	if !forceNew {
		if id, ok := state.groupsByKey[keyString]; ok {
			return id, nil
		}
		for _, group := range state.plan.groups {
			if sameFactGroupIdentity(memory.FactGroupIdentity{
				ScopeKey: group.ScopeKey, Subject: group.Subject, Predicate: group.Predicate,
				Scope: group.Scope, Conditions: group.Conditions,
			}, identity) {
				state.groupsByKey[keyString] = group.ID
				return group.ID, nil
			}
		}
	}
	subject := strings.TrimSpace(proposal.Subject)
	predicate := strings.TrimSpace(proposal.Predicate)
	if !forceNew {
		if existing, err := tx.FindFactGroup(ctx, identity); err == nil {
			state.plan.groups[existing.ID] = existing
			state.groupsByKey[keyString] = existing.ID
			return existing.ID, nil
		} else if !errors.Is(err, memory.ErrNotFound) {
			return "", err
		}
		// Optional identity text may be omitted on a later mention. Only reuse a
		// single compatible group; several scene/condition variants stay
		// ambiguous and receive a new explicit identity instead of a guess.
		if groups, err := tx.FindFactGroups(ctx, identity.ScopeKey, subject, predicate); err != nil {
			return "", err
		} else if len(groups) == 1 {
			existing := groups[0]
			if compatible := compatibleGroup(groups, identity); compatible != nil {
				existing = *compatible
				changed := false
				if existing.Scope == "" && identity.Scope != "" {
					existing.Scope, existing.SceneKey, changed = identity.Scope, memory.NormalizeIdentityText(identity.Scope), true
				}
				if len(existing.Conditions) == 0 && len(identity.Conditions) > 0 {
					existing.Conditions = memory.NormalizeConditions(identity.Conditions)
					existing.ConditionsKey = memory.ConditionsKey(existing.Conditions)
					changed = true
				}
				if identity.Cardinality != "" && identity.Cardinality != memory.CardinalityUnknown &&
					existing.Cardinality != identity.Cardinality {
					existing.Cardinality = identity.Cardinality
					changed = true
				}
				if changed {
					existing.Generation++
					existing.UpdatedAt = time.Now().UTC()
					if err := tx.UpdateFactGroup(ctx, existing); err != nil {
						return "", err
					}
				}
				state.plan.groups[existing.ID] = existing
				state.groupsByKey[keyString] = existing.ID
				return existing.ID, nil
			}
		}
	}
	group, err := memory.NewFactGroup(memory.FactGroupInput{
		ScopeKey:    identity.ScopeKey,
		Subject:     subject,
		Predicate:   predicate,
		Scope:       identity.Scope,
		Conditions:  identity.Conditions,
		Cardinality: identity.Cardinality,
	}, time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	if err := tx.InsertFactGroup(ctx, group); err != nil {
		return "", err
	}
	state.plan.groups[group.ID] = group
	state.groupsByKey[keyString] = group.ID
	return group.ID, nil
}

func findSameContentMember(ctx context.Context, tx Tx, groupID string, action plannedAction, content string) *memory.Memory {
	if groupID == "" {
		return nil
	}
	members, err := tx.ListFactGroupMemories(ctx, groupID)
	if err != nil {
		return nil
	}
	from, to := action.candidate.ValidFrom, action.candidate.ValidTo
	if action.decision.ValidFrom != nil {
		from = action.decision.ValidFrom
	}
	if action.decision.ValidTo != nil {
		to = action.decision.ValidTo
	}
	for index := range members {
		member := &members[index]
		if member.State != memory.StateActive || member.AssertionStatus != memory.AssertionAccepted {
			continue
		}
		if normalizeComparable(member.Content) != normalizeComparable(content) {
			continue
		}
		if !memory.Overlaps(from, to, member.ValidFrom, member.ValidTo) {
			continue
		}
		return member
	}
	return nil
}

func findGroupConflicts(ctx context.Context, tx Tx, groupID string, action plannedAction, content string) ([]memory.Memory, memory.ConflictReason) {
	if groupID == "" {
		return nil, ""
	}
	group, err := tx.GetFactGroup(ctx, groupID)
	if err != nil || group.Cardinality == memory.CardinalityMultiple {
		return nil, ""
	}
	from, to, _, _ := actionRange(action)
	return findGroupConflictsForRange(ctx, tx, groupID, "", from, to, content, &action.candidate)
}

func findGroupConflictsForRange(ctx context.Context, tx Tx, groupID, excludeID string, from, to *time.Time, content string, observation *Candidate) ([]memory.Memory, memory.ConflictReason) {
	if groupID == "" {
		return nil, ""
	}
	group, err := tx.GetFactGroup(ctx, groupID)
	if err != nil || group.Cardinality == memory.CardinalityMultiple {
		return nil, ""
	}
	members, err := tx.ListFactGroupMemories(ctx, groupID)
	if err != nil {
		return nil, ""
	}
	result := []memory.Memory{}
	reason := memory.ConflictContradictoryValue
	if group.Cardinality != memory.CardinalitySingle || group.Subject == "" || group.Predicate == "" {
		reason = memory.ConflictAmbiguousScope
	}
	for _, member := range members {
		if member.ID == excludeID || member.State != memory.StateActive {
			continue
		}
		if member.AssertionStatus != memory.AssertionAccepted && member.AssertionStatus != memory.AssertionPending {
			continue
		}
		if normalizeComparable(member.Content) == normalizeComparable(content) {
			continue
		}
		if independentObservation(observation, member, from, to) {
			continue
		}
		if !memory.Overlaps(from, to, member.ValidFrom, member.ValidTo) {
			continue
		}
		if reason != memory.ConflictAmbiguousScope && (from == nil || to == nil || member.ValidFrom == nil || member.ValidTo == nil) {
			reason = memory.ConflictUncertainTime
		}
		result = append(result, member)
	}
	return result, reason
}

// independentObservation distinguishes separate measurements or checks from a
// contradiction. Different values observed at different explicit times are not
// mutually exclusive unless a known validity interval also overlaps.
func independentObservation(candidate *Candidate, member memory.Memory, from, to *time.Time) bool {
	if candidate == nil || candidate.ObservedAt == nil || candidate.ObservedAtBasis == memory.BasisRecorded ||
		member.ObservedAt.IsZero() || member.ObservedAtBasis == memory.BasisRecorded {
		return false
	}
	if from != nil || to != nil || member.ValidFrom != nil || member.ValidTo != nil {
		return false
	}
	return !candidate.ObservedAt.Equal(member.ObservedAt)
}

func recordConflict(ctx context.Context, tx Tx, state *applyState, groupID string,
	reason memory.ConflictReason, members []memory.Memory) (string, error) {
	reason = normalizeConflictReason(string(reason))
	byID := make(map[string]memory.ConflictMember)
	for _, member := range members {
		byID[member.ID] = memory.ConflictMember{
			MemoryID: member.ID, Version: member.Version, CurrentVersion: member.Version,
		}
	}
	// A decision may name only one predecessor. Include every overlapping
	// alternative in the same group, not just the model's partial target list.
	if groupID != "" {
		groupMembers, err := tx.ListFactGroupMemories(ctx, groupID)
		if err != nil {
			return "", err
		}
		for _, member := range groupMembers {
			if member.State != memory.StateActive || member.AssertionStatus == memory.AssertionRejected {
				continue
			}
			for _, anchor := range members {
				if member.ScopeKey == anchor.ScopeKey && memory.Overlaps(member.ValidFrom, member.ValidTo, anchor.ValidFrom, anchor.ValidTo) {
					byID[member.ID] = memory.ConflictMember{MemoryID: member.ID, Version: member.Version, CurrentVersion: member.Version}
					break
				}
			}
		}
	}
	// Compute the connected open-conflict closure. Detection reason changes do
	// not split one contradictory fact into different, incomplete member sets.
	existingByID := make(map[string]memory.Conflict)
	for {
		ids := make([]string, 0, len(byID))
		for id := range byID {
			ids = append(ids, id)
		}
		existing, err := tx.ListOpenConflictsForMemories(ctx, ids)
		if err != nil {
			return "", err
		}
		before := len(byID)
		for _, conflict := range existing {
			if conflict.FactGroupID != groupID {
				continue
			}
			existingByID[conflict.ID] = conflict
			for _, member := range conflict.Members {
				if _, exists := byID[member.MemoryID]; !exists {
					byID[member.MemoryID] = member
				}
			}
		}
		if len(byID) == before {
			break
		}
	}
	if len(byID) < 2 {
		return "", nil
	}
	versioned := make([]memory.ConflictMember, 0, len(byID))
	for _, id := range orderedKeysOfMembers(byID) {
		versioned = append(versioned, byID[id])
	}
	existingIDs := make(map[string]struct{}, len(existingByID))
	for id := range existingByID {
		existingIDs[id] = struct{}{}
	}
	ordered := orderedKeys(existingIDs)
	if len(ordered) > 0 {
		conflict := existingByID[ordered[0]]
		known := make(map[string]struct{}, len(conflict.Members))
		for _, member := range conflict.Members {
			known[member.MemoryID] = struct{}{}
		}
		changed := false
		for _, member := range versioned {
			if _, exists := known[member.MemoryID]; exists {
				continue
			}
			changed = true
			if err := tx.UpsertConflictMember(ctx, conflict.ID, member); err != nil {
				return "", err
			}
			if err := tx.InsertConflictEvent(ctx, memory.ConflictEvent{
				ID: idgen.NewUUID(), ConflictID: conflict.ID, Action: "members_changed",
				MemoryID: member.MemoryID, Version: member.Version, CreatedAt: time.Now().UTC(),
			}); err != nil {
				return "", err
			}
		}
		if changed {
			conflict.Revision++
			conflict.UpdatedAt = time.Now().UTC()
			if err := tx.UpdateConflict(ctx, conflict); err != nil {
				return "", err
			}
		}
		// Preserve historical records when overlapping open sets coalesce.
		for _, id := range ordered[1:] {
			old := existingByID[id]
			if _, err := memory.CompleteConflictResolution(ctx, tx, old,
				`{"action":"merged","conflict_id":"`+conflict.ID+`"}`, time.Now().UTC()); err != nil {
				return "", err
			}
		}
		conflict, err := tx.GetConflict(ctx, conflict.ID)
		if err != nil {
			return "", err
		}
		if err := memory.QueueConflictReview(ctx, tx, conflict, 0, conflict.UpdatedAt); err != nil {
			return "", err
		}
		state.conflictIDs[conflict.ID] = struct{}{}
		return conflict.ID, nil
	}
	now := time.Now().UTC()
	conflict := memory.Conflict{
		ID:          idgen.NewUUID(),
		FactGroupID: groupID,
		Status:      memory.ConflictOpen,
		ReasonCode:  reason,
		Revision:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
		Members:     versioned,
	}
	if err := tx.InsertConflict(ctx, conflict); err != nil {
		return "", err
	}
	for _, member := range versioned {
		if err := tx.InsertConflictEvent(ctx, memory.ConflictEvent{
			ID:         idgen.NewUUID(),
			ConflictID: conflict.ID,
			Action:     "detected",
			MemoryID:   member.MemoryID,
			Version:    member.Version,
			CreatedAt:  now,
		}); err != nil {
			return "", err
		}
	}
	if err := memory.QueueConflictReview(ctx, tx, conflict, 0, now); err != nil {
		return "", err
	}
	state.conflictIDs[conflict.ID] = struct{}{}
	return conflict.ID, nil
}

func orderedKeysOfMembers(members map[string]memory.ConflictMember) []string {
	keys := make(map[string]struct{}, len(members))
	for id := range members {
		keys[id] = struct{}{}
	}
	return orderedKeys(keys)
}

func insertRelation(ctx context.Context, tx Tx, from, to memory.Memory) error {
	if from.ID == "" || to.ID == "" || from.ID == to.ID {
		return nil
	}
	return tx.InsertRelation(ctx, memory.Relation{
		ID:           idgen.NewUUID(),
		FromMemoryID: from.ID,
		FromVersion:  from.Version,
		ToMemoryID:   to.ID,
		ToVersion:    to.Version,
		Kind:         memory.RelationSupersedes,
		CreatedAt:    time.Now().UTC(),
	})
}

func normalizedBasis(value memory.TemporalBasis) memory.TemporalBasis {
	switch value {
	case memory.BasisExplicit, memory.BasisInferred, memory.BasisUnknown:
		return value
	default:
		return memory.BasisInferred
	}
}

func normalizeConflictReason(value string) memory.ConflictReason {
	switch memory.ConflictReason(value) {
	case memory.ConflictContradictoryValue, memory.ConflictUncertainTime,
		memory.ConflictAmbiguousScope, memory.ConflictComplexTimeline:
		return memory.ConflictReason(value)
	default:
		return memory.ConflictContradictoryValue
	}
}

func expandsBounds(target memory.Memory, action plannedAction) bool {
	from, to, _, _ := actionRange(action)
	if from != nil && target.ValidFrom != nil && from.Before(*target.ValidFrom) {
		return true
	}
	if to != nil && target.ValidTo != nil && to.After(*target.ValidTo) {
		return true
	}
	if target.ValidFrom != nil && from == nil {
		return true
	}
	if target.ValidTo != nil && to == nil {
		return true
	}
	return false
}

func decisionContent(action plannedAction) string {
	content := strings.TrimSpace(action.decision.Content)
	if content == "" {
		content = action.candidate.Content
	}
	return content
}

// reviseTopics returns extracted labels for a content-changing update. A nil
// suggestion intentionally clears old labels so governance reclassifies the new
// content instead of carrying unrelated tags forward.
func reviseTopics(action plannedAction) []string {
	return action.candidate.Topics
}

func observedValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func sameEvidence(left, right []source.Evidence) bool {
	if len(left) != len(right) {
		return false
	}
	ids := make(map[string]struct{}, len(left))
	for _, item := range left {
		ids[item.ID] = struct{}{}
	}
	for _, item := range right {
		if _, ok := ids[item.ID]; !ok {
			return false
		}
	}
	return true
}

func unionEvidence(left, right []source.Evidence) []source.Evidence {
	result := make([]source.Evidence, 0, len(left)+len(right))
	seen := make(map[string]struct{}, len(left)+len(right))
	for _, item := range left {
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, item)
	}
	for _, item := range right {
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, item)
	}
	return result
}

func orderedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	// Stable output for jobs and tests.
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j] < result[j-1]; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func orderedConvergence(items map[string]ConvergenceDiagnostic, planned []ConvergenceDiagnostic) []ConvergenceDiagnostic {
	result := make([]ConvergenceDiagnostic, 0, len(items)+len(planned))
	seen := make(map[string]struct{}, len(items)+len(planned))
	appendItem := func(item ConvergenceDiagnostic) {
		if item.Code == "" {
			return
		}
		key := item.CandidateID + "\x00" + item.Code
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	for _, item := range planned {
		appendItem(item)
	}
	for _, item := range items {
		appendItem(item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CandidateID != result[j].CandidateID {
			return result[i].CandidateID < result[j].CandidateID
		}
		return result[i].Code < result[j].Code
	})
	return result
}
