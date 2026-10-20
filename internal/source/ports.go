package source

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a source or evidence identifier does not exist.
var ErrNotFound = errors.New("source not found")

// SourceService exposes source lookup and explicit redaction. There is no
// public source-creation endpoint: sources are created only by ingest.
type SourceService interface {
	Get(ctx context.Context, id string) (Source, error)
	Redact(ctx context.Context, id string) (RedactResult, error)
}

// Reader is the read capability used for source lookup.
type Reader interface {
	GetSource(ctx context.Context, id string) (Source, error)
}

// SourceTx is the source-scoped subset of a writable transaction.
type SourceTx interface {
	InsertSource(ctx context.Context, source Source) error
	GetSource(ctx context.Context, id string) (Source, error)
	RedactSource(ctx context.Context, id string, at time.Time) error
}

// EvidenceTx is the evidence-scoped subset of a writable transaction.
type EvidenceTx interface {
	InsertEvidence(ctx context.Context, evidence Evidence) error
	GetEvidence(ctx context.Context, id string) (Evidence, error)
	ListEvidenceBySource(ctx context.Context, sourceID string) ([]Evidence, error)
	RedactEvidenceBySource(ctx context.Context, sourceID string, at time.Time) error
}

// JobCancellationTx lets source redaction cancel queued/running jobs and scrub
// terminal job error summaries in the same transaction that removes content.
type JobCancellationTx interface {
	CancelJobsBySource(ctx context.Context, sourceID, reason string, at time.Time) (int64, error)
	ScrubJobErrorsBySource(ctx context.Context, sourceID, reason string, at time.Time) (int64, error)
}

// Tx is the capability set available inside one source transaction. The
// concrete implementation lives in store/sqlite; business code never sees
// *sql.Tx.
type Tx interface {
	SourceTx
	EvidenceTx
	JobCancellationTx
}

// Store runs a short transaction over source, evidence and related job state.
// The callback must not perform network calls or long computations.
type Store interface {
	WithTx(ctx context.Context, fn func(context.Context, Tx) error) error
}
