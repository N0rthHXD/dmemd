package observation

import (
	"context"
	"time"

	"github.com/N0rthHXD/dmemd/internal/source"
)

// Tx is the writable observation capability. It is intentionally expressed as
// storage primitives so evidence, observation rows and their links can commit in
// the caller's existing transaction.
type Tx interface {
	InsertObservation(ctx context.Context, item Observation) error
	InsertObservationEvidence(ctx context.Context, observationID, evidenceID string) error
	// UpdateObservationStatus applies a lifecycle transition to one L0
	// observation. Observation content is never edited; retraction and deletion
	// only change status and index eligibility.
	UpdateObservationStatus(ctx context.Context, id string, status Status, now time.Time) error
}

// Reader is the read capability for L0 observations and their evidence.
type Reader interface {
	GetObservation(ctx context.Context, id string) (Observation, error)
	ListObservations(ctx context.Context, options ListOptions) ([]Observation, error)
	ListObservationsBySource(ctx context.Context, sourceID string) ([]Observation, error)
	ListObservationEvidence(ctx context.Context, observationID string) ([]source.Evidence, error)
}

// Service exposes read-only observation queries. Observations are never edited
// through this service; their content is fixed by the extraction stage.
type Service struct {
	reader Reader
}

// NewService assembles the observation query service.
func NewService(reader Reader) *Service {
	return &Service{reader: reader}
}

var _ ServiceAPI = (*Service)(nil)

// Get returns one observation by identifier.
func (s *Service) Get(ctx context.Context, id string) (Observation, error) {
	return s.reader.GetObservation(ctx, id)
}

// ListBySource returns every observation extracted from one source.
func (s *Service) ListBySource(ctx context.Context, sourceID string) ([]Observation, error) {
	return s.reader.ListObservationsBySource(ctx, sourceID)
}

// List returns a bounded filtered observation page. It never rewrites or
// reinterprets observation content; filters only select L0 rows.
func (s *Service) List(ctx context.Context, options ListOptions) ([]Observation, error) {
	return s.reader.ListObservations(ctx, options)
}

// Evidence returns the code-generated evidence bound to one observation.
func (s *Service) Evidence(ctx context.Context, observationID string) ([]source.Evidence, error) {
	return s.reader.ListObservationEvidence(ctx, observationID)
}
