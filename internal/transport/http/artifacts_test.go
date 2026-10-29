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
)

type stubArtifacts struct {
	get     func(context.Context, string) (artifact.Detail, error)
	version func(context.Context, string, int) (artifact.Detail, error)
	list    func(context.Context, artifact.ListQuery) (artifact.ListPage, error)
	targets func(context.Context, artifact.TargetQuery) (artifact.TargetPage, error)
}

func (s stubArtifacts) Get(ctx context.Context, id string) (artifact.Detail, error) {
	return s.get(ctx, id)
}

func (s stubArtifacts) GetVersion(ctx context.Context, id string, version int) (artifact.Detail, error) {
	return s.version(ctx, id, version)
}

func (s stubArtifacts) ListVersions(context.Context, string, int) (artifact.Detail, []artifact.Revision, error) {
	return artifact.Detail{}, nil, nil
}

func (s stubArtifacts) List(ctx context.Context, query artifact.ListQuery) (artifact.ListPage, error) {
	if s.list == nil {
		return artifact.ListPage{}, nil
	}
	return s.list(ctx, query)
}

func (s stubArtifacts) ListTargets(ctx context.Context, query artifact.TargetQuery) (artifact.TargetPage, error) {
	if s.targets == nil {
		return artifact.TargetPage{}, nil
	}
	return s.targets(ctx, query)
}

func TestArtifactReadRoutesReturnCurrentAndHistoricalRevisions(t *testing.T) {
	created := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	detail := artifact.Detail{
		DerivedMemory: artifact.DerivedMemory{
			ID: "artifact-1", Kind: artifact.KindTopicSummary, TargetKey: "summary:x:y",
			ScopeKey: "project:dmemd", Topic: "go", CurrentVersion: 2,
			State: artifact.StateActive, UpdatedAt: created,
		},
		Revision: &artifact.Revision{
			ArtifactID: "artifact-1", Version: 2, Title: "摘要", Content: "内容",
			CreatedAt: created,
		},
	}
	services := Services{Derived: stubArtifacts{
		get: func(_ context.Context, id string) (artifact.Detail, error) {
			if id != "artifact-1" {
				t.Fatalf("id = %q", id)
			}
			return detail, nil
		},
		version: func(_ context.Context, id string, version int) (artifact.Detail, error) {
			if id != "artifact-1" || version != 1 {
				t.Fatalf("history = %q@%d", id, version)
			}
			history := detail
			history.Revision = &artifact.Revision{ArtifactID: id, Version: 1, Content: "旧内容"}
			return history, nil
		},
	}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/artifacts/artifact-1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("current = %d %q", response.Code, response.Body.String())
	}
	var decoded artifactDetailResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Artifact.State != "active" || decoded.Revision == nil || decoded.Revision.Version != 2 {
		t.Fatalf("current detail = %+v", decoded)
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/artifacts/artifact-1/history/1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":1`) {
		t.Fatalf("history = %d %q", response.Code, response.Body.String())
	}

	missing := Services{Derived: stubArtifacts{
		get: func(context.Context, string) (artifact.Detail, error) {
			return artifact.Detail{}, artifact.ErrNotFound
		},
		version: func(context.Context, string, int) (artifact.Detail, error) {
			return artifact.Detail{}, artifact.ErrNotFound
		},
	}}
	response = serve(t, missing, nil, httptest.NewRequest(http.MethodGet, "/v1/artifacts/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing = %d %q", response.Code, response.Body.String())
	}
	response = serve(t, missing, nil, httptest.NewRequest(http.MethodGet, "/v1/artifacts/missing/history/0", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("invalid history version = %d", response.Code)
	}
}

func TestArtifactDisabledDiagnosticWithoutRevision(t *testing.T) {
	services := Services{Derived: stubArtifacts{get: func(context.Context, string) (artifact.Detail, error) {
		return artifact.Detail{DerivedMemory: artifact.DerivedMemory{ID: "skill-1", Kind: artifact.KindSkill, State: artifact.StateDisabled, ReasonCode: "no_procedure_input"}}, nil
	}}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/artifacts/skill-1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"reason_code":"no_procedure_input"`) {
		t.Fatalf("diagnostic = %d %s", response.Code, response.Body.String())
	}
}

func TestArtifactListAndTargetRoutesExposeLifecycleState(t *testing.T) {
	updated := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	evaluated := updated.Add(time.Minute)
	services := Services{Derived: stubArtifacts{
		list: func(_ context.Context, query artifact.ListQuery) (artifact.ListPage, error) {
			if query.ScopeKey != "project:dmemd" || query.Kind != artifact.KindScopeSummary ||
				query.State != artifact.StateActive || query.Limit != 10 {
				t.Fatalf("list query = %+v", query)
			}
			return artifact.ListPage{Items: []artifact.DerivedMemory{{
				ID: "scope-1", Kind: artifact.KindScopeSummary,
				TargetKey: "scope_summary:x", ScopeKey: "project:dmemd",
				CurrentVersion: 1, State: artifact.StateActive, UpdatedAt: updated,
			}}, NextCursor: "next"}, nil
		},
		targets: func(_ context.Context, query artifact.TargetQuery) (artifact.TargetPage, error) {
			if query.ScopeKey != "project:dmemd" || query.Status != artifact.TargetStatusNoEligibleInput {
				t.Fatalf("target query = %+v", query)
			}
			return artifact.TargetPage{Items: []artifact.TargetRecord{{
				Kind: artifact.KindSkill, TargetKey: "skill:x", ScopeKey: "project:dmemd",
				Topic: "missing", Status: artifact.TargetStatusNoEligibleInput,
				ReasonCode: "no_procedure_input", EvaluatedAt: &evaluated, UpdatedAt: evaluated,
			}}}, nil
		},
	}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodGet,
		"/v1/artifacts?scope_key=project:dmemd&kind=scope_summary&state=active&limit=10", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"next_cursor":"next"`) ||
		!strings.Contains(response.Body.String(), `"kind":"scope_summary"`) {
		t.Fatalf("artifact list = %d %s", response.Code, response.Body.String())
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet,
		"/v1/artifact-targets?scope_key=project:dmemd&status=no_eligible_input", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"has_usable_version":false`) ||
		!strings.Contains(response.Body.String(), `"reason_code":"no_procedure_input"`) ||
		!strings.Contains(response.Body.String(), `"evaluated_at":"2026-10-22T08:01:00Z"`) {
		t.Fatalf("target list = %d %s", response.Code, response.Body.String())
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet,
		"/v1/artifacts?kind=unknown", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid list query = %d", response.Code)
	}
}

func TestArtifactRouteUnavailableWithoutService(t *testing.T) {
	response := serve(t, Services{}, nil, httptest.NewRequest(http.MethodGet, "/v1/artifacts/x", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}
