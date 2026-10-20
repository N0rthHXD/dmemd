package retrieval

import (
	"errors"
)

// ErrInvalidQuery is reserved for a query that cannot be compiled at all. The
// HTTP contract treats an empty query as a valid request that returns no items,
// so normal user text does not produce this error.
var ErrInvalidQuery = errors.New("invalid search query")
