package memory

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrNotFound is returned when a memory or requested version does not exist.
var ErrNotFound = errors.New("memory not found")

// MemoryService is the public management capability consumed by HTTP. Content
// creation and correction are intentionally absent: those enter through ingest.
type MemoryService interface {
	Get(ctx context.Context, id string) (Memory, error)
	List(ctx context.Context, options ListOptions) ([]Memory, error)
	UpdateState(ctx context.Context, id string, state State) (Memory, error)
	Delete(ctx context.Context, id string) (Memory, error)
	Purge(ctx context.Context, id string) error
	ListVersions(ctx context.Context, id string, limit int) ([]Revision, error)
	GetVersion(ctx context.Context, id string, version int) (Revision, error)
	GetEvidence(ctx context.Context, id string, version int) ([]source.Evidence, error)
	Restore(ctx context.Context, input RestoreInput) (Memory, error)
}

// Reader is the read capability used by the memory service for snapshots,
// management lists and immutable history. Search never uses this interface.
type Reader interface {
	GetMemory(ctx context.Context, id string) (Memory, error)
	ListMemories(ctx context.Context, options ListOptions) ([]Memory, error)
	GetRevision(ctx context.Context, memoryID string, version int) (Revision, error)
	ListRevisions(ctx context.Context, memoryID string, limit int) ([]Revision, error)
	ListRevisionEvidence(ctx context.Context, memoryID string, version int) ([]source.Evidence, error)
}

// Tx is the memory-scoped writable transaction. It also carries evidence and
// derived-index capabilities so one write unit can commit snapshot, revision,
// complete evidence set and search document together.
type Tx interface {
	source.EvidenceTx

	GetMemory(ctx context.Context, id string) (Memory, error)
	InsertMemory(ctx context.Context, memory Memory) error
	UpdateMemory(ctx context.Context, memory Memory) error

	InsertRevision(ctx context.Context, revision Revision) error
	GetRevision(ctx context.Context, memoryID string, version int) (Revision, error)
	ListRevisions(ctx context.Context, memoryID string, limit int) ([]Revision, error)
	LinkRevisionEvidence(ctx context.Context, memoryID string, version int, evidenceIDs []string) error
	ListRevisionEvidence(ctx context.Context, memoryID string, version int) ([]source.Evidence, error)

	UpsertSearchDocument(ctx context.Context, document SearchDocument) error
	DeleteSearchDocument(ctx context.Context, memoryID string) error

	// PurgeMemory removes the current snapshot, all revisions, revision/evidence
	// links, dedicated evidence and the search document. Shared evidence and
	// sources are preserved.
	PurgeMemory(ctx context.Context, id string) error
}

// Store runs a short memory transaction. The callback must not perform network
// calls or long computations; model work happens before the transaction.
type Store interface {
	WithTx(ctx context.Context, fn func(context.Context, Tx) error) error
}

// Writer is the internal unified write unit used by ingest and lifecycle
// operations. It always receives an already-open transaction so callers can
// include job state or source changes in the same commit.
type Writer interface {
	Create(ctx context.Context, tx Tx, input CreateInput) (Memory, error)
	Revise(ctx context.Context, tx Tx, input ReviseInput) (Memory, error)
	ChangeState(ctx context.Context, tx Tx, input StateChangeInput) (Memory, error)
	RestoreVersion(ctx context.Context, tx Tx, input RestoreInput) (Memory, error)
	PurgeInTx(ctx context.Context, tx Tx, id string) error
}
