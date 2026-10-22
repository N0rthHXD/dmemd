package governance

import (
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
