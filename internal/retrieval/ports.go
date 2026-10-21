package retrieval

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

// ErrInvalidQuery is returned for an internally inconsistent keyword plan. The
// HTTP contract treats empty or punctuation-only user input as a valid request
// that returns no items, so ordinary user text does not produce this error.
var ErrInvalidQuery = errors.New("invalid search query")

// ErrInvalidRequest is returned for a contract violation such as an unknown
// mode or a top_k above MaxTopK. The HTTP layer maps it to 400.
var ErrInvalidRequest = errors.New("invalid search request")

// ErrDocumentBuilderMissing is returned by index maintenance when no production
// document builder was configured.
var ErrDocumentBuilderMissing = errors.New("retrieval: 搜索文档构造器未配置")

// Searcher is the keyword read capability implemented by store/sqlite.
type Searcher interface {
	Search(ctx context.Context, query Query) ([]Hit, error)
}

// VectorSearcher is the exact, filtered cosine scan. It only reads vectors whose
// profile and dimensions match and whose content hash still matches the current
// derived document.
type VectorSearcher interface {
	SearchVectors(ctx context.Context, query VectorQuery) ([]VectorHit, error)
}

// CandidateReader loads current snapshots, derived document hashes and the
// final Top-K snapshot/evidence pairs in short read transactions.
type CandidateReader interface {
	LoadSearchCandidates(ctx context.Context, ids []string) (map[string]Candidate, error)
	LoadSearchAssemblies(ctx context.Context, keys []VersionKey) (map[VersionKey]Assembly, error)
}

// IndexWriter regenerates derived search documents from non-deleted business
// snapshots and rebuilds FTS in one transaction.
type IndexWriter interface {
	RebuildSearchDocuments(ctx context.Context, builder memory.DocumentBuilder) (int, error)
}

// Backend is the persistence capability set consumed by the retrieval service.
type Backend interface {
	Searcher
	VectorSearcher
	CandidateReader
	IndexWriter
}

// QueryEncoder is the online Embedding capability. It is nil when semantic
// retrieval is disabled.
type QueryEncoder interface {
	ProfileID() string
	Dimensions() int
	MinSimilarity() float64
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}
