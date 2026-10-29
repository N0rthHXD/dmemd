package memory

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dmemd/internal/artifact"
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
	Relations(ctx context.Context, id string) ([]Relation, error)
	Restore(ctx context.Context, input RestoreInput) (Memory, error)
}

// ConflictService is the management capability for conflict inspection and
// explicit adjudication.
type ConflictService interface {
	ListConflicts(ctx context.Context, options ConflictListOptions) ([]Conflict, error)
	GetConflict(ctx context.Context, id string) (Conflict, error)
	Resolve(ctx context.Context, input ResolveInput) (Conflict, error)
}

// DetailService is the optional canonical-detail capability implemented by the
// production Service. Transport type-asserts it so read-only test doubles that
// only implement MemoryService remain valid.
type DetailService interface {
	Detail(ctx context.Context, id string) (Detail, error)
}

// Reader is the read capability used by the memory service for snapshots,
// management lists, immutable history and conflicts. Search never uses this
// interface.
type Reader interface {
	GetMemory(ctx context.Context, id string) (Memory, error)
	ListMemories(ctx context.Context, options ListOptions) ([]Memory, error)
	GetRevision(ctx context.Context, memoryID string, version int) (Revision, error)
	ListRevisions(ctx context.Context, memoryID string, limit int) ([]Revision, error)
	ListRevisionEvidence(ctx context.Context, memoryID string, version int) ([]source.Evidence, error)
	ListRelationsForMemory(ctx context.Context, memoryID string) ([]Relation, error)
	ListMemoryObservationRefs(ctx context.Context, memoryID string, version int) ([]string, error)
	ListMemoryRelationRefs(ctx context.Context, memoryID string) ([]RelationRef, error)
	ListConflictIDsForMemory(ctx context.Context, memoryID string) ([]string, error)
	ListDependents(ctx context.Context, input artifact.DependencyRef) ([]artifact.DerivedRef, error)
	ListConflicts(ctx context.Context, options ConflictListOptions) ([]Conflict, error)
	GetConflict(ctx context.Context, id string) (Conflict, error)
}

// Tx is the memory-scoped writable transaction. It also carries evidence and
// derived-index capabilities so one write unit can commit snapshot, revision,
// complete evidence set and search document together.
type Tx interface {
	source.EvidenceTx
	artifact.Tx

	GetMemory(ctx context.Context, id string) (Memory, error)
	InsertMemory(ctx context.Context, memory Memory) error
	UpdateMemory(ctx context.Context, memory Memory) error

	InsertRevision(ctx context.Context, revision Revision) error
	GetRevision(ctx context.Context, memoryID string, version int) (Revision, error)
	ListRevisions(ctx context.Context, memoryID string, limit int) ([]Revision, error)
	LinkRevisionEvidence(ctx context.Context, memoryID string, version int, evidenceIDs []string) error
	ListRevisionEvidence(ctx context.Context, memoryID string, version int) ([]source.Evidence, error)

	GetCatalogGeneration(ctx context.Context) (int64, error)
	BumpCatalogGeneration(ctx context.Context) error

	InsertConflict(ctx context.Context, conflict Conflict) error
	UpdateConflict(ctx context.Context, conflict Conflict) error
	GetConflict(ctx context.Context, id string) (Conflict, error)
	ListOpenConflictsForMemories(ctx context.Context, memoryIDs []string) ([]Conflict, error)
	InsertConflictEvent(ctx context.Context, event ConflictEvent) error
	UpsertConflictMember(ctx context.Context, conflictID string, member ConflictMember) error
	PruneConflict(ctx context.Context, id string) error

	UpsertSearchDocument(ctx context.Context, document SearchDocument) error
	DeleteSearchDocument(ctx context.Context, memoryID string) error

	// PurgeMemory removes the current snapshot, all revisions, revision/evidence
	// links, dedicated evidence, relations, conflict membership and the search
	// document. Shared evidence and sources are preserved.
	PurgeMemory(ctx context.Context, id string) error
}

// Store runs a short memory transaction. The callback must not perform network
// calls or long computations; model work happens before the transaction.
type Store interface {
	WithTx(ctx context.Context, fn func(context.Context, Tx) error) error
}

// ArtifactSync is the derived-layer hook used by the unified writer. It runs in
// the same transaction as the atomic change so stale state and aggregation
// targets cannot be left behind by a partially committed write.
type ArtifactSync interface {
	MemoryChanged(ctx context.Context, tx artifact.Tx, change artifact.MemoryChange) error
	MemoryPurged(ctx context.Context, tx artifact.Tx, change artifact.MemoryChange) error
}

// Writer is the internal unified write unit used by ingest and lifecycle
// operations. It always receives an already-open transaction so callers can
// include job state or source changes in the same commit.
type Writer interface {
	Create(ctx context.Context, tx Tx, input CreateInput) (Memory, error)
	Revise(ctx context.Context, tx Tx, input ReviseInput) (Memory, error)
	ChangeState(ctx context.Context, tx Tx, input StateChangeInput) (Memory, error)
	RestoreVersion(ctx context.Context, tx Tx, input RestoreInput) (Memory, error)
	Classify(ctx context.Context, tx Tx, input ClassificationInput) (Memory, error)
	PurgeInTx(ctx context.Context, tx Tx, id string) error
}
