package ingest_test

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

type timeoutExtractor struct{}

func (timeoutExtractor) Extract(ctx context.Context, _ ingest.ExtractionRequest) (ingest.ExtractionResult, error) {
	<-ctx.Done()
	return ingest.ExtractionResult{}, ctx.Err()
}

func TestDecisionRepairRunsWithinIngestAttempt(t *testing.T) {
	for _, repairSucceeds := range []bool{true, false} {
		name := "invalid repair"
		if repairSucceeds {
			name = "successful repair"
		}
		t.Run(name, func(t *testing.T) {
			f := newProcessorFixture(t)
			f.createMemory(t, memory.CreateInput{Kind: memory.KindFact, Content: "项目使用 SQLite 保存原子记忆"})
			f.fake.EnqueueContent(`{"candidates":[{"content":"项目使用 SQLite 保存摘要"},{"content":"项目使用 SQLite 保存画像"}]}`)
			f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"skip"},{"candidate_id":"c2","action":"change","target_memory_ids":["a","b"]}]}`)
			if repairSucceeds {
				f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c2","action":"separate"}]}`)
			} else {
				f.fake.EnqueueContent(`{"decisions":[{"candidate_id":"c2","action":"change","target_memory_ids":["a","b"]}]}`)
			}
			receipt := f.submit(t, `"项目使用 SQLite 保存摘要。项目使用 SQLite 保存画像。"`)
			processed, err := newProcessorWorker(f, ingest.WithMaxAttempts(1)).ProcessOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("processed=%v err=%v", processed, err)
			}
			job := f.job(t, receipt.JobID)
			if repairSucceeds {
				if job.Status != ingest.JobSucceeded || job.Attempts != 1 || job.ProtocolFailures != 0 || len(job.MemoryIDs) != 1 {
					t.Fatalf("job=%+v", job)
				}
			} else if job.Status != ingest.JobFailed || job.ProtocolFailures != 1 || job.Stage != "decision_repair" || job.ReasonCode != "decision_repair_failed" {
				t.Fatalf("job=%+v", job)
			}
			if f.fake.RequestCount() != 3 {
				t.Fatalf("model calls=%d want extraction + decision + repair", f.fake.RequestCount())
			}
		})
	}
}

func TestWaitingPersistsBudgetStageAndReason(t *testing.T) {
	for _, tc := range []struct {
		name               string
		budget, extraction time.Duration
		reason             string
	}{
		{"attempt", 100 * time.Millisecond, time.Second, "process_budget_exceeded"},
		{"model", time.Second, time.Millisecond, "model_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProcessorFixture(t)
			f.processor = ingest.NewProcessor(sqlite.IngestStore{Store: f.store}, f.store, f.search, f.memories, timeoutExtractor{},
				ingest.WithProcessingBudget(tc.budget, tc.extraction, time.Second))
			receipt := f.submit(t, `"private source"`)
			processed, err := newProcessorWorker(f).ProcessOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("processed=%v err=%v", processed, err)
			}
			job := f.job(t, receipt.JobID)
			if job.Status != ingest.JobWaiting || job.Stage != "extraction" || job.ReasonCode != tc.reason || job.WaitReason != ingest.WaitReasonTransient || job.ProtocolFailures != 0 {
				t.Fatalf("job=%+v", job)
			}
			if job.NextAttemptAt == nil {
				t.Fatal("missing retry time")
			}
		})
	}
}
