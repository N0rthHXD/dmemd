// Package memory owns the current memory snapshot, immutable revision history,
// complete evidence sets, lifecycle state, fact groups, conflicts and the
// unified write unit.
//
// The package does not expose a direct HTTP write API. New content and
// corrections enter through ingest; management operations cover state changes,
// restoration, conflict resolution and purge.
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

// AssertionStatus describes whether the system currently accepts an assertion.
// It is independent from lifecycle state: an expired fact may stay active while
// an unadjudicated contradiction stays pending.
type AssertionStatus string

const (
	AssertionAccepted AssertionStatus = "accepted"
	AssertionPending  AssertionStatus = "pending"
	AssertionRejected AssertionStatus = "rejected"
)

// Temporal status values returned for a current snapshot.
const (
	TemporalWithinBounds = "within_bounds"
	TemporalUnknown      = "unknown"
)

// TemporalBasis records where a valid-time boundary came from.
type TemporalBasis string

const (
	BasisExplicit TemporalBasis = "explicit"
	BasisInferred TemporalBasis = "inferred"
	BasisUnknown  TemporalBasis = "unknown"
)

// Cardinality describes whether a fact group may hold one or several values.
type Cardinality string

const (
	CardinalitySingle   Cardinality = "single"
	CardinalityMultiple Cardinality = "multiple"
	CardinalityUnknown  Cardinality = "unknown"
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
	OperationCorrect     Operation = "correct"
	OperationChange      Operation = "change"
	OperationResolve     Operation = "resolve"
	OperationClassify    Operation = "classify"
)

// Memory is the current snapshot of one independently updatable assertion.
type Memory struct {
	ID              string          `json:"id"`
	Kind            Kind            `json:"kind"`
	Title           string          `json:"title"`
	Content         string          `json:"content"`
	State           State           `json:"state"`
	AssertionStatus AssertionStatus `json:"assertion_status"`
	Version         int             `json:"version"`
	ObservedAt      time.Time       `json:"observed_at"`
	ValidFrom       *time.Time      `json:"valid_from,omitempty"`
	ValidTo         *time.Time      `json:"valid_to,omitempty"`
	ValidFromBasis  TemporalBasis   `json:"valid_from_basis"`
	ValidToBasis    TemporalBasis   `json:"valid_to_basis"`
	FactGroupID     string          `json:"fact_group_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	CreatedByApp    string          `json:"created_by_app,omitempty"`
	OriginSourceID  string          `json:"origin_source_id,omitempty"`
	ScopeKey        string          `json:"scope_key,omitempty"`
	Topics          []string        `json:"topics"`
}

// TemporalStatus reports whether a current snapshot has fully known validity
// bounds. It only describes the record, not objective truth.
func (m Memory) TemporalStatus() string {
	if m.ValidFrom != nil && m.ValidTo != nil &&
		m.ValidFromBasis != BasisUnknown && m.ValidToBasis != BasisUnknown {
		return TemporalWithinBounds
	}
	return TemporalUnknown
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

// ListOptions filters the management list. A zero-valued field means no filter;
// Limit zero means the contract default (50).
type ListOptions struct {
	Kinds           []Kind
	States          []State
	AssertionStatus []AssertionStatus
	FactGroupID     string
	CreatedByApp    string
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

// TimeRange is a nullable, half-open [From, To) fact validity range.
type TimeRange struct {
	From      *time.Time
	To        *time.Time
	FromBasis TemporalBasis
	ToBasis   TemporalBasis
}

// CreateInput is the internal input for a new memory. Evidence entries already
// carry their identifiers and source relationships.
type CreateInput struct {
	Kind            Kind
	Title           string
	Content         string
	ObservedAt      time.Time
	ValidFrom       *time.Time
	ValidTo         *time.Time
	ValidFromBasis  TemporalBasis
	ValidToBasis    TemporalBasis
	AssertionStatus AssertionStatus
	FactGroupID     string
	CreatedByApp    string
	OriginSourceID  string
	ScopeKey        string
	Topics          []string
	Evidence        []source.Evidence
	Reason          string
	Operation       Operation
}

// ReviseInput is an internal content, time, assertion, group or evidence
// correction. Version is never supplied by the caller; it is derived from the
// persisted current snapshot.
type ReviseInput struct {
	MemoryID        string
	Kind            Kind
	Title           string
	Content         string
	ObservedAt      time.Time
	ValidFrom       *time.Time
	ValidTo         *time.Time
	ValidFromBasis  TemporalBasis
	ValidToBasis    TemporalBasis
	AssertionStatus AssertionStatus
	FactGroupID     string
	ScopeKey        string
	Topics          []string
	Evidence        []source.Evidence
	Operation       Operation
	Reason          string
}

// ClassificationInput updates only the derived organization labels of one
// atomic memory. It never changes kind, content, validity, evidence or
// assertion status, and it does not re-enqueue a classify task.
type ClassificationInput struct {
	MemoryID string
	ScopeKey string
	Topics   []string
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

// FactGroup is a service-owned grouping of assertions about one subject,
// predicate and scope.
type FactGroup struct {
	ID          string      `json:"id"`
	Subject     string      `json:"subject"`
	Predicate   string      `json:"predicate"`
	Scope       string      `json:"scope"`
	Cardinality Cardinality `json:"cardinality"`
	Generation  int         `json:"generation"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// FactGroupInput is the service-created group metadata proposed by a model.
type FactGroupInput struct {
	Subject     string
	Predicate   string
	Scope       string
	Cardinality Cardinality
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
	FactGroupID string         `json:"fact_group_id,omitempty"`
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
	ID          string           `json:"id"`
	FactGroupID string           `json:"fact_group_id,omitempty"`
	Status      ConflictStatus   `json:"status"`
	ReasonCode  ConflictReason   `json:"reason_code"`
	Resolution  string           `json:"resolution,omitempty"`
	Revision    int              `json:"revision"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Members     []ConflictMember `json:"members"`
	Events      []ConflictEvent  `json:"events,omitempty"`
}

// ConflictListOptions filters the management conflict list.
type ConflictListOptions struct {
	Status ConflictStatus
	Limit  int
}

// ResolutionAction is one explicit human adjudication.
type ResolutionAction string

const (
	ResolutionChoose        ResolutionAction = "choose"
	ResolutionTimeline      ResolutionAction = "timeline"
	ResolutionSeparateScope ResolutionAction = "separate_scope"
	ResolutionRejectAll     ResolutionAction = "reject_all"
)

// TimelineSegment is one non-overlapping segment submitted by an explicit
// timeline resolution.
type TimelineSegment struct {
	MemoryID  string
	Content   string
	ValidFrom *time.Time
	ValidTo   *time.Time
}

// ResolveInput is a version-checked conflict adjudication.
type ResolveInput struct {
	ConflictID     string
	Revision       int
	Action         ResolutionAction
	MemberIDs      []string
	MemberVersions []ConflictMember
	SeparateGroup  *FactGroupInput
	Timeline       []TimelineSegment
	Reason         string
}
