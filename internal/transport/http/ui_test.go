package httptransport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubUI struct{}

func (stubUI) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte("<html>ui</html>"))
}

func TestOptionalUIHandlerRoutes(t *testing.T) {
	handler := New(Options{UI: stubUI{}})

	request := httptest.NewRequest(http.MethodGet, "/ui", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMovedPermanently || response.Header().Get("Location") != "/ui/" {
		t.Fatalf("/ui = %d %q", response.Code, response.Header().Get("Location"))
	}

	request = httptest.NewRequest(http.MethodGet, "/ui/memories/123", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ui") {
		t.Fatalf("/ui/ = %d %q", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/ui/memories", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "资源不存在") {
		t.Fatalf("POST /ui/ = %d %q", response.Code, response.Body.String())
	}
}

func TestUIIgnoresDatabaseReadiness(t *testing.T) {
	handler := New(Options{
		UI: stubUI{},
		Ready: func(context.Context) error {
			return errors.New("database down")
		},
	})
	request := httptest.NewRequest(http.MethodGet, "/ui/overview", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("/ui/overview = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d", response.Code)
	}
}

func TestNoUIHandlerKeepsRootJSON404(t *testing.T) {
	handler := New(Options{})
	for _, target := range []string{"/", "/ui", "/ui/overview"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound ||
			!strings.Contains(response.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s = %d %q", target, response.Code, response.Header().Get("Content-Type"))
		}
	}
}
