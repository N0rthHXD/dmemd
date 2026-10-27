package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/governance"
)

type stubForget struct {
	request governance.ForgetRequest
	result  governance.ForgetResult
	err     error
}

func (s *stubForget) Forget(_ context.Context, request governance.ForgetRequest) (governance.ForgetResult, error) {
	s.request = request
	return s.result, s.err
}

func TestForgetRouteForwardsExplicitIdentifiers(t *testing.T) {
	service := &stubForget{result: governance.ForgetResult{
		MemoryIDs: []string{"memory-1"}, SourceIDs: []string{"source-1"}, ExperienceIDs: []string{"experience-1"},
		Warnings: []string{"related_memories_retained"}, RetainedRelatedMemoryIDs: []string{"memory-2"},
	}}
	body := `{"memory_ids":["memory-1"],"source_ids":["source-1"],"experience_ids":["experience-1"]}`
	response := serve(t, Services{Forget: service}, nil,
		httptest.NewRequest(http.MethodPost, "/v1/forget", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %q", response.Code, response.Body.String())
	}
	if len(service.request.MemoryIDs) != 1 || len(service.request.SourceIDs) != 1 ||
		len(service.request.ExperienceIDs) != 1 {
		t.Fatalf("request = %+v", service.request)
	}
	for _, want := range []string{`"memory_ids":["memory-1"]`, `"source_ids":["source-1"]`, `"experience_ids":["experience-1"]`,
		`"warnings":["related_memories_retained"]`, `"retained_related_memory_ids":["memory-2"]`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("response %q missing %q", response.Body.String(), want)
		}
	}
}

func TestForgetRouteMapsEmptyAndMissingTargets(t *testing.T) {
	empty := &stubForget{err: governance.ErrInvalidForget}
	response := serve(t, Services{Forget: empty}, nil,
		httptest.NewRequest(http.MethodPost, "/v1/forget", strings.NewReader(`{}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty status = %d %q", response.Code, response.Body.String())
	}

	missing := &stubForget{err: governance.ErrExperienceNotFound}
	response = serve(t, Services{Forget: missing}, nil,
		httptest.NewRequest(http.MethodPost, "/v1/forget", strings.NewReader(`{"experience_ids":["missing"]}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d %q", response.Code, response.Body.String())
	}

	unavailable := serve(t, Services{}, nil,
		httptest.NewRequest(http.MethodPost, "/v1/forget", strings.NewReader(`{}`)))
	if unavailable.Code != http.StatusNotFound {
		t.Fatalf("unconfigured status = %d", unavailable.Code)
	}
}

func TestForgetRouteRejectsMalformedJSON(t *testing.T) {
	service := &stubForget{}
	response := serve(t, Services{Forget: service}, nil,
		httptest.NewRequest(http.MethodPost, "/v1/forget", strings.NewReader(`[`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d %q", response.Code, response.Body.String())
	}
	if len(service.request.MemoryIDs)+len(service.request.SourceIDs)+len(service.request.ExperienceIDs) != 0 {
		t.Fatalf("service called for malformed body: %+v", service.request)
	}
}
