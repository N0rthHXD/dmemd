package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
)

// ListConflicts returns a fixed-order management list.
func (s *Service) ListConflicts(ctx context.Context, options ConflictListOptions) ([]Conflict, error) {
	return s.reader.ListConflicts(ctx, options)
}

// GetConflict returns one conflict with its members and immutable events.
func (s *Service) GetConflict(ctx context.Context, id string) (Conflict, error) {
	return s.reader.GetConflict(ctx, id)
}

type resolutionPayload struct {
	Action    ResolutionAction `json:"action"`
	MemberIDs []string         `json:"member_ids,omitempty"`
	Reason    string           `json:"reason,omitempty"`
}

// Resolve performs one explicit, version-checked adjudication in a short
// transaction. Every result is recorded as a revision, a conflict event and a
// catalog generation change; no model call happens here.
func (s *Service) Resolve(ctx context.Context, input ResolveInput) (Conflict, error) {
	var result Conflict
	err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		result, err = s.ResolveInTx(ctx, tx, input)
		return err
	})
	return result, err
}

// ResolveInTx applies an adjudication inside an existing transaction. Explicit
// management and deterministic automatic review share this path so member
// versions, assertion changes and derived invalidation stay identical.
func (s *Service) ResolveInTx(ctx context.Context, tx Tx, input ResolveInput) (Conflict, error) {
	conflict, err := tx.GetConflict(ctx, input.ConflictID)
	if err != nil {
		return Conflict{}, err
	}
	if conflict.Status == ConflictResolved {
		payload, _ := json.Marshal(s.resolutionPayload(input))
		if conflict.Resolution == string(payload) {
			return conflict, nil
		}
		return Conflict{}, fmt.Errorf("%w: 冲突已按其他结果解决", ErrConflictStale)
	}
	if input.Revision <= 0 || input.Revision != conflict.Revision {
		return Conflict{}, fmt.Errorf("%w: revision %d != %d", ErrConflictStale, input.Revision, conflict.Revision)
	}
	if err := validateMemberVersions(conflict, input.MemberVersions); err != nil {
		return Conflict{}, err
	}
	if err := s.applyResolution(ctx, tx, &conflict, input); err != nil {
		return Conflict{}, err
	}
	payload, err := json.Marshal(s.resolutionPayload(input))
	if err != nil {
		return Conflict{}, fmt.Errorf("编码裁决结果: %w", err)
	}
	conflict, err = CompleteConflictResolution(ctx, tx, conflict, string(payload), s.timestamp())
	if err != nil {
		return Conflict{}, err
	}
	memberIDs := make([]string, 0, len(conflict.Members))
	for _, member := range conflict.Members {
		memberIDs = append(memberIDs, member.MemoryID)
	}
	if err := s.syncConflictChanged(ctx, tx, memberIDs,
		"conflict:"+conflict.ID+":"+strconv.Itoa(conflict.Revision)); err != nil {
		return Conflict{}, err
	}
	return tx.GetConflict(ctx, conflict.ID)
}

// CompleteConflictResolution seals an adjudication after its memory revisions
// have been written. Ingest and management share the same immutable audit path.
func CompleteConflictResolution(ctx context.Context, tx Tx, conflict Conflict, resolution string, now time.Time) (Conflict, error) {
	if conflict.Status != ConflictOpen {
		return Conflict{}, ErrConflictStale
	}
	conflict.Status = ConflictResolved
	conflict.Revision++
	conflict.UpdatedAt = now.UTC()
	conflict.Resolution = resolution
	if err := tx.UpdateConflict(ctx, conflict); err != nil {
		return Conflict{}, err
	}
	if err := tx.InsertConflictEvent(ctx, ConflictEvent{
		ID: idgen.NewUUID(), ConflictID: conflict.ID, Action: "resolved", CreatedAt: conflict.UpdatedAt,
	}); err != nil {
		return Conflict{}, err
	}
	if _, err := tx.CancelJob(ctx, task.KindReviewConflict, task.ConflictKey(conflict.ID), conflict.UpdatedAt); err != nil {
		return Conflict{}, err
	}
	if err := tx.BumpCatalogGeneration(ctx); err != nil {
		return Conflict{}, err
	}
	return conflict, nil
}

func (s *Service) resolutionPayload(input ResolveInput) resolutionPayload {
	return resolutionPayload{
		Action:    input.Action,
		MemberIDs: append([]string(nil), input.MemberIDs...),
		Reason:    input.Reason,
	}
}

func validateMemberVersions(conflict Conflict, supplied []ConflictMember) error {
	if len(supplied) == 0 {
		return fmt.Errorf("%w: 必须提交冲突成员版本", ErrInvalidResolution)
	}
	current := make(map[string]ConflictMember, len(conflict.Members))
	for _, member := range conflict.Members {
		current[member.MemoryID] = member
	}
	seen := make(map[string]struct{}, len(supplied))
	for _, member := range supplied {
		if _, duplicate := seen[member.MemoryID]; duplicate {
			return fmt.Errorf("%w: 成员 %s 重复", ErrInvalidResolution, member.MemoryID)
		}
		detected, ok := current[member.MemoryID]
		if !ok {
			return fmt.Errorf("%w: 成员 %s 不属于该冲突", ErrInvalidResolution, member.MemoryID)
		}
		expected := detected.Version
		if detected.CurrentVersion > 0 {
			expected = detected.CurrentVersion
		}
		if member.Version != expected {
			return fmt.Errorf("%w: 成员 %s 版本已变化", ErrConflictStale, member.MemoryID)
		}
		seen[member.MemoryID] = struct{}{}
	}
	if len(seen) != len(current) {
		return fmt.Errorf("%w: 必须提交全部成员版本", ErrConflictStale)
	}
	return nil
}

func (s *Service) applyResolution(ctx context.Context, tx Tx, conflict *Conflict, input ResolveInput) error {
	switch input.Action {
	case ResolutionChoose:
		return s.resolveChoose(ctx, tx, conflict, input)
	case ResolutionRejectAll:
		return s.resolveRejectAll(ctx, tx, conflict)
	default:
		return fmt.Errorf("%w: action %q", ErrInvalidResolution, input.Action)
	}
}

func (s *Service) resolveChoose(ctx context.Context, tx Tx, conflict *Conflict, input ResolveInput) error {
	if len(input.MemberIDs) != 1 {
		return fmt.Errorf("%w: choose 必须选择一个成员", ErrInvalidResolution)
	}
	chosenID := input.MemberIDs[0]
	chosen, err := s.conflictMember(ctx, tx, conflict, chosenID)
	if err != nil {
		return err
	}
	if err := s.setAssertion(ctx, tx, chosen, AssertionAccepted, OperationResolve); err != nil {
		return err
	}
	for _, member := range conflict.Members {
		if member.MemoryID == chosenID {
			continue
		}
		other, err := tx.GetMemory(ctx, member.MemoryID)
		if err != nil {
			return err
		}
		if err := s.setAssertion(ctx, tx, other, AssertionRejected, OperationResolve); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) resolveRejectAll(ctx context.Context, tx Tx, conflict *Conflict) error {
	for _, member := range conflict.Members {
		item, err := tx.GetMemory(ctx, member.MemoryID)
		if err != nil {
			return err
		}
		if err := s.setAssertion(ctx, tx, item, AssertionRejected, OperationResolve); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) conflictMember(ctx context.Context, tx Tx, conflict *Conflict, memoryID string) (Memory, error) {
	for _, member := range conflict.Members {
		if member.MemoryID == memoryID {
			return tx.GetMemory(ctx, memoryID)
		}
	}
	return Memory{}, fmt.Errorf("%w: 成员 %s 不属于该冲突", ErrInvalidResolution, memoryID)
}

func (s *Service) setAssertion(ctx context.Context, tx Tx, item Memory, status AssertionStatus, operation Operation) error {
	if item.AssertionStatus == status {
		return nil
	}
	evidence, err := tx.ListRevisionEvidence(ctx, item.ID, item.Version)
	if err != nil {
		return err
	}
	_, err = s.Revise(ctx, tx, ReviseInput{
		MemoryID:        item.ID,
		Kind:            item.Kind,
		Title:           item.Title,
		Content:         item.Content,
		AssertionStatus: status,
		ScopeKey:        item.ScopeKey,
		Evidence:        evidence,
		Operation:       operation,
	})
	return err
}
