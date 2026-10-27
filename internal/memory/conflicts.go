package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
)

// NewFactGroup creates service-owned metadata. The model never supplies the ID.
func NewFactGroup(input FactGroupInput, now time.Time) (FactGroup, error) {
	cardinality, err := NormalizeCardinality(input.Cardinality)
	if err != nil {
		return FactGroup{}, err
	}
	now = now.UTC()
	return FactGroup{
		ID:          idgen.NewUUID(),
		Subject:     strings.TrimSpace(input.Subject),
		Predicate:   strings.TrimSpace(input.Predicate),
		Scope:       strings.TrimSpace(input.Scope),
		Cardinality: cardinality,
		Generation:  1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// ListConflicts returns a fixed-order management list.
func (s *Service) ListConflicts(ctx context.Context, options ConflictListOptions) ([]Conflict, error) {
	return s.reader.ListConflicts(ctx, options)
}

// GetConflict returns one conflict with its members and immutable events.
func (s *Service) GetConflict(ctx context.Context, id string) (Conflict, error) {
	return s.reader.GetConflict(ctx, id)
}

type resolutionPayload struct {
	Action        ResolutionAction    `json:"action"`
	MemberIDs     []string            `json:"member_ids,omitempty"`
	SeparateGroup *FactGroupInput     `json:"separate_group,omitempty"`
	Segments      []resolutionSegment `json:"segments,omitempty"`
	Reason        string              `json:"reason,omitempty"`
}

type resolutionSegment struct {
	MemoryID  string     `json:"memory_id,omitempty"`
	Content   string     `json:"content,omitempty"`
	ValidFrom *time.Time `json:"valid_from,omitempty"`
	ValidTo   *time.Time `json:"valid_to,omitempty"`
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
// versions, intervals, assertion changes and derived invalidation stay
// identical. Resolving cancels any queued review for the same conflict.
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
	payload := resolutionPayload{
		Action:        input.Action,
		MemberIDs:     append([]string(nil), input.MemberIDs...),
		SeparateGroup: input.SeparateGroup,
		Reason:        input.Reason,
	}
	for _, segment := range input.Timeline {
		payload.Segments = append(payload.Segments, resolutionSegment{
			MemoryID:  segment.MemoryID,
			Content:   segment.Content,
			ValidFrom: UTCPointer(segment.ValidFrom),
			ValidTo:   UTCPointer(segment.ValidTo),
		})
	}
	return payload
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
		// The detail exposes both detection and current versions. If the
		// current version is known, the client must adjudicate that snapshot.
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
	case ResolutionTimeline:
		return s.resolveTimeline(ctx, tx, conflict, input)
	case ResolutionSeparateScope:
		return s.resolveSeparate(ctx, tx, conflict, input)
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
	for _, member := range conflict.Members {
		if member.MemoryID == chosenID {
			continue
		}
		other, err := tx.GetMemory(ctx, member.MemoryID)
		if err != nil {
			return err
		}
		if !Overlaps(chosen.ValidFrom, chosen.ValidTo, other.ValidFrom, other.ValidTo) {
			return fmt.Errorf("%w: 成员时间段不重叠，必须使用 timeline", ErrInvalidResolution)
		}
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

func (s *Service) resolveSeparate(ctx context.Context, tx Tx, conflict *Conflict, input ResolveInput) error {
	if input.SeparateGroup == nil {
		return fmt.Errorf("%w: separate_scope 需要新的主体或场景", ErrInvalidResolution)
	}
	group, err := NewFactGroup(*input.SeparateGroup, s.timestamp())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidResolution, err)
	}
	if err := tx.InsertFactGroup(ctx, group); err != nil {
		return err
	}
	for _, member := range conflict.Members {
		item, err := tx.GetMemory(ctx, member.MemoryID)
		if err != nil {
			return err
		}
		evidence, err := tx.ListRevisionEvidence(ctx, item.ID, item.Version)
		if err != nil {
			return err
		}
		updated, err := s.Revise(ctx, tx, ReviseInput{
			MemoryID:        item.ID,
			Kind:            item.Kind,
			Title:           item.Title,
			Content:         item.Content,
			ObservedAt:      item.ObservedAt,
			ValidFrom:       item.ValidFrom,
			ValidTo:         item.ValidTo,
			ValidFromBasis:  item.ValidFromBasis,
			ValidToBasis:    item.ValidToBasis,
			AssertionStatus: item.AssertionStatus,
			FactGroupID:     group.ID,
			Evidence:        evidence,
			Operation:       OperationResolve,
			Reason:          input.Reason,
		})
		if err != nil {
			return err
		}
		if updated.ID != item.ID {
			return errors.New("memory: 重新归组不应更换记忆 ID")
		}
	}
	return nil
}

func (s *Service) resolveTimeline(ctx context.Context, tx Tx, conflict *Conflict, input ResolveInput) error {
	if len(input.Timeline) == 0 {
		return fmt.Errorf("%w: timeline 至少需要一个分段", ErrInvalidResolution)
	}
	// Validate non-overlapping, non-empty segments first. A timeline segment
	// must have both bounds; unknown boundaries require returning to unresolved.
	previousTo := (*time.Time)(nil)
	for _, segment := range input.Timeline {
		if segment.ValidFrom == nil || segment.ValidTo == nil {
			return fmt.Errorf("%w: 时间线分段必须提供明确起止", ErrInvalidResolution)
		}
		timeRange, err := NormalizeTimeRange(segment.ValidFrom, segment.ValidTo, BasisInferred, BasisInferred)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidResolution, err)
		}
		if previousTo != nil && previousTo.After(*timeRange.From) {
			return fmt.Errorf("%w: 时间线分段必须递增且不重叠", ErrInvalidResolution)
		}
		previousTo = timeRange.To
	}

	covered := make(map[string]struct{}, len(input.Timeline))
	for _, segment := range input.Timeline {
		if segment.MemoryID != "" {
			item, err := s.conflictMember(ctx, tx, conflict, segment.MemoryID)
			if err != nil {
				return err
			}
			content := strings.TrimSpace(segment.Content)
			if content == "" {
				content = item.Content
			}
			evidence, err := tx.ListRevisionEvidence(ctx, item.ID, item.Version)
			if err != nil {
				return err
			}
			if _, err := s.Revise(ctx, tx, ReviseInput{
				MemoryID:        item.ID,
				Kind:            item.Kind,
				Title:           item.Title,
				Content:         content,
				ObservedAt:      item.ObservedAt,
				ValidFrom:       segment.ValidFrom,
				ValidTo:         segment.ValidTo,
				ValidFromBasis:  BasisInferred,
				ValidToBasis:    BasisInferred,
				AssertionStatus: AssertionAccepted,
				FactGroupID:     item.FactGroupID,
				Evidence:        evidence,
				Operation:       OperationResolve,
				Reason:          input.Reason,
			}); err != nil {
				return err
			}
			covered[item.ID] = struct{}{}
			continue
		}
		if strings.TrimSpace(segment.Content) == "" {
			return fmt.Errorf("%w: 新分段缺少正文", ErrInvalidResolution)
		}
		timeRange, _ := NormalizeTimeRange(segment.ValidFrom, segment.ValidTo, BasisInferred, BasisInferred)
		created, err := s.Create(ctx, tx, CreateInput{
			Kind:            KindFact,
			Title:           defaultTitle(segment.Content),
			Content:         strings.TrimSpace(segment.Content),
			ObservedAt:      s.timestamp(),
			ValidFrom:       timeRange.From,
			ValidTo:         timeRange.To,
			ValidFromBasis:  timeRange.FromBasis,
			ValidToBasis:    timeRange.ToBasis,
			AssertionStatus: AssertionAccepted,
			FactGroupID:     conflict.FactGroupID,
			Reason:          input.Reason,
			Operation:       OperationResolve,
		})
		if err != nil {
			return err
		}
		covered[created.ID] = struct{}{}
	}
	for _, member := range conflict.Members {
		if _, ok := covered[member.MemoryID]; ok {
			continue
		}
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
		ObservedAt:      item.ObservedAt,
		ValidFrom:       item.ValidFrom,
		ValidTo:         item.ValidTo,
		ValidFromBasis:  item.ValidFromBasis,
		ValidToBasis:    item.ValidToBasis,
		AssertionStatus: status,
		FactGroupID:     item.FactGroupID,
		Evidence:        evidence,
		Operation:       operation,
	})
	return err
}

func defaultTitle(content string) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) > 40 {
		runes = runes[:40]
	}
	return strings.TrimSpace(string(runes))
}
