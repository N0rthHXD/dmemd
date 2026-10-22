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

func TestArtifactReadRoutesReturnCurrentAndHistoricalRevisions(t *testing.T) {
	created := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	detail := artifact.Detail{
		Artifact: artifact.Artifact{
			ID: "artifact-1", Kind: artifact.KindTopicSummary, TargetKey: "summary:x:y",
			ScopeKey: "project:dmemd", Topic: "go", CurrentVersion: 2,
			State: artifact.StateActive, UpdatedAt: created,
		},
		Revision: &artifact.Revision{
			ArtifactID: "artifact-1", Version: 2, Title: "摘要", Content: "内容",
			MemoryRefs: []artifact.MemoryRef{{MemoryID: "memory-1", Version: 3}},
			CreatedAt:  created,
		},
	}
	services := Services{Artifacts: stubArtifacts{
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
			history.Revision = &artifact.Revision{ArtifactID: id, Version: 1, Content: "旧内容", MemoryRefs: []artifact.MemoryRef{}}
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
	if decoded.Artifact.State != "active" || decoded.Revision == nil || decoded.Revision.Version != 2 ||
		len(decoded.Revision.MemoryRefs) != 1 || decoded.Revision.MemoryRefs[0].Version != 3 {
		t.Fatalf("current detail = %+v", decoded)
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/artifacts/artifact-1/history/1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":1`) {
		t.Fatalf("history = %d %q", response.Code, response.Body.String())
	}

	missing := Services{Artifacts: stubArtifacts{
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

func TestArtifactRouteUnavailableWithoutService(t *testing.T) {
	response := serve(t, Services{}, nil, httptest.NewRequest(http.MethodGet, "/v1/artifacts/x", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}
