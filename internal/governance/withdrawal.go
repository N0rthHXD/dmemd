package governance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ObservationImpact reports the committed effects of one observation
// withdrawal. ObservationIDs always contains the target; MemoryIDs contains the
// canonical views deleted because the target was their only remaining source.
type ObservationImpact struct {
	ObservationID      string   `json:"observation_id"`
	MemoryIDs          []string `json:"memory_ids"`
	InvalidatedDerived []string `json:"invalidated_derived_ids"`
}

// ObservationLifecycleService withdraws one L0 observation and propagates the
// change through canonical memories and the complete derived graph.
type ObservationLifecycleService interface {
	Delete(ctx context.Context, id string) (ObservationImpact, error)
	Retract(ctx context.Context, id string) (ObservationImpact, error)
}

// ObservationLifecycleAPI applies logical observation deletion or retraction in
// one short transaction. Observation content is never rewritten.
type ObservationLifecycleAPI struct {
	store    Store
	memories memory.Writer
	now      func() time.Time
}

// ObservationLifecycleOption customizes the lifecycle service.
type ObservationLifecycleOption func(*ObservationLifecycleAPI)

// WithObservationLifecycleClock overrides the service clock.
func WithObservationLifecycleClock(now func() time.Time) ObservationLifecycleOption {
	return func(service *ObservationLifecycleAPI) {
		if now != nil {
			service.now = now
		}
	}
}

// NewObservationLifecycleService assembles observation withdrawal over the same
// short transaction used by governance commits.
func NewObservationLifecycleService(store Store, memories memory.Writer,
	options ...ObservationLifecycleOption) *ObservationLifecycleAPI {
	service := &ObservationLifecycleAPI{store: store, memories: memories, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

var _ ObservationLifecycleService = (*ObservationLifecycleAPI)(nil)

// Delete marks one observation deleted and invalidates every dependent view.
func (s *ObservationLifecycleAPI) Delete(ctx context.Context, id string) (ObservationImpact, error) {
	return s.withdraw(ctx, id, observation.StatusDeleted)
}

// Retract marks one observation retracted and invalidates every dependent view.
func (s *ObservationLifecycleAPI) Retract(ctx context.Context, id string) (ObservationImpact, error) {
	return s.withdraw(ctx, id, observation.StatusRetracted)
}

func (s *ObservationLifecycleAPI) withdraw(ctx context.Context, id string,
	status observation.Status) (ObservationImpact, error) {
	if s == nil || s.store == nil {
		return ObservationImpact{}, errors.New("governance: observation lifecycle store not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ObservationImpact{}, fmt.Errorf("%w: empty observation id", observation.ErrNotFound)
	}
	var result ObservationImpact
	err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		item, err := tx.GetObservation(ctx, id)
		if err != nil {
			return err
		}
		if item.Status == status ||
			(status == observation.StatusRetracted && item.Status == observation.StatusDeleted) {
			result = ObservationImpact{ObservationID: id}
			return nil
		}
		reason := ReasonObservationDeleted
		if status == observation.StatusRetracted {
			reason = ReasonObservationRetracted
		}
		outcome, err := withdrawObservations(ctx, tx, s.memories,
			[]observation.Observation{item}, status, s.now().UTC(), reason)
		if err != nil {
			return err
		}
		result = ObservationImpact{
			ObservationID:      id,
			MemoryIDs:          outcome.DeletedMemoryIDs,
			InvalidatedDerived: outcome.InvalidatedDerived,
		}
		return nil
	})
	if err != nil {
		return ObservationImpact{}, err
	}
	return result, nil
}

// RedactionPropagator withdraws the observations of one redacted source and
// invalidates every direct and indirect derived object. It runs inside the
// source redaction transaction.
type RedactionPropagator struct {
	memories memory.Writer
}

// NewRedactionPropagator assembles the governance half of source redaction.
func NewRedactionPropagator(memories memory.Writer) *RedactionPropagator {
	return &RedactionPropagator{memories: memories}
}

var _ source.RedactionPropagator = (*RedactionPropagator)(nil)

// PropagateRedaction implements source.RedactionPropagator.
func (p *RedactionPropagator) PropagateRedaction(ctx context.Context, sourceTx source.Tx,
	sourceID string, at time.Time) (source.RedactionImpact, error) {
	if p == nil {
		return source.RedactionImpact{}, nil
	}
	tx, ok := sourceTx.(Tx)
	if !ok {
		return source.RedactionImpact{}, errors.New("governance: source redaction transaction lacks governance capabilities")
	}
	items, err := tx.ListObservationsBySource(ctx, sourceID)
	if err != nil {
		return source.RedactionImpact{}, err
	}
	outcome, err := withdrawObservations(ctx, tx, p.memories, items,
		observation.StatusRetracted, at.UTC(), ReasonSourceRedacted)
	if err != nil {
		return source.RedactionImpact{}, err
	}
	return source.RedactionImpact{
		ObservationIDs:     outcome.ObservationIDs,
		InvalidatedDerived: outcome.InvalidatedDerived,
	}, nil
}

type withdrawalOutcome struct {
	ObservationIDs     []string
	DeletedMemoryIDs   []string
	InvalidatedDerived []string
}

// withdrawObservations applies one status transition to a set of observations,
// deletes canonical views that lose every remaining source, and propagates the
// resulting invalidation through the derived graph. All work happens in the
// caller's transaction.
func withdrawObservations(ctx context.Context, tx Tx, memories memory.Writer,
	items []observation.Observation, status observation.Status, now time.Time,
	reason string) (withdrawalOutcome, error) {
	outcome := withdrawalOutcome{}
	if len(items) == 0 {
		return outcome, nil
	}
	now = now.UTC()
	memoryIDs := make(map[string]struct{})
	observationIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		observationIDs[item.ID] = struct{}{}
		// A retraction never weakens an explicit deletion.
		if item.Status != status &&
			!(status == observation.StatusRetracted && item.Status == observation.StatusDeleted) {
			if err := tx.UpdateObservationStatus(ctx, item.ID, status, now); err != nil {
				return withdrawalOutcome{}, err
			}
		}
		// A queued or running governance task must never commit against a
		// withdrawn observation. Cancellation increments the task revision.
		if _, err := tx.CancelJob(ctx, task.KindGovernObservation,
			task.ObservationKey(item.ID), now); err != nil {
			return withdrawalOutcome{}, err
		}
		// The L1 job may exist before any artifact row was committed. Cancel it
		// explicitly; an existing artifact target is re-registered by the
		// invalidation pass below when other observations remain.
		if !item.CreatedAt.IsZero() && strings.TrimSpace(item.ScopeKey) != "" {
			l1 := artifact.Target{
				Layer: artifact.LayerL1, Kind: artifact.KindLocalSummary,
				ScopeKey: item.ScopeKey,
				Bucket:   artifact.L1BucketForTime(item.CreatedAt, artifact.DefaultL1BucketSize),
			}
			if _, err := tx.CancelJob(ctx, task.KindAggregateL1, l1.TargetKey(), now); err != nil {
				return withdrawalOutcome{}, err
			}
		}
		ids, err := tx.ListMemoryIDsForObservation(ctx, item.ID)
		if err != nil {
			return withdrawalOutcome{}, err
		}
		for _, memoryID := range ids {
			memoryIDs[memoryID] = struct{}{}
		}
	}

	for _, memoryID := range sortedStrings(memoryIDs) {
		current, err := tx.GetMemory(ctx, memoryID)
		if errors.Is(err, memory.ErrNotFound) {
			continue
		}
		if err != nil {
			return withdrawalOutcome{}, err
		}
		if current.State == memory.StateDeleted {
			continue
		}
		valid, err := remainingObservationRefs(ctx, tx, current)
		if err != nil {
			return withdrawalOutcome{}, err
		}
		if valid > 0 {
			// The canonical view still has a legitimate observation source.
			// Its body was not rewritten by this logical withdrawal.
			continue
		}
		if memories == nil {
			return withdrawalOutcome{}, errors.New("governance: memory writer not configured")
		}
		if _, err := memories.ChangeState(ctx, tx, memory.StateChangeInput{
			MemoryID: memoryID,
			State:    memory.StateDeleted,
			Reason:   reason,
		}); err != nil {
			return withdrawalOutcome{}, err
		}
		outcome.DeletedMemoryIDs = append(outcome.DeletedMemoryIDs, memoryID)
	}

	for _, observationID := range sortedStrings(observationIDs) {
		invalidated, err := invalidateDependents(ctx, tx, artifact.DependencyRef{
			Type: artifact.DependencyObservation,
			ID:   observationID,
		}, reason, now)
		if err != nil {
			return withdrawalOutcome{}, err
		}
		outcome.InvalidatedDerived = append(outcome.InvalidatedDerived, invalidated...)
	}
	for _, memoryID := range outcome.DeletedMemoryIDs {
		invalidated, err := invalidateDependents(ctx, tx, artifact.DependencyRef{
			Type: artifact.DependencyMemory,
			ID:   memoryID,
		}, reason, now)
		if err != nil {
			return withdrawalOutcome{}, err
		}
		outcome.InvalidatedDerived = append(outcome.InvalidatedDerived, invalidated...)
	}

	outcome.ObservationIDs = sortedStrings(observationIDs)
	outcome.InvalidatedDerived = uniqueSortedStrings(outcome.InvalidatedDerived)
	sort.Strings(outcome.DeletedMemoryIDs)
	return outcome, nil
}

// remainingObservationRefs counts the observations that still support the
// current canonical revision. A missing or withdrawn source is not legal input.
func remainingObservationRefs(ctx context.Context, tx Tx,
	current memory.Memory) (int, error) {
	refs, err := tx.ListMemoryObservationRefs(ctx, current.ID, current.Version)
	if err != nil {
		return 0, err
	}
	valid := 0
	for _, refID := range refs {
		item, err := tx.GetObservation(ctx, refID)
		if errors.Is(err, observation.ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if item.Status == observation.StatusDeleted || item.Status == observation.StatusRetracted {
			continue
		}
		src, err := tx.GetSource(ctx, item.SourceID)
		if errors.Is(err, source.ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if src.Redacted {
			continue
		}
		valid++
	}
	return valid, nil
}

func sortedStrings(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
