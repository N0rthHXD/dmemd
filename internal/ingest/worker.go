package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

const (
	defaultWorkerMaxAttempts  = 3
	defaultWorkerPollInterval = 100 * time.Millisecond
	defaultMaxRetryDelay      = 30 * time.Minute
)

var defaultWorkerRetryDelays = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

// JobProcessor executes one claimed job. Processor implements this interface.
type JobProcessor interface {
	Process(ctx context.Context, job Job) (ProcessResult, error)
}

// Worker is the single sequential persistent-task executor. It claims jobs from
// SQLite in durable order and never keeps an in-memory queue as the source of
// truth.
type Worker struct {
	store       Store
	jobs        JobReader
	processor   JobProcessor
	maxAttempts int
	retryDelays []time.Duration
	poll        time.Duration
	now         func() time.Time
	logger      *slog.Logger
}

// WorkerOption customizes retry timing and logging for tests.
type WorkerOption func(*Worker)

// WithWorkerClock overrides the worker clock.
func WithWorkerClock(now func() time.Time) WorkerOption {
	return func(worker *Worker) {
		if now != nil {
			worker.now = now
		}
	}
}

// WithWorkerLogger sets the structured task logger. It never receives source or
// memory text.
func WithWorkerLogger(logger *slog.Logger) WorkerOption {
	return func(worker *Worker) {
		if logger != nil {
			worker.logger = logger
		}
	}
}

// WithMaxAttempts overrides the consecutive protocol-failure limit. Transient
// network failures are never capped by this value; they wait and resume.
func WithMaxAttempts(maxAttempts int) WorkerOption {
	return func(worker *Worker) {
		if maxAttempts > 0 {
			worker.maxAttempts = maxAttempts
		}
	}
}

// WithRetryDelays overrides the backoff between attempts.
func WithRetryDelays(delays ...time.Duration) WorkerOption {
	return func(worker *Worker) {
		worker.retryDelays = append([]time.Duration(nil), delays...)
	}
}

// WithPollInterval sets how long an idle worker waits before checking for a due
// queued job again.
func WithPollInterval(interval time.Duration) WorkerOption {
	return func(worker *Worker) {
		if interval > 0 {
			worker.poll = interval
		}
	}
}

// NewWorker assembles one sequential worker. There is intentionally no worker
// count parameter: the first release has exactly one executor per process.
func NewWorker(store Store, jobs JobReader, processor JobProcessor, options ...WorkerOption) *Worker {
	worker := &Worker{
		store:       store,
		jobs:        jobs,
		processor:   processor,
		maxAttempts: defaultWorkerMaxAttempts,
		retryDelays: append([]time.Duration(nil), defaultWorkerRetryDelays...),
		poll:        defaultWorkerPollInterval,
		now:         time.Now,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(worker)
	}
	return worker
}

// Recover requeues leftover running jobs, re-evaluates configuration-blocked
// jobs, and gives protocol-isolated jobs one fresh evaluation after a restart.
// It is safe to call more than once and does not touch succeeded or cancelled
// jobs.
func (w *Worker) Recover(ctx context.Context) error {
	if w.store == nil {
		return errors.New("ingest: worker store not configured")
	}
	now := w.now().UTC()
	var requeued, configuration, protocolFailed int64
	err := w.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		requeued, err = tx.RequeueRunningJobs(ctx, now)
		if err != nil {
			return err
		}
		configuration, err = tx.RequeueWaitingConfigurationJobs(ctx, now)
		if err != nil {
			return err
		}
		protocolFailed, err = tx.RequeueProtocolFailedJobs(ctx, now)
		return err
	})
	if err != nil {
		return fmt.Errorf("恢复遗留任务: %w", err)
	}
	if requeued > 0 || configuration > 0 || protocolFailed > 0 {
		w.logger.Info("ingest recovery completed",
			"requeued", requeued,
			"configuration_retry", configuration,
			"protocol_retry", protocolFailed,
		)
	}
	return nil
}

// Run recovers startup state and processes due jobs sequentially until ctx is
// cancelled. Cancellation during an idle wait exits immediately; cancellation
// during a model call requeues the in-flight job without resetting attempts.
func (w *Worker) Run(ctx context.Context) error {
	if w.processor == nil {
		return errors.New("ingest: worker processor not configured")
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
			w.logger.Error("ingest worker loop error", "error", safeErrorSummary(err))
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

// ProcessOne claims at most one due job and processes it. It returns false when
// no queued job is currently due.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if w.store == nil || w.jobs == nil || w.processor == nil {
		return false, errors.New("ingest: worker dependencies not configured")
	}
	now := w.now().UTC()
	var claimed Job
	var found bool
	if err := w.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		claimed, found, err = tx.ClaimNextJob(ctx, now)
		return err
	}); err != nil {
		return false, fmt.Errorf("领取任务: %w", err)
	}
	if !found {
		return false, nil
	}
	if claimed.ProtocolFailures >= w.maxAttempts {
		// Defensive path for a job that somehow stayed queued after reaching the
		// protocol-failure limit. It must not reach the model again.
		return true, w.failProtocol(claimed)
	}

	started := time.Now()
	result, err := w.processor.Process(ctx, claimed)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			w.requeueAfterStop(claimed)
			return true, nil
		}
		if handleErr := w.handleFailure(ctx, claimed, err); handleErr != nil {
			return true, handleErr
		}
		w.logger.Warn("ingest job failed",
			"job_id", claimed.ID,
			"attempts", claimed.Attempts,
			"duration_ms", duration,
			"error", safeErrorSummary(err),
		)
		return true, nil
	}
	w.logger.Info("ingest job completed",
		"job_id", claimed.ID,
		"status", result.Status,
		"memories", len(result.MemoryIDs),
		"duration_ms", duration,
	)
	return true, nil
}

func (w *Worker) handleFailure(ctx context.Context, claimed Job, cause error) error {
	now := w.now().UTC()
	summary := safeErrorSummary(cause)
	kind := failureKind(cause)
	return w.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		current, err := tx.GetJob(ctx, claimed.ID)
		if err != nil {
			return err
		}
		switch current.Status {
		case JobCancelled, JobSucceeded, JobFailed:
			return nil
		}
		current.UpdatedAt = now
		current.Error = summary
		current.NextAttemptAt = nil
		current.WaitReason = ""
		current.FinishedAt = nil
		switch kind {
		case "configuration":
			// Credentials and request configuration are not probed at high
			// frequency. The job is parked and reevaluated after a restart.
			current.Status = JobWaiting
			current.WaitReason = WaitReasonConfiguration
		case "protocol":
			current.ProtocolFailures++
			if current.ProtocolFailures >= w.maxAttempts {
				current.Status = JobFailed
				current.FinishedAt = &now
				break
			}
			next := now.Add(w.failureDelay(current.ProtocolFailures))
			current.Status = JobWaiting
			current.WaitReason = WaitReasonProtocol
			current.NextAttemptAt = &next
		default:
			next := now.Add(w.failureDelay(current.Attempts))
			current.Status = JobWaiting
			current.WaitReason = WaitReasonTransient
			current.NextAttemptAt = &next
		}
		return tx.UpdateJob(ctx, current)
	})
}

func (w *Worker) failProtocol(claimed Job) error {
	now := w.now().UTC()
	return w.store.WithTx(context.Background(), func(ctx context.Context, tx Tx) error {
		current, err := tx.GetJob(ctx, claimed.ID)
		if err != nil {
			return err
		}
		switch current.Status {
		case JobCancelled, JobSucceeded, JobFailed:
			return nil
		}
		current.Status = JobFailed
		current.Error = safeErrorSummary(&ProviderError{Operation: "protocol"})
		current.NextAttemptAt = nil
		current.WaitReason = ""
		current.UpdatedAt = now
		current.FinishedAt = &now
		return tx.UpdateJob(ctx, current)
	})
}

func (w *Worker) requeueAfterStop(claimed Job) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := w.now().UTC()
	err := w.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		current, err := tx.GetJob(ctx, claimed.ID)
		if err != nil {
			return err
		}
		switch current.Status {
		case JobQueued, JobCancelled, JobSucceeded, JobFailed:
			return nil
		}
		current.Status = JobQueued
		current.NextAttemptAt = &now
		current.WaitReason = ""
		current.UpdatedAt = now
		current.FinishedAt = nil
		return tx.UpdateJob(ctx, current)
	})
	if err != nil {
		w.logger.Warn("ingest shutdown requeue failed", "job_id", claimed.ID, "error", safeErrorSummary(err))
	}
}

func (w *Worker) failureDelay(failures int) time.Duration {
	if len(w.retryDelays) == 0 {
		return defaultMaxRetryDelay
	}
	index := failures - 1
	if index < 0 {
		index = 0
	}
	if index >= len(w.retryDelays) {
		return defaultMaxRetryDelay
	}
	delay := w.retryDelays[index]
	if delay < 0 {
		return 0
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

func failureKind(err error) string {
	var classified interface{ FailureKind() string }
	if errors.As(err, &classified) {
		return classified.FailureKind()
	}
	if errors.Is(err, ErrProtocol) {
		return "protocol"
	}
	return "transient"
}

func safeErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	var provider *ProviderError
	switch {
	case errors.As(err, &provider):
		message := provider.Operation
		if message == "" {
			message = "model provider"
		}
		if provider.Err != nil {
			message += ": " + provider.Err.Error()
		}
		return truncateSummary(message)
	case errors.Is(err, context.DeadlineExceeded):
		return "model request timed out"
	case errors.Is(err, context.Canceled):
		return "processing cancelled"
	default:
		return truncateSummary(err.Error())
	}
}

func truncateSummary(value string) string {
	value = strings.Map(func(char rune) rune {
		switch char {
		case '\r', '\n', '\t':
			return ' '
		default:
			return char
		}
	}, value)
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 240 {
		runes = runes[:240]
	}
	return string(runes)
}
