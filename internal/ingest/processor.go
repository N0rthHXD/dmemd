package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/extraction"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const (
	// DefaultProcessBudget bounds one extraction attempt, including model calls.
	DefaultProcessBudget     = 600 * time.Second
	DefaultExtractionTimeout = extraction.DefaultTimeout
	// DefaultExtractionChunkRunes is the bounded source window for one request.
	DefaultExtractionChunkRunes = extraction.DefaultChunkRunes
)

// SourceReader loads the accepted source for background processing.
type SourceReader interface {
	GetSource(ctx context.Context, id string) (source.Source, error)
}

// Processor owns one extraction attempt: source loading, the extraction model
// call, deterministic observation construction and the short commit transaction
// that saves L0 observations, code-generated evidence and governance tasks.
//
// It deliberately has no retrieval, memory writer or decision-model dependency.
type Processor struct {
	store             Store
	sources           SourceReader
	extractor         extraction.Provider
	service           *extraction.Service
	modelName         string
	now               func() time.Time
	logger            *slog.Logger
	processBudget     time.Duration
	extractionTimeout time.Duration
	chunkRunes        int
}

// ProcessorOption customizes a Processor.
type ProcessorOption func(*Processor)

// WithExtractionTimeout bounds one extraction model call. The attempt itself
// keeps its fixed local budget so long sources can still use bounded chunks.
func WithExtractionTimeout(timeout time.Duration) ProcessorOption {
	return func(p *Processor) {
		if timeout > 0 {
			p.extractionTimeout = timeout
		}
	}
}

// WithExtractionChunkRunes bounds one extraction request to a deterministic
// source window. Non-positive keeps the built-in default.
func WithExtractionChunkRunes(value int) ProcessorOption {
	return func(p *Processor) {
		if value > 0 {
			p.chunkRunes = value
		}
	}
}

// WithProcessorClock overrides the processing clock. Values are normalized to UTC.
func WithProcessorClock(now func() time.Time) ProcessorOption {
	return func(processor *Processor) {
		if now != nil {
			processor.now = now
		}
	}
}

// WithProcessorLogger sets the structured logger. It never receives source
// content, observation text, model requests or model responses.
func WithProcessorLogger(logger *slog.Logger) ProcessorOption {
	return func(processor *Processor) {
		if logger != nil {
			processor.logger = logger
		}
	}
}

// WithModelName sets the model identifier used only for logs and metrics.
func WithModelName(name string) ProcessorOption {
	return func(processor *Processor) {
		processor.modelName = name
	}
}

// NewProcessor assembles the extraction stage. It does not read memory state and
// does not create canonical memories; governance is a separate asynchronous
// stage reached through the task table.
func NewProcessor(store Store, sources SourceReader, provider extraction.Provider, options ...ProcessorOption) *Processor {
	processor := &Processor{
		store:             store,
		sources:           sources,
		extractor:         provider,
		now:               time.Now,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		processBudget:     DefaultProcessBudget,
		extractionTimeout: DefaultExtractionTimeout,
		chunkRunes:        extraction.DefaultChunkRunes,
	}
	for _, option := range options {
		option(processor)
	}
	processor.service = extraction.NewService(
		processor.extractor,
		extraction.WithTimeout(processor.extractionTimeout),
		extraction.WithChunkRunes(processor.chunkRunes),
		extraction.WithLogger(processor.logger),
	)
	return processor
}

// Process executes one claimed extraction job. The model call happens outside
// the write transaction; observations, evidence, governance tasks and the job
// result commit together.
func (p *Processor) Process(ctx context.Context, job Job) (output ProcessResult, processErr error) {
	if p.store == nil || p.sources == nil || p.service == nil {
		return ProcessResult{}, errors.New("ingest: processor dependencies not configured")
	}
	processStarted := time.Now()
	queueAge := queueAgeMillis(job, processStarted)
	budgetCtx, cancel := context.WithTimeout(ctx, p.processBudget)
	defer cancel()
	stage := "source"
	defer func() {
		if processErr != nil {
			code := ""
			if ctx.Err() == nil && errors.Is(budgetCtx.Err(), context.DeadlineExceeded) {
				code = "process_budget_exceeded"
			}
			processErr = processingError(stage, code, processErr)
		}
	}()

	src, err := p.sources.GetSource(budgetCtx, job.SourceID)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("读取待处理来源: %w", err)
	}
	if src.Redacted {
		return p.finishCancelled(budgetCtx, job)
	}

	stage = "extraction"
	started := time.Now()
	extracted, err := p.service.Extract(budgetCtx, src)
	timings := ProcessingTimings{
		QueueAgeMillis:   queueAge,
		ExtractionMillis: time.Since(started).Milliseconds(),
	}
	if err != nil {
		timings.TotalMillis = time.Since(processStarted).Milliseconds()
		return ProcessResult{}, processingError(stage, "", err)
	}

	stage = "observations"
	now := p.now().UTC()
	buildStarted := time.Now()
	records, buildStats := extraction.BuildObservations(src, extracted.Drafts, extraction.ProtocolVersion, now)
	timings.NormalizationMillis = time.Since(buildStarted).Milliseconds()
	stats := ExtractionStats{
		Items:                  len(extracted.Drafts),
		Observations:           len(records),
		NormalizedObservations: buildStats.Normalized,
		PendingObservations:    buildStats.Pending,
		InvalidItems:           buildStats.Invalid,
		DuplicateObservations:  buildStats.Duplicates,
		Chunks:                 extracted.Chunks,
		ChunkFailures:          extracted.ChunkFailures,
	}
	warnings := []string{}
	if extracted.ChunkFailures > 0 {
		warnings = append(warnings, WarningExtractionChunkFailed)
	}
	if buildStats.Pending > 0 {
		warnings = append(warnings, WarningObservationPending)
	}
	p.logger.Info("model extraction completed",
		"job_id", job.ID,
		"model", p.modelName,
		"drafts", len(extracted.Drafts),
		"observations", len(records),
		"pending_observations", buildStats.Pending,
		"duplicates", buildStats.Duplicates,
		"chunks", extracted.Chunks,
		"duration_ms", time.Since(started).Milliseconds(),
	)

	now = p.now().UTC()
	observationIDs := make([]string, 0, len(records))
	resultStatus := JobSucceeded
	stage = "commit"
	commitStarted := time.Now()
	err = p.store.WithTx(budgetCtx, func(ctx context.Context, tx Tx) error {
		currentJob, err := tx.GetJob(ctx, job.ID)
		if err != nil {
			return err
		}
		switch currentJob.Status {
		case JobCancelled, JobSucceeded, JobFailed:
			resultStatus = currentJob.Status
			observationIDs = append(observationIDs, currentJob.ObservationIDs...)
			return nil
		}
		currentSource, err := tx.GetSource(ctx, job.SourceID)
		if err != nil {
			return err
		}
		if currentSource.Redacted {
			cancelJobForRedaction(&currentJob, now)
			resultStatus = JobCancelled
			return tx.UpdateJob(ctx, currentJob)
		}

		for _, record := range records {
			item := record.Observation
			if err := tx.InsertObservation(ctx, item); err != nil {
				return err
			}
			for _, evidence := range record.Evidence {
				if err := tx.InsertEvidence(ctx, evidence); err != nil {
					return err
				}
				if err := tx.InsertObservationEvidence(ctx, item.ID, evidence.ID); err != nil {
					return err
				}
			}
			if item.Status == observation.StatusObserved {
				if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
					Kind:      task.KindGovernObservation,
					TargetKey: task.ObservationKey(item.ID),
					InputHash: task.InputHash(item.ID, item.SourceID, item.Content),
					Now:       now,
				}); err != nil {
					return err
				}
			}
			observationIDs = append(observationIDs, item.ID)
		}

		timings.CommitMillis = time.Since(commitStarted).Milliseconds()
		timings.TotalMillis = time.Since(processStarted).Milliseconds()
		currentJob.Status = JobSucceeded
		currentJob.ObservationIDs = append([]string(nil), observationIDs...)
		currentJob.Warnings = uniqueWarnings(warnings)
		currentJob.QualityOutcome = observationQuality(len(extracted.Drafts), buildStats, extracted)
		currentJob.ExtractionStats = stats
		currentJob.Timings = timings
		currentJob.ModelUsage = extracted.Usage
		currentJob.Diagnostics = observationDiagnostics(buildStats)
		currentJob.Error = ""
		currentJob.ProtocolFailures = 0
		currentJob.WaitReason = ""
		currentJob.Stage = ""
		currentJob.ReasonCode = ""
		currentJob.NextAttemptAt = nil
		currentJob.UpdatedAt = now
		currentJob.FinishedAt = &now
		return tx.UpdateJob(ctx, currentJob)
	})
	if err != nil {
		timings.CommitMillis = time.Since(commitStarted).Milliseconds()
		timings.TotalMillis = time.Since(processStarted).Milliseconds()
		return ProcessResult{}, processingError(stage, "", fmt.Errorf("提交观察结果: %w", err))
	}
	if resultStatus != JobSucceeded {
		return ProcessResult{Status: resultStatus, ObservationIDs: []string{}}, nil
	}
	timings.CommitMillis = time.Since(commitStarted).Milliseconds()
	timings.TotalMillis = time.Since(processStarted).Milliseconds()
	return ProcessResult{
		Status:         JobSucceeded,
		ObservationIDs: observationIDs,
		Warnings:       uniqueWarnings(warnings),
		Quality:        observationQuality(len(extracted.Drafts), buildStats, extracted),
		Stats:          stats,
		Timings:        timings,
		ModelUsage:     extracted.Usage,
		Diagnostics:    observationDiagnostics(buildStats),
	}, nil
}

func observationQuality(raw int, stats extraction.BuildStats, result extraction.Result) QualityOutcome {
	switch {
	case raw == 0 && stats.Invalid == 0:
		return QualityEmpty
	case stats.Pending > 0, stats.Invalid > 0, result.ChunkFailures > 0:
		return QualityPartial
	default:
		return QualityComplete
	}
}

func observationDiagnostics(stats extraction.BuildStats) []ExtractionDiagnostic {
	diagnostics := make([]ExtractionDiagnostic, 0, 2)
	if stats.Invalid > 0 {
		diagnostics = append(diagnostics, ExtractionDiagnostic{
			Field: "span_ids", Code: "observation_draft_invalid", Stage: "observations", Disposition: "isolated",
		})
	}
	if stats.Pending > 0 {
		diagnostics = append(diagnostics, ExtractionDiagnostic{
			Field: "span_ids", Code: "observation_evidence_unbound", Stage: "observations", Disposition: "pending",
		})
	}
	return diagnostics
}

func cancelJobForRedaction(job *Job, now time.Time) {
	job.Status = JobCancelled
	job.Error = source.RedactedJobReason
	job.ObservationIDs = []string{}
	job.Warnings = []string{}
	job.QualityOutcome = ""
	job.ExtractionStats = ExtractionStats{}
	job.Timings = ProcessingTimings{}
	job.ModelUsage = ModelUsage{}
	job.Diagnostics = []ExtractionDiagnostic{}
	job.WaitReason = ""
	job.NextAttemptAt = nil
	job.UpdatedAt = now
	job.FinishedAt = &now
}

func (p *Processor) finishCancelled(ctx context.Context, job Job) (ProcessResult, error) {
	now := p.now().UTC()
	err := p.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		current, err := tx.GetJob(ctx, job.ID)
		if err != nil {
			return err
		}
		switch current.Status {
		case JobCancelled, JobSucceeded, JobFailed:
			return nil
		}
		cancelJobForRedaction(&current, now)
		return tx.UpdateJob(ctx, current)
	})
	if err != nil {
		return ProcessResult{}, err
	}
	return ProcessResult{Status: JobCancelled, ObservationIDs: []string{}}, nil
}

func queueAgeMillis(job Job, now time.Time) int64 {
	if job.CreatedAt.IsZero() || now.Before(job.CreatedAt) {
		return 0
	}
	return now.Sub(job.CreatedAt).Milliseconds()
}

func uniqueWarnings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
