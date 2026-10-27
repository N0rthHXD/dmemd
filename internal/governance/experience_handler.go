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

// processExperience builds one traceable case from exactly one recorded run plus
// a bounded set of related procedures and events. The application-reported
// outcome is copied verbatim; the model cannot upgrade it or attach arbitrary
// sources.
func (h *AggregateHandler) processExperience(ctx context.Context, job task.Job) error {
	experienceID, ok := task.ParseExperienceKey(job.TargetKey)
	if !ok {
		return &ingest.ProviderError{Operation: "protocol", Retryable: false,
			Err: fmt.Errorf("invalid experience target %q", job.TargetKey)}
	}
	now := h.now().UTC()
	var run ExperienceRun
	var inputs []artifact.MemoryInput
	var truncated bool
	var skillContext *artifact.SkillContext
	err := h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		current, err := tx.GetExperienceRun(ctx, experienceID)
		if err != nil {
			return err
		}
		if current.State != ExperienceRecorded {
			return ErrRevisionChanged
		}
		run = current
		var listErr error
		inputs, truncated, listErr = tx.ListAggregationInputs(ctx,
			artifact.Target{Kind: artifact.KindExperienceCase, ScopeKey: current.ScopeKey}, now, h.maxInputs)
		if listErr != nil {
			return listErr
		}
		if current.SkillID != "" {
			skillContext = loadSkillContext(ctx, tx, current.SkillID, current.SkillVersion)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrExperienceNotFound) {
			return CancelMissingTask(ctx, h.store, job, now)
		}
		return err
	}
	if h.experience == nil {
		return &ingest.ProviderError{Operation: "configuration", Retryable: true,
			Err: errors.New("经验案例模型未配置")}
	}
	bounded, truncatedInputs := boundAggregateInputs(inputs, h.itemRunes, h.totalRunes)
	truncated = truncated || truncatedInputs
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
	result, callErr := h.experience.ComposeExperience(callCtx, artifact.ExperienceRequest{
		Run:       experienceInputFromRun(run),
		ScopeKey:  run.ScopeKey,
		Skill:     skillContext,
		Memories:  bounded,
		Truncated: truncated,
	})
	cancel()
	h.limiter.Record("experience_case", len(bounded), aggregateContextSize(bounded), started, callErr)
	if callErr != nil {
		return callErr
	}
	if !usableExperienceResult(result) {
		return &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ingest.ErrProtocol}
	}
	content, title, payload := renderExperienceCase(run, result, bounded)
	reasonCode := ""
	if truncated {
		reasonCode = "input_truncated"
	}

	topics := memoryTopics(bounded)
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		latest, err := tx.GetExperienceRun(ctx, experienceID)
		if err != nil {
			return ErrRevisionChanged
		}
		if latest.State != ExperienceRecorded {
			return ErrRevisionChanged
		}
		currentInputs, _, err := tx.ListAggregationInputs(ctx,
			artifact.Target{Kind: artifact.KindExperienceCase, ScopeKey: latest.ScopeKey}, h.now().UTC(), h.maxInputs)
		if err != nil {
			return err
		}
		if !sameMemoryInputs(inputs, currentInputs) {
			return &ingest.ProviderError{Operation: "response", Retryable: true,
				Err: errors.New("experience input changed during generation")}
		}
		if _, err := h.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Kind:            artifact.KindExperienceCase,
			TargetKey:       artifact.ExperienceTarget(experienceID).TargetKey(),
			ScopeKey:        latest.ScopeKey,
			ExperienceID:    experienceID,
			Title:           title,
			Content:         content,
			Payload:         payload,
			ReasonCode:      reasonCode,
			Truncated:       truncated,
			Generator:       "governance:experience",
			ProtocolVersion: "experience-v2",
			RunID:           job.ID + ":" + fmt.Sprint(job.Revision),
			Memories:        bounded,
			Runs:            []artifact.RunRef{{ExperienceID: experienceID}},
		}); err != nil {
			return err
		}
		if err := h.artifacts.ExperienceChanged(ctx, tx, latest.ScopeKey, topics,
			latest.SkillID, experienceID, "experience-case:"+experienceID, h.now().UTC()); err != nil {
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

func experienceTarget(targetKey string) bool {
	_, ok := task.ParseExperienceKey(targetKey)
	return ok
}

func experienceInputFromRun(run ExperienceRun) artifact.ExperienceInput {
	return artifact.ExperienceInput{
		ID:           run.ID,
		RunID:        run.RunID,
		ScopeKey:     run.ScopeKey,
		Goal:         run.Goal,
		Conditions:   run.Conditions,
		Actions:      run.Actions,
		Outcome:      string(run.Outcome),
		SkillID:      run.SkillID,
		SkillVersion: run.SkillVersion,
		CreatedAt:    run.CreatedAt,
	}
}

func loadSkillContext(ctx context.Context, tx Tx, skillID string, version int) *artifact.SkillContext {
	item, err := tx.GetArtifact(ctx, skillID)
	if err != nil || item.Kind != artifact.KindSkill || item.CurrentVersion <= 0 {
		return nil
	}
	if version <= 0 || version > item.CurrentVersion {
		version = item.CurrentVersion
	}
	revision, err := tx.GetArtifactRevision(ctx, skillID, version)
	if err != nil {
		return nil
	}
	return &artifact.SkillContext{
		ID: item.ID, Version: version, Title: revision.Title, Content: revision.Content,
	}
}

func usableExperienceResult(result artifact.ExperienceResult) bool {
	return strings.TrimSpace(result.Content) != "" ||
		strings.TrimSpace(result.Problem) != "" ||
		len(result.Lessons) > 0 || len(result.FailureModes) > 0 || len(result.Actions) > 0
}

func renderExperienceCase(run ExperienceRun, result artifact.ExperienceResult,
	memories []artifact.MemoryInput) (string, string, json.RawMessage) {
	problem := strings.TrimSpace(result.Problem)
	if problem == "" {
		problem = strings.TrimSpace(run.Goal)
	}
	problem = truncateRunes(problem, defaultAggregateTitleRunes)
	preconditions := nonEmpty(result.Preconditions, run.Conditions)
	actions := nonEmpty(result.Actions, run.Actions)
	language := experienceLanguage(run, memories)
	content := strings.TrimSpace(result.Content)
	if content == "" {
		content = renderExperienceText(problem, preconditions, actions, run.Outcome,
			result.Lessons, result.FailureModes, language)
	}
	content = truncateRunes(content, maxAggregateContentRunes)
	// This service-owned statement cannot be omitted by model-authored content.
	if language == "en" {
		content = "This is an application report, not independently verified. Outcome: " +
			string(run.Outcome) + ".\n\n" + content
	} else {
		content = "这是应用报告，不代表独立验证。结果：" + string(run.Outcome) + "。\n\n" + content
	}

	memoryRefs := make([]map[string]any, 0, len(memories))
	for _, memory := range memories {
		if memory.ID == "" || memory.Version <= 0 {
			continue
		}
		memoryRefs = append(memoryRefs, map[string]any{"memory_id": memory.ID, "version": memory.Version})
	}
	payload, err := json.Marshal(map[string]any{
		"problem":         problem,
		"scope_key":       run.ScopeKey,
		"topics":          memoryTopics(memories),
		"language":        language,
		"preconditions":   preconditions,
		"actions":         actions,
		"outcome":         string(run.Outcome),
		"verification":    "application_report_not_independently_verified",
		"lessons":         nonNilStrings(result.Lessons),
		"failure_modes":   nonNilStrings(result.FailureModes),
		"memory_refs":     memoryRefs,
		"experience_refs": []string{run.ID},
	})
	if err != nil {
		payload = []byte("{}")
	}
	return content, problem, json.RawMessage(payload)
}

func renderExperienceText(problem string, preconditions, actions []string, outcome ExperienceOutcome,
	lessons, failureModes []string, language string) string {
	var builder strings.Builder
	if language == "en" {
		builder.WriteString("Problem: ")
		builder.WriteString(problem)
		builder.WriteString("\n\nConditions:\n")
		writeBulletsWithFallback(&builder, preconditions, "- not provided\n")
		builder.WriteString("\nActions:\n")
		writeBulletsWithFallback(&builder, actions, "- not provided\n")
		builder.WriteString("\nOutcome (application report): ")
		builder.WriteString(string(outcome))
		builder.WriteString("\n")
		if len(lessons) > 0 {
			builder.WriteString("\nLessons:\n")
			writeBulletsWithFallback(&builder, lessons, "- not provided\n")
		}
		if len(failureModes) > 0 {
			builder.WriteString("\nFailure modes:\n")
			writeBulletsWithFallback(&builder, failureModes, "- not provided\n")
		}
		return strings.TrimSpace(builder.String())
	}
	builder.WriteString("问题：")
	builder.WriteString(problem)
	builder.WriteString("\n\n条件：\n")
	writeBullets(&builder, preconditions)
	builder.WriteString("\n行动：\n")
	writeBullets(&builder, actions)
	builder.WriteString("\n结果（应用报告）：")
	builder.WriteString(string(outcome))
	builder.WriteString("\n")
	if len(lessons) > 0 {
		builder.WriteString("\n教训：\n")
		writeBullets(&builder, lessons)
	}
	if len(failureModes) > 0 {
		builder.WriteString("\n失败模式：\n")
		writeBullets(&builder, failureModes)
	}
	return strings.TrimSpace(builder.String())
}

func experienceLanguage(run ExperienceRun, memories []artifact.MemoryInput) string {
	text := run.Goal + strings.Join(run.Conditions, "") + strings.Join(run.Actions, "")
	for _, memory := range memories {
		text += memory.Title + memory.Content
	}
	if text == "" {
		return "zh"
	}
	cjk, total := 0, 0
	for _, r := range text {
		if r == ' ' || r == '\n' || r == '\t' {
			continue
		}
		total++
		if r >= 0x4e00 && r <= 0x9fff {
			cjk++
		}
	}
	if total > 0 && cjk*2 < total {
		return "en"
	}
	return "zh"
}

func writeBullets(builder *strings.Builder, values []string) {
	writeBulletsWithFallback(builder, values, "- 未提供\n")
}

func writeBulletsWithFallback(builder *strings.Builder, values []string, fallback string) {
	if len(values) == 0 {
		builder.WriteString(fallback)
		return
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(value)
		builder.WriteString("\n")
	}
}

func nonEmpty(primary, fallback []string) []string {
	cleaned := cleanStrings(primary)
	if len(cleaned) > 0 {
		return cleaned
	}
	return cleanStrings(fallback)
}

func memoryTopics(inputs []artifact.MemoryInput) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, input := range inputs {
		for _, topic := range input.Topics {
			topic = strings.ToLower(strings.TrimSpace(topic))
			if topic == "" {
				continue
			}
			if _, ok := seen[topic]; ok {
				continue
			}
			seen[topic] = struct{}{}
			result = append(result, topic)
		}
	}
	return result
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
