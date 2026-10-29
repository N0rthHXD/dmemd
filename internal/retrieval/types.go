// Package retrieval owns keyword normalization, query compilation, filtering,
// hybrid ranking, snippets and explanations. It never changes memory facts.
//
// Recall is layer-aware: L0 observations, canonical memories and the L1-L4
// derived hierarchy are selected by query target instead of being concatenated
// into one ranked list. High-level derived results that are stale or missing can
// fall back to the next lower layer without blocking an HTTP request on a model.
package retrieval

import (
	"context"
	"encoding/json"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const (
	// DefaultTopK is the contracted per-layer result count when top_k is
	// missing or non-positive.
	DefaultTopK = 20
	// MaxTopK is the contracted upper bound. Larger values are a request error.
	MaxTopK = 100
	// CandidateLimitMin and CandidateLimitMax bound each keyword recall path
	// before coverage reranking or Reciprocal Rank Fusion.
	CandidateLimitMin = 100
	CandidateLimitMax = 500
	// RRFConstant is the Cormack et al. rank-fusion constant.
	RRFConstant = 60
	// DefaultSnippetRunes bounds a readable snippet taken from original content.
	DefaultSnippetRunes = 160
	// FieldWeightTitle and FieldWeightContent are the FTS5 BM25 column weights.
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
	WarningSemanticConfigError      = "semantic_config_error"
	WarningSemanticRateLimited      = "semantic_rate_limited"
	WarningSemanticInvalidResponse  = "semantic_invalid_response"
	WarningSemanticIndexNotReady    = "semantic_index_not_ready"
	WarningFactConflict             = "fact_conflict"
	WarningConflictResultsTruncated = "conflict_results_truncated"
	WarningDerivedUnavailable       = "derived_unavailable"
	WarningDerivedStaleFallback     = "derived_stale_fallback"
	WarningStaleIncluded            = "stale_included"
	WarningObservationsUnavailable  = "observations_unavailable"
	WarningRelationsUnavailable     = "relations_unavailable"
	WarningContextBudgetTruncated   = "context_budget_truncated"
)

// Layer selects one knowledge level in the unified recall entry point. Layers
// are independent from filters.kinds, which only narrows the selected content
// vocabulary.
type Layer string

const (
	LayerObservations Layer = "observations"
	LayerMemories     Layer = "memories"
	LayerL1           Layer = "l1"
	LayerL2           Layer = "l2"
	LayerL3           Layer = "l3"
	LayerL4           Layer = "l4"
)

// Known reports whether layer is part of the fixed recall vocabulary.
func (l Layer) Known() bool {
	switch l {
	case LayerObservations, LayerMemories, LayerL1, LayerL2, LayerL3, LayerL4:
		return true
	default:
		return false
	}
}

// Derived reports whether layer belongs to the L1-L4 aggregation hierarchy.
func (l Layer) Derived() bool {
	switch l {
	case LayerL1, LayerL2, LayerL3, LayerL4:
		return true
	default:
		return false
	}
}

// ArtifactLayer maps one recall layer onto the persisted derived hierarchy.
func (l Layer) ArtifactLayer() artifact.Layer {
	switch l {
	case LayerL1:
		return artifact.LayerL1
	case LayerL2:
		return artifact.LayerL2
	case LayerL3:
		return artifact.LayerL3
	case LayerL4:
		return artifact.LayerL4
	default:
		return ""
	}
}

// Lower returns the next lower recall layer used when a stale derived result
// cannot be shown as a current conclusion.
func (l Layer) Lower() (Layer, bool) {
	switch l {
	case LayerL4:
		return LayerL3, true
	case LayerL3:
		return LayerL2, true
	case LayerL2:
		return LayerL1, true
	case LayerL1:
		return LayerObservations, true
	case LayerMemories:
		return LayerObservations, true
	default:
		return "", false
	}
}

// QueryMode selects the layer combination for one recall request. It is a
// semantic target, not a retrieval strategy; Mode still selects keyword or
// hybrid ranking inside the selected layers.
type QueryMode string

const (
	QueryModeUnspecified QueryMode = ""
	QueryModeOverview    QueryMode = "overview"
	QueryModeDetail      QueryMode = "detail"
	QueryModeHistory     QueryMode = "history"
	QueryModeConflict    QueryMode = "conflict"
	QueryModeGuidance    QueryMode = "guidance"
)

// Known reports whether mode is part of the fixed query-target vocabulary.
func (m QueryMode) Known() bool {
	switch m {
	case QueryModeUnspecified, QueryModeOverview, QueryModeDetail,
		QueryModeHistory, QueryModeConflict, QueryModeGuidance:
		return true
	default:
		return false
	}
}

// Layers returns the fixed default layer selection for one query target.
func (m QueryMode) Layers() map[Layer]bool {
	layers := map[Layer]bool{}
	switch m {
	case QueryModeOverview:
		layers[LayerL2] = true
		layers[LayerL3] = true
		layers[LayerL1] = true
	case QueryModeDetail:
		layers[LayerObservations] = true
		layers[LayerMemories] = true
	case QueryModeHistory, QueryModeConflict:
		layers[LayerObservations] = true
		layers[LayerMemories] = true
	case QueryModeGuidance:
		layers[LayerL4] = true
	default:
		for _, layer := range []Layer{LayerObservations, LayerMemories,
			LayerL1, LayerL2, LayerL3, LayerL4} {
			layers[layer] = true
		}
	}
	return layers
}

// Filters narrow a search. Zero values mean no filter. Search defaults to
// active memories only; archived must be requested explicitly and deleted is
// never returned by keyword or semantic retrieval. Temporally scoped queries
// belong to a future annotation capability and are not part of this contract.
type Filters struct {
	Kinds               []memory.Kind
	States              []memory.State
	AssertionStatus     []memory.AssertionStatus
	ObservationStatuses []observation.Status
	ScopeKey            string
	UpdatedAfter        *time.Time
	UpdatedBefore       *time.Time
}

// Request is one Top-K canonical-memory search. Mode defaults to keyword and
// only controls retrieval strategy; it never changes the response shape.
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

// ObservationQuery is the normalized keyword plan for the L0 observation index.
type ObservationQuery struct {
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

// ObservationHit is one L0 keyword match with its code-generated evidence
// references already loaded.
type ObservationHit struct {
	Observation  observation.Observation
	Score        float64
	EvidenceRefs []EvidenceRef
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

// SearchDiagnostics reports bounded stage counts when explain=true. Counts are
// candidate counts, not proof of semantic correctness; a relaxed count never
// changes the returned result set.
type SearchDiagnostics struct {
	KeywordCandidates      int    `json:"keyword_candidates"`
	ObservationCandidates  int    `json:"observation_candidates"`
	MemoryCandidates       int    `json:"memory_candidates"`
	DerivedCandidates      int    `json:"derived_candidates"`
	SemanticCandidates     int    `json:"semantic_candidates"`
	SemanticExcluded       int    `json:"semantic_excluded"`
	TermCandidates         int    `json:"term_candidates"`
	KindFilterExcluded     int    `json:"kind_filter_excluded"`
	StateFilterExcluded    int    `json:"state_filter_excluded"`
	RankingCandidates      int    `json:"ranking_candidates"`
	ReturnedItems          int    `json:"returned_items"`
	ContextBudgetTruncated int    `json:"context_budget_truncated"`
	SemanticDegradation    string `json:"semantic_degradation,omitempty"`
}

// Item is one canonical-memory search result.
type Item struct {
	Memory       memory.Memory        `json:"memory"`
	Score        float64              `json:"score"`
	Snippet      string               `json:"snippet"`
	MatchedTerms []string             `json:"matched_terms"`
	EvidenceRefs []EvidenceRef        `json:"evidence_refs"`
	Explanation  *Explanation         `json:"explanation,omitempty"`
	ConflictRefs []memory.ConflictRef `json:"conflict_refs"`
}

// ObservationItem is one L0 recall result. The observation content is raw
// extracted text; EvidenceRefs are generated by code from source spans.
type ObservationItem struct {
	Observation  observation.Observation `json:"observation"`
	Score        float64                 `json:"score"`
	Snippet      string                  `json:"snippet"`
	MatchedTerms []string                `json:"matched_terms"`
	EvidenceRefs []EvidenceRef           `json:"evidence_refs"`
	Explanation  *Explanation            `json:"explanation,omitempty"`
}

// DerivedItem is one L1-L4 recall result. Dependencies are the drill-down
// references of the exact current revision; they never come from model output.
type DerivedItem struct {
	DerivedMemory artifact.DerivedMemory   `json:"artifact"`
	Revision      artifact.Revision        `json:"revision"`
	Score         float64                  `json:"score"`
	Snippet       string                   `json:"snippet"`
	MatchedTerms  []string                 `json:"matched_terms"`
	Dependencies  []artifact.DependencyRef `json:"dependencies"`
	Explanation   *Explanation             `json:"explanation,omitempty"`
}

// Retrieval reports the strategy actually executed, independent of whether a
// path happened to return an empty set.
type Retrieval struct {
	Mode Mode `json:"mode"`
}

// Result is a single Top-K response. It has no cursor or next-page metadata.
type Result struct {
	Items       []Item                   `json:"items"`
	Retrieval   Retrieval                `json:"retrieval"`
	Warnings    []string                 `json:"warnings,omitempty"`
	Conflicts   []memory.ConflictSummary `json:"conflicts"`
	Diagnostics *SearchDiagnostics       `json:"diagnostics,omitempty"`
}

// RecallRequest is the single unified recall entry. QueryMode selects a fixed
// target layer set; an explicit Layers list takes precedence. Each selected
// layer receives its own TopK bound.
type RecallRequest struct {
	Query     string
	ScopeKey  string
	QueryMode QueryMode
	Layers    []Layer
	// LayersSpecified distinguishes an omitted layers field (query-mode/default
	// selection) from an explicit empty list (no layers).
	LayersSpecified bool
	Mode            Mode
	Filters         Filters
	// DerivedStates defaults to active. Requesting stale explicitly returns the
	// marked historical result instead of treating it as a current conclusion.
	DerivedStates    []artifact.State
	TopK             int
	Explain          bool
	MaxContextTokens int
}

// RecallResult preserves the knowledge partitions instead of forcing raw,
// canonical and derived memories into one item shape.
type RecallResult struct {
	QueryMode    QueryMode                `json:"query_mode,omitempty"`
	Observations []ObservationItem        `json:"observations"`
	Memories     []Item                   `json:"memories"`
	L1           []DerivedItem            `json:"l1"`
	L2           []DerivedItem            `json:"l2"`
	L3           []DerivedItem            `json:"l3"`
	L4           []DerivedItem            `json:"l4"`
	Relations    []governance.Relation    `json:"relations,omitempty"`
	Conflicts    []memory.ConflictSummary `json:"conflicts"`
	Warnings     []string                 `json:"warnings,omitempty"`
	Diagnostics  *SearchDiagnostics       `json:"diagnostics,omitempty"`
}

// Derived returns the result partition of one L1-L4 layer.
func (r RecallResult) Derived(layer Layer) []DerivedItem {
	switch layer {
	case LayerL1:
		return r.L1
	case LayerL2:
		return r.L2
	case LayerL3:
		return r.L3
	case LayerL4:
		return r.L4
	default:
		return nil
	}
}

// SetDerived replaces one L1-L4 result partition.
func (r *RecallResult) SetDerived(layer Layer, items []DerivedItem) {
	if items == nil {
		items = []DerivedItem{}
	}
	switch layer {
	case LayerL1:
		r.L1 = items
	case LayerL2:
		r.L2 = items
	case LayerL3:
		r.L3 = items
	case LayerL4:
		r.L4 = items
	}
}

// RebuildResult reports a successful, committed index rebuild.
type RebuildResult struct {
	Indexed  int           `json:"indexed"`
	Duration time.Duration `json:"duration"`
}

// SearchService is the internal canonical-memory search capability.
type SearchService interface {
	Search(ctx context.Context, request Request) (Result, error)
}

// RecallService is the single public recall capability.
type RecallService interface {
	Recall(ctx context.Context, request RecallRequest) (RecallResult, error)
}

// IndexMaintainer rebuilds derived search documents and FTS from current
// business snapshots.
type IndexMaintainer interface {
	RebuildIndex(ctx context.Context) (RebuildResult, error)
}
