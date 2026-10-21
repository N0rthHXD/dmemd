package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/model"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

// NewMemoryService wires the internal memory write/read service with the
// production search-document builder. Ingest and HTTP assembly use this helper
// instead of injecting the deterministic test builder.
func NewMemoryService(store *sqlite.Store) *memory.Service {
	return memory.NewService(store, sqlite.MemoryStore{Store: store}, retrieval.NewDocumentBuilder())
}

// NewSearchService wires keyword search and index maintenance with the same
// production normalization rules used by NewMemoryService.
func NewSearchService(store *sqlite.Store, logger *slog.Logger) *retrieval.Service {
	return retrieval.NewService(store, retrieval.NewDocumentBuilder(), retrieval.WithLogger(logger))
}

// NewSemanticSearchService wires keyword and hybrid search. A nil encoder keeps
// the service keyword-only.
func NewSemanticSearchService(store *sqlite.Store, encoder retrieval.QueryEncoder, logger *slog.Logger) *retrieval.Service {
	return retrieval.NewService(
		store,
		retrieval.NewDocumentBuilder(),
		retrieval.WithLogger(logger),
		retrieval.WithSemantic(encoder),
	)
}

// NewEmbeddingEncoder assembles the separate Embedding provider and validates
// its credential-free profile. It performs no network request.
func NewEmbeddingEncoder(cfg EmbeddingConfig) (*embedding.Encoder, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	provider, err := model.NewEmbeddingProvider(model.EmbeddingConfig{
		Name:       cfg.Model,
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Timeout:    cfg.Timeout.Std(),
		Dimensions: cfg.Dimensions,
	})
	if err != nil {
		return nil, err
	}
	encoder, err := embedding.NewEncoder(provider, cfg.toEmbedding())
	if err != nil {
		return nil, err
	}
	return encoder, nil
}

// NewEmbeddingWorker constructs the single background embedding worker.
func NewEmbeddingWorker(store *sqlite.Store, encoder *embedding.Encoder, logger *slog.Logger) *embedding.Worker {
	return embedding.NewWorker(
		store,
		encoder,
		embedding.WithEmbeddingLogger(logger),
	)
}

// NewIngestService wires unified submission and task queries over the durable
// source/job transaction.
func NewIngestService(store *sqlite.Store) *ingest.Service {
	return ingest.NewService(sqlite.IngestStore{Store: store}, store)
}

// NewSourceService wires source lookup and explicit redaction.
func NewSourceService(store *sqlite.Store) *source.Service {
	return source.NewService(store, sqlite.SourceStore{Store: store})
}

// NewModelProvider maps the local model configuration to the first remote
// protocol adapter. It performs no network request.
func NewModelProvider(cfg ModelConfig) (*model.Provider, error) {
	return model.NewProvider(model.Config{
		Name:    cfg.Name,
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
		Timeout: cfg.Timeout.Std(),
	})
}

// NewIngestProcessor wires source loading, related active-memory selection,
// remote extraction and the unified memory write unit.
func NewIngestProcessor(store *sqlite.Store, memories *memory.Service, related retrieval.RelatedService, provider ingest.ExtractionProvider, modelName string, logger *slog.Logger) *ingest.Processor {
	return ingest.NewProcessor(
		sqlite.IngestStore{Store: store},
		store,
		related,
		memories,
		provider,
		ingest.WithModelName(modelName),
		ingest.WithProcessorLogger(logger),
	)
}

// NewIngestWorker constructs the one sequential worker required by the first
// release. Configuration with a different worker count is rejected rather than
// silently starting multiple executors.
func NewIngestWorker(store *sqlite.Store, processor *ingest.Processor, cfg WorkerConfig, logger *slog.Logger) (*ingest.Worker, error) {
	if cfg.Workers != DefaultWorkerCount {
		return nil, errors.New("首版只允许一个 Worker")
	}
	if cfg.MaxAttempts != DefaultMaxAttempts {
		return nil, errors.New("首版任务最多尝试三次")
	}
	retryDelays := make([]time.Duration, 0, len(cfg.RetryDelays))
	for _, delay := range cfg.RetryDelays {
		retryDelays = append(retryDelays, delay.Std())
	}
	return ingest.NewWorker(
		sqlite.IngestStore{Store: store},
		store,
		processor,
		ingest.WithMaxAttempts(cfg.MaxAttempts),
		ingest.WithRetryDelays(retryDelays...),
		ingest.WithWorkerLogger(logger),
	), nil
}

// Stack is the complete process assembly. It contains the HTTP application, the
// background workers and the business services exposed by local HTTP.
type Stack struct {
	App             *App
	Worker          *ingest.Worker
	EmbeddingWorker *embedding.Worker
	Services        Services
}

// NewStack assembles configuration, business services, the remote extraction
// provider, one worker and the full HTTP API. A missing model base URL is a
// valid degraded state: submissions are accepted and jobs fail after the normal
// retry budget, while reads, search and management keep working.
func NewStack(cfg Config, logger *slog.Logger, store *sqlite.Store) (*Stack, error) {
	cfg.applyDefaults()
	if store == nil {
		return nil, errors.New("数据库未初始化")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	encoder, err := NewEmbeddingEncoder(cfg.Embedding)
	if err != nil {
		return nil, err
	}
	if encoder != nil {
		store.ConfigureEmbedding(true, encoder.ProfileID())
	} else {
		store.ConfigureEmbedding(false, "")
	}

	memories := NewMemoryService(store)
	search := NewSemanticSearchService(store, encoder, logger)
	ingestService := NewIngestService(store)
	sourceService := NewSourceService(store)

	var provider ingest.ExtractionProvider
	if strings.TrimSpace(cfg.Model.BaseURL) == "" {
		logger.Warn("model provider not configured; submissions will be queued but extraction will fail")
		provider = unavailableProvider{}
	} else {
		remote, err := NewModelProvider(cfg.Model)
		if err != nil {
			return nil, err
		}
		provider = remote
	}

	processor := NewIngestProcessor(store, memories, search, provider, cfg.Model.Name, logger)
	worker, err := NewIngestWorker(store, processor, cfg.Worker, logger)
	if err != nil {
		return nil, err
	}

	var embeddingWorker *embedding.Worker
	if encoder != nil {
		// A migrated database or a changed document contract requires one full
		// local rebuild before vector generation. Otherwise the worker's
		// startup scan only enqueues missing or profile-mismatched vectors.
		needsRebuild, err := needsIndexRebuild(context.Background(), store)
		if err != nil {
			return nil, err
		}
		if needsRebuild {
			if _, err := search.RebuildIndex(context.Background()); err != nil {
				return nil, fmt.Errorf("启用语义检索时重建索引: %w", err)
			}
			if err := store.SetIndexVersion(context.Background(), embedding.IndexVersion); err != nil {
				return nil, err
			}
		}
		embeddingWorker = NewEmbeddingWorker(store, encoder, logger)
	}

	services := Services{
		Ingest:   ingestService,
		Memories: memories,
		Sources:  sourceService,
		Search:   search,
	}
	app, err := New(cfg, logger, store, WithServices(services), WithWorker(worker), WithEmbeddingWorker(embeddingWorker))
	if err != nil {
		return nil, err
	}
	return &Stack{App: app, Worker: worker, EmbeddingWorker: embeddingWorker, Services: services}, nil
}

type unavailableProvider struct{}

func (unavailableProvider) Extract(context.Context, ingest.ExtractionRequest) (ingest.ExtractionResult, error) {
	return ingest.ExtractionResult{}, &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New("模型服务未配置"),
	}
}

func needsIndexRebuild(ctx context.Context, store *sqlite.Store) (bool, error) {
	version, err := store.IndexVersion(ctx)
	if err != nil {
		return false, err
	}
	if version != embedding.IndexVersion {
		return true, nil
	}
	return store.HasUnhashedSearchDocuments(ctx)
}
