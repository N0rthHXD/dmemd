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
	"sync"
	"sync/atomic"
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
			t.Fatalf("GET /v1/memories status = %d, want 404 without business routes", response.Code)
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

func TestSuperviseWorkerNeverOverlapsRestart(t *testing.T) {
	app, err := New(testConfig(t), discardLogger(), ReadinessFunc(func(context.Context) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var active, maxActive, starts int32
	release := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go app.superviseWorker(ctx, &wg, "test", func(ctx context.Context) error {
		once.Do(func() { close(release) })
		current := atomic.AddInt32(&active, 1)
		for {
			observed := atomic.LoadInt32(&maxActive)
			if current <= observed || atomic.CompareAndSwapInt32(&maxActive, observed, current) {
				break
			}
		}
		// Keep the first run alive long enough to observe a replacement if one
		// were started concurrently, then exit and let the supervisor back off.
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Millisecond):
		}
		atomic.AddInt32(&active, -1)
		atomic.AddInt32(&starts, 1)
		return errors.New("scripted worker failure")
	})
	select {
	case <-release:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	// Wait for one supervised restart (initial delay is one second).
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&starts) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	if atomic.LoadInt32(&starts) < 2 {
		t.Fatalf("worker was not restarted: starts=%d", atomic.LoadInt32(&starts))
	}
	if got := atomic.LoadInt32(&maxActive); got > 1 {
		t.Fatalf("worker executions overlapped: max active = %d", got)
	}
}
