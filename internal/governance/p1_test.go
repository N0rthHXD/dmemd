package governance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/topic"
)

func TestP1HierarchyRawTopicsAndNewMemberInvalidation(t *testing.T) {
	f := newAggregateFixture(t)
	created := f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "节点内存不足。",
		ScopeKey: "user", Topics: []string{"K8s", "节点内存耗尽"}, Durability: memory.DurabilityDurable})
	loaded, err := f.store.GetMemory(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Topics, []string{"containers", "kubernetes", "oom"}) || !reflect.DeepEqual(loaded.RawTopics, []string{"K8s", "节点内存耗尽"}) {
		t.Fatalf("labels = %+v", loaded)
	}
	w := f.aggregateWorker(t, aggregateFunc(func(_ context.Context, req artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: req.Memories[0].Content}, nil
	}))
	processDue(t, w)
	parent := f.artifactByTarget(t, artifact.KindTopicSummary, task.AggregateKey("user", "containers"))
	detail, err := f.artifacts.Get(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := detail.Revision
	if r == nil || r.TaxonomyVersion != topic.Version || r.RunID == "" || r.GeneratorVersion == "" || len(r.Provenance) == 0 {
		t.Fatalf("audit = %+v", r)
	}
	f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "Docker 部署。", ScopeKey: "user", Topics: []string{"docker"}})
	parent = f.artifactByTarget(t, artifact.KindTopicSummary, parent.TargetKey)
	if parent.State != artifact.StateStale {
		t.Fatalf("new sibling did not stale parent: %+v", parent)
	}
	processDue(t, w)
	detail, err = f.artifacts.Get(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Revision.MemoryRefs) != 2 ||
		!strings.Contains(string(detail.Revision.Payload), `"selected":2`) {
		t.Fatalf("parent coverage = %+v", detail.Revision)
	}
	report, err := f.store.ArtifactIntegrityCheck(context.Background())
	if err != nil || len(report.Issues) != 0 {
		t.Fatalf("integrity = %+v, %v", report, err)
	}
}

func TestP1SkillModelFailuresAreDiagnosable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider governance.SkillProvider
		reason   string
	}{
		{"unavailable", nil, "model_unavailable"},
		{"generation", skillFunc(func(context.Context, skill.Request) (skill.Draft, error) { return skill.Draft{}, errors.New("failed") }), "model_generation_failed"},
		{"protocol", skillFunc(func(context.Context, skill.Request) (skill.Draft, error) { return skill.Draft{}, nil }), "invalid_model_output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExperienceFixture(t)
			f.createMemory(t, memory.CreateInput{Kind: memory.KindProcedure, Content: "先检查，再部署。", ScopeKey: "user", Topics: []string{"testing"}})
			w := f.worker(t, nil, tc.provider)
			// Failure is persisted on the first attempt; normal worker retry policy
			// remains independent of artifact diagnostic visibility.
			for i := 0; i < 10; i++ {
				if _, err := w.ProcessOne(context.Background()); err != nil {
					t.Fatal(err)
				}
				a, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindSkill, task.SkillKey("user", "testing"))
				if err == nil && a.ReasonCode == tc.reason && a.State == artifact.StateDisabled {
					return
				}
			}
			t.Fatal("skill failure did not produce diagnostic")
		})
	}
}

func TestP1PartialExperienceAlwaysDisclaimsVerification(t *testing.T) {
	f := newExperienceFixture(t)
	run, err := f.experiences.Submit(context.Background(), governance.ExperienceSubmission{Goal: "部分部署", ScopeKey: "user", Outcome: governance.OutcomePartialSuccess})
	if err != nil {
		t.Fatal(err)
	}
	w := f.worker(t, experienceFunc(func(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
		return artifact.ExperienceResult{Content: "部分节点完成部署。"}, nil
	}), nil)
	f.processAll(t, w)
	a, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindExperienceCase, task.ExperienceKey(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.artifacts.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Revision.Content, "这是应用报告，不代表独立验证") || !strings.Contains(d.Revision.Content, "partial_success") {
		t.Fatalf("case = %+v", d.Revision)
	}
	report, err := f.store.ArtifactIntegrityCheck(context.Background())
	if err != nil || len(report.Issues) != 0 {
		t.Fatalf("integrity = %+v, %v", report, err)
	}
}

func TestP1ProfileDurabilityGateAndSkillDiagnostic(t *testing.T) {
	f := newAggregateFixture(t)
	stable := f.create(t, memory.CreateInput{Kind: memory.KindPreference, Content: "长期偏好安静环境。", Durability: memory.DurabilityDurable, ScopeKey: "user", Topics: []string{"work"}})
	for _, in := range []memory.CreateInput{
		{Kind: memory.KindEvent, Durability: memory.DurabilityDurable, Content: "单次出差。"},
		{Kind: memory.KindFact, Durability: memory.DurabilityTemporary, Content: "今天短期疼痛。"},
		{Kind: memory.KindFact, Content: "未知持久性。"},
		{Kind: memory.KindProcedure, Durability: memory.DurabilityEpisodic, Content: "本次行程步骤。"},
	} {
		in.ScopeKey, in.Topics = "user", []string{"travel"}
		f.create(t, in)
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		items, truncated, err := tx.ListAggregationInputs(ctx, artifact.Target{Kind: artifact.KindProfile, ScopeKey: "user"}, f.now, 64)
		if err == nil && (truncated || len(items) != 1 || items[0].ID != stable.ID) {
			t.Fatalf("profile inputs = %+v", items)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w := f.aggregateWorker(t, aggregateFunc(func(_ context.Context, req artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: req.Memories[0].Content}, nil
	}))
	processDue(t, w)
	if _, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindSkill, task.SkillKey("user", "work")); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("topic summary must not create an empty skill artifact: %v", err)
	}
}

func TestP1IntegrityDetectsSerializedSourceAndHashTampering(t *testing.T) {
	f := newAggregateFixture(t)
	f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "容器事实。", ScopeKey: "user", Topics: []string{"containers"}})
	w := f.aggregateWorker(t, aggregateFunc(func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: "摘要"}, nil
	}))
	processDue(t, w)
	a := f.artifactByTarget(t, artifact.KindTopicSummary, task.AggregateKey("user", "containers"))
	db, err := sql.Open("sqlite", f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `UPDATE artifact_revisions SET provenance_json = ?, content = 'tampered' WHERE artifact_id = ?`, `{"memory_refs":[],"run_refs":[]}`, a.ID); err != nil {
		t.Fatal(err)
	}
	report, err := f.store.ArtifactIntegrityCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(report)
	for _, code := range []string{"content_hash_mismatch", "dependency_mismatch", "metadata_mismatch"} {
		if !strings.Contains(string(encoded), code) {
			t.Fatalf("missing %s: %s", code, encoded)
		}
	}
}

func TestP1IntegrityDetectsMissingEdgesDeletedSourcesAndTruncation(t *testing.T) {
	f := newAggregateFixture(t)
	created := f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "原子事实。", ScopeKey: "user", Topics: []string{"testing"}})
	w := f.aggregateWorker(t, aggregateFunc(func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
		return artifact.AggregateResult{Content: "摘要"}, nil
	}))
	processDue(t, w)
	a := f.artifactByTarget(t, artifact.KindTopicSummary, task.AggregateKey("user", "testing"))
	db, err := sql.Open("sqlite", f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Simulate corruption outside the service: deleting a historical leaf and
	// its dependency edge must still be visible through immutable provenance.
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM artifact_memory_dependencies WHERE artifact_id = ?`, []any{a.ID}},
		{`DELETE FROM memory_revisions WHERE memory_id = ?`, []any{created.ID}},
		{`UPDATE artifact_revisions SET truncated = 1 WHERE artifact_id = ?`, []any{a.ID}},
	} {
		if _, err := db.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	report, err := f.store.ArtifactIntegrityCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(report)
	for _, code := range []string{"dependency_mismatch", "missing_memory_revision", "truncation_mismatch"} {
		if !strings.Contains(string(encoded), code) {
			t.Fatalf("missing %s: %s", code, encoded)
		}
	}
}
