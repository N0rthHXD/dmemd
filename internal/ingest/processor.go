package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// SourceReader loads the accepted source for background processing.
type SourceReader interface {
	GetSource(ctx context.Context, id string) (source.Source, error)
}

// ProcessResult reports one completed processing attempt.
type ProcessResult struct {
	Status    JobStatus
	MemoryIDs []string
}

// Processor orchestrates source loading, related active-memory selection and
// provider extraction outside the write transaction, then applies merge
// decisions and the job success state in one short transaction.
type Processor struct {
	store     Store
	sources   SourceReader
	related   retrieval.RelatedService
	writer    memory.Writer
	provider  ExtractionProvider
	modelName string
	now       func() time.Time
	logger    *slog.Logger
}

// ProcessorOption customizes a Processor.
type ProcessorOption func(*Processor)

// WithProcessorClock overrides the processing clock. Values are UTC.
func WithProcessorClock(now func() time.Time) ProcessorOption {
	return func(processor *Processor) {
		if now != nil {
			processor.now = now
		}
	}
}

// WithProcessorLogger sets the structured logger. It never receives source
// content, memory text, model requests or model responses.
func WithProcessorLogger(logger *slog.Logger) ProcessorOption {
	return func(processor *Processor) {
		if logger != nil {
			processor.logger = logger
		}
	}
}

// WithModelName sets the model identifier recorded on generated evidence.
func WithModelName(name string) ProcessorOption {
	return func(processor *Processor) {
		processor.modelName = name
	}
}

// NewProcessor assembles the background extraction chain. All network work and
// candidate preparation happen before Store.WithTx.
func NewProcessor(store Store, sources SourceReader, related retrieval.RelatedService, writer memory.Writer, provider ExtractionProvider, options ...ProcessorOption) *Processor {
	processor := &Processor{
		store:    store,
		sources:  sources,
		related:  related,
		writer:   writer,
		provider: provider,
		now:      time.Now,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(processor)
	}
	return processor
}

// Process runs one claimed job. On success it returns the generated or updated
// memory IDs. Cancelled or already-terminal jobs are a no-op so a redaction
// that commits first is never overwritten.
func (p *Processor) Process(ctx context.Context, job Job) (ProcessResult, error) {
	if p.store == nil || p.sources == nil || p.related == nil || p.writer == nil || p.provider == nil {
		return ProcessResult{}, errors.New("ingest: processor dependencies not configured")
	}

	src, err := p.sources.GetSource(ctx, job.SourceID)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("读取待处理来源: %w", err)
	}
	if src.Redacted {
		// The redaction transaction already cancelled pending jobs. Do not call
		// the provider and do not write a result.
		return p.finishCancelled(ctx, job)
	}

	queryText := sourceText(src.Content)
	related, err := p.related.RelatedActive(ctx, queryText, 0)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("选择相关记忆: %w", err)
	}

	started := time.Now()
	extracted, err := p.provider.Extract(ctx, ExtractionRequest{Source: src, RelatedMemories: related})
	if err != nil {
		return ProcessResult{}, err
	}
	p.logger.Info("model extraction completed",
		"job_id", job.ID,
		"model", p.modelName,
		"candidates", len(extracted.Candidates),
		"duration_ms", time.Since(started).Milliseconds(),
	)

	candidates := normalizeCandidates(src, related, extracted.Candidates, p.modelName, p.now().UTC())
	var result mergeResult
	outcome := JobSucceeded
	now := p.now().UTC()
	err = p.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		currentJob, err := tx.GetJob(ctx, job.ID)
		if err != nil {
			return err
		}
		switch currentJob.Status {
		case JobCancelled, JobSucceeded, JobFailed:
			outcome = currentJob.Status
			return nil
		}
		currentSource, err := tx.GetSource(ctx, job.SourceID)
		if err != nil {
			return err
		}
		if currentSource.Redacted {
			outcome = JobCancelled
			currentJob.Status = JobCancelled
			currentJob.Error = source.RedactedJobReason
			currentJob.MemoryIDs = []string{}
			currentJob.NextAttemptAt = nil
			currentJob.UpdatedAt = now
			currentJob.FinishedAt = &now
			return tx.UpdateJob(ctx, currentJob)
		}

		result, err = applyCandidates(ctx, tx, p.writer, currentSource, related, candidates)
		if err != nil {
			return err
		}
		currentJob.Status = JobSucceeded
		currentJob.MemoryIDs = result.MemoryIDs
		if currentJob.MemoryIDs == nil {
			currentJob.MemoryIDs = []string{}
		}
		currentJob.Error = ""
		currentJob.NextAttemptAt = nil
		currentJob.UpdatedAt = now
		currentJob.FinishedAt = &now
		if err := tx.UpdateJob(ctx, currentJob); err != nil {
			return err
		}
		for _, decision := range result.Decisions {
			p.logger.Debug("ingest decision",
				"job_id", job.ID,
				"action", decision.Action,
				"memory_id", decision.MemoryID,
			)
		}
		return nil
	})
	if err != nil {
		return ProcessResult{}, fmt.Errorf("提交处理结果: %w", err)
	}
	if outcome != JobSucceeded {
		return ProcessResult{Status: outcome, MemoryIDs: []string{}}, nil
	}
	return ProcessResult{Status: JobSucceeded, MemoryIDs: result.MemoryIDs}, nil
}

func (p *Processor) finishCancelled(ctx context.Context, job Job) (ProcessResult, error) {
	now := p.now().UTC()
	err := p.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		current, err := tx.GetJob(ctx, job.ID)
		if err != nil {
			return err
		}
		switch current.Status {
		case JobCancelled, JobSucceeded, JobFailed:
			return nil
		}
		current.Status = JobCancelled
		current.Error = source.RedactedJobReason
		current.MemoryIDs = []string{}
		current.NextAttemptAt = nil
		current.UpdatedAt = now
		current.FinishedAt = &now
		return tx.UpdateJob(ctx, current)
	})
	if err != nil {
		return ProcessResult{}, err
	}
	return ProcessResult{Status: JobCancelled, MemoryIDs: []string{}}, nil
}

// sourceText extracts readable source text for keyword context selection. The
// original JSON shape is never modified; this is only a derived query.
func sourceText(content json.RawMessage) string {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return string(trimmed)
	}
	parts := make([]string, 0, 8)
	collectText(root, &parts)
	return strings.Join(parts, "\n")
}

func collectText(value any, parts *[]string) {
	switch typed := value.(type) {
	case string:
		*parts = append(*parts, typed)
	case json.Number:
		*parts = append(*parts, typed.String())
	case bool:
		if typed {
			*parts = append(*parts, "true")
		} else {
			*parts = append(*parts, "false")
		}
	case []any:
		for _, item := range typed {
			collectText(item, parts)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectText(typed[key], parts)
		}
	}
}
