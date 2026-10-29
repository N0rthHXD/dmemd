package sqlite

import (
	"context"

	"github.com/N0rthHXD/dmemd/internal/aggregation"
)

// AggregationStore adapts the SQLite transaction to the layer-aggregation
// module's short transaction capability.
type AggregationStore struct{ *Store }

// WithTx implements aggregation.Store.
func (s AggregationStore) WithTx(ctx context.Context, fn func(context.Context, aggregation.Tx) error) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx *Tx) error { return fn(ctx, tx) })
}

var _ aggregation.Store = AggregationStore{}
