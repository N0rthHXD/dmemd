package quality_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestLabeledClassification(t *testing.T) {
	dataset := loadDataset(t)
	passed := 0
	for _, testCase := range dataset.Classification {
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			f := newFixture(t, false)
			calls := 0
			suggest := func(context.Context, string, string, string) ([]string, error) {
				calls++
				return append([]string(nil), testCase.ModelTopics...), nil
			}
			created := f.create(t, memory.CreateInput{
				Kind:     memory.KindFact,
				Title:    testCase.Title,
				Content:  testCase.Content,
				ScopeKey: testCase.ScopeKey,
				Topics:   testCase.ExistingTopics,
			})
			classify := governance.NewClassifyHandler(f.store, sqlite.GovernanceStore{Store: f.store},
				f.memories, suggest, governance.NewLimiter(0, 0),
				governance.WithClassifyClock(func() time.Time { return f.now }))
			worker := f.worker(map[task.Kind]governance.Handler{task.KindClassify: classify})
			if processed := processAll(t, worker); processed == 0 {
				t.Fatal("classification task was not processed")
			}

			final, err := f.memories.Get(context.Background(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(final.Topics, memory.CanonicalTopics(testCase.ExpectedTopics)) {
				t.Errorf("topics = %v, want %v", final.Topics, testCase.ExpectedTopics)
			}
			if final.ScopeKey != testCase.ScopeKey {
				t.Errorf("scope_key = %q, want %q", final.ScopeKey, testCase.ScopeKey)
			}
			if final.Version != testCase.ExpectedVersion {
				t.Errorf("version = %d, want %d", final.Version, testCase.ExpectedVersion)
			}
			if testCase.ExpectModel != (calls > 0) {
				t.Errorf("model calls = %d, expect_model = %v", calls, testCase.ExpectModel)
			}
			if calls > 1 {
				t.Errorf("classification called the model %d times for one target", calls)
			}
			if !t.Failed() {
				passed++
			}
		})
	}
	t.Logf("quality classification: %d/%d cases", passed, len(dataset.Classification))
}
