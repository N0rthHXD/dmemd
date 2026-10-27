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
	err = h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		var listErr error
		inputs, truncated, listErr = tx.ListAggregationInputs(ctx, target, now, h.maxInputs)
		if listErr != nil {
			return listErr
		}
		if target.Kind == artifact.KindTopicSummary {
			return h.diagnoseSkillInput(ctx, tx, target, now)
		}
		return nil
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
	coverage := "full"
	for _, input := range bounded {
		if !strings.Contains(result.Content, strings.TrimSpace(input.Content)) {
			coverage = "subset"
		}
	}
	if truncated {
		coverage = "subset"
	}
	if target.Kind == artifact.KindTopicSummary && coverage == "subset" {
		result.Content = "覆盖说明：本摘要仅覆盖所列来源的部分内容，不代表该主题的全部原子事实。\n\n" + result.Content
	}
	payload, err := json.Marshal(map[string]any{"coverage": coverage, "truncated": truncated})
	if err != nil {
		return err
	}
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
		if !sameMemoryInputs(inputs, current) || currentTruncated != selectionTruncated {
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
			ProtocolVersion: "aggregate-v2",
			RunID:           job.ID + ":" + fmt.Sprint(job.Revision),
			Memories:        bounded,
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

func aggregateContextSize(inputs []artifact.MemoryInput) int {
	size := 0
	for _, input := range inputs {
		size += len(input.Title) + len(input.Content)
	}
	return size
}

func (h *AggregateHandler) diagnoseSkillInput(ctx context.Context, tx Tx, target artifact.Target, now time.Time) error {
	skillTarget := artifact.Target{Kind: artifact.KindSkill, ScopeKey: target.ScopeKey, Topic: target.Topic}
	procedures, _, err := tx.ListAggregationInputs(ctx, skillTarget, now, 1)
	if err != nil || len(procedures) > 0 {
		return err
	}
	reason := "no_procedure_input"
	hasProcedures, err := tx.HasProcedureInputs(ctx, target.ScopeKey, now)
	if err != nil {
		return err
	}
	if hasProcedures {
		reason = "topic_mismatch"
	}
	_, err = h.artifacts.DisableWithReason(ctx, tx, skillTarget, reason)
	return err
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
