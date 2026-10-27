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
)

// ErrInvalidForget marks an empty or otherwise malformed permanent-forget
// request. Unknown identifiers are reported as their ordinary not-found errors.
var ErrInvalidForget = errors.New("invalid forget request")

// ForgetRequest names leaves to forget permanently. At least one list must
// contain an identifier. Sources expand through origins and evidence in every
// revision, then memories expand through safe same-group duplicates. Runs also
// remove their dedicated report source.
type ForgetRequest struct {
	MemoryIDs     []string `json:"memory_ids,omitempty"`
	SourceIDs     []string `json:"source_ids,omitempty"`
	ExperienceIDs []string `json:"experience_ids,omitempty"`
}

// ForgetResult lists the identifiers removed by one committed transaction.
type ForgetResult struct {
	MemoryIDs                []string `json:"memory_ids"`
	SourceIDs                []string `json:"source_ids"`
	ExperienceIDs            []string `json:"experience_ids"`
	Warnings                 []string `json:"warnings,omitempty"`
	RetainedRelatedMemoryIDs []string `json:"retained_related_memory_ids,omitempty"`
}

// ForgetService is the HTTP-facing permanent-forget capability.
type ForgetService interface {
	Forget(ctx context.Context, request ForgetRequest) (ForgetResult, error)
}

// MemoryPurger removes one atomic memory and its dependent derived objects in
// the caller's transaction.
type MemoryPurger interface {
	PurgeInTx(ctx context.Context, tx memory.Tx, id string) error
}

// ForgetServiceAPI performs explicit permanent deletion in one short
// transaction. Enumeration happens before deletion, so no foreign-key cascade
// can hide a dependent artifact, evidence row or application report.
type ForgetServiceAPI struct {
	store    Store
	memories MemoryPurger
	now      func() time.Time
}

// ForgetOption customizes the forget service.
type ForgetOption func(*ForgetServiceAPI)

// WithForgetClock overrides the service clock.
func WithForgetClock(now func() time.Time) ForgetOption {
	return func(service *ForgetServiceAPI) {
		if now != nil {
			service.now = now
		}
	}
}

// NewForgetService assembles permanent forget over the unified short
// transaction.
func NewForgetService(store Store, memories MemoryPurger, options ...ForgetOption) *ForgetServiceAPI {
	service := &ForgetServiceAPI{store: store, memories: memories, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

var _ ForgetService = (*ForgetServiceAPI)(nil)

// Forget permanently removes the requested scope. The whole operation is one
// transaction: a missing identifier or storage failure leaves every requested
// row intact and returns an error.
func (s *ForgetServiceAPI) Forget(ctx context.Context, request ForgetRequest) (ForgetResult, error) {
	if s.store == nil || s.memories == nil {
		return ForgetResult{}, errors.New("governance: forget service not configured")
	}
	explicitMemory := cleanIDs(request.MemoryIDs)
	explicitSource := cleanIDs(request.SourceIDs)
	explicitExperience := cleanIDs(request.ExperienceIDs)
	if len(explicitMemory)+len(explicitSource)+len(explicitExperience) == 0 {
		return ForgetResult{}, fmt.Errorf("%w: at least one identifier is required", ErrInvalidForget)
	}

	var result ForgetResult
	err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		now := s.now().UTC()
		memoryIDs := make(map[string]struct{}, len(explicitMemory))
		sourceIDs := make(map[string]struct{}, len(explicitSource))
		experienceIDs := make(map[string]struct{}, len(explicitExperience))

		for _, id := range explicitMemory {
			if _, err := tx.GetMemory(ctx, id); err != nil {
				return err
			}
			memoryIDs[id] = struct{}{}
		}
		for _, id := range explicitSource {
			if _, err := tx.GetSource(ctx, id); err != nil {
				return err
			}
			sourceIDs[id] = struct{}{}
		}
		for _, id := range explicitExperience {
			run, err := tx.GetExperienceRun(ctx, id)
			if err != nil {
				return err
			}
			experienceIDs[id] = struct{}{}
			if run.SourceID != "" {
				sourceIDs[run.SourceID] = struct{}{}
			}
		}

		// Expand every source before deleting anything. Associated memories
		// are removed conservatively even when they also used other sources.
		for _, sourceID := range sortedSet(sourceIDs) {
			ids, err := tx.ListMemoryIDsForSource(ctx, sourceID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				memoryIDs[id] = struct{}{}
			}
			runs, err := tx.ListExperienceIDsForSource(ctx, sourceID)
			if err != nil {
				return err
			}
			for _, id := range runs {
				experienceIDs[id] = struct{}{}
			}
		}

		retained, err := expandForgetDuplicates(ctx, tx, memoryIDs)
		if err != nil {
			return err
		}
		for _, id := range sortedSet(experienceIDs) {
			if err := s.purgeExperienceInTx(ctx, tx, id, now); err != nil {
				return err
			}
		}
		for _, id := range sortedSet(memoryIDs) {
			if err := tx.DeleteForgetConflicts(ctx, id, now); err != nil {
				return err
			}
			if err := s.memories.PurgeInTx(ctx, tx, id); err != nil {
				return err
			}
		}
		for _, sourceID := range sortedSet(sourceIDs) {
			// Ingest jobs and evidence use RESTRICT foreign keys; remove both
			// before the source row itself.
			if err := tx.DeleteIngestJobsBySource(ctx, sourceID); err != nil {
				return err
			}
			if err := tx.DeleteEvidenceBySource(ctx, sourceID); err != nil {
				return err
			}
			if err := tx.DeleteSource(ctx, sourceID); err != nil {
				return err
			}
		}

		result = ForgetResult{
			MemoryIDs:                sortedSet(memoryIDs),
			SourceIDs:                sortedSet(sourceIDs),
			ExperienceIDs:            sortedSet(experienceIDs),
			RetainedRelatedMemoryIDs: retained,
		}
		if len(retained) > 0 {
			result.Warnings = []string{"related_memories_retained"}
		}
		return nil
	})
	if err != nil {
		return ForgetResult{}, err
	}
	return result, nil
}

// purgeExperienceInTx removes one application report and every artifact whose
// current or historical revision used it. Skill targets are requeued from
// remaining inputs; the report's own case target is cancelled.
func (s *ForgetServiceAPI) purgeExperienceInTx(ctx context.Context, tx Tx, experienceID string, now time.Time) error {
	run, err := tx.GetExperienceRun(ctx, experienceID)
	if err != nil {
		return err
	}
	caseTarget := artifact.ExperienceTarget(experienceID).TargetKey()
	if _, err := tx.CancelJob(ctx, task.KindAggregate, caseTarget, now); err != nil {
		return err
	}
	if run.ScopeKey != "" {
		if _, err := tx.BumpSkillJobsForScope(ctx, run.ScopeKey, now); err != nil {
			return err
		}
	}
	if run.SkillID != "" {
		item, err := tx.GetArtifact(ctx, run.SkillID)
		switch {
		case err == nil && item.Kind == artifact.KindSkill:
			if err := requeueSkillAfterForget(ctx, tx, item, now); err != nil {
				return err
			}
		case err != nil && !errors.Is(err, artifact.ErrNotFound):
			return err
		}
	}

	affected, err := tx.ListAllArtifactsForRun(ctx, experienceID)
	if err != nil {
		return err
	}
	for _, item := range affected {
		if item.Kind == artifact.KindExperienceCase {
			target, targetErr := artifact.TargetForArtifact(item)
			if targetErr != nil {
				continue
			}
			if _, err := tx.CancelJob(ctx, task.KindAggregate, target.TargetKey(), now); err != nil {
				return err
			}
			continue
		}
		if item.Kind != artifact.KindSkill {
			continue
		}
		if err := requeueSkillAfterForget(ctx, tx, item, now); err != nil {
			return err
		}
	}
	if err := tx.DeleteArtifactsForRun(ctx, experienceID); err != nil {
		return err
	}
	return tx.DeleteExperienceRun(ctx, experienceID)
}

func requeueSkillAfterForget(ctx context.Context, tx Tx, item artifact.Artifact, now time.Time) error {
	target, err := artifact.TargetForArtifact(item)
	if err != nil {
		return nil
	}
	if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
		Kind:          task.KindBuildSkill,
		TargetKey:     target.TargetKey(),
		InputHash:     task.InputHash("forget-experience", item.ID, now.Format(time.RFC3339Nano)),
		NextAttemptAt: &now,
		Now:           now,
	}); err != nil {
		return fmt.Errorf("登记 Skill 清除后重建任务: %w", err)
	}
	return nil
}

func cleanIDs(values []string) []string {
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
	return result
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
