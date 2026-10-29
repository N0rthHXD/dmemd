// Package observation owns L0 raw memories extracted from one source. An
// observation is an auditable record of what a source expressed; it is not a
// canonical fact and it is never updated by governance in place.
package observation

import "time"

// Kind is the minimal semantic type carried by one observation.
type Kind string

const (
	KindFact       Kind = "fact"
	KindPreference Kind = "preference"
	KindEvent      Kind = "event"
	KindProcedure  Kind = "procedure"
)

// Known reports whether kind belongs to the fixed observation vocabulary.
func (k Kind) Known() bool {
	switch k {
	case KindFact, KindPreference, KindEvent, KindProcedure:
		return true
	default:
		return false
	}
}

// Status is the persisted processing state of one observation.
type Status string

const (
	StatusObserved  Status = "observed"
	StatusPending   Status = "pending"
	StatusRetracted Status = "retracted"
	StatusDeleted   Status = "deleted"
)

// Known reports whether status belongs to the fixed observation state set.
func (s Status) Known() bool {
	switch s {
	case StatusObserved, StatusPending, StatusRetracted, StatusDeleted:
		return true
	default:
		return false
	}
}

// ContentMode records how the observation body was produced. Extractive content
// is copied directly from a selected source span; normalized content is a model
// expression that still requires code-generated evidence.
type ContentMode string

const (
	ContentExtractive ContentMode = "extractive"
	ContentNormalized ContentMode = "normalized"
)

// Known reports whether mode belongs to the fixed content-mode set.
func (m ContentMode) Known() bool {
	switch m {
	case ContentExtractive, ContentNormalized:
		return true
	default:
		return false
	}
}

// Observation is one raw L0 memory extracted from a source.
type Observation struct {
	ID                string      `json:"id"`
	SourceID          string      `json:"source_id"`
	ScopeKey          string      `json:"scope_key,omitempty"`
	Kind              Kind        `json:"kind"`
	Content           string      `json:"content"`
	ContentMode       ContentMode `json:"content_mode"`
	OriginRole        string      `json:"origin_role,omitempty"`
	Status            Status      `json:"status"`
	ExtractionVersion string      `json:"extraction_version,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

// Draft is the minimal model-independent unit produced by extraction. It
// contains only model-selected span references and an optional normalized
// expression; identity, evidence, status and timestamps are added by code.
type Draft struct {
	SpanIDs []string
	Kind    Kind
	Content string
}

// ListOptions filters the read-only L0 observation list. Zero values mean no
// filter; Limit zero uses the storage default.
type ListOptions struct {
	SourceID string
	ScopeKey string
	Kind     Kind
	Status   Status
	Limit    int
}
