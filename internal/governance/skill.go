package governance

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

// enqueueExperienceSkills registers the L4 skill targets affected by a new,
// changed or retracted application run. A run with an explicit skill reference
// invalidates exactly that skill. Otherwise the scope-level fallback target is
// registered; the handler decides whether a stable procedure exists. No raw
// observation is ever selected for an L4 skill.
func enqueueExperienceSkills(ctx context.Context, tx Tx, run ExperienceRun, now time.Time) error {
	targets := make(map[string]artifact.Target)
	if skillID := strings.TrimSpace(run.SkillID); skillID != "" {
		item, err := tx.GetArtifact(ctx, skillID)
		switch {
		case err == nil && item.Kind == artifact.KindSkill:
			target, targetErr := artifact.TargetForArtifact(item)
			if targetErr == nil {
				targets[target.TargetKey()] = target
			}
		case err == nil:
			// An explicit reference to a non-skill object is not evidence that a
			// skill should be rebuilt.
		case errors.Is(err, artifact.ErrNotFound):
		default:
			return err
		}
	}
	if len(targets) == 0 && strings.TrimSpace(run.ScopeKey) != "" {
		for _, target := range artifact.SkillTargets(run.ScopeKey, nil) {
			targets[target.TargetKey()] = target
		}
	}
	for _, target := range targets {
		if err := invalidateGuidanceTarget(ctx, tx, target, "experience:"+run.ID, now); err != nil {
			return err
		}
	}
	return nil
}

// invalidateExperienceDependents walks the generic derived-dependency edges of
// one application run. Experience cases leave default recall; skills that used
// the run are marked stale and registered for bounded regeneration.
func invalidateExperienceDependents(ctx context.Context, tx Tx, experienceID string, now time.Time) error {
	dependents, err := tx.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyExperience, ID: experienceID,
	})
	if err != nil {
		return err
	}
	for _, dependent := range dependents {
		if dependent.Kind == artifact.KindExperienceCase {
			item, err := tx.GetArtifact(ctx, dependent.ID)
			if errors.Is(err, artifact.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if err := disableArtifact(ctx, tx, item, now); err != nil {
				return err
			}
			continue
		}
		if dependent.Layer != artifact.LayerL4 && artifact.LayerForKind(dependent.Kind) != artifact.LayerL4 {
			continue
		}
		target, err := artifact.TargetForArtifact(artifact.DerivedMemory{
			ID: dependent.ID, Layer: dependent.Layer, Kind: dependent.Kind,
			TargetKey: dependent.TargetKey, ScopeKey: dependent.ScopeKey, Topic: dependent.Topic,
		})
		if err != nil {
			continue
		}
		if err := invalidateGuidanceTarget(ctx, tx, target,
			"experience-revoked:"+experienceID, now); err != nil {
			return err
		}
	}
	return nil
}

// invalidateGuidanceTarget takes one current L4 object out of default recall and
// registers a new build in the same short transaction.
func invalidateGuidanceTarget(ctx context.Context, tx Tx, target artifact.Target, reason string, now time.Time) error {
	if target.LayerOrDefault() != artifact.LayerL4 || strings.TrimSpace(target.TargetKey()) == "" {
		return nil
	}
	item, err := tx.GetArtifactByTarget(ctx, target.Kind, target.TargetKey())
	switch {
	case err == nil:
		if item.State == artifact.StateActive {
			item.State = artifact.StateStale
			item.UpdatedAt = now
			if err := tx.UpdateArtifact(ctx, item); err != nil {
				return err
			}
		}
		if err := tx.DeleteArtifactSearchDocument(ctx, item.ID); err != nil {
			return err
		}
	case errors.Is(err, artifact.ErrNotFound):
	default:
		return err
	}
	_, _, err = tx.UpsertJob(ctx, task.UpsertInput{
		Kind:      task.KindBuildGuidance,
		TargetKey: target.TargetKey(),
		InputHash: task.InputHash("guidance-invalidate", target.TargetKey(), reason,
			strconv.Itoa(item.CurrentVersion), now.UTC().Format(time.RFC3339Nano)),
		NextAttemptAt: &now,
		Now:           now,
	})
	if err != nil {
		return fmt.Errorf("登记 L4 重建任务: %w", err)
	}
	return nil
}
