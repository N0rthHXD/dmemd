package governance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

// TopicSuggester is the optional classification model operation. It receives
// only the selected snapshot and returns raw topic labels.
type TopicSuggester func(ctx context.Context, title, content, scopeKey string) ([]string, error)

// ClassificationWriter applies classification-only revisions without
// registering another classify task.
type ClassificationWriter interface {
	memory.Writer
	Classify(ctx context.Context, tx memory.Tx, input memory.ClassificationInput) (memory.Memory, error)
}

// ClassifyHandler is retained as a task-completion boundary while topics move
// to an independent classification result. It never writes labels into the
// canonical memory.
type ClassifyHandler struct {
	reader  MemoryReader
	store   Store
	writer  ClassificationWriter
	suggest TopicSuggester
	limiter *Limiter
	now     func() time.Time
	timeout time.Duration
}

// ClassifyOption customizes the classify handler.
type ClassifyOption func(*ClassifyHandler)

// WithClassifyClock overrides the handler clock for deterministic tests.
func WithClassifyClock(now func() time.Time) ClassifyOption {
	return func(handler *ClassifyHandler) {
		if now != nil {
			handler.now = now
		}
	}
}

// WithClassifyTimeout bounds one optional classification model call.
func WithClassifyTimeout(timeout time.Duration) ClassifyOption {
	return func(handler *ClassifyHandler) {
		if timeout > 0 {
			handler.timeout = timeout
		}
	}
}

// NewClassifyHandler assembles the classify processor.
func NewClassifyHandler(reader MemoryReader, store Store, writer ClassificationWriter,
	suggester TopicSuggester, limiter *Limiter, options ...ClassifyOption) *ClassifyHandler {
	handler := &ClassifyHandler{
		reader: reader, store: store, writer: writer,
		suggest: suggester, limiter: limiter, now: time.Now, timeout: time.Minute,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// Process implements Handler. Classification output is independent of the
// canonical memory, so an absent classification pipeline completes the task
// rather than mutating a memory body or scope.
func (h *ClassifyHandler) Process(ctx context.Context, job task.Job) error {
	if job.Kind != task.KindClassifyObservation {
		return fmt.Errorf("classify handler received kind %s", job.Kind)
	}
	if h.reader == nil || h.store == nil {
		return errors.New("governance: classify handler dependencies not configured")
	}
	current, err := h.reader.GetMemory(ctx, job.TargetKey)
	if errors.Is(err, memory.ErrNotFound) {
		return CancelMissingTask(ctx, h.store, job, h.now().UTC())
	}
	if err != nil {
		return fmt.Errorf("读取待分类记忆: %w", err)
	}
	if current.State == memory.StateDeleted {
		return CancelMissingTask(ctx, h.store, job, h.now().UTC())
	}
	now := h.now().UTC()
	return h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		ok, err := tx.FinishJob(ctx, job.ID, job.Revision, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrRevisionChanged
		}
		return nil
	})
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
