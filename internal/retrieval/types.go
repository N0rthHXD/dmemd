// Package retrieval owns keyword normalization, query compilation, filtering,
// ranking, snippets and explanations. It never changes memory facts.
package retrieval

import (
	"context"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

const (
	// DefaultTopK is the contracted result count when top_k is missing or
	// non-positive.
	DefaultTopK = 20
	// DefaultRelatedLimit is the fixed maximum number of related active
	// memories supplied to background extraction.
	DefaultRelatedLimit = 10
	// MaxRelatedQueryGroups bounds the keyword groups derived from one source
	// when building extraction context. The full user submission is still
	// accepted; only this derived query is bounded.
	MaxRelatedQueryGroups = 32
	// DefaultSnippetRunes bounds a readable snippet taken from original content.
	DefaultSnippetRunes = 160
	// FieldWeightTitle and FieldWeightContent are the FTS5 BM25 column weights.
	FieldWeightTitle   = 4
	FieldWeightContent = 1
)

// Match controls how dictionary terms in a query are combined.
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

// Group is one query unit. Text is the normalized, readable term used for
// matched_terms; Tokens is its FTS token sequence. Groups produced by Groups
// contain one dictionary term each.
type Group struct {
	Text   string
	Tokens []string
}

// Query is the normalized plan passed to the storage search capability.
// Expression is compiled only from quoted, escaped tokens and is always bound
// as a parameter; raw user text is never used as FTS syntax.
type Query struct {
	Match      Match
	Groups     []Group
	Expression string
	Filters    Filters
	TopK       int
}

// Hit is a storage-level match before snippet and evidence assembly.
type Hit struct {
	MemoryID string
	BM25     float64
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

// RebuildResult reports a successful, committed index rebuild.
type RebuildResult struct {
	Indexed  int           `json:"indexed"`
	Duration time.Duration `json:"duration"`
}

// SearchService is the public keyword search capability.
type SearchService interface {
	Search(ctx context.Context, request Request) (Result, error)
}

// RelatedService exposes keyword-based context selection to ingest without
// changing the public search request/response contract.
type RelatedService interface {
	RelatedActive(ctx context.Context, text string, limit int) ([]memory.Memory, error)
}

// IndexMaintainer rebuilds derived search documents and FTS from current
// business snapshots.
type IndexMaintainer interface {
	RebuildIndex(ctx context.Context) (RebuildResult, error)
}
