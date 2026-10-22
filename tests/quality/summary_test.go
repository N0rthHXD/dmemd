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
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestLabeledSummaries(t *testing.T) {
	dataset := loadDataset(t)
	passed := 0
	for _, testCase := range dataset.Summaries {
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			f := newFixture(t, true)
			requests := make(map[string]artifact.AggregateRequest)
			provider := aggregateFunc(func(_ context.Context, request artifact.AggregateRequest) (artifact.AggregateResult, error) {
				requests[string(request.Kind)+"|"+request.Topic] = request
				return testCase.Replay, nil
			})
			for _, item := range testCase.Memories {
				kind := memory.Kind(item.Kind)
				if kind == "" {
					kind = memory.KindFact
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

			maxInputs := testCase.MaxInputs
			if maxInputs <= 0 {
				maxInputs = 64
			}
			classify := governance.NewClassifyHandler(f.store,
				sqlite.GovernanceStore{Store: f.store}, f.memories,
				func(context.Context, string, string, string) ([]string, error) { return nil, nil },
				governance.NewLimiter(0, 0),
				governance.WithClassifyClock(func() time.Time { return f.now }))
			aggregate := governance.NewAggregateHandler(
				sqlite.GovernanceStore{Store: f.store},
				f.artifacts,
				provider,
				governance.NewLimiter(0, 0),
				governance.WithAggregateClock(func() time.Time { return f.now }),
				governance.WithAggregateMaxInputs(maxInputs),
			)
			worker := f.worker(map[task.Kind]governance.Handler{
				task.KindClassify:  classify,
				task.KindAggregate: aggregate,
			})
			if processed := processAll(t, worker); processed == 0 {
				t.Fatal("aggregate task was not processed")
			}

			target := targetOf(testCase)
			item, err := f.store.GetArtifactByTarget(context.Background(), target.Kind, target.TargetKey())
			if err != nil {
				t.Fatalf("read artifact %s: %v", target.TargetKey(), err)
			}
			if item.State != artifact.StateActive {
				t.Errorf("artifact state = %s, want active", item.State)
			}
			detail, err := f.artifacts.Get(context.Background(), item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Revision == nil {
				t.Fatal("artifact has no current revision")
			}
			if ok, problems := checkTerms(testCase.Name, detail.Revision.Content,
				testCase.RequiredTerms, testCase.ForbiddenTerms); !ok {
				t.Error(strings.Join(problems, "\n"))
			}
			if testCase.MaxInputs > 0 {
				if !detail.Revision.Truncated {
					t.Error("bounded summary was not marked truncated")
				}
				if detail.Revision.ReasonCode != "input_truncated" {
					t.Errorf("reason_code = %q, want input_truncated", detail.Revision.ReasonCode)
				}
			}
			eligible := summaryEligibleMemories(testCase)
			expectedRefs := len(eligible)
			if testCase.MaxInputs > 0 && expectedRefs > testCase.MaxInputs {
				expectedRefs = testCase.MaxInputs
			}
			if len(detail.Revision.MemoryRefs) != expectedRefs {
				t.Errorf("memory refs = %d, want %d", len(detail.Revision.MemoryRefs), expectedRefs)
			}
			request, found := requests[string(testCase.Kind)+"|"+testCase.Topic]
			if !found {
				t.Fatalf("model did not receive a %s request for topic %q", testCase.Kind, testCase.Topic)
			}
			if request.ScopeKey != testCase.ScopeKey || request.Kind != testCase.Kind {
				t.Errorf("request target = %s/%s, want %s/%s", request.Kind, request.ScopeKey,
					testCase.Kind, testCase.ScopeKey)
			}
			if testCase.MaxInputs > 0 {
				if !request.Truncated {
					t.Error("bounded model request was not marked truncated")
				}
				if len(request.Memories) != testCase.MaxInputs {
					t.Errorf("request inputs = %d, want %d", len(request.Memories), testCase.MaxInputs)
				}
			} else if len(request.Memories) != len(eligible) {
				t.Errorf("request inputs = %d, want %d", len(request.Memories), len(eligible))
			}
			if !t.Failed() {
				passed++
			}
		})
	}
	t.Logf("quality summaries: %d/%d cases", passed, len(dataset.Summaries))
}
