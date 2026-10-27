package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/temporal"
	"github.com/N0rthHXD/dmemd/internal/topic"
)

const (
	// DefaultProcessBudget bounds an attempt, independently of per-call limits.
	DefaultProcessBudget     = 600 * time.Second
	DefaultExtractionTimeout = 180 * time.Second
	DefaultDecisionTimeout   = 300 * time.Second
	// DefaultExtractionChunkRunes is the bounded source window used for a long
	// extraction input. It is a capacity bound, not a semantic segmentation.
	DefaultExtractionChunkRunes = 12000
	// maxCandidatesPerSource is the fixed first-release extraction budget.
	maxCandidatesPerSource = 64
	// maxDecisionBatch is the fixed dependency batch size.
	maxDecisionBatch = 8
	// maxInitialContext is the per-candidate similar-memory cap.
	maxInitialContext = 20
	// maxTimelineContext is the per-group timeline cap.
	maxTimelineContext = 50
)

// SourceReader loads the accepted source for background processing.
type SourceReader interface {
	GetSource(ctx context.Context, id string) (source.Source, error)
}

// FactGroupReader reads group metadata for decision context. sqlite.Store
// implements it alongside SourceReader.
type FactGroupReader interface {
	GetFactGroup(ctx context.Context, id string) (memory.FactGroup, error)
	FindFactGroups(ctx context.Context, scopeKey, subject, predicate string) ([]memory.FactGroup, error)
}

// Processor orchestrates source loading, extraction, per-candidate context
// selection, on-demand model decisions and one atomic merge transaction.
type Processor struct {
	store             Store
	sources           SourceReader
	related           retrieval.RelatedService
	writer            memory.Writer
	extractor         ExtractionProvider
	decider           DecisionProvider
	modelName         string
	decisionModelName string
	now               func() time.Time
	logger            *slog.Logger
	processBudget     time.Duration
	extractionTimeout time.Duration
	decisionTimeout   time.Duration
	chunkRunes        int
}

// ProcessorOption customizes a Processor.
type ProcessorOption func(*Processor)

// WithProcessingBudget configures the attempt budget and individual model calls.
func WithProcessingBudget(budget, extraction, decision time.Duration) ProcessorOption {
	return func(p *Processor) {
		if budget > 0 {
			p.processBudget = budget
		}
		if extraction > 0 {
			p.extractionTimeout = extraction
		}
		if decision > 0 {
			p.decisionTimeout = decision
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

// WithProcessorClock overrides the processing clock. Values are UTC.
func WithProcessorClock(now func() time.Time) ProcessorOption {
	return func(processor *Processor) {
		if now != nil {
			processor.now = now
		}
	}
}

// WithProcessorLogger sets the structured logger. It never receives source
// content, memory text, model requests or model responses.
func WithProcessorLogger(logger *slog.Logger) ProcessorOption {
	return func(processor *Processor) {
		if logger != nil {
			processor.logger = logger
		}
	}
}

// WithModelName sets the model identifier recorded on generated evidence.
func WithModelName(name string) ProcessorOption {
	return func(processor *Processor) {
		processor.modelName = name
	}
}

// WithDecisionModelName records the effective decision model for logs. It does
// not change provider behavior or evidence provenance.
func WithDecisionModelName(name string) ProcessorOption {
	return func(processor *Processor) {
		processor.decisionModelName = name
	}
}

// WithDecisionProvider injects the separate decision capability. When omitted,
// a provider that also implements DecisionProvider is used automatically.
func WithDecisionProvider(provider DecisionProvider) ProcessorOption {
	return func(processor *Processor) {
		if provider != nil {
			processor.decider = provider
		}
	}
}

// NewProcessor assembles the background chain. All network work and plan
// preparation happen before Store.WithTx.
func NewProcessor(store Store, sources SourceReader, related retrieval.RelatedService, writer memory.Writer, provider ExtractionProvider, options ...ProcessorOption) *Processor {
	processor := &Processor{
		store:             store,
		sources:           sources,
		related:           related,
		writer:            writer,
		extractor:         provider,
		now:               time.Now,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		processBudget:     DefaultProcessBudget,
		extractionTimeout: DefaultExtractionTimeout,
		decisionTimeout:   DefaultDecisionTimeout,
		chunkRunes:        DefaultExtractionChunkRunes,
	}
	if decider, ok := provider.(DecisionProvider); ok {
		processor.decider = decider
	}
	for _, option := range options {
		option(processor)
	}
	return processor
}

// Process runs one claimed job. Extraction and decision calls happen outside the
// write transaction; all writes and the job success state commit together.
func (p *Processor) Process(ctx context.Context, job Job) (output ProcessResult, processErr error) {
	if p.store == nil || p.sources == nil || p.related == nil || p.writer == nil || p.extractor == nil {
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

	started := time.Now()
	stage = "extraction"
	extraction, err := p.extractValidated(budgetCtx, src)
	if err != nil {
		extraction.Timings.QueueAgeMillis = queueAge
		extraction.Timings.TotalMillis = time.Since(processStarted).Milliseconds()
		return ProcessResult{}, extractionFailure(extraction, err)
	}
	extraction.Timings.QueueAgeMillis = queueAge
	candidates := extraction.Candidates
	normalizeStarted := time.Now()
	candidates = normalizeCandidates(src, source.BuildView(src.Content), candidates, p.modelName, p.now().UTC())
	extraction.Timings.NormalizationMillis = time.Since(normalizeStarted).Milliseconds()
	p.logger.Info("model extraction completed",
		"job_id", job.ID,
		"model", p.modelName,
		"candidates", len(candidates),
		"raw_candidates", extraction.Stats.RawCandidates,
		"isolated_candidates", extraction.Stats.IsolatedCandidates,
		"chunks", extraction.Stats.ExtractionChunks,
		"duration_ms", time.Since(started).Milliseconds(),
	)

	// Pin the catalog generation before any context read or model call. A write
	// that lands while we decide therefore invalidates the plan.
	stage = "context"
	contextStarted := time.Now()
	generation, err := p.related.CatalogGeneration(budgetCtx)
	if err != nil {
		extraction.Timings.ContextMillis = time.Since(contextStarted).Milliseconds()
		extraction.Timings.TotalMillis = time.Since(processStarted).Milliseconds()
		return ProcessResult{}, extractionFailure(extraction, fmt.Errorf("读取目录版本: %w", err))
	}
	plan, err := p.buildPlan(budgetCtx, src, candidates)
	if err != nil {
		extraction.Timings.ContextMillis = time.Since(contextStarted).Milliseconds()
		extraction.Timings.TotalMillis = time.Since(processStarted).Milliseconds()
		return ProcessResult{}, extractionFailure(extraction, err)
	}
	plan.catalogGeneration = generation
	plan.warnings = append(plan.warnings, extraction.Warnings...)
	if err := p.loadContext(budgetCtx, plan); err != nil {
		extraction.Timings.ContextMillis = time.Since(contextStarted).Milliseconds()
		extraction.Timings.TotalMillis = time.Since(processStarted).Milliseconds()
		return ProcessResult{}, extractionFailure(extraction, err)
	}
	extraction.Timings.ContextMillis = time.Since(contextStarted).Milliseconds()
	if !plan.complete {
		plan.warnings = append(plan.warnings, WarningContextTruncated)
	}
	stage = "decision"
	decisionStarted := time.Now()
	decisionUsage, err := p.decide(budgetCtx, plan)
	extraction.Timings.DecisionMillis = time.Since(decisionStarted).Milliseconds()
	extraction.Usage.Add(decisionUsage)
	if err != nil {
		extraction.Timings.TotalMillis = time.Since(processStarted).Milliseconds()
		return ProcessResult{}, extractionFailure(extraction, err)
	}

	skipped := 0
	for _, action := range plan.actions {
		if strings.HasPrefix(string(action.decision.Action), "skip") {
			skipped++
			reason := memory.ClassifyRetention(action.candidate.Kind, action.candidate.Phase,
				action.candidate.Durability, action.candidate.EventTime, action.candidate.ValidTo, p.now().UTC())
			extraction.Retention.removeReason(reason)
			extraction.Retention.addNotRetained(RetentionNotDecisionSkip, 1)
		}
	}
	extraction.Stats.SkippedCandidates = skipped

	var result mergeResult
	stage = "commit"
	commitStarted := time.Now()
	now := p.now().UTC()
	err = p.store.WithTx(budgetCtx, func(ctx context.Context, tx Tx) error {
		currentJob, err := tx.GetJob(ctx, job.ID)
		if err != nil {
			return err
		}
		switch currentJob.Status {
		case JobCancelled, JobSucceeded, JobFailed:
			result = mergeResult{Status: currentJob.Status}
			return nil
		}
		currentSource, err := tx.GetSource(ctx, job.SourceID)
		if err != nil {
			return err
		}
		if currentSource.Redacted {
			currentJob.Status = JobCancelled
			currentJob.Error = source.RedactedJobReason
			currentJob.MemoryIDs = []string{}
			currentJob.ConflictIDs = []string{}
			currentJob.PendingMemoryIDs = []string{}
			currentJob.Warnings = []string{}
			currentJob.QualityOutcome = ""
			currentJob.ExtractionStats = ExtractionStats{}
			currentJob.Retention = RetentionStats{}
			currentJob.Retention.Normalize()
			currentJob.Timings = ProcessingTimings{}
			currentJob.ModelUsage = ModelUsage{}
			currentJob.Diagnostics = []ExtractionDiagnostic{}
			currentJob.Convergence = []ConvergenceDiagnostic{}
			currentJob.NextAttemptAt = nil
			currentJob.UpdatedAt = now
			currentJob.FinishedAt = &now
			result = mergeResult{Status: JobCancelled}
			return tx.UpdateJob(ctx, currentJob)
		}
		if err := checkPlanState(ctx, tx, plan); err != nil {
			return err
		}
		result, err = applyPlan(ctx, tx, p.writer, plan)
		if err != nil {
			return err
		}
		stats := extraction.Stats
		stats.MemoryWrites = len(result.MemoryIDs)
		quality := extraction.quality
		if quality == "" {
			quality = extraction.finalQuality()
		}
		timings := extraction.Timings
		timings.CommitMillis = time.Since(commitStarted).Milliseconds()
		timings.TotalMillis = time.Since(processStarted).Milliseconds()
		currentJob.Status = JobSucceeded
		currentJob.MemoryIDs = result.MemoryIDs
		currentJob.ConflictIDs = result.ConflictIDs
		currentJob.PendingMemoryIDs = result.PendingMemoryIDs
		currentJob.Warnings = result.Warnings
		currentJob.QualityOutcome = quality
		currentJob.ExtractionStats = stats
		currentJob.Retention = extraction.Retention
		currentJob.Timings = timings
		currentJob.ModelUsage = extraction.Usage
		currentJob.Diagnostics = extraction.Diagnostics
		currentJob.Convergence = result.Convergence
		if currentJob.MemoryIDs == nil {
			currentJob.MemoryIDs = []string{}
		}
		if currentJob.ConflictIDs == nil {
			currentJob.ConflictIDs = []string{}
		}
		if currentJob.PendingMemoryIDs == nil {
			currentJob.PendingMemoryIDs = []string{}
		}
		if currentJob.Warnings == nil {
			currentJob.Warnings = []string{}
		}
		if currentJob.Diagnostics == nil {
			currentJob.Diagnostics = []ExtractionDiagnostic{}
		}
		if currentJob.Convergence == nil {
			currentJob.Convergence = []ConvergenceDiagnostic{}
		}
		currentJob.Error = ""
		currentJob.ProtocolFailures = 0
		currentJob.WaitReason = ""
		currentJob.Stage = ""
		currentJob.ReasonCode = ""
		currentJob.NextAttemptAt = nil
		currentJob.UpdatedAt = now
		currentJob.FinishedAt = &now
		if err := tx.UpdateJob(ctx, currentJob); err != nil {
			return err
		}
		for _, decision := range result.Decisions {
			p.logger.Debug("ingest decision",
				"job_id", job.ID,
				"action", decision.Action,
				"memory_id", decision.MemoryID,
			)
		}
		return nil
	})
	if err != nil {
		extraction.Timings.CommitMillis = time.Since(commitStarted).Milliseconds()
		extraction.Timings.TotalMillis = time.Since(processStarted).Milliseconds()
		return ProcessResult{}, extractionFailure(extraction, fmt.Errorf("提交处理结果: %w", err))
	}
	if result.Status == "" || result.Status == JobSucceeded {
		extraction.Stats.MemoryWrites = len(result.MemoryIDs)
		if extraction.quality == "" {
			extraction.quality = extraction.finalQuality()
		}
		extraction.Timings.CommitMillis = time.Since(commitStarted).Milliseconds()
		extraction.Timings.TotalMillis = time.Since(processStarted).Milliseconds()
	}
	if result.Status != "" && result.Status != JobSucceeded {
		return ProcessResult{Status: result.Status, MemoryIDs: []string{}}, nil
	}
	return ProcessResult{
		Status:           JobSucceeded,
		MemoryIDs:        result.MemoryIDs,
		ConflictIDs:      result.ConflictIDs,
		PendingMemoryIDs: result.PendingMemoryIDs,
		Warnings:         result.Warnings,
		Quality:          extraction.quality,
		Stats:            extraction.Stats,
		Retention:        extraction.Retention,
		Timings:          extraction.Timings,
		ModelUsage:       extraction.Usage,
		Diagnostics:      extraction.Diagnostics,
		Convergence:      result.Convergence,
	}, nil
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
		current.Status = JobCancelled
		current.Error = source.RedactedJobReason
		current.MemoryIDs = []string{}
		current.ConflictIDs = []string{}
		current.PendingMemoryIDs = []string{}
		current.Warnings = []string{}
		current.QualityOutcome = ""
		current.ExtractionStats = ExtractionStats{}
		current.Retention = RetentionStats{}
		current.Retention.Normalize()
		current.Timings = ProcessingTimings{}
		current.ModelUsage = ModelUsage{}
		current.Diagnostics = []ExtractionDiagnostic{}
		current.Convergence = []ConvergenceDiagnostic{}
		current.WaitReason = ""
		current.NextAttemptAt = nil
		current.UpdatedAt = now
		current.FinishedAt = &now
		return tx.UpdateJob(ctx, current)
	})
	if err != nil {
		return ProcessResult{}, err
	}
	return ProcessResult{Status: JobCancelled, MemoryIDs: []string{}}, nil
}

// extractValidated is the boundary between an untrusted model response and
// deterministic ingestion. One local repair is allowed. Candidates that still
// fail are isolated; candidates that already passed are retained byte-for-byte.
// Long sources are split into deterministic segment chunks with read-only
// overlap context, and cross-chunk representations are deduplicated before
// validation. The same deterministic source view is used for every chunk.
func (p *Processor) extractValidated(ctx context.Context, src source.Source) (outcome extractionOutcome, err error) {
	view := source.BuildView(src.Content)
	outcome = extractionOutcome{Warnings: []string{}}
	outcome.Retention.Normalize()
	seenDiagnostics := make(map[ExtractionDiagnostic]struct{})
	repairer, canRepair := p.extractor.(ExtractionRepairProvider)
	repairUsed := false
	repairFailureKind := ""

	chunks := view.Chunks(p.chunkRunes)
	if len(chunks) == 0 {
		chunks = []source.Chunk{{}}
	}
	outcome.Stats.ExtractionChunks = len(chunks)

	var (
		collected       []Candidate
		rawTotal        int
		chunkFailures   int
		contextDropped  int
		crossDuplicates int
		limitOverflow   int
	)
	for index, chunk := range chunks {
		request := ExtractionRequest{Source: src}
		if len(chunks) > 1 {
			request.Segments = chunk.Segments
			request.ContextSegments = chunk.ContextSegments
			request.ChunkIndex = index + 1
			request.ChunkCount = len(chunks)
		}
		callStarted := time.Now()
		extracted, usage, callErr := p.extractChunk(ctx, request)
		outcome.Timings.ExtractionMillis += time.Since(callStarted).Milliseconds()
		outcome.Usage.Add(usage)
		if callErr != nil {
			// A structural protocol failure may be repaired once for this
			// window. Other chunks keep their already-safe output.
			var providerErr *ProviderError
			if canRepair && !repairUsed && errors.As(callErr, &providerErr) && providerErr.Operation == "protocol" {
				repairUsed = true
				outcome.Stats.RepairAttempts++
				repairRequest := ExtractionRepairRequest{
					Source: src, Retry: true,
					Segments: request.Segments, ContextSegments: request.ContextSegments,
					ChunkIndex: request.ChunkIndex, ChunkCount: request.ChunkCount,
					Issues: []ExtractionIssue{{Code: "invalid_structure", Class: IssueRepairable,
						Message: "return a JSON object with a candidates array"}},
				}
				repairStarted := time.Now()
				repaired, repairErr := p.repairExtraction(ctx, repairer, repairRequest)
				outcome.Timings.RepairMillis += time.Since(repairStarted).Milliseconds()
				outcome.Usage.Add(repaired.Usage)
				repairFailureStats(&outcome.Stats, repairErr)
				if repairErr == nil {
					extracted = repaired
					callErr = nil
				} else {
					repairFailureKind = failureKind(repairErr)
					appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
						"", "candidates", repairFailureCode(repairErr), "repair", "isolated",
					))
					callErr = repairErr
				}
			}
			if callErr != nil {
				if failureKind(callErr) != "protocol" {
					return outcome, callErr
				}
				if len(chunks) == 1 {
					kind := repairFailureKind
					if kind == "" {
						kind = "protocol"
					}
					return unusableExtraction(outcome, kind, callErr)
				}
				chunkFailures++
				outcome.Retention.addNotRetained(RetentionNotChunkFailure, 1)
				appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
					"", "chunk", "extraction_chunk_failed", "extraction", "isolated",
				))
				continue
			}
		}

		chunkRaw := extracted.RawCandidates
		if chunkRaw < len(extracted.Candidates) {
			chunkRaw = len(extracted.Candidates)
		}
		rawTotal += chunkRaw
		if len(chunks) > 1 {
			filtered, dropped := filterChunkCandidates(extracted.Candidates, chunk.Segments, chunk.ContextSegments)
			contextDropped += dropped
			extracted.Candidates = filtered
			if chunk.Oversized {
				outcome.Warnings = append(outcome.Warnings, WarningExtractionChunkOversized)
				appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
					"", "chunk", "extraction_chunk_oversized", "extraction", "accepted",
				))
			}
		}
		if len(chunks) > 1 {
			for _, candidate := range extracted.Candidates {
				if mergeChunkDuplicate(collected, candidate) {
					crossDuplicates++
					continue
				}
				collected = append(collected, candidate)
			}
		} else {
			collected = append(collected, extracted.Candidates...)
		}
	}
	outcome.Stats.ExtractionChunkFailures = chunkFailures
	if chunkFailures > 0 {
		outcome.Warnings = append(outcome.Warnings, WarningExtractionChunkFailed)
	}
	outcome.Stats.ContextOnlyCandidates = contextDropped
	outcome.Stats.CrossChunkDuplicates = crossDuplicates
	outcome.Retention.addNotRetained(RetentionNotCrossChunkContext, contextDropped)
	outcome.Retention.addNotRetained(RetentionNotCrossChunkDuplicate, crossDuplicates)
	if len(chunks) > 1 && chunkFailures == len(chunks) {
		return unusableExtraction(outcome, "protocol", ErrProtocol)
	}

	extracted := ExtractionResult{Candidates: collected, RawCandidates: rawTotal}
	outcome.Stats.RawCandidates = rawTotal
	if outcome.Stats.RawCandidates < len(extracted.Candidates) {
		outcome.Stats.RawCandidates = len(extracted.Candidates)
	}
	outcome.Stats.ParsedCandidates = len(extracted.Candidates)
	if outcome.Stats.ParsedCandidates == 0 {
		outcome.Retention.addNotRetained(RetentionNotEmptyExtraction, 1)
	}

	localStarted := time.Now()
	defer func() {
		outcome.Timings.NormalizationMillis += time.Since(localStarted).Milliseconds()
	}()

	original := prepareExtractionCandidates(extracted.Candidates)
	if len(original) > maxCandidatesPerSource {
		limitOverflow = len(original) - maxCandidatesPerSource
		for _, candidate := range original[maxCandidatesPerSource:] {
			appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
				candidate.CandidateID, "candidates", "candidate_limit_exceeded", "extraction", "isolated",
			))
		}
		original = original[:maxCandidatesPerSource]
		outcome.Stats.DroppedCandidates += limitOverflow
		outcome.Retention.addNotRetained(RetentionNotCandidateLimit, limitOverflow)
	}
	normalizeExtractionMetadata(original, src.CreatedAt)
	issues := validateExtraction(view, src, original)
	outcome.Stats.NormalizedCandidates, outcome.Stats.Normalizations = countNormalized(original)
	appendNormalizationDiagnostics(&outcome.Diagnostics, seenDiagnostics, original)

	// Explicit privacy exclusions are not repairable. Do not let a second
	// model response resurrect them by omitting their exclusion evidence.
	excluded := make(map[string]struct{})
	for _, issue := range issues {
		if issue.Code == "explicit_exclusion" {
			excluded[issue.CandidateID] = struct{}{}
		}
	}
	outcome.Stats.ExcludedCandidates = len(excluded)
	outcome.Retention.addNotRetained(RetentionNotExplicitExclusion, len(excluded))
	if len(excluded) > 0 {
		outcome.Warnings = append(outcome.Warnings, WarningExtractionExcluded)
	}
	eligible := make([]Candidate, 0, len(original))
	for _, candidate := range original {
		if _, skip := excluded[candidate.CandidateID]; !skip {
			eligible = append(eligible, candidate)
		}
	}
	for _, issue := range issues {
		if _, skip := excluded[issue.CandidateID]; skip {
			appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
				issue.CandidateID, issue.Field, issue.Code, "validate", "excluded",
			))
		}
	}
	repairIssues := make([]ExtractionIssue, 0, len(issues))
	for _, issue := range issues {
		if _, skip := excluded[issue.CandidateID]; skip {
			continue
		}
		// Semantic quality doubts are recorded and checked, but they are not
		// protocol failures and do not consume the single repair budget.
		if issue.Class == IssueQuality {
			appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
				issue.CandidateID, issue.Field, issue.Code, "validate", "accepted",
			))
			continue
		}
		repairIssues = append(repairIssues, issue)
	}
	if len(repairIssues) == 0 {
		outcome.Stats.IsolatedCandidates = limitOverflow
		outcome.Candidates = eligible
		outcome.Stats.AcceptedCandidates = len(eligible)
		outcome.Stats.NormalizedCandidates, outcome.Stats.Normalizations = countNormalized(eligible)
		finalizeDiagnosticDispositions(outcome.Diagnostics, candidateIDSet(eligible), nil, excluded, nil)
		addRetentionReasons(&outcome.Retention, eligible, p.now().UTC())
		outcome.quality = outcome.finalQuality()
		return prepareExtractionOutcome(outcome), nil
	}
	invalid := issueIDs(repairIssues)
	current := eligible
	replaced := make(map[string][]string)
	repairAttempted := repairUsed
	if canRepair && !repairUsed {
		repairAttempted = true
		repairUsed = true
		batch := make([]Candidate, 0, len(invalid))
		for _, candidate := range eligible {
			if _, bad := invalid[candidate.CandidateID]; bad {
				batch = append(batch, candidate)
			}
		}
		outcome.Stats.RepairAttempts++
		repairRequest := ExtractionRepairRequest{
			Source: src, Candidates: batch, Issues: repairIssues,
			Retry: len(batch) >= 3 && len(batch)*2 >= len(eligible),
		}
		if len(chunks) > 1 {
			repairRequest.Segments = evidenceSegments(view, batch)
		}
		repairStarted := time.Now()
		repaired, repairErr := p.repairExtraction(ctx, repairer, repairRequest)
		outcome.Timings.RepairMillis += time.Since(repairStarted).Milliseconds()
		outcome.Usage.Add(repaired.Usage)
		repairFailureStats(&outcome.Stats, repairErr)
		if repairErr == nil {
			current, replaced = mergeExtractionRepair(eligible, repaired.Candidates, repairIssues)
			for _, children := range replaced {
				outcome.Stats.RepairedCandidates += len(children)
			}
		} else {
			repairFailureKind = failureKind(repairErr)
			code := repairFailureCode(repairErr)
			for _, candidate := range batch {
				appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
					candidate.CandidateID, "", code, "repair", "isolated",
				))
			}
		}
		for _, issue := range repairIssues {
			disposition := "isolated"
			if len(replaced[issue.CandidateID]) > 0 {
				disposition = "repaired"
			}
			appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
				issue.CandidateID, issue.Field, issue.Code, "repair", disposition,
			))
		}
	}
	if ctx.Err() != nil {
		return outcome, ctx.Err()
	}
	normalizeExtractionMetadata(current, src.CreatedAt)
	remainingIssues := validateExtraction(view, src, current)
	outcome.Stats.NormalizedCandidates, outcome.Stats.Normalizations = countNormalized(current)
	appendNormalizationDiagnostics(&outcome.Diagnostics, seenDiagnostics, current)
	blocking := blockingIssueIDs(remainingIssues)
	accepted := make([]Candidate, 0, len(current))
	isolatedIDs := make(map[string]struct{})
	for _, candidate := range current {
		if _, bad := blocking[candidate.CandidateID]; bad {
			isolatedIDs[candidate.CandidateID] = struct{}{}
			continue
		}
		accepted = append(accepted, candidate)
	}
	for _, issue := range remainingIssues {
		disposition := "accepted"
		if _, bad := blocking[issue.CandidateID]; bad {
			disposition = "isolated"
		}
		appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
			issue.CandidateID, issue.Field, issue.Code, "validate", disposition,
		))
	}
	// A parent that received no replacement is an isolated original candidate,
	// not a silently dropped one. A parent still present in current is counted
	// by the final validation pass instead.
	for _, id := range orderedKeys(invalid) {
		present := false
		for _, candidate := range current {
			if candidate.CandidateID == id || strings.HasPrefix(candidate.CandidateID, id+".") {
				present = true
				break
			}
		}
		if present {
			continue
		}
		isolatedIDs[id] = struct{}{}
		if !repairAttempted {
			appendDiagnostic(&outcome.Diagnostics, seenDiagnostics, extractionDiagnostic(
				id, "", "repair_unavailable", "repair", "isolated",
			))
		}
	}
	outcome.Stats.AcceptedCandidates = len(accepted)
	outcome.Stats.IsolatedCandidates = len(isolatedIDs) + limitOverflow
	outcome.Stats.DroppedCandidates += len(isolatedIDs)
	outcome.Retention.addNotRetained(RetentionNotInvalidCandidate, len(isolatedIDs))
	if len(isolatedIDs) > 0 {
		outcome.Warnings = append(outcome.Warnings, WarningExtractionIsolated)
	}
	repairedIDs := make(map[string]struct{})
	for id, children := range replaced {
		if len(children) > 0 {
			repairedIDs[id] = struct{}{}
		}
	}
	finalizeDiagnosticDispositions(outcome.Diagnostics, candidateIDSet(accepted), isolatedIDs, excluded, repairedIDs)
	addRetentionReasons(&outcome.Retention, accepted, p.now().UTC())
	if len(accepted) == 0 {
		if outcome.Stats.ParsedCandidates > 0 && outcome.Stats.ExcludedCandidates >= outcome.Stats.ParsedCandidates {
			outcome.Candidates = []Candidate{}
			outcome.quality = QualityComplete
			return prepareExtractionOutcome(outcome), nil
		}
		kind := repairFailureKind
		if kind == "" {
			kind = "protocol"
		}
		return unusableExtraction(outcome, kind, ErrProtocol)
	}
	outcome.Candidates = accepted
	outcome.quality = outcome.finalQuality()
	return prepareExtractionOutcome(outcome), nil
}

func (p *Processor) extractChunk(ctx context.Context, request ExtractionRequest) (ExtractionResult, ModelUsage, error) {
	started := time.Now()
	result, err := modelCall(ctx, p.extractionTimeout, func(callCtx context.Context) (ExtractionResult, error) {
		return p.extractor.Extract(callCtx, request)
	})
	p.logModelCall("extraction", 1, extractionRequestSize(request), started, result.Usage, err)
	return result, result.Usage, err
}

func extractionRequestSize(request ExtractionRequest) int {
	if len(request.Segments) == 0 && len(request.ContextSegments) == 0 {
		return len(request.Source.Content)
	}
	size := 0
	for _, segment := range request.Segments {
		size += len(segment.Text)
	}
	for _, segment := range request.ContextSegments {
		size += len(segment.Text)
	}
	return size
}

func filterChunkCandidates(candidates []Candidate, current, contextSegments []source.Segment) ([]Candidate, int) {
	if len(contextSegments) == 0 {
		return candidates, 0
	}
	currentIDs := make(map[string]struct{}, len(current))
	for _, segment := range current {
		currentIDs[segment.ID] = struct{}{}
	}
	contextIDs := make(map[string]struct{}, len(contextSegments))
	for _, segment := range contextSegments {
		contextIDs[segment.ID] = struct{}{}
	}
	result := make([]Candidate, 0, len(candidates))
	dropped := 0
	for _, candidate := range candidates {
		filtered := make([]source.EvidenceReference, 0, len(candidate.Evidence))
		for _, evidence := range candidate.Evidence {
			inContext := containsSegment(contextIDs, evidence.SegmentID)
			inCurrent := containsSegment(currentIDs, evidence.SegmentID)
			switch {
			case evidence.SegmentID != "" && inContext && !inCurrent:
				continue
			case evidence.SegmentID == "" && !quoteInSegments(evidence.Quote, current) && quoteInSegments(evidence.Quote, contextSegments):
				continue
			default:
				filtered = append(filtered, evidence)
			}
		}
		if len(candidate.Evidence) > 0 && len(filtered) == 0 {
			dropped++
			continue
		}
		candidate.Evidence = filtered
		result = append(result, candidate)
	}
	return result, dropped
}

func quoteInSegments(quote string, segments []source.Segment) bool {
	if strings.TrimSpace(quote) == "" {
		return false
	}
	for _, segment := range segments {
		if strings.Contains(segment.Text, quote) {
			return true
		}
	}
	return false
}

func containsSegment(values map[string]struct{}, id string) bool {
	_, ok := values[id]
	return ok
}

func mergeChunkDuplicate(existing []Candidate, candidate Candidate) bool {
	key := candidateDedupKey(candidate)
	for index := range existing {
		if candidateDedupKey(existing[index]) != key {
			continue
		}
		existing[index].Evidence = mergeEvidenceReferences(existing[index].Evidence, candidate.Evidence)
		return true
	}
	return false
}

func candidateDedupKey(candidate Candidate) string {
	return strings.Join([]string{
		normalizeComparable(candidate.Content),
		string(candidate.Kind),
		string(candidate.Phase),
		timeKey(candidate.EventTime),
		timeKey(candidate.ValidFrom),
		timeKey(candidate.ValidTo),
		candidateGroupKey(candidate.FactGroup),
	}, "\x00")
}

func candidateGroupKey(group *FactGroupProposal) string {
	if group == nil {
		return ""
	}
	return strings.Join([]string{
		normalizeComparable(group.Subject), normalizeComparable(group.Predicate),
		normalizeComparable(group.Scope), strings.Join(group.Conditions, "|"),
	}, "\x00")
}

func timeKey(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func mergeEvidenceReferences(left, right []source.EvidenceReference) []source.EvidenceReference {
	seen := make(map[string]struct{}, len(left))
	for _, item := range left {
		seen[evidenceReferenceKey(item)] = struct{}{}
	}
	for _, item := range right {
		key := evidenceReferenceKey(item)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		left = append(left, item)
	}
	return left
}

func evidenceReferenceKey(item source.EvidenceReference) string {
	start, end := -1, -1
	if item.Start != nil {
		start = *item.Start
	}
	if item.End != nil {
		end = *item.End
	}
	return strings.Join([]string{item.SegmentID, item.Quote, strconv.Itoa(start), strconv.Itoa(end)}, "\x00")
}

func evidenceSegments(view source.View, candidates []Candidate) []source.Segment {
	ids := make(map[string]struct{})
	for _, candidate := range candidates {
		for _, evidence := range candidate.Evidence {
			if evidence.SegmentID != "" {
				ids[evidence.SegmentID] = struct{}{}
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	result := make([]source.Segment, 0, len(ids))
	for _, segment := range view.Segments {
		if _, ok := ids[segment.ID]; ok {
			result = append(result, segment)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func addRetentionReasons(stats *RetentionStats, candidates []Candidate, now time.Time) {
	for _, candidate := range candidates {
		reason := memory.ClassifyRetention(candidate.Kind, candidate.Phase, candidate.Durability,
			candidate.EventTime, candidate.ValidTo, now)
		stats.addReason(reason)
	}
}

func queueAgeMillis(job Job, now time.Time) int64 {
	if job.CreatedAt.IsZero() || now.Before(job.CreatedAt) {
		return 0
	}
	return now.Sub(job.CreatedAt).Milliseconds()
}

func unusableExtraction(outcome extractionOutcome, kind string, err error) (extractionOutcome, error) {
	outcome.Candidates = []Candidate{}
	if len(outcome.Diagnostics) == 0 {
		outcome.Diagnostics = append(outcome.Diagnostics, extractionDiagnostic("", "candidates", "invalid_structure", "extraction", "isolated"))
	}
	quality := QualityUnusable
	if kind != "protocol" {
		quality = ""
	}
	outcome.Retention.Normalize()
	return outcome, &ExtractionQualityError{
		Kind: kind, Quality: quality, Stats: outcome.Stats, Retention: outcome.Retention,
		Timings: outcome.Timings, ModelUsage: outcome.Usage,
		Diagnostics: outcome.Diagnostics, Err: err,
	}
}

func prepareExtractionOutcome(outcome extractionOutcome) extractionOutcome {
	outcome.prepare()
	return outcome
}

func mergeExtractionRepair(original, repaired []Candidate, issues []ExtractionIssue) ([]Candidate, map[string][]string) {
	invalid := issueIDs(issues)
	result := make([]Candidate, 0, len(original)+len(repaired))
	replaced := make(map[string][]string, len(invalid))
	for _, candidate := range original {
		if _, bad := invalid[candidate.CandidateID]; !bad {
			result = append(result, candidate)
			continue
		}
		seen := make(map[string]struct{})
		for _, replacement := range repaired {
			id := replacement.CandidateID
			if !repairChildID(candidate.CandidateID, id) {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				replacement.ProtocolIssues = append(replacement.ProtocolIssues, FieldIssue{Field: "candidate_id", Code: "duplicate_candidate_id"})
			}
			seen[id] = struct{}{}
			replaced[candidate.CandidateID] = append(replaced[candidate.CandidateID], id)
			result = append(result, replacement)
		}
	}
	// A repair response that does not preserve candidate ids cannot safely be
	// associated with the invalid source candidate, so it is ignored.
	return result, replaced
}

func repairChildID(parent, id string) bool {
	if id == parent {
		return true
	}
	suffix, ok := strings.CutPrefix(id, parent+".")
	if !ok || suffix == "" {
		return false
	}
	number, err := strconv.Atoi(suffix)
	return err == nil && number > 0 && strconv.Itoa(number) == suffix
}

func uniqueWarnings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// buildPlan validates extraction output and creates the deterministic subset of
// actions that needs no decision model call.
func (p *Processor) buildPlan(ctx context.Context, src source.Source, candidates []Candidate) (*mergePlan, error) {
	plan := &mergePlan{
		source:               src,
		candidates:           make(map[string]Candidate, len(candidates)),
		related:              make(map[string]memory.Memory),
		groups:               make(map[string]memory.FactGroup),
		actions:              make([]plannedAction, 0, len(candidates)),
		complete:             true,
		decided:              make(map[string]struct{}, len(candidates)),
		incompleteCandidates: make(map[string]struct{}),
		incompleteGroups:     make(map[string]struct{}),
		convergenceReasons:   make(map[string]ConvergenceDiagnostic),
	}
	for _, candidate := range candidates {
		plan.candidates[candidate.CandidateID] = candidate
	}
	if len(candidates) == 0 {
		return plan, nil
	}
	if len(candidates) == 1 {
		context, err := p.related.RelatedContext(ctx, src.ScopeKey, candidateQuery(candidates[0]), maxInitialContext)
		if err != nil {
			return nil, fmt.Errorf("选择相关事实: %w", err)
		}
		mergeMemories(plan.related, context.Memories)
		plan.warnings = append(plan.warnings, context.Warnings...)
		if !context.Complete {
			plan.complete = false
			plan.markIncompleteCandidate(candidates[0].CandidateID)
		}
		groups, err := p.identityGroups(ctx, src, candidates[0])
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			plan.groups[group.ID] = group
		}
		if duplicate := exactDuplicate(context.Memories, candidates[0]); duplicate != nil {
			plan.actions = append(plan.actions, plannedAction{
				candidate: candidates[0],
				decision: Decision{
					CandidateID:     candidates[0].CandidateID,
					Action:          ActionMergeEvidence,
					TargetMemoryIDs: []string{duplicate.ID},
				},
				evidence: candidates[0].SourceEvidence,
			})
			plan.decided[candidates[0].CandidateID] = struct{}{}
			return plan, nil
		}
		if len(context.Memories) == 0 && len(groups) == 0 {
			plan.actions = append(plan.actions, plannedAction{
				candidate: candidates[0],
				decision: Decision{
					CandidateID: candidates[0].CandidateID,
					Action:      ActionAdd,
				},
				evidence: candidates[0].SourceEvidence,
			})
			plan.decided[candidates[0].CandidateID] = struct{}{}
		}
	}
	return plan, nil
}

// identityGroups performs the scoped identity-first lookup that does not depend
// on whether the new wording happens to share search terms with the old value.
func (p *Processor) identityGroups(ctx context.Context, src source.Source, candidate Candidate) ([]memory.FactGroup, error) {
	identity, ok := candidateGroupIdentity(src.ScopeKey, candidate)
	if !ok {
		return nil, nil
	}
	reader, _ := p.sources.(FactGroupReader)
	if reader == nil {
		return nil, nil
	}
	groups, err := reader.FindFactGroups(ctx, identity.ScopeKey, identity.Subject, identity.Predicate)
	if err != nil {
		return nil, fmt.Errorf("按身份读取事实组: %w", err)
	}
	return groups, nil
}

// loadContext selects bounded related facts, identity-based group timelines,
// groups and open conflicts for every candidate that still needs a decision.
// Completeness is tracked per candidate and per group so one truncated
// unrelated result cannot downgrade a safe update elsewhere in the source.
func (p *Processor) loadContext(ctx context.Context, plan *mergePlan) error {
	if len(plan.actions) > 0 {
		return nil
	}
	reader, _ := p.sources.(FactGroupReader)
	for _, candidate := range sortedCandidates(plan) {
		context, err := p.related.RelatedContext(ctx, plan.source.ScopeKey, candidateQuery(candidate), maxInitialContext)
		if err != nil {
			return fmt.Errorf("选择相关事实: %w", err)
		}
		mergeMemories(plan.related, context.Memories)
		plan.warnings = append(plan.warnings, context.Warnings...)
		if !context.Complete {
			plan.complete = false
			plan.markIncompleteCandidate(candidate.CandidateID)
		}
		groups, err := p.identityGroups(ctx, plan.source, candidate)
		if err != nil {
			return err
		}
		for _, group := range groups {
			plan.groups[group.ID] = group
		}
	}
	// Add group timelines and metadata for every discovered identity or related
	// group. Identity groups are loaded even when keyword recall returned no
	// member, so a differently worded new value can still see its predecessor.
	groupIDs := make(map[string]struct{})
	for _, item := range plan.related {
		if item.FactGroupID != "" {
			groupIDs[item.FactGroupID] = struct{}{}
		}
	}
	for groupID := range plan.groups {
		groupIDs[groupID] = struct{}{}
	}
	for groupID := range groupIDs {
		timeline, err := p.related.FactTimeline(ctx, groupID, maxTimelineContext)
		if err != nil {
			return fmt.Errorf("读取事实组时间线: %w", err)
		}
		mergeMemories(plan.related, timeline.Memories)
		if !timeline.Complete {
			plan.complete = false
			plan.markIncompleteGroup(groupID)
		}
		if reader != nil {
			group, err := reader.GetFactGroup(ctx, groupID)
			if err == nil {
				plan.groups[group.ID] = group
			} else if !errors.Is(err, memory.ErrNotFound) {
				return err
			}
		}
	}
	memoryIDs := make([]string, 0, len(plan.related))
	for id := range plan.related {
		memoryIDs = append(memoryIDs, id)
	}
	conflicts, err := p.related.OpenConflictsForMemories(ctx, memoryIDs)
	if err != nil {
		return fmt.Errorf("读取未决冲突: %w", err)
	}
	plan.conflicts = conflicts

	// Conflict members may live outside the initial keyword hits. Load them and
	// their group timelines so one decision batch can see all involved facts.
	extraIDs := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		for _, member := range conflict.Members {
			if _, exists := plan.related[member.MemoryID]; !exists {
				extraIDs = append(extraIDs, member.MemoryID)
			}
		}
	}
	if len(extraIDs) > 0 {
		extra, err := p.related.MemoriesByIDs(ctx, extraIDs)
		if err != nil {
			return fmt.Errorf("读取冲突成员: %w", err)
		}
		extraGroups := make(map[string]struct{})
		for _, item := range extra {
			mergeMemories(plan.related, []memory.Memory{item})
			if item.FactGroupID != "" {
				extraGroups[item.FactGroupID] = struct{}{}
			}
		}
		for groupID := range extraGroups {
			if _, loaded := plan.groups[groupID]; loaded {
				continue
			}
			timeline, err := p.related.FactTimeline(ctx, groupID, maxTimelineContext)
			if err != nil {
				return fmt.Errorf("读取冲突组时间线: %w", err)
			}
			mergeMemories(plan.related, timeline.Memories)
			if !timeline.Complete {
				plan.complete = false
				plan.markIncompleteGroup(groupID)
			}
			if reader != nil {
				group, err := reader.GetFactGroup(ctx, groupID)
				if err == nil {
					plan.groups[group.ID] = group
				} else if !errors.Is(err, memory.ErrNotFound) {
					return err
				}
			}
		}
	}
	// Explicit, well-scoped replacements are deterministic. Mark them as add
	// actions so the transaction performs the same guarded convergence check;
	// ambiguous candidates continue to the decision model below.
	for _, candidate := range sortedCandidates(plan) {
		if _, done := plan.decided[candidate.CandidateID]; done {
			continue
		}
		action, ok, reason := planConvergence(plan, candidate)
		if ok {
			plan.actions = append(plan.actions, action)
			plan.decided[candidate.CandidateID] = struct{}{}
			continue
		}
		if mergeAction, ok := planEvidenceMerge(plan, candidate); ok {
			plan.actions = append(plan.actions, mergeAction)
			plan.decided[candidate.CandidateID] = struct{}{}
			continue
		}
		if reason != "" {
			if group, _ := candidateGroup(plan.groups, plan.source.ScopeKey, candidate); group != nil {
				plan.recordConvergence(candidate.CandidateID, group.ID, reason)
			} else {
				plan.recordConvergence(candidate.CandidateID, "", reason)
			}
		}
	}
	planDeterministicAdds(plan)
	return nil
}

// planDeterministicAdds avoids a decision call only when the service can prove
// that every remaining candidate has a complete scoped identity and that no
// related memory, fact group or conflict exists. Empty keyword recall alone is
// not treated as proof, so ungrouped candidates still use the decision model.
func planDeterministicAdds(plan *mergePlan) {
	if plan == nil || !plan.complete || len(plan.actions) > 0 ||
		len(plan.related) > 0 || len(plan.groups) > 0 || len(plan.conflicts) > 0 {
		return
	}
	pending := make([]Candidate, 0, len(plan.candidates))
	identities := make(map[string]struct{}, len(plan.candidates))
	for _, candidate := range sortedCandidates(plan) {
		if _, done := plan.decided[candidate.CandidateID]; done {
			continue
		}
		identity, ok := candidateGroupIdentity(plan.source.ScopeKey, candidate)
		if !ok {
			return
		}
		key := fmt.Sprintf("%v", memory.FactGroupKey(identity))
		if _, duplicate := identities[key]; duplicate {
			return
		}
		identities[key] = struct{}{}
		pending = append(pending, candidate)
	}
	if len(pending) <= 1 {
		return
	}
	for _, candidate := range pending {
		plan.actions = append(plan.actions, plannedAction{
			candidate: candidate,
			decision:  Decision{CandidateID: candidate.CandidateID, Action: ActionAdd},
			evidence:  candidate.SourceEvidence,
		})
		plan.decided[candidate.CandidateID] = struct{}{}
	}
}

// decide calls the decision provider in fixed batches of at most eight and
// returns the aggregate token usage.
func (p *Processor) decide(ctx context.Context, plan *mergePlan) (ModelUsage, error) {
	var usage ModelUsage
	if len(plan.candidates) == 0 {
		return usage, nil
	}
	pending := make([]Candidate, 0, len(plan.candidates))
	for _, candidate := range plan.candidates {
		if _, done := plan.decided[candidate.CandidateID]; done {
			continue
		}
		pending = append(pending, candidate)
	}
	if len(pending) == 0 {
		return usage, nil
	}
	if p.decider == nil {
		return usage, &ProviderError{Operation: "decision", Retryable: false, Err: errors.New("决策模型未配置")}
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].CandidateID < pending[j].CandidateID })
	related := mapValues(plan.related)
	groups := mapGroupValues(plan.groups)
	sourceView := source.BuildView(plan.source.Content)
	for start := 0; start < len(pending); start += maxDecisionBatch {
		end := start + maxDecisionBatch
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		request := DecisionRequest{
			Source:                 plan.source,
			Candidates:             batch,
			RelatedMemories:        related,
			FactGroups:             groups,
			Conflicts:              plan.conflicts,
			SourceSegments:         evidenceSegments(sourceView, batch),
			ContextComplete:        plan.complete && len(plan.incompleteCandidates) == 0 && len(plan.incompleteGroups) == 0,
			IncompleteCandidateIDs: orderedKeys(plan.incompleteCandidates),
			IncompleteFactGroupIDs: orderedKeys(plan.incompleteGroups),
		}
		decisions, callUsage, err := p.decideValidated(ctx, request)
		usage.Add(callUsage)
		if err != nil {
			return usage, err
		}
		for _, decision := range decisions {
			candidate := plan.candidates[decision.CandidateID]
			evidence, err := decisionEvidence(candidate, decision, plan.candidates)
			if err != nil {
				return usage, err
			}
			if !plan.decisionContextComplete(decision) {
				switch decision.Action {
				case ActionChange, ActionCorrect, ActionEnrich, ActionMergeEvidence:
					decision.Action = ActionUnresolved
					decision.TargetMemoryIDs = nil
					decision.ReasonCode = string(memory.ConflictComplexTimeline)
				}
				plan.recordConvergence(decision.CandidateID, decision.TargetGroupID, ConvergenceContextTruncated)
				plan.warnings = append(plan.warnings, WarningContextTruncated)
			}
			plan.actions = append(plan.actions, plannedAction{
				candidate: candidate,
				decision:  decision,
				evidence:  evidence,
			})
		}
	}
	return usage, nil
}

func candidateQuery(candidate Candidate) string {
	parts := []string{candidate.Content}
	if candidate.FactGroup != nil {
		parts = append(parts, candidate.FactGroup.Subject, candidate.FactGroup.Predicate, candidate.FactGroup.Scope)
		parts = append(parts, candidate.FactGroup.Conditions...)
	}
	return strings.Join(parts, " ")
}

func prepareExtractionCandidates(candidates []Candidate) []Candidate {
	result := make([]Candidate, 0, len(candidates))
	for index, candidate := range candidates {
		candidate.CandidateID = fmt.Sprintf("c%d", index+1)
		result = append(result, candidate)
	}
	return result
}

func normalizeCandidates(src source.Source, view source.View, candidates []Candidate, modelName string, now time.Time) []Candidate {
	fallbackObserved := src.CreatedAt
	if fallbackObserved.IsZero() {
		fallbackObserved = now
	}
	fallbackObserved = fallbackObserved.UTC()
	result := make([]Candidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for index, candidate := range candidates {
		content := strings.TrimSpace(candidate.Content)
		if content == "" {
			continue
		}
		if candidate.CandidateID == "" {
			candidate.CandidateID = fmt.Sprintf("c%d", index+1)
		}
		if _, duplicate := seen[candidate.CandidateID]; duplicate {
			continue
		}
		seen[candidate.CandidateID] = struct{}{}
		candidate.Content = content
		candidate.Kind = normalizeKind(candidate.Kind)
		candidate.Title = strings.TrimSpace(candidate.Title)
		if candidate.Title == "" {
			candidate.Title = defaultTitle(candidate.Content)
		}
		candidate.ValidFrom = memory.UTCPointer(candidate.ValidFrom)
		candidate.ValidTo = memory.UTCPointer(candidate.ValidTo)
		candidate.EventTime = memory.UTCPointer(candidate.EventTime)
		observedFromContext := false
		observedContextText := ""
		if candidate.ObservedAt == nil || candidate.ObservedAt.IsZero() {
			if contextValue, contextText, ok := evidenceObservationTime(view, candidate.Evidence); ok {
				observed := contextValue.UTC()
				candidate.ObservedAt = &observed
				candidate.ObservedAtBasis = memory.BasisInferred
				candidate.ObservedAtPrecision = memory.PrecisionSecond
				observedFromContext = true
				observedContextText = contextText
			} else {
				observed := fallbackObserved
				candidate.ObservedAt = &observed
				candidate.ObservedAtBasis = memory.BasisRecorded
			}
		} else {
			observed := candidate.ObservedAt.UTC()
			candidate.ObservedAt = &observed
			if candidate.ObservedAtBasis == "" || candidate.ObservedAtBasis == memory.BasisUnknown {
				candidate.ObservedAtBasis = memory.BasisInferred
			}
		}
		if candidate.EventTime != nil {
			if candidate.EventTimeBasis == "" || candidate.EventTimeBasis == memory.BasisUnknown {
				candidate.EventTimeBasis = memory.BasisInferred
			}
			if candidate.EventTimePrecision == "" || candidate.EventTimePrecision == memory.PrecisionUnknown {
				candidate.EventTimePrecision = inferPrecision(*candidate.EventTime)
			}
		}
		if candidate.FactGroup == nil {
			candidate.FactGroup = &FactGroupProposal{}
		}
		if len(candidate.Topics) > 0 {
			candidate.Topics = topic.Raw(candidate.Topics)
		}
		candidate.SourceEvidence = buildServiceEvidence(src, view, candidate.Evidence, modelName, now)
		candidate.Temporal = candidateTemporalDetails(view, &candidate)
		switch {
		case observedFromContext:
			candidate.Temporal.Observed = memory.TemporalDetail{
				Text: observedContextText, Precision: memory.PrecisionSecond, TimezoneKnown: true,
			}
		case candidate.ObservedAtBasis == memory.BasisRecorded:
			candidate.Temporal.Observed = memory.TemporalDetail{
				Precision: memory.PrecisionSecond, TimezoneKnown: true,
			}
		}
		normalizeTemporalValues(&candidate)
		result = append(result, candidate)
	}
	return result
}

func evidenceObservationTime(view source.View, references []source.EvidenceReference) (time.Time, string, bool) {
	for _, reference := range references {
		resolved, err := view.Resolve(reference)
		if err != nil {
			continue
		}
		if value, ok := temporal.ContextTime(resolved.Segment.OccurredAt); ok {
			return value, resolved.Segment.OccurredAt, true
		}
	}
	return time.Time{}, "", false
}

func candidateTemporalDetails(view source.View, candidate *Candidate) memory.TemporalDetails {
	quotes := make([]string, 0, len(candidate.Evidence))
	for _, reference := range candidate.Evidence {
		if strings.TrimSpace(reference.Quote) != "" {
			quotes = append(quotes, reference.Quote)
		}
	}
	text := strings.Join(quotes, "\n")
	details := memory.TemporalDetails{}
	if candidate.ObservedAt != nil {
		details.Observed = temporalDetail(text, *candidate.ObservedAt, candidate.ObservedAtPrecision)
	}
	if candidate.EventTime != nil {
		details.Event = temporalDetail(text, *candidate.EventTime, candidate.EventTimePrecision)
	}
	if candidate.ValidFrom != nil {
		details.ValidFrom = temporalDetail(text, *candidate.ValidFrom, candidate.ValidFromPrecision)
	}
	if candidate.ValidTo != nil {
		details.ValidTo = temporalDetail(text, *candidate.ValidTo, candidate.ValidToPrecision)
	}
	return details
}

func temporalDetail(text string, value time.Time, precision memory.TemporalPrecision) memory.TemporalDetail {
	if mention, ok := temporal.FindMention(text, value, precision); ok {
		return memory.TemporalDetail{
			Text: mention.Text, Precision: mention.Precision, TimezoneKnown: mention.TimezoneKnown,
		}
	}
	if precision == "" || precision == memory.PrecisionUnknown {
		precision = inferPrecision(value)
	}
	if relative := temporal.RelativeText(text); relative != "" {
		return memory.TemporalDetail{Text: relative, Precision: precision}
	}
	return memory.TemporalDetail{Precision: precision}
}

// normalizeTemporalValues aligns a stored instant with the source-stated
// precision. A date without a timezone is represented at the UTC day boundary
// with timezone_known=false; it never keeps an invented time-of-day.
func normalizeTemporalValues(candidate *Candidate) {
	if candidate.ValidFrom != nil {
		value := normalizedTemporalInstant(*candidate.ValidFrom, candidate.Temporal.ValidFrom)
		candidate.ValidFrom = &value
	}
	if candidate.ValidTo != nil {
		value := normalizedTemporalInstant(*candidate.ValidTo, candidate.Temporal.ValidTo)
		candidate.ValidTo = &value
	}
	if candidate.EventTime != nil {
		value := normalizedTemporalInstant(*candidate.EventTime, candidate.Temporal.Event)
		candidate.EventTime = &value
		if candidate.Temporal.Event.Precision != "" && candidate.Temporal.Event.Precision != memory.PrecisionUnknown {
			candidate.EventTimePrecision = candidate.Temporal.Event.Precision
		}
	}
	if candidate.ObservedAt != nil {
		value := normalizedTemporalInstant(*candidate.ObservedAt, candidate.Temporal.Observed)
		candidate.ObservedAt = &value
	}
}

func normalizedTemporalInstant(value time.Time, detail memory.TemporalDetail) time.Time {
	if detail.Precision != memory.PrecisionDay || detail.TimezoneKnown {
		return value.UTC()
	}
	if mention, ok := temporal.ParseValue(detail.Text); ok && mention.Time != nil {
		return mention.Time.UTC()
	}
	return time.Date(value.UTC().Year(), value.UTC().Month(), value.UTC().Day(), 0, 0, 0, 0, time.UTC)
}

func normalizeKind(kind memory.Kind) memory.Kind {
	switch kind {
	case memory.KindFact, memory.KindPreference, memory.KindEvent, memory.KindProcedure:
		return kind
	default:
		return memory.KindFact
	}
}

func defaultTitle(content string) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) > 40 {
		runes = runes[:40]
	}
	return strings.TrimSpace(string(runes))
}

func buildServiceEvidence(src source.Source, view source.View, proposed []source.EvidenceReference, modelName string, now time.Time) []source.Evidence {
	result := make([]source.Evidence, 0, len(proposed))
	byKey := make(map[string]source.Evidence, len(proposed))
	for _, item := range proposed {
		if _, err := view.Resolve(item); err != nil {
			// Validation has already isolated unresolvable evidence. Keep one
			// source association per reference without inventing a quote.
			result = append(result, source.NewSourceEvidence(src, modelName, now))
			continue
		}
		evidence := source.NewEvidence(src, item, modelName, now)
		key := evidenceKey(evidence)
		if existing, duplicate := byKey[key]; duplicate {
			result = append(result, existing)
			continue
		}
		byKey[key] = evidence
		result = append(result, evidence)
	}
	if len(result) == 0 {
		// A candidate without explicit quotes still keeps a source association,
		// but cannot alone justify an automatic correct/change.
		result = append(result, source.NewSourceEvidence(src, modelName, now))
	}
	return result
}

func evidenceKey(item source.Evidence) string {
	field := ""
	start, end := 0, 0
	if item.Location != nil {
		field = item.Location.Field
		start = item.Location.Start
		end = item.Location.End
	}
	return strings.Join([]string{
		item.SourceID, item.MessageID, item.Quote, field,
		fmt.Sprintf("%d", start), fmt.Sprintf("%d", end),
	}, "\x00")
}

func exactDuplicate(items []memory.Memory, candidate Candidate) *memory.Memory {
	candidateContent := normalizeComparable(candidate.Content)
	for index := range items {
		item := &items[index]
		if item.State != memory.StateActive || item.AssertionStatus != memory.AssertionAccepted {
			continue
		}
		if normalizeComparable(item.Content) != candidateContent {
			continue
		}
		if item.Kind != candidate.Kind || !samePhase(item.Phase, candidate.Phase) {
			continue
		}
		if candidate.EventTime != nil && (item.EventTime == nil || !candidate.EventTime.Equal(*item.EventTime)) {
			continue
		}
		// Same text in a different subject/scope is not the same assertion.
		// Grouped candidates always go through the decision model.
		if item.FactGroupID != "" || candidateHasGroup(candidate) {
			continue
		}
		if candidate.ValidFrom != nil && item.ValidFrom != nil && !candidate.ValidFrom.Equal(*item.ValidFrom) {
			continue
		}
		if candidate.ValidTo != nil && item.ValidTo != nil && !candidate.ValidTo.Equal(*item.ValidTo) {
			continue
		}
		return item
	}
	return nil
}

func candidateHasGroup(candidate Candidate) bool {
	return candidate.FactGroup != nil &&
		(candidate.FactGroup.Subject != "" || candidate.FactGroup.Predicate != "" || candidate.FactGroup.Scope != "")
}

func normalizeComparable(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func mergeMemories(target map[string]memory.Memory, items []memory.Memory) {
	for _, item := range items {
		if item.State == memory.StateDeleted {
			continue
		}
		if _, exists := target[item.ID]; !exists {
			target[item.ID] = item
		}
	}
}

func mapValues(items map[string]memory.Memory) []memory.Memory {
	result := make([]memory.Memory, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func mapGroupValues(items map[string]memory.FactGroup) []memory.FactGroup {
	result := make([]memory.FactGroup, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
