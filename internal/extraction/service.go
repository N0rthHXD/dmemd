package extraction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/source"
)

// ProtocolVersion is recorded as the extraction version of every generated
// observation. It changes only when the model contract or deterministic
// extraction semantics change.
const ProtocolVersion = "extraction-v6"

const (
	// DefaultTimeout bounds one remote extraction call.
	DefaultTimeout = 180 * time.Second
	// DefaultChunkRunes is the bounded source window used for a long source.
	DefaultChunkRunes = 12000
)

// Provider is the minimal extraction model capability. It receives only the
// bounded source view and returns span selections; it never sees memory state
// and never writes storage.
type Provider interface {
	Extract(ctx context.Context, request Request) (Result, error)
}

// Service implements the extraction stage: bounded source windows, model calls
// and cross-chunk draft collection. Persistence and task scheduling are owned by
// the caller's transaction.
type Service struct {
	provider   Provider
	timeout    time.Duration
	chunkRunes int
	logger     *slog.Logger
}

// Option customizes an extraction Service.
type Option func(*Service)

// WithTimeout overrides the per-call timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(service *Service) {
		if timeout > 0 {
			service.timeout = timeout
		}
	}
}

// WithChunkRunes bounds one model input window. Non-positive keeps the default.
func WithChunkRunes(value int) Option {
	return func(service *Service) {
		if value > 0 {
			service.chunkRunes = value
		}
	}
}

// WithLogger installs a structured logger. It never receives source text or raw
// model responses.
func WithLogger(logger *slog.Logger) Option {
	return func(service *Service) {
		if logger != nil {
			service.logger = logger
		}
	}
}

// NewService assembles the extraction stage. No network request is made here.
func NewService(provider Provider, options ...Option) *Service {
	service := &Service{
		provider:   provider,
		timeout:    DefaultTimeout,
		chunkRunes: DefaultChunkRunes,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

// Extract calls the provider over deterministic source windows and collects
// every parsed draft. A failed window is isolated only when another window
// succeeded; with a single window the provider error is returned unchanged so
// the worker can apply its normal retry policy.
func (s *Service) Extract(ctx context.Context, src source.Source) (Result, error) {
	if s == nil || s.provider == nil {
		return Result{}, errors.New("extraction: provider not configured")
	}
	view := source.BuildView(src.Content)
	chunks := view.Chunks(s.chunkRunes)
	if len(chunks) == 0 {
		chunks = []source.Chunk{{}}
	}
	result := Result{Chunks: len(chunks)}
	var lastErr error
	for index, chunk := range chunks {
		request := Request{
			Source:     src,
			Segments:   chunk.Segments,
			ChunkIndex: index + 1,
			ChunkCount: len(chunks),
		}
		if len(chunks) > 1 {
			request.ContextSegments = chunk.ContextSegments
		}
		callCtx, cancel := context.WithTimeout(ctx, s.timeout)
		extracted, err := s.provider.Extract(callCtx, request)
		cancel()
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Usage.Add(extracted.Usage)
		if err != nil {
			lastErr = err
			result.ChunkFailures++
			if len(chunks) == 1 {
				return result, err
			}
			s.logger.Warn("extraction chunk failed",
				"chunk", index+1, "chunks", len(chunks), "error_type", fmt.Sprintf("%T", err))
			continue
		}
		chunkView := source.View{Segments: chunk.Segments, ContextSegments: chunk.ContextSegments}
		for _, draft := range extracted.Drafts {
			if _, err := source.ResolveSegments(chunkView, draft.SpanIDs); err != nil {
				// A context-only or unknown selection is never promoted into
				// evidence. A normalized expression may still survive as a
				// pending observation; an extractive draft has nothing to save.
				if strings.TrimSpace(draft.Content) == "" {
					continue
				}
				draft.SpanIDs = nil
			}
			result.Drafts = append(result.Drafts, draft)
		}
	}
	if result.ChunkFailures == len(chunks) && lastErr != nil {
		return result, fmt.Errorf("extraction: all chunks failed: %w", lastErr)
	}
	return result, nil
}
