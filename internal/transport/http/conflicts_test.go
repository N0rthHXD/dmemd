package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

type stubConflicts struct {
	memory.ConflictService
	list    func(context.Context, memory.ConflictListOptions) ([]memory.Conflict, error)
	get     func(context.Context, string) (memory.Conflict, error)
	resolve func(context.Context, memory.ResolveInput) (memory.Conflict, error)
}

func (s stubConflicts) ListConflicts(ctx context.Context, options memory.ConflictListOptions) ([]memory.Conflict, error) {
	return s.list(ctx, options)
}

func (s stubConflicts) GetConflict(ctx context.Context, id string) (memory.Conflict, error) {
	return s.get(ctx, id)
}

func (s stubConflicts) Resolve(ctx context.Context, input memory.ResolveInput) (memory.Conflict, error) {
	return s.resolve(ctx, input)
}

func TestRecallAsOfIsStrictAndForwarded(t *testing.T) {
	var received retrieval.RecallRequest
	services := Services{Search: stubSearch{recall: func(_ context.Context, request retrieval.RecallRequest) (retrieval.RecallResult, error) {
		received = request
		return retrieval.RecallResult{Memories: []retrieval.Item{}, Conflicts: []memory.ConflictSummary{}}, nil
	}}}
	asOf := "2026-07-01T00:00:00Z"
	response := serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/recall",
		strings.NewReader(`{"query":"住址","filters":{"as_of":"`+asOf+`"}}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	want, _ := time.Parse(time.RFC3339Nano, asOf)
	if received.Filters.AsOf == nil || !received.Filters.AsOf.Equal(want) {
		t.Fatalf("as_of = %v", received.Filters.AsOf)
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodPost, "/v1/memories/recall",
		strings.NewReader(`{"query":"住址","filters":{"as_of":"not-a-time"}}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid as_of status = %d", response.Code)
	}
}

func TestConflictRoutesMapValidationNotFoundAndStale(t *testing.T) {
	conflictID := "conflict-1"
	conflict := memory.Conflict{
		ID: conflictID, Status: memory.ConflictOpen, ReasonCode: memory.ConflictContradictoryValue,
		Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Members: []memory.ConflictMember{{MemoryID: "m1", Version: 2}, {MemoryID: "m2", Version: 1}},
	}
	services := Services{Conflicts: stubConflicts{
		list: func(_ context.Context, options memory.ConflictListOptions) ([]memory.Conflict, error) {
			if options.Status != memory.ConflictOpen || options.Limit != 10 {
				t.Fatalf("list options = %+v", options)
			}
			return []memory.Conflict{conflict}, nil
		},
		get: func(_ context.Context, id string) (memory.Conflict, error) {
			if id != conflictID {
				t.Fatalf("get id = %s", id)
			}
			return conflict, nil
		},
		resolve: func(_ context.Context, input memory.ResolveInput) (memory.Conflict, error) {
			if input.ConflictID != conflictID || input.Action != memory.ResolutionChoose ||
				input.Revision != 1 || len(input.MemberVersions) != 2 {
				t.Fatalf("resolve input = %+v", input)
			}
			return conflict, nil
		},
	}}

	list := serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/memory-conflicts?limit=10", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", list.Code, list.Body.String())
	}
	var listBody conflictListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listBody); err != nil || len(listBody.Items) != 1 {
		t.Fatalf("list body = %s err=%v", list.Body.String(), err)
	}

	get := serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/memory-conflicts/"+conflictID, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d", get.Code)
	}

	body := `{"revision":1,"action":"choose","member_ids":["m1"],"member_versions":[{"memory_id":"m1","version":2},{"memory_id":"m2","version":1}]}`
	resolve := serve(t, services, nil, httptest.NewRequest(http.MethodPost,
		"/v1/memory-conflicts/"+conflictID+"/resolve", strings.NewReader(body)))
	if resolve.Code != http.StatusOK {
		t.Fatalf("resolve status = %d body=%s", resolve.Code, resolve.Body.String())
	}

	invalid := serve(t, services, nil, httptest.NewRequest(http.MethodPost,
		"/v1/memory-conflicts/"+conflictID+"/resolve", strings.NewReader(`{"revision":1,"action":"invent"}`)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid action status = %d", invalid.Code)
	}

	staleServices := Services{Conflicts: stubConflicts{
		resolve: func(context.Context, memory.ResolveInput) (memory.Conflict, error) {
			return memory.Conflict{}, memory.ErrConflictStale
		},
	}}
	stale := serve(t, staleServices, nil, httptest.NewRequest(http.MethodPost,
		"/v1/memory-conflicts/"+conflictID+"/resolve", strings.NewReader(body)))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale status = %d", stale.Code)
	}
	var staleBody errorResponse
	if err := json.Unmarshal(stale.Body.Bytes(), &staleBody); err != nil || staleBody.Code != codeConflictStale {
		t.Fatalf("stale body = %s", stale.Body.String())
	}

	missingServices := Services{Conflicts: stubConflicts{
		get: func(context.Context, string) (memory.Conflict, error) {
			return memory.Conflict{}, memory.ErrConflictNotFound
		},
	}}
	missing := serve(t, missingServices, nil, httptest.NewRequest(http.MethodGet, "/v1/memory-conflicts/missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
}
