package runtime

import (
	"log/slog"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

// NewMemoryService wires the internal memory write/read service with the
// production search-document builder. Ingest and HTTP assembly use this helper
// instead of injecting the deterministic test builder.
func NewMemoryService(store *sqlite.Store) *memory.Service {
	return memory.NewService(store, sqlite.MemoryStore{Store: store}, retrieval.NewDocumentBuilder())
}

// NewSearchService wires keyword search and index maintenance with the same
// production normalization rules used by NewMemoryService.
func NewSearchService(store *sqlite.Store, logger *slog.Logger) *retrieval.Service {
	return retrieval.NewService(store, retrieval.NewDocumentBuilder(), retrieval.WithLogger(logger))
}
