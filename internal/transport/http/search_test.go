package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

func TestRecallModeTopKValidationAndRequestMapping(t *testing.T) {
	var captured retrieval.RecallRequest
	calls := 0
	services := Services{Search: stubSearch{recall: func(_ context.Context, request retrieval.RecallRequest) (retrieval.RecallResult, error) {
		calls++
		captured = request
		return retrieval.RecallResult{Memories: []retrieval.Item{}}, nil
	}}}

	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/recall",
		strings.NewReader(`{"query":"吃饭有什么忌口","scope_key":"project:dmemd","layers":["atomic","summary"],"mode":"hybrid","top_k":10,"explain":true}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("hybrid request = %d %q", response.Code, response.Body.String())
	}
	if calls != 1 || captured.Mode != retrieval.ModeHybrid ||
		captured.TopK != 10 || !captured.Explain || captured.ScopeKey != "project:dmemd" {
		t.Fatalf("captured request = %+v", captured)
	}
	if len(captured.Layers) != 2 || captured.Layers[0] != retrieval.LayerAtomic ||
		captured.Layers[1] != retrieval.LayerSummary {
		t.Fatalf("layers = %+v", captured.Layers)
	}
	if !strings.Contains(response.Body.String(), `"memories":[]`) {
		t.Fatalf("response = %q", response.Body.String())
	}

	for _, body := range []string{
		`{"query":"x","mode":"semantic"}`,
		`{"query":"x","top_k":101}`,
		`{"query":"x","layers":["episode"]}`,
	} {
		before := calls
		response = serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/recall", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s = %d %q", body, response.Code, response.Body.String())
		}
		if calls != before {
			t.Fatalf("invalid request %s called the service", body)
		}
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/recall",
		strings.NewReader(`{"query":"x"}`)))
	if response.Code != http.StatusOK || captured.Mode != retrieval.ModeKeyword {
		t.Fatalf("default mode = %d %+v", response.Code, captured)
	}
	if len(captured.Layers) != 0 {
		t.Fatalf("omitted layers should reach the service empty: %+v", captured.Layers)
	}
}

func TestRecallResponseIncludesPartitionsWarningsAndExplanation(t *testing.T) {
	created := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	services := Services{Search: stubSearch{recall: func(context.Context, retrieval.RecallRequest) (retrieval.RecallResult, error) {
		return retrieval.RecallResult{
			Memories: []retrieval.Item{{
				Memory:       memory.Memory{ID: "memory-1", Kind: memory.KindFact, State: memory.StateActive, Version: 1},
				Score:        1.0 / 61.0,
				Snippet:      "摘要",
				MatchedTerms: []string{},
				EvidenceRefs: []retrieval.EvidenceRef{},
				Explanation: &retrieval.Explanation{
					Ranking:  "rrf",
					Keyword:  &retrieval.KeywordExplanation{Rank: 1, BM25: -1.5},
					Semantic: &retrieval.SemanticExplanation{Rank: 2, Cosine: 0.9},
				},
				Related: &retrieval.RelatedInfo{
					ReasonCodes:     []string{retrieval.RelatedFactGroupMember},
					AnchorMemoryIDs: []string{"memory-primary"},
				},
				Cluster: &retrieval.DisplayCluster{
					FactGroupID: "group-1", MemberIDs: []string{"memory-1", "memory-2"}, EvidenceCount: 2,
				},
			}},
			Diagnostics: &retrieval.SearchDiagnostics{
				KeywordCandidates: 3, SemanticCandidates: 2, TermCandidates: 5,
				KindFilterExcluded: 1, StateFilterExcluded: 1, TimeFilterExcluded: 1,
				RankingCandidates: 2, RelatedExpanded: 1, RelatedExcluded: 1,
				ReturnedItems: 1, ContextBudgetTruncated: 1,
				SemanticDegradation: retrieval.WarningSemanticTimeout,
			},
			Summaries: []artifact.SearchHit{{
				Artifact: artifact.Artifact{
					ID: "artifact-1", Kind: artifact.KindTopicSummary, ScopeKey: "project:dmemd",
					Topic: "go", State: artifact.StateActive, CurrentVersion: 1, UpdatedAt: created,
				},
				Revision: artifact.Revision{
					ArtifactID: "artifact-1", Version: 1, Title: "Go 摘要", Content: "使用 SQLite",
					MemoryRefs: []artifact.MemoryRef{{MemoryID: "memory-1", Version: 1}}, CreatedAt: created,
				},
				Score: -1.2,
			}},
			Warnings: []string{retrieval.WarningSemanticTimeout},
		}, nil
	}}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/recall",
		strings.NewReader(`{"query":"x","mode":"hybrid","explain":true}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	var decoded struct {
		Memories []struct {
			MatchedTerms []string `json:"matched_terms"`
			Explanation  struct {
				Ranking      string          `json:"ranking"`
				FieldWeights json.RawMessage `json:"field_weights"`
				Keyword      struct {
					Rank int     `json:"rank"`
					BM25 float64 `json:"bm25"`
				} `json:"keyword"`
				Semantic struct {
					Rank   int     `json:"rank"`
					Cosine float64 `json:"cosine"`
				} `json:"semantic"`
			} `json:"explanation"`
			Related *struct {
				ReasonCodes     []string `json:"reason_codes"`
				AnchorMemoryIDs []string `json:"anchor_memory_ids"`
			} `json:"related"`
			Cluster *struct {
				FactGroupID   string   `json:"fact_group_id"`
				MemberIDs     []string `json:"member_ids"`
				EvidenceCount int      `json:"evidence_count"`
			} `json:"cluster"`
		} `json:"memories"`
		Diagnostics *struct {
			KeywordCandidates      int    `json:"keyword_candidates"`
			KindFilterExcluded     int    `json:"kind_filter_excluded"`
			RelatedExpanded        int    `json:"related_expanded"`
			ContextBudgetTruncated int    `json:"context_budget_truncated"`
			SemanticDegradation    string `json:"semantic_degradation"`
		} `json:"diagnostics"`
		Summaries []struct {
			ID         string `json:"id"`
			Kind       string `json:"kind"`
			Version    int    `json:"version"`
			MemoryRefs []struct {
				MemoryID string `json:"memory_id"`
				Version  int    `json:"version"`
			} `json:"memory_refs"`
		} `json:"summaries"`
		Experiences []any    `json:"experiences"`
		Skills      []any    `json:"skills"`
		Conflicts   []any    `json:"conflicts"`
		Warnings    []string `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Warnings) != 1 || decoded.Warnings[0] != retrieval.WarningSemanticTimeout {
		t.Fatalf("warnings = %+v", decoded.Warnings)
	}
	if len(decoded.Memories) != 1 || decoded.Memories[0].Explanation.Ranking != "rrf" {
		t.Fatalf("memories = %+v", decoded.Memories)
	}
	item := decoded.Memories[0]
	if item.Related == nil || len(item.Related.ReasonCodes) != 1 ||
		item.Related.ReasonCodes[0] != retrieval.RelatedFactGroupMember ||
		len(item.Related.AnchorMemoryIDs) != 1 || item.Related.AnchorMemoryIDs[0] != "memory-primary" {
		t.Fatalf("related = %+v", item.Related)
	}
	if item.Cluster == nil || item.Cluster.FactGroupID != "group-1" ||
		len(item.Cluster.MemberIDs) != 2 || item.Cluster.EvidenceCount != 2 {
		t.Fatalf("cluster = %+v", item.Cluster)
	}
	if decoded.Diagnostics == nil || decoded.Diagnostics.KeywordCandidates != 3 ||
		decoded.Diagnostics.KindFilterExcluded != 1 || decoded.Diagnostics.RelatedExpanded != 1 ||
		decoded.Diagnostics.ContextBudgetTruncated != 1 ||
		decoded.Diagnostics.SemanticDegradation != retrieval.WarningSemanticTimeout {
		t.Fatalf("diagnostics = %+v", decoded.Diagnostics)
	}
	explanation := decoded.Memories[0].Explanation
	if len(explanation.FieldWeights) != 0 || explanation.Keyword.Rank != 1 || explanation.Semantic.Rank != 2 {
		t.Fatalf("explanation = %+v", explanation)
	}
	if len(decoded.Summaries) != 1 || decoded.Summaries[0].ID != "artifact-1" ||
		decoded.Summaries[0].Kind != "topic_summary" ||
		len(decoded.Summaries[0].MemoryRefs) != 1 || decoded.Summaries[0].MemoryRefs[0].MemoryID != "memory-1" {
		t.Fatalf("summaries = %+v", decoded.Summaries)
	}
	if decoded.Experiences == nil || decoded.Skills == nil || decoded.Conflicts == nil {
		t.Fatalf("empty partitions must be arrays: %+v", decoded)
	}
}

func TestKeywordResponseKeepsFieldWeightsAndNoRetrievalEnvelope(t *testing.T) {
	services := Services{Search: stubSearch{recall: func(context.Context, retrieval.RecallRequest) (retrieval.RecallResult, error) {
		return retrieval.RecallResult{Memories: []retrieval.Item{{
			Memory:       memory.Memory{ID: "m", State: memory.StateActive, Version: 1},
			MatchedTerms: []string{"回答"},
			EvidenceRefs: []retrieval.EvidenceRef{},
			Explanation: &retrieval.Explanation{
				Ranking:      "bm25",
				FieldWeights: [2]int{retrieval.FieldWeightTitle, retrieval.FieldWeightContent},
			},
		}}}, nil
	}}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/recall",
		strings.NewReader(`{"query":"回答","explain":true}`)))
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %q", response.Code, body)
	}
	if !strings.Contains(body, `"field_weights":[4,1]`) {
		t.Fatalf("keyword explanation = %q", body)
	}
	if strings.Contains(body, `"retrieval"`) || strings.Contains(body, `"items"`) {
		t.Fatalf("old recall envelope leaked into response: %q", body)
	}
}
