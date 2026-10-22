package governance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

const (
	defaultPollInterval       = 250 * time.Millisecond
	defaultMaxProtocolFailure = 3
	defaultReconcileInterval  = 15 * time.Minute
	defaultMaxRetryDelay      = 30 * time.Minute
)

var defaultRetryDelays = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

// Worker is the single sequential governance executor.
type Worker struct {
	store             Store
	handlers          map[task.Kind]Handler
	poll              time.Duration
	retryDelays       []time.Duration
	maxProtocol       int
	reconcileInterval time.Duration
	now               func() time.Time
	logger            *slog.Logger
}

// WorkerOption customizes scheduling and logging for tests.
type WorkerOption func(*Worker)

// WithWorkerClock overrides the worker clock.
func WithWorkerClock(now func() time.Time) WorkerOption {
	return func(worker *Worker) {
		if now != nil {
			worker.now = now
		}
	}
}

// WithWorkerLogger installs the structured task logger. It never receives
// memory text, model requests or model responses.
func WithWorkerLogger(logger *slog.Logger) WorkerOption {
	return func(worker *Worker) {
		if logger != nil {
			worker.logger = logger
		}
	}
}

// WithPollInterval sets the idle claim interval.
func WithPollInterval(interval time.Duration) WorkerOption {
	return func(worker *Worker) {
		if interval > 0 {
			worker.poll = interval
		}
	}
}

// WithRetryDelays overrides the transient backoff sequence. The cap after the
// final entry is 30 minutes.
func WithRetryDelays(delays ...time.Duration) WorkerOption {
	return func(worker *Worker) {
		worker.retryDelays = append([]time.Duration(nil), delays...)
	}
}

// WithMaxProtocolFailures overrides the consecutive protocol-failure limit.
func WithMaxProtocolFailures(limit int) WorkerOption {
	return func(worker *Worker) {
		if limit > 0 {
			worker.maxProtocol = limit
		}
	}
}

// WithReconcileInterval sets the periodic reconcile cadence. A non-positive
// value disables scheduling; the handler remains available for manual tests.
func WithReconcileInterval(interval time.Duration) WorkerOption {
	return func(worker *Worker) {
		worker.reconcileInterval = interval
	}
}

// NewWorker assembles one sequential executor over the fixed handler map.
func NewWorker(store Store, handlers map[task.Kind]Handler, options ...WorkerOption) *Worker {
	worker := &Worker{
		store:             store,
		handlers:          handlers,
		poll:              defaultPollInterval,
		retryDelays:       append([]time.Duration(nil), defaultRetryDelays...),
		maxProtocol:       defaultMaxProtocolFailure,
		reconcileInterval: defaultReconcileInterval,
		now:               time.Now,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(worker)
	}
	return worker
}

// Recover requeues interrupted running tasks, re-evaluates configuration-error
// waits, and gives protocol-isolated tasks one fresh evaluation after a restart.
// Context cancellation never loses a running target.
func (w *Worker) Recover(ctx context.Context) error {
	if w.store == nil {
		return errors.New("governance: worker store not configured")
	}
	now := w.now().UTC()
	var running, configuration, protocolFailed int64
	err := w.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		running, err = tx.RequeueRunningGovernanceJobs(ctx, now)
		if err != nil {
			return err
		}
		configuration, err = tx.RequeueWaitingConfigurationGovernanceJobs(ctx, now)
		if err != nil {
			return err
		}
		protocolFailed, err = tx.RequeueProtocolFailedGovernanceJobs(ctx, now)
		return err
	})
	if err != nil {
		return fmt.Errorf("恢复治理任务: %w", err)
	}
	if running > 0 || configuration > 0 || protocolFailed > 0 {
		w.logger.Info("governance recovery completed",
			"requeued", running,
			"configuration_retry", configuration,
			"protocol_retry", protocolFailed,
		)
	}
	return nil
}

// Run recovers startup state, schedules reconcile work and processes due tasks
// one at a time until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if w.store == nil || len(w.handlers) == 0 {
		return errors.New("governance: worker dependencies not configured")
	}
	if err := w.Recover(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	nextReconcile := w.now().UTC()
	for {
		now := w.now().UTC()
		if w.reconcileInterval > 0 && !now.Before(nextReconcile) {
			if _, ok := w.handlers[task.KindReconcile]; ok {
				if err := w.ensureReconcile(ctx, now); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					w.logger.Error("governance reconcile scheduling failed", "error", safeError(err))
				}
			}
			nextReconcile = now.Add(w.reconcileInterval)
		}
		processed, err := w.ProcessOne(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.logger.Error("governance worker loop error", "error", safeError(err))
			if !w.wait(ctx, w.poll) {
				return nil
			}
			continue
		}
		if processed {
			continue
		}
		wait := w.poll
		if w.reconcileInterval > 0 {
			remaining := nextReconcile.Sub(w.now().UTC())
			if remaining > 0 && remaining < wait {
				wait = remaining
			}
		}
		if !w.wait(ctx, wait) {
			return nil
		}
	}
}

// ProcessOne claims and processes at most one due task.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if w.store == nil {
		return false, errors.New("governance: worker store not configured")
	}
	now := w.now().UTC()
	var claimed task.Job
	var found bool
	if err := w.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		claimed, found, err = tx.ClaimNextGovernanceJob(ctx, now)
		return err
	}); err != nil {
		return false, fmt.Errorf("领取治理任务: %w", err)
	}
	if !found {
		return false, nil
	}
	handler := w.handlers[claimed.Kind]
	if handler == nil {
		w.logger.Warn("governance handler missing", "kind", claimed.Kind, "job_id", claimed.ID)
		return true, w.reschedule(claimed, task.StatusFailed, nil, "handler_missing", "", claimed.ConsecutiveFailures)
	}

	started := time.Now()
	err := handler.Process(ctx, claimed)
	duration := time.Since(started).Milliseconds()
	if err == nil {
		w.logger.Info("governance job completed",
			"job_id", claimed.ID,
			"kind", claimed.Kind,
			"attempts", claimed.Attempts,
			"duration_ms", duration,
		)
		return true, nil
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		w.requeueAfterStop(claimed)
		return true, nil
	}
	if errors.Is(err, ErrRevisionChanged) {
		w.logger.Info("governance stale result discarded",
			"job_id", claimed.ID,
			"kind", claimed.Kind,
			"revision", claimed.Revision,
		)
		return true, nil
	}
	var retry *RetryError
	if errors.As(err, &retry) {
		at := retry.At.UTC()
		if at.IsZero() || at.Before(w.now().UTC()) {
			at = w.now().UTC()
		}
		reason := retry.Reason
		if reason == "" {
			reason = task.WaitRateLimited
		}
		code := retry.Code
		if code == "" {
			code = task.ErrorRateLimited
		}
		w.logger.Info("governance job deferred",
			"job_id", claimed.ID,
			"kind", claimed.Kind,
			"reason", reason,
			"duration_ms", duration,
		)
		if rescheduleErr := w.reschedule(claimed, task.StatusWaiting, &at, code, reason, claimed.ConsecutiveFailures); rescheduleErr != nil {
			return true, rescheduleErr
		}
		return true, nil
	}

	kind := failureKind(err)
	failures := claimed.ConsecutiveFailures + 1
	status := task.StatusWaiting
	reason := task.WaitTransient
	code := task.ErrorTransient
	var next *time.Time
	switch kind {
	case "configuration":
		reason = task.WaitConfiguration
		code = task.ErrorConfiguration
	case "protocol":
		reason = task.WaitProtocol
		code = task.ErrorProtocol
		if failures >= w.maxProtocol {
			status = task.StatusFailed
		}
	}
	if status == task.StatusWaiting {
		at := w.now().UTC().Add(w.failureDelay(failures))
		next = &at
	}
	w.logger.Warn("governance job failed",
		"job_id", claimed.ID,
		"kind", claimed.Kind,
		"attempts", claimed.Attempts,
		"status", status,
		"reason", reason,
		"duration_ms", duration,
		"error", safeError(err),
	)
	if rescheduleErr := w.reschedule(claimed, status, next, code, reason, failures); rescheduleErr != nil {
		return true, rescheduleErr
	}
	return true, nil
}

func (w *Worker) ensureReconcile(ctx context.Context, now time.Time) error {
	return w.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:          task.KindReconcile,
			TargetKey:     task.ReconcileKey,
			InputHash:     "tick:" + now.Format(time.RFC3339Nano),
			NextAttemptAt: &now,
			Now:           now,
		})
		return err
	})
}

func (w *Worker) reschedule(job task.Job, status task.Status, next *time.Time, code, reason string, failures int) error {
	now := w.now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return w.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		ok, err := tx.RescheduleJob(ctx, job.ID, job.Revision, task.RescheduleInput{
			Status:              status,
			NextAttemptAt:       next,
			LastErrorCode:       code,
			WaitReason:          reason,
			ConsecutiveFailures: failures,
			Now:                 now,
		})
		if err != nil {
			return err
		}
		if !ok {
			w.logger.Info("governance reschedule skipped after revision change", "job_id", job.ID)
		}
		return nil
	})
}

func (w *Worker) requeueAfterStop(claimed task.Job) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := w.now().UTC()
	err := w.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		ok, err := tx.RescheduleJob(ctx, claimed.ID, claimed.Revision, task.RescheduleInput{
			Status:              task.StatusQueued,
			NextAttemptAt:       &now,
			ConsecutiveFailures: claimed.ConsecutiveFailures,
			Now:                 now,
		})
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return nil
	})
	if err != nil {
		w.logger.Warn("governance shutdown requeue failed", "job_id", claimed.ID, "error", safeError(err))
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

func (w *Worker) wait(ctx context.Context, interval time.Duration) bool {
	if interval <= 0 {
		interval = defaultPollInterval
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
	return "transient"
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	runes := []rune(message)
	if len(runes) > 240 {
		runes = runes[:240]
	}
	return string(runes)
}
