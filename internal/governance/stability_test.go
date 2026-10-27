package governance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func TestGovernanceYieldsToRunnableIngestion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  ingest.JobStatus
		reason  string
		future  bool
		blocked bool
	}{
		{"queued", ingest.JobQueued, "", false, true},
		{"running", ingest.JobRunning, "", false, true},
		{"due waiting", ingest.JobWaiting, ingest.WaitReasonTransient, false, true},
		{"backoff", ingest.JobWaiting, ingest.WaitReasonTransient, true, false},
		{"configuration", ingest.JobWaiting, ingest.WaitReasonConfiguration, false, false},
		{"finished", ingest.JobSucceeded, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGovernanceFixture(t)
			service := ingest.NewService(sqlite.IngestStore{Store: f.store}, f.store, ingest.WithClock(func() time.Time { return f.now }))
			receipt, err := service.Submit(context.Background(), ingest.Submission{Content: []byte(`"queued input"`)})
			if err != nil {
				t.Fatal(err)
			}
			err = f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
				job, err := tx.GetJob(ctx, receipt.JobID)
				if err != nil {
					return err
				}
				job.Status, job.WaitReason = tc.status, tc.reason
				if tc.future {
					next := f.now.Add(time.Hour)
					job.NextAttemptAt = &next
				}
				if err := tx.UpdateJob(ctx, job); err != nil {
					return err
				}
				_, _, err = tx.UpsertJob(ctx, task.UpsertInput{Kind: task.KindAggregate, TargetKey: task.AggregateKey("scope", "go"), InputHash: "v1", Now: f.now})
				if err != nil {
					return err
				}
				_, found, err := tx.ClaimNextGovernanceJob(ctx, f.now)
				if err == nil && found == tc.blocked {
					t.Errorf("found=%v blocked=%v", found, tc.blocked)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAggregateCoalescesAndSupersedesPendingRevision(t *testing.T) {
	f := newAggregateFixture(t)
	ctx := context.Background()
	first := f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "first fact", ScopeKey: "scope", Topics: []string{"go"}})
	key := task.AggregateKey("scope", "go")
	original, err := f.store.GetGovernanceJobByTarget(ctx, task.KindAggregate, key)
	if err != nil {
		t.Fatal(err)
	}
	f.create(t, memory.CreateInput{Kind: memory.KindFact, Content: "second fact", ScopeKey: "scope", Topics: []string{"go"}})
	merged, err := f.store.GetGovernanceJobByTarget(ctx, task.KindAggregate, key)
	if err != nil {
		t.Fatal(err)
	}
	if merged.ID != original.ID || merged.Revision <= original.Revision || merged.Attempts != 0 {
		t.Fatalf("old=%+v merged=%+v", original, merged)
	}
	f.revise(t, memory.ReviseInput{MemoryID: first.ID, Content: "updated fact", Reason: "update"})
	latest, err := f.store.GetGovernanceJobByTarget(ctx, task.KindAggregate, key)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != original.ID || latest.Revision <= merged.Revision {
		t.Fatalf("latest=%+v", latest)
	}
	calls := 0
	handler := governance.NewAggregateHandler(sqlite.GovernanceStore{Store: f.store}, f.artifacts,
		aggregateFunc(func(context.Context, artifact.AggregateRequest) (artifact.AggregateResult, error) {
			calls++
			return artifact.AggregateResult{Content: "must not generate"}, nil
		}), nil,
		governance.WithAggregateClock(func() time.Time { return f.now }))
	if err := handler.Process(ctx, merged); !errors.Is(err, governance.ErrRevisionChanged) || calls != 0 {
		t.Fatalf("superseded revision executed: err=%v calls=%d", err, calls)
	}
}

func TestGovernanceModelStatsAreIndependent(t *testing.T) {
	limiter := governance.NewLimiter(30, 0)
	limiter.Record("topic_summary", 2, 100, time.Now(), nil)
	limiter.Record("profile", 1, 50, time.Now(), context.DeadlineExceeded)
	limiter.Record("skill", 3, 120, time.Now(), nil)
	stats := limiter.Stats()
	if len(stats) != 3 || stats["topic_summary"].Calls != 1 || stats["profile"].Failures != 1 || stats["skill"].Calls != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	delete(stats, "skill")
	if len(limiter.Stats()) != 3 {
		t.Fatal("stats not copied")
	}
}
