package retrieval

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrInvalidQuery is returned for an internally inconsistent query plan. The
// HTTP contract treats empty or punctuation-only user input as a valid request
// that returns no items, so ordinary user text does not produce this error.
var ErrInvalidQuery = errors.New("invalid search query")

// ErrDocumentBuilderMissing is returned by index maintenance when no production
// document builder was configured.
var ErrDocumentBuilderMissing = errors.New("retrieval: 搜索文档构造器未配置")

// Searcher is the read capability implemented by store/sqlite.
type Searcher interface {
	Search(ctx context.Context, query Query) ([]Hit, error)
}

// Reader loads current snapshots and their complete current evidence set for
// result assembly. It never mutates facts.
type Reader interface {
	GetMemory(ctx context.Context, id string) (memory.Memory, error)
	ListRevisionEvidence(ctx context.Context, memoryID string, version int) ([]source.Evidence, error)
}

// IndexWriter regenerates derived search documents from non-deleted business
// snapshots and rebuilds FTS in one transaction.
type IndexWriter interface {
	RebuildSearchDocuments(ctx context.Context, builder memory.DocumentBuilder) (int, error)
}

// Backend is the persistence capability set consumed by the retrieval service.
type Backend interface {
	Searcher
	Reader
	IndexWriter
}
