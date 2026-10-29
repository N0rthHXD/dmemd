package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrNotFound is returned when a requested ingest job does not exist.
var ErrNotFound = errors.New("ingest job not found")

// ErrProtocol marks an unusable provider extraction response. It is retryable.
var ErrProtocol = errors.New("ingest: invalid model protocol")

// IngestService is the unified submission and job-query capability. There is
// no direct memory-write or extraction-submission endpoint.
type IngestService interface {
	Submit(ctx context.Context, submission Submission) (Receipt, error)
	GetJob(ctx context.Context, id string) (Job, error)
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

// Tx is the complete transaction scope for ingestion. Source checks,
// evidence, observations, governance task registration and the extraction job
// result all commit together.
type Tx interface {
	memory.Tx
	source.Tx
	observation.Tx
	task.Tx
	JobTx
}

// Store runs the short ingestion transaction. Model calls and observation
// construction happen before the callback.
type Store interface {
	WithTx(ctx context.Context, fn func(context.Context, Tx) error) error
}
