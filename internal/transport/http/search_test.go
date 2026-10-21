package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

func TestSearchModeTopKValidationAndRequestMapping(t *testing.T) {
	var captured retrieval.Request
	calls := 0
	services := Services{Search: stubSearch{search: func(_ context.Context, request retrieval.Request) (retrieval.Result, error) {
		calls++
		captured = request
		return retrieval.Result{Items: []retrieval.Item{}, Retrieval: retrieval.Retrieval{Mode: request.Mode}}, nil
	}}}

	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/search",
		strings.NewReader(`{"query":"吃饭有什么忌口","mode":"hybrid","match":"any","top_k":10,"explain":true}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("hybrid request = %d %q", response.Code, response.Body.String())
	}
	if calls != 1 || captured.Mode != retrieval.ModeHybrid || captured.Match != retrieval.MatchAny ||
		captured.TopK != 10 || !captured.Explain {
		t.Fatalf("captured request = %+v", captured)
	}
	if !strings.Contains(response.Body.String(), `"retrieval":{"mode":"hybrid"}`) {
		t.Fatalf("response = %q", response.Body.String())
	}

	for _, body := range []string{
		`{"query":"x","mode":"semantic"}`,
		`{"query":"x","top_k":101}`,
	} {
		before := calls
		response = serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/search", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s = %d %q", body, response.Code, response.Body.String())
		}
		if calls != before {
			t.Fatalf("invalid request %s called the service", body)
		}
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/search",
		strings.NewReader(`{"query":"x"}`)))
	if response.Code != http.StatusOK || captured.Mode != retrieval.ModeKeyword {
		t.Fatalf("default mode = %d %+v", response.Code, captured)
	}
}

func TestSearchResponseIncludesWarningsAndHybridExplanation(t *testing.T) {
	services := Services{Search: stubSearch{search: func(context.Context, retrieval.Request) (retrieval.Result, error) {
		return retrieval.Result{
			Items: []retrieval.Item{{
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
			Retrieval: retrieval.Retrieval{Mode: retrieval.ModeHybrid},
			Warnings:  []string{retrieval.WarningSemanticTimeout},
		}, nil
	}}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/search",
		strings.NewReader(`{"query":"x","mode":"hybrid","explain":true}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	var decoded struct {
		Items []struct {
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
		} `json:"items"`
		Retrieval struct {
			Mode string `json:"mode"`
		} `json:"retrieval"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Retrieval.Mode != "hybrid" || len(decoded.Warnings) != 1 || decoded.Warnings[0] != retrieval.WarningSemanticTimeout {
		t.Fatalf("retrieval/warnings = %+v", decoded)
	}
	if len(decoded.Items) != 1 {
		t.Fatalf("items = %+v", decoded.Items)
	}
	explanation := decoded.Items[0].Explanation
	if explanation.Ranking != "rrf" || len(explanation.FieldWeights) != 0 {
		t.Fatalf("explanation = %+v", explanation)
	}
	if explanation.Keyword.Rank != 1 || explanation.Semantic.Rank != 2 {
		t.Fatalf("path ranks = %+v", explanation)
	}
	if decoded.Items[0].MatchedTerms == nil || len(decoded.Items[0].MatchedTerms) != 0 {
		t.Fatalf("matched_terms = %#v", decoded.Items[0].MatchedTerms)
	}
}

func TestKeywordResponseKeepsFieldWeightsAndDefaultRetrievalMode(t *testing.T) {
	services := Services{Search: stubSearch{search: func(context.Context, retrieval.Request) (retrieval.Result, error) {
		return retrieval.Result{Items: []retrieval.Item{{
			Memory:       memory.Memory{ID: "m", State: memory.StateActive, Version: 1},
			MatchedTerms: []string{"回答"},
			EvidenceRefs: []retrieval.EvidenceRef{},
			Explanation: &retrieval.Explanation{
				Ranking:      "bm25",
				FieldWeights: [2]int{retrieval.FieldWeightTitle, retrieval.FieldWeightContent},
			},
		}}}, nil
	}}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/search",
		strings.NewReader(`{"query":"回答","explain":true}`)))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"retrieval":{"mode":"keyword"}`) {
		t.Fatalf("response = %d %q", response.Code, body)
	}
	if !strings.Contains(body, `"field_weights":[4,1]`) {
		t.Fatalf("keyword explanation = %q", body)
	}
	if strings.Contains(body, `"warnings"`) {
		t.Fatalf("empty warnings were emitted: %q", body)
	}
}
