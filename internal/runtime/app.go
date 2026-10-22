package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	httptransport "github.com/N0rthHXD/dmemd/internal/transport/http"
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

// Services is the complete business capability set exposed by the local HTTP
// API. The runtime owns assembly; transport owns DTOs and response mapping.
type Services = httptransport.Services

// Option customizes the assembled application. Options are used by the full
// process assembly while health-only tests can keep calling New with three
// arguments.
type Option func(*App)

// WithServices installs the business services exposed under /v1.
func WithServices(services Services) Option {
	return func(app *App) {
		app.services = services
	}
}

// WithWorker installs the single sequential ingest worker. Serve recovers
// leftover jobs before accepting HTTP traffic and stops the worker on shutdown.
func WithWorker(worker *ingest.Worker) Option {
	return func(app *App) {
		app.worker = worker
	}
}

// WithEmbeddingWorker installs the independent single embedding worker. It runs
// alongside the ingest worker and shares the same shutdown signal.
func WithEmbeddingWorker(worker *embedding.Worker) Option {
	return func(app *App) {
		app.embeddingWorker = worker
	}
}

// App is the process runtime: configuration, HTTP lifecycle, health endpoints,
// business routes and the optional background workers.
type App struct {
	cfg             Config
	logger          *slog.Logger
	ready           Readiness
	services        Services
	worker          *ingest.Worker
	embeddingWorker *embedding.Worker
	server          *http.Server
}

// New validates configuration and assembles the HTTP surface. Business routes
// are installed when services are supplied through WithServices.
func New(cfg Config, logger *slog.Logger, ready Readiness, options ...Option) (*App, error) {
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
	for _, option := range options {
		if option != nil {
			option(app)
		}
	}

	handler := httptransport.New(httptransport.Options{
		Services: app.services,
		Ready:    ready.Ready,
		Logger:   logger,
	})
	app.server = &http.Server{
		Handler:           app.withRequestLogging(handler),
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

// Serve serves an already-created listener until ctx is cancelled, then stops
// accepting requests and waits for the worker to leave its current job without
// committing a partial transaction.
func (a *App) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("HTTP listener 为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var workerCtx context.Context
	var cancelWorker context.CancelFunc
	var workerWG sync.WaitGroup
	workerErrors := make(chan error, 2)
	activeWorkers := 0

	if a.worker != nil || a.embeddingWorker != nil {
		workerCtx, cancelWorker = context.WithCancel(ctx)
		defer cancelWorker()
	}

	if a.worker != nil {
		if err := a.worker.Recover(workerCtx); err != nil {
			cancelWorker()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("恢复后台任务: %w", err)
		}
	}
	if a.embeddingWorker != nil {
		if err := a.embeddingWorker.Recover(workerCtx); err != nil {
			cancelWorker()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("恢复向量任务: %w", err)
		}
	}
	if a.worker != nil {
		activeWorkers++
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			workerErrors <- a.worker.Run(workerCtx)
		}()
		a.logger.Info("ingest worker starting")
	}
	if a.embeddingWorker != nil {
		activeWorkers++
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			workerErrors <- a.embeddingWorker.Run(workerCtx)
		}()
		a.logger.Info("embedding worker starting")
	}

	a.logger.Info("http server starting", "listen", listener.Addr().String())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- a.server.Serve(listener)
	}()

	var runErr error
	serveFinished := false
	select {
	case <-ctx.Done():
		// Normal shutdown path.
	case err := <-serveErr:
		serveFinished = true
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("HTTP 服务: %w", err)
		}
	case err := <-workerErrors:
		if err == nil {
			err = errors.New("后台任务意外退出")
		}
		runErr = fmt.Errorf("后台任务: %w", err)
	}
	if cancelWorker != nil {
		cancelWorker()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := a.server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = a.server.Close()
	}
	if !serveFinished {
		err := <-serveErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) && runErr == nil {
			runErr = fmt.Errorf("HTTP 服务: %w", err)
		}
	}
	if shutdownErr != nil && runErr == nil {
		runErr = fmt.Errorf("关闭 HTTP 服务: %w", shutdownErr)
	}

	if activeWorkers > 0 {
		workersDone := make(chan struct{})
		go func() {
			workerWG.Wait()
			close(workersDone)
		}()
		select {
		case <-workersDone:
		case <-time.After(shutdownTimeout):
			if runErr == nil {
				runErr = errors.New("后台任务未在关闭期限内退出")
			}
		}
	}

	if runErr == nil {
		a.logger.Info("http server stopped")
	}
	return runErr
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
