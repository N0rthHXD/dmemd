package source

import (
	"context"
	"time"
)

// RedactedJobReason is the fixed, content-free explanation written when a
// source is redacted. It replaces pending-job errors and scrubs terminal error
// summaries that could contain original text.
const RedactedJobReason = "source redacted"

// Service implements source lookup and explicit redaction. Sources themselves
// are created only by ingest, so there is no public creation method.
type Service struct {
	reader     Reader
	store      Store
	now        func() time.Time
	propagator RedactionPropagator
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

// WithRedactionPropagator installs the governance propagation step. It runs in
// the same transaction as the source redaction, so retracted observations and
// invalidated derived objects cannot be separated from the cleared payload.
func WithRedactionPropagator(propagator RedactionPropagator) Option {
	return func(service *Service) {
		if propagator != nil {
			service.propagator = propagator
		}
	}
}

// NewService assembles the source service.
func NewService(reader Reader, store Store, options ...Option) *Service {
	service := &Service{reader: reader, store: store, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

var _ SourceService = (*Service)(nil)

// Get returns a source, including its redacted status after explicit removal.
func (s *Service) Get(ctx context.Context, id string) (Source, error) {
	return s.reader.GetSource(ctx, id)
}

// Redact clears the original payload and every evidence copy in one
// transaction, then cancels queued/running jobs for the source and scrubs
// terminal error summaries. Repeating the operation is stable: the first
// redacted_at is preserved and no completed job status or result is changed.
func (s *Service) Redact(ctx context.Context, id string) (RedactResult, error) {
	var result RedactResult
	err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		now := s.now().UTC()
		existing, err := tx.GetSource(ctx, id)
		if err != nil {
			return err
		}
		alreadyRedacted := existing.Redacted
		if err := tx.RedactSource(ctx, id, now); err != nil {
			return err
		}
		if err := tx.RedactEvidenceBySource(ctx, id, now); err != nil {
			return err
		}
		cancelled, err := tx.CancelJobsBySource(ctx, id, RedactedJobReason, now)
		if err != nil {
			return err
		}
		if _, err := tx.ScrubJobErrorsBySource(ctx, id, RedactedJobReason, now); err != nil {
			return err
		}
		result = RedactResult{SourceID: id, Redacted: true, CancelledJobs: cancelled}
		if s.propagator != nil && !alreadyRedacted {
			impact, err := s.propagator.PropagateRedaction(ctx, tx, id, now)
			if err != nil {
				return err
			}
			result.RetractedObservations = len(impact.ObservationIDs)
			result.InvalidatedDerived = impact.InvalidatedDerived
		}
		return nil
	})
	return result, err
}
