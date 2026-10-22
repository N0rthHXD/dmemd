package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/model"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

// NewMemoryService wires the internal memory write/read service with the
// production search-document builder. Ingest and HTTP assembly use this helper
// instead of injecting the deterministic test builder.
func NewMemoryService(store *sqlite.Store, options ...memory.Option) *memory.Service {
	return memory.NewService(store, sqlite.MemoryStore{Store: store}, retrieval.NewDocumentBuilder(), options...)
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

// NewArtifactService wires derived reads, invalidation and the derived FTS index.
func NewArtifactService(store *sqlite.Store, debounce time.Duration, logger *slog.Logger) *artifact.Service {
	return artifact.NewService(
		store,
		retrieval.NewArtifactDocumentBuilder(),
		artifact.WithDebounce(debounce),
		artifact.WithLogger(logger),
	)
}

// NewRecallService wires the unified recall service over atomic and derived
// layers. A nil encoder keeps atomic retrieval keyword-only.
func NewRecallService(store *sqlite.Store, artifacts *artifact.Service,
	encoder retrieval.QueryEncoder, logger *slog.Logger) *retrieval.Service {
	return retrieval.NewService(
		store,
		retrieval.NewDocumentBuilder(),
		retrieval.WithLogger(logger),
		retrieval.WithSemantic(encoder),
		retrieval.WithArtifacts(artifacts),
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

// NewExperienceService wires application result feedback and retraction.
func NewExperienceService(store *sqlite.Store) *governance.ExperienceServiceAPI {
	return governance.NewExperienceService(sqlite.GovernanceStore{Store: store})
}

// NewForgetService wires explicit permanent deletion over the same short
// governance transaction used by derived invalidation.
func NewForgetService(store *sqlite.Store, memories *memory.Service) *governance.ForgetServiceAPI {
	return governance.NewForgetService(sqlite.GovernanceStore{Store: store}, memories)
}

// NewSkillService wires rendered SKILL.md reads over immutable artifact
// revisions.
func NewSkillService(artifacts *artifact.Service) *skill.Service {
	return skill.NewService(artifacts)
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
		return nil, errors.New("首版连续协议失败上限为三次")
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
	App              *App
	Worker           *ingest.Worker
	GovernanceWorker *governance.Worker
	EmbeddingWorker  *embedding.Worker
	Services         Services
}

type topicSuggester interface {
	SuggestTopics(ctx context.Context, title, content, scopeKey string) ([]string, error)
}

// NewGovernanceWorker wires classification, summary/profile aggregation and
// periodic reconciliation. An unconfigured model parks model-dependent work
// until a restart; deterministic labels still commit normally, and reconcile
// never loops on the same input.
func NewGovernanceWorker(store *sqlite.Store, memories *memory.Service, artifacts *artifact.Service,
	provider ingest.ExtractionProvider, cfg GovernanceConfig, modelTimeout time.Duration, logger *slog.Logger) *governance.Worker {
	var suggest governance.TopicSuggester
	if typed, ok := provider.(topicSuggester); ok {
		suggest = func(ctx context.Context, title, content, scopeKey string) ([]string, error) {
			return typed.SuggestTopics(ctx, title, content, scopeKey)
		}
	} else {
		// An unconfigured model must not silently mark missing labels as final.
		// The task waits for configuration and is re-evaluated after a restart.
		suggest = func(context.Context, string, string, string) ([]string, error) {
			return nil, &ingest.ProviderError{
				Operation: "configuration",
				Retryable: true,
				Err:       errors.New("分类模型未配置"),
			}
		}
	}
	limiter := governance.NewLimiter(defaultGovernanceMaxCallsPerHour, cfg.MinCallInterval.Std())
	classify := governance.NewClassifyHandler(
		store,
		sqlite.GovernanceStore{Store: store},
		memories,
		suggest,
		limiter,
		governance.WithClassifyTimeout(modelTimeout),
	)
	reviewConflict := governance.NewConflictHandler(
		sqlite.GovernanceStore{Store: store},
		memories,
	)
	reconcile := governance.NewReconcileHandler(
		sqlite.GovernanceStore{Store: store},
		cfg.Debounce.Std(),
		100,
		governance.WithArtifactReconcile(artifacts),
	)
	aggregateProvider, ok := any(provider).(governance.AggregateProvider)
	if !ok {
		aggregateProvider = unavailableAggregateProvider{}
	}
	experienceProvider, ok := any(provider).(governance.ExperienceProvider)
	if !ok {
		experienceProvider = unavailableExperienceProvider{}
	}
	aggregate := governance.NewAggregateHandler(
		sqlite.GovernanceStore{Store: store},
		artifacts,
		aggregateProvider,
		limiter,
		governance.WithAggregateTimeout(modelTimeout),
		governance.WithAggregateMaxInputs(cfg.MaxInputs),
		governance.WithAggregateGenerator("governance:aggregate"),
		governance.WithExperienceProvider(experienceProvider),
	)
	skillProvider, ok := any(provider).(governance.SkillProvider)
	if !ok {
		skillProvider = unavailableSkillProvider{}
	}
	buildSkill := governance.NewSkillHandler(
		sqlite.GovernanceStore{Store: store},
		artifacts,
		skillProvider,
		limiter,
		governance.WithSkillTimeout(modelTimeout),
		governance.WithSkillMaxInputs(cfg.MaxInputs),
	)
	handlers := map[task.Kind]governance.Handler{
		task.KindClassify:       classify,
		task.KindReviewConflict: reviewConflict,
		task.KindAggregate:      aggregate,
		task.KindBuildSkill:     buildSkill,
		task.KindReconcile:      reconcile,
	}
	return governance.NewWorker(
		sqlite.GovernanceStore{Store: store},
		handlers,
		governance.WithReconcileInterval(cfg.ReconcileInterval.Std()),
		governance.WithWorkerLogger(logger),
	)
}

// NewStack assembles configuration, business services, the remote extraction
// provider, the background workers and the full HTTP API. A missing model base
// URL is a valid degraded state: submissions are accepted and parked for
// configuration while reads, search and management keep working.
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

	artifacts := NewArtifactService(store, cfg.Governance.Debounce.Std(), logger)
	memories := NewMemoryService(store,
		memory.WithGovernanceDebounce(cfg.Governance.Debounce.Std()),
		memory.WithArtifactSync(artifacts),
	)
	search := NewRecallService(store, artifacts, encoder, logger)
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
	var governanceWorker *governance.Worker
	if cfg.Governance.IsEnabled() {
		governanceWorker = NewGovernanceWorker(store, memories, artifacts, provider,
			cfg.Governance, cfg.Model.Timeout.Std(), logger)
	} else {
		logger.Info("governance worker disabled by configuration")
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
		Ingest:      ingestService,
		Memories:    memories,
		Conflicts:   memories,
		Sources:     sourceService,
		Search:      search,
		Artifacts:   artifacts,
		Experiences: NewExperienceService(store),
		Skills:      NewSkillService(artifacts),
		Forget:      NewForgetService(store, memories),
	}
	app, err := New(cfg, logger, store, WithServices(services), WithWorker(worker),
		WithGovernanceWorker(governanceWorker), WithEmbeddingWorker(embeddingWorker))
	if err != nil {
		return nil, err
	}
	return &Stack{App: app, Worker: worker, GovernanceWorker: governanceWorker,
		EmbeddingWorker: embeddingWorker, Services: services}, nil
}

type unavailableProvider struct{}

func (unavailableProvider) Extract(context.Context, ingest.ExtractionRequest) (ingest.ExtractionResult, error) {
	return ingest.ExtractionResult{}, &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New("模型服务未配置"),
	}
}

func (unavailableProvider) Decide(context.Context, ingest.DecisionRequest) (ingest.DecisionResult, error) {
	return ingest.DecisionResult{}, &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New("决策模型未配置"),
	}
}

func (unavailableProvider) Summarize(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
	return artifact.AggregateResult{}, &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New("聚合模型未配置"),
	}
}

func (unavailableProvider) ComposeExperience(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
	return artifact.ExperienceResult{}, &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New("经验案例模型未配置"),
	}
}

func (unavailableProvider) ComposeSkill(context.Context, skill.Request) (skill.Draft, error) {
	return skill.Draft{}, &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New("Skill 模型未配置"),
	}
}

type unavailableExperienceProvider struct{}

func (unavailableExperienceProvider) ComposeExperience(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
	return artifact.ExperienceResult{}, &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New("经验案例模型未配置"),
	}
}

type unavailableSkillProvider struct{}

func (unavailableSkillProvider) ComposeSkill(context.Context, skill.Request) (skill.Draft, error) {
	return skill.Draft{}, &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New("Skill 模型未配置"),
	}
}

type unavailableAggregateProvider struct{}

func (unavailableAggregateProvider) Summarize(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
	return artifact.AggregateResult{}, &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New("聚合模型未配置"),
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
