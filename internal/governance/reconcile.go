package governance

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

// ReconcileHandler scans a bounded keyset batch for memories that never got a
// classify target, re-queues stale running tasks and registers debounced
// follow-up work. The cursor is intentionally in memory: a restart is allowed to
// scan from the beginning and uniqueness prevents duplicate tasks.
type ReconcileHandler struct {
	store      Store
	artifacts  *artifact.Service
	debounce   time.Duration
	batchSize  int
	staleAfter time.Duration
	now        func() time.Time

	mu            sync.Mutex
	cursor        string
	staleCursor   string
	expiredCursor string
	groupCursor   string
	skillCursor   string
}

// ReconcileOption customizes the reconcile handler.
type ReconcileOption func(*ReconcileHandler)

// WithReconcileClock overrides the handler clock for deterministic tests.
func WithReconcileClock(now func() time.Time) ReconcileOption {
	return func(handler *ReconcileHandler) {
		if now != nil {
			handler.now = now
		}
	}
}

// WithArtifactReconcile enables bounded derived-target scanning. Without it,
// reconcile only repairs missing classification tasks.
func WithArtifactReconcile(artifacts *artifact.Service) ReconcileOption {
	return func(handler *ReconcileHandler) {
		if artifacts != nil {
			handler.artifacts = artifacts
		}
	}
}

// NewReconcileHandler assembles the bounded periodic scan.
func NewReconcileHandler(store Store, debounce time.Duration, batchSize int, options ...ReconcileOption) *ReconcileHandler {
	if batchSize <= 0 {
		batchSize = 100
	}
	if debounce < 0 {
		debounce = 0
	}
	handler := &ReconcileHandler{
		store:      store,
		debounce:   debounce,
		batchSize:  batchSize,
		staleAfter: 30 * time.Minute,
		now:        time.Now,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// Process implements Handler.
func (h *ReconcileHandler) Process(ctx context.Context, job task.Job) error {
	if job.Kind != task.KindReconcile {
		return nil
	}
	now := h.now().UTC()
	// Keep one reconcile tick bounded across all scans instead of letting each
	// category consume a full batch in the same write transaction.
	scanLimit := h.batchSize / 6
	if scanLimit < 1 {
		scanLimit = 1
	}
	h.mu.Lock()
	after := h.cursor
	staleAfter := h.staleCursor
	expiredAfter := h.expiredCursor
	groupAfter := h.groupCursor
	skillAfter := h.skillCursor
	h.mu.Unlock()
	nextCursor := after
	nextStale := staleAfter
	nextExpired := expiredAfter
	nextGroup := groupAfter
	nextSkill := skillAfter

	err := h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		if _, err := tx.RequeueStaleRunningGovernanceJobs(ctx, now.Add(-h.staleAfter), now); err != nil {
			return err
		}
		ids, err := tx.ListMemoryIDsMissingJob(ctx, task.KindClassify, after, scanLimit)
		if err != nil {
			return err
		}
		due := now.Add(h.debounce)
		for _, id := range ids {
			if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
				Kind:          task.KindClassify,
				TargetKey:     task.MemoryKey(id),
				InputHash:     "",
				NextAttemptAt: &due,
				Now:           now,
			}); err != nil {
				return err
			}
		}
		if len(ids) < scanLimit {
			nextCursor = ""
		} else if len(ids) > 0 {
			nextCursor = ids[len(ids)-1]
		}

		if h.artifacts != nil {
			staleTargets, err := h.artifacts.ListStaleTargets(ctx, staleAfter, scanLimit)
			if err != nil {
				return err
			}
			for _, target := range staleTargets {
				jobKind := task.KindAggregate
				if target.Kind == artifact.KindSkill {
					jobKind = task.KindBuildSkill
				}
				existing, err := tx.GetGovernanceJobByTarget(ctx, jobKind, target.TargetKey())
				switch {
				case err == nil && (existing.Status == task.StatusQueued ||
					existing.Status == task.StatusRunning ||
					existing.Status == task.StatusWaiting ||
					existing.Status == task.StatusFailed ||
					existing.Status == task.StatusCancelled):
					// Active work or an isolated terminal task needs no repair.
					continue
				case err != nil && !errors.Is(err, task.ErrNotFound):
					return err
				}
				if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
					Kind:          jobKind,
					TargetKey:     target.TargetKey(),
					InputHash:     "reconcile-stale:" + target.TargetKey(),
					NextAttemptAt: &due,
					Now:           now,
				}); err != nil {
					return err
				}
			}
			if len(staleTargets) < scanLimit {
				nextStale = ""
			} else if len(staleTargets) > 0 {
				nextStale = staleTargets[len(staleTargets)-1].TargetKey()
			}

			expiredTargets, err := h.artifacts.ListExpiredTargets(ctx, now, expiredAfter, scanLimit)
			if err != nil {
				return err
			}
			for _, target := range expiredTargets {
				item, err := tx.GetArtifactByTarget(ctx, target.Kind, target.TargetKey())
				if errors.Is(err, artifact.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				if item.State != artifact.StateActive {
					continue
				}
				item.State = artifact.StateStale
				item.UpdatedAt = now
				if err := tx.UpdateArtifact(ctx, item); err != nil {
					return err
				}
				if err := tx.DeleteArtifactSearchDocument(ctx, item.ID); err != nil {
					return err
				}
				jobKind := task.KindAggregate
				if target.Kind == artifact.KindSkill {
					jobKind = task.KindBuildSkill
				}
				if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
					Kind:          jobKind,
					TargetKey:     target.TargetKey(),
					InputHash:     "reconcile-expired:" + target.TargetKey() + ":" + now.Format(time.RFC3339Nano),
					NextAttemptAt: &due,
					Now:           now,
				}); err != nil {
					return err
				}
			}
			if len(expiredTargets) < scanLimit {
				nextExpired = ""
			} else if len(expiredTargets) > 0 {
				nextExpired = expiredTargets[len(expiredTargets)-1].TargetKey()
			}

			groups, err := h.artifacts.ListAggregationGroups(ctx, groupAfter, scanLimit)
			if err != nil {
				return err
			}
			for _, group := range groups {
				target := group.Target
				targetKey := target.TargetKey()
				if _, err := tx.GetArtifactByTarget(ctx, target.Kind, targetKey); err == nil {
					continue
				} else if !errors.Is(err, artifact.ErrNotFound) {
					return err
				}
				if _, err := tx.GetGovernanceJobByTarget(ctx, task.KindAggregate, targetKey); err == nil {
					continue
				} else if !errors.Is(err, task.ErrNotFound) {
					return err
				}
				if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
					Kind:          task.KindAggregate,
					TargetKey:     targetKey,
					InputHash:     "reconcile-group:" + targetKey,
					NextAttemptAt: &due,
					Now:           now,
				}); err != nil {
					return err
				}
			}
			if len(groups) < scanLimit {
				nextGroup = ""
			} else if len(groups) > 0 {
				nextGroup = groups[len(groups)-1].Cursor
			}

			skillGroups, err := h.artifacts.ListSkillGroups(ctx, skillAfter, scanLimit)
			if err != nil {
				return err
			}
			for _, group := range skillGroups {
				target := group.Target
				targetKey := target.TargetKey()
				if _, err := tx.GetArtifactByTarget(ctx, target.Kind, targetKey); err == nil {
					continue
				} else if !errors.Is(err, artifact.ErrNotFound) {
					return err
				}
				if _, err := tx.GetGovernanceJobByTarget(ctx, task.KindBuildSkill, targetKey); err == nil {
					continue
				} else if !errors.Is(err, task.ErrNotFound) {
					return err
				}
				if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
					Kind:          task.KindBuildSkill,
					TargetKey:     targetKey,
					InputHash:     "reconcile-skill:" + targetKey,
					NextAttemptAt: &due,
					Now:           now,
				}); err != nil {
					return err
				}
			}
			if len(skillGroups) < scanLimit {
				nextSkill = ""
			} else if len(skillGroups) > 0 {
				nextSkill = skillGroups[len(skillGroups)-1].Cursor
			}
		}

		ok, err := tx.FinishJob(ctx, job.ID, job.Revision, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.cursor = nextCursor
	h.staleCursor = nextStale
	h.expiredCursor = nextExpired
	h.groupCursor = nextGroup
	h.skillCursor = nextSkill
	h.mu.Unlock()
	return nil
}
