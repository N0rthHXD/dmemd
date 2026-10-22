package quality_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

const datasetPath = "testdata/labeled.json"

type dataset struct {
	Classification []classificationCase `json:"classification"`
	Summaries      []summaryCase        `json:"summaries"`
	Conflicts      []conflictCase       `json:"conflicts"`
	Skills         []skillCase          `json:"skills"`
}

type classificationCase struct {
	Name            string   `json:"name"`
	ScopeKey        string   `json:"scope_key"`
	Title           string   `json:"title"`
	Content         string   `json:"content"`
	ExistingTopics  []string `json:"existing_topics"`
	ModelTopics     []string `json:"model_topics"`
	ExpectedTopics  []string `json:"expected_topics"`
	ExpectedVersion int      `json:"expected_version"`
	ExpectModel     bool     `json:"expect_model"`
}

type memorySpec struct {
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Topics  []string `json:"topics,omitempty"`
}

type summaryCase struct {
	Name           string                   `json:"name"`
	Kind           artifact.Kind            `json:"kind"`
	ScopeKey       string                   `json:"scope_key"`
	Topic          string                   `json:"topic"`
	MaxInputs      int                      `json:"max_inputs"`
	Memories       []memorySpec             `json:"memories"`
	Replay         artifact.AggregateResult `json:"replay"`
	RequiredTerms  []string                 `json:"required_terms"`
	ForbiddenTerms []string                 `json:"forbidden_terms"`
}

type conflictMemberCase struct {
	Content   string     `json:"content"`
	State     string     `json:"state"`
	Assertion string     `json:"assertion"`
	ValidFrom *time.Time `json:"valid_from,omitempty"`
	ValidTo   *time.Time `json:"valid_to,omitempty"`
}

type conflictCase struct {
	Name           string               `json:"name"`
	ExpectedStatus string               `json:"expected_status"`
	ExpectedAction string               `json:"expected_action,omitempty"`
	Members        []conflictMemberCase `json:"members"`
}

type experienceSpec struct {
	Goal       string   `json:"goal"`
	Conditions []string `json:"conditions"`
	Actions    []string `json:"actions"`
	Outcome    string   `json:"outcome"`
}

type skillCase struct {
	Name            string           `json:"name"`
	ScopeKey        string           `json:"scope_key"`
	Topic           string           `json:"topic"`
	MaxInputs       int              `json:"max_inputs"`
	Procedures      []memorySpec     `json:"procedures"`
	Experiences     []experienceSpec `json:"experiences"`
	Replay          skill.Draft      `json:"replay"`
	RequiredTerms   []string         `json:"required_terms"`
	ForbiddenTerms  []string         `json:"forbidden_terms"`
	ForbidCodeFence bool             `json:"forbid_code_fence"`
}

func loadDataset(t *testing.T) dataset {
	t.Helper()
	body, err := os.ReadFile(datasetPath)
	if err != nil {
		t.Fatalf("read labeled dataset: %v", err)
	}
	var data dataset
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("decode labeled dataset: %v", err)
	}
	if len(data.Classification) == 0 || len(data.Summaries) == 0 ||
		len(data.Conflicts) == 0 || len(data.Skills) == 0 {
		t.Fatalf("labeled dataset must cover all four categories: %+v", data)
	}
	return data
}

func memoryInputs(c summaryCase) []artifact.MemoryInput {
	eligible := summaryEligibleMemories(c)
	limit := len(eligible)
	if c.MaxInputs > 0 && limit > c.MaxInputs {
		limit = c.MaxInputs
	}
	result := make([]artifact.MemoryInput, 0, limit)
	for index, item := range eligible[:limit] {
		topics := item.Topics
		if len(topics) == 0 && c.Topic != "" {
			topics = []string{c.Topic}
		}
		result = append(result, artifact.MemoryInput{
			ID:       fmt.Sprintf("summary-memory-%d", index+1),
			Version:  1,
			Kind:     item.Kind,
			Title:    item.Title,
			Content:  item.Content,
			ScopeKey: c.ScopeKey,
			Topics:   topics,
		})
	}
	return result
}

func procedureInputs(c skillCase) []artifact.MemoryInput {
	result := make([]artifact.MemoryInput, 0, len(c.Procedures))
	for index, item := range c.Procedures {
		topics := item.Topics
		if len(topics) == 0 && c.Topic != "" {
			topics = []string{c.Topic}
		}
		result = append(result, artifact.MemoryInput{
			ID:       fmt.Sprintf("skill-memory-%d", index+1),
			Version:  1,
			Kind:     item.Kind,
			Title:    item.Title,
			Content:  item.Content,
			ScopeKey: c.ScopeKey,
			Topics:   topics,
		})
	}
	return result
}

func skillExperienceInputs(c skillCase) []artifact.ExperienceInput {
	result := make([]artifact.ExperienceInput, 0, len(c.Experiences))
	for index, item := range c.Experiences {
		result = append(result, artifact.ExperienceInput{
			ID:         fmt.Sprintf("skill-experience-%d", index+1),
			ScopeKey:   c.ScopeKey,
			Goal:       item.Goal,
			Conditions: append([]string(nil), item.Conditions...),
			Actions:    append([]string(nil), item.Actions...),
			Outcome:    item.Outcome,
		})
	}
	return result
}

func checkTerms(name, text string, required, forbidden []string) (ok bool, problems []string) {
	lower := strings.ToLower(text)
	for _, term := range required {
		if !strings.Contains(lower, strings.ToLower(term)) {
			problems = append(problems, fmt.Sprintf("%s: missing required term %q", name, term))
		}
	}
	for _, term := range forbidden {
		if strings.Contains(lower, strings.ToLower(term)) {
			problems = append(problems, fmt.Sprintf("%s: contains forbidden term %q", name, term))
		}
	}
	return len(problems) == 0, problems
}

type fixture struct {
	store     *sqlite.Store
	memories  *memory.Service
	artifacts *artifact.Service
	now       time.Time
}

func newFixture(t *testing.T, withArtifacts bool) *fixture {
	t.Helper()
	store := testutil.NewStore(t)
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	var artifacts *artifact.Service
	var options []memory.Option
	options = append(options, memory.WithGovernanceDebounce(0), memory.WithClock(clock))
	if withArtifacts {
		artifacts = artifact.NewService(store, retrieval.NewArtifactDocumentBuilder(),
			artifact.WithClock(clock), artifact.WithDebounce(0))
		options = append(options, memory.WithArtifactSync(artifacts))
	}
	memories := memory.NewService(store, sqlite.MemoryStore{Store: store},
		retrieval.NewDocumentBuilder(), options...)
	return &fixture{store: store, memories: memories, artifacts: artifacts, now: now}
}

func (f *fixture) create(t *testing.T, input memory.CreateInput) memory.Memory {
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

func (f *fixture) worker(handlers map[task.Kind]governance.Handler) *governance.Worker {
	return governance.NewWorker(
		sqlite.GovernanceStore{Store: f.store},
		handlers,
		governance.WithReconcileInterval(0),
		governance.WithRetryDelays(0, 0, 0),
		governance.WithPollInterval(time.Millisecond),
		governance.WithWorkerClock(func() time.Time { return f.now }),
	)
}

func processAll(t *testing.T, worker *governance.Worker) int {
	t.Helper()
	for processed := 0; processed < 2000; processed++ {
		ok, err := worker.ProcessOne(context.Background())
		if err != nil {
			t.Fatalf("governance ProcessOne: %v", err)
		}
		if !ok {
			return processed
		}
	}
	t.Fatal("governance did not quiesce within 2000 tasks")
	return 0
}

type aggregateFunc func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error)

func (f aggregateFunc) Summarize(ctx context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
	return f(ctx, request)
}

type experienceFunc func(context.Context, artifact.ExperienceRequest) (artifact.ExperienceResult, error)

func (f experienceFunc) ComposeExperience(ctx context.Context, request artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
	return f(ctx, request)
}

type skillFunc func(context.Context, skill.Request) (skill.Draft, error)

func (f skillFunc) ComposeSkill(ctx context.Context, request skill.Request) (skill.Draft, error) {
	return f(ctx, request)
}

func targetOf(c summaryCase) artifact.Target {
	return artifact.Target{Kind: c.Kind, ScopeKey: c.ScopeKey, Topic: c.Topic}
}

func summaryEligibleMemories(c summaryCase) []memorySpec {
	result := make([]memorySpec, 0, len(c.Memories))
	for _, item := range c.Memories {
		if c.Kind == artifact.KindProfile && item.Kind == string(memory.KindEvent) {
			// Profiles describe stable preferences, procedures and facts. A
			// one-off event is intentionally not selected as profile input.
			continue
		}
		result = append(result, item)
	}
	return result
}
