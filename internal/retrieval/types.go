// Package retrieval owns keyword normalization, query compilation, filtering,
// ranking, snippets and explanations. It never changes memory facts.
package retrieval

import (
	"context"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

// Match controls how whitespace-separated query groups are combined.
type Match string

const (
	MatchAll Match = "all"
	MatchAny Match = "any"
)

// Filters narrow a search. Zero values mean no filter. Search defaults to
// active memories only; archived must be requested explicitly and deleted is
// never returned by keyword search.
type Filters struct {
	Kinds         []memory.Kind
	States        []memory.State
	CreatedByApp  string
	UpdatedAfter  *time.Time
	UpdatedBefore *time.Time
}

// Request is a keyword Top-K search. TopK zero means the contract default (20).
type Request struct {
	Query   string
	Match   Match
	Filters Filters
	TopK    int
	Explain bool
}

// EvidenceRef is a lightweight current-version evidence reference. Quote text
// is deliberately not part of a search hit.
type EvidenceRef struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
}

// Explanation is returned only when Request.Explain is true.
type Explanation struct {
	Ranking      string `json:"ranking"`
	FieldWeights [2]int `json:"field_weights"`
}

// Item is one search result.
type Item struct {
	Memory       memory.Memory `json:"memory"`
	Score        float64       `json:"score"`
	Snippet      string        `json:"snippet"`
	MatchedTerms []string      `json:"matched_terms"`
	EvidenceRefs []EvidenceRef `json:"evidence_refs"`
	Explanation  *Explanation  `json:"explanation,omitempty"`
}

// Result is a single Top-K response. It has no cursor or next-page metadata.
type Result struct {
	Items []Item `json:"items"`
}

// SearchService is the public keyword search capability.
type SearchService interface {
	Search(ctx context.Context, request Request) (Result, error)
}

// Query is the normalized plan passed to the storage search capability.
type Query struct {
	Match   Match
	Terms   []string
	Filters Filters
	TopK    int
}

// Hit is a storage-level match before snippet and evidence assembly.
type Hit struct {
	MemoryID string
	BM25     float64
}

// Searcher is the read capability implemented by store/sqlite.
type Searcher interface {
	Search(ctx context.Context, query Query) ([]Hit, error)
}
