package aggregation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

// processExperience builds one L4 case from exactly one recorded application
// run plus bounded stable memories and lower-layer aggregates. The outcome and
// verification statement are copied by code from the stored run.
func (h *GuidanceHandler) processExperience(ctx context.Context, job task.Job, target artifact.Target) error {
	experienceID := strings.TrimSpace(target.ExperienceID)
	if experienceID == "" {
		return &Error{Kind: "protocol", Err: errors.New("experience target has no run identifier")}
	}
	now := h.now().UTC()
	var run artifact.ExperienceInput
	var inputs []artifact.InputRef
	var eligible int
	var selectionTruncated bool
	var signature string
	var activeSignature string
	var scopeKey string
	err := h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		recorded, err := tx.GetExperienceRun(ctx, experienceID)
		if errors.Is(err, governance.ErrExperienceNotFound) {
			return cancelClaimed(ctx, tx, job, now)
		}
		if err != nil {
			return err
		}
		if recorded.State != governance.ExperienceRecorded {
			return cancelClaimed(ctx, tx, job, now)
		}
		scopeKey = recorded.ScopeKey
		run = experienceInputFromRun(recorded)
		target.Layer = artifact.LayerL4
		target.Kind = artifact.KindExperienceCase
		target.ExperienceID = experienceID
		target.ScopeKey = scopeKey
		if strings.TrimSpace(scopeKey) != "" {
			var listErr error
			inputs, selectionTruncated, listErr = tx.ListDerivedInputs(ctx, target, h.maxInputs)
			if listErr != nil {
				return listErr
			}
			if eligible, listErr = tx.CountDerivedInputs(ctx, target); listErr != nil {
				return listErr
			}
		}
		signature = experienceInputSignature(run, inputs, eligible, selectionTruncated)
		activeSignature, err = currentActiveSignature(ctx, tx, target)
		return err
	})
	if err != nil {
		return err
	}
	if activeSignature != "" && activeSignature == signature {
		return h.finishGuidance(ctx, job)
	}
	if h.provider == nil {
		return &Error{Kind: "configuration", Err: errors.New("guidance provider not configured")}
	}
	bounded, boundedTruncated := boundInputs(inputs, h.itemRunes, h.totalRunes)
	truncated := selectionTruncated || boundedTruncated || eligible > len(inputs)

	callCtx, cancel := h.callContext(ctx)
	result, callErr := h.provider.ComposeExperience(callCtx, artifact.ExperienceRequest{
		Run:       run,
		ScopeKey:  scopeKey,
		Inputs:    bounded,
		Truncated: truncated,
	})
	cancel()
	if callErr != nil {
		return callErr
	}
	content := truncateRunes(strings.TrimSpace(result.Content), maxGuidanceContentRunes)
	if content == "" {
		return &Error{Kind: "protocol", Err: errors.New("experience case returned empty content")}
	}
	title, body, payload := renderExperienceCase(run, content, bounded, eligible, truncated, signature)
	reasonCode := ""
	if truncated {
		reasonCode = "input_truncated"
	}

	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkJob(ctx, tx, job); err != nil {
			return err
		}
		latest, err := tx.GetExperienceRun(ctx, experienceID)
		if err != nil || latest.State != governance.ExperienceRecorded {
			return task.ErrRevisionChanged
		}
		var current []artifact.InputRef
		var currentEligible int
		var currentTruncated bool
		if strings.TrimSpace(scopeKey) != "" {
			current, currentTruncated, err = tx.ListDerivedInputs(ctx, target, h.maxInputs)
			if err != nil {
				return err
			}
			if currentEligible, err = tx.CountDerivedInputs(ctx, target); err != nil {
				return err
			}
		}
		if !sameInputs(inputs, current) || currentTruncated != selectionTruncated ||
			currentEligible != eligible ||
			experienceInputSignature(experienceInputFromRun(latest), current,
				currentEligible, currentTruncated) != signature {
			return &Error{Kind: "transient", Err: errors.New("experience input changed during generation")}
		}
		if _, err := h.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Target:          target,
			Title:           title,
			Content:         body,
			Payload:         payload,
			ReasonCode:      reasonCode,
			Truncated:       truncated,
			Generator:       "aggregation:experience",
			ProtocolVersion: "experience-v1",
			Dependencies: artifact.MergeDependencies(
				artifact.DependencyRefs(bounded),
				artifact.ExperienceDependencies([]string{experienceID}),
			),
		}); err != nil {
			return err
		}
		return finishJob(ctx, tx, job, h.now().UTC())
	})
}

func experienceInputFromRun(run governance.ExperienceRun) artifact.ExperienceInput {
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

func experienceInputSignature(run artifact.ExperienceInput, inputs []artifact.InputRef,
	eligible int, truncated bool) string {
	parts := []string{
		"experience-case",
		run.ID,
		run.Goal,
		strings.Join(run.Conditions, "\x00"),
		strings.Join(run.Actions, "\x00"),
		run.Outcome,
		run.SkillID,
		fmt.Sprint(run.SkillVersion),
		inputSignature(inputs, eligible, truncated),
	}
	return task.InputHash(parts...)
}

func renderExperienceCase(run artifact.ExperienceInput, content string,
	inputs []artifact.InputRef, eligible int, truncated bool,
	signature string) (string, string, json.RawMessage) {
	title := truncateRunes(strings.TrimSpace(run.Goal), maxGuidanceTitleRunes)
	if title == "" {
		title = "经验 " + run.ID
	}
	content = truncateRunes(strings.TrimSpace(content), maxGuidanceContentRunes)
	body := "这是应用报告，不代表独立验证。结果：" + run.Outcome + "。\n\n" + content
	payload, err := json.Marshal(map[string]any{
		"problem":         title,
		"scope_key":       run.ScopeKey,
		"outcome":         run.Outcome,
		"verification":    "application_report_not_independently_verified",
		"experience_refs": []string{run.ID},
		"input_selection": map[string]any{
			"eligible":  eligible,
			"included":  len(inputs),
			"truncated": truncated,
		},
		"input_signature": signature,
		"truncated":       truncated,
	})
	if err != nil {
		payload = []byte("{}")
	}
	return title, body, json.RawMessage(payload)
}
