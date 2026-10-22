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
	"github.com/N0rthHXD/dmemd/internal/skill"
)

const (
	defaultSkillTimeout          = time.Minute
	defaultSkillInputs           = 32
	defaultSkillItemRunes        = 2000
	maxSkillContentRunes         = 12000
	defaultSkillNameRunes        = 120
	defaultSkillDescriptionRunes = 500
)

// SkillProvider is the one guidance-workflow model operation.
type SkillProvider interface {
	ComposeSkill(ctx context.Context, request skill.Request) (skill.Draft, error)
}

// SkillHandler builds and enables guidance skills from bounded procedures and
// reported experience. It never generates executable scripts or tool grants.
type SkillHandler struct {
	store     Store
	artifacts *artifact.Service
	provider  SkillProvider
	limiter   *Limiter
	now       func() time.Time
	timeout   time.Duration
	maxInputs int
	itemRunes int
}

// SkillOption customizes the skill handler.
type SkillOption func(*SkillHandler)

// WithSkillClock overrides the handler clock for deterministic tests.
func WithSkillClock(now func() time.Time) SkillOption {
	return func(handler *SkillHandler) {
		if now != nil {
			handler.now = now
		}
	}
}

// WithSkillTimeout bounds one skill-generation model call.
func WithSkillTimeout(timeout time.Duration) SkillOption {
	return func(handler *SkillHandler) {
		if timeout > 0 {
			handler.timeout = timeout
		}
	}
}

// WithSkillMaxInputs overrides the per-call input cap.
func WithSkillMaxInputs(limit int) SkillOption {
	return func(handler *SkillHandler) {
		if limit > 0 {
			handler.maxInputs = limit
		}
	}
}

// NewSkillHandler assembles the skill processor.
func NewSkillHandler(store Store, artifacts *artifact.Service, provider SkillProvider,
	limiter *Limiter, options ...SkillOption) *SkillHandler {
	handler := &SkillHandler{
		store:     store,
		artifacts: artifacts,
		provider:  provider,
		limiter:   limiter,
		now:       time.Now,
		timeout:   defaultSkillTimeout,
		maxInputs: defaultSkillInputs,
		itemRunes: defaultSkillItemRunes,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// Process implements Handler.
func (h *SkillHandler) Process(ctx context.Context, job task.Job) error {
	if job.Kind != task.KindBuildSkill {
		return fmt.Errorf("skill handler received kind %s", job.Kind)
	}
	if h.store == nil || h.artifacts == nil {
		return errors.New("governance: skill handler dependencies not configured")
	}
	scopeKey, topic, ok := task.ParseSkillKey(job.TargetKey)
	if !ok {
		return &ingest.ProviderError{Operation: "protocol", Retryable: false,
			Err: fmt.Errorf("invalid skill target %q", job.TargetKey)}
	}
	now := h.now().UTC()
	target := artifact.Target{Kind: artifact.KindSkill, ScopeKey: scopeKey, Topic: topic}
	var procedures []artifact.MemoryInput
	var runs []ExperienceRun
	var truncated bool
	err := h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		var listErr error
		procedures, truncated, listErr = tx.ListAggregationInputs(ctx, target, now, h.maxInputs)
		if listErr != nil {
			return listErr
		}
		var runsTruncated bool
		runs, runsTruncated, listErr = loadSkillRuns(ctx, tx, job.TargetKey, scopeKey, h.maxInputs)
		truncated = truncated || runsTruncated
		return listErr
	})
	if err != nil {
		return err
	}
	if len(procedures) == 0 && len(runs) == 0 {
		return h.disable(ctx, job, target)
	}
	if h.provider == nil {
		return &ingest.ProviderError{Operation: "configuration", Retryable: true,
			Err: errors.New("Skill 模型未配置")}
	}
	boundedProcedures, truncatedProcedures := boundAggregateInputs(procedures, h.itemRunes, 16000)
	boundedRuns, truncatedRuns := boundExperienceInputs(runs, h.itemRunes, 16000)
	truncated = truncated || truncatedProcedures || truncatedRuns
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
	draft, callErr := h.provider.ComposeSkill(callCtx, skill.Request{
		ScopeKey:    scopeKey,
		Topic:       topic,
		Procedures:  boundedProcedures,
		Experiences: experienceInputs(boundedRuns),
		Truncated:   truncated,
	})
	cancel()
	if callErr != nil {
		return callErr
	}
	if !draft.Usable() {
		return &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ingest.ErrProtocol}
	}
	name, body, payload := renderSkill(target, draft)
	reasonCode := ""
	if truncated {
		reasonCode = "input_truncated"
	}
	runRefs := make([]artifact.RunRef, 0, len(boundedRuns))
	for _, run := range boundedRuns {
		runRefs = append(runRefs, artifact.RunRef{ExperienceID: run.ID})
	}

	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		for _, run := range boundedRuns {
			current, err := tx.GetExperienceRun(ctx, run.ID)
			if err != nil || current.State != ExperienceRecorded {
				return ErrRevisionChanged
			}
		}
		currentProcedures, _, err := tx.ListAggregationInputs(ctx, target, now, h.maxInputs)
		if err != nil {
			return err
		}
		currentRuns, _, err := loadSkillRuns(ctx, tx, job.TargetKey, scopeKey, h.maxInputs)
		if err != nil {
			return err
		}
		if !sameMemoryInputs(procedures, currentProcedures) || !sameRunIDs(runs, currentRuns) {
			return &ingest.ProviderError{Operation: "response", Retryable: true,
				Err: errors.New("skill input changed during generation")}
		}
		if _, err := h.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Kind:            artifact.KindSkill,
			TargetKey:       target.TargetKey(),
			ScopeKey:        scopeKey,
			Topic:           topic,
			Title:           name,
			Content:         body,
			Payload:         payload,
			ReasonCode:      reasonCode,
			Truncated:       truncated,
			Generator:       "governance:skill",
			ProtocolVersion: "skill-v1",
			Memories:        boundedProcedures,
			Runs:            runRefs,
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

func (h *SkillHandler) disable(ctx context.Context, job task.Job, target artifact.Target) error {
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

func loadSkillRuns(ctx context.Context, tx Tx, targetKey, scopeKey string, limit int) ([]ExperienceRun, bool, error) {
	seen := map[string]struct{}{}
	result := []ExperienceRun{}
	truncated := false
	appendRuns := func(items []ExperienceRun) {
		for _, item := range items {
			if _, duplicate := seen[item.ID]; duplicate {
				continue
			}
			seen[item.ID] = struct{}{}
			result = append(result, item)
		}
	}
	byScope, err := tx.ListExperienceRunsByScope(ctx, scopeKey, limit+1)
	if err != nil {
		return nil, false, err
	}
	appendRuns(byScope)
	if len(byScope) > limit {
		truncated = true
	}
	if item, err := tx.GetArtifactByTarget(ctx, artifact.KindSkill, targetKey); err == nil {
		bySkill, err := tx.ListExperienceRunsBySkill(ctx, item.ID, limit+1)
		if err != nil {
			return nil, false, err
		}
		appendRuns(bySkill)
		if len(bySkill) > limit {
			truncated = true
		}
	} else if !errors.Is(err, artifact.ErrNotFound) {
		return nil, false, err
	}
	if len(result) > limit {
		result = result[:limit]
		truncated = true
	}
	return result, truncated, nil
}

func sameMemoryInputs(left, right []artifact.MemoryInput) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Version != right[index].Version {
			return false
		}
	}
	return true
}

func sameRunIDs(left, right []ExperienceRun) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID {
			return false
		}
	}
	return true
}

func boundExperienceInputs(runs []ExperienceRun, itemRunes, totalRunes int) ([]ExperienceRun, bool) {
	truncated := false
	remaining := totalRunes
	result := make([]ExperienceRun, 0, len(runs))
	for _, run := range runs {
		if remaining <= 0 {
			truncated = true
			break
		}
		bounded := run
		goal := strings.TrimSpace(run.Goal)
		bounded.Goal = truncateRunes(goal, itemRunes)
		if len([]rune(bounded.Goal)) < len([]rune(goal)) {
			truncated = true
		}
		bounded.Conditions = boundStringList(run.Conditions, itemRunes, &remaining, &truncated)
		bounded.Actions = boundStringList(run.Actions, itemRunes, &remaining, &truncated)
		remaining -= len([]rune(bounded.Goal))
		if remaining < 0 {
			remaining = 0
		}
		result = append(result, bounded)
	}
	return result, truncated
}

func boundStringList(values []string, itemRunes int, remaining *int, truncated *bool) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if *remaining <= 0 {
			*truncated = true
			break
		}
		budget := itemRunes
		if budget > *remaining {
			budget = *remaining
		}
		bounded := truncateRunes(value, budget)
		if len([]rune(bounded)) < len([]rune(value)) {
			*truncated = true
		}
		*remaining -= len([]rune(bounded))
		result = append(result, bounded)
	}
	return result
}

func experienceInputs(runs []ExperienceRun) []artifact.ExperienceInput {
	result := make([]artifact.ExperienceInput, 0, len(runs))
	for _, run := range runs {
		result = append(result, experienceInputFromRun(run))
	}
	return result
}

func renderSkill(target artifact.Target, draft skill.Draft) (string, string, json.RawMessage) {
	name := truncateRunes(strings.TrimSpace(draft.Name), defaultSkillNameRunes)
	if name == "" {
		if target.Topic != "" {
			name = target.Topic + " 工作流"
		} else {
			name = target.ScopeKey + " 工作流"
		}
	}
	description := truncateRunes(strings.TrimSpace(draft.Description), defaultSkillDescriptionRunes)
	if description == "" {
		description = name
	}
	body := normalizeSkillBody(draft.Content)
	if body == "" {
		body = renderSkillSections(draft)
	}
	body = truncateRunes(body, maxSkillContentRunes)
	payload, err := json.Marshal(map[string]any{
		"name":              name,
		"description":       description,
		"scope_key":         target.ScopeKey,
		"topic":             target.Topic,
		"preconditions":     nonNilStrings(draft.Preconditions),
		"steps":             nonNilStrings(draft.Steps),
		"stop_conditions":   nonNilStrings(draft.StopConditions),
		"completion_checks": nonNilStrings(draft.CompletionChecks),
		"counterexamples":   nonNilStrings(draft.Counterexamples),
	})
	if err != nil {
		payload = []byte("{}")
	}
	return name, body, json.RawMessage(payload)
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
