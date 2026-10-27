package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
)

func TestClaimNextJobOrdersDueJobsAndCountsAttempts(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-jobs"
	seedSource(t, store, sourceID)
	ctx := context.Background()
	base := testTime(0)

	first := insertTestJob(t, store, ingest.Job{
		ID: "job-first", SourceID: sourceID, Status: ingest.JobQueued,
		CreatedAt: base.Add(-2 * time.Second), UpdatedAt: base.Add(-2 * time.Second),
	})
	second := insertTestJob(t, store, ingest.Job{
		ID: "job-second", SourceID: sourceID, Status: ingest.JobQueued,
		CreatedAt: base.Add(-time.Second), UpdatedAt: base.Add(-time.Second),
	})
	future := base.Add(time.Hour)
	insertTestJob(t, store, ingest.Job{
		ID: "job-future", SourceID: sourceID, Status: ingest.JobQueued,
		NextAttemptAt: &future, CreatedAt: base, UpdatedAt: base,
	})

	claimed, ok, err := claimJob(store, ctx, base)
	if err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v", ok, err)
	}
	if claimed.ID != first.ID || claimed.Status != ingest.JobRunning || claimed.Attempts != 1 {
		t.Fatalf("first claim = %+v", claimed)
	}
	if claimed.StartedAt == nil || !claimed.StartedAt.Equal(base) {
		t.Fatalf("started_at = %v, want %v", claimed.StartedAt, base)
	}

	claimed, ok, err = claimJob(store, ctx, base)
	if err != nil || !ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
	if claimed.ID != second.ID || claimed.Attempts != 1 {
		t.Fatalf("second claim = %+v", claimed)
	}

	if _, ok, err := claimJob(store, ctx, base); err != nil || ok {
		t.Fatalf("future job claimed early: ok=%v err=%v", ok, err)
	}
	claimed, ok, err = claimJob(store, ctx, future)
	if err != nil || !ok || claimed.ID != "job-future" {
		t.Fatalf("future claim = %+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestTerminalJobsAreNeverClaimedAgain(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-terminal"
	seedSource(t, store, sourceID)
	ctx := context.Background()
	base := testTime(0)
	for i, status := range []ingest.JobStatus{ingest.JobSucceeded, ingest.JobFailed, ingest.JobCancelled} {
		insertTestJob(t, store, ingest.Job{
			ID: "terminal-" + string(status), SourceID: sourceID, Status: status,
			CreatedAt: base.Add(time.Duration(i) * time.Second), UpdatedAt: base,
		})
	}
	if claimed, ok, err := claimJob(store, ctx, base.Add(time.Hour)); err != nil || ok {
		t.Fatalf("terminal job claimed: %+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestUpdateJobPersistsTerminalResultAndError(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-update"
	seedSource(t, store, sourceID)
	ctx := context.Background()
	base := testTime(0)
	job := insertTestJob(t, store, ingest.Job{
		ID: "job-update", SourceID: sourceID, Status: ingest.JobQueued,
		CreatedAt: base, UpdatedAt: base,
	})
	claimed, ok, err := claimJob(store, ctx, base)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if claimed.ID != job.ID {
		t.Fatalf("claimed %s, want %s", claimed.ID, job.ID)
	}

	claimed.Status = ingest.JobSucceeded
	claimed.MemoryIDs = []string{"memory-1", "memory-2"}
	claimed.Error = ""
	claimed.QualityOutcome = ingest.QualityPartial
	claimed.ExtractionStats = ingest.ExtractionStats{
		RawCandidates: 3, ParsedCandidates: 3, NormalizedCandidates: 1, Normalizations: 2,
		RepairAttempts: 1, RepairedCandidates: 1, RepairSucceeded: 1,
		ExcludedCandidates: 1, IsolatedCandidates: 1, AcceptedCandidates: 2, MemoryWrites: 2,
	}
	claimed.Diagnostics = []ingest.ExtractionDiagnostic{{
		CandidateID: "c2", Field: "evidence[0]", Code: "evidence_quote_ambiguous",
		Stage: "validate", Disposition: "isolated",
	}}
	claimed.Convergence = []ingest.ConvergenceDiagnostic{{
		CandidateID: "c1", GroupID: "group-1", Code: ingest.ConvergenceMissingUpdateEvidence,
	}}
	claimed.FinishedAt = &base
	claimed.UpdatedAt = base
	claimed.NextAttemptAt = nil
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.UpdateJob(ctx, claimed)
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != ingest.JobSucceeded || len(loaded.MemoryIDs) != 2 || loaded.MemoryIDs[1] != "memory-2" {
		t.Fatalf("loaded job = %+v", loaded)
	}
	if loaded.QualityOutcome != ingest.QualityPartial || loaded.ExtractionStats.RepairSucceeded != 1 ||
		loaded.ExtractionStats.MemoryWrites != 2 || len(loaded.Diagnostics) != 1 ||
		loaded.Diagnostics[0].CandidateID != "c2" || len(loaded.Convergence) != 1 ||
		loaded.Convergence[0].Code != ingest.ConvergenceMissingUpdateEvidence {
		t.Fatalf("loaded quality = %+v", loaded)
	}
	if loaded.FinishedAt == nil || !loaded.FinishedAt.Equal(base) {
		t.Fatalf("finished_at = %v", loaded.FinishedAt)
	}
	if loaded.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", loaded.Attempts)
	}
}

func TestUpdateJobPersistsFailedErrorSummary(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-failed"
	seedSource(t, store, sourceID)
	ctx := context.Background()
	base := testTime(0)
	insertTestJob(t, store, ingest.Job{
		ID: "job-failed", SourceID: sourceID, Status: ingest.JobQueued,
		CreatedAt: base, UpdatedAt: base,
	})
	job, ok, err := claimJob(store, ctx, base)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	job.Status = ingest.JobFailed
	job.Attempts = 3
	job.Error = "model timeout after 60s"
	job.FinishedAt = &base
	job.UpdatedAt = base
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.UpdateJob(ctx, job)
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != ingest.JobFailed || loaded.Attempts != 3 || loaded.Error != "model timeout after 60s" {
		t.Fatalf("failed job = %+v", loaded)
	}
	if loaded.MemoryIDs == nil || len(loaded.MemoryIDs) != 0 {
		t.Fatalf("failed job memory_ids = %#v", loaded.MemoryIDs)
	}
	if claimed, ok, err := claimJob(store, ctx, base.Add(time.Hour)); err != nil || ok {
		t.Fatalf("failed job reclaimed: %+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestRequeueRunningJobsPreservesAttempts(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-requeue"
	seedSource(t, store, sourceID)
	ctx := context.Background()
	base := testTime(0)
	started := base.Add(-time.Minute)
	insertTestJob(t, store, ingest.Job{
		ID: "job-running", SourceID: sourceID, Status: ingest.JobRunning, Attempts: 2,
		StartedAt: &started, CreatedAt: base.Add(-time.Minute), UpdatedAt: started,
	})
	insertTestJob(t, store, ingest.Job{
		ID: "job-finished", SourceID: sourceID, Status: ingest.JobSucceeded, Attempts: 1,
		CreatedAt: base.Add(-time.Minute), UpdatedAt: started,
	})

	var count int64
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		count, err = tx.RequeueRunningJobs(ctx, base)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("requeued = %d, want 1", count)
	}
	requeued, err := store.GetJob(ctx, "job-running")
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != ingest.JobQueued || requeued.Attempts != 2 {
		t.Fatalf("requeued job = %+v", requeued)
	}
	if requeued.NextAttemptAt == nil || !requeued.NextAttemptAt.Equal(base) {
		t.Fatalf("next_attempt_at = %v, want %v", requeued.NextAttemptAt, base)
	}
	if requeued.StartedAt == nil || !requeued.StartedAt.Equal(started) {
		t.Fatalf("started_at changed to %v", requeued.StartedAt)
	}

	claimed, ok, err := claimJob(store, ctx, base)
	if err != nil || !ok {
		t.Fatalf("claim requeued ok=%v err=%v", ok, err)
	}
	if claimed.ID != "job-running" || claimed.Attempts != 3 {
		t.Fatalf("claimed requeued job = %+v", claimed)
	}
}

func TestCancelJobsBySourceOnlyCancelsPendingWork(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-cancel"
	seedSource(t, store, sourceID)
	ctx := context.Background()
	base := testTime(0)
	insertTestJob(t, store, ingest.Job{ID: "queued", SourceID: sourceID, Status: ingest.JobQueued, CreatedAt: base, UpdatedAt: base})
	insertTestJob(t, store, ingest.Job{ID: "running", SourceID: sourceID, Status: ingest.JobRunning, Attempts: 1, CreatedAt: base, UpdatedAt: base})
	insertTestJob(t, store, ingest.Job{ID: "succeeded", SourceID: sourceID, Status: ingest.JobSucceeded, Attempts: 1, CreatedAt: base, UpdatedAt: base})

	var cancelled int64
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		cancelled, err = tx.CancelJobsBySource(ctx, sourceID, "source redacted", base)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if cancelled != 2 {
		t.Fatalf("cancelled = %d, want 2", cancelled)
	}
	for _, id := range []string{"queued", "running"} {
		job, err := store.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != ingest.JobCancelled || job.Error != "source redacted" {
			t.Fatalf("%s = %+v", id, job)
		}
	}
	succeeded, err := store.GetJob(ctx, "succeeded")
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.Status != ingest.JobSucceeded {
		t.Fatalf("succeeded job changed to %s", succeeded.Status)
	}
}

func TestRecoveryRequeuesRunningAndConfigurationWaits(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-recovery"
	seedSource(t, store, sourceID)
	ctx := context.Background()
	base := testTime(0)
	insertTestJob(t, store, ingest.Job{ID: "running-three", SourceID: sourceID, Status: ingest.JobRunning, Attempts: 3, CreatedAt: base, UpdatedAt: base})
	insertTestJob(t, store, ingest.Job{ID: "queued-three", SourceID: sourceID, Status: ingest.JobQueued, Attempts: 3, CreatedAt: base, UpdatedAt: base})
	insertTestJob(t, store, ingest.Job{ID: "waiting-config", SourceID: sourceID, Status: ingest.JobWaiting,
		Attempts: 1, WaitReason: ingest.WaitReasonConfiguration, CreatedAt: base, UpdatedAt: base})
	insertTestJob(t, store, ingest.Job{ID: "queued-two", SourceID: sourceID, Status: ingest.JobQueued, Attempts: 2, CreatedAt: base, UpdatedAt: base})
	insertTestJob(t, store, ingest.Job{ID: "succeeded-three", SourceID: sourceID, Status: ingest.JobSucceeded, Attempts: 3, CreatedAt: base, UpdatedAt: base})

	var requeuedRunning, requeuedConfiguration int64
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		requeuedRunning, err = tx.RequeueRunningJobs(ctx, base)
		if err != nil {
			return err
		}
		requeuedConfiguration, err = tx.RequeueWaitingConfigurationJobs(ctx, base)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if requeuedRunning != 1 || requeuedConfiguration != 1 {
		t.Fatalf("requeued running=%d configuration=%d", requeuedRunning, requeuedConfiguration)
	}
	for _, id := range []string{"running-three", "waiting-config"} {
		job, err := store.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != ingest.JobQueued || job.NextAttemptAt == nil {
			t.Fatalf("%s = %+v", id, job)
		}
	}
	if job, err := store.GetJob(ctx, "queued-three"); err != nil || job.Status != ingest.JobQueued {
		t.Fatalf("queued-three = %+v err=%v", job, err)
	}
	if job, err := store.GetJob(ctx, "queued-two"); err != nil || job.Status != ingest.JobQueued || job.Attempts != 2 {
		t.Fatalf("queued-two = %+v err=%v", job, err)
	}
	if job, err := store.GetJob(ctx, "succeeded-three"); err != nil || job.Status != ingest.JobSucceeded {
		t.Fatalf("succeeded-three = %+v err=%v", job, err)
	}
}

func insertTestJob(t *testing.T, store *Store, job ingest.Job) ingest.Job {
	t.Helper()
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		return tx.InsertJob(ctx, job)
	}); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	return job
}

func claimJob(store *Store, ctx context.Context, now time.Time) (ingest.Job, bool, error) {
	var claimed ingest.Job
	var ok bool
	err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		claimed, ok, err = tx.ClaimNextJob(ctx, now)
		return err
	})
	return claimed, ok, err
}

func TestGovernanceFairnessBypassesContinuousIngestOnlyWhenOld(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-fairness"
	seedSource(t, store, sourceID)
	ctx := context.Background()
	base := testTime(0)
	insertTestJob(t, store, ingest.Job{
		ID: "busy-ingest", SourceID: sourceID, Status: ingest.JobQueued,
		CreatedAt: base, UpdatedAt: base,
	})
	if err := store.ConfigureGovernanceFairness(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	upsert := func(target string, createdAt time.Time) {
		t.Helper()
		if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
			_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
				Kind: task.KindClassify, TargetKey: target, InputHash: target,
				NextAttemptAt: &base, Now: createdAt,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	upsert("young", base)
	var claimed task.Job
	var ok bool
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		claimed, ok, err = tx.ClaimNextGovernanceJob(ctx, base)
		return err
	}); err != nil || ok {
		t.Fatalf("young governance claimed during ingest: job=%+v ok=%v err=%v", claimed, ok, err)
	}

	upsert("old", base.Add(-time.Minute))
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		claimed, ok, err = tx.ClaimNextGovernanceJob(ctx, base)
		return err
	}); err != nil || !ok || claimed.TargetKey != "old" {
		t.Fatalf("stale governance not claimed: job=%+v ok=%v err=%v", claimed, ok, err)
	}
}
