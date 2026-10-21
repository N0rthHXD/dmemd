package embedding

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"time"
)

const defaultWorkerPollInterval = time.Second

// Worker is the single background embedding executor. It claims durable tasks
// from SQLite, calls the model outside any transaction and commits conditionally.
type Worker struct {
	backend     Backend
	encoder     *Encoder
	maxAttempts int
	retryBase   time.Duration
	retryMax    time.Duration
	poll        time.Duration
	now         func() time.Time
	jitter      func(time.Duration) time.Duration
	logger      *slog.Logger
}

// WorkerOption customizes clocks and logging for tests.
type WorkerOption func(*Worker)

// WithEmbeddingLogger sets the structured task logger. It never receives query,
// content, vector or raw provider data.
func WithEmbeddingLogger(logger *slog.Logger) WorkerOption {
	return func(worker *Worker) {
		if logger != nil {
			worker.logger = logger
		}
	}
}

// WithEmbeddingClock overrides the worker clock.
func WithEmbeddingClock(now func() time.Time) WorkerOption {
	return func(worker *Worker) {
		if now != nil {
			worker.now = now
		}
	}
}

// WithEmbeddingJitter overrides the retry jitter. Tests use the identity
// function for deterministic schedules.
func WithEmbeddingJitter(jitter func(time.Duration) time.Duration) WorkerOption {
	return func(worker *Worker) {
		if jitter != nil {
			worker.jitter = jitter
		}
	}
}

// NewWorker assembles the embedding worker from one immutable encoder profile.
func NewWorker(backend Backend, encoder *Encoder, options ...WorkerOption) *Worker {
	worker := &Worker{
		backend:     backend,
		encoder:     encoder,
		maxAttempts: DefaultMaxAttempts,
		retryBase:   DefaultRetryBase,
		retryMax:    DefaultRetryMax,
		poll:        defaultWorkerPollInterval,
		now:         time.Now,
		jitter:      defaultJitter,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(worker)
	}
	return worker
}

// Recover requeues running tasks left by a previous process, preserving their
// attempt counts, and refreshes missing-vector tasks.
func (w *Worker) Recover(ctx context.Context) error {
	if w.backend == nil || w.encoder == nil {
		return errors.New("embedding: worker dependencies not configured")
	}
	now := w.now().UTC()
	requeued, err := w.backend.RecoverJobs(ctx, w.encoder.ProfileID(), now)
	if err != nil {
		return err
	}
	created, err := w.backend.EnqueueMissingJobs(ctx, w.encoder.ProfileID(), now)
	if err != nil {
		return err
	}
	if requeued > 0 || created > 0 {
		w.logger.Info("embedding recovery completed",
			"requeued", requeued,
			"enqueued", created,
		)
	}
	return nil
}

// Run processes durable tasks until ctx is cancelled. Cancellation during a
// model call requeues the in-flight generation so the next process can resume.
func (w *Worker) Run(ctx context.Context) error {
	if w.backend == nil || w.encoder == nil {
		return errors.New("embedding: worker dependencies not configured")
	}
	if err := w.Recover(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	for {
		processed, err := w.ProcessOne(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.logger.Error("embedding worker loop error", "error", err)
			if !w.wait(ctx) {
				return nil
			}
			continue
		}
		if processed {
			continue
		}
		if !w.wait(ctx) {
			return nil
		}
	}
}

// ProcessOne claims and processes at most one due task.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if w.backend == nil || w.encoder == nil {
		return false, errors.New("embedding: worker dependencies not configured")
	}
	now := w.now().UTC()
	job, found, err := w.backend.ClaimJob(ctx, w.encoder.ProfileID(), now)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if job.Attempts > w.maxAttempts {
		// Defensive path for a generation left queued after a shutdown on the
		// final attempt. It must never reach the model beyond the budget.
		return true, w.fail(ctx, job, "retry_exhausted", nil)
	}

	document, err := w.backend.LoadDocument(ctx, job.MemoryID)
	if err != nil {
		if errors.Is(err, ErrInputMissing) {
			return true, w.fail(ctx, job, "input_missing", nil)
		}
		if ctx.Err() != nil {
			w.requeueAfterStop(job)
			return true, nil
		}
		w.logger.Warn("embedding task input read failed",
			"memory_id", job.MemoryID,
			"attempts", job.Attempts,
			"error", "load_failed",
		)
		return true, w.retryOrFail(ctx, job, "load_failed", true, 0)
	}
	if document.ContentHash == "" || document.ContentHash != job.ContentHash {
		// A newer write generation replaced this task. The old claim must not
		// touch it.
		w.logger.Info("embedding task discarded",
			"memory_id", job.MemoryID,
			"reason", "content_changed",
		)
		return true, nil
	}

	started := time.Now()
	embedded, err := w.encoder.EmbedDocument(ctx, document.Memory)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			w.requeueAfterStop(job)
			return true, nil
		}
		code := ErrorCode(err)
		w.logger.Warn("embedding job failed",
			"memory_id", job.MemoryID,
			"attempts", job.Attempts,
			"duration_ms", duration,
			"error", code,
		)
		return true, w.retryOrFail(ctx, job, code, IsRetryable(err), RetryAfter(err))
	}
	if embedded.ContentHash != job.ContentHash {
		w.logger.Info("embedding task discarded",
			"memory_id", job.MemoryID,
			"reason", "content_changed",
		)
		return true, nil
	}

	committed, err := w.backend.CommitJob(ctx, job, embedded.Vector, embedded.Dimensions, w.now().UTC())
	if err != nil {
		if ctx.Err() != nil {
			w.requeueAfterStop(job)
			return true, nil
		}
		w.logger.Warn("embedding task commit failed",
			"memory_id", job.MemoryID,
			"attempts", job.Attempts,
			"error", "commit_failed",
		)
		return true, w.retryOrFail(ctx, job, "commit_failed", true, 0)
	}
	if !committed {
		w.logger.Info("embedding task discarded",
			"memory_id", job.MemoryID,
			"reason", "generation_changed",
		)
		return true, nil
	}
	w.logger.Info("embedding job completed",
		"memory_id", job.MemoryID,
		"attempts", job.Attempts,
		"duration_ms", duration,
	)
	return true, nil
}

func (w *Worker) retryOrFail(ctx context.Context, job Job, code string, retryable bool, retryAfter time.Duration) error {
	var next *time.Time
	if retryable && job.Attempts < w.maxAttempts {
		at := w.now().UTC().Add(w.retryDelay(job.Attempts, retryAfter))
		next = &at
	}
	return w.fail(ctx, job, code, next)
}

func (w *Worker) fail(ctx context.Context, job Job, code string, nextAttempt *time.Time) error {
	return w.backend.FailJob(ctx, job, code, nextAttempt, w.now().UTC())
}

func (w *Worker) requeueAfterStop(job Job) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := w.now().UTC()
	if err := w.backend.FailJob(ctx, job, "cancelled", &now, now); err != nil {
		w.logger.Warn("embedding shutdown requeue failed",
			"memory_id", job.MemoryID,
			"error", err,
		)
	}
}

func (w *Worker) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > w.retryMax {
			retryAfter = w.retryMax
		}
		return retryAfter
	}
	delay := w.retryBase
	for try := 1; try < attempt; try++ {
		if delay >= w.retryMax {
			delay = w.retryMax
			break
		}
		delay *= 2
	}
	if delay > w.retryMax {
		delay = w.retryMax
	}
	if w.jitter != nil {
		delay = w.jitter(delay)
	}
	if delay < 0 {
		return 0
	}
	if delay > w.retryMax {
		return w.retryMax
	}
	return delay
}

func (w *Worker) wait(ctx context.Context) bool {
	interval := w.poll
	if interval <= 0 {
		interval = defaultWorkerPollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func defaultJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	// Up to ±20% keeps the exponential ceiling and spreads synchronized
	// retries without requiring deterministic behavior in production.
	span := int64(delay) / 5
	if span <= 0 {
		return delay
	}
	offset := rand.Int63n(2*span+1) - span
	return delay + time.Duration(offset)
}
