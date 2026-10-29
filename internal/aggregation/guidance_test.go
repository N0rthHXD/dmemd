package aggregation_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/aggregation"
	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type recordingGuidanceProvider struct {
	experiences []artifact.ExperienceRequest
	skills      []skill.Request
}

func (p *recordingGuidanceProvider) ComposeExperience(_ context.Context,
	request artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
	p.experiences = append(p.experiences, request)
	return artifact.ExperienceResult{Content: "问题：验证修改。\n行动：运行 go test。"}, nil
}

func (p *recordingGuidanceProvider) ComposeSkill(_ context.Context,
	request skill.Request) (skill.Draft, error) {
	p.skills = append(p.skills, request)
	return skill.Draft{
		Name:        "验证工作流",
		Description: "稳定流程与经验形成的指导",
		Content:     "## 步骤\n1. 运行 go test。",
		Steps:       []string{"运行 go test。"},
	}, nil
}

func TestGuidancePipelineUsesStableInputsAndGenericDependencies(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)

	src := source.Source{ID: "source-guidance", ScopeKey: "project:dmemd",
		Content: []byte(`"项目验证使用 go test。"`), CreatedAt: now}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		return tx.InsertObservation(ctx, observation.Observation{
			ID: "obs-guidance", SourceID: src.ID, ScopeKey: src.ScopeKey,
			Kind: observation.KindFact, Content: "项目验证使用 go test。",
			ContentMode: observation.ContentExtractive, Status: observation.StatusObserved,
			CreatedAt: now, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}

	memories := memory.NewService(store, sqlite.MemoryStore{Store: store}, retrieval.NewDocumentBuilder(),
		memory.WithClock(func() time.Time { return now }))
	var procedure memory.Memory
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		procedure, err = memories.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindProcedure, Title: "验证步骤",
			Content: "修改后运行 go test。", ScopeKey: src.ScopeKey,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	provider := &recordingGuidanceProvider{}
	artifacts := artifact.NewService(store, nil,
		artifact.WithClock(func() time.Time { return now }))
	handler := aggregation.NewGuidanceHandler(
		sqlite.AggregationStore{Store: store}, artifacts, provider,
		aggregation.WithGuidanceClock(func() time.Time { return now }),
	)
	worker := governance.NewWorker(sqlite.GovernanceStore{Store: store},
		map[task.Kind]governance.Handler{task.KindBuildGuidance: handler},
		governance.WithWorkerClock(func() time.Time { return now }))

	experiences := governance.NewExperienceService(sqlite.GovernanceStore{Store: store},
		governance.WithExperienceClock(func() time.Time { return now }))
	receipt, err := experiences.Submit(ctx, governance.ExperienceSubmission{
		RunID: "run-guidance", ScopeKey: src.ScopeKey, Goal: "验证修改",
		Conditions: []string{"依赖已安装"}, Actions: []string{"运行 go test"},
		Outcome: governance.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	caseJob, err := store.GetGovernanceJobByTarget(ctx, task.KindBuildGuidance,
		artifact.ExperienceTarget(receipt.ID).TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if caseJob.Kind != task.KindBuildGuidance {
		t.Fatalf("case job kind = %s", caseJob.Kind)
	}

	for attempt := 0; attempt < 8; attempt++ {
		processed, err := worker.ProcessOne(ctx)
		if err != nil {
			t.Fatalf("process guidance job: %v", err)
		}
		if !processed {
			break
		}
	}

	caseArtifact, err := store.GetArtifactByTarget(ctx, artifact.KindExperienceCase,
		artifact.ExperienceTarget(receipt.ID).TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if caseArtifact.State != artifact.StateActive || caseArtifact.Layer != artifact.LayerL4 ||
		caseArtifact.ScopeKey != src.ScopeKey {
		t.Fatalf("case artifact = %+v", caseArtifact)
	}
	caseRevision, err := store.GetArtifactRevision(ctx, caseArtifact.ID, caseArtifact.CurrentVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(caseRevision.Content, "应用报告") ||
		!strings.Contains(caseRevision.Content, "验证修改") {
		t.Fatalf("case content = %q", caseRevision.Content)
	}
	caseDeps, err := store.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyExperience, ID: receipt.ID,
	})
	if err != nil || len(caseDeps) == 0 {
		t.Fatalf("case dependencies = %+v err=%v", caseDeps, err)
	}

	skillTarget := artifact.Target{Layer: artifact.LayerL4, Kind: artifact.KindSkill,
		ScopeKey: src.ScopeKey}
	skillArtifact, err := store.GetArtifactByTarget(ctx, artifact.KindSkill, skillTarget.TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if skillArtifact.State != artifact.StateActive || skillArtifact.Layer != artifact.LayerL4 {
		t.Fatalf("skill artifact = %+v", skillArtifact)
	}
	if len(provider.skills) == 0 {
		t.Fatal("skill provider was not called")
	}
	request := provider.skills[0]
	var hasProcedure, hasExperience bool
	for _, input := range request.Inputs {
		switch {
		case input.Type == artifact.DependencyObservation:
			t.Fatalf("skill received raw observation input: %+v", input)
		case input.Type == artifact.DependencyMemory && input.ID == procedure.ID:
			hasProcedure = true
		case input.Type == artifact.DependencyExperience && input.ID == receipt.ID:
			hasExperience = true
		}
	}
	if !hasProcedure || !hasExperience {
		t.Fatalf("skill inputs = %+v", request.Inputs)
	}
	skillDeps, err := store.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyMemory, ID: procedure.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundSkill := false
	for _, dependent := range skillDeps {
		if dependent.Kind == artifact.KindSkill {
			foundSkill = true
		}
	}
	if !foundSkill {
		t.Fatalf("skill memory dependencies = %+v err=%v", skillDeps, err)
	}
	skillDeps, err = store.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyExperience, ID: receipt.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundSkill = false
	for _, dependent := range skillDeps {
		if dependent.Kind == artifact.KindSkill {
			foundSkill = true
		}
	}
	if !foundSkill {
		t.Fatalf("skill experience dependency missing: %+v", skillDeps)
	}

	// A repeated job over the same selected inputs must reuse the active
	// revision instead of paying for a second model call.
	skillCalls := len(provider.skills)
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind: task.KindBuildGuidance, TargetKey: skillTarget.TargetKey(),
			InputHash: "manual-rebuild", NextAttemptAt: &now, Now: now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("process idempotent skill job: processed=%v err=%v", processed, err)
	}
	if len(provider.skills) != skillCalls {
		t.Fatalf("identical skill inputs called the model again: %d -> %d", skillCalls, len(provider.skills))
	}

	if _, err := experiences.Retract(ctx, receipt.ID); err != nil {
		t.Fatal(err)
	}
	disabled, err := store.GetArtifactByTarget(ctx, artifact.KindExperienceCase,
		artifact.ExperienceTarget(receipt.ID).TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if disabled.State != artifact.StateDisabled {
		t.Fatalf("revoked case state = %s", disabled.State)
	}
	for attempt := 0; attempt < 8; attempt++ {
		processed, err := worker.ProcessOne(ctx)
		if err != nil {
			t.Fatalf("process revoked guidance job: %v", err)
		}
		if !processed {
			break
		}
	}
	rebuilt, err := store.GetArtifactByTarget(ctx, artifact.KindSkill, skillTarget.TargetKey())
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.State != artifact.StateActive || rebuilt.CurrentVersion != 2 {
		t.Fatalf("rebuilt skill = %+v", rebuilt)
	}
	for _, input := range provider.skills[len(provider.skills)-1].Inputs {
		if input.Type == artifact.DependencyExperience {
			t.Fatalf("revoked experience remained a skill input: %+v", input)
		}
	}
}

func TestGuidanceSkillRequiresProcedure(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	provider := &recordingGuidanceProvider{}
	artifacts := artifact.NewService(store, nil, artifact.WithClock(func() time.Time { return now }))
	handler := aggregation.NewGuidanceHandler(
		sqlite.AggregationStore{Store: store}, artifacts, provider,
		aggregation.WithGuidanceClock(func() time.Time { return now }),
	)
	worker := governance.NewWorker(sqlite.GovernanceStore{Store: store},
		map[task.Kind]governance.Handler{task.KindBuildGuidance: handler},
		governance.WithWorkerClock(func() time.Time { return now }))

	experiences := governance.NewExperienceService(sqlite.GovernanceStore{Store: store},
		governance.WithExperienceClock(func() time.Time { return now }))
	if _, err := experiences.Submit(ctx, governance.ExperienceSubmission{
		RunID: "run-no-procedure", ScopeKey: "project:dmemd", Goal: "没有流程",
		Outcome: governance.OutcomeUnknown,
	}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 8; attempt++ {
		processed, err := worker.ProcessOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			break
		}
	}
	if len(provider.skills) != 0 {
		t.Fatalf("skill model called without procedure: %+v", provider.skills)
	}
	skillTarget := artifact.Target{Layer: artifact.LayerL4, Kind: artifact.KindSkill,
		ScopeKey: "project:dmemd"}
	if _, err := store.GetArtifactByTarget(ctx, artifact.KindSkill, skillTarget.TargetKey()); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("empty skill artifact exists: %v", err)
	}
	targets, err := store.ListTargets(ctx, artifact.TargetQuery{
		ScopeKey: "project:dmemd", Kind: artifact.KindSkill,
		Status: artifact.TargetStatusNoEligibleInput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Items) != 1 || targets.Items[0].ReasonCode != "no_procedure_input" {
		t.Fatalf("skill evaluation = %+v", targets.Items)
	}
}
