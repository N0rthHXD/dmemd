package integration_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	appruntime "github.com/N0rthHXD/dmemd/internal/runtime"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func TestRealDatabaseDrivesReadinessOnly(t *testing.T) {
	store := testutil.NewStore(t)
	cfg := appruntime.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	cfg.Model.BaseURL = "http://127.0.0.1:1/v1"

	app, err := appruntime.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("readyz with database = %d, want 200; body=%s", response.Code, response.Body.String())
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz with closed database = %d, want 503", response.Code)
	}
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("healthz with closed database = %d, want 200", response.Code)
	}
}
