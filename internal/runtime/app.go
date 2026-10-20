package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const shutdownTimeout = 10 * time.Second

// Readiness reports whether the service can serve database-backed requests.
// A model outage must not affect readiness.
type Readiness interface {
	Ready(ctx context.Context) error
}

// ReadinessFunc adapts a function to Readiness.
type ReadinessFunc func(ctx context.Context) error

// Ready implements Readiness.
func (f ReadinessFunc) Ready(ctx context.Context) error {
	if f == nil {
		return errors.New("readiness probe not configured")
	}
	return f(ctx)
}

// App is the process runtime skeleton: configuration, HTTP lifecycle and
// health endpoints. The database store supplies the real readiness probe.
type App struct {
	cfg    Config
	logger *slog.Logger
	ready  Readiness
	server *http.Server
}

// New validates configuration and assembles the HTTP surface. Business routes
// are added in later stages; P2 wires the real database readiness probe.
func New(cfg Config, logger *slog.Logger, ready Readiness) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if ready == nil {
		ready = ReadinessFunc(func(context.Context) error {
			return errors.New("readiness probe not configured")
		})
	}

	app := &App{cfg: cfg, logger: logger, ready: ready}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", app.handleHealthz)
	mux.HandleFunc("GET /readyz", app.handleReadyz)

	app.server = &http.Server{
		Handler:           app.withRequestLogging(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return app, nil
}

// Handler returns the HTTP handler for tests and embedding.
func (a *App) Handler() http.Handler {
	return a.server.Handler
}

// Run listens on the configured address and serves until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", a.cfg.Listen, err)
	}
	return a.Serve(ctx, listener)
}

// Serve serves an already-created listener until ctx is cancelled, then shuts
// the HTTP server down within a bounded timeout.
func (a *App) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("HTTP listener 为空")
	}
	a.logger.Info("http server starting", "listen", listener.Addr().String())

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- a.server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownErr := a.server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			_ = a.server.Close()
		}
		err := <-serveErr
		if shutdownErr != nil {
			return fmt.Errorf("关闭 HTTP 服务: %w", shutdownErr)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务: %w", err)
		}
		a.logger.Info("http server stopped")
		return nil
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务: %w", err)
		}
		return nil
	}
}

type healthResponse struct {
	Status string `json:"status"`
}

func (a *App) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (a *App) handleReadyz(w http.ResponseWriter, request *http.Request) {
	if err := a.ready.Ready(request.Context()); err != nil {
		a.logger.Warn("readiness check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ready"})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(data)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (a *App) withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, request)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		a.logger.Info("http request",
			"method", request.Method,
			"path", request.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
