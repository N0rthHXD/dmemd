package governance_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestScopeSummaryUsesScopeAndDerivedTypeOnly(t *testing.T) {
	f := newAggregateFixture(t)
	f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "项目使用 SQLite。",
		ScopeKey: "project:atlas", Topics: []string{"go"}})
	f.create(t, memory.CreateInput{Kind: memory.KindPreference, Content: "偏好先跑测试。",
		Durability: memory.DurabilityDurable, ScopeKey: "project:atlas", Topics: []string{"testing"}})
	f.create(t, memory.CreateInput{Kind: memory.KindEvent, Content: "下月发布。", Phase: memory.PhaseScheduled,
		ScopeKey: "project:atlas", Topics: []string{"release"}})

	var scopeRequests atomic.Int32
	worker := f.aggregateWorker(t, aggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
		if request.Kind != artifact.KindScopeSummary {
			return artifact.AggregateResult{Content: "摘要"}, nil
		}
		scopeRequests.Add(1)
		if request.Topic != "" {
			t.Fatalf("scope summary received topic %q", request.Topic)
		}
		if len(request.Memories) != 3 || request.Eligible != 3 {
			t.Fatalf("scope input = %+v eligible=%d", request.Memories, request.Eligible)
		}
		citations := make([]artifact.Citation, 0, len(request.Memories))
		for _, input := range request.Memories {
			citations = append(citations, artifact.Citation{
				Point: "要点", MemoryID: input.ID, Version: input.Version,
			})
		}
		return artifact.AggregateResult{Title: "项目概览", Content: "现状、约束、历史与计划。", Citations: citations}, nil
	}))
	processDue(t, worker)
	if scopeRequests.Load() != 1 {
		t.Fatalf("scope summary calls = %d", scopeRequests.Load())
	}
	item, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindScopeSummary,
		task.ScopeSummaryKey("project:atlas"))
	if err != nil {
		t.Fatal(err)
	}
	if item.State != artifact.StateActive || item.CurrentVersion != 1 || item.Topic != "" {
		t.Fatalf("scope summary = %+v", item)
	}
	detail, err := f.artifacts.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		InputSelection struct {
			Eligible int `json:"eligible"`
			Selected int `json:"selected"`
			Included int `json:"included"`
			Omitted  int `json:"omitted"`
		} `json:"input_selection"`
		CitationIntegrity string              `json:"citation_integrity"`
		SemanticCoverage  string              `json:"semantic_coverage"`
		Citations         []artifact.Citation `json:"citations"`
	}
	if err := json.Unmarshal(detail.Revision.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.InputSelection.Eligible != 3 || payload.InputSelection.Selected != 3 ||
		payload.InputSelection.Included != 3 || payload.InputSelection.Omitted != 0 ||
		payload.CitationIntegrity != "valid" || len(payload.Citations) != 3 {
		t.Fatalf("scope payload = %+v", payload)
	}
	if payload.SemanticCoverage != "unknown" {
		t.Fatalf("three-input semantic coverage = %q, want unknown", payload.SemanticCoverage)
	}

	groups, err := f.artifacts.ListAggregationGroups(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, group := range groups {
		if group.Target.Kind == artifact.KindScopeSummary && group.Target.ScopeKey == "project:atlas" &&
			group.Target.Topic == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("scope summary group missing: %+v", groups)
	}
}

func TestAggregateRejectsUnknownCitationButKeepsUsableBody(t *testing.T) {
	f := newAggregateFixture(t)
	created := f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "项目使用 SQLite。",
		ScopeKey: "project:dmemd", Topics: []string{"go"}})
	worker := f.aggregateWorker(t, aggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
		if request.Kind != artifact.KindTopicSummary {
			return artifact.AggregateResult{Content: "摘要"}, nil
		}
		return artifact.AggregateResult{
			Title: "摘要", Content: "项目使用 SQLite。",
			Citations: []artifact.Citation{
				{Section: "数据库", Point: "使用 SQLite", MemoryID: created.ID, Version: created.Version},
				{Point: "不存在的引用", MemoryID: "missing", Version: 9},
			},
			OmittedInputs: []artifact.OmittedInput{{MemoryID: "missing", Version: 9, Reason: "bogus"}},
		}, nil
	}))
	processDue(t, worker)
	item := f.artifactByTarget(t, artifact.KindTopicSummary, task.AggregateKey("project:dmemd", "go"))
	detail, err := f.artifacts.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		CitationIntegrity string                  `json:"citation_integrity"`
		SemanticCoverage  string                  `json:"semantic_coverage"`
		Citations         []artifact.Citation     `json:"citations"`
		Omitted           []artifact.OmittedInput `json:"omitted_inputs"`
	}
	if err := json.Unmarshal(detail.Revision.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CitationIntegrity != "partial" || len(payload.Citations) != 1 ||
		len(payload.Omitted) != 0 || payload.SemanticCoverage != "unknown" {
		t.Fatalf("payload = %+v", payload)
	}
	if len(detail.Revision.MemoryRefs) != 1 || detail.Revision.Content != "项目使用 SQLite。" {
		t.Fatalf("revision = %+v", detail.Revision)
	}
}

func TestAggregateSkipsIdenticalInputSignature(t *testing.T) {
	f := newAggregateFixture(t)
	f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "项目使用 SQLite。",
		ScopeKey: "project:dmemd", Topics: []string{"go"}})
	var calls atomic.Int32
	worker := f.aggregateWorker(t, aggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
		if request.Kind != artifact.KindTopicSummary {
			return artifact.AggregateResult{Content: "摘要"}, nil
		}
		calls.Add(1)
		return artifact.AggregateResult{Content: "摘要"}, nil
	}))
	processDue(t, worker)
	if calls.Load() != 1 {
		t.Fatalf("initial calls = %d", calls.Load())
	}
	targetKey := task.AggregateKey("project:dmemd", "go")
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind: task.KindAggregate, TargetKey: targetKey, InputHash: "manual-repeat",
			NextAttemptAt: &f.now, Now: f.now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	processDue(t, worker)
	if calls.Load() != 1 {
		t.Fatalf("identical input called the model again: %d", calls.Load())
	}
}

func TestSkillWithoutProcedureRecordsEvaluationWithoutArtifact(t *testing.T) {
	f := newExperienceFixture(t)
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind: task.KindBuildSkill, TargetKey: task.SkillKey("user", "missing"),
			InputHash: "no-procedure", NextAttemptAt: &f.now, Now: f.now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	w := f.worker(t, nil, skillFunc(func(context.Context, skill.Request) (skill.Draft, error) {
		called.Store(true)
		return skill.Draft{Content: "## 步骤\n1. 不应发生。"}, nil
	}))
	f.processAll(t, w)
	if called.Load() {
		t.Fatal("skill model was called without eligible procedure input")
	}
	if _, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindSkill,
		task.SkillKey("user", "missing")); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("no-input skill artifact = %v", err)
	}
	targets, err := f.artifacts.ListTargets(context.Background(), artifact.TargetQuery{
		ScopeKey: "user", Kind: artifact.KindSkill, Status: artifact.TargetStatusNoEligibleInput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Items) != 1 || targets.Items[0].ReasonCode != "no_procedure_input" ||
		targets.Items[0].Artifact != nil {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestSkillScopeMembershipAloneIsNotExperienceRelevance(t *testing.T) {
	f := newExperienceFixture(t)
	f.createMemory(t, memory.CreateInput{Kind: memory.KindProcedure, Content: "先跑 go test。",
		ScopeKey: "project:dmemd", Topics: []string{"testing"}})
	f.createMemory(t, memory.CreateInput{Kind: memory.KindProcedure, Content: "先申请发布窗口。",
		ScopeKey: "project:dmemd", Topics: []string{"release"}})
	var skillCalls atomic.Int32
	w := f.worker(t,
		experienceFunc(func(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
			return artifact.ExperienceResult{Content: "应用报告内容。"}, nil
		}),
		skillFunc(func(_ context.Context, request skill.Request) (skill.Draft, error) {
			skillCalls.Add(1)
			return skill.Draft{Name: "工作流", Content: "## 步骤\n1. 执行报告中的步骤。"}, nil
		}),
	)
	f.processAll(t, w)
	testingSkill := f.skillByTarget(t, "project:dmemd", "testing")
	releaseSkill := f.skillByTarget(t, "project:dmemd", "release")

	// No explicit skill reference and the generated case spans two workflows,
	// so scope membership alone must not stale either skill.
	if _, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID: "run-unrelated", ScopeKey: "project:dmemd", Goal: "验证修改",
		Outcome: governance.OutcomeSuccess,
	}); err != nil {
		t.Fatal(err)
	}
	f.processAll(t, w)
	if got := f.skillByTarget(t, "project:dmemd", "testing"); got.State != artifact.StateActive ||
		got.CurrentVersion != testingSkill.CurrentVersion {
		t.Fatalf("testing skill changed on scope-only experience: %+v", got)
	}
	if got := f.skillByTarget(t, "project:dmemd", "release"); got.State != artifact.StateActive ||
		got.CurrentVersion != releaseSkill.CurrentVersion {
		t.Fatalf("release skill changed on scope-only experience: %+v", got)
	}

	// An explicit reference is decisive: only that workflow is stale.
	before := skillCalls.Load()
	if _, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID: "run-testing", ScopeKey: "project:dmemd", Goal: "验证修改",
		Outcome: governance.OutcomeSuccess, SkillID: testingSkill.ID,
		SkillVersion: testingSkill.CurrentVersion,
	}); err != nil {
		t.Fatal(err)
	}
	stale := f.skillByTarget(t, "project:dmemd", "testing")
	if stale.State != artifact.StateStale {
		t.Fatalf("explicit skill reference did not stale testing skill: %+v", stale)
	}
	if got := f.skillByTarget(t, "project:dmemd", "release"); got.State != artifact.StateActive {
		t.Fatalf("release skill was affected by explicit reference: %+v", got)
	}
	f.processAll(t, w)
	if skillCalls.Load() == before {
		t.Fatal("explicitly referenced skill was not rebuilt")
	}
	final := f.skillByTarget(t, "project:dmemd", "testing")
	if final.State != artifact.StateActive || final.CurrentVersion <= stale.CurrentVersion {
		t.Fatalf("rebuilt testing skill = %+v", final)
	}
}

func TestSkillRejectsUnstructuredModelOutput(t *testing.T) {
	f := newExperienceFixture(t)
	f.createMemory(t, memory.CreateInput{Kind: memory.KindProcedure, Content: "先跑 go test。",
		ScopeKey: "project:dmemd", Topics: []string{"testing"}})
	w := f.worker(t, nil, skillFunc(func(context.Context, skill.Request) (skill.Draft, error) {
		return skill.Draft{Name: "不完整", Content: "只有一句没有步骤的说明。"}, nil
	}))
	for i := 0; i < 12; i++ {
		if _, err := w.ProcessOne(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	item, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindSkill,
		task.SkillKey("project:dmemd", "testing"))
	if err != nil {
		t.Fatal(err)
	}
	if item.State != artifact.StateDisabled || item.ReasonCode != "invalid_model_output" {
		t.Fatalf("unstructured output artifact = %+v", item)
	}
	if item.CurrentVersion != 0 {
		t.Fatalf("unstructured output produced a revision: %+v", item)
	}
	targets, err := f.artifacts.ListTargets(context.Background(), artifact.TargetQuery{
		ScopeKey: "project:dmemd", Kind: artifact.KindSkill,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Items) != 1 || targets.Items[0].Status != artifact.TargetStatusInvalidOutput {
		t.Fatalf("target status = %+v", targets)
	}
}

func TestExistingSkillDisablesWhenProcedureInputDisappears(t *testing.T) {
	f := newExperienceFixture(t)
	procedure := f.createMemory(t, memory.CreateInput{Kind: memory.KindProcedure,
		Content: "先跑 go test。", ScopeKey: "project:dmemd", Topics: []string{"testing"}})
	w := f.worker(t, nil, skillFunc(func(context.Context, skill.Request) (skill.Draft, error) {
		return skill.Draft{Name: "验证工作流", Content: "## 步骤\n1. 运行 go test。"}, nil
	}))
	f.processAll(t, w)
	active := f.skillByTarget(t, "project:dmemd", "testing")
	if active.State != artifact.StateActive || active.CurrentVersion != 1 {
		t.Fatalf("initial skill = %+v", active)
	}
	if _, err := f.memories.UpdateState(context.Background(), procedure.ID, memory.StateArchived); err != nil {
		t.Fatal(err)
	}
	if stale := f.skillByTarget(t, "project:dmemd", "testing"); stale.State != artifact.StateStale {
		t.Fatalf("archive did not stale skill: %+v", stale)
	}
	f.processAll(t, w)
	disabled := f.skillByTarget(t, "project:dmemd", "testing")
	if disabled.State != artifact.StateDisabled || disabled.ReasonCode != "no_procedure_input" ||
		disabled.CurrentVersion != 1 {
		t.Fatalf("disabled skill = %+v", disabled)
	}
	targets, err := f.artifacts.ListTargets(context.Background(), artifact.TargetQuery{
		ScopeKey: "project:dmemd", Kind: artifact.KindSkill,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Items) != 1 || targets.Items[0].Status != artifact.TargetStatusNoEligibleInput ||
		!targets.Items[0].HasUsableVersion() {
		t.Fatalf("retained skill target = %+v", targets)
	}
}

func TestEnglishExperienceRendersEnglishReportDisclaimer(t *testing.T) {
	f := newExperienceFixture(t)
	w := f.worker(t,
		experienceFunc(func(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
			return artifact.ExperienceResult{Content: "Ran go test and build."}, nil
		}),
		nil,
	)
	receipt, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID: "run-en", ScopeKey: "project:dmemd", Goal: "Validate the change",
		Actions: []string{"run go test"}, Outcome: governance.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.processAll(t, w)
	item, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindExperienceCase,
		task.ExperienceKey(receipt.ID))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.artifacts.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.Revision.Content, "This is an application report") ||
		!strings.Contains(detail.Revision.Content, "Outcome: success") ||
		strings.Contains(detail.Revision.Content, "这是应用报告") {
		t.Fatalf("english case = %q", detail.Revision.Content)
	}
	var payload struct {
		Outcome  string `json:"outcome"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(detail.Revision.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Outcome != "success" || payload.Language != "en" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestProfileTransientContentIsNotActivated(t *testing.T) {
	f := newAggregateFixture(t)
	f.create(t, memory.CreateInput{Kind: memory.KindPreference, Content: "长期偏好先跑测试。",
		Durability: memory.DurabilityDurable, ScopeKey: "user", Topics: []string{"testing"}})
	worker := f.aggregateWorker(t, aggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
		if request.Kind == artifact.KindProfile {
			return artifact.AggregateResult{Content: "本周进度：正在完成发布准备。"}, nil
		}
		return artifact.AggregateResult{Content: "摘要"}, nil
	}))
	processDue(t, worker)
	item, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindProfile, task.ProfileKey("user"))
	if err != nil {
		t.Fatal(err)
	}
	if item.State != artifact.StateDisabled || item.ReasonCode != "invalid_profile_content" ||
		item.CurrentVersion != 0 {
		t.Fatalf("profile = %+v", item)
	}
	targets, err := f.artifacts.ListTargets(context.Background(), artifact.TargetQuery{
		ScopeKey: "user", Kind: artifact.KindProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Items) != 1 || targets.Items[0].Status != artifact.TargetStatusInvalidOutput {
		t.Fatalf("profile target = %+v", targets)
	}
}

func TestTargetQueryIncludesQueuedRunningAndNoArtifactTargets(t *testing.T) {
	f := newExperienceFixture(t)
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		now := f.now
		for _, target := range []task.UpsertInput{
			{Kind: task.KindBuildSkill, TargetKey: task.SkillKey("user", "queued"),
				InputHash: "q", NextAttemptAt: &now, Now: now},
			{Kind: task.KindBuildSkill, TargetKey: task.SkillKey("user", "missing"),
				InputHash: "m", NextAttemptAt: &now, Now: now},
		} {
			if _, _, err := tx.UpsertJob(ctx, target); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := f.worker(t, nil, nil)
	// Process only the first queued target; the other remains queued.
	if _, err := w.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	queued, err := f.artifacts.ListTargets(context.Background(), artifact.TargetQuery{
		ScopeKey: "user", Kind: artifact.KindSkill, Status: artifact.TargetStatusQueued,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Items) != 1 {
		t.Fatalf("queued targets = %+v", queued)
	}
	missing, err := f.artifacts.ListTargets(context.Background(), artifact.TargetQuery{
		ScopeKey: "user", Kind: artifact.KindSkill, Status: artifact.TargetStatusNoEligibleInput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Items) != 1 || missing.Items[0].ReasonCode != "no_procedure_input" {
		t.Fatalf("missing targets = %+v", missing)
	}
}

func TestTargetEvaluationTimestampsAreStable(t *testing.T) {
	f := newExperienceFixture(t)
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.UpsertTargetEvaluation(ctx, artifact.TargetEvaluation{
			Kind: artifact.KindSkill, TargetKey: task.SkillKey("user", "empty"),
			ScopeKey: "user", Topic: "empty", Status: artifact.TargetStatusNoEligibleInput,
			ReasonCode: "no_procedure_input", EvaluatedAt: time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC),
		})
	}); err != nil {
		t.Fatal(err)
	}
	targets, err := f.artifacts.ListTargets(context.Background(), artifact.TargetQuery{
		ScopeKey: "user", Kind: artifact.KindSkill,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Items) != 1 || targets.Items[0].EvaluatedAt == nil ||
		!targets.Items[0].EvaluatedAt.Equal(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("targets = %+v", targets)
	}
}
