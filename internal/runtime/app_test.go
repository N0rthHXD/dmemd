package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.DataDir = dir
	cfg.Database.Path = filepath.Join(dir, "dmemd.db")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	return cfg
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthAndReadinessEndpoints(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		app, err := New(testConfig(t), discardLogger(), ReadinessFunc(func(context.Context) error { return nil }))
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("healthz status = %d", response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}

		response = httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("readyz status = %d", response.Code)
		}
	})

	t.Run("database unavailable", func(t *testing.T) {
		app, err := New(testConfig(t), discardLogger(), ReadinessFunc(func(context.Context) error {
			return errors.New("database down")
		}))
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("readyz status = %d, want 503", response.Code)
		}

		response = httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("healthz must remain 200, got %d", response.Code)
		}
	})

	t.Run("no business placeholders", func(t *testing.T) {
		app, err := New(testConfig(t), discardLogger(), ReadinessFunc(func(context.Context) error { return nil }))
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/memories", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("GET /v1/memories status = %d, want 404 in P1", response.Code)
		}
	})
}

func TestServeStopsOnContextCancellation(t *testing.T) {
	app, err := New(testConfig(t), discardLogger(), ReadinessFunc(func(context.Context) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx, listener) }()

	client := &http.Client{Timeout: time.Second}
	url := "http://" + listener.Addr().String() + "/healthz"
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, requestErr := client.Get(url)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("healthz status = %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server did not start: %v", requestErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not stop after cancellation")
	}
}
