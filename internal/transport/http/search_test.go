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
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
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
		strings.NewReader(`{"query":"吃饭有什么忌口","scope_key":"project:dmemd","query_mode":"detail","layers":["observations","memories"],"mode":"hybrid","top_k":10,"explain":true}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("hybrid request = %d %q", response.Code, response.Body.String())
	}
	if calls != 1 || captured.Mode != retrieval.ModeHybrid ||
		captured.TopK != 10 || !captured.Explain || captured.ScopeKey != "project:dmemd" ||
		captured.QueryMode != retrieval.QueryModeDetail {
		t.Fatalf("captured request = %+v", captured)
	}
	if len(captured.Layers) != 2 || captured.Layers[0] != retrieval.LayerObservations ||
		captured.Layers[1] != retrieval.LayerMemories {
		t.Fatalf("layers = %+v", captured.Layers)
	}
	if !strings.Contains(response.Body.String(), `"memories":[]`) {
		t.Fatalf("response = %q", response.Body.String())
	}

	for _, body := range []string{
		`{"query":"x","mode":"semantic"}`,
		`{"query":"x","top_k":101}`,
		`{"query":"x","layers":["episode"]}`,
		`{"query":"x","query_mode":"invented"}`,
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

func TestRecallResponseIncludesLayerPartitionsWarningsAndExplanation(t *testing.T) {
	created := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	services := Services{Search: stubSearch{recall: func(context.Context, retrieval.RecallRequest) (retrieval.RecallResult, error) {
		return retrieval.RecallResult{
			QueryMode: retrieval.QueryModeDetail,
			Observations: []retrieval.ObservationItem{{
				Observation: observation.Observation{
					ID: "observation-1", SourceID: "source-1", Kind: observation.KindFact,
					Content: "项目使用 SQLite 保存记忆。", ContentMode: observation.ContentExtractive,
					Status: observation.StatusObserved, CreatedAt: created, UpdatedAt: created,
				},
				Score:        1.5,
				Snippet:      "项目使用 SQLite",
				MatchedTerms: []string{"项目"},
				EvidenceRefs: []retrieval.EvidenceRef{{ID: "evidence-1", SourceID: "source-1"}},
			}},
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
			}},
			L1: []retrieval.DerivedItem{{
				DerivedMemory: artifact.DerivedMemory{
					ID: "artifact-1", Layer: artifact.LayerL1, Kind: artifact.KindLocalSummary,
					ScopeKey: "project:dmemd", State: artifact.StateActive, CurrentVersion: 1, UpdatedAt: created,
				},
				Revision: artifact.Revision{
					ArtifactID: "artifact-1", Version: 1, Title: "Go 摘要", Content: "使用 SQLite",
					CreatedAt: created,
				},
				Score:        -1.2,
				Snippet:      "使用 SQLite",
				MatchedTerms: []string{"SQLite"},
				Dependencies: []artifact.DependencyRef{{
					Type: artifact.DependencyObservation, ID: "observation-1", Required: true,
				}},
			}},
			L4: []retrieval.DerivedItem{{
				DerivedMemory: artifact.DerivedMemory{
					ID: "artifact-skill", Layer: artifact.LayerL4, Kind: artifact.KindSkill,
					ScopeKey: "project:dmemd", State: artifact.StateActive, CurrentVersion: 1, UpdatedAt: created,
				},
				Revision: artifact.Revision{ArtifactID: "artifact-skill", Version: 1, Title: "验证流程", Content: "运行测试", CreatedAt: created},
			}},
			Relations: []governance.Relation{{
				ID: "relation-1", Kind: governance.RelationSupports,
				FromType: governance.EndpointObservation, FromID: "observation-1",
				ToType: governance.EndpointMemory, ToID: "memory-1", ToVersion: 1, CreatedAt: created,
			}},
			Diagnostics: &retrieval.SearchDiagnostics{
				KeywordCandidates: 3, ObservationCandidates: 1, MemoryCandidates: 1,
				DerivedCandidates: 2, TermCandidates: 5, KindFilterExcluded: 1,
				StateFilterExcluded: 1, RankingCandidates: 2, ReturnedItems: 4,
				ContextBudgetTruncated: 1, SemanticDegradation: retrieval.WarningSemanticTimeout,
			},
			Warnings: []string{retrieval.WarningSemanticTimeout},
		}, nil
	}}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/recall",
		strings.NewReader(`{"query":"x","mode":"hybrid","explain":true}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	var decoded struct {
		QueryMode    string `json:"query_mode"`
		Observations []struct {
			Observation struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"observation"`
			EvidenceRefs []struct {
				ID string `json:"id"`
			} `json:"evidence_refs"`
		} `json:"observations"`
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
		} `json:"memories"`
		L1 []struct {
			Artifact struct {
				ID    string `json:"id"`
				Kind  string `json:"kind"`
				State string `json:"state"`
			} `json:"artifact"`
			Dependencies []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"dependencies"`
		} `json:"l1"`
		L4 []struct {
			Artifact struct {
				Kind string `json:"kind"`
			} `json:"artifact"`
		} `json:"l4"`
		Relations []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"relations"`
		Conflicts   []any    `json:"conflicts"`
		Warnings    []string `json:"warnings"`
		Diagnostics *struct {
			KeywordCandidates      int    `json:"keyword_candidates"`
			ObservationCandidates  int    `json:"observation_candidates"`
			DerivedCandidates      int    `json:"derived_candidates"`
			KindFilterExcluded     int    `json:"kind_filter_excluded"`
			ContextBudgetTruncated int    `json:"context_budget_truncated"`
			SemanticDegradation    string `json:"semantic_degradation"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.QueryMode != string(retrieval.QueryModeDetail) {
		t.Fatalf("query mode = %q", decoded.QueryMode)
	}
	if len(decoded.Warnings) != 1 || decoded.Warnings[0] != retrieval.WarningSemanticTimeout {
		t.Fatalf("warnings = %+v", decoded.Warnings)
	}
	if len(decoded.Observations) != 1 || decoded.Observations[0].Observation.ID != "observation-1" ||
		decoded.Observations[0].Observation.Status != "observed" ||
		len(decoded.Observations[0].EvidenceRefs) != 1 {
		t.Fatalf("observations = %+v", decoded.Observations)
	}
	if len(decoded.Memories) != 1 || decoded.Memories[0].Explanation.Ranking != "rrf" {
		t.Fatalf("memories = %+v", decoded.Memories)
	}
	explanation := decoded.Memories[0].Explanation
	if len(explanation.FieldWeights) != 0 || explanation.Keyword.Rank != 1 || explanation.Semantic.Rank != 2 {
		t.Fatalf("explanation = %+v", explanation)
	}
	if len(decoded.L1) != 1 || decoded.L1[0].Artifact.ID != "artifact-1" ||
		decoded.L1[0].Artifact.Kind != "local_summary" || decoded.L1[0].Artifact.State != "active" ||
		len(decoded.L1[0].Dependencies) != 1 || decoded.L1[0].Dependencies[0].Type != "observation" {
		t.Fatalf("l1 = %+v", decoded.L1)
	}
	if len(decoded.L4) != 1 || decoded.L4[0].Artifact.Kind != "skill" {
		t.Fatalf("l4 = %+v", decoded.L4)
	}
	if len(decoded.Relations) != 1 || decoded.Relations[0].Kind != "supports" {
		t.Fatalf("relations = %+v", decoded.Relations)
	}
	if decoded.Conflicts == nil || decoded.Warnings == nil {
		t.Fatalf("empty partitions must be arrays: %+v", decoded)
	}
	if decoded.Diagnostics == nil || decoded.Diagnostics.KeywordCandidates != 3 ||
		decoded.Diagnostics.ObservationCandidates != 1 || decoded.Diagnostics.DerivedCandidates != 2 ||
		decoded.Diagnostics.KindFilterExcluded != 1 || decoded.Diagnostics.ContextBudgetTruncated != 1 ||
		decoded.Diagnostics.SemanticDegradation != retrieval.WarningSemanticTimeout {
		t.Fatalf("diagnostics = %+v", decoded.Diagnostics)
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
