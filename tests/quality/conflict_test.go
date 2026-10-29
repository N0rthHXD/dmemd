//go:build legacy

package quality_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestLabeledConflictAdjudication(t *testing.T) {
	dataset := loadDataset(t)
	passed := 0
	falsePositives := 0
	falseNegatives := 0
	for _, testCase := range dataset.Conflicts {
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			f := newFixture(t, false)
			scopeKey := "quality:" + testCase.Name
			group, err := memory.NewFactGroup(memory.FactGroupInput{
				Subject: "质量样例主体", Predicate: "单值属性", Scope: scopeKey, Cardinality: memory.CardinalitySingle,
			}, f.now)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
				return tx.InsertFactGroup(ctx, group)
			}); err != nil {
				t.Fatal(err)
			}
			created := make([]memory.Memory, 0, len(testCase.Members))
			for index, spec := range testCase.Members {
				assertion := memory.AssertionStatus(spec.Assertion)
				if assertion == "" {
					assertion = memory.AssertionAccepted
				}
				input := memory.CreateInput{
					Kind:            memory.KindFact,
					Title:           "冲突样例",
					Content:         spec.Content,
					ScopeKey:        scopeKey,
					FactGroupID:     group.ID,
					Topics:          []string{"conflict-quality"},
					AssertionStatus: assertion,
					ValidFrom:       spec.ValidFrom,
					ValidTo:         spec.ValidTo,
					ValidFromBasis:  memory.BasisExplicit,
					ValidToBasis:    memory.BasisExplicit,
					Reason:          testCase.Name,
					Operation:       memory.OperationCreate,
				}
				if spec.ValidFrom == nil {
					input.ValidFromBasis = ""
				}
				if spec.ValidTo == nil {
					input.ValidToBasis = ""
				}
				item := f.create(t, input)
				if spec.State == "archived" {
					archived, err := f.memories.UpdateState(context.Background(), item.ID, memory.StateArchived)
					if err != nil {
						t.Fatalf("archive member %d: %v", index, err)
					}
					item = archived
				}
				created = append(created, item)
			}

			conflictID := idgen.NewUUID()
			members := make([]memory.ConflictMember, 0, len(created))
			for _, item := range created {
				members = append(members, memory.ConflictMember{MemoryID: item.ID, Version: item.Version})
			}
			if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
				if err := tx.InsertConflict(ctx, memory.Conflict{
					ID:          conflictID,
					FactGroupID: group.ID,
					Status:      memory.ConflictOpen,
					ReasonCode:  memory.ConflictContradictoryValue,
					Revision:    1,
					CreatedAt:   f.now,
					UpdatedAt:   f.now,
					Members:     members,
				}); err != nil {
					return err
				}
				_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
					Kind:          task.KindReviewConflict,
					TargetKey:     task.ConflictKey(conflictID),
					InputHash:     "quality-conflict:" + testCase.Name,
					NextAttemptAt: &f.now,
					Now:           f.now,
				})
				return err
			}); err != nil {
				t.Fatalf("insert conflict: %v", err)
			}

			classify := governance.NewClassifyHandler(f.store,
				sqlite.GovernanceStore{Store: f.store}, f.memories,
				func(context.Context, string, string, string) ([]string, error) { return nil, nil },
				governance.NewLimiter(0, 0),
				governance.WithClassifyClock(func() time.Time { return f.now }))
			review := governance.NewConflictHandler(
				sqlite.GovernanceStore{Store: f.store}, f.memories,
				governance.WithConflictClock(func() time.Time { return f.now }))
			worker := f.worker(map[task.Kind]governance.Handler{
				task.KindClassify:       classify,
				task.KindReviewConflict: review,
			})
			processAll(t, worker)

			conflict, err := f.store.GetConflict(context.Background(), conflictID)
			if err != nil {
				t.Fatalf("read conflict: %v", err)
			}
			if got := string(conflict.Status); got != testCase.ExpectedStatus {
				if testCase.ExpectedStatus == "open" {
					falsePositives++
				} else {
					falseNegatives++
				}
				t.Errorf("status = %s, want %s", got, testCase.ExpectedStatus)
			} else if testCase.ExpectedAction != "" {
				var resolution struct {
					Action string `json:"action"`
				}
				if err := json.Unmarshal([]byte(conflict.Resolution), &resolution); err != nil {
					t.Errorf("decode resolution: %v", err)
				} else if resolution.Action != testCase.ExpectedAction {
					t.Errorf("action = %q, want %q", resolution.Action, testCase.ExpectedAction)
				}
			}
			if !t.Failed() {
				passed++
			}
		})
	}
	t.Logf("quality conflicts: %d/%d cases, false_positive=%d, false_negative=%d",
		passed, len(dataset.Conflicts), falsePositives, falseNegatives)
	if falsePositives != 0 || falseNegatives != 0 {
		t.Errorf("deterministic conflict review misclassified %d+%d cases",
			falsePositives, falseNegatives)
	}
}
