package governance

import (
	"log/slog"
	"sync"
	"time"
)

// Limiter is a deliberately small in-process soft budget for governance model
// calls. It is not a durable ledger and resets when the process restarts.
type Limiter struct {
	mu           sync.Mutex
	maxPerWindow int
	window       time.Duration
	minInterval  time.Duration
	windowStart  time.Time
	count        int
	lastCall     time.Time
	stats        map[string]CallStats
	logger       *slog.Logger
}

// CallStats separates actual model calls by operation, not task kind (profiles
// and summaries share the aggregate task kind). It resets on process restart.
type CallStats struct {
	Calls    int64
	Failures int64
	Duration time.Duration
}

// WithLogger enables content-free per-operation call diagnostics.
func (l *Limiter) WithLogger(logger *slog.Logger) *Limiter { l.logger = logger; return l }

// Stats returns a copy of the per-operation counters.
func (l *Limiter) Stats() map[string]CallStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make(map[string]CallStats, len(l.stats))
	for stage, stats := range l.stats {
		result[stage] = stats
	}
	return result
}

// Record records a completed call, independently from the shared rate budget.
func (l *Limiter) Record(stage string, batchSize, contextSize int, started time.Time, err error) {
	if l == nil {
		return
	}
	duration := time.Since(started)
	l.mu.Lock()
	if l.stats == nil {
		l.stats = make(map[string]CallStats)
	}
	stats := l.stats[stage]
	stats.Calls++
	stats.Duration += duration
	if err != nil {
		stats.Failures++
	}
	l.stats[stage] = stats
	l.mu.Unlock()
	if l.logger != nil {
		code := ""
		if err != nil {
			code = failureKind(err)
		}
		l.logger.Info("governance model call", "stage", stage, "batch_size", batchSize, "context_size", contextSize,
			"duration_ms", duration.Milliseconds(), "error_type", code, "calls", stats.Calls, "failures", stats.Failures)
	}
}

// NewLimiter creates a soft limiter. A non-positive maxPerWindow disables the
// hourly cap; a non-positive minInterval disables spacing.
func NewLimiter(maxPerWindow int, minInterval time.Duration) *Limiter {
	return &Limiter{
		maxPerWindow: maxPerWindow,
		window:       time.Hour,
		minInterval:  minInterval,
	}
}

// Acquire records one call when allowed and returns the earliest time a later
// call may be attempted otherwise.
func (l *Limiter) Acquire(now time.Time) (bool, time.Time) {
	if l == nil {
		return true, now
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now = now.UTC()
	if l.maxPerWindow > 0 && !l.windowStart.IsZero() && now.Sub(l.windowStart) >= l.window {
		l.windowStart = time.Time{}
		l.count = 0
	}
	next := time.Time{}
	if l.minInterval > 0 && !l.lastCall.IsZero() {
		earliest := l.lastCall.Add(l.minInterval)
		if now.Before(earliest) {
			next = earliest
		}
	}
	if l.maxPerWindow > 0 && l.count >= l.maxPerWindow {
		reset := l.windowStart.Add(l.window)
		if next.IsZero() || reset.After(next) {
			next = reset
		}
	}
	if !next.IsZero() {
		return false, next
	}
	if l.windowStart.IsZero() {
		l.windowStart = now
	}
	l.count++
	l.lastCall = now
	return true, now
}
