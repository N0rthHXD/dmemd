package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	files := fstest.MapFS{
		"index.html": {
			Data: []byte("<!doctype html><div id=\"root\"></div>"),
		},
		"assets/index-abcdef12.js": {
			Data: []byte("console.log('ui');"),
		},
		"assets/index-abcdef12.css": {
			Data: []byte("body{}"),
		},
	}
	handler, err := newHandler(files)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	return handler
}

func serve(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestPageRoutesFallBackToIndex(t *testing.T) {
	handler := testHandler(t)
	for _, target := range []string{"/ui/", "/ui/overview", "/ui/memories/123", "/ui/not-found"} {
		response := serve(handler, http.MethodGet, target)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", target, response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s cache-control = %q", target, got)
		}
		if !strings.Contains(response.Body.String(), `id="root"`) {
			t.Fatalf("%s did not return index", target)
		}
	}
}

func TestHashedAssetsAreImmutable(t *testing.T) {
	handler := testHandler(t)
	response := serve(handler, http.MethodGet, "/ui/assets/index-abcdef12.js")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("content-type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("cache-control = %q", got)
	}
}

func TestMissingFilesNeverFallBack(t *testing.T) {
	handler := testHandler(t)
	for _, target := range []string{
		"/ui/assets/missing-abcdef12.js",
		"/ui/assets/",
		"/ui/manifest.json",
		"/ui/app.js",
	} {
		response := serve(handler, http.MethodGet, target)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", target, response.Code)
		}
	}
}

func TestOutsideUIIsNotFound(t *testing.T) {
	handler := testHandler(t)
	for _, target := range []string{"/", "/v1/memories", "/ui/../v1/memories"} {
		response := serve(handler, http.MethodGet, target)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", target, response.Code)
		}
	}
}

func TestNonReadMethodsDoNotFallBack(t *testing.T) {
	handler := testHandler(t)
	response := serve(handler, http.MethodPost, "/ui/memories")
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestHeadReturnsIndex(t *testing.T) {
	handler := testHandler(t)
	response := serve(handler, http.MethodHead, "/ui/overview")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("HEAD returned a body")
	}
}

func TestHandlerRequiresIndex(t *testing.T) {
	_, err := newHandler(fstest.MapFS{})
	if err == nil {
		t.Fatal("expected missing index error")
	}
	if _, statErr := fs.Stat(fstest.MapFS{}, "index.html"); statErr == nil {
		t.Fatal("test fixture unexpectedly contains index.html")
	}
}
