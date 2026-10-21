package embedding

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

type workerProvider struct {
	vectors [][]float32
	err     error
	calls   int
	block   bool
}

func (p *workerProvider) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	p.calls++
	if p.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.vectors, nil
}

type failureRecord struct {
	code string
	next *time.Time
}

type fakeBackend struct {
	job          Job
	hasJob       bool
	doc          Document
	loadErr      error
	commitOK     bool
	commitVector []byte
	commitDims   int
	commits      int
	failures     []failureRecord
	enqueued     int64
	recovered    int64
}

func (b *fakeBackend) ClaimJob(context.Context, string, time.Time) (Job, bool, error) {
	if !b.hasJob {
		return Job{}, false, nil
	}
	b.hasJob = false
	job := b.job
	job.Attempts++
	b.job = job
	return job, true, nil
}

func (b *fakeBackend) LoadDocument(context.Context, string) (Document, error) {
	return b.doc, b.loadErr
}

func (b *fakeBackend) CommitJob(_ context.Context, _ Job, vector []byte, dimensions int, _ time.Time) (bool, error) {
	if !b.commitOK {
		return false, nil
	}
	b.commits++
	b.commitVector = append([]byte(nil), vector...)
	b.commitDims = dimensions
	return true, nil
}

func (b *fakeBackend) FailJob(_ context.Context, _ Job, code string, next *time.Time, _ time.Time) error {
	b.failures = append(b.failures, failureRecord{code: code, next: next})
	return nil
}

func (b *fakeBackend) RecoverJobs(context.Context, string, time.Time) (int64, error) {
	return b.recovered, nil
}

func (b *fakeBackend) EnqueueMissingJobs(context.Context, string, time.Time) (int64, error) {
	return b.enqueued, nil
}

func newTestEncoder(t *testing.T, provider Provider) *Encoder {
	t.Helper()
	encoder, err := NewEncoder(provider, Config{
		Enabled: true, BaseURL: "http://localhost:1", Model: "test", Dimensions: 2,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoder
}

func newWorkerBackend(t *testing.T, encoder *Encoder) (*fakeBackend, memory.Memory) {
	t.Helper()
	item := memory.Memory{ID: "memory-1", Kind: memory.KindFact, Title: "标题", Content: "正文"}
	return &fakeBackend{
		hasJob: true,
		job: Job{
			MemoryID:    item.ID,
			ProfileID:   encoder.ProfileID(),
			ContentHash: DocumentHash(item),
		},
		doc:      Document{Memory: item, ContentHash: DocumentHash(item)},
		commitOK: true,
	}, item
}

func TestWorkerCommitsValidatedVector(t *testing.T) {
	provider := &workerProvider{vectors: [][]float32{{3, 4}}}
	encoder := newTestEncoder(t, provider)
	backend, _ := newWorkerBackend(t, encoder)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	worker := NewWorker(backend, encoder, WithEmbeddingClock(func() time.Time { return now }), WithEmbeddingJitter(func(delay time.Duration) time.Duration { return delay }))
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed = %v, err = %v", processed, err)
	}
	if backend.commits != 1 || backend.commitDims != 2 || len(backend.commitVector) != 8 {
		t.Fatalf("commit = %d/%d/%d", backend.commits, backend.commitDims, len(backend.commitVector))
	}
	if len(backend.failures) != 0 {
		t.Fatalf("failures = %+v", backend.failures)
	}
}

func TestWorkerRetryClassificationAndBackoff(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		provider    *workerProvider
		jobAttempts int
		wantCode    string
		wantNext    time.Duration
		wantNil     bool
	}{
		{
			name:     "retryable below budget",
			provider: &workerProvider{err: &ProviderError{Code: "network", Retryable: true}},
			wantCode: "network", wantNext: time.Second,
		},
		{
			name:     "retryable honors capped retry-after",
			provider: &workerProvider{err: &ProviderError{Code: "http_429", Retryable: true, RetryAfter: 2 * time.Minute}},
			wantCode: "http_429", wantNext: time.Minute,
		},
		{
			name:     "permanent failure",
			provider: &workerProvider{err: &ProviderError{Code: "http_401", Retryable: false}},
			wantCode: "http_401", wantNil: true,
		},
		{
			name:        "budget exhausted",
			provider:    &workerProvider{err: &ProviderError{Code: "timeout", Retryable: true}},
			jobAttempts: 2,
			wantCode:    "timeout", wantNil: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			encoder := newTestEncoder(t, testCase.provider)
			backend, _ := newWorkerBackend(t, encoder)
			backend.job.Attempts = testCase.jobAttempts
			worker := NewWorker(backend, encoder,
				WithEmbeddingClock(func() time.Time { return now }),
				WithEmbeddingJitter(func(delay time.Duration) time.Duration { return delay }),
			)
			processed, err := worker.ProcessOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("processed = %v, err = %v", processed, err)
			}
			if len(backend.failures) != 1 {
				t.Fatalf("failures = %+v", backend.failures)
			}
			failure := backend.failures[0]
			if failure.code != testCase.wantCode {
				t.Fatalf("code = %q", failure.code)
			}
			if testCase.wantNil {
				if failure.next != nil {
					t.Fatalf("terminal failure next = %v", failure.next)
				}
				return
			}
			if failure.next == nil || !failure.next.Equal(now.Add(testCase.wantNext)) {
				t.Fatalf("next = %v, want %v", failure.next, now.Add(testCase.wantNext))
			}
		})
	}
}

func TestWorkerDiscardsChangedContentAndRequeuesCancellation(t *testing.T) {
	provider := &workerProvider{vectors: [][]float32{{1, 0}}}
	encoder := newTestEncoder(t, provider)
	backend, item := newWorkerBackend(t, encoder)
	backend.doc.ContentHash = "new-generation"
	worker := NewWorker(backend, encoder)
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("changed content = %v, %v", processed, err)
	}
	if provider.calls != 0 || backend.commits != 0 || len(backend.failures) != 0 {
		t.Fatalf("changed content called model=%d commit=%d failures=%+v", provider.calls, backend.commits, backend.failures)
	}

	provider = &workerProvider{block: true}
	encoder = newTestEncoder(t, provider)
	backend, _ = newWorkerBackend(t, encoder)
	worker = NewWorker(backend, encoder)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	processed, err = worker.ProcessOne(ctx)
	if err != nil || !processed {
		t.Fatalf("cancelled = %v, %v", processed, err)
	}
	if len(backend.failures) != 1 || backend.failures[0].code != "cancelled" || backend.failures[0].next == nil {
		t.Fatalf("cancel failure = %+v", backend.failures)
	}
	_ = item
}

func TestWorkerRejectsClaimBeyondBudget(t *testing.T) {
	provider := &workerProvider{vectors: [][]float32{{1, 0}}}
	encoder := newTestEncoder(t, provider)
	backend, _ := newWorkerBackend(t, encoder)
	backend.job.Attempts = 5
	worker := NewWorker(backend, encoder)
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed = %v, err = %v", processed, err)
	}
	if provider.calls != 0 {
		t.Fatal("over-budget job reached the model")
	}
	if len(backend.failures) != 1 || backend.failures[0].code != "retry_exhausted" || backend.failures[0].next != nil {
		t.Fatalf("failure = %+v", backend.failures)
	}
}

func TestWorkerRecoverRunsEnqueueMissing(t *testing.T) {
	provider := &workerProvider{vectors: [][]float32{{1, 0}}}
	encoder := newTestEncoder(t, provider)
	backend, _ := newWorkerBackend(t, encoder)
	backend.recovered = 2
	backend.enqueued = 3
	worker := NewWorker(backend, encoder)
	if err := worker.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backend.recovered != 2 || backend.enqueued != 3 {
		t.Fatalf("recover = %+v", backend)
	}
}
