package source_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

var serviceTime = time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)

type fixedClock struct {
	at time.Time
}

func (c *fixedClock) Now() time.Time { return c.at }

func newSourceFixture(t *testing.T) (*sqlite.Store, *source.Service, *fixedClock) {
	t.Helper()
	store := testutil.NewStore(t)
	clock := &fixedClock{at: serviceTime}
	service := source.NewService(store, sqlite.SourceStore{Store: store}, source.WithClock(clock.Now))
	return store, service, clock
}

func TestRedactClearsSourceEvidenceAndCancelsOnlyPendingJobs(t *testing.T) {
	store, service, clock := newSourceFixture(t)
	ctx := context.Background()
	src := source.Source{
		ID: "source-redact-jobs", AppID: "app",
		Content:   []byte(`{"messages":[{"id":"m1","content":"sensitive quote"}]}`),
		CreatedAt: serviceTime,
	}
	evidence := source.NewEvidence(src, source.EvidenceReference{Quote: "sensitive quote"}, "model", serviceTime)
	jobs := []ingest.Job{
		{ID: "job-queued", SourceID: src.ID, Status: ingest.JobQueued, CreatedAt: serviceTime, UpdatedAt: serviceTime},
		{ID: "job-running", SourceID: src.ID, Status: ingest.JobRunning, Attempts: 1, CreatedAt: serviceTime, UpdatedAt: serviceTime},
		{ID: "job-waiting", SourceID: src.ID, Status: ingest.JobWaiting, Attempts: 2,
			WaitReason: ingest.WaitReasonTransient, CreatedAt: serviceTime, UpdatedAt: serviceTime},
		{ID: "job-succeeded", SourceID: src.ID, Status: ingest.JobSucceeded, Attempts: 1, MemoryIDs: []string{"m-1"}, CreatedAt: serviceTime, UpdatedAt: serviceTime},
		{ID: "job-failed", SourceID: src.ID, Status: ingest.JobFailed, Attempts: 3, Error: "model echoed sensitive quote", CreatedAt: serviceTime, UpdatedAt: serviceTime},
		{ID: "job-other-source", SourceID: "other-source", Status: ingest.JobQueued, CreatedAt: serviceTime, UpdatedAt: serviceTime},
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, source.Source{
			ID: "other-source", Content: []byte(`"other"`), CreatedAt: serviceTime,
		}); err != nil {
			return err
		}
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		if err := tx.InsertEvidence(ctx, evidence); err != nil {
			return err
		}
		for _, job := range jobs {
			if err := tx.InsertJob(ctx, job); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	result, err := service.Redact(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceID != src.ID || !result.Redacted || result.CancelledJobs != 3 {
		t.Fatalf("result = %+v", result)
	}

	loaded, err := service.Get(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Redacted || len(loaded.Content) != 0 || loaded.RedactedAt == nil || !loaded.RedactedAt.Equal(serviceTime) {
		t.Fatalf("source = %+v", loaded)
	}
	loadedEvidence := loadEvidence(t, store, evidence.ID)
	if !loadedEvidence.Redacted || loadedEvidence.Quote != "" || loadedEvidence.Location != nil {
		t.Fatalf("evidence = %+v", loadedEvidence)
	}

	queued := loadJob(t, store, "job-queued")
	running := loadJob(t, store, "job-running")
	waiting := loadJob(t, store, "job-waiting")
	if queued.Status != ingest.JobCancelled || running.Status != ingest.JobCancelled ||
		waiting.Status != ingest.JobCancelled {
		t.Fatalf("pending jobs = %+v %+v %+v", queued, running, waiting)
	}
	if queued.Error != source.RedactedJobReason || running.Error != source.RedactedJobReason ||
		waiting.Error != source.RedactedJobReason {
		t.Fatalf("cancellation reason = %q %q %q", queued.Error, running.Error, waiting.Error)
	}
	if queued.FinishedAt == nil || running.FinishedAt == nil || waiting.FinishedAt == nil {
		t.Fatal("cancelled jobs did not record finished_at")
	}
	succeeded := loadJob(t, store, "job-succeeded")
	if succeeded.Status != ingest.JobSucceeded || succeeded.Error != "" || len(succeeded.MemoryIDs) != 1 {
		t.Fatalf("succeeded job changed: %+v", succeeded)
	}
	failed := loadJob(t, store, "job-failed")
	if failed.Status != ingest.JobFailed || failed.Error != source.RedactedJobReason || failed.Attempts != 3 {
		t.Fatalf("failed job error was not scrubbed: %+v", failed)
	}
	other := loadJob(t, store, "job-other-source")
	if other.Status != ingest.JobQueued {
		t.Fatalf("unrelated job = %+v", other)
	}
	cancelledUpdatedAt := queued.UpdatedAt
	failedUpdatedAt := failed.UpdatedAt

	clock.at = serviceTime.Add(time.Hour)
	second, err := service.Redact(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Redacted || second.CancelledJobs != 0 {
		t.Fatalf("repeat result = %+v", second)
	}
	reloaded, err := service.Get(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RedactedAt == nil || !reloaded.RedactedAt.Equal(*loaded.RedactedAt) {
		t.Fatalf("redacted_at changed on repeat: %v", reloaded.RedactedAt)
	}
	if repeat := loadJob(t, store, "job-queued"); !repeat.UpdatedAt.Equal(cancelledUpdatedAt) {
		t.Fatalf("repeat redaction touched cancelled job: %v -> %v", cancelledUpdatedAt, repeat.UpdatedAt)
	}
	if repeat := loadJob(t, store, "job-failed"); !repeat.UpdatedAt.Equal(failedUpdatedAt) {
		t.Fatalf("repeat redaction touched failed job: %v -> %v", failedUpdatedAt, repeat.UpdatedAt)
	}
}

func TestRedactRollsBackWhenAnyStepFails(t *testing.T) {
	store, _, _ := newSourceFixture(t)
	ctx := context.Background()
	src := source.Source{
		ID: "source-rollback", Content: []byte(`{"text":"keep me"}`), CreatedAt: serviceTime,
	}
	evidence := source.NewEvidence(src, source.EvidenceReference{Quote: "keep me"}, "model", serviceTime)
	job := ingest.Job{
		ID: "job-rollback", SourceID: src.ID, Status: ingest.JobQueued,
		CreatedAt: serviceTime, UpdatedAt: serviceTime,
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		if err := tx.InsertEvidence(ctx, evidence); err != nil {
			return err
		}
		return tx.InsertJob(ctx, job)
	}); err != nil {
		t.Fatal(err)
	}

	failure := errors.New("injected redaction failure")
	service := source.NewService(store, failingRedactStore{
		inner: sqlite.SourceStore{Store: store},
		err:   failure,
	})
	if _, err := service.Redact(ctx, src.ID); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}

	loaded, err := store.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Redacted || string(loaded.Content) != `{"text":"keep me"}` {
		t.Fatalf("source after rollback = %+v", loaded)
	}
	loadedEvidence := loadEvidence(t, store, evidence.ID)
	if loadedEvidence.Redacted || loadedEvidence.Quote != "keep me" {
		t.Fatalf("evidence after rollback = %+v", loadedEvidence)
	}
	loadedJob := loadJob(t, store, job.ID)
	if loadedJob.Status != ingest.JobQueued || loadedJob.Error != "" {
		t.Fatalf("job after rollback = %+v", loadedJob)
	}
}

func TestRedactMissingSourceReturnsNotFound(t *testing.T) {
	_, service, _ := newSourceFixture(t)
	if _, err := service.Redact(context.Background(), "missing"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
	if _, err := service.Get(context.Background(), "missing"); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("get error = %v", err)
	}
}

type failingRedactStore struct {
	inner source.Store
	err   error
}

func (s failingRedactStore) WithTx(ctx context.Context, fn func(context.Context, source.Tx) error) error {
	return s.inner.WithTx(ctx, func(ctx context.Context, tx source.Tx) error {
		return fn(ctx, failingRedactTx{Tx: tx, err: s.err})
	})
}

type failingRedactTx struct {
	source.Tx
	err error
}

func (t failingRedactTx) RedactSource(ctx context.Context, id string, at time.Time) error {
	if err := t.Tx.RedactSource(ctx, id, at); err != nil {
		return err
	}
	return t.err
}

func loadEvidence(t *testing.T, store *sqlite.Store, id string) source.Evidence {
	t.Helper()
	var result source.Evidence
	err := store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		result, err = tx.GetEvidence(ctx, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func loadJob(t *testing.T, store *sqlite.Store, id string) ingest.Job {
	t.Helper()
	job, err := store.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestNewEvidenceUsesGeneratedIDAndSourceStatus(t *testing.T) {
	src := source.Source{
		ID: "source-evidence", Content: []byte(`{"messages":[{"id":"m1","content":"你好世界"}]}`),
	}
	item := source.NewEvidence(src, source.EvidenceReference{Quote: "世界"}, "model", serviceTime)
	if item.ID == "" {
		t.Fatal("evidence ID is empty")
	}
	if item.SourceID != src.ID || item.MessageID != "m1" || item.ModelName != "model" ||
		item.CreatedAt != serviceTime || item.Redacted {
		t.Fatalf("evidence = %+v", item)
	}
	if item.Location == nil || item.Location.Field != "messages.0.content" ||
		item.Location.Start != 2 || item.Location.End != 4 {
		t.Fatalf("location = %+v", item.Location)
	}

	redactedAt := serviceTime.Add(time.Hour)
	redacted := source.NewEvidence(source.Source{
		ID: "source-redacted", Redacted: true, RedactedAt: &redactedAt,
	}, source.EvidenceReference{Quote: "世界"}, "model", serviceTime)
	if !redacted.Redacted || redacted.Quote != "" || redacted.Location != nil ||
		redacted.RedactedAt == nil || !redacted.RedactedAt.Equal(redactedAt) {
		t.Fatalf("redacted evidence = %+v", redacted)
	}
}
