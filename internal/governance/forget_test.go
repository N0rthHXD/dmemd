package governance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type forgetFixture struct {
	store       *sqlite.Store
	memories    *memory.Service
	artifacts   *artifact.Service
	experiences *governance.ExperienceServiceAPI
	forget      *governance.ForgetServiceAPI
	now         time.Time
}

func newForgetFixture(t *testing.T) *forgetFixture {
	t.Helper()
	store := testutil.NewStore(t)
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	artifacts := artifact.NewService(store, nil,
		artifact.WithClock(func() time.Time { return now }), artifact.WithDebounce(0))
	memories := memory.NewService(store, sqlite.MemoryStore{Store: store}, testutil.DocumentBuilder{},
		memory.WithGovernanceDebounce(0),
		memory.WithClock(func() time.Time { return now }),
		memory.WithArtifactSync(artifacts),
	)
	return &forgetFixture{
		store:       store,
		memories:    memories,
		artifacts:   artifacts,
		experiences: governance.NewExperienceService(sqlite.GovernanceStore{Store: store}),
		forget:      governance.NewForgetService(sqlite.GovernanceStore{Store: store}, memories),
		now:         now,
	}
}

func (f *forgetFixture) createMemory(t *testing.T, originSource string) memory.Memory {
	t.Helper()
	var created memory.Memory
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = f.memories.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindFact, Title: "数据库", Content: "项目使用 SQLite。",
			ObservedAt: f.now, ScopeKey: "project:dmemd", Topics: []string{"go"},
			OriginSourceID: originSource,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GetGovernanceJobByTarget(context.Background(), task.KindClassify, created.ID); err == nil {
		if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.CancelJob(ctx, task.KindClassify, created.ID, f.now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return created
}

func (f *forgetFixture) createSource(t *testing.T, content string) source.Source {
	t.Helper()
	item := source.Source{ID: idgen.NewUUID(), Content: []byte(content), CreatedAt: f.now}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.InsertSource(ctx, item)
	}); err != nil {
		t.Fatal(err)
	}
	return item
}

func (f *forgetFixture) commitSummary(t *testing.T, item memory.Memory) artifact.Artifact {
	t.Helper()
	var created artifact.Artifact
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = f.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Kind: artifact.KindTopicSummary, TargetKey: task.AggregateKey("project:dmemd", "go"),
			ScopeKey: "project:dmemd", Topic: "go", Title: "摘要", Content: "项目使用 SQLite。",
			Generator: "test", ProtocolVersion: "test-v1",
			Memories: []artifact.MemoryInput{{ID: item.ID, Version: item.Version}},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return created
}

func TestForgetSourceRemovesAssociatedMemoryArtifactEvidenceAndJobs(t *testing.T) {
	f := newForgetFixture(t)
	src := f.createSource(t, `"项目使用 SQLite。"`)
	var jobID string
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		jobID = idgen.NewUUID()
		return tx.InsertJob(ctx, ingest.Job{
			ID: jobID, SourceID: src.ID, Status: ingest.JobQueued,
			CreatedAt: f.now, UpdatedAt: f.now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	item := f.createMemory(t, src.ID)
	summary := f.commitSummary(t, item)

	result, err := f.forget.Forget(context.Background(), governance.ForgetRequest{SourceIDs: []string{src.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.MemoryIDs) != 1 || result.MemoryIDs[0] != item.ID ||
		len(result.SourceIDs) != 1 || result.SourceIDs[0] != src.ID {
		t.Fatalf("forget result = %+v", result)
	}
	if _, err := f.memories.Get(context.Background(), item.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("memory survived forget: %v", err)
	}
	if _, err := f.store.GetSource(context.Background(), src.ID); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("source survived forget: %v", err)
	}
	if _, err := f.store.GetArtifact(context.Background(), summary.ID); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("artifact survived forget: %v", err)
	}
	if _, err := f.memories.ListVersions(context.Background(), item.ID, 0); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("memory history survived forget: %v", err)
	}
	if _, err := f.artifacts.GetVersion(context.Background(), summary.ID, 1); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("artifact history survived forget: %v", err)
	}
	if _, err := f.store.GetJob(context.Background(), jobID); !errors.Is(err, ingest.ErrNotFound) {
		t.Fatalf("ingest job survived forget: %v", err)
	}
}

func TestForgetExperienceRemovesReportAndDependentCaseAndSkill(t *testing.T) {
	f := newForgetFixture(t)
	receipt, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{
		ScopeKey: "project:dmemd", Goal: "验证修改", Outcome: governance.OutcomeSuccess,
		Actions: []string{"运行 go test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var caseArtifact, skillArtifact artifact.Artifact
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		caseArtifact, err = f.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Kind: artifact.KindExperienceCase, TargetKey: artifact.ExperienceTarget(receipt.ID).TargetKey(),
			ExperienceID: receipt.ID, Title: "经验", Content: "经验内容",
			Generator: "test", ProtocolVersion: "test-v1",
			Runs: []artifact.RunRef{{ExperienceID: receipt.ID}},
		})
		if err != nil {
			return err
		}
		skillArtifact, err = f.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Kind: artifact.KindSkill, TargetKey: task.SkillKey("project:dmemd", ""),
			ScopeKey: "project:dmemd", Title: "工作流", Content: "指导内容",
			Generator: "test", ProtocolVersion: "test-v1",
			Runs: []artifact.RunRef{{ExperienceID: receipt.ID}},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	result, err := f.forget.Forget(context.Background(), governance.ForgetRequest{
		ExperienceIDs: []string{receipt.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ExperienceIDs) != 1 || result.ExperienceIDs[0] != receipt.ID ||
		len(result.SourceIDs) != 1 || result.SourceIDs[0] != receipt.SourceID {
		t.Fatalf("forget result = %+v", result)
	}
	if _, err := f.store.GetExperienceRun(context.Background(), receipt.ID); !errors.Is(err, governance.ErrExperienceNotFound) {
		t.Fatalf("experience survived forget: %v", err)
	}
	if _, err := f.store.GetSource(context.Background(), receipt.SourceID); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("report source survived forget: %v", err)
	}
	if _, err := f.store.GetArtifact(context.Background(), caseArtifact.ID); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("case survived forget: %v", err)
	}
	if _, err := f.store.GetArtifact(context.Background(), skillArtifact.ID); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("skill survived forget: %v", err)
	}
	if _, err := skill.NewService(f.artifacts).Get(context.Background(), skillArtifact.ID, 1); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("skill historical render survived forget: %v", err)
	}
	if _, err := f.store.GetGovernanceJobByTarget(context.Background(), task.KindAggregate,
		task.ExperienceKey(receipt.ID)); !errors.Is(err, task.ErrNotFound) {
		// CancelJob keeps the row; a cancelled row is expected instead.
		if job, jobErr := f.store.GetGovernanceJobByTarget(context.Background(), task.KindAggregate,
			task.ExperienceKey(receipt.ID)); jobErr != nil || job.Status != task.StatusCancelled {
			t.Fatalf("case job after forget = %+v err=%v", job, jobErr)
		}
	}
}

func TestForgetRollsBackWhenAnyTargetIsMissing(t *testing.T) {
	f := newForgetFixture(t)
	item := f.createMemory(t, "")
	_, err := f.forget.Forget(context.Background(), governance.ForgetRequest{
		MemoryIDs: []string{item.ID}, ExperienceIDs: []string{"missing-experience"},
	})
	if !errors.Is(err, governance.ErrExperienceNotFound) {
		t.Fatalf("forget error = %v", err)
	}
	if _, err := f.memories.Get(context.Background(), item.ID); err != nil {
		t.Fatalf("valid memory was partially removed: %v", err)
	}
}

func TestForgetRejectsEmptyRequest(t *testing.T) {
	f := newForgetFixture(t)
	if _, err := f.forget.Forget(context.Background(), governance.ForgetRequest{}); !errors.Is(err, governance.ErrInvalidForget) {
		t.Fatalf("empty forget error = %v", err)
	}
}

type failingPurger struct {
	inner *memory.Service
}

func (p failingPurger) PurgeInTx(ctx context.Context, tx memory.Tx, id string) error {
	if err := p.inner.PurgeInTx(ctx, tx, id); err != nil {
		return err
	}
	return errors.New("disk I/O error")
}

func TestForgetStorageFailureRollsBackWholeScope(t *testing.T) {
	f := newForgetFixture(t)
	src := f.createSource(t, `"项目使用 SQLite。"`)
	item := f.createMemory(t, src.ID)
	service := governance.NewForgetService(sqlite.GovernanceStore{Store: f.store}, failingPurger{inner: f.memories})
	if _, err := service.Forget(context.Background(), governance.ForgetRequest{SourceIDs: []string{src.ID}}); err == nil {
		t.Fatal("forget succeeded despite storage failure")
	}
	if _, err := f.memories.Get(context.Background(), item.ID); err != nil {
		t.Fatalf("memory was partially removed: %v", err)
	}
	if _, err := f.store.GetSource(context.Background(), src.ID); err != nil {
		t.Fatalf("source was partially removed: %v", err)
	}
}

func TestForgetBlocksInFlightExperienceResult(t *testing.T) {
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
		ScopeKey: "project:dmemd", Goal: "在途遗忘", Outcome: governance.OutcomeUnknown,
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
	forgetService := governance.NewForgetService(sqlite.GovernanceStore{Store: f.store}, f.memories)
	if _, err := forgetService.Forget(context.Background(), governance.ForgetRequest{
		ExperienceIDs: []string{receipt.ID},
	}); err != nil {
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
	if _, err := f.store.GetExperienceRun(context.Background(), receipt.ID); !errors.Is(err, governance.ErrExperienceNotFound) {
		t.Fatalf("forgotten run was restored: %v", err)
	}
	if _, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindExperienceCase,
		task.ExperienceKey(receipt.ID)); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("late case result restored a forgotten run: %v", err)
	}
}

func TestForgetRemovesHistoricalDerivedVersions(t *testing.T) {
	f := newForgetFixture(t)
	src := f.createSource(t, `"项目使用 SQLite。"`)
	item := f.createMemory(t, src.ID)
	summary := f.commitSummary(t, item)
	// A later revision that no longer references the memory must not preserve
	// the object or its historical version after a permanent forget.
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := f.artifacts.Commit(ctx, tx, artifact.CommitInput{
			Kind: artifact.KindTopicSummary, TargetKey: task.AggregateKey("project:dmemd", "go"),
			ScopeKey: "project:dmemd", Topic: "go", Title: "新摘要", Content: "新内容",
			Generator: "test", ProtocolVersion: "test-v1",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The second revision has no dependency edge, so a source forget still
	// leaves it; forgetting the memory must remove every version of the
	// dependent object through DeleteArtifactsForMemory.
	if _, err := f.forget.Forget(context.Background(), governance.ForgetRequest{MemoryIDs: []string{item.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GetArtifact(context.Background(), summary.ID); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("historical artifact survived memory forget: %v", err)
	}
	if _, err := f.store.GetSource(context.Background(), src.ID); err != nil {
		t.Fatalf("origin source should remain: %v", err)
	}
}
