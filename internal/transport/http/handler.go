package httptransport

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// Services is the complete business capability set used by the local HTTP API.
// A nil service is treated as unavailable rather than silently succeeding.
type Services struct {
	Ingest   ingest.IngestService
	Memories memory.MemoryService
	Sources  source.SourceService
	Search   retrieval.SearchService
}

func (s Services) configured() bool {
	return s.Ingest != nil || s.Memories != nil || s.Sources != nil || s.Search != nil
}

// Options configures the HTTP transport. Ready checks database readiness before
// every /v1 request; model availability is intentionally not part of it.
type Options struct {
	Services Services
	Ready    func(context.Context) error
	Logger   *slog.Logger
}

type handler struct {
	services Services
	ready    func(context.Context) error
	logger   *slog.Logger
}

// New builds the complete HTTP surface. It installs no authentication,
// application identity, field validation or request-size middleware.
func New(options Options) http.Handler {
	h := &handler{
		services: options.Services,
		ready:    options.Ready,
		logger:   loggerOrDiscard(options.Logger),
	}

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", h.healthz)
	root.HandleFunc("GET /readyz", h.readyz)
	if options.Services.configured() {
		root.Handle("/v1/", h.withReadiness(h.businessRoutes()))
	}
	root.HandleFunc("/", h.notFound)
	return root
}

func (h *handler) businessRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingestions", h.createIngestion)
	mux.HandleFunc("GET /v1/memories", h.listMemories)
	mux.HandleFunc("POST /v1/memories/recall", h.recallMemories)
	mux.HandleFunc("GET /v1/memories/{id}", h.getMemory)
	mux.HandleFunc("PATCH /v1/memories/{id}/state", h.setMemoryState)
	mux.HandleFunc("DELETE /v1/memories/{id}", h.deleteMemory)
	mux.HandleFunc("POST /v1/memories/{id}/purge", h.purgeMemory)
	mux.HandleFunc("GET /v1/memories/{id}/history", h.listMemoryHistory)
	mux.HandleFunc("GET /v1/memories/{id}/history/{version}", h.getMemoryHistoryEntry)
	mux.HandleFunc("POST /v1/memories/{id}/history/{version}/restore", h.restoreMemoryVersion)
	mux.HandleFunc("GET /v1/memories/{id}/evidence", h.getMemoryEvidence)
	mux.HandleFunc("GET /v1/sources/{id}", h.getSource)
	mux.HandleFunc("DELETE /v1/sources/{id}", h.redactSource)
	mux.HandleFunc("GET /v1/ingest-jobs/{id}", h.getJob)
	mux.HandleFunc("/", h.notFound)
	return mux
}

func (h *handler) withReadiness(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if h.ready != nil {
			if err := h.ready(request.Context()); err != nil {
				h.logger.Warn("service not ready", "path", request.URL.Path, "error", err)
				h.writeUnavailable(w)
				return
			}
		}
		next.ServeHTTP(w, request)
	})
}

func (h *handler) notFound(w http.ResponseWriter, _ *http.Request) {
	h.writeNotFound(w)
}

func (h *handler) requireIngest(w http.ResponseWriter) (ingest.IngestService, bool) {
	if h.services.Ingest == nil {
		h.writeUnavailable(w)
		return nil, false
	}
	return h.services.Ingest, true
}

func (h *handler) requireMemories(w http.ResponseWriter) (memory.MemoryService, bool) {
	if h.services.Memories == nil {
		h.writeUnavailable(w)
		return nil, false
	}
	return h.services.Memories, true
}

func (h *handler) requireSources(w http.ResponseWriter) (source.SourceService, bool) {
	if h.services.Sources == nil {
		h.writeUnavailable(w)
		return nil, false
	}
	return h.services.Sources, true
}

func (h *handler) requireSearch(w http.ResponseWriter) (retrieval.SearchService, bool) {
	if h.services.Search == nil {
		h.writeUnavailable(w)
		return nil, false
	}
	return h.services.Search, true
}
