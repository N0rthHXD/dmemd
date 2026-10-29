package governance

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RelationKind is the fixed semantic relationship between two governed
// objects. Relations are persisted facts; database actions and materialization
// are derived by code from them.
type RelationKind string

const (
	RelationDuplicate   RelationKind = "duplicate"
	RelationSupports    RelationKind = "supports"
	RelationEnriches    RelationKind = "enriches"
	RelationSupersedes  RelationKind = "supersedes"
	RelationContradicts RelationKind = "contradicts"
	RelationSeparate    RelationKind = "separate"
	RelationIndependent RelationKind = "independent"
	RelationUnresolved  RelationKind = "unresolved"
)

// Known reports whether kind belongs to the fixed relation vocabulary.
func (k RelationKind) Known() bool {
	switch k {
	case RelationDuplicate, RelationSupports, RelationEnriches, RelationSupersedes,
		RelationContradicts, RelationSeparate, RelationIndependent, RelationUnresolved:
		return true
	default:
		return false
	}
}

// RequiresTarget reports whether a relation must name one existing object.
// Separate, independent and unresolved are legitimate outcomes without a target
// reference: they describe that no bounded candidate is the counterpart.
func (k RelationKind) RequiresTarget() bool {
	switch k {
	case RelationSeparate, RelationIndependent, RelationUnresolved:
		return false
	default:
		return true
	}
}

// EndpointType identifies the kind of object on one side of a relation. The
// service validates the endpoint and object existence before persisting it.
type EndpointType string

const (
	EndpointObservation EndpointType = "observation"
	EndpointMemory      EndpointType = "memory"
)

// Known reports whether type belongs to the fixed endpoint vocabulary.
func (t EndpointType) Known() bool {
	switch t {
	case EndpointObservation, EndpointMemory:
		return true
	default:
		return false
	}
}

// Relation connects two typed endpoints. Version zero is valid for an
// observation endpoint or when the relation intentionally refers to the
// current object rather than one materialized revision.
type Relation struct {
	ID          string       `json:"id"`
	Kind        RelationKind `json:"kind"`
	FromType    EndpointType `json:"from_type"`
	FromID      string       `json:"from_id"`
	FromVersion int          `json:"from_version"`
	ToType      EndpointType `json:"to_type"`
	ToID        string       `json:"to_id"`
	ToVersion   int          `json:"to_version"`
	CreatedAt   time.Time    `json:"created_at"`
}

// ErrInvalidRelation marks a relation whose kind, endpoint or version cannot be
// persisted. The service validates semantic object existence separately.
var ErrInvalidRelation = errors.New("governance: invalid relation")

// Validate checks the fixed relation vocabulary and endpoint shape. It does not
// read storage: object existence and deletion/retraction state are checked by
// the transaction that commits the relation.
func (r Relation) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidRelation)
	}
	if !r.Kind.Known() {
		return fmt.Errorf("%w: kind %q", ErrInvalidRelation, r.Kind)
	}
	if !r.FromType.Known() || !r.ToType.Known() {
		return fmt.Errorf("%w: endpoints %q/%q", ErrInvalidRelation, r.FromType, r.ToType)
	}
	if r.FromID == "" || r.ToID == "" {
		return fmt.Errorf("%w: empty endpoint id", ErrInvalidRelation)
	}
	if r.FromVersion < 0 || r.ToVersion < 0 {
		return fmt.Errorf("%w: negative endpoint version", ErrInvalidRelation)
	}
	if r.FromType == EndpointObservation && r.FromVersion != 0 {
		return fmt.Errorf("%w: observation endpoint is not versioned", ErrInvalidRelation)
	}
	if r.ToType == EndpointObservation && r.ToVersion != 0 {
		return fmt.Errorf("%w: observation endpoint is not versioned", ErrInvalidRelation)
	}
	return nil
}

// RelationService is the read capability for the typed relation graph.
type RelationService interface {
	ListForEndpoint(ctx context.Context, endpointType EndpointType, id string) ([]Relation, error)
	ListBetween(ctx context.Context, fromType EndpointType, fromID string,
		toType EndpointType, toID string) ([]Relation, error)
}

type relationService struct {
	reader RelationReader
}

// NewRelationService assembles the relation read service.
func NewRelationService(reader RelationReader) RelationService {
	return &relationService{reader: reader}
}

// ListForEndpoint returns every relation in either direction for one object.
func (s *relationService) ListForEndpoint(ctx context.Context, endpointType EndpointType, id string) ([]Relation, error) {
	if s == nil || s.reader == nil {
		return nil, errors.New("governance: relation reader not configured")
	}
	return s.reader.ListGovernanceRelationsForEndpoint(ctx, endpointType, id)
}

// ListBetween returns exact directed relations between two typed endpoints.
func (s *relationService) ListBetween(ctx context.Context, fromType EndpointType, fromID string,
	toType EndpointType, toID string) ([]Relation, error) {
	if s == nil || s.reader == nil {
		return nil, errors.New("governance: relation reader not configured")
	}
	return s.reader.ListGovernanceRelationsBetween(ctx, fromType, fromID, toType, toID)
}
