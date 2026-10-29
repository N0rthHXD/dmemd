package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ExperienceOutcome is the application's own report. The service never upgrades
// it to a verified result.
type ExperienceOutcome string

const (
	OutcomeSuccess        ExperienceOutcome = "success"
	OutcomeFailure        ExperienceOutcome = "failure"
	OutcomeUnknown        ExperienceOutcome = "unknown"
	OutcomePartialSuccess ExperienceOutcome = "partial_success"
)

// Known reports whether outcome is part of the first-release vocabulary.
func (o ExperienceOutcome) Known() bool {
	switch o {
	case OutcomeSuccess, OutcomeFailure, OutcomeUnknown, OutcomePartialSuccess:
		return true
	default:
		return false
	}
}

// ExperienceState is the lifecycle of one reported result.
type ExperienceState string

const (
	ExperienceRecorded ExperienceState = "recorded"
	ExperienceRevoked  ExperienceState = "revoked"
)

// ErrInvalidExperience marks callers' malformed feedback.
var ErrInvalidExperience = errors.New("invalid experience feedback")

// ErrExperienceNotFound marks an unknown result identifier.
var ErrExperienceNotFound = errors.New("experience not found")

// ExperienceRun is one saved application result. RunID is only an optional
// client deduplication key; ID is always the internal authority.
type ExperienceRun struct {
	ID           string            `json:"id"`
	RunID        string            `json:"run_id,omitempty"`
	SourceID     string            `json:"source_id"`
	SkillID      string            `json:"skill_id,omitempty"`
	SkillVersion int               `json:"skill_version,omitempty"`
	ScopeKey     string            `json:"scope_key,omitempty"`
	Goal         string            `json:"goal"`
	Conditions   []string          `json:"conditions"`
	Actions      []string          `json:"actions"`
	Outcome      ExperienceOutcome `json:"outcome"`
	State        ExperienceState   `json:"state"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	RevokedAt    *time.Time        `json:"revoked_at,omitempty"`
}

// ExperienceSubmission is one completed result supplied by an application.
// Content is the original report payload; when absent the structured fields are
// stored as the source payload instead.
type ExperienceSubmission struct {
	RunID        string
	AppID        string
	ExternalID   string
	ScopeKey     string
	Goal         string
	Conditions   []string
	Actions      []string
	Outcome      ExperienceOutcome
	SkillID      string
	SkillVersion int
	Content      json.RawMessage
}

// ExperienceReceipt is returned for both new and duplicate submissions.
type ExperienceReceipt struct {
	ID        string            `json:"id"`
	SourceID  string            `json:"source_id"`
	RunID     string            `json:"run_id,omitempty"`
	Outcome   ExperienceOutcome `json:"outcome"`
	State     ExperienceState   `json:"state"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// ExperienceService is the public result-feedback capability.
type ExperienceService interface {
	Submit(ctx context.Context, submission ExperienceSubmission) (ExperienceReceipt, error)
	Retract(ctx context.Context, id string) (ExperienceReceipt, error)
}

// ExperienceServiceAPI is the concrete first-release implementation.
type ExperienceServiceAPI struct {
	store Store
	now   func() time.Time
}

// ExperienceOption customizes the service for tests.
type ExperienceOption func(*ExperienceServiceAPI)

// WithExperienceClock overrides the service clock.
func WithExperienceClock(now func() time.Time) ExperienceOption {
	return func(service *ExperienceServiceAPI) {
		if now != nil {
			service.now = now
		}
	}
}

// NewExperienceService assembles result feedback over the short governance
// transaction. Model work stays in the background handler.
func NewExperienceService(store Store, options ...ExperienceOption) *ExperienceServiceAPI {
	service := &ExperienceServiceAPI{store: store, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

var _ ExperienceService = (*ExperienceServiceAPI)(nil)

// Submit stores the original report as a source, saves the application-reported
// result and queues its experience case. A repeated non-empty run_id returns the
// existing receipt without comparing or overwriting the request body.
func (s *ExperienceServiceAPI) Submit(ctx context.Context, submission ExperienceSubmission) (ExperienceReceipt, error) {
	if s.store == nil {
		return ExperienceReceipt{}, errors.New("governance: experience store not configured")
	}
	runID := strings.TrimSpace(submission.RunID)
	now := s.now().UTC()

	var receipt ExperienceReceipt
	err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if runID != "" {
			existing, err := tx.GetExperienceRunByRunID(ctx, runID)
			if err == nil {
				receipt = receiptFromRun(existing)
				return nil
			}
			if !errors.Is(err, ErrExperienceNotFound) {
				return err
			}
		}

		outcome := submission.Outcome
		if outcome == "" {
			outcome = OutcomeUnknown
		}
		if !outcome.Known() {
			return fmt.Errorf("%w: outcome %q", ErrInvalidExperience, outcome)
		}
		goal := strings.TrimSpace(submission.Goal)
		if goal == "" {
			return fmt.Errorf("%w: goal is required", ErrInvalidExperience)
		}
		submission.Outcome = outcome
		content, err := experienceSourceContent(submission)
		if err != nil {
			return err
		}

		src := source.Source{
			ID:         idgen.NewUUID(),
			AppID:      submission.AppID,
			ExternalID: submission.ExternalID,
			ScopeKey:   memory.NormalizeScopeKey(submission.ScopeKey),
			Content:    content,
			CreatedAt:  now,
		}
		run := ExperienceRun{
			ID:           idgen.NewUUID(),
			RunID:        runID,
			SourceID:     src.ID,
			SkillID:      strings.TrimSpace(submission.SkillID),
			SkillVersion: submission.SkillVersion,
			ScopeKey:     memory.NormalizeScopeKey(submission.ScopeKey),
			Goal:         goal,
			Conditions:   cleanStrings(submission.Conditions),
			Actions:      cleanStrings(submission.Actions),
			Outcome:      outcome,
			State:        ExperienceRecorded,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		if err := tx.InsertExperienceRun(ctx, run); err != nil {
			return err
		}
		if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:          task.KindBuildGuidance,
			TargetKey:     artifact.ExperienceTarget(run.ID).TargetKey(),
			InputHash:     task.InputHash("experience-case", run.ID, string(run.Outcome), strconv.Itoa(run.SkillVersion)),
			NextAttemptAt: &now,
			Now:           now,
		}); err != nil {
			return fmt.Errorf("登记经验案例任务: %w", err)
		}
		if err := enqueueExperienceSkills(ctx, tx, run, now); err != nil {
			return err
		}
		receipt = receiptFromRun(run)
		return nil
	})
	if err != nil {
		return ExperienceReceipt{}, err
	}
	return receipt, nil
}

// Retract marks one application report as revoked. The case leaves default
// recall, pending generation cannot commit and every skill that used the run is
// marked stale and re-queued. The operation is idempotent.
func (s *ExperienceServiceAPI) Retract(ctx context.Context, id string) (ExperienceReceipt, error) {
	if s.store == nil {
		return ExperienceReceipt{}, errors.New("governance: experience store not configured")
	}
	now := s.now().UTC()
	var receipt ExperienceReceipt
	err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		run, err := tx.GetExperienceRun(ctx, id)
		if err != nil {
			return err
		}
		if run.State == ExperienceRevoked {
			receipt = receiptFromRun(run)
			return nil
		}
		if err := tx.MarkExperienceRunRevoked(ctx, id, now); err != nil {
			return err
		}
		run.State = ExperienceRevoked
		run.UpdatedAt = now
		run.RevokedAt = &now

		caseTarget := artifact.ExperienceTarget(id).TargetKey()
		if job, err := tx.GetGovernanceJobByTarget(ctx, task.KindBuildGuidance, caseTarget); err == nil {
			if !job.Status.Terminal() {
				if _, err := tx.CancelJob(ctx, task.KindBuildGuidance, caseTarget, now); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, task.ErrNotFound) {
			return err
		}
		if err := invalidateCaseForRun(ctx, tx, id, now); err != nil {
			return err
		}
		if err := invalidateExperienceDependents(ctx, tx, id, now); err != nil {
			return err
		}
		receipt = receiptFromRun(run)
		return nil
	})
	if err != nil {
		return ExperienceReceipt{}, err
	}
	return receipt, nil
}

func invalidateCaseForRun(ctx context.Context, tx Tx, experienceID string, now time.Time) error {
	target := artifact.ExperienceTarget(experienceID)
	item, err := tx.GetArtifactByTarget(ctx, artifact.KindExperienceCase, target.TargetKey())
	if errors.Is(err, artifact.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return disableArtifact(ctx, tx, item, now)
}

func disableArtifact(ctx context.Context, tx Tx, item artifact.DerivedMemory, now time.Time) error {
	if item.State != artifact.StateDisabled {
		item.State = artifact.StateDisabled
		item.UpdatedAt = now
		if err := tx.UpdateArtifact(ctx, item); err != nil {
			return err
		}
	}
	return tx.DeleteArtifactSearchDocument(ctx, item.ID)
}

func receiptFromRun(run ExperienceRun) ExperienceReceipt {
	return ExperienceReceipt{
		ID:        run.ID,
		SourceID:  run.SourceID,
		RunID:     run.RunID,
		Outcome:   run.Outcome,
		State:     run.State,
		CreatedAt: run.CreatedAt,
		UpdatedAt: run.UpdatedAt,
	}
}

func experienceSourceContent(submission ExperienceSubmission) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(submission.Content))) == 0 {
		payload, err := json.Marshal(map[string]any{
			"goal":          submission.Goal,
			"scope_key":     submission.ScopeKey,
			"conditions":    cleanStrings(submission.Conditions),
			"actions":       cleanStrings(submission.Actions),
			"outcome":       submission.Outcome,
			"skill_id":      submission.SkillID,
			"skill_version": submission.SkillVersion,
			"run_id":        submission.RunID,
		})
		if err != nil {
			return nil, fmt.Errorf("编码结果反馈: %w", err)
		}
		return payload, nil
	}
	if !json.Valid(submission.Content) {
		return nil, fmt.Errorf("%w: content must be valid JSON", ErrInvalidExperience)
	}
	return append(json.RawMessage(nil), submission.Content...), nil
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
