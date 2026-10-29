package observation

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrNotFound is returned when an observation does not exist.
var ErrNotFound = errors.New("observation not found")

// ServiceAPI is the read-only L0 observation capability exposed to transport
// and runtime assembly.
type ServiceAPI interface {
	Get(ctx context.Context, id string) (Observation, error)
	List(ctx context.Context, options ListOptions) ([]Observation, error)
	ListBySource(ctx context.Context, sourceID string) ([]Observation, error)
	Evidence(ctx context.Context, observationID string) ([]source.Evidence, error)
}
