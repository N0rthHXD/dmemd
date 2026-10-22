package ingest

import (
	"context"
	"errors"
	"fmt"
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
}

type mergeDecision struct {
	Action   string
	MemoryID string
}

type plannedAction struct {
	candidate Candidate
	decision  Decision
	evidence  []source.Evidence
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
}

type applyState struct {
	plan        *mergePlan
	groupsByKey map[string]string
	memoryIDs   map[string]struct{}
	conflictIDs map[string]struct{}
	pendingIDs  map[string]struct{}
	warnings    []string
	decisions   []mergeDecision
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
		return candidate.Evidence, nil
	}
	result := make([]source.Evidence, 0, len(decision.EvidenceRefs))
	seen := make(map[string]struct{}, len(decision.EvidenceRefs))
	for _, ref := range decision.EvidenceRefs {
		referenced, ok := candidates[ref.CandidateID]
		if !ok {
			return nil, fmt.Errorf("%w: 证据引用越界", ErrProtocol)
		}
		if ref.EvidenceIndex < 0 || ref.EvidenceIndex >= len(referenced.Evidence) {
			return nil, fmt.Errorf("%w: 证据索引越界", ErrProtocol)
		}
		item := referenced.Evidence[ref.EvidenceIndex]
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, item)
	}
	if len(result) == 0 {
		return candidate.Evidence, nil
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
	}
	for _, action := range plan.actions {
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
	}
	return result, nil
}

func applyOne(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	decision := action.decision
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
		return applyChange(ctx, tx, writer, state, action)
	case ActionCorrect:
		return applyCorrect(ctx, tx, writer, state, action)
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
			MemoryID:        duplicate.ID,
			Kind:            duplicate.Kind,
			Title:           duplicate.Title,
			Content:         duplicate.Content,
			ObservedAt:      duplicate.ObservedAt,
			ValidFrom:       duplicate.ValidFrom,
			ValidTo:         duplicate.ValidTo,
			ValidFromBasis:  duplicate.ValidFromBasis,
			ValidToBasis:    duplicate.ValidToBasis,
			AssertionStatus: duplicate.AssertionStatus,
			FactGroupID:     duplicate.FactGroupID,
			ScopeKey:        duplicate.ScopeKey,
			Topics:          duplicate.Topics,
			Evidence:        incoming,
			Operation:       memory.OperationAddEvidence,
			Reason:          reasonMerge,
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
	if expandsBounds(target, action) {
		return applyUnresolved(ctx, tx, writer, state, action)
	}
	if action.decision.Content != "" && normalizeComparable(action.decision.Content) != normalizeComparable(target.Content) {
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
		MemoryID:        target.ID,
		Kind:            target.Kind,
		Title:           target.Title,
		Content:         target.Content,
		ObservedAt:      target.ObservedAt,
		ValidFrom:       target.ValidFrom,
		ValidTo:         target.ValidTo,
		ValidFromBasis:  target.ValidFromBasis,
		ValidToBasis:    target.ValidToBasis,
		AssertionStatus: target.AssertionStatus,
		FactGroupID:     target.FactGroupID,
		ScopeKey:        target.ScopeKey,
		Topics:          target.Topics,
		Evidence:        incoming,
		Operation:       memory.OperationAddEvidence,
		Reason:          reasonMerge,
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
	if action.decision.ValidFrom != nil {
		from = memory.UTCPointer(action.decision.ValidFrom)
		fromBasis = normalizedBasis(action.decision.ValidFromBasis)
	}
	if action.decision.ValidTo != nil {
		to = memory.UTCPointer(action.decision.ValidTo)
		toBasis = normalizedBasis(action.decision.ValidToBasis)
	}
	status := target.AssertionStatus
	conflicts, reason := findGroupConflictsForRange(ctx, tx, target.FactGroupID, target.ID, from, to, content)
	if len(conflicts) > 0 {
		status = memory.AssertionPending
	}
	revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
		MemoryID:        target.ID,
		Kind:            target.Kind,
		Title:           target.Title,
		Content:         content,
		ObservedAt:      target.ObservedAt,
		ValidFrom:       from,
		ValidTo:         to,
		ValidFromBasis:  fromBasis,
		ValidToBasis:    toBasis,
		AssertionStatus: status,
		FactGroupID:     target.FactGroupID,
		ScopeKey:        target.ScopeKey,
		Topics:          reviseTopics(action),
		Evidence:        unionEvidence(currentEvidence, action.evidence),
		Operation:       memory.OperationUpdate,
		Reason:          reasonEnrich,
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

func applyChange(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	target, err := currentTarget(ctx, tx, state.plan, action.decision.TargetMemoryIDs[0])
	if err != nil {
		return err
	}
	if target.State != memory.StateActive || target.AssertionStatus != memory.AssertionAccepted {
		state.decisions = append(state.decisions, mergeDecision{Action: "skip-inactive-target"})
		return nil
	}
	transition := action.decision.ValidFrom
	if transition == nil {
		transition = action.candidate.ValidFrom
	}
	if transition == nil || !hasVerifiedEvidence(state.plan.source, action.evidence) {
		return changeToConflict(ctx, tx, writer, state, action, memory.ConflictUncertainTime)
	}
	if target.FactGroupID == "" {
		return changeToConflict(ctx, tx, writer, state, action, memory.ConflictAmbiguousScope)
	}
	group, err := tx.GetFactGroup(ctx, target.FactGroupID)
	if err != nil {
		return err
	}
	if group.Cardinality != memory.CardinalitySingle {
		return changeToConflict(ctx, tx, writer, state, action, memory.ConflictAmbiguousScope)
	}
	from := transition.UTC()
	if target.ValidFrom != nil && !from.After(*target.ValidFrom) {
		return changeToConflict(ctx, tx, writer, state, action, memory.ConflictComplexTimeline)
	}
	if target.ValidTo != nil {
		return changeToConflict(ctx, tx, writer, state, action, memory.ConflictComplexTimeline)
	}
	members, err := tx.ListFactGroupMemories(ctx, target.FactGroupID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if member.ID == target.ID || member.State != memory.StateActive || member.AssertionStatus != memory.AssertionAccepted {
			continue
		}
		if member.ValidFrom != nil && !member.ValidFrom.Before(from) {
			return changeToConflict(ctx, tx, writer, state, action, memory.ConflictComplexTimeline)
		}
		if memory.Overlaps(&from, nil, member.ValidFrom, member.ValidTo) {
			return changeToConflict(ctx, tx, writer, state, action, memory.ConflictContradictoryValue)
		}
	}
	currentEvidence, err := tx.ListRevisionEvidence(ctx, target.ID, target.Version)
	if err != nil {
		return err
	}
	closed, err := writer.Revise(ctx, tx, memory.ReviseInput{
		MemoryID:        target.ID,
		Kind:            target.Kind,
		Title:           target.Title,
		Content:         target.Content,
		ObservedAt:      target.ObservedAt,
		ValidFrom:       target.ValidFrom,
		ValidTo:         &from,
		ValidFromBasis:  target.ValidFromBasis,
		ValidToBasis:    memory.BasisInferred,
		AssertionStatus: target.AssertionStatus,
		FactGroupID:     target.FactGroupID,
		ScopeKey:        target.ScopeKey,
		Topics:          target.Topics,
		Evidence:        currentEvidence,
		Operation:       memory.OperationChange,
		Reason:          reasonChange,
	})
	if err != nil {
		return err
	}
	recordCreated(state, closed)
	created, err := createMemory(ctx, tx, writer, state, action, memory.AssertionAccepted,
		target.FactGroupID, decisionContent(action), action.evidence, reasonChange)
	if err != nil {
		return err
	}
	recordCreated(state, created)
	if err := insertRelation(ctx, tx, created, closed); err != nil {
		return err
	}
	state.decisions = append(state.decisions, mergeDecision{Action: "change", MemoryID: created.ID})
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

func applyCorrect(ctx context.Context, tx Tx, writer memory.Writer, state *applyState, action plannedAction) error {
	target, err := currentTarget(ctx, tx, state.plan, action.decision.TargetMemoryIDs[0])
	if err != nil {
		return err
	}
	if target.State != memory.StateActive {
		state.decisions = append(state.decisions, mergeDecision{Action: "skip-inactive-target"})
		return nil
	}
	if !hasVerifiedEvidence(state.plan.source, action.evidence) {
		return correctToConflict(ctx, tx, writer, state, action, memory.ConflictUncertainTime)
	}
	content := strings.TrimSpace(action.decision.Content)
	if content == "" {
		content = action.candidate.Content
	}
	from := target.ValidFrom
	to := target.ValidTo
	fromBasis := target.ValidFromBasis
	toBasis := target.ValidToBasis
	partial := false
	if action.decision.ValidFrom != nil {
		if target.ValidFrom != nil && action.decision.ValidFrom.After(*target.ValidFrom) {
			partial = true
		}
		from = memory.UTCPointer(action.decision.ValidFrom)
		fromBasis = normalizedBasis(action.decision.ValidFromBasis)
	}
	if action.decision.ValidTo != nil {
		if target.ValidTo != nil && action.decision.ValidTo.Before(*target.ValidTo) {
			partial = true
		}
		to = memory.UTCPointer(action.decision.ValidTo)
		toBasis = normalizedBasis(action.decision.ValidToBasis)
	}
	if partial {
		return correctToConflict(ctx, tx, writer, state, action, memory.ConflictComplexTimeline)
	}
	status := memory.AssertionAccepted
	conflicts, reason := findGroupConflictsForRange(ctx, tx, target.FactGroupID, target.ID, from, to, content)
	if len(conflicts) > 0 {
		status = memory.AssertionPending
	}
	revised, err := writer.Revise(ctx, tx, memory.ReviseInput{
		MemoryID:        target.ID,
		Kind:            target.Kind,
		Title:           target.Title,
		Content:         content,
		ObservedAt:      target.ObservedAt,
		ValidFrom:       from,
		ValidTo:         to,
		ValidFromBasis:  fromBasis,
		ValidToBasis:    toBasis,
		AssertionStatus: status,
		FactGroupID:     target.FactGroupID,
		ScopeKey:        target.ScopeKey,
		Topics:          reviseTopics(action),
		Evidence:        action.evidence,
		Operation:       memory.OperationCorrect,
		Reason:          reasonCorrect,
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
	state.decisions = append(state.decisions, mergeDecision{Action: "correct", MemoryID: revised.ID})
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
	if len(targets) > 0 {
		members := append([]memory.Memory{created}, targets...)
		if _, err := recordConflict(ctx, tx, state, groupID, reason, members); err != nil {
			return err
		}
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
	candidate := action.candidate
	from, to, fromBasis, toBasis := actionRange(action)
	title := candidate.Title
	created, err := writer.Create(ctx, tx, memory.CreateInput{
		Kind:            candidate.Kind,
		Title:           title,
		Content:         content,
		ObservedAt:      observedValue(candidate.ObservedAt),
		ValidFrom:       from,
		ValidTo:         to,
		ValidFromBasis:  fromBasis,
		ValidToBasis:    toBasis,
		AssertionStatus: status,
		FactGroupID:     groupID,
		CreatedByApp:    state.plan.source.AppID,
		OriginSourceID:  state.plan.source.ID,
		ScopeKey:        state.plan.source.ScopeKey,
		Topics:          candidate.Topics,
		Evidence:        evidence,
		Reason:          reason,
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
	key := strings.Join([]string{
		strings.TrimSpace(proposal.Subject),
		strings.TrimSpace(proposal.Predicate),
		strings.TrimSpace(proposal.Scope),
	}, "\x00")
	if !forceNew {
		if id, ok := state.groupsByKey[key]; ok {
			return id, nil
		}
		for _, group := range state.plan.groups {
			if group.Subject == strings.TrimSpace(proposal.Subject) &&
				group.Predicate == strings.TrimSpace(proposal.Predicate) &&
				group.Scope == strings.TrimSpace(proposal.Scope) {
				state.groupsByKey[key] = group.ID
				return group.ID, nil
			}
		}
	}
	subject := strings.TrimSpace(proposal.Subject)
	predicate := strings.TrimSpace(proposal.Predicate)
	scope := strings.TrimSpace(proposal.Scope)
	if !forceNew {
		if existing, err := tx.FindFactGroup(ctx, subject, predicate, scope); err == nil {
			if proposal.Cardinality != "" && proposal.Cardinality != memory.CardinalityUnknown &&
				existing.Cardinality != proposal.Cardinality {
				existing.Cardinality = proposal.Cardinality
				existing.Generation++
				existing.UpdatedAt = time.Now().UTC()
				if err := tx.UpdateFactGroup(ctx, existing); err != nil {
					return "", err
				}
			}
			state.plan.groups[existing.ID] = existing
			state.groupsByKey[key] = existing.ID
			return existing.ID, nil
		} else if !errors.Is(err, memory.ErrNotFound) {
			return "", err
		}
	}
	group, err := memory.NewFactGroup(memory.FactGroupInput{
		Subject:     subject,
		Predicate:   predicate,
		Scope:       scope,
		Cardinality: proposal.Cardinality,
	}, time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	if err := tx.InsertFactGroup(ctx, group); err != nil {
		return "", err
	}
	state.plan.groups[group.ID] = group
	state.groupsByKey[key] = group.ID
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
	if err != nil || group.Cardinality != memory.CardinalitySingle {
		return nil, ""
	}
	from, to, _, _ := actionRange(action)
	return findGroupConflictsForRange(ctx, tx, groupID, "", from, to, content)
}

func findGroupConflictsForRange(ctx context.Context, tx Tx, groupID, excludeID string, from, to *time.Time, content string) ([]memory.Memory, memory.ConflictReason) {
	if groupID == "" {
		return nil, ""
	}
	group, err := tx.GetFactGroup(ctx, groupID)
	if err != nil || group.Cardinality != memory.CardinalitySingle {
		return nil, ""
	}
	members, err := tx.ListFactGroupMemories(ctx, groupID)
	if err != nil {
		return nil, ""
	}
	result := []memory.Memory{}
	reason := memory.ConflictContradictoryValue
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
		if !memory.Overlaps(from, to, member.ValidFrom, member.ValidTo) {
			continue
		}
		if from == nil || to == nil || member.ValidFrom == nil || member.ValidTo == nil {
			reason = memory.ConflictUncertainTime
		}
		result = append(result, member)
	}
	return result, reason
}

func recordConflict(ctx context.Context, tx Tx, state *applyState, groupID string,
	reason memory.ConflictReason, members []memory.Memory) (string, error) {
	if len(members) < 2 {
		return "", nil
	}
	reason = normalizeConflictReason(string(reason))
	versioned := make([]memory.ConflictMember, 0, len(members))
	for _, member := range members {
		versioned = append(versioned, memory.ConflictMember{
			MemoryID: member.ID, Version: member.Version, CurrentVersion: member.Version,
		})
	}
	existing, err := tx.ListOpenConflictsForMemories(ctx, memberIDsOf(versioned))
	if err != nil {
		return "", err
	}
	for _, conflict := range existing {
		if conflict.ReasonCode != reason || conflict.FactGroupID != groupID {
			continue
		}
		changed := !sameMembers(conflict.Members, versioned)
		if changed {
			// Extend the same open conflict with newly detected or revised
			// members instead of creating a duplicate record for the group.
			known := make(map[string]int, len(conflict.Members))
			merged := append([]memory.ConflictMember(nil), conflict.Members...)
			indexByID := make(map[string]int, len(merged))
			for index, member := range merged {
				known[member.MemoryID] = member.Version
				indexByID[member.MemoryID] = index
			}
			for _, member := range versioned {
				if err := tx.UpsertConflictMember(ctx, conflict.ID, member); err != nil {
					return "", err
				}
				if index, exists := indexByID[member.MemoryID]; exists {
					merged[index] = member
				} else {
					indexByID[member.MemoryID] = len(merged)
					merged = append(merged, member)
				}
				previous, exists := known[member.MemoryID]
				if !exists || previous != member.Version {
					if err := tx.InsertConflictEvent(ctx, memory.ConflictEvent{
						ID:         idgen.NewUUID(),
						ConflictID: conflict.ID,
						Action:     "members_changed",
						MemoryID:   member.MemoryID,
						Version:    member.Version,
						CreatedAt:  time.Now().UTC(),
					}); err != nil {
						return "", err
					}
				}
			}
			conflict.Members = merged
			conflict.UpdatedAt = time.Now().UTC()
			conflict.Revision++
			if err := tx.UpdateConflict(ctx, conflict); err != nil {
				return "", err
			}
			if err := memory.QueueConflictReview(ctx, tx, conflict, 0, conflict.UpdatedAt); err != nil {
				return "", err
			}
		}
		if _, ok := state.conflictIDs[conflict.ID]; !ok {
			state.conflictIDs[conflict.ID] = struct{}{}
		}
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
	for _, member := range members {
		if err := tx.InsertConflictEvent(ctx, memory.ConflictEvent{
			ID:         idgen.NewUUID(),
			ConflictID: conflict.ID,
			Action:     "detected",
			MemoryID:   member.ID,
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

// hasVerifiedEvidence requires a non-empty quote that actually occurs in the
// current source. A quote that cannot be located is only a source association
// and may not justify an automatic correct/change.
func hasVerifiedEvidence(src source.Source, evidence []source.Evidence) bool {
	text := sourceText(src.Content)
	for _, item := range evidence {
		quote := strings.TrimSpace(item.Quote)
		if quote != "" && strings.Contains(text, quote) {
			return true
		}
	}
	return false
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

func sameMembers(left, right []memory.ConflictMember) bool {
	if len(left) != len(right) {
		return false
	}
	index := make(map[string]int, len(left))
	for _, member := range left {
		index[member.MemoryID] = member.Version
	}
	for _, member := range right {
		if version, ok := index[member.MemoryID]; !ok || version != member.Version {
			return false
		}
	}
	return true
}

func memberIDsOf(members []memory.ConflictMember) []string {
	result := make([]string, 0, len(members))
	for _, member := range members {
		result = append(result, member.MemoryID)
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
