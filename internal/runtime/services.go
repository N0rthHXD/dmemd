package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/aggregation"
	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/extraction"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/jobs"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/model"
	"github.com/N0rthHXD/dmemd/internal/observation"
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

// NewObservationService wires the read-only L0 observation queries.
func NewObservationService(store *sqlite.Store) *observation.Service {
	return observation.NewService(store)
}

// NewRelationService wires the read-only typed relation graph.
func NewRelationService(store *sqlite.Store) governance.RelationService {
	return governance.NewRelationService(store)
}

// NewJobsService wires the read-only extraction and governance job views.
func NewJobsService(store *sqlite.Store) *jobs.Service {
	return jobs.NewService(store, store)
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

// NewSourceService wires source lookup and explicit redaction. Options allow
// runtime to install governance propagation without making source depend on the
// governance package.
func NewSourceService(store *sqlite.Store, options ...source.Option) *source.Service {
	return source.NewService(store, sqlite.SourceStore{Store: store}, options...)
}

// NewObservationLifecycleService wires observation retraction/deletion over the
// same short transaction used by governance.
func NewObservationLifecycleService(store *sqlite.Store, memories *memory.Service) *governance.ObservationLifecycleAPI {
	return governance.NewObservationLifecycleService(sqlite.GovernanceStore{Store: store}, memories)
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

// NewExtractionProvider assembles the L0 extraction model adapter. It performs
// no network request and never configures governance or aggregation tasks.
func NewExtractionProvider(cfg ModelConfig, worker WorkerConfig) (*model.Provider, error) {
	remote := remoteModelConfig(cfg)
	remote.ExtractionTimeout = cfg.TaskTimeout(cfg.Extraction, worker.ExtractionTimeout.Std())
	return model.NewProvider(remote)
}

// NewGovernanceProvider assembles the relation/classification model adapter.
func NewGovernanceProvider(cfg ModelConfig, worker WorkerConfig) (*model.Provider, error) {
	remote := remoteModelConfig(cfg)
	remote.GovernanceTimeout = cfg.TaskTimeout(cfg.Governance, worker.GovernanceTimeout.Std())
	return model.NewProvider(remote)
}

// NewAggregationProvider assembles the derived-memory and L4 model adapter.
func NewAggregationProvider(cfg ModelConfig, worker WorkerConfig) (*model.Provider, error) {
	remote := remoteModelConfig(cfg)
	remote.AggregationTimeout = cfg.TaskTimeout(cfg.Aggregation, worker.AggregationTimeout.Std())
	return model.NewProvider(remote)
}

func remoteModelConfig(cfg ModelConfig) model.Config {
	return model.Config{
		Name:                 cfg.Name,
		BaseURL:              cfg.BaseURL,
		APIKey:               cfg.APIKey,
		Timeout:              cfg.Timeout.Std(),
		ExtractionModel:      cfg.TaskName(cfg.Extraction),
		GovernanceModel:      cfg.TaskName(cfg.Governance),
		AggregationModel:     cfg.TaskName(cfg.Aggregation),
		ExtractionReasoning:  cfg.TaskReasoning(cfg.Extraction),
		GovernanceReasoning:  cfg.TaskReasoning(cfg.Governance),
		AggregationReasoning: cfg.TaskReasoning(cfg.Aggregation),
	}
}

// NewExtractionService wires source loading and the minimal extraction protocol
// onto the L0 observation commit path. It has no retrieval, canonical-memory or
// governance dependency.
func NewExtractionService(store *sqlite.Store, provider model.ExtractionProvider,
	modelName string, cfg WorkerConfig, logger *slog.Logger,
	options ...ingest.ProcessorOption) *ingest.Processor {
	all := []ingest.ProcessorOption{
		ingest.WithExtractionTimeout(cfg.ExtractionTimeout.Std()),
		ingest.WithExtractionChunkRunes(cfg.ExtractionChunkRunes),
		ingest.WithModelName(modelName),
		ingest.WithProcessorLogger(logger),
	}
	all = append(all, options...)
	return ingest.NewProcessor(sqlite.IngestStore{Store: store}, store, provider, all...)
}

// NewExtractionWorker constructs the single sequential extraction executor.
func NewExtractionWorker(store *sqlite.Store, processor *ingest.Processor,
	cfg WorkerConfig, logger *slog.Logger) (*ingest.Worker, error) {
	if cfg.Workers != DefaultWorkerCount {
		return nil, errors.New("首版只允许一个 Extraction Worker")
	}
	if cfg.MaxAttempts <= 0 {
		return nil, errors.New("连续协议失败上限必须大于 0")
	}
	return ingest.NewWorker(
		sqlite.IngestStore{Store: store},
		store,
		processor,
		ingest.WithMaxAttempts(cfg.MaxAttempts),
		ingest.WithRetryDelays(workerRetryDelays(cfg)...),
		ingest.WithWorkerLogger(logger),
	), nil
}

// GovernanceService is the assembled set of governance-stage handlers. The
// worker owns only these task kinds; aggregation commits never pass through it.
type GovernanceService struct {
	handlers map[task.Kind]governance.Handler
}

// Handlers returns the fixed governance handler map.
func (s *GovernanceService) Handlers() map[task.Kind]governance.Handler {
	if s == nil {
		return nil
	}
	return s.handlers
}

// AggregationService is the assembled set of derived-memory handlers plus the
// write-path trigger that registers L1 work after governance commits.
type AggregationService struct {
	handlers map[task.Kind]governance.Handler
	trigger  *aggregation.Trigger
}

// Handlers returns the fixed aggregation handler map.
func (s *AggregationService) Handlers() map[task.Kind]governance.Handler {
	if s == nil {
		return nil
	}
	return s.handlers
}

// Trigger returns the write-path L1 trigger, or nil when aggregation is off.
func (s *AggregationService) Trigger() *aggregation.Trigger {
	if s == nil {
		return nil
	}
	return s.trigger
}

type topicSuggester interface {
	SuggestTopics(ctx context.Context, title, content, scopeKey string) ([]string, error)
}

// NewGovernanceService wires observation governance, classification, conflict
// review and periodic reconciliation. Aggregation model calls are deliberately
// absent from this stage.
func NewGovernanceService(store *sqlite.Store, memories *memory.Service, artifacts *artifact.Service,
	provider any, cfg GovernanceConfig, trigger *aggregation.Trigger,
	modelTimeout time.Duration, logger *slog.Logger) *GovernanceService {
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
	limiter := governance.NewLimiter(defaultGovernanceMaxCallsPerHour, cfg.MinCallInterval.Std()).WithLogger(logger)
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
	reconcileOptions := []governance.ReconcileOption{}
	if trigger != nil {
		reconcileOptions = append(reconcileOptions, governance.WithArtifactReconcile(artifacts))
	}
	reconcile := governance.NewReconcileHandler(
		sqlite.GovernanceStore{Store: store},
		cfg.Debounce.Std(),
		100,
		reconcileOptions...,
	)
	relationProvider, ok := provider.(governance.RelationProvider)
	if !ok {
		relationProvider = unavailableRelationProvider{}
	}
	materializer := governance.NewMaterializer(memories)
	observationOptions := []governance.ObservationGovernanceOption{
		governance.WithObservationGovernanceTimeout(modelTimeout),
		governance.WithObservationGovernanceMaxCandidates(cfg.MaxInputs),
		governance.WithObservationGovernanceLogger(logger),
	}
	if trigger != nil {
		observationOptions = append(observationOptions, governance.WithObservationAggregationSink(
			governance.AggregationSinkFunc(
				func(ctx context.Context, tx governance.Tx, item observation.Observation, _ []string) error {
					return trigger.ObservationGoverned(ctx, tx, item)
				})))
	}
	observationGovernance := governance.NewObservationGovernanceHandler(
		store,
		sqlite.GovernanceStore{Store: store},
		materializer,
		relationProvider,
		limiter,
		observationOptions...,
	)
	return &GovernanceService{handlers: map[task.Kind]governance.Handler{
		task.KindGovernObservation:   observationGovernance,
		task.KindClassifyObservation: classify,
		task.KindReviewConflict:      reviewConflict,
		task.KindReconcile:           reconcile,
	}}
}

// governanceTaskKinds is the fixed set owned by the governance worker.
var governanceTaskKinds = []task.Kind{
	task.KindGovernObservation,
	task.KindClassifyObservation,
	task.KindReviewConflict,
	task.KindReconcile,
}

// aggregationTaskKinds is the fixed set owned by the aggregation worker.
var aggregationTaskKinds = []task.Kind{
	task.KindAggregateL1,
	task.KindAggregateL2,
	task.KindAggregateL3,
	task.KindBuildGuidance,
}

// NewGovernanceWorker constructs the governance-only sequential executor.
func NewGovernanceWorker(store *sqlite.Store, service *GovernanceService,
	cfg GovernanceConfig, worker WorkerConfig, logger *slog.Logger) *governance.Worker {
	return governance.NewWorker(
		sqlite.GovernanceStore{Store: store},
		service.Handlers(),
		governance.WithKinds(governanceTaskKinds...),
		governance.WithReconcileInterval(cfg.ReconcileInterval.Std()),
		governance.WithRetryDelays(workerRetryDelays(worker)...),
		governance.WithMaxProtocolFailures(worker.MaxAttempts),
		governance.WithWorkerLogger(logger),
	)
}

// NewAggregationService wires L1/L2/L3 aggregation and L4 guidance generation.
// The write-path trigger is always built so governance can register L1 work when
// aggregation is enabled; it never calls a model itself.
func NewAggregationService(store *sqlite.Store, artifacts *artifact.Service,
	provider any, cfg AggregationConfig, modelTimeout time.Duration,
	logger *slog.Logger) *AggregationService {
	aggregationProvider, ok := provider.(aggregation.Provider)
	if !ok {
		aggregationProvider = unavailableAggregationProvider{}
	}
	guidanceProvider, ok := provider.(aggregation.GuidanceProvider)
	if !ok {
		guidanceProvider = unavailableGuidanceProvider{}
	}
	options := []aggregation.Option{
		aggregation.WithTimeout(modelTimeout),
		aggregation.WithMaxInputs(cfg.MaxInputs),
		aggregation.WithDebounce(cfg.Debounce.Std()),
		aggregation.WithGenerator("aggregation:layer"),
	}
	aggregationStore := sqlite.AggregationStore{Store: store}
	l1Aggregate := aggregation.NewL1AggregationHandler(
		aggregationStore, artifacts, aggregationProvider, options...)
	l2Aggregate := aggregation.NewL2AggregationHandler(
		aggregationStore, artifacts, aggregationProvider, options...)
	l3Aggregate := aggregation.NewL3AggregationHandler(
		aggregationStore, artifacts, aggregationProvider, options...)
	guidance := aggregation.NewGuidanceHandler(
		aggregationStore, artifacts, guidanceProvider,
		aggregation.WithGuidanceTimeout(modelTimeout),
		aggregation.WithGuidanceMaxInputs(cfg.MaxInputs),
		aggregation.WithGuidanceDebounce(cfg.Debounce.Std()),
	)
	trigger := aggregation.NewTrigger(aggregation.TriggerConfig{
		BucketSize: cfg.L1BucketSize.Std(),
		Debounce:   cfg.Debounce.Std(),
	})
	handlers := map[task.Kind]governance.Handler{
		task.KindAggregateL1:   l1Aggregate,
		task.KindAggregateL2:   l2Aggregate,
		task.KindAggregateL3:   l3Aggregate,
		task.KindBuildGuidance: guidance,
	}
	return &AggregationService{handlers: handlers, trigger: trigger}
}

// NewAggregationWorker constructs the aggregation-only sequential executor.
func NewAggregationWorker(store *sqlite.Store, service *AggregationService,
	cfg AggregationConfig, worker WorkerConfig, logger *slog.Logger) *governance.Worker {
	return governance.NewWorker(
		sqlite.GovernanceStore{Store: store},
		service.Handlers(),
		governance.WithKinds(aggregationTaskKinds...),
		governance.WithReconcileInterval(cfg.ReconcileInterval.Std()),
		governance.WithRetryDelays(workerRetryDelays(worker)...),
		governance.WithMaxProtocolFailures(worker.MaxAttempts),
		governance.WithWorkerLogger(logger),
	)
}

func workerRetryDelays(cfg WorkerConfig) []time.Duration {
	delays := make([]time.Duration, 0, len(cfg.RetryDelays))
	for _, delay := range cfg.RetryDelays {
		delays = append(delays, delay.Std())
	}
	return delays
}

// Stack is the complete process assembly. It contains the HTTP application, the
// independent stage workers and the business services exposed by local HTTP.
type Stack struct {
	App               *App
	ExtractionWorker  *ingest.Worker
	GovernanceWorker  *governance.Worker
	AggregationWorker *governance.Worker
	EmbeddingWorker   *embedding.Worker

	ObservationLifecycle *governance.ObservationLifecycleAPI
	Services             Services
}

// Recover requeues interrupted work in stage order before HTTP starts serving.
func (s *Stack) Recover(ctx context.Context) error {
	if s == nil {
		return nil
	}
	workers := []struct {
		name string
		run  func(context.Context) error
	}{
		{"extraction", recoverIngest(s.ExtractionWorker)},
		{"governance", recoverGovernance(s.GovernanceWorker)},
		{"aggregation", recoverGovernance(s.AggregationWorker)},
		{"embedding", recoverEmbedding(s.EmbeddingWorker)},
	}
	for _, worker := range workers {
		if worker.run == nil {
			continue
		}
		if err := worker.run(ctx); err != nil {
			return fmt.Errorf("恢复 %s 后台任务: %w", worker.name, err)
		}
	}
	return nil
}

func recoverIngest(worker *ingest.Worker) func(context.Context) error {
	if worker == nil {
		return nil
	}
	return worker.Recover
}

func recoverGovernance(worker *governance.Worker) func(context.Context) error {
	if worker == nil {
		return nil
	}
	return worker.Recover
}

func recoverEmbedding(worker *embedding.Worker) func(context.Context) error {
	if worker == nil {
		return nil
	}
	return worker.Recover
}

// NewStack assembles configuration, business services, independent stage
// providers, background workers and the full HTTP API. A missing model base URL
// is a valid degraded state: submissions are accepted and each model-dependent
// stage parks only its own work while reads, search and management keep working.
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
	var embeddingProbe embedding.ProbeEncoder
	if encoder != nil {
		embeddingProbe = encoder
	}
	embeddingDiagnostics := embedding.NewDiagnosticService(store, embeddingProbe, cfg.Embedding.toEmbedding())

	artifacts := NewArtifactService(store, cfg.Aggregation.Debounce.Std(), logger)
	memories := NewMemoryService(store,
		memory.WithGovernanceDebounce(cfg.Governance.Debounce.Std()),
		memory.WithArtifactSync(artifacts),
	)
	search := NewRecallService(store, artifacts, encoder, logger)
	ingestService := NewIngestService(store)
	observationLifecycle := NewObservationLifecycleService(store, memories)
	sourceService := NewSourceService(store,
		source.WithRedactionPropagator(governance.NewRedactionPropagator(memories)))

	extractionProvider, governanceProvider, aggregationProvider := stageProviders(cfg, logger)

	aggregationTimeout := cfg.Model.TaskTimeout(cfg.Model.Aggregation, cfg.Worker.AggregationTimeout.Std())
	governanceTimeout := cfg.Model.TaskTimeout(cfg.Model.Governance, cfg.Worker.GovernanceTimeout.Std())
	extractionTimeout := cfg.Model.TaskTimeout(cfg.Model.Extraction, cfg.Worker.ExtractionTimeout.Std())

	aggregationService := NewAggregationService(store, artifacts, aggregationProvider,
		cfg.Aggregation, aggregationTimeout, logger)
	var trigger *aggregation.Trigger
	if cfg.Aggregation.IsEnabled() {
		trigger = aggregationService.Trigger()
	}
	governanceService := NewGovernanceService(store, memories, artifacts, governanceProvider,
		cfg.Governance, trigger, governanceTimeout, logger)

	processor := NewExtractionService(store, extractionProvider,
		cfg.Model.TaskName(cfg.Model.Extraction), cfg.Worker, logger,
		ingest.WithExtractionTimeout(extractionTimeout))
	extractionWorker, err := NewExtractionWorker(store, processor, cfg.Worker, logger)
	if err != nil {
		return nil, err
	}
	if err := store.ConfigureGovernanceFairness(cfg.Governance.MaxWait.Std()); err != nil {
		return nil, err
	}
	var governanceWorker *governance.Worker
	if cfg.Governance.IsEnabled() {
		governanceWorker = NewGovernanceWorker(store, governanceService,
			cfg.Governance, cfg.Worker, logger)
	} else {
		logger.Info("governance worker disabled by configuration")
	}
	var aggregationWorker *governance.Worker
	if cfg.Aggregation.IsEnabled() {
		aggregationWorker = NewAggregationWorker(store, aggregationService,
			cfg.Aggregation, cfg.Worker, logger)
	} else {
		logger.Info("aggregation worker disabled by configuration")
	}

	var embeddingWorker *embedding.Worker
	if encoder != nil {
		// A migrated database or a changed document contract requires one full
		// local rebuild before vector generation. Otherwise the worker's
		// startup scan only enqueues missing or profile-mismatched vectors.
		registration, err := store.RegisterEmbeddingConfiguration(
			context.Background(), encoder.ProfileID(), embedding.IndexVersion, time.Now().UTC(),
		)
		if err != nil {
			return nil, err
		}
		needsRebuild, err := needsIndexRebuild(context.Background(), store)
		if err != nil {
			return nil, err
		}
		if needsRebuild || registration.IndexVersionChanged {
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
		Ingest:       ingestService,
		Observations: NewObservationService(store),
		Memories:     memories,
		Conflicts:    memories,
		Relations:    NewRelationService(store),
		Sources:      sourceService,
		Search:       search,
		Derived:      artifacts,
		Experiences:  NewExperienceService(store),
		Skills:       NewSkillService(artifacts),
		Forget:       NewForgetService(store, memories),
		Jobs:         NewJobsService(store),
		Embedding:    embeddingDiagnostics,
	}
	app, err := New(cfg, logger, store,
		WithServices(services),
		WithExtractionWorker(extractionWorker),
		WithGovernanceWorker(governanceWorker),
		WithAggregationWorker(aggregationWorker),
		WithEmbeddingWorker(embeddingWorker))
	if err != nil {
		return nil, err
	}
	return &Stack{
		App:                  app,
		ExtractionWorker:     extractionWorker,
		GovernanceWorker:     governanceWorker,
		AggregationWorker:    aggregationWorker,
		EmbeddingWorker:      embeddingWorker,
		ObservationLifecycle: observationLifecycle,
		Services:             services,
	}, nil
}

// stageProviders builds one independent remote adapter per stage. A missing
// endpoint installs only the unavailable provider for that stage, so a model
// outage never blocks source submission, reads or another background stage.
func stageProviders(cfg Config, logger *slog.Logger) (model.ExtractionProvider, any, any) {
	if strings.TrimSpace(cfg.Model.BaseURL) == "" {
		logger.Warn("model provider not configured; submissions will be queued but extraction will fail")
		provider := unavailableProvider{}
		return provider, provider, provider
	}
	extractionProvider, extractionErr := NewExtractionProvider(cfg.Model, cfg.Worker)
	if extractionErr != nil {
		logger.Warn("extraction provider configuration invalid", "error", extractionErr)
	}
	governanceProvider, governanceErr := NewGovernanceProvider(cfg.Model, cfg.Worker)
	if governanceErr != nil {
		logger.Warn("governance provider configuration invalid", "error", governanceErr)
	}
	aggregationProvider, aggregationErr := NewAggregationProvider(cfg.Model, cfg.Worker)
	if aggregationErr != nil {
		logger.Warn("aggregation provider configuration invalid", "error", aggregationErr)
	}
	if extractionErr != nil || governanceErr != nil || aggregationErr != nil {
		fallback := unavailableProvider{}
		var extractionStage model.ExtractionProvider = fallback
		var governanceStage any = fallback
		var aggregationStage any = fallback
		if extractionErr == nil {
			extractionStage = extractionProvider
		}
		if governanceErr == nil {
			governanceStage = governanceProvider
		}
		if aggregationErr == nil {
			aggregationStage = aggregationProvider
		}
		return extractionStage, governanceStage, aggregationStage
	}
	observer := modelObserver(logger)
	extractionProvider.SetObserver(observer)
	governanceProvider.SetObserver(observer)
	aggregationProvider.SetObserver(observer)
	return extractionProvider, governanceProvider, aggregationProvider
}

func modelObserver(logger *slog.Logger) model.CallObserver {
	return func(metrics model.CallMetrics) {
		level := slog.LevelDebug
		switch metrics.Operation {
		case "classification", "aggregate", "aggregation", "experience", "skill":
			level = slog.LevelInfo
		}
		logger.Log(context.Background(), level, "model call metrics",
			"stage", metrics.Operation,
			"model", metrics.Model,
			"duration_ms", metrics.Duration.Milliseconds(),
			"calls", metrics.Usage.Calls,
			"input_tokens", metrics.Usage.InputTokens,
			"output_tokens", metrics.Usage.OutputTokens,
		)
	}
}

// unavailableProvider keeps one stage parked without affecting another. It
// implements every model capability so callers can type-assert the stage they
// need and still receive a configuration error rather than a panic.
type unavailableProvider struct{}

func (unavailableProvider) Extract(context.Context, extraction.Request) (extraction.Result, error) {
	return extraction.Result{}, configurationError("模型服务未配置")
}

func (unavailableProvider) Relate(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
	return governance.RelationResult{}, configurationError("关系模型未配置")
}

func (unavailableProvider) SuggestTopics(context.Context, string, string, string) ([]string, error) {
	return nil, configurationError("分类模型未配置")
}

func (unavailableProvider) Aggregate(context.Context, aggregation.Request) (aggregation.Result, error) {
	return aggregation.Result{}, configurationError("聚合模型未配置")
}

func (unavailableProvider) ComposeExperience(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
	return artifact.ExperienceResult{}, configurationError("经验案例模型未配置")
}

func (unavailableProvider) ComposeSkill(context.Context, skill.Request) (skill.Draft, error) {
	return skill.Draft{}, configurationError("Skill 模型未配置")
}

func configurationError(message string) error {
	return &ingest.ProviderError{
		Operation: "configuration",
		Retryable: true,
		Err:       errors.New(message),
	}
}

type unavailableRelationProvider struct{}

func (unavailableRelationProvider) Relate(context.Context, governance.RelationRequest) (governance.RelationResult, error) {
	return governance.RelationResult{}, configurationError("关系模型未配置")
}

type unavailableGuidanceProvider struct{}

func (unavailableGuidanceProvider) ComposeExperience(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
	return artifact.ExperienceResult{}, configurationError("经验案例模型未配置")
}

func (unavailableGuidanceProvider) ComposeSkill(context.Context, skill.Request) (skill.Draft, error) {
	return skill.Draft{}, configurationError("Skill 模型未配置")
}

type unavailableAggregationProvider struct{}

func (unavailableAggregationProvider) Aggregate(context.Context, aggregation.Request) (aggregation.Result, error) {
	return aggregation.Result{}, configurationError("聚合模型未配置")
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
