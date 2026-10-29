package governance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

// DependencyRef and DependencyType are the generic derived-input references
// owned by artifact. They are aliased here so the invalidation entry point reads
// naturally in governance code without duplicating the vocabulary.
type DependencyRef = artifact.DependencyRef
type DependencyType = artifact.DependencyType

const (
	// ReasonSourceRedacted is written when a source withdrawal invalidates an
	// observation, canonical view or derived object.
	ReasonSourceRedacted = "source_redacted"
	// ReasonObservationDeleted is written when one L0 observation is withdrawn.
	ReasonObservationDeleted = "observation_deleted"
	// ReasonObservationRetracted is written when one L0 observation is retracted.
	ReasonObservationRetracted = "observation_retracted"
)

// InvalidateDependents propagates one invalid input through the complete
// derived_dependencies graph. It walks breadth-first rather than handling only
// the first artifact layer: every invalidated derived object becomes a new input
// for its own dependents.
//
// A dependent with remaining or rebuildable lower-layer inputs is marked stale
// and receives an immediate rebuild task. A dependent with no legal input at all
// is disabled and its pending task is cancelled. The current derived object
// always leaves the default index before this function returns.
func InvalidateDependents(ctx context.Context, tx Tx, input DependencyRef, reason string) error {
	_, err := invalidateDependents(ctx, tx, input, reason, time.Now().UTC())
	return err
}

func invalidateDependents(ctx context.Context, tx Tx, input DependencyRef, reason string,
	now time.Time) ([]string, error) {
	if tx == nil {
		return nil, errors.New("governance: invalidation transaction is missing")
	}
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" || !input.Type.Known() {
		return nil, fmt.Errorf("%w: dependency %q/%q", artifact.ErrInvalidInput, input.Type, input.ID)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "input_invalidated"
	}
	now = now.UTC()

	queue := []DependencyRef{input}
	visitedInputs := make(map[string]struct{})
	visitedDerived := make(map[string]struct{})
	impacted := make(map[string]struct{})

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		key := dependencyKey(current)
		if _, visited := visitedInputs[key]; visited {
			continue
		}
		visitedInputs[key] = struct{}{}

		dependents, err := tx.ListDependents(ctx, current)
		if err != nil {
			return nil, err
		}
		for _, dependent := range dependents {
			if strings.TrimSpace(dependent.ID) == "" {
				continue
			}
			if _, visited := visitedDerived[dependent.ID]; visited {
				continue
			}
			visitedDerived[dependent.ID] = struct{}{}
			impacted[dependent.ID] = struct{}{}

			target, targetErr := invalidationTarget(dependent)
			if targetErr != nil {
				// Unknown target metadata cannot rebuild through the task
				// system, but the current result must still leave default recall.
				if dependent.State != artifact.StateDisabled {
					if err := tx.MarkDerivedStale(ctx, dependent.ID, reason); err != nil {
						return nil, err
					}
				}
				queue = append(queue, DependencyRef{
					Type: artifact.DependencyDerived,
					ID:   dependent.ID,
				})
				continue
			}

			eligible, err := tx.CountDerivedInputs(ctx, target)
			if err != nil {
				return nil, err
			}
			candidates := eligible
			if candidates == 0 {
				candidates, err = tx.CountDerivedInputCandidates(ctx, target)
				if err != nil {
					return nil, err
				}
			}
			if candidates > 0 {
				if err := tx.MarkDerivedStale(ctx, dependent.ID, reason); err != nil {
					return nil, err
				}
				if err := enqueueInvalidatedTarget(ctx, tx, target, dependent.ID, reason, now); err != nil {
					return nil, err
				}
			} else {
				if err := tx.MarkDerivedDisabled(ctx, dependent.ID, reason); err != nil {
					return nil, err
				}
				if _, err := tx.CancelJob(ctx, invalidationJobKind(target), target.TargetKey(), now); err != nil {
					return nil, err
				}
			}

			queue = append(queue, DependencyRef{
				Type: artifact.DependencyDerived,
				ID:   dependent.ID,
			})
		}
	}

	result := make([]string, 0, len(impacted))
	for id := range impacted {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func invalidationTarget(item artifact.DerivedRef) (artifact.Target, error) {
	target, err := artifact.TargetForArtifact(artifact.DerivedMemory{
		ID:             item.ID,
		Layer:          item.Layer,
		Kind:           item.Kind,
		TargetKey:      item.TargetKey,
		ScopeKey:       item.ScopeKey,
		Topic:          item.Topic,
		CurrentVersion: item.Version,
		State:          item.State,
	})
	if err != nil {
		return artifact.Target{}, err
	}
	if target.Kind == "" || !target.Kind.Known() || target.TargetKey() == "" {
		return artifact.Target{}, fmt.Errorf("%w: dependent %s", artifact.ErrInvalidInput, item.ID)
	}
	return target, nil
}

func enqueueInvalidatedTarget(ctx context.Context, tx Tx, target artifact.Target,
	derivedID, reason string, now time.Time) error {
	start := now
	if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
		Kind:      invalidationJobKind(target),
		TargetKey: target.TargetKey(),
		InputHash: task.InputHash("invalidate-dependency", target.TargetKey(), derivedID,
			reason, now.Format(time.RFC3339Nano)),
		NextAttemptAt: &start,
		Now:           now,
	}); err != nil {
		return fmt.Errorf("登记失效重建任务 %s: %w", target.TargetKey(), err)
	}
	return nil
}

func invalidationJobKind(target artifact.Target) task.Kind {
	switch target.LayerOrDefault() {
	case artifact.LayerL1:
		return task.KindAggregateL1
	case artifact.LayerL2:
		return task.KindAggregateL2
	case artifact.LayerL3:
		return task.KindAggregateL3
	case artifact.LayerL4:
		return task.KindBuildGuidance
	default:
		return task.KindReconcile
	}
}

func dependencyKey(ref DependencyRef) string {
	return strings.Join([]string{string(ref.Type), ref.ID, strconv.Itoa(ref.Version)}, "\x00")
}
