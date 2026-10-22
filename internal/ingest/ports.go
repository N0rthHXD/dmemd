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

// ErrCandidateLimitExceeded fails one attempt without dropping candidates. The
// caller may split the source into smaller submissions.
var ErrCandidateLimitExceeded = errors.New("ingest: candidate_limit_exceeded")

// ErrStateChanged signals that facts or catalog state moved during a model
// call. The transaction is rolled back and the worker retries the whole source.
var ErrStateChanged = errors.New("ingest: state changed during model call")

// ErrProtocol marks an unusable provider decision set. It is retryable.
var ErrProtocol = errors.New("ingest: invalid model protocol")

// IngestService is the unified submission and job-query capability. There is
// no direct memory-write or extraction-submission endpoint.
type IngestService interface {
	Submit(ctx context.Context, submission Submission) (Receipt, error)
	GetJob(ctx context.Context, id string) (Job, error)
}

// ExtractionProvider turns the current input into independent candidate facts.
// Implementations must not receive the full memory database and must not write
// storage.
type ExtractionProvider interface {
	Extract(ctx context.Context, request ExtractionRequest) (ExtractionResult, error)
}

// DecisionProvider judges the relationship between candidates and bounded
// existing-fact context. The same remote adapter may implement both providers.
type DecisionProvider interface {
	Decide(ctx context.Context, request DecisionRequest) (DecisionResult, error)
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

// FailureKind classifies a provider error for the scheduling policy without
// exposing raw response text. Configuration and credential failures park until a
// restart; protocol failures are retried a small fixed number of times;
// everything else is transient.
func (e *ProviderError) FailureKind() string {
	if e == nil {
		return "transient"
	}
	switch e.Operation {
	case "configuration", "credentials":
		return "configuration"
	case "protocol":
		return "protocol"
	}
	if !e.Retryable {
		return "protocol"
	}
	return "transient"
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
	RequeueWaitingConfigurationJobs(ctx context.Context, now time.Time) (int64, error)
	RequeueProtocolFailedJobs(ctx context.Context, now time.Time) (int64, error)
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
