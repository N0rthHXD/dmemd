package ingest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
)

func newProcessorWorker(f *processorFixture, options ...ingest.WorkerOption) *ingest.Worker {
	base := []ingest.WorkerOption{
		ingest.WithWorkerClock(time.Now),
		ingest.WithRetryDelays(0, 0, 0),
		ingest.WithPollInterval(5 * time.Millisecond),
	}
	return ingest.NewWorker(sqlite.IngestStore{Store: f.store}, f.store, f.processor, append(base, options...)...)
}

func waitForJob(t *testing.T, f *processorFixture, id string, done func(ingest.Job) bool) ingest.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		job := f.job(t, id)
		if done(job) {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job timeout: %+v", job)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWorkerKeepsTransientFailuresWaitingInsteadOfFailing(t *testing.T) {
	f := newProcessorFixture(t)
	var logs bytes.Buffer
	worker := newProcessorWorker(f, ingest.WithWorkerLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	f.fake.EnqueueStatus(500)
	f.fake.EnqueueStatus(500)
	f.fake.EnqueueStatus(500)
	receipt := f.submit(t, `"模型暂时不可用的输入"`)

	deadline := time.Now().Add(3 * time.Second)
	for {
		job := f.job(t, receipt.JobID)
		if job.Status == ingest.JobWaiting && job.Attempts == 3 {
			break
		}
		processed, err := worker.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			time.Sleep(5 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not wait: %+v", job)
		}
	}
	job := f.job(t, receipt.JobID)
	if job.Status != ingest.JobWaiting || job.Attempts != 3 || job.WaitReason != ingest.WaitReasonTransient {
		t.Fatalf("job = %+v", job)
	}
	if job.NextAttemptAt == nil {
		t.Fatal("waiting job has no next attempt")
	}
	if job.Error == "" || job.Error == "scripted failure" || strings.Contains(job.Error, "模型暂时不可用的输入") {
		t.Fatalf("error summary = %q", job.Error)
	}
	if f.fake.RequestCount() != 3 {
		t.Fatalf("model requests = %d, want 3", f.fake.RequestCount())
	}
	if strings.Contains(logs.String(), "模型暂时不可用的输入") || strings.Contains(logs.String(), "scripted failure") {
		t.Fatalf("logs leaked input or raw response: %s", logs.String())
	}
}

func TestWorkerRecoversAfterTransientFailure(t *testing.T) {
	f := newProcessorFixture(t)
	worker := newProcessorWorker(f)
	f.fake.EnqueueStatus(500)
	f.fake.EnqueueContent(`{"items":[{"span_ids":["seg-0001"],"content":"恢复后形成的记忆"}]}`)
	receipt := f.submit(t, `"恢复后形成的记忆"`)

	deadline := time.Now().Add(3 * time.Second)
	for {
		job := f.job(t, receipt.JobID)
		if job.Status == ingest.JobSucceeded {
			if job.Attempts != 2 || len(job.ObservationIDs) != 1 {
				t.Fatalf("succeeded job = %+v", job)
			}
			break
		}
		processed, err := worker.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			time.Sleep(5 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not succeed: %+v", job)
		}
	}
	if f.fake.RequestCount() != 2 {
		t.Fatalf("model requests = %d", f.fake.RequestCount())
	}
}

func TestWorkerParksPermanentProviderErrorUntilRestart(t *testing.T) {
	f := newProcessorFixture(t)
	worker := newProcessorWorker(f)
	f.fake.EnqueueStatus(401)
	receipt := f.submit(t, `"永久失败"`)
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	job := f.job(t, receipt.JobID)
	if job.Status != ingest.JobWaiting || job.WaitReason != ingest.WaitReasonConfiguration ||
		job.Attempts != 1 || f.fake.RequestCount() != 1 {
		t.Fatalf("job = %+v requests=%d", job, f.fake.RequestCount())
	}
	if job.NextAttemptAt != nil {
		t.Fatalf("configuration wait must not poll: %+v", job)
	}
}

func TestWorkerUnparseableResponseUsesRetryBudget(t *testing.T) {
	f := newProcessorFixture(t)
	worker := newProcessorWorker(f)
	// The minimal extraction protocol has no separate repair call: the worker
	// spends one model call per attempt and counts structural failures directly.
	for i := 0; i < 3; i++ {
		f.fake.EnqueueMalformed()
	}
	receipt := f.submit(t, `"不可解析响应"`)
	deadline := time.Now().Add(3 * time.Second)
	for {
		job := f.job(t, receipt.JobID)
		if job.Status == ingest.JobFailed {
			if job.Attempts != 3 || f.fake.RequestCount() != 3 {
				t.Fatalf("job=%+v requests=%d", job, f.fake.RequestCount())
			}
			if strings.Contains(job.Error, "I could not") {
				t.Fatalf("raw model text stored in error: %q", job.Error)
			}
			break
		}
		processed, err := worker.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			time.Sleep(5 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not fail: %+v", job)
		}
	}
}

func TestWorkerRunsSequentiallyAndCancellationExitsWait(t *testing.T) {
	t.Run("sequential", func(t *testing.T) {
		f := newProcessorFixture(t)
		worker := newProcessorWorker(f)
		f.fake.Enqueue(ingestFakeBlockedDraft("第一条"))
		first := f.submit(t, `"第一条"`)
		f.advance(time.Millisecond)
		second := f.submit(t, `"第二条"`)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		if err := f.fake.WaitStarted(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.fake.RequestCount() != 1 {
			t.Fatalf("model calls while first is blocked = %d", f.fake.RequestCount())
		}
		if job := f.job(t, second.JobID); job.Status != ingest.JobQueued {
			t.Fatalf("second job = %+v", job)
		}
		// Task queries and submissions must remain responsive while the model
		// call is blocked.
		readDone := make(chan error, 1)
		go func() {
			_, err := f.service.GetJob(context.Background(), first.JobID)
			readDone <- err
		}()
		select {
		case err := <-readDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("job query blocked behind model processing")
		}
		f.fake.Release()
		waitForJob(t, f, first.JobID, func(job ingest.Job) bool { return job.Status == ingest.JobSucceeded })
		waitForJob(t, f, second.JobID, func(job ingest.Job) bool { return job.Status == ingest.JobSucceeded })
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not stop")
		}
	})

	t.Run("idle wait cancellation", func(t *testing.T) {
		f := newProcessorFixture(t)
		worker := newProcessorWorker(f)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		time.Sleep(20 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("idle worker did not stop")
		}
	})

	t.Run("retry wait cancellation", func(t *testing.T) {
		f := newProcessorFixture(t)
		worker := newProcessorWorker(f, ingest.WithRetryDelays(time.Hour))
		f.fake.EnqueueStatus(503)
		receipt := f.submit(t, `"等待重试"`)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()
		waitForJob(t, f, receipt.JobID, func(job ingest.Job) bool {
			return job.Status == ingest.JobWaiting && job.Attempts == 1
		})
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not stop while waiting for retry")
		}
		job := f.job(t, receipt.JobID)
		if job.Status != ingest.JobWaiting || job.Attempts != 1 || job.NextAttemptAt == nil {
			t.Fatalf("job after cancellation = %+v", job)
		}
	})
}

func TestWorkerRecoveryRequeuesRunningWithoutFailingAttemptBudget(t *testing.T) {
	f := newProcessorFixture(t)
	worker := newProcessorWorker(f)
	ctx := context.Background()
	now := time.Now().UTC()

	insertJob := func(id string, status ingest.JobStatus, attempts int) {
		t.Helper()
		src := source.Source{ID: "source-" + id, Content: json.RawMessage(`"恢复"`), CreatedAt: now}
		if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			if err := tx.InsertSource(ctx, src); err != nil {
				return err
			}
			return tx.InsertJob(ctx, ingest.Job{
				ID: id, SourceID: src.ID, Status: status, Attempts: attempts,
				CreatedAt: now, UpdatedAt: now,
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	insertJob("running-1", ingest.JobRunning, 1)
	insertJob("running-3", ingest.JobRunning, 3)
	insertJob("succeeded", ingest.JobSucceeded, 2)
	insertJob("cancelled", ingest.JobCancelled, 1)
	insertJob("queued-2", ingest.JobQueued, 2)
	src := source.Source{ID: "source-failed-protocol", Content: json.RawMessage(`"恢复"`), CreatedAt: now}
	if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		return tx.InsertJob(ctx, ingest.Job{
			ID: "failed-protocol", SourceID: src.ID, Status: ingest.JobFailed, Attempts: 3,
			ProtocolFailures: 3, CreatedAt: now, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}

	if err := worker.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	running := f.job(t, "running-1")
	if running.Status != ingest.JobQueued || running.Attempts != 1 {
		t.Fatalf("running-1 = %+v", running)
	}
	exhausted := f.job(t, "running-3")
	if exhausted.Status != ingest.JobQueued || exhausted.Attempts != 3 {
		t.Fatalf("running-3 = %+v", exhausted)
	}
	if job := f.job(t, "succeeded"); job.Status != ingest.JobSucceeded {
		t.Fatalf("succeeded = %+v", job)
	}
	if job := f.job(t, "cancelled"); job.Status != ingest.JobCancelled {
		t.Fatalf("cancelled = %+v", job)
	}
	if job := f.job(t, "queued-2"); job.Status != ingest.JobQueued || job.Attempts != 2 {
		t.Fatalf("queued-2 = %+v", job)
	}
	if job := f.job(t, "failed-protocol"); job.Status != ingest.JobQueued ||
		job.ProtocolFailures != 0 || job.Attempts != 3 {
		t.Fatalf("failed-protocol = %+v", job)
	}
	if f.fake.RequestCount() != 0 {
		t.Fatalf("recovery called the model %d times", f.fake.RequestCount())
	}

	// running-1 is due and keeps its attempt count when claimed again. Other
	// queued jobs may be older in the deterministic insertion order, so keep
	// processing until running-1 reaches a terminal state.
	f.fake.EnqueueContent(`{"items":[]}`)
	deadline := time.Now().Add(3 * time.Second)
	for {
		completed := f.job(t, "running-1")
		if completed.Status == ingest.JobSucceeded {
			if completed.Attempts != 2 {
				t.Fatalf("running-1 attempts = %d", completed.Attempts)
			}
			break
		}
		processed, err := worker.ProcessOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			time.Sleep(5 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatalf("running-1 did not complete: %+v", completed)
		}
	}
}

func TestWorkerRejectsTerminalJobs(t *testing.T) {
	f := newProcessorFixture(t)
	worker := newProcessorWorker(f)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, status := range []ingest.JobStatus{ingest.JobSucceeded, ingest.JobFailed, ingest.JobCancelled} {
		id := "terminal-" + string(status)
		src := source.Source{ID: "source-" + id, Content: json.RawMessage(`"x"`), CreatedAt: now}
		if err := f.store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			if err := tx.InsertSource(ctx, src); err != nil {
				return err
			}
			return tx.InsertJob(ctx, ingest.Job{ID: id, SourceID: src.ID, Status: status, CreatedAt: now, UpdatedAt: now})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if processed, err := worker.ProcessOne(ctx); err != nil || processed {
		t.Fatalf("terminal processed=%v err=%v", processed, err)
	}
	if f.fake.RequestCount() != 0 {
		t.Fatalf("terminal jobs called the model")
	}
}
