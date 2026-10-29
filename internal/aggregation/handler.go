package aggregation

import (
	"context"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

// L1AggregationHandler turns governed L0 observations into one local aggregate.
type L1AggregationHandler struct {
	service *Service
}

// NewL1AggregationHandler assembles the local aggregate processor.
func NewL1AggregationHandler(store Store, artifacts *artifact.Service, provider Provider,
	options ...Option) *L1AggregationHandler {
	return &L1AggregationHandler{service: NewService(store, artifacts, provider, options...)}
}

// Process implements the fixed task handler shape.
func (h *L1AggregationHandler) Process(ctx context.Context, job task.Job) error {
	return h.service.Process(ctx, job, artifact.LayerL1)
}

// L2AggregationHandler turns active L1 aggregates into one domain aggregate.
type L2AggregationHandler struct {
	service *Service
}

// NewL2AggregationHandler assembles the domain aggregate processor.
func NewL2AggregationHandler(store Store, artifacts *artifact.Service, provider Provider,
	options ...Option) *L2AggregationHandler {
	return &L2AggregationHandler{service: NewService(store, artifacts, provider, options...)}
}

// Process implements the fixed task handler shape.
func (h *L2AggregationHandler) Process(ctx context.Context, job task.Job) error {
	return h.service.Process(ctx, job, artifact.LayerL2)
}

// L3AggregationHandler turns active L2 aggregates into the scope aggregate.
type L3AggregationHandler struct {
	service *Service
}

// NewL3AggregationHandler assembles the scope aggregate processor.
func NewL3AggregationHandler(store Store, artifacts *artifact.Service, provider Provider,
	options ...Option) *L3AggregationHandler {
	return &L3AggregationHandler{service: NewService(store, artifacts, provider, options...)}
}

// Process implements the fixed task handler shape.
func (h *L3AggregationHandler) Process(ctx context.Context, job task.Job) error {
	return h.service.Process(ctx, job, artifact.LayerL3)
}
