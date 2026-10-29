package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

type stubObservations struct {
	observation.ServiceAPI
	list     func(context.Context, observation.ListOptions) ([]observation.Observation, error)
	get      func(context.Context, string) (observation.Observation, error)
	evidence func(context.Context, string) ([]source.Evidence, error)
}

func (s stubObservations) List(ctx context.Context, options observation.ListOptions) ([]observation.Observation, error) {
	return s.list(ctx, options)
}

func (s stubObservations) Get(ctx context.Context, id string) (observation.Observation, error) {
	return s.get(ctx, id)
}

func (s stubObservations) Evidence(ctx context.Context, id string) ([]source.Evidence, error) {
	return s.evidence(ctx, id)
}

type stubRelations struct {
	governance.RelationService
	list func(context.Context, governance.EndpointType, string) ([]governance.Relation, error)
}

func (s stubRelations) ListForEndpoint(ctx context.Context, endpointType governance.EndpointType, id string) ([]governance.Relation, error) {
	return s.list(ctx, endpointType, id)
}

func TestObservationRoutesExposeOnlyL0Data(t *testing.T) {
	created := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	item := observation.Observation{
		ID: "observation-1", SourceID: "source-1", ScopeKey: "project:dmemd",
		Kind: observation.KindFact, Content: "项目使用 SQLite。",
		ContentMode: observation.ContentExtractive, OriginRole: "user",
		Status: observation.StatusObserved, ExtractionVersion: "extraction-v6",
		CreatedAt: created, UpdatedAt: created,
	}
	services := Services{
		Observations: stubObservations{
			list: func(_ context.Context, options observation.ListOptions) ([]observation.Observation, error) {
				if options.SourceID != "source-1" || options.Status != observation.StatusObserved || options.Limit != 10 {
					t.Fatalf("options = %+v", options)
				}
				return []observation.Observation{item}, nil
			},
			get: func(_ context.Context, id string) (observation.Observation, error) {
				if id != item.ID {
					t.Fatalf("id = %q", id)
				}
				return item, nil
			},
			evidence: func(context.Context, string) ([]source.Evidence, error) {
				return []source.Evidence{{
					ID: "evidence-1", SourceID: "source-1", SegmentID: "seg-0001",
					Quote: "项目使用 SQLite。", Redacted: false, CreatedAt: created,
				}}, nil
			},
		},
		Relations: stubRelations{
			list: func(_ context.Context, endpointType governance.EndpointType, id string) ([]governance.Relation, error) {
				if endpointType != governance.EndpointObservation || id != item.ID {
					t.Fatalf("endpoint = %s/%s", endpointType, id)
				}
				return []governance.Relation{{
					ID: "relation-1", Kind: governance.RelationSupports,
					FromType: governance.EndpointObservation, FromID: item.ID,
					ToType: governance.EndpointMemory, ToID: "memory-1", ToVersion: 2,
					CreatedAt: created,
				}}, nil
			},
		},
	}

	response := serve(t, services, nil, httptest.NewRequest(http.MethodGet,
		"/v1/observations?source_id=source-1&status=observed&limit=10", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"observation-1"`) {
		t.Fatalf("list = %d %s", response.Code, response.Body.String())
	}
	var list observationListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list body = %s err=%v", response.Body.String(), err)
	}
	if list.Items[0].Content != item.Content || list.Items[0].SourceID != item.SourceID {
		t.Fatalf("list item = %+v", list.Items[0])
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/observations/observation-1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content_mode":"extractive"`) {
		t.Fatalf("get = %d %s", response.Code, response.Body.String())
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/observations/observation-1/evidence", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"segment_id":"seg-0001"`) {
		t.Fatalf("evidence = %d %s", response.Code, response.Body.String())
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/observations/observation-1/relations", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"from_type":"observation"`) ||
		!strings.Contains(response.Body.String(), `"kind":"supports"`) {
		t.Fatalf("relations = %d %s", response.Code, response.Body.String())
	}
}

func TestObservationRoutesValidateFiltersAndMissingObject(t *testing.T) {
	called := false
	services := Services{
		Observations: stubObservations{
			list: func(context.Context, observation.ListOptions) ([]observation.Observation, error) {
				called = true
				return nil, nil
			},
			get: func(context.Context, string) (observation.Observation, error) {
				return observation.Observation{}, observation.ErrNotFound
			},
		},
		Relations: stubRelations{
			list: func(context.Context, governance.EndpointType, string) ([]governance.Relation, error) {
				t.Fatal("relations called for missing observation")
				return nil, nil
			},
		},
	}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/observations?kind=unknown", nil))
	if response.Code != http.StatusBadRequest || called {
		t.Fatalf("invalid kind = %d called=%v", response.Code, called)
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/observations/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", response.Code)
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/observations/missing/relations", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing relations = %d", response.Code)
	}
}

func TestObservationUnavailableWhenServiceMissing(t *testing.T) {
	response := serve(t, Services{}, nil, httptest.NewRequest(http.MethodGet, "/v1/observations", nil))
	if response.Code != http.StatusNotFound {
		// No services at all means the business router is not mounted.
		t.Fatalf("status = %d", response.Code)
	}
}
