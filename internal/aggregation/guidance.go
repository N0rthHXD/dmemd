package aggregation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/skill"
)

const (
	defaultGuidanceTimeout    = time.Minute
	defaultGuidanceMaxInputs  = 64
	defaultGuidanceItemRunes  = 2000
	defaultGuidanceTotalRunes = 24000
	maxGuidanceContentRunes   = 12000
	maxGuidanceTitleRunes     = 300
	maxGuidanceNameRunes      = 120
	maxGuidanceDescription    = 500
)

// ExperienceProvider writes only the readable body of one application-reported
// experience case. The service owns the run reference, outcome, dependencies,
// title and verification statement.
type ExperienceProvider interface {
	ComposeExperience(ctx context.Context, request artifact.ExperienceRequest) (artifact.ExperienceResult, error)
}

// SkillProvider writes one guidance-only workflow from stable procedures,
// lower-layer aggregates and recorded application results.
type SkillProvider interface {
	ComposeSkill(ctx context.Context, request skill.Request) (skill.Draft, error)
}

// GuidanceProvider is the complete L4 generation capability.
type GuidanceProvider interface {
	ExperienceProvider
	SkillProvider
}

// GuidanceHandler builds L4 experience cases and skills from existing governed
// objects. It never reads raw observations and never lets a model choose the
// target, dependencies, version or state.
type GuidanceHandler struct {
	store      Store
	artifacts  *artifact.Service
	provider   GuidanceProvider
	now        func() time.Time
	timeout    time.Duration
	maxInputs  int
	itemRunes  int
	totalRunes int
	debounce   time.Duration
}

// GuidanceOption customizes one L4 guidance handler.
type GuidanceOption func(*GuidanceHandler)

// WithGuidanceClock overrides the handler clock for deterministic tests.
func WithGuidanceClock(now func() time.Time) GuidanceOption {
	return func(handler *GuidanceHandler) {
		if now != nil {
			handler.now = now
		}
	}
}

// WithGuidanceTimeout bounds one L4 model call.
func WithGuidanceTimeout(timeout time.Duration) GuidanceOption {
	return func(handler *GuidanceHandler) {
		if timeout > 0 {
			handler.timeout = timeout
		}
	}
}

// WithGuidanceMaxInputs bounds the selected lower-layer input set.
func WithGuidanceMaxInputs(limit int) GuidanceOption {
	return func(handler *GuidanceHandler) {
		if limit > 0 {
			handler.maxInputs = limit
		}
	}
}

// WithGuidanceDebounce sets the next skill rebuild delay after an experience
// run changes. The write path only marks stale and registers work.
func WithGuidanceDebounce(delay time.Duration) GuidanceOption {
	return func(handler *GuidanceHandler) {
		if delay < 0 {
			delay = 0
		}
		handler.debounce = delay
	}
}

// NewGuidanceHandler assembles the L4 experience/skill processor.
func NewGuidanceHandler(store Store, artifacts *artifact.Service, provider GuidanceProvider,
	options ...GuidanceOption) *GuidanceHandler {
	handler := &GuidanceHandler{
		store:      store,
		artifacts:  artifacts,
		provider:   provider,
		now:        time.Now,
		timeout:    defaultGuidanceTimeout,
		maxInputs:  defaultGuidanceMaxInputs,
		itemRunes:  defaultGuidanceItemRunes,
		totalRunes: defaultGuidanceTotalRunes,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// Process implements the governance handler shape for build_guidance tasks.
// Experience cases and skills share the task kind because both are L4 derived
// memories; the target key determines which bounded workflow runs.
func (h *GuidanceHandler) Process(ctx context.Context, job task.Job) error {
	if job.Kind != task.KindBuildGuidance {
		return fmt.Errorf("guidance handler received kind %s", job.Kind)
	}
	if h.store == nil || h.artifacts == nil {
		return errors.New("aggregation: guidance dependencies not configured")
	}
	target, err := artifact.ParseTargetKey(job.TargetKey)
	if err != nil {
		return &Error{Kind: "protocol", Err: fmt.Errorf("invalid guidance target %q", job.TargetKey)}
	}
	switch target.Kind {
	case artifact.KindExperienceCase:
		return h.processExperience(ctx, job, target)
	case artifact.KindSkill, artifact.KindGuidance:
		return h.processSkill(ctx, job, target)
	default:
		return &Error{Kind: "protocol",
			Err: fmt.Errorf("unsupported L4 target kind %q", target.Kind)}
	}
}

func (h *GuidanceHandler) processSkill(ctx context.Context, job task.Job, target artifact.Target) error {
	target.Layer = artifact.LayerL4
	if target.LayerOrDefault() != artifact.LayerL4 || strings.TrimSpace(target.ScopeKey) == "" {
		return &Error{Kind: "protocol", Err: errors.New("skill target has no scope")}
	}
	now := h.now().UTC()
	var inputs []artifact.InputRef
	var eligible int
	var selectionTruncated bool
	var signature string
	var activeSignature string
	err := h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		var listErr error
		inputs, selectionTruncated, listErr = tx.ListDerivedInputs(ctx, target, h.maxInputs)
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
	if len(inputs) == 0 || !hasProcedureInput(inputs) {
		return h.disableSkill(ctx, job, target, now)
	}
	if activeSignature != "" && activeSignature == signature {
		return h.finishGuidance(ctx, job)
	}
	if h.provider == nil {
		return &Error{Kind: "configuration", Err: errors.New("guidance provider not configured")}
	}
	bounded, boundedTruncated := boundInputs(inputs, h.itemRunes, h.totalRunes)
	truncated := selectionTruncated || boundedTruncated || eligible > len(inputs)
	if len(bounded) == 0 || !hasProcedureInput(bounded) {
		return h.disableSkill(ctx, job, target, now)
	}

	callCtx, cancel := h.callContext(ctx)
	draft, callErr := h.provider.ComposeSkill(callCtx, skill.Request{
		ScopeKey:  target.ScopeKey,
		Topic:     target.Topic,
		Inputs:    bounded,
		Truncated: truncated,
	})
	cancel()
	if callErr != nil {
		return callErr
	}
	if !draft.Usable() {
		return &Error{Kind: "protocol", Err: errors.New("skill returned no usable guidance")}
	}
	if issue := draft.Issue(); issue != "" {
		return &Error{Kind: "protocol", Err: fmt.Errorf("skill output: %s", issue)}
	}
	title, body, payload := renderGuidanceSkill(target, draft, eligible, len(inputs), truncated, signature)
	reasonCode := ""
	if truncated {
		reasonCode = "input_truncated"
	}

	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		current, currentTruncated, err := tx.ListDerivedInputs(ctx, target, h.maxInputs)
		if err != nil {
			return err
		}
		currentEligible, err := tx.CountDerivedInputs(ctx, target)
		if err != nil {
			return err
		}
		if !sameInputs(inputs, current) || currentTruncated != selectionTruncated ||
			currentEligible != eligible || !hasProcedureInput(current) {
			return &Error{Kind: "transient", Err: errors.New("skill input changed during generation")}
		}
		if _, err := h.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Target:          target,
			Title:           title,
			Content:         body,
			Payload:         payload,
			ReasonCode:      reasonCode,
			Truncated:       truncated,
			Generator:       "aggregation:guidance",
			ProtocolVersion: "guidance-v1",
			Dependencies:    artifact.DependencyRefs(bounded),
		}); err != nil {
			return err
		}
		return finishJob(ctx, tx, job, h.now().UTC())
	})
}

func (h *GuidanceHandler) disableSkill(ctx context.Context, job task.Job, target artifact.Target, now time.Time) error {
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		reason := "no_procedure_input"
		if _, _, err := h.artifacts.DisableExisting(ctx, tx, target, reason); err != nil {
			return err
		}
		if err := tx.UpsertTargetEvaluation(ctx, artifact.TargetEvaluation{
			Kind: target.Kind, TargetKey: target.TargetKey(), ScopeKey: target.ScopeKey,
			Topic: target.Topic, Status: artifact.TargetStatusNoEligibleInput,
			ReasonCode: reason, EvaluatedAt: now,
		}); err != nil {
			return err
		}
		return finishJob(ctx, tx, job, h.now().UTC())
	})
}

func (h *GuidanceHandler) finishGuidance(ctx context.Context, job task.Job) error {
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		return finishJob(ctx, tx, job, h.now().UTC())
	})
}

func (h *GuidanceHandler) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if h.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, h.timeout)
}

func hasProcedureInput(inputs []artifact.InputRef) bool {
	for _, input := range inputs {
		if input.Type == artifact.DependencyMemory &&
			strings.EqualFold(strings.TrimSpace(input.Kind), "procedure") {
			return true
		}
	}
	return false
}

func cancelClaimed(ctx context.Context, tx Tx, job task.Job, now time.Time) error {
	ok, err := tx.RescheduleJob(ctx, job.ID, job.Revision, task.RescheduleInput{
		Status: task.StatusCancelled,
		Now:    now,
	})
	if err != nil {
		return err
	}
	if !ok {
		return task.ErrRevisionChanged
	}
	return nil
}

func renderGuidanceSkill(target artifact.Target, draft skill.Draft, eligible, selected int,
	truncated bool, signature string) (string, string, json.RawMessage) {
	name := truncateRunes(strings.TrimSpace(draft.Name), maxGuidanceNameRunes)
	if name == "" {
		if target.Topic != "" {
			name = target.Topic + " 工作流"
		} else {
			name = target.ScopeKey + " 工作流"
		}
	}
	description := truncateRunes(strings.TrimSpace(draft.Description), maxGuidanceDescription)
	if description == "" {
		description = name
	}
	body := normalizeSkillBody(draft.Content)
	if body == "" {
		body = renderSkillSections(draft)
	}
	body = truncateRunes(body, maxGuidanceContentRunes)
	payload, err := json.Marshal(map[string]any{
		"name":                    name,
		"description":             description,
		"scope_key":               target.ScopeKey,
		"topic":                   target.Topic,
		"preconditions":           nonNilStrings(draft.Preconditions),
		"steps":                   nonNilStrings(draft.Steps),
		"stop_conditions":         nonNilStrings(draft.StopConditions),
		"completion_checks":       nonNilStrings(draft.CompletionChecks),
		"counterexamples":         nonNilStrings(draft.Counterexamples),
		"unknown_fields":          unknownSkillFields(draft),
		"application_report_only": true,
		"input_selection": map[string]any{
			"eligible":  eligible,
			"included":  selected,
			"truncated": truncated,
		},
		"input_signature": signature,
		"truncated":       truncated,
	})
	if err != nil {
		payload = []byte("{}")
	}
	return name, body, json.RawMessage(payload)
}

// unknownSkillFields records which optional structure the model did not supply
// instead of inventing it from weak inputs.
func unknownSkillFields(draft skill.Draft) []string {
	result := []string{}
	if len(cleanStrings(draft.Preconditions)) == 0 {
		result = append(result, "preconditions")
	}
	if len(cleanStrings(draft.Steps)) == 0 {
		result = append(result, "steps")
	}
	if len(cleanStrings(draft.CompletionChecks)) == 0 {
		result = append(result, "completion_checks")
	}
	if len(cleanStrings(draft.Counterexamples)) == 0 {
		result = append(result, "counterexamples")
	}
	return result
}

func normalizeSkillBody(content string) string {
	body := strings.TrimSpace(content)
	if !strings.HasPrefix(body, "---") {
		return body
	}
	lines := strings.Split(body, "\n")
	end := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return body
	}
	return strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
}

func renderSkillSections(draft skill.Draft) string {
	var builder strings.Builder
	writeSkillSection(&builder, "前置条件", draft.Preconditions)
	writeSkillSection(&builder, "步骤", draft.Steps)
	writeSkillSection(&builder, "停止条件", draft.StopConditions)
	writeSkillSection(&builder, "完成检查", draft.CompletionChecks)
	writeSkillSection(&builder, "反例", draft.Counterexamples)
	return strings.TrimSpace(builder.String())
}

func writeSkillSection(builder *strings.Builder, title string, values []string) {
	values = cleanStrings(values)
	if len(values) == 0 {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n\n")
	}
	builder.WriteString("## ")
	builder.WriteString(title)
	builder.WriteString("\n")
	for index, value := range values {
		if title == "步骤" {
			fmt.Fprintf(builder, "%d. %s\n", index+1, value)
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(value)
		builder.WriteString("\n")
	}
}

func cleanStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result = append(result, value)
	}
	return result
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
