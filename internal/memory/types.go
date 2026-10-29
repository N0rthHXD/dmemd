// Package memory owns the current canonical memory view and its immutable
// revision history. A canonical memory is materialized by governance from
// observations; it is not the result of the first extraction step.
//
// The package does not expose a direct HTTP write API. New observations enter
// through ingestion; management operations cover state changes, restoration,
// conflict resolution and purge.
package memory

import (
	"time"

	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// Kind shares the fixed semantic vocabulary of an Observation. A canonical
// memory keeps the kind supplied by the observations it was materialized from.
type Kind = observation.Kind

const (
	KindFact       = observation.KindFact
	KindPreference = observation.KindPreference
	KindEvent      = observation.KindEvent
	KindProcedure  = observation.KindProcedure
)

// State controls lifecycle and default search visibility.
type State string

const (
	StateActive   State = "active"
	StateArchived State = "archived"
	StateDeleted  State = "deleted"
)

// AssertionStatus describes whether the system currently accepts an assertion.
// It is independent from lifecycle state: an unadjudicated contradiction stays
// pending.
type AssertionStatus string

const (
	AssertionAccepted AssertionStatus = "accepted"
	AssertionPending  AssertionStatus = "pending"
	AssertionRejected AssertionStatus = "rejected"
)

// RelationKind connects two memory records without rewriting either record.
type RelationKind string

const (
	RelationSupersedes RelationKind = "supersedes"
)

// ConflictStatus is the adjudication state of a conflict.
type ConflictStatus string

const (
	ConflictOpen     ConflictStatus = "open"
	ConflictResolved ConflictStatus = "resolved"
)

// ConflictReason is a fixed audit reason, never model free text.
type ConflictReason string

const (
	ConflictContradictoryValue ConflictReason = "contradictory_value"
	ConflictUncertainTime      ConflictReason = "uncertain_time"
	ConflictAmbiguousScope     ConflictReason = "ambiguous_scope"
	ConflictComplexTimeline    ConflictReason = "complex_timeline"
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
	OperationResolve     Operation = "resolve"
	OperationClassify    Operation = "classify"
)

// Memory is the current canonical view produced by governance. It is not an
// extraction result: observations are the source records, and this snapshot is
// materialized from them after relations have been resolved.
type Memory struct {
	ID              string          `json:"id"`
	Kind            Kind            `json:"kind"`
	Title           string          `json:"title"`
	Content         string          `json:"content"`
	State           State           `json:"state"`
	AssertionStatus AssertionStatus `json:"assertion_status"`
	Version         int             `json:"version"`
	ScopeKey        string          `json:"scope_key,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	OriginSourceID  string          `json:"origin_source_id,omitempty"`
}

// ConflictRef is a lightweight conflict reference attached to a search item.
type ConflictRef struct {
	ID         string         `json:"id"`
	ReasonCode ConflictReason `json:"reason_code"`
}

// Revision is a complete, immutable snapshot of a memory plus the full
// evidence set that supported that version. Evidence is supplied by the source
// package and linked through revision_evidence. CreatedAt is the revision time;
// storage derives it from the snapshot's UpdatedAt.
type Revision struct {
	MemoryID  string            `json:"memory_id"`
	Version   int               `json:"version"`
	Operation Operation         `json:"operation"`
	Reason    string            `json:"reason,omitempty"`
	Snapshot  Memory            `json:"snapshot"`
	Evidence  []source.Evidence `json:"evidence"`
	CreatedAt time.Time         `json:"created_at"`
}

// RelationRef is the neutral read shape of one typed governance relation. The
// governance package owns relation semantics; memory only needs a transport-safe
// reference for the canonical detail view and therefore does not import it.
type RelationRef struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	FromType    string    `json:"from_type"`
	FromID      string    `json:"from_id"`
	FromVersion int       `json:"from_version"`
	ToType      string    `json:"to_type"`
	ToID        string    `json:"to_id"`
	ToVersion   int       `json:"to_version"`
	CreatedAt   time.Time `json:"created_at"`
}

// DerivedDependent is one derived memory whose current revision consumes this
// canonical memory. It is a bounded reference, not a copy of derived content.
type DerivedDependent struct {
	ID        string `json:"id"`
	Layer     string `json:"layer"`
	Kind      string `json:"kind"`
	TargetKey string `json:"target_key"`
	ScopeKey  string `json:"scope_key,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Version   int    `json:"version"`
	State     string `json:"state"`
}

// Detail is the complete canonical-memory detail view: the current snapshot,
// its observation source references, typed relations, open conflict references
// and derived dependents. None of these fields is editable through this view.
type Detail struct {
	Memory
	ObservationIDs    []string           `json:"observation_ids"`
	Relations         []RelationRef      `json:"relations"`
	ConflictIDs       []string           `json:"conflict_ids"`
	DerivedDependents []DerivedDependent `json:"derived_dependents"`
}

// ListOptions filters the management list. A zero-valued field means no filter;
// Limit zero means the contract default (50).
type ListOptions struct {
	Kinds           []Kind
	States          []State
	AssertionStatus []AssertionStatus
	UpdatedAfter    *time.Time
	UpdatedBefore   *time.Time
	Limit           int
}

// SearchDocument is the derived, normalized representation written alongside a
// memory snapshot. TitleTerms and ContentTerms are the normalized text indexed
// by FTS5; ContentHash identifies the exact embedding document bytes. Original
// title and content stay in the memory snapshot.
type SearchDocument struct {
	MemoryID     string
	TitleTerms   string
	ContentTerms string
	ContentHash  string
}

// DocumentBuilder is injected into the write path. Production wiring supplies
// the NFC/tokenization implementation; tests may supply a deterministic builder.
// Building is pure and must not perform I/O.
type DocumentBuilder interface {
	BuildDocument(memory Memory) (SearchDocument, error)
}

// CreateInput is the internal input for a new canonical memory. Evidence
// entries already carry their identifiers and source relationships.
type CreateInput struct {
	Kind            Kind
	Title           string
	Content         string
	AssertionStatus AssertionStatus
	OriginSourceID  string
	ScopeKey        string
	Evidence        []source.Evidence
	Reason          string
	Operation       Operation
}

// ReviseInput is an internal content, assertion or evidence correction.
// Version is never supplied by the caller; it is derived from the persisted
// current snapshot.
type ReviseInput struct {
	MemoryID        string
	Kind            Kind
	Title           string
	Content         string
	AssertionStatus AssertionStatus
	ScopeKey        string
	Evidence        []source.Evidence
	Operation       Operation
	Reason          string
}

// ClassificationInput updates only the scope of one canonical memory. Topics
// are a separate classification result and are no longer embedded in a memory.
type ClassificationInput struct {
	MemoryID string
	ScopeKey string
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

// Relation connects two memory versions. The version fields are snapshots from
// decision time, not a promise that either memory is still current.
type Relation struct {
	ID                 string       `json:"id"`
	FromMemoryID       string       `json:"from_memory_id"`
	FromVersion        int          `json:"from_version"`
	ToMemoryID         string       `json:"to_memory_id"`
	ToVersion          int          `json:"to_version"`
	Kind               RelationKind `json:"kind"`
	CreatedAt          time.Time    `json:"created_at"`
	FromVersionChanged bool         `json:"from_version_changed"`
	ToVersionChanged   bool         `json:"to_version_changed"`
}

// ConflictMember is one participant at detection time.
type ConflictMember struct {
	MemoryID       string `json:"memory_id"`
	Version        int    `json:"version"`
	CurrentVersion int    `json:"current_version,omitempty"`
}

// ConflictSummary is the compact conflict view attached to recall responses.
type ConflictSummary struct {
	ID          string         `json:"id"`
	ReasonCode  ConflictReason `json:"reason_code"`
	MemberIDs   []string       `json:"member_ids"`
	HasAccepted bool           `json:"has_accepted"`
}

// ConflictEvent is an immutable audit event. It stores fixed action codes and
// references only; source text and model reasoning are never copied here.
type ConflictEvent struct {
	ID         string    `json:"id"`
	ConflictID string    `json:"conflict_id"`
	Action     string    `json:"action"`
	MemoryID   string    `json:"memory_id,omitempty"`
	Version    int       `json:"version,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Conflict is an unresolved or resolved contradiction.
type Conflict struct {
	ID         string           `json:"id"`
	Status     ConflictStatus   `json:"status"`
	ReasonCode ConflictReason   `json:"reason_code"`
	Resolution string           `json:"resolution,omitempty"`
	Revision   int              `json:"revision"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
	Members    []ConflictMember `json:"members"`
	Events     []ConflictEvent  `json:"events,omitempty"`
}

// ConflictListOptions filters the management conflict list.
type ConflictListOptions struct {
	Status ConflictStatus
	Limit  int
}

// ResolutionAction is one explicit human adjudication.
type ResolutionAction string

const (
	ResolutionChoose    ResolutionAction = "choose"
	ResolutionRejectAll ResolutionAction = "reject_all"
)

// ResolveInput is a version-checked conflict adjudication.
type ResolveInput struct {
	ConflictID     string
	Revision       int
	Action         ResolutionAction
	MemberIDs      []string
	MemberVersions []ConflictMember
	Reason         string
}
