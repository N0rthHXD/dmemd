package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
)

const (
	defaultAggregateTimeout    = time.Minute
	defaultAggregateInputs     = 64
	defaultAggregateItemRunes  = 2000
	defaultAggregateTotalRunes = 24000
	defaultAggregateTitleRunes = 300
	maxAggregateContentRunes   = 8000
)

// AggregateProvider is the one structured summary/profile model operation.
type AggregateProvider interface {
	Summarize(ctx context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error)
}

// ExperienceProvider is the optional experience-case operation. It is part of
// the aggregation family but has its own bounded request because its leaves are
// application reports rather than only atomic memories.
type ExperienceProvider interface {
	ComposeExperience(ctx context.Context, request artifact.ExperienceRequest) (artifact.ExperienceResult, error)
}

// AggregateHandler builds bounded topic summaries and scope profiles. It never
// reads source payloads and never lets model text become configuration or an
// executable action.
type AggregateHandler struct {
	store      Store
	artifacts  *artifact.Service
	provider   AggregateProvider
	experience ExperienceProvider
	limiter    *Limiter
	now        func() time.Time
	timeout    time.Duration
	maxInputs  int
	itemRunes  int
	totalRunes int
	generator  string
}

// AggregateOption customizes the aggregate handler.
type AggregateOption func(*AggregateHandler)

// WithAggregateClock overrides the handler clock for deterministic tests.
func WithAggregateClock(now func() time.Time) AggregateOption {
	return func(handler *AggregateHandler) {
		if now != nil {
			handler.now = now
		}
	}
}

// WithAggregateTimeout bounds one summary model call.
func WithAggregateTimeout(timeout time.Duration) AggregateOption {
	return func(handler *AggregateHandler) {
		if timeout > 0 {
			handler.timeout = timeout
		}
	}
}

// WithAggregateMaxInputs overrides the fixed per-call input cap.
func WithAggregateMaxInputs(limit int) AggregateOption {
	return func(handler *AggregateHandler) {
		if limit > 0 {
			handler.maxInputs = limit
		}
	}
}

// WithAggregateGenerator records the producer identity on each revision.
func WithAggregateGenerator(name string) AggregateOption {
	return func(handler *AggregateHandler) {
		if strings.TrimSpace(name) != "" {
			handler.generator = strings.TrimSpace(name)
		}
	}
}

// WithExperienceProvider enables experience-case targets on the same aggregate
// job kind. Without it those jobs fail as a protocol error and are isolated.
func WithExperienceProvider(provider ExperienceProvider) AggregateOption {
	return func(handler *AggregateHandler) {
		if provider != nil {
			handler.experience = provider
		}
	}
}

// NewAggregateHandler assembles the summary/profile processor.
func NewAggregateHandler(store Store, artifacts *artifact.Service, provider AggregateProvider,
	limiter *Limiter, options ...AggregateOption) *AggregateHandler {
	handler := &AggregateHandler{
		store:      store,
		artifacts:  artifacts,
		provider:   provider,
		limiter:    limiter,
		now:        time.Now,
		timeout:    defaultAggregateTimeout,
		maxInputs:  defaultAggregateInputs,
		itemRunes:  defaultAggregateItemRunes,
		totalRunes: defaultAggregateTotalRunes,
		generator:  "governance:aggregate",
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// Process implements Handler.
func (h *AggregateHandler) Process(ctx context.Context, job task.Job) error {
	if job.Kind != task.KindAggregate {
		return fmt.Errorf("aggregate handler received kind %s", job.Kind)
	}
	if h.store == nil || h.artifacts == nil {
		return errors.New("governance: aggregate handler dependencies not configured")
	}
	if experienceTarget(job.TargetKey) {
		return h.processExperience(ctx, job)
	}
	if h.provider == nil {
		return errors.New("governance: aggregate handler dependencies not configured")
	}
	target, err := aggregateTarget(job.TargetKey)
	if err != nil {
		return &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: err}
	}

	now := h.now().UTC()
	var inputs []artifact.MemoryInput
	var truncated bool
	var eligible int
	var currentSignature string
	err = h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		var listErr error
		inputs, truncated, listErr = tx.ListAggregationInputs(ctx, target, now, h.maxInputs)
		if listErr != nil {
			return listErr
		}
		if eligible, listErr = tx.CountAggregationInputs(ctx, target, now); listErr != nil {
			return listErr
		}
		currentSignature, listErr = currentAggregateSignature(ctx, tx, target)
		return listErr
	})
	if err != nil {
		return err
	}

	selectionTruncated := truncated
	bounded, truncatedInputs := boundAggregateInputs(inputs, h.itemRunes, h.totalRunes)
	truncated = truncated || truncatedInputs
	if len(bounded) == 0 || h.provider == nil {
		return h.disable(ctx, job, target)
	}
	inputSignature := aggregateInputSignature(inputs)
	if currentSignature != "" && currentSignature == inputSignature {
		// A new generation task can be registered after a membership or
		// evidence change without changing the selected input set. Reuse the
		// active revision instead of paying for an identical model call.
		return h.finishWithoutWork(ctx, job)
	}
	if h.limiter != nil {
		allowed, next := h.limiter.Acquire(now)
		if !allowed {
			return &RetryError{At: next, Code: task.ErrorRateLimited, Reason: task.WaitRateLimited}
		}
	}

	callCtx := ctx
	cancel := func() {}
	if h.timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, h.timeout)
	}
	started := time.Now()
	result, callErr := h.provider.Summarize(callCtx, artifact.AggregateRequest{
		Kind:      target.Kind,
		ScopeKey:  target.ScopeKey,
		Topic:     target.Topic,
		Truncated: truncated,
		Eligible:  eligible,
		Memories:  bounded,
	})
	cancel()
	h.limiter.Record(string(target.Kind), len(bounded), aggregateContextSize(bounded), started, callErr)
	if callErr != nil {
		return callErr
	}
	result.Title = truncateRunes(strings.TrimSpace(result.Title), defaultAggregateTitleRunes)
	result.Content = strings.TrimSpace(result.Content)
	if result.Content == "" {
		return &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ingest.ErrProtocol}
	}
	result.Content = truncateRunes(result.Content, maxAggregateContentRunes)
	if target.Kind == artifact.KindProfile && len(profileTransientMarkers(result.Content)) > 0 {
		return h.failOutput(ctx, job, target, "invalid_profile_content")
	}
	acceptedCitations, rejectedCitations := validateCitations(result.Citations, bounded)
	acceptedOmissions, rejectedOmissions := validateOmittedInputs(result.OmittedInputs, bounded)
	payload := aggregatePayload(target, result, inputs, bounded, eligible, selectionTruncated,
		truncatedInputs, acceptedCitations, rejectedCitations, acceptedOmissions, rejectedOmissions, inputSignature)
	reasonCode := ""
	if truncated {
		reasonCode = "input_truncated"
	}

	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		current, currentTruncated, err := tx.ListAggregationInputs(ctx, target, h.now().UTC(), h.maxInputs)
		if err != nil {
			return err
		}
		currentEligible, err := tx.CountAggregationInputs(ctx, target, h.now().UTC())
		if err != nil {
			return err
		}
		if !sameMemoryInputs(inputs, current) || currentTruncated != selectionTruncated || currentEligible != eligible {
			return &ingest.ProviderError{Operation: "response", Retryable: true,
				Err: errors.New("aggregate input changed during generation")}
		}
		if _, err := h.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Kind:            target.Kind,
			TargetKey:       target.TargetKey(),
			ScopeKey:        target.ScopeKey,
			Topic:           target.Topic,
			Title:           result.Title,
			Content:         result.Content,
			Payload:         payload,
			ReasonCode:      reasonCode,
			Truncated:       truncated,
			Generator:       h.generator,
			ProtocolVersion: "aggregate-v3",
			RunID:           job.ID + ":" + fmt.Sprint(job.Revision),
			Memories:        inputs,
		}); err != nil {
			return err
		}
		ok, err := tx.FinishJob(ctx, job.ID, job.Revision, h.now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
}

func (h *AggregateHandler) finishWithoutWork(ctx context.Context, job task.Job) error {
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		ok, err := tx.FinishJob(ctx, job.ID, job.Revision, h.now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
}

// failOutput records a fixed output-quality failure and completes the task. It
// is used for deterministic content checks that a retry cannot repair.
func (h *AggregateHandler) failOutput(ctx context.Context, job task.Job, target artifact.Target, reason string) error {
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		if _, err := h.artifacts.DisableWithReason(ctx, tx, target, reason); err != nil {
			return err
		}
		if err := tx.UpsertTargetEvaluation(ctx, artifact.TargetEvaluation{
			Kind: target.Kind, TargetKey: target.TargetKey(), ScopeKey: target.ScopeKey,
			Topic: target.Topic, Status: artifact.TargetStatusInvalidOutput,
			ReasonCode: reason, EvaluatedAt: h.now().UTC(),
		}); err != nil {
			return err
		}
		ok, err := tx.FinishJob(ctx, job.ID, job.Revision, h.now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
}

func currentAggregateSignature(ctx context.Context, tx Tx, target artifact.Target) (string, error) {
	item, err := tx.GetArtifactByTarget(ctx, target.Kind, target.TargetKey())
	if errors.Is(err, artifact.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if item.State != artifact.StateActive || item.CurrentVersion <= 0 {
		return "", nil
	}
	revision, err := tx.GetArtifactRevision(ctx, item.ID, item.CurrentVersion)
	if err != nil {
		return "", err
	}
	var payload struct {
		InputSignature string `json:"input_signature"`
	}
	if err := json.Unmarshal(revision.Payload, &payload); err != nil {
		return "", nil
	}
	return payload.InputSignature, nil
}

func aggregateInputSignature(inputs []artifact.MemoryInput) string {
	parts := make([]string, 0, len(inputs)*2+1)
	for _, input := range inputs {
		parts = append(parts, input.ID, fmt.Sprint(input.Version))
	}
	return task.InputHash(parts...)
}

func validateCitations(citations []artifact.Citation, allowed []artifact.MemoryInput) ([]artifact.Citation, int) {
	versions := make(map[string]int, len(allowed))
	for _, input := range allowed {
		versions[input.ID] = input.Version
	}
	accepted := make([]artifact.Citation, 0, len(citations))
	rejected := 0
	for _, citation := range citations {
		version, ok := versions[citation.MemoryID]
		if !ok || citation.Version != version || strings.TrimSpace(citation.Point) == "" {
			rejected++
			continue
		}
		citation.Point = truncateRunes(strings.TrimSpace(citation.Point), 500)
		citation.Section = truncateRunes(strings.TrimSpace(citation.Section), defaultAggregateTitleRunes)
		accepted = append(accepted, citation)
	}
	return accepted, rejected
}

func validateOmittedInputs(omitted []artifact.OmittedInput, allowed []artifact.MemoryInput) ([]artifact.OmittedInput, int) {
	versions := make(map[string]int, len(allowed))
	for _, input := range allowed {
		versions[input.ID] = input.Version
	}
	accepted := make([]artifact.OmittedInput, 0, len(omitted))
	rejected := 0
	for _, item := range omitted {
		version, ok := versions[item.MemoryID]
		if !ok || item.Version != version {
			rejected++
			continue
		}
		item.Reason = truncateRunes(strings.TrimSpace(item.Reason), defaultAggregateTitleRunes)
		accepted = append(accepted, item)
	}
	return accepted, rejected
}

func aggregatePayload(target artifact.Target, result artifact.AggregateResult,
	selected, included []artifact.MemoryInput, eligible int, selectionTruncated, budgetTruncated bool,
	citations []artifact.Citation, rejectedCitations int, omitted []artifact.OmittedInput,
	rejectedOmissions int, inputSignature string) json.RawMessage {
	omittedCount := eligible - len(included)
	if omittedCount < 0 {
		omittedCount = 0
	}
	reasons := []string{}
	if selectionTruncated || len(selected) < eligible {
		reasons = append(reasons, "input_limit")
	}
	if budgetTruncated {
		reasons = append(reasons, "content_budget")
	}
	citationIntegrity := "unknown"
	if len(result.Citations) > 0 {
		switch {
		case rejectedCitations == 0:
			citationIntegrity = "valid"
		case len(citations) > 0:
			citationIntegrity = "partial"
		default:
			citationIntegrity = "invalid"
		}
	}
	coverage := "unknown"
	if omittedCount > 0 || selectionTruncated || budgetTruncated || len(omitted) > 0 {
		coverage = "subset"
	}
	payload := map[string]any{
		"input_selection": map[string]any{
			"eligible":  eligible,
			"selected":  len(selected),
			"included":  len(included),
			"omitted":   omittedCount,
			"truncated": selectionTruncated || budgetTruncated || omittedCount > 0,
			"reasons":   reasons,
		},
		"citation_integrity": citationIntegrity,
		"rejected_citations": rejectedCitations,
		"rejected_omissions": rejectedOmissions,
		"semantic_coverage":  coverage,
		"citations":          citations,
		"omitted_inputs":     omitted,
		"input_signature":    inputSignature,
		"truncated":          omittedCount > 0 || selectionTruncated || budgetTruncated,
	}
	if target.Kind == artifact.KindProfile {
		markers := profileTransientMarkers(result.Content)
		check := "passed"
		if citationIntegrity == "invalid" {
			check = "invalid_citation"
		} else if len(markers) > 0 {
			check = "transient_markers_present"
		}
		payload["profile_check"] = map[string]any{
			"status":             check,
			"input_scope":        "durable_non_event",
			"citation_integrity": citationIntegrity,
			"transient_markers":  markers,
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{"semantic_coverage":"unknown"}`)
	}
	return json.RawMessage(encoded)
}

func profileTransientMarkers(content string) []string {
	markers := []string{"进行中", "进度", "即将", "本周", "下周", "今天", "明天",
		"in progress", "next week", "tomorrow", "today", "TODO"}
	found := []string{}
	lower := strings.ToLower(content)
	for _, marker := range markers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			found = append(found, marker)
		}
	}
	return found
}

func aggregateContextSize(inputs []artifact.MemoryInput) int {
	size := 0
	for _, input := range inputs {
		size += len(input.Title) + len(input.Content)
	}
	return size
}

func (h *AggregateHandler) disable(ctx context.Context, job task.Job, target artifact.Target) error {
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		if _, err := h.artifacts.Disable(ctx, tx, target); err != nil {
			return err
		}
		ok, err := tx.FinishJob(ctx, job.ID, job.Revision, h.now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
}

func aggregateTarget(key string) (artifact.Target, error) {
	if scopeKey, ok := task.ParseScopeSummaryKey(key); ok {
		return artifact.Target{Kind: artifact.KindScopeSummary, ScopeKey: scopeKey}, nil
	}
	if scopeKey, ok := task.ParseProfileKey(key); ok {
		return artifact.Target{Kind: artifact.KindProfile, ScopeKey: scopeKey}, nil
	}
	if scopeKey, topic, ok := task.ParseAggregateKey(key); ok {
		return artifact.Target{Kind: artifact.KindTopicSummary, ScopeKey: scopeKey, Topic: topic}, nil
	}
	return artifact.Target{}, fmt.Errorf("invalid aggregate target key %q", key)
}

func boundAggregateInputs(inputs []artifact.MemoryInput, itemRunes, totalRunes int) ([]artifact.MemoryInput, bool) {
	truncated := false
	remaining := totalRunes
	result := make([]artifact.MemoryInput, 0, len(inputs))
	for _, item := range inputs {
		if remaining <= 0 {
			truncated = true
			break
		}
		content := strings.TrimSpace(item.Content)
		if content == "" {
			truncated = true
			continue
		}
		budget := itemRunes
		if budget <= 0 || budget > remaining {
			budget = remaining
		}
		bounded := truncateRunes(content, budget)
		if len([]rune(bounded)) < len([]rune(content)) {
			truncated = true
		}
		title := strings.TrimSpace(item.Title)
		item.Title = truncateRunes(title, defaultAggregateTitleRunes)
		if item.Title != title {
			truncated = true
		}
		item.Content = bounded
		remaining -= len([]rune(bounded))
		result = append(result, item)
	}
	return result, truncated
}
