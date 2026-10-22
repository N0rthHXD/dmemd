// Package governance owns the persistent background worker, fixed task
// handlers, debounce and the bounded in-process model-call limiter.
//
// Business transactions register tasks directly through the task capability in
// their storage transaction. This package never performs long work inside a
// database transaction: reading inputs, model calls and prompt preparation
// happen outside, and a short conditional transaction commits the result.
package governance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrRevisionChanged means another transaction changed the task target after it
// was claimed. The worker discards the result; it never overwrites newer state.
var ErrRevisionChanged = errors.New("governance task revision changed")

// RetryError asks the worker to wait until At before trying the same task again.
// It is used for call throttling and is not counted as a protocol failure.
type RetryError struct {
	At     time.Time
	Code   string
	Reason string
}

func (e *RetryError) Error() string {
	if e == nil {
		return "governance task deferred"
	}
	if e.Code == "" {
		return "governance task deferred"
	}
	return "governance task deferred: " + e.Code
}

// Handler processes one claimed task. A handler that succeeds must commit its
// result and the conditional job completion in one transaction.
type Handler interface {
	Process(ctx context.Context, job task.Job) error
}

// MemoryReader is the read capability used by handlers before a model call.
type MemoryReader interface {
	GetMemory(ctx context.Context, id string) (memory.Memory, error)
}

// ExperienceTx persists application-reported results and reads the bounded
// inputs used by case and skill generation.
type ExperienceTx interface {
	GetExperienceRun(ctx context.Context, id string) (ExperienceRun, error)
	GetExperienceRunByRunID(ctx context.Context, runID string) (ExperienceRun, error)
	InsertExperienceRun(ctx context.Context, run ExperienceRun) error
	MarkExperienceRunRevoked(ctx context.Context, id string, at time.Time) error
	ListExperienceRunsByScope(ctx context.Context, scopeKey string, limit int) ([]ExperienceRun, error)
	ListExperienceRunsBySkill(ctx context.Context, skillID string, limit int) ([]ExperienceRun, error)
}

// ForgetTx enumerates and removes the leaf records that must disappear in the
// same transaction as their dependent artifacts. Enumeration always happens
// before any foreign-key cascade removes an edge.
type ForgetTx interface {
	ListMemoryIDsForSource(ctx context.Context, sourceID string) ([]string, error)
	ListExperienceIDsForSource(ctx context.Context, sourceID string) ([]string, error)
	DeleteExperienceRun(ctx context.Context, experienceID string) error
	DeleteIngestJobsBySource(ctx context.Context, sourceID string) error
	DeleteEvidenceBySource(ctx context.Context, sourceID string) error
	DeleteSource(ctx context.Context, sourceID string) error
}

// Tx is one short governance transaction: memory writes, derived versions,
// application results, task completion and task registration commit together.
type Tx interface {
	memory.Tx
	task.QueueTx
	ExperienceTx
	ForgetTx
	source.SourceTx
}

// Store runs one short transaction. The callback must not perform network
// calls or long computations.
type Store interface {
	WithTx(ctx context.Context, fn func(context.Context, Tx) error) error
}

// CancelMissingTask marks a task cancelled when its target no longer exists. It
// is conditional on the claimed revision, so a late result cannot cancel a
// newer target.
func CancelMissingTask(ctx context.Context, store Store, job task.Job, now time.Time) error {
	return store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		current, err := tx.GetGovernanceJob(ctx, job.ID)
		if err != nil {
			return err
		}
		if current.Revision != job.Revision || current.Status != task.StatusRunning {
			return ErrRevisionChanged
		}
		ok, err := tx.RescheduleJob(ctx, job.ID, job.Revision, task.RescheduleInput{
			Status:     task.StatusCancelled,
			Now:        now,
			WaitReason: "",
		})
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
}

func checkedJob(ctx context.Context, tx Tx, job task.Job) error {
	current, err := tx.GetGovernanceJob(ctx, job.ID)
	if err != nil {
		return err
	}
	if current.Revision != job.Revision {
		return fmt.Errorf("%w: %d != %d", ErrRevisionChanged, current.Revision, job.Revision)
	}
	if current.Status != task.StatusRunning {
		return fmt.Errorf("%w: status %s", ErrRevisionChanged, current.Status)
	}
	return nil
}
