package aggregation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

const (
	defaultTimeout    = time.Minute
	defaultMaxInputs  = 64
	defaultItemRunes  = 2000
	defaultTotalRunes = 24000
	maxContentRunes   = 8000
	maxTitleRunes     = 96
)

// Store runs one short aggregation transaction. Model calls happen outside the
// callback.
type Store interface {
	WithTx(ctx context.Context, fn func(context.Context, Tx) error) error
}

// Tx is the transaction-scoped capability used by one aggregation commit. It
// includes the exact dependency edges, the current derived pointer, task state
// and the next-layer registration.
type Tx interface {
	artifact.Tx
	GetGovernanceJob(ctx context.Context, id string) (task.Job, error)
	FinishJob(ctx context.Context, id string, revision int64, now time.Time) (bool, error)
	RescheduleJob(ctx context.Context, id string, revision int64, input task.RescheduleInput) (bool, error)
	GetExperienceRun(ctx context.Context, id string) (governance.ExperienceRun, error)
}

// Provider is the one aggregation model capability. It receives only the
// bounded target and lower-layer inputs and returns the derived body.
type Provider interface {
	Aggregate(ctx context.Context, request Request) (Result, error)
}

// Error classifies an aggregation failure for the worker without exposing raw
// model text. Kind is one of configuration, protocol or transient.
type Error struct {
	Kind string
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return "aggregation error"
	}
	if e.Err == nil {
		if e.Kind == "" {
			return "aggregation error"
		}
		return e.Kind
	}
	if e.Kind == "" {
		return e.Err.Error()
	}
	return e.Kind + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// FailureKind implements the worker's error-classification interface.
func (e *Error) FailureKind() string {
	if e == nil || e.Kind == "" {
		return "transient"
	}
	return e.Kind
}

// Service owns layer input selection, model invocation and the short commit
// transaction. It never lets a model choose a target, title or dependency.
type Service struct {
	store      Store
	artifacts  *artifact.Service
	provider   Provider
	now        func() time.Time
	timeout    time.Duration
	maxInputs  int
	itemRunes  int
	totalRunes int
	debounce   time.Duration
	generator  string
}

// Option customizes one aggregation service.
type Option func(*Service)

// WithClock overrides the service clock for deterministic tests.
func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

// WithTimeout bounds one aggregation model call.
func WithTimeout(timeout time.Duration) Option {
	return func(service *Service) {
		if timeout > 0 {
			service.timeout = timeout
		}
	}
}

// WithMaxInputs overrides the per-call lower-layer input cap.
func WithMaxInputs(limit int) Option {
	return func(service *Service) {
		if limit > 0 {
			service.maxInputs = limit
		}
	}
}

// WithDebounce sets the delay before a next-layer rebuild becomes due.
func WithDebounce(delay time.Duration) Option {
	return func(service *Service) {
		if delay < 0 {
			delay = 0
		}
		service.debounce = delay
	}
}

// WithGenerator records the producer identity on each revision.
func WithGenerator(name string) Option {
	return func(service *Service) {
		if strings.TrimSpace(name) != "" {
			service.generator = strings.TrimSpace(name)
		}
	}
}

// NewService assembles the layer processor. The provider may be unavailable; in
// that case only model-dependent tasks wait, while reads and other workers keep
// running.
func NewService(store Store, artifacts *artifact.Service, provider Provider, options ...Option) *Service {
	service := &Service{
		store:      store,
		artifacts:  artifacts,
		provider:   provider,
		now:        time.Now,
		timeout:    defaultTimeout,
		maxInputs:  defaultMaxInputs,
		itemRunes:  defaultItemRunes,
		totalRunes: defaultTotalRunes,
		debounce:   DefaultDebounce,
		generator:  "aggregation:layer",
	}
	for _, option := range options {
		option(service)
	}
	return service
}

// Process executes one claimed aggregation job. The layer is chosen by code
// from the task kind, never by the model.
func (s *Service) Process(ctx context.Context, job task.Job, layer artifact.Layer) error {
	if s == nil || s.store == nil || s.artifacts == nil {
		return errors.New("aggregation: service dependencies not configured")
	}
	if job.Kind != JobKind(layer) {
		return fmt.Errorf("aggregation: handler for %s received kind %s", layer, job.Kind)
	}
	target, err := artifact.ParseTargetKey(job.TargetKey)
	if err != nil {
		return &Error{Kind: "protocol", Err: fmt.Errorf("invalid target %q", job.TargetKey)}
	}
	target = NormalizeTarget(target)
	if target.Layer != layer {
		return &Error{Kind: "protocol",
			Err: fmt.Errorf("target %q layer %s does not match %s", job.TargetKey, target.Layer, layer)}
	}
	if target.ScopeKey == "" {
		return &Error{Kind: "protocol", Err: errors.New("aggregation target has no scope")}
	}

	now := s.now().UTC()
	var inputs []artifact.InputRef
	var eligible int
	var selectionTruncated bool
	var signature string
	var activeSignature string
	err = s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		var listErr error
		inputs, selectionTruncated, listErr = tx.ListDerivedInputs(ctx, target, s.maxInputs)
		if listErr != nil {
			return listErr
		}
		if eligible, listErr = tx.CountDerivedInputs(ctx, target); listErr != nil {
			return listErr
		}
		signature = inputSignature(inputs, eligible, selectionTruncated)
		activeSignature, listErr = currentActiveSignature(ctx, tx, target)
		return listErr
	})
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return s.disable(ctx, job, target, now)
	}
	if activeSignature != "" && activeSignature == signature {
		return s.finish(ctx, job)
	}
	if s.provider == nil {
		return &Error{Kind: "configuration", Err: errors.New("aggregation provider not configured")}
	}

	bounded, boundedTruncated := boundInputs(inputs, s.itemRunes, s.totalRunes)
	truncated := selectionTruncated || boundedTruncated || eligible > len(inputs)
	if len(bounded) == 0 {
		return s.disable(ctx, job, target, now)
	}

	callCtx := ctx
	cancel := func() {}
	if s.timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, s.timeout)
	}
	result, callErr := s.provider.Aggregate(callCtx, Request{
		Target:     target,
		Inputs:     bounded,
		InputCount: eligible,
		Truncated:  truncated,
	})
	cancel()
	if callErr != nil {
		return callErr
	}
	content := truncateRunes(strings.TrimSpace(result.Content), maxContentRunes)
	if content == "" {
		return &Error{Kind: "protocol", Err: errors.New("aggregation returned empty content")}
	}
	title := deriveTitle(content)
	payload := buildPayload(target, eligible, len(inputs), len(bounded), truncated, signature)

	return s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		current, currentTruncated, err := tx.ListDerivedInputs(ctx, target, s.maxInputs)
		if err != nil {
			return err
		}
		currentEligible, err := tx.CountDerivedInputs(ctx, target)
		if err != nil {
			return err
		}
		if !sameInputs(inputs, current) || currentTruncated != selectionTruncated || currentEligible != eligible {
			return &Error{Kind: "transient", Err: errors.New("aggregation input changed during generation")}
		}
		committed, err := s.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Target:          target,
			Title:           title,
			Content:         content,
			Payload:         payload,
			ReasonCode:      reasonCode(truncated),
			Truncated:       truncated,
			Generator:       s.generator,
			ProtocolVersion: "aggregate-v1",
			Dependencies:    artifact.DependencyRefs(bounded),
		})
		if err != nil {
			return err
		}
		if err := s.enqueueNext(ctx, tx, target, committed, now); err != nil {
			return err
		}
		return finishJob(ctx, tx, job, s.now().UTC())
	})
}

func (s *Service) disable(ctx context.Context, job task.Job, target artifact.Target, now time.Time) error {
	return s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		inputs, _, err := tx.ListDerivedInputs(ctx, target, s.maxInputs)
		if err != nil {
			return err
		}
		eligible, err := tx.CountDerivedInputs(ctx, target)
		if err != nil {
			return err
		}
		if len(inputs) != 0 || eligible != 0 {
			return &Error{Kind: "transient", Err: errors.New("aggregation input appeared during generation")}
		}
		item, err := s.artifacts.DisableWithReason(ctx, tx, target, "no_eligible_input")
		if err != nil {
			return err
		}
		if err := s.enqueueNext(ctx, tx, target, item, now); err != nil {
			return err
		}
		return finishJob(ctx, tx, job, s.now().UTC())
	})
}

func (s *Service) finish(ctx context.Context, job task.Job) error {
	return s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		return finishJob(ctx, tx, job, s.now().UTC())
	})
}

// enqueueNext marks the next layer's current object stale and registers one
// debounced rebuild in the same transaction as the revision that invalidated it.
func (s *Service) enqueueNext(ctx context.Context, tx Tx, current artifact.Target,
	committed artifact.DerivedMemory, now time.Time) error {
	next, ok := NextTarget(current)
	if !ok {
		return nil
	}
	existing, err := tx.GetArtifactByTarget(ctx, next.Kind, next.TargetKey())
	switch {
	case err == nil:
		if existing.State == artifact.StateActive {
			existing.State = artifact.StateStale
			existing.UpdatedAt = now
			if err := tx.UpdateArtifact(ctx, existing); err != nil {
				return err
			}
			if err := tx.DeleteArtifactSearchDocument(ctx, existing.ID); err != nil {
				return err
			}
		}
	case errors.Is(err, artifact.ErrNotFound):
	default:
		return err
	}
	nextAttempt := now.Add(s.debounce)
	_, _, err = tx.UpsertJob(ctx, task.UpsertInput{
		Kind:      JobKind(next.Layer),
		TargetKey: next.TargetKey(),
		InputHash: task.InputHash("aggregate-next", next.TargetKey(), committed.ID,
			strconv.Itoa(committed.CurrentVersion), string(committed.State), committed.ReasonCode),
		NextAttemptAt: &nextAttempt,
		Now:           now,
	})
	return err
}

func checkJob(ctx context.Context, tx Tx, job task.Job) error {
	current, err := tx.GetGovernanceJob(ctx, job.ID)
	if err != nil {
		return err
	}
	if current.Revision != job.Revision || current.Status != task.StatusRunning {
		return task.ErrRevisionChanged
	}
	return nil
}

func finishJob(ctx context.Context, tx Tx, job task.Job, now time.Time) error {
	ok, err := tx.FinishJob(ctx, job.ID, job.Revision, now)
	if err != nil {
		return err
	}
	if !ok {
		return task.ErrRevisionChanged
	}
	return nil
}

func currentActiveSignature(ctx context.Context, tx Tx, target artifact.Target) (string, error) {
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

func inputSignature(inputs []artifact.InputRef, eligible int, truncated bool) string {
	parts := make([]string, 0, len(inputs)*3+2)
	parts = append(parts, strconv.Itoa(eligible), strconv.FormatBool(truncated))
	for _, input := range inputs {
		parts = append(parts, string(input.Type), input.ID, strconv.Itoa(input.Version))
	}
	return task.InputHash(parts...)
}

func sameInputs(left, right []artifact.InputRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Type != right[index].Type ||
			left[index].ID != right[index].ID ||
			left[index].Version != right[index].Version {
			return false
		}
	}
	return true
}

func boundInputs(inputs []artifact.InputRef, itemRunes, totalRunes int) ([]artifact.InputRef, bool) {
	truncated := false
	remaining := totalRunes
	result := make([]artifact.InputRef, 0, len(inputs))
	for _, input := range inputs {
		if remaining <= 0 {
			truncated = true
			break
		}
		content := strings.TrimSpace(input.Content)
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
		title := strings.TrimSpace(input.Title)
		input.Title = truncateRunes(title, maxTitleRunes)
		if input.Title != title {
			truncated = true
		}
		input.Content = bounded
		remaining -= len([]rune(bounded))
		result = append(result, input)
	}
	return result, truncated
}

func deriveTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return truncateRunes(line, maxTitleRunes)
	}
	return ""
}

func buildPayload(target artifact.Target, eligible, selected, included int,
	truncated bool, signature string) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"layer":     string(target.LayerOrDefault()),
		"scope_key": target.ScopeKey,
		"bucket":    target.Bucket,
		"domain":    target.Topic,
		"input_selection": map[string]any{
			"eligible":  eligible,
			"selected":  selected,
			"included":  included,
			"truncated": truncated,
		},
		"input_signature": signature,
		"truncated":       truncated,
	})
	if err != nil {
		return json.RawMessage(`{"truncated":true}`)
	}
	return json.RawMessage(payload)
}

func reasonCode(truncated bool) string {
	if truncated {
		return "input_truncated"
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
