package quality_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestLabeledSkills(t *testing.T) {
	dataset := loadDataset(t)
	passed := 0
	for _, testCase := range dataset.Skills {
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			f := newFixture(t, true)
			var skillRequest skill.Request
			provider := struct {
				aggregateFunc
				experienceFunc
				skillFunc
			}{
				aggregateFunc: func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
					return artifact.AggregateResult{Title: "案例", Content: "应用报告案例摘要。"}, nil
				},
				experienceFunc: func(_ context.Context, request artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
					return artifact.ExperienceResult{
						Problem: request.Run.Goal,
						Actions: request.Run.Actions,
						Content: "问题：" + request.Run.Goal + "\n结果（应用报告）：" + request.Run.Outcome,
					}, nil
				},
				skillFunc: func(_ context.Context, request skill.Request) (skill.Draft, error) {
					skillRequest = request
					return testCase.Replay, nil
				},
			}

			for _, item := range testCase.Procedures {
				kind := memory.Kind(item.Kind)
				if kind == "" {
					kind = memory.KindProcedure
				}
				topics := item.Topics
				if len(topics) == 0 && testCase.Topic != "" {
					topics = []string{testCase.Topic}
				}
				f.create(t, memory.CreateInput{
					Kind:     kind,
					Title:    item.Title,
					Content:  item.Content,
					ScopeKey: testCase.ScopeKey,
					Topics:   topics,
				})
			}
			experiences := governance.NewExperienceService(sqlite.GovernanceStore{Store: f.store},
				governance.WithExperienceClock(func() time.Time { return f.now }))
			for _, item := range testCase.Experiences {
				if _, err := experiences.Submit(context.Background(), governance.ExperienceSubmission{
					ScopeKey:   testCase.ScopeKey,
					Goal:       item.Goal,
					Conditions: item.Conditions,
					Actions:    item.Actions,
					Outcome:    governance.ExperienceOutcome(item.Outcome),
				}); err != nil {
					t.Fatalf("submit experience: %v", err)
				}
			}

			maxInputs := testCase.MaxInputs
			if maxInputs <= 0 {
				maxInputs = 32
			}
			classify := governance.NewClassifyHandler(f.store,
				sqlite.GovernanceStore{Store: f.store}, f.memories,
				func(context.Context, string, string, string) ([]string, error) { return nil, nil },
				governance.NewLimiter(0, 0),
				governance.WithClassifyClock(func() time.Time { return f.now }))
			aggregate := governance.NewAggregateHandler(
				sqlite.GovernanceStore{Store: f.store}, f.artifacts, provider,
				governance.NewLimiter(0, 0),
				governance.WithAggregateClock(func() time.Time { return f.now }),
				governance.WithAggregateMaxInputs(maxInputs),
				governance.WithExperienceProvider(provider),
			)
			build := governance.NewSkillHandler(
				sqlite.GovernanceStore{Store: f.store}, f.artifacts, provider,
				governance.NewLimiter(0, 0),
				governance.WithSkillClock(func() time.Time { return f.now }),
				governance.WithSkillMaxInputs(maxInputs),
			)
			worker := f.worker(map[task.Kind]governance.Handler{
				task.KindClassify:   classify,
				task.KindAggregate:  aggregate,
				task.KindBuildSkill: build,
			})
			if processed := processAll(t, worker); processed == 0 {
				t.Fatal("skill task was not processed")
			}

			item, err := f.store.GetArtifactByTarget(context.Background(), artifact.KindSkill,
				task.SkillKey(testCase.ScopeKey, testCase.Topic))
			if err != nil {
				t.Fatalf("read skill artifact: %v", err)
			}
			if item.State != artifact.StateActive {
				t.Errorf("skill state = %s, want active", item.State)
			}
			content, err := skill.NewService(f.artifacts).Get(context.Background(), item.ID, 0)
			if err != nil {
				t.Fatalf("render skill: %v", err)
			}
			if ok, problems := checkTerms(testCase.Name, content.Content,
				testCase.RequiredTerms, testCase.ForbiddenTerms); !ok {
				t.Error(strings.Join(problems, "\n"))
			}
			if testCase.ForbidCodeFence && strings.Contains(content.Content, "```") {
				t.Error("skill contains an executable or code block")
			}
			if !strings.Contains(content.Content, "name:") || !strings.Contains(content.Content, "description:") {
				t.Error("rendered SKILL.md is missing frontmatter")
			}
			if testCase.MaxInputs > 0 {
				if !skillRequest.Truncated {
					t.Error("bounded skill request was not marked truncated")
				}
				if !content.Truncated {
					t.Error("bounded skill revision was not marked truncated")
				}
			}
			expectedMemories := len(testCase.Procedures)
			if testCase.MaxInputs > 0 && expectedMemories > testCase.MaxInputs {
				expectedMemories = testCase.MaxInputs
			}
			if len(content.MemoryRefs) != expectedMemories {
				t.Errorf("memory refs = %d, want %d", len(content.MemoryRefs), expectedMemories)
			}
			if len(content.RunRefs) != len(testCase.Experiences) {
				t.Errorf("run refs = %d, want %d", len(content.RunRefs), len(testCase.Experiences))
			}
			if !t.Failed() {
				passed++
			}
		})
	}
	t.Logf("quality skills: %d/%d cases", passed, len(dataset.Skills))
}
