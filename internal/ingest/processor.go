package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/topic"
)

const (
	// DefaultProcessBudget bounds an attempt, independently of per-call limits.
	DefaultProcessBudget     = 600 * time.Second
	DefaultExtractionTimeout = 180 * time.Second
	DefaultDecisionTimeout   = 300 * time.Second
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
	now               func() time.Time
	logger            *slog.Logger
	processBudget     time.Duration
	extractionTimeout time.Duration
	decisionTimeout   time.Duration
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
	candidates, extractionWarnings, err := p.extractValidated(budgetCtx, src)
	if err != nil {
		return ProcessResult{}, err
	}
	candidates = normalizeCandidates(src, candidates, p.modelName, p.now().UTC())
	if len(candidates) > maxCandidatesPerSource {
		return ProcessResult{}, &ProviderError{
			Operation: "candidate_limit_exceeded",
			Retryable: false,
			Err:       fmt.Errorf("%w: %d > %d", ErrCandidateLimitExceeded, len(candidates), maxCandidatesPerSource),
		}
	}
	p.logger.Info("model extraction completed",
		"job_id", job.ID,
		"model", p.modelName,
		"candidates", len(candidates),
		"duration_ms", time.Since(started).Milliseconds(),
	)

	// Pin the catalog generation before any context read or model call. A write
	// that lands while we decide therefore invalidates the plan.
	stage = "context"
	generation, err := p.related.CatalogGeneration(budgetCtx)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("读取目录版本: %w", err)
	}
	plan, err := p.buildPlan(budgetCtx, src, candidates)
	if err != nil {
		return ProcessResult{}, err
	}
	plan.catalogGeneration = generation
	plan.warnings = append(plan.warnings, extractionWarnings...)
	if err := p.loadContext(budgetCtx, plan); err != nil {
		return ProcessResult{}, err
	}
	if !plan.complete {
		plan.warnings = append(plan.warnings, WarningContextTruncated)
	}
	stage = "decision"
	if err := p.decide(budgetCtx, plan); err != nil {
		return ProcessResult{}, err
	}

	var result mergeResult
	stage = "commit"
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
		currentJob.Status = JobSucceeded
		currentJob.MemoryIDs = result.MemoryIDs
		currentJob.ConflictIDs = result.ConflictIDs
		currentJob.PendingMemoryIDs = result.PendingMemoryIDs
		currentJob.Warnings = result.Warnings
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
		return ProcessResult{}, fmt.Errorf("提交处理结果: %w", err)
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
func (p *Processor) extractValidated(ctx context.Context, src source.Source) ([]Candidate, []string, error) {
	started := time.Now()
	extracted, err := modelCall(ctx, p.extractionTimeout, func(callCtx context.Context) (ExtractionResult, error) {
		return p.extractor.Extract(callCtx, ExtractionRequest{Source: src})
	})
	p.logModelCall("extraction", 1, len(src.Content), started, err)
	repairer, canRepair := p.extractor.(ExtractionRepairProvider)
	repairUsed := false
	if err != nil {
		var providerErr *ProviderError
		if !canRepair || !errors.As(err, &providerErr) || providerErr.Operation != "protocol" {
			return nil, nil, err
		}
		extracted, err = p.repairExtraction(ctx, repairer, ExtractionRepairRequest{
			Source: src, Retry: true,
			Issues: []ExtractionIssue{{Code: "invalid_structure", Message: "return a JSON object with a candidates array"}},
		})
		repairUsed = true
		if err != nil {
			return nil, nil, err
		}
	}
	if len(extracted.Candidates) > maxCandidatesPerSource {
		return nil, nil, &ProviderError{Operation: "candidate_limit_exceeded", Err: ErrCandidateLimitExceeded}
	}
	original := prepareExtractionCandidates(extracted.Candidates)
	normalizeExtractionMetadata(original)
	issues := ValidateExtraction(src, original)
	if len(issues) == 0 {
		return original, nil, nil
	}
	// Explicit privacy exclusions are not repairable. Do not let a second
	// model response resurrect them by omitting their exclusion evidence.
	excluded := make(map[string]struct{})
	for _, issue := range issues {
		if issue.Code == "explicit_exclusion" {
			excluded[issue.CandidateID] = struct{}{}
		}
	}
	warnings := []string{}
	if len(excluded) > 0 {
		warnings = append(warnings, WarningExtractionExcluded)
	}
	eligible := make([]Candidate, 0, len(original))
	for _, candidate := range original {
		if _, skip := excluded[candidate.CandidateID]; !skip {
			eligible = append(eligible, candidate)
		}
	}
	repairIssues := make([]ExtractionIssue, 0, len(issues))
	for _, issue := range issues {
		if _, skip := excluded[issue.CandidateID]; !skip {
			repairIssues = append(repairIssues, issue)
		}
	}
	if len(repairIssues) == 0 {
		return eligible, warnings, nil
	}
	invalid := issueIDs(repairIssues)
	current := eligible
	if canRepair && !repairUsed {
		batch := make([]Candidate, 0, len(invalid))
		for _, candidate := range eligible {
			if _, bad := invalid[candidate.CandidateID]; bad {
				batch = append(batch, candidate)
			}
		}
		repaired, repairErr := p.repairExtraction(ctx, repairer, ExtractionRepairRequest{
			Source: src, Candidates: batch, Issues: repairIssues,
			Retry: len(batch) >= 3 && len(batch)*2 >= len(eligible),
		})
		if repairErr == nil {
			current = mergeExtractionRepair(eligible, repaired.Candidates, repairIssues)
		}
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if len(current) > maxCandidatesPerSource {
		return nil, nil, &ProviderError{Operation: "candidate_limit_exceeded", Err: ErrCandidateLimitExceeded}
	}
	normalizeExtractionMetadata(current)
	remainingIssues := ValidateExtraction(src, current)
	current = isolateInvalidCandidates(current, remainingIssues)
	// A missing/invalid replacement is observable, rather than silently losing
	// a fact. Successful repairs need no warning.
	for id := range invalid {
		found := false
		for _, candidate := range current {
			if candidate.CandidateID == id || strings.HasPrefix(candidate.CandidateID, id+".") {
				found = true
				break
			}
		}
		if !found {
			warnings = append(warnings, WarningExtractionIsolated)
			break
		}
	}
	if len(remainingIssues) > 0 {
		warnings = append(warnings, WarningExtractionIsolated)
	}
	for _, issue := range issues {
		p.logger.Debug("extraction validation issue", "candidate_id", issue.CandidateID, "field", issue.Field, "code", issue.Code)
	}
	return prepareExtractionCandidates(current), uniqueWarnings(warnings), nil
}

func mergeExtractionRepair(original, repaired []Candidate, issues []ExtractionIssue) []Candidate {
	invalid := issueIDs(issues)
	result := make([]Candidate, 0, len(original)+len(repaired))
	for _, candidate := range original {
		if _, bad := invalid[candidate.CandidateID]; !bad {
			result = append(result, candidate)
			continue
		}
		seen := make(map[string]struct{})
		for _, replacement := range repaired {
			id := replacement.CandidateID
			if id != candidate.CandidateID && !strings.HasPrefix(id, candidate.CandidateID+".") {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				replacement.ProtocolErrors = append(replacement.ProtocolErrors, "candidate_id")
			}
			seen[id] = struct{}{}
			result = append(result, replacement)
		}
	}
	// A repair response that does not preserve candidate ids cannot safely be
	// associated with the invalid source candidate, so it is ignored.
	return result
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
		source:     src,
		candidates: make(map[string]Candidate, len(candidates)),
		related:    make(map[string]memory.Memory),
		groups:     make(map[string]memory.FactGroup),
		actions:    make([]plannedAction, 0, len(candidates)),
		complete:   true,
		decided:    make(map[string]struct{}, len(candidates)),
	}
	for _, candidate := range candidates {
		plan.candidates[candidate.CandidateID] = candidate
	}
	if len(candidates) == 0 {
		return plan, nil
	}
	if len(candidates) == 1 {
		context, err := p.related.RelatedContext(ctx, candidateQuery(candidates[0]), maxInitialContext)
		if err != nil {
			return nil, fmt.Errorf("选择相关事实: %w", err)
		}
		mergeMemories(plan.related, context.Memories)
		plan.warnings = append(plan.warnings, context.Warnings...)
		if !context.Complete {
			plan.complete = false
		}
		if duplicate := exactDuplicate(context.Memories, candidates[0]); duplicate != nil {
			plan.actions = append(plan.actions, plannedAction{
				candidate: candidates[0],
				decision: Decision{
					CandidateID:     candidates[0].CandidateID,
					Action:          ActionMergeEvidence,
					TargetMemoryIDs: []string{duplicate.ID},
				},
				evidence: candidates[0].Evidence,
			})
			plan.decided[candidates[0].CandidateID] = struct{}{}
			return plan, nil
		}
		if len(context.Memories) == 0 {
			plan.actions = append(plan.actions, plannedAction{
				candidate: candidates[0],
				decision: Decision{
					CandidateID: candidates[0].CandidateID,
					Action:      ActionAdd,
				},
				evidence: candidates[0].Evidence,
			})
			plan.decided[candidates[0].CandidateID] = struct{}{}
		}
	}
	return plan, nil
}

// loadContext selects bounded related facts, group timelines, groups and open
// conflicts for every candidate that still needs a decision.
func (p *Processor) loadContext(ctx context.Context, plan *mergePlan) error {
	if len(plan.actions) > 0 {
		return nil
	}
	for _, candidate := range plan.candidates {
		context, err := p.related.RelatedContext(ctx, candidateQuery(candidate), maxInitialContext)
		if err != nil {
			return fmt.Errorf("选择相关事实: %w", err)
		}
		mergeMemories(plan.related, context.Memories)
		plan.warnings = append(plan.warnings, context.Warnings...)
		if !context.Complete {
			plan.complete = false
		}
	}
	// Add group timelines and metadata for every discovered group.
	groupIDs := make(map[string]struct{})
	for _, item := range plan.related {
		if item.FactGroupID != "" {
			groupIDs[item.FactGroupID] = struct{}{}
		}
	}
	for _, candidate := range plan.candidates {
		if candidate.FactGroup != nil {
			if group := matchingGroup(plan.groups, *candidate.FactGroup); group != nil {
				groupIDs[group.ID] = struct{}{}
			}
		}
	}
	reader, _ := p.sources.(FactGroupReader)
	for groupID := range groupIDs {
		timeline, err := p.related.FactTimeline(ctx, groupID, maxTimelineContext)
		if err != nil {
			return fmt.Errorf("读取事实组时间线: %w", err)
		}
		mergeMemories(plan.related, timeline.Memories)
		if !timeline.Complete {
			plan.complete = false
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
	for _, candidate := range plan.candidates {
		if candidate.FactGroup == nil {
			continue
		}
		if group := matchingGroup(plan.groups, *candidate.FactGroup); group != nil {
			plan.groups[group.ID] = *group
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
		if action, ok := planConvergence(plan, candidate); ok {
			plan.actions = append(plan.actions, action)
			plan.decided[candidate.CandidateID] = struct{}{}
		}
	}
	return nil
}

// decide calls the decision provider in fixed batches of at most eight.
func (p *Processor) decide(ctx context.Context, plan *mergePlan) error {
	if len(plan.candidates) == 0 {
		return nil
	}
	pending := make([]Candidate, 0, len(plan.candidates))
	for _, candidate := range plan.candidates {
		if _, done := plan.decided[candidate.CandidateID]; done {
			continue
		}
		pending = append(pending, candidate)
	}
	if len(pending) == 0 {
		return nil
	}
	if p.decider == nil {
		return &ProviderError{Operation: "decision", Retryable: false, Err: errors.New("决策模型未配置")}
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].CandidateID < pending[j].CandidateID })
	related := mapValues(plan.related)
	groups := mapGroupValues(plan.groups)
	for start := 0; start < len(pending); start += maxDecisionBatch {
		end := start + maxDecisionBatch
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		request := DecisionRequest{
			Source:          plan.source,
			Candidates:      batch,
			RelatedMemories: related,
			FactGroups:      groups,
			Conflicts:       plan.conflicts,
			ContextComplete: plan.complete,
		}
		decisions, err := p.decideValidated(ctx, request)
		if err != nil {
			return err
		}
		for _, decision := range decisions {
			candidate := plan.candidates[decision.CandidateID]
			evidence, err := decisionEvidence(candidate, decision, plan.candidates)
			if err != nil {
				return err
			}
			if !plan.complete {
				switch decision.Action {
				case ActionChange, ActionCorrect, ActionEnrich, ActionMergeEvidence:
					decision.Action = ActionUnresolved
					decision.TargetMemoryIDs = nil
					decision.ReasonCode = string(memory.ConflictComplexTimeline)
				}
				plan.warnings = append(plan.warnings, WarningContextTruncated)
			}
			plan.actions = append(plan.actions, plannedAction{
				candidate: candidate,
				decision:  decision,
				evidence:  evidence,
			})
		}
	}
	return nil
}

// sourceText extracts readable source text for derived queries. The original
// JSON shape is never modified.
func sourceText(content json.RawMessage) string {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return string(trimmed)
	}
	parts := make([]string, 0, 8)
	collectText(root, &parts)
	return strings.Join(parts, "\n")
}

func collectText(value any, parts *[]string) {
	switch typed := value.(type) {
	case string:
		*parts = append(*parts, typed)
	case json.Number:
		*parts = append(*parts, typed.String())
	case bool:
		if typed {
			*parts = append(*parts, "true")
		} else {
			*parts = append(*parts, "false")
		}
	case []any:
		for _, item := range typed {
			collectText(item, parts)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectText(typed[key], parts)
		}
	}
}

func candidateQuery(candidate Candidate) string {
	parts := []string{candidate.Content}
	if candidate.FactGroup != nil {
		parts = append(parts, candidate.FactGroup.Subject, candidate.FactGroup.Predicate, candidate.FactGroup.Scope)
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

func normalizeCandidates(src source.Source, candidates []Candidate, modelName string, now time.Time) []Candidate {
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
		if candidate.ObservedAt == nil || candidate.ObservedAt.IsZero() {
			observed := fallbackObserved
			candidate.ObservedAt = &observed
		} else {
			observed := candidate.ObservedAt.UTC()
			candidate.ObservedAt = &observed
		}
		if candidate.FactGroup == nil {
			candidate.FactGroup = &FactGroupProposal{}
		}
		candidate.ValidFrom = memory.UTCPointer(candidate.ValidFrom)
		candidate.ValidTo = memory.UTCPointer(candidate.ValidTo)
		if len(candidate.Topics) > 0 {
			candidate.Topics = topic.Raw(candidate.Topics)
		}
		candidate.Evidence = buildServiceEvidence(src, candidate.Evidence, modelName, now)
		result = append(result, candidate)
	}
	return result
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

func buildServiceEvidence(src source.Source, proposed []source.Evidence, modelName string, now time.Time) []source.Evidence {
	result := make([]source.Evidence, 0, len(proposed))
	seen := make(map[string]struct{}, len(proposed))
	for _, item := range proposed {
		evidence := source.NewEvidence(src, item.MessageID, item.Quote, modelName, now)
		key := evidenceKey(evidence)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, evidence)
	}
	if len(result) == 0 {
		// A candidate without explicit quotes still keeps a source association,
		// but cannot alone justify an automatic correct/change.
		evidence := source.NewEvidence(src, "", "", modelName, now)
		result = append(result, evidence)
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

func matchingGroup(groups map[string]memory.FactGroup, proposal FactGroupProposal) *memory.FactGroup {
	for _, group := range groups {
		if group.Subject == strings.TrimSpace(proposal.Subject) &&
			group.Predicate == strings.TrimSpace(proposal.Predicate) &&
			group.Scope == strings.TrimSpace(proposal.Scope) {
			copied := group
			return &copied
		}
	}
	return nil
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
