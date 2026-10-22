package governance_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type experienceFixture struct {
	store       *sqlite.Store
	memories    *memory.Service
	artifacts   *artifact.Service
	experiences *governance.ExperienceServiceAPI
	search      *retrieval.Service
	skills      *skill.Service
	now         time.Time
}

func newExperienceFixture(t *testing.T) *experienceFixture {
	t.Helper()
	store := testutil.NewStore(t)
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	artifacts := artifact.NewService(store, retrieval.NewArtifactDocumentBuilder(),
		artifact.WithClock(func() time.Time { return now }),
		artifact.WithDebounce(0),
	)
	memories := memory.NewService(store, sqlite.MemoryStore{Store: store}, retrieval.NewDocumentBuilder(),
		memory.WithGovernanceDebounce(0),
		memory.WithArtifactSync(artifacts),
		memory.WithClock(func() time.Time { return now }),
	)
	search := retrieval.NewService(store, retrieval.NewDocumentBuilder(),
		retrieval.WithArtifacts(artifacts))
	experiences := governance.NewExperienceService(sqlite.GovernanceStore{Store: store},
		governance.WithExperienceClock(func() time.Time { return now }))
	return &experienceFixture{
		store:       store,
		memories:    memories,
		artifacts:   artifacts,
		experiences: experiences,
		search:      search,
		skills:      skill.NewService(artifacts),
		now:         now,
	}
}

func (f *experienceFixture) createMemory(t *testing.T, input memory.CreateInput) memory.Memory {
	t.Helper()
	if input.ObservedAt.IsZero() {
		input.ObservedAt = f.now
	}
	var created memory.Memory
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = f.memories.Create(ctx, tx, input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return created
}

func (f *experienceFixture) worker(t *testing.T, experience governance.ExperienceProvider,
	skills governance.SkillProvider) *governance.Worker {
	t.Helper()
	limiter := governance.NewLimiter(0, 0)
	classify := governance.NewClassifyHandler(f.store, sqlite.GovernanceStore{Store: f.store}, f.memories,
		func(context.Context, string, string, string) ([]string, error) {
			t.Fatal("classifier called although topics were supplied")
			return nil, nil
		},
		limiter,
		governance.WithClassifyClock(func() time.Time { return f.now }),
	)
	aggregate := governance.NewAggregateHandler(sqlite.GovernanceStore{Store: f.store}, f.artifacts,
		aggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
			return artifact.AggregateResult{Title: request.Topic + " 摘要", Content: "摘要：" + request.Topic}, nil
		}),
		limiter,
		governance.WithAggregateClock(func() time.Time { return f.now }),
		governance.WithExperienceProvider(experience),
	)
	buildSkill := governance.NewSkillHandler(sqlite.GovernanceStore{Store: f.store}, f.artifacts,
		skills, limiter, governance.WithSkillClock(func() time.Time { return f.now }))
	reconcile := governance.NewReconcileHandler(sqlite.GovernanceStore{Store: f.store}, 0, 100,
		governance.WithReconcileClock(func() time.Time { return f.now }),
		governance.WithArtifactReconcile(f.artifacts),
	)
	return governance.NewWorker(
		sqlite.GovernanceStore{Store: f.store},
		map[task.Kind]governance.Handler{
			task.KindClassify:   classify,
			task.KindAggregate:  aggregate,
			task.KindBuildSkill: buildSkill,
			task.KindReconcile:  reconcile,
		},
		governance.WithReconcileInterval(0),
		governance.WithRetryDelays(0, 0, 0),
		governance.WithPollInterval(time.Millisecond),
		governance.WithWorkerClock(func() time.Time { return f.now }),
	)
}

func (f *experienceFixture) processAll(t *testing.T, worker *governance.Worker) {
	t.Helper()
	for i := 0; i < 60; i++ {
		processed, err := worker.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			return
		}
	}
	t.Fatal("governance did not quiesce")
}

type experienceFunc func(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error)

func (f experienceFunc) ComposeExperience(ctx context.Context, request artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
	return f(ctx, request)
}

type skillFunc func(context.Context, skill.Request) (skill.Draft, error)

func (f skillFunc) ComposeSkill(ctx context.Context, request skill.Request) (skill.Draft, error) {
	return f(ctx, request)
}

func (f *experienceFixture) skillByTarget(t *testing.T, scopeKey, topic string) artifact.Artifact {
	t.Helper()
	item, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindSkill,
		task.SkillKey(scopeKey, topic))
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestExperienceFeedbackCreatesCaseEnablesSkillAndDuplicateRunIDReturnsReceipt(t *testing.T) {
	f := newExperienceFixture(t)
	procedure := f.createMemory(t, memory.CreateInput{
		Kind:     memory.KindProcedure,
		Title:    "修改后验证",
		Content:  "修改 dmemd 后先运行 go test，再执行构建检查。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"testing"},
	})
	var sawFailure atomic.Bool
	worker := f.worker(t,
		experienceFunc(func(_ context.Context, request artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
			return artifact.ExperienceResult{
				Problem: "验证 dmemd 修改",
				Actions: request.Run.Actions,
				Lessons: []string{"先执行单元测试"},
				Content: "问题：验证修改。\n行动：运行 go test。\n结果：按应用报告记录。",
			}, nil
		}),
		skillFunc(func(_ context.Context, request skill.Request) (skill.Draft, error) {
			for _, experience := range request.Experiences {
				if experience.Outcome == string(governance.OutcomeFailure) {
					sawFailure.Store(true)
				}
			}
			if len(request.Procedures) == 0 {
				t.Fatal("skill request missing the procedure")
			}
			return skill.Draft{
				Name:        "验证工作流",
				Description: "修改 dmemd 后的验证步骤",
				Content:     "## 步骤\n1. 运行 go test。\n2. 执行构建检查。",
				Steps:       []string{"运行 go test", "执行构建检查"},
			}, nil
		}),
	)
	f.processAll(t, worker)
	skillBefore := f.skillByTarget(t, "project:dmemd", "testing")
	if skillBefore.State != artifact.StateActive || skillBefore.CurrentVersion != 1 {
		t.Fatalf("initial skill = %+v", skillBefore)
	}

	first, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID:    "run-1",
		ScopeKey: "project:dmemd",
		Goal:     "验证 dmemd 修改",
		Actions:  []string{"运行 go test"},
		Outcome:  governance.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.SourceID == "" || first.State != governance.ExperienceRecorded ||
		first.Outcome != governance.OutcomeSuccess {
		t.Fatalf("receipt = %+v", first)
	}
	duplicate, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID:    "run-1",
		ScopeKey: "project:dmemd",
		Outcome:  governance.ExperienceOutcome("invalid"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != first.ID || duplicate.SourceID != first.SourceID || duplicate.Outcome != first.Outcome {
		t.Fatalf("duplicate receipt = %+v, first = %+v", duplicate, first)
	}

	f.processAll(t, worker)
	caseArtifact, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindExperienceCase,
		task.ExperienceKey(first.ID))
	if err != nil {
		t.Fatal(err)
	}
	if caseArtifact.State != artifact.StateActive || caseArtifact.CurrentVersion != 1 {
		t.Fatalf("experience case = %+v", caseArtifact)
	}
	caseDetail, err := f.artifacts.Get(context.Background(), caseArtifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if caseDetail.Revision == nil || !strings.Contains(caseDetail.Revision.Content, "go test") {
		t.Fatalf("case content = %+v", caseDetail.Revision)
	}
	if len(caseDetail.Revision.RunRefs) != 1 || caseDetail.Revision.RunRefs[0].ExperienceID != first.ID {
		t.Fatalf("case run refs = %+v", caseDetail.Revision.RunRefs)
	}
	if len(caseDetail.Revision.MemoryRefs) != 1 || caseDetail.Revision.MemoryRefs[0].MemoryID != procedure.ID {
		t.Fatalf("case memory refs = %+v", caseDetail.Revision.MemoryRefs)
	}

	skillAfterCase := f.skillByTarget(t, "project:dmemd", "testing")
	if skillAfterCase.State != artifact.StateActive || skillAfterCase.CurrentVersion != 2 {
		t.Fatalf("skill after case = %+v", skillAfterCase)
	}
	skillDetail, err := f.artifacts.Get(context.Background(), skillAfterCase.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(skillDetail.Revision.RunRefs) != 1 || skillDetail.Revision.RunRefs[0].ExperienceID != first.ID {
		t.Fatalf("skill run refs = %+v", skillDetail.Revision.RunRefs)
	}

	recall, err := f.search.Recall(context.Background(), retrieval.RecallRequest{
		Query: "test", ScopeKey: "project:dmemd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recall.Experiences) == 0 || len(recall.Skills) == 0 {
		t.Fatalf("recall partitions = %+v", recall)
	}
	content, err := f.skills.Get(context.Background(), skillAfterCase.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(content.Content, "---\nname: ") || content.State != "active" {
		t.Fatalf("rendered skill = %+v", content)
	}

	failure, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID:        "run-2",
		ScopeKey:     "project:dmemd",
		Goal:         "再次验证",
		Actions:      []string{"跳过测试"},
		Outcome:      governance.OutcomeFailure,
		SkillID:      skillAfterCase.ID,
		SkillVersion: skillAfterCase.CurrentVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := f.skillByTarget(t, "project:dmemd", "testing")
	if stale.State != artifact.StateStale {
		t.Fatalf("failure feedback did not stale skill: %+v", stale)
	}
	if _, err := f.skills.Get(context.Background(), stale.ID, 0); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("stale skill content error = %v", err)
	}
	f.processAll(t, worker)
	if !sawFailure.Load() {
		t.Fatal("skill regeneration did not receive failure feedback")
	}
	rebuilt := f.skillByTarget(t, "project:dmemd", "testing")
	if rebuilt.State != artifact.StateActive || rebuilt.CurrentVersion != stale.CurrentVersion+1 {
		t.Fatalf("rebuilt skill = %+v", rebuilt)
	}
	rebuiltDetail, err := f.artifacts.Get(context.Background(), rebuilt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRunRef(rebuiltDetail.Revision.RunRefs, failure.ID) || !hasRunRef(rebuiltDetail.Revision.RunRefs, first.ID) {
		t.Fatalf("rebuilt skill did not retain both sources: %+v", rebuiltDetail.Revision.RunRefs)
	}

	if _, err := f.experiences.Retract(context.Background(), failure.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID:    "run-2",
		ScopeKey: "project:dmemd",
		Goal:     "试图恢复已撤销结果",
		Outcome:  governance.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != failure.ID || replayed.State != governance.ExperienceRevoked ||
		replayed.Outcome != governance.OutcomeFailure {
		t.Fatalf("duplicate run_id recovered revoked result: %+v", replayed)
	}
	if item := f.skillByTarget(t, "project:dmemd", "testing"); item.State != artifact.StateStale {
		t.Fatalf("retract did not stale skill: %+v", item)
	}
	disabledCase, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindExperienceCase,
		task.ExperienceKey(failure.ID))
	if err != nil {
		t.Fatal(err)
	}
	if disabledCase.State != artifact.StateDisabled {
		t.Fatalf("retracted case = %+v", disabledCase)
	}
	f.processAll(t, worker)
	afterRetract := f.skillByTarget(t, "project:dmemd", "testing")
	if afterRetract.State != artifact.StateActive {
		t.Fatalf("skill did not recover after retract: %+v", afterRetract)
	}
	afterDetail, err := f.artifacts.Get(context.Background(), afterRetract.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasRunRef(afterDetail.Revision.RunRefs, failure.ID) {
		t.Fatalf("revoked run remained a skill source: %+v", afterDetail.Revision.RunRefs)
	}
	historical, err := f.skills.Get(context.Background(), afterRetract.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if historical.Version != 1 || historical.State == "" {
		t.Fatalf("historical skill = %+v", historical)
	}
}

func TestRetractBlocksInFlightCaseResult(t *testing.T) {
	f := newExperienceFixture(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	worker := f.worker(t,
		experienceFunc(func(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return artifact.ExperienceResult{Content: "迟到的经验案例"}, nil
		}),
		skillFunc(func(context.Context, skill.Request) (skill.Draft, error) {
			return skill.Draft{Content: "迟到的工作流"}, nil
		}),
	)
	receipt, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID:    "run-late",
		ScopeKey: "project:dmemd",
		Goal:     "迟到结果",
		Outcome:  governance.OutcomeUnknown,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := worker.ProcessOne(context.Background())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("experience model was not called")
	}
	if _, err := f.experiences.Retract(context.Background(), receipt.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not finish")
	}
	item, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindExperienceCase,
		task.ExperienceKey(receipt.ID))
	if err == nil && (item.State != artifact.StateDisabled || item.CurrentVersion != 0) {
		t.Fatalf("late case result restored a revoked run: %+v", item)
	}
	if err != nil && !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRetractBlocksInFlightSkillResult(t *testing.T) {
	f := newExperienceFixture(t)
	f.createMemory(t, memory.CreateInput{
		Kind:     memory.KindProcedure,
		Title:    "修改后验证",
		Content:  "修改后运行 go test。",
		ScopeKey: "project:dmemd",
		Topics:   []string{"testing"},
	})
	block := atomic.Bool{}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	worker := f.worker(t,
		experienceFunc(func(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
			return artifact.ExperienceResult{Content: "经验：运行 go test。"}, nil
		}),
		skillFunc(func(_ context.Context, request skill.Request) (skill.Draft, error) {
			blocked := block.Load()
			if blocked {
				select {
				case started <- struct{}{}:
				default:
				}
				<-release
			}
			if !blocked {
				for _, experience := range request.Experiences {
					if experience.RunID == "run-late-skill" {
						t.Error("retraction did not remove the run from the skill input")
					}
				}
			}
			return skill.Draft{Name: "验证工作流", Content: "## 步骤\n1. 运行 go test。"}, nil
		}),
	)
	f.processAll(t, worker)
	initial := f.skillByTarget(t, "project:dmemd", "testing")
	if initial.State != artifact.StateActive || initial.CurrentVersion != 1 {
		t.Fatalf("initial skill = %+v", initial)
	}

	receipt, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		RunID:    "run-late-skill",
		ScopeKey: "project:dmemd",
		Goal:     "阻断在途 Skill",
		Outcome:  governance.OutcomeSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("case processed=%v err=%v", processed, err)
	}
	block.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := worker.ProcessOne(context.Background())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("skill model was not called")
	}
	stale := f.skillByTarget(t, "project:dmemd", "testing")
	if stale.State != artifact.StateStale {
		t.Fatalf("skill was not stale before retract result: %+v", stale)
	}
	if _, err := f.experiences.Retract(context.Background(), receipt.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not finish")
	}
	block.Store(false)
	f.processAll(t, worker)
	final := f.skillByTarget(t, "project:dmemd", "testing")
	if final.State != artifact.StateActive {
		t.Fatalf("skill did not recover = %+v", final)
	}
	detail, err := f.artifacts.Get(context.Background(), final.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasRunRef(detail.Revision.RunRefs, receipt.ID) {
		t.Fatalf("late skill result restored a revoked run: %+v", detail.Revision.RunRefs)
	}
}

func hasRunRef(refs []artifact.RunRef, id string) bool {
	for _, ref := range refs {
		if ref.ExperienceID == id {
			return true
		}
	}
	return false
}
