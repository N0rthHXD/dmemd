package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrInvalidContent is returned when a submission carries content that is not
// valid JSON. HTTP decodes the request body before calling the service; this
// guard keeps the internal entry point honest as well. It is not a business
// validation rule.
var ErrInvalidContent = errors.New("ingest: content must be valid JSON")

// Service implements unified submission and task queries over the durable
// source/job tables. Submit never calls a model and returns only after the
// source and initial queued job have committed together.
type Service struct {
	store Store
	jobs  JobReader
	now   func() time.Time
}

// Option customizes a Service for deterministic tests.
type Option func(*Service)

// WithClock overrides the service clock. Values are normalized to UTC.
func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

// NewService assembles the ingestion service. store must provide the combined
// source/job transaction and jobs the read-only task query.
func NewService(store Store, jobs JobReader, options ...Option) *Service {
	service := &Service{store: store, jobs: jobs, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

var _ IngestService = (*Service)(nil)

// Submit stores one source and one queued job in a single short transaction.
// Repeated submissions create independent sources and jobs by design.
func (s *Service) Submit(ctx context.Context, submission Submission) (Receipt, error) {
	content, err := normalizeContent(submission.Content)
	if err != nil {
		return Receipt{}, err
	}
	now := s.timestamp()
	src := source.Source{
		ID:         idgen.NewUUID(),
		AppID:      submission.AppID,
		ExternalID: submission.ExternalID,
		ScopeKey:   memory.NormalizeScopeKey(submission.ScopeKey),
		Content:    content,
		CreatedAt:  now,
	}
	job := Job{
		ID:             idgen.NewUUID(),
		SourceID:       src.ID,
		Status:         JobQueued,
		Attempts:       0,
		ObservationIDs: []string{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.InsertSource(ctx, src); err != nil {
			return err
		}
		return tx.InsertJob(ctx, job)
	}); err != nil {
		return Receipt{}, fmt.Errorf("接收内容: %w", err)
	}
	return Receipt{SourceID: src.ID, JobID: job.ID, Status: JobQueued}, nil
}

// GetJob returns the durable task state and generated memory IDs.
func (s *Service) GetJob(ctx context.Context, id string) (Job, error) {
	if s.jobs == nil {
		return Job{}, errors.New("ingest: job reader not configured")
	}
	return s.jobs.GetJob(ctx, id)
}

func (s *Service) timestamp() time.Time {
	return s.now().UTC()
}

func normalizeContent(content json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(content)) == 0 {
		// Missing content is stored as explicit JSON null, matching the HTTP
		// contract and keeping the original-shape rule for all other values.
		return json.RawMessage("null"), nil
	}
	if !json.Valid(content) {
		return nil, ErrInvalidContent
	}
	return append(json.RawMessage(nil), content...), nil
}
