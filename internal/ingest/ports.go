package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrNotFound is returned when a requested ingest job does not exist.
var ErrNotFound = errors.New("ingest job not found")

// IngestService is the unified submission and job-query capability. There is
// no direct memory-write or extraction-submission endpoint.
type IngestService interface {
	Submit(ctx context.Context, submission Submission) (Receipt, error)
	GetJob(ctx context.Context, id string) (Job, error)
}

// ExtractionProvider turns the current input and selected related memories
// into candidate proposals. Implementations must not receive the full memory
// database and must not write storage.
type ExtractionProvider interface {
	Extract(ctx context.Context, request ExtractionRequest) (ExtractionResult, error)
}

// ProviderError describes a provider failure without retaining raw model
// responses. Retryable is false for permanent request/authentication errors.
type ProviderError struct {
	Operation string
	Retryable bool
	Err       error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "model provider error"
	}
	if e.Operation == "" {
		return fmt.Sprintf("model provider: %v", e.Err)
	}
	return fmt.Sprintf("model provider %s: %v", e.Operation, e.Err)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// JobReader is the read capability for task-status queries. It is separate
// from the write transaction so GetJob never needs a write lock.
type JobReader interface {
	GetJob(ctx context.Context, id string) (Job, error)
}

// JobTx is the writable job-state capability. A claim increases Attempts and
// moves queued to running; terminal states are never claimed again.
type JobTx interface {
	InsertJob(ctx context.Context, job Job) error
	GetJob(ctx context.Context, id string) (Job, error)
	UpdateJob(ctx context.Context, job Job) error
	ClaimNextJob(ctx context.Context, now time.Time) (Job, bool, error)
	RequeueRunningJobs(ctx context.Context, now time.Time) (int64, error)
	FailExhaustedJobs(ctx context.Context, maxAttempts int, reason string, now time.Time) (int64, error)
}

// Tx is the complete transaction scope for ingestion: memory snapshot,
// revision, evidence, search index, source and job result all commit together.
type Tx interface {
	memory.Tx
	source.Tx
	JobTx
}

// Store runs the short ingestion transaction. Model calls and candidate
// analysis happen before the callback.
type Store interface {
	WithTx(ctx context.Context, fn func(context.Context, Tx) error) error
}
