package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

type stubIngest struct {
	ingest.IngestService
	submit func(context.Context, ingest.Submission) (ingest.Receipt, error)
}

func (s stubIngest) Submit(ctx context.Context, submission ingest.Submission) (ingest.Receipt, error) {
	return s.submit(ctx, submission)
}

type stubMemory struct {
	memory.MemoryService
	get     func(context.Context, string) (memory.Memory, error)
	list    func(context.Context, memory.ListOptions) ([]memory.Memory, error)
	update  func(context.Context, string, memory.State) (memory.Memory, error)
	restore func(context.Context, memory.RestoreInput) (memory.Memory, error)
}

func (s stubMemory) Get(ctx context.Context, id string) (memory.Memory, error) {
	return s.get(ctx, id)
}

func (s stubMemory) List(ctx context.Context, options memory.ListOptions) ([]memory.Memory, error) {
	return s.list(ctx, options)
}

func (s stubMemory) UpdateState(ctx context.Context, id string, state memory.State) (memory.Memory, error) {
	return s.update(ctx, id, state)
}

func (s stubMemory) Restore(ctx context.Context, input memory.RestoreInput) (memory.Memory, error) {
	return s.restore(ctx, input)
}

type stubSearch struct {
	retrieval.RecallService
	recall func(context.Context, retrieval.RecallRequest) (retrieval.RecallResult, error)
}

func (s stubSearch) Recall(ctx context.Context, request retrieval.RecallRequest) (retrieval.RecallResult, error) {
	return s.recall(ctx, request)
}

func serve(t *testing.T, services Services, ready func(context.Context) error, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	handler := New(Options{Services: services, Ready: ready})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestHealthAndReadiness(t *testing.T) {
	response := serve(t, Services{}, nil, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("healthz = %d %q", response.Code, response.Body.String())
	}
	response = serve(t, Services{}, func(context.Context) error { return errors.New("db down") }, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "unavailable") {
		t.Fatalf("readyz = %d %q", response.Code, response.Body.String())
	}
	response = serve(t, Services{}, nil, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ready") {
		t.Fatalf("readyz = %d %q", response.Code, response.Body.String())
	}
}

func TestUnknownRouteReturnsSimpleJSON404(t *testing.T) {
	response := serve(t, Services{}, nil, httptest.NewRequest(http.MethodGet, "/v1/memories", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error != notFoundMessage {
		t.Fatalf("body = %q err=%v", response.Body.String(), err)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestMalformedJSONReturns400WithoutCallingService(t *testing.T) {
	called := false
	services := Services{Ingest: stubIngest{submit: func(context.Context, ingest.Submission) (ingest.Receipt, error) {
		called = true
		return ingest.Receipt{}, nil
	}}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/ingestions", strings.NewReader("{")))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	if called {
		t.Fatal("service called for malformed JSON")
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/ingestions", strings.NewReader("{} {}")))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status = %d, want 400", response.Code)
	}
}

func TestServiceErrorMapping(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	t.Run("not found", func(t *testing.T) {
		services := Services{Memories: stubMemory{get: func(context.Context, string) (memory.Memory, error) {
			return memory.Memory{}, memory.ErrNotFound
		}}}
		response := serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/memories/"+id, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", response.Code)
		}
	})
	t.Run("internal", func(t *testing.T) {
		services := Services{Memories: stubMemory{get: func(context.Context, string) (memory.Memory, error) {
			return memory.Memory{}, errors.New("boom")
		}}}
		response := serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/memories/"+id, nil))
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", response.Code)
		}
		if !strings.Contains(response.Body.String(), internalMessage) {
			t.Fatalf("body = %q", response.Body.String())
		}
	})
}

func TestReadinessBlocksBusinessRoutesWith503(t *testing.T) {
	services := Services{Memories: stubMemory{get: func(context.Context, string) (memory.Memory, error) {
		t.Fatal("service called while not ready")
		return memory.Memory{}, nil
	}}}
	response := serve(t, services, func(context.Context) error { return errors.New("db down") },
		httptest.NewRequest(http.MethodGet, "/v1/memories/11111111-1111-4111-8111-111111111111", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestListOptionsAndInvalidFilter(t *testing.T) {
	var captured memory.ListOptions
	calls := 0
	services := Services{Memories: stubMemory{list: func(_ context.Context, options memory.ListOptions) ([]memory.Memory, error) {
		calls++
		captured = options
		return []memory.Memory{}, nil
	}}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodGet,
		"/v1/memories?kinds=fact,preference&states=active,archived&created_by_app=desktop&updated_after=2026-01-01T00:00:00Z&limit=10", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if calls != 1 || len(captured.Kinds) != 2 || len(captured.States) != 2 || captured.Limit != 10 || captured.CreatedByApp != "desktop" {
		t.Fatalf("options = %+v calls=%d", captured, calls)
	}
	if captured.UpdatedAfter == nil || !captured.UpdatedAfter.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("updated_after = %v", captured.UpdatedAfter)
	}

	calls = 0
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/memories?kinds=unknown", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("invalid kinds response = %d %q", response.Code, response.Body.String())
	}
	if calls != 0 {
		t.Fatalf("service called for empty filter set")
	}
}

func TestSetStateIgnoresContentAndRecallParsing(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	var state memory.State
	services := Services{
		Memories: stubMemory{update: func(_ context.Context, gotID string, gotState memory.State) (memory.Memory, error) {
			if gotID != id {
				t.Fatalf("id = %q", gotID)
			}
			state = gotState
			return memory.Memory{ID: id, State: gotState, Version: 2}, nil
		}},
		Search: stubSearch{recall: func(_ context.Context, request retrieval.RecallRequest) (retrieval.RecallResult, error) {
			if request.Query != "回答 语言" || request.TopK != 5 || !request.Explain {
				t.Fatalf("search request = %+v", request)
			}
			if len(request.Filters.Kinds) != 1 || request.Filters.Kinds[0] != memory.KindPreference {
				t.Fatalf("filters = %+v", request.Filters)
			}
			return retrieval.RecallResult{Memories: []retrieval.Item{}}, nil
		}},
	}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPatch, "/v1/memories/"+id+"/state",
		strings.NewReader(`{"state":"archived","content":"must not be written"}`)))
	if response.Code != http.StatusOK || state != memory.StateArchived {
		t.Fatalf("patch = %d state=%q body=%s", response.Code, state, response.Body.String())
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/recall",
		strings.NewReader(`{"query":"回答 语言","filters":{"kinds":["preference"]},"top_k":5,"explain":true}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"memories":[]`) {
		t.Fatalf("recall = %d %q", response.Code, response.Body.String())
	}
}

func TestRestoreRejectsInvalidVersionWithoutCallingService(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	services := Services{Memories: stubMemory{restore: func(context.Context, memory.RestoreInput) (memory.Memory, error) {
		t.Fatal("restore called with invalid version")
		return memory.Memory{}, nil
	}}}
	for _, version := range []string{"0", "-1", "two"} {
		response := serve(t, services, nil, httptest.NewRequest(http.MethodPost,
			"/v1/memories/"+id+"/history/"+version+"/restore", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("version %s status = %d, want 404", version, response.Code)
		}
	}
}
