// Package retrieval owns keyword normalization, query compilation, filtering,
// hybrid ranking, snippets and explanations. It never changes memory facts.
package retrieval

import (
	"context"
	"encoding/json"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const (
	// DefaultTopK is the contracted result count when top_k is missing or
	// non-positive.
	DefaultTopK = 20
	// MaxTopK is the contracted upper bound. Larger values are a request error.
	MaxTopK = 100
	// CandidateLimitMin and CandidateLimitMax bound each keyword recall path
	// before coverage reranking or Reciprocal Rank Fusion.
	CandidateLimitMin = 100
	CandidateLimitMax = 500
	// RRFConstant is the Cormack et al. rank-fusion constant.
	RRFConstant = 60
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
	// Candidate settings 4/1, 2/1 and 1/1 remain possible; 4/1 stays the
	// default and is defined only here, never per request.
	FieldWeightTitle   = 4
	FieldWeightContent = 1

	// DefaultQueryBudget bounds the query Embedding plus vector scan inside one
	// hybrid request.
	DefaultQueryBudget = 500 * time.Millisecond
)

// Mode is the requested retrieval strategy.
type Mode string

const (
	ModeKeyword Mode = "keyword"
	ModeHybrid  Mode = "hybrid"
)

// Warning codes are stable, deduplicated and contain no provider data.
const (
	WarningSemanticUnavailable      = "semantic_unavailable"
	WarningSemanticTimeout          = "semantic_timeout"
	WarningFactConflict             = "fact_conflict"
	WarningConflictResultsTruncated = "conflict_results_truncated"
	WarningDerivedAsOfUnsupported   = "derived_as_of_unsupported"
	WarningDerivedUnavailable       = "derived_unavailable"
	WarningContextBudgetTruncated   = "context_budget_truncated"
)

// Layer selects one knowledge level in the unified recall entry point. Layers
// are independent from filters.kinds, which only narrows atomic memories.
type Layer string

const (
	LayerAtomic     Layer = "atomic"
	LayerSummary    Layer = "summary"
	LayerExperience Layer = "experience"
	LayerSkill      Layer = "skill"
)

// Known reports whether layer is part of the first-release vocabulary.
func (l Layer) Known() bool {
	switch l {
	case LayerAtomic, LayerSummary, LayerExperience, LayerSkill:
		return true
	default:
		return false
	}
}

// Filters narrow a search. Zero values mean no filter. Search defaults to
// active memories only; archived must be requested explicitly and deleted is
// never returned by keyword or semantic retrieval.
type Filters struct {
	Kinds           []memory.Kind
	States          []memory.State
	AssertionStatus []memory.AssertionStatus
	AsOf            *time.Time
	ScopeKey        string
	CreatedByApp    string
	UpdatedAfter    *time.Time
	UpdatedBefore   *time.Time
}

// Request is one Top-K search. Mode defaults to keyword and only controls
// retrieval strategy; it never changes the response shape.
type Request struct {
	Query   string
	Mode    Mode
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

// Query is the normalized keyword plan passed to the storage search capability.
// Expression is compiled only from quoted, escaped tokens and is always bound
// as a parameter; raw user text is never used as FTS syntax.
type Query struct {
	Groups     []Group
	Expression string
	Filters    Filters
	TopK       int
}

// Hit is a keyword-storage match before snippet and evidence assembly.
type Hit struct {
	MemoryID string
	BM25     float64
}

// VectorQuery is one exact cosine scan over filtered, current vectors.
type VectorQuery struct {
	ProfileID     string
	Dimensions    int
	Query         []float32
	Filters       Filters
	Limit         int
	MinSimilarity float64
}

// VectorHit is one semantic candidate. ContentHash ties the vector to the exact
// derived document it was generated from.
type VectorHit struct {
	MemoryID    string
	ContentHash string
	Cosine      float64
	UpdatedAt   time.Time
}

// Candidate is the current snapshot and derived document hash loaded in one
// short read transaction.
type Candidate struct {
	Memory      memory.Memory
	ContentHash string
}

// VersionKey identifies one current revision whose evidence must be loaded.
type VersionKey struct {
	MemoryID string
	Version  int
}

// Assembly is the final consistent snapshot and evidence set read in one short
// transaction. Current is false when the memory disappeared or its version
// changed after candidate selection.
type Assembly struct {
	Memory   memory.Memory
	Evidence []source.Evidence
	Current  bool
}

// EvidenceRef is a lightweight current-version evidence reference. Quote text
// is deliberately not part of a search hit.
type EvidenceRef struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
}

// KeywordExplanation reports the final keyword-path rank and raw BM25 score.
type KeywordExplanation struct {
	Rank int     `json:"rank"`
	BM25 float64 `json:"bm25"`
}

// SemanticExplanation reports the semantic-path rank and cosine similarity.
type SemanticExplanation struct {
	Rank   int     `json:"rank"`
	Cosine float64 `json:"cosine"`
}

// Explanation is returned only when Request.Explain is true. It never contains
// the profile, content hash, fusion constant or per-path contribution details.
type Explanation struct {
	Ranking      string               `json:"ranking"`
	FieldWeights [2]int               `json:"field_weights,omitempty"`
	Keyword      *KeywordExplanation  `json:"keyword,omitempty"`
	Semantic     *SemanticExplanation `json:"semantic,omitempty"`
}

// MarshalJSON omits field_weights for hybrid RRF explanations while keeping the
// keyword-mode field weights in the existing contract.
func (e Explanation) MarshalJSON() ([]byte, error) {
	type wire struct {
		Ranking      string               `json:"ranking"`
		FieldWeights *[2]int              `json:"field_weights,omitempty"`
		Keyword      *KeywordExplanation  `json:"keyword,omitempty"`
		Semantic     *SemanticExplanation `json:"semantic,omitempty"`
	}
	output := wire{
		Ranking:  e.Ranking,
		Keyword:  e.Keyword,
		Semantic: e.Semantic,
	}
	if e.FieldWeights != [2]int{} {
		weights := e.FieldWeights
		output.FieldWeights = &weights
	}
	return json.Marshal(output)
}

// Item is one search result.
type Item struct {
	Memory       memory.Memory        `json:"memory"`
	Score        float64              `json:"score"`
	Snippet      string               `json:"snippet"`
	MatchedTerms []string             `json:"matched_terms"`
	EvidenceRefs []EvidenceRef        `json:"evidence_refs"`
	Explanation  *Explanation         `json:"explanation,omitempty"`
	ConflictRefs []memory.ConflictRef `json:"conflict_refs"`
}

// Retrieval reports the strategy actually executed, independent of whether a
// path happened to return an empty set.
type Retrieval struct {
	Mode Mode `json:"mode"`
}

// Result is a single Top-K response. It has no cursor or next-page metadata.
type Result struct {
	Items     []Item                   `json:"items"`
	Retrieval Retrieval                `json:"retrieval"`
	Warnings  []string                 `json:"warnings,omitempty"`
	Conflicts []memory.ConflictSummary `json:"conflicts"`
}

// RecallRequest is the single unified recall entry. Layers default to every
// layer when omitted; filters.kinds only narrows the atomic partition.
type RecallRequest struct {
	Query    string
	ScopeKey string
	Layers   []Layer
	// LayersSpecified distinguishes an omitted layers field (all layers) from
	// an explicit empty list (no layers).
	LayersSpecified  bool
	Mode             Mode
	Filters          Filters
	TopK             int
	Explain          bool
	MaxContextTokens int
}

// RecallResult preserves the knowledge partitions instead of forcing atomic
// memories and derived artifacts into one item shape.
type RecallResult struct {
	Memories    []Item
	Summaries   []artifact.SearchHit
	Experiences []artifact.SearchHit
	Skills      []artifact.SearchHit
	Conflicts   []memory.ConflictSummary
	Warnings    []string
}

// RebuildResult reports a successful, committed index rebuild.
type RebuildResult struct {
	Indexed  int           `json:"indexed"`
	Duration time.Duration `json:"duration"`
}

// SearchService is the internal atomic search capability.
type SearchService interface {
	Search(ctx context.Context, request Request) (Result, error)
}

// RecallService is the single public recall capability.
type RecallService interface {
	Recall(ctx context.Context, request RecallRequest) (RecallResult, error)
}

// RelatedService exposes keyword-based context selection to ingest without
// changing the public search request/response contract.
type RelatedService interface {
	RelatedContext(ctx context.Context, text string, limit int) (ContextResult, error)
	FactTimeline(ctx context.Context, groupID string, limit int) (ContextResult, error)
	OpenConflictsForMemories(ctx context.Context, memoryIDs []string) ([]memory.Conflict, error)
	MemoriesByIDs(ctx context.Context, ids []string) ([]memory.Memory, error)
	CatalogGeneration(ctx context.Context) (int64, error)
}

// ContextResult is a bounded consistency-context load. Complete is false when
// the fixed cap was reached; callers must then avoid destructive rewrites.
type ContextResult struct {
	Memories []memory.Memory
	Complete bool
	Warnings []string
}

// IndexMaintainer rebuilds derived search documents and FTS from current
// business snapshots.
type IndexMaintainer interface {
	RebuildIndex(ctx context.Context) (RebuildResult, error)
}
