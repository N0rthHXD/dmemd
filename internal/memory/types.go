// Package memory owns the current memory snapshot, immutable revision history,
// complete evidence sets, lifecycle state and the unified write unit.
//
// The package does not expose a direct public write API. New content and
// corrections enter through ingest; only state changes, restoration and purge
// are exposed as management operations.
package memory

import (
	"time"

	"github.com/N0rthHXD/dmemd/internal/source"
)

// Kind is the kind of knowledge represented by a memory.
type Kind string

const (
	KindFact       Kind = "fact"
	KindPreference Kind = "preference"
	KindEvent      Kind = "event"
	KindProcedure  Kind = "procedure"
)

// State controls lifecycle and default search visibility.
type State string

const (
	StateActive   State = "active"
	StateArchived State = "archived"
	StateDeleted  State = "deleted"
)

// Operation records why a revision was created.
type Operation string

const (
	OperationCreate      Operation = "create"
	OperationUpdate      Operation = "update"
	OperationAddEvidence Operation = "add_evidence"
	OperationArchive     Operation = "archive"
	OperationActivate    Operation = "activate"
	OperationDelete      Operation = "delete"
	OperationRestore     Operation = "restore"
)

// Memory is the current snapshot of one independently updatable fact,
// preference, event or procedure.
type Memory struct {
	ID             string    `json:"id"`
	Kind           Kind      `json:"kind"`
	Title          string    `json:"title"`
	Content        string    `json:"content"`
	State          State     `json:"state"`
	Version        int       `json:"version"`
	ObservedAt     time.Time `json:"observed_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	CreatedByApp   string    `json:"created_by_app,omitempty"`
	OriginSourceID string    `json:"origin_source_id,omitempty"`
}

// Revision is a complete, immutable snapshot of a memory plus the full
// evidence set that supported that version. Evidence is supplied by the source
// package and linked through revision_evidence.
type Revision struct {
	MemoryID  string            `json:"memory_id"`
	Version   int               `json:"version"`
	Operation Operation         `json:"operation"`
	Reason    string            `json:"reason,omitempty"`
	Snapshot  Memory            `json:"snapshot"`
	Evidence  []source.Evidence `json:"evidence"`
	CreatedAt time.Time         `json:"created_at"`
}

// ListOptions filters the management list. A zero-valued field means no filter;
// Limit zero means the contract default (50).
type ListOptions struct {
	Kinds         []Kind
	States        []State
	CreatedByApp  string
	UpdatedAfter  *time.Time
	UpdatedBefore *time.Time
	Limit         int
}

// SearchDocument is the derived, normalized representation written alongside a
// memory snapshot. Title and Content keep the original text for snippets;
// TitleTerms and ContentTerms are the normalized text indexed by FTS5.
type SearchDocument struct {
	MemoryID     string
	State        State
	Title        string
	Content      string
	TitleTerms   string
	ContentTerms string
	UpdatedAt    time.Time
}

// DocumentBuilder is injected into the write path. P4 provides the production
// NFC/tokenization implementation; tests may supply a deterministic builder.
// Building is pure and must not perform I/O.
type DocumentBuilder interface {
	BuildDocument(memory Memory) (SearchDocument, error)
}

// CreateInput is the internal input for a new memory. Evidence entries already
// carry their identifiers and source relationships.
type CreateInput struct {
	Kind           Kind
	Title          string
	Content        string
	ObservedAt     time.Time
	CreatedByApp   string
	OriginSourceID string
	Evidence       []source.Evidence
	Reason         string
}

// ReviseInput is an internal content or evidence correction. Version is never
// supplied by the caller; it is derived from the persisted current snapshot.
type ReviseInput struct {
	MemoryID   string
	Kind       Kind
	Title      string
	Content    string
	ObservedAt time.Time
	Evidence   []source.Evidence
	Operation  Operation
	Reason     string
}

// StateChangeInput changes lifecycle state without directly changing content.
type StateChangeInput struct {
	MemoryID string
	State    State
	Reason   string
}

// RestoreInput copies an existing historical version into a new current
// version. It never rewrites history.
type RestoreInput struct {
	MemoryID string
	Version  int
	Reason   string
}
