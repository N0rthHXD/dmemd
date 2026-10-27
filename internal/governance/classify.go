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

// ClassifyHandler normalizes existing extraction labels and, when they are
// missing, asks the classification model for labels; the writer applies taxonomy.
type ClassifyHandler struct {
	reader   MemoryReader
	store    Store
	writer   ClassificationWriter
	suggest  TopicSuggester
	limiter  *Limiter
	now      func() time.Time
	timeout  time.Duration
	maxRunes int
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

// NewClassifyHandler assembles the classify processor. A nil suggester keeps
// deterministic classification only; missing topics remain empty and the task
// still succeeds, so reconcile does not loop on the same input.
func NewClassifyHandler(reader MemoryReader, store Store, writer ClassificationWriter,
	suggester TopicSuggester, limiter *Limiter, options ...ClassifyOption) *ClassifyHandler {
	handler := &ClassifyHandler{
		reader:   reader,
		store:    store,
		writer:   writer,
		suggest:  suggester,
		limiter:  limiter,
		now:      time.Now,
		timeout:  time.Minute,
		maxRunes: 8000,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// Process implements Handler.
func (h *ClassifyHandler) Process(ctx context.Context, job task.Job) error {
	if job.Kind != task.KindClassify {
		return fmt.Errorf("classify handler received kind %s", job.Kind)
	}
	if h.reader == nil || h.store == nil || h.writer == nil {
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

	topics := current.RawTopics
	if topics == nil {
		topics = current.Topics
	}
	if len(topics) == 0 && h.suggest != nil {
		allowed, next := h.limiter.Acquire(h.now().UTC())
		if !allowed {
			return &RetryError{At: next, Code: task.ErrorRateLimited, Reason: task.WaitRateLimited}
		}
		callCtx := ctx
		cancel := func() {}
		if h.timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, h.timeout)
		}
		started := time.Now()
		suggested, suggestErr := h.suggest(callCtx, current.Title, truncateRunes(current.Content, h.maxRunes), current.ScopeKey)
		cancel()
		h.limiter.Record("classify", 1, len(current.Title)+len(current.Content), started, suggestErr)
		if suggestErr != nil {
			return suggestErr
		}
		topics = suggested
	}

	now := h.now().UTC()
	err = h.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := checkedJob(ctx, tx, job); err != nil {
			return err
		}
		latest, err := tx.GetMemory(ctx, job.TargetKey)
		if errors.Is(err, memory.ErrNotFound) {
			return ErrRevisionChanged
		}
		if err != nil {
			return err
		}
		if latest.State == memory.StateDeleted {
			return ErrRevisionChanged
		}
		if latest.Version != current.Version {
			return ErrRevisionChanged
		}
		if _, err := h.writer.Classify(ctx, tx, memory.ClassificationInput{
			MemoryID: latest.ID,
			ScopeKey: latest.ScopeKey,
			Topics:   topics,
		}); err != nil {
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
	if err != nil {
		return err
	}
	return nil
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
