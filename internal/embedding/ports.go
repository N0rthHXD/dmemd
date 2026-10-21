package embedding

import (
	"context"
	"errors"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

// ErrInputMissing is returned when a claimed task's current search document or
// memory snapshot no longer exists. The caller discards the work; the delete
// cascade normally removes the task in the same transaction.
var ErrInputMissing = errors.New("embedding: 记忆输入不存在")

// Job is one durable embedding task generation.
type Job struct {
	MemoryID    string
	ProfileID   string
	ContentHash string
	Attempts    int
}

// Document is the current snapshot plus the persisted search-document content
// hash. The worker compares it with the claimed job before calling the model.
type Document struct {
	Memory      memory.Memory
	ContentHash string
}

// Backend is the persistence capability consumed by the background worker. All
// methods except LoadDocument run in a short write transaction inside the
// store.
type Backend interface {
	// ClaimJob selects one due queued task for profileID and increments its
	// attempt count.
	ClaimJob(ctx context.Context, profileID string, now time.Time) (Job, bool, error)
	// LoadDocument reads the current snapshot and its derived content hash.
	LoadDocument(ctx context.Context, memoryID string) (Document, error)
	// CommitJob conditionally writes the vector and deletes the task. It
	// returns false when the task generation, profile or hash changed.
	CommitJob(ctx context.Context, job Job, vector []byte, dimensions int, embeddedAt time.Time) (bool, error)
	// FailJob conditionally records a sanitized error. A nil nextAttempt marks
	// the task failed; otherwise it returns to queued for another try.
	FailJob(ctx context.Context, job Job, code string, nextAttempt *time.Time, now time.Time) error
	// RecoverJobs requeues running tasks left by a crash, keeping attempts.
	RecoverJobs(ctx context.Context, profileID string, now time.Time) (int64, error)
	// EnqueueMissingJobs creates or requeues tasks for current documents that
	// lack a valid vector for profileID. Queued and running tasks are not reset;
	// failed tasks are retried.
	EnqueueMissingJobs(ctx context.Context, profileID string, now time.Time) (int64, error)
}
