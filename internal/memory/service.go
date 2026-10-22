package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrDocumentBuilderMissing is returned by a write operation when the injected
// search-document builder was not configured. Production wiring supplies the
// normalized builder; tests may inject a deterministic builder.
var ErrDocumentBuilderMissing = errors.New("memory: 搜索文档构造器未配置")

// Service implements the management read API and the internal unified write
// unit. Content creation and correction are exposed only to ingest; the public
// MemoryService never writes content directly.
type Service struct {
	reader    Reader
	store     Store
	documents DocumentBuilder
	now       func() time.Time
}

// Option customizes a Service. It is primarily useful for deterministic tests.
type Option func(*Service)

// WithClock overrides the service clock. Values are always normalized to UTC.
func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

// NewService assembles the memory service. The store owns transaction
// boundaries, so callers such as Worker can hold one transaction across several
// memory writes without nesting.
func NewService(reader Reader, store Store, documents DocumentBuilder, options ...Option) *Service {
	service := &Service{reader: reader, store: store, documents: documents, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

var (
	_ MemoryService = (*Service)(nil)
	_ Writer        = (*Service)(nil)
)

// Get returns the current snapshot, including a deleted tombstone.
func (s *Service) Get(ctx context.Context, id string) (Memory, error) {
	return s.reader.GetMemory(ctx, id)
}

// List returns the management list. Empty states default to active and
// archived; deleted tombstones require an explicit filter. Storage applies the
// fixed updated_at DESC, id ASC ordering and the default limit of 50.
func (s *Service) List(ctx context.Context, options ListOptions) ([]Memory, error) {
	if len(options.States) == 0 {
		options.States = []State{StateActive, StateArchived}
	}
	return s.reader.ListMemories(ctx, options)
}

// ListVersions returns complete history snapshots, newest first.
func (s *Service) ListVersions(ctx context.Context, id string, limit int) ([]Revision, error) {
	if _, err := s.reader.GetMemory(ctx, id); err != nil {
		return nil, err
	}
	return s.reader.ListRevisions(ctx, id, limit)
}

// GetVersion returns one complete, immutable revision.
func (s *Service) GetVersion(ctx context.Context, id string, version int) (Revision, error) {
	return s.reader.GetRevision(ctx, id, version)
}

// GetEvidence returns the current evidence set when version is zero or
// negative, otherwise the complete set for that historical version.
func (s *Service) GetEvidence(ctx context.Context, id string, version int) ([]source.Evidence, error) {
	current, err := s.reader.GetMemory(ctx, id)
	if err != nil {
		return nil, err
	}
	target := version
	if target <= 0 {
		target = current.Version
	}
	revision, err := s.reader.GetRevision(ctx, id, target)
	if err != nil {
		return nil, err
	}
	return revision.Evidence, nil
}

// Create writes the first snapshot, revision, complete evidence set and search
// document in the caller's transaction.
func (s *Service) Create(ctx context.Context, tx Tx, input CreateInput) (Memory, error) {
	now := s.timestamp()
	observedAt := normalizeObservedAt(input.ObservedAt, now)
	kind := input.Kind
	if kind == "" {
		kind = KindFact
	}
	item := Memory{
		ID:             idgen.NewUUID(),
		Kind:           kind,
		Title:          input.Title,
		Content:        input.Content,
		State:          StateActive,
		Version:        1,
		ObservedAt:     observedAt,
		CreatedAt:      now,
		UpdatedAt:      now,
		CreatedByApp:   input.CreatedByApp,
		OriginSourceID: input.OriginSourceID,
	}
	evidence, err := s.prepareEvidence(ctx, tx, input.Evidence, now)
	if err != nil {
		return Memory{}, err
	}
	if err := tx.InsertMemory(ctx, item); err != nil {
		return Memory{}, err
	}
	if err := s.writeRevision(ctx, tx, item, OperationCreate, input.Reason, evidence); err != nil {
		return Memory{}, err
	}
	if err := s.syncSearchDocument(ctx, tx, item); err != nil {
		return Memory{}, err
	}
	return item, nil
}

// Revise creates a new content or evidence revision from the persisted current
// snapshot. The caller never supplies a version precondition. Content changes
// do not inherit the previous conclusion's evidence unless the caller explicitly
// includes it; add_evidence appends to the current complete set.
func (s *Service) Revise(ctx context.Context, tx Tx, input ReviseInput) (Memory, error) {
	current, err := tx.GetMemory(ctx, input.MemoryID)
	if err != nil {
		return Memory{}, err
	}
	now := s.timestamp()
	candidate := current
	candidate.Version = current.Version + 1
	candidate.UpdatedAt = now
	if input.Operation == OperationAddEvidence {
		// Evidence-only changes keep the current fact text unchanged.
	} else {
		candidate.Kind = input.Kind
		if candidate.Kind == "" {
			candidate.Kind = current.Kind
		}
		candidate.Title = input.Title
		candidate.Content = input.Content
		candidate.ObservedAt = normalizeObservedAt(input.ObservedAt, current.ObservedAt)
	}

	contentUnchanged := candidate.Kind == current.Kind &&
		candidate.Title == current.Title &&
		candidate.Content == current.Content &&
		candidate.ObservedAt.Equal(current.ObservedAt)

	currentEvidence, err := tx.ListRevisionEvidence(ctx, current.ID, current.Version)
	if err != nil {
		return Memory{}, err
	}

	operation := input.Operation
	if !contentUnchanged && (operation == "" || operation == OperationAddEvidence) {
		operation = OperationUpdate
	}
	if operation == "" {
		operation = OperationAddEvidence
	}

	wanted := input.Evidence
	if operation == OperationAddEvidence && contentUnchanged {
		wanted = unionEvidence(currentEvidence, input.Evidence)
	}
	evidence, err := s.prepareEvidence(ctx, tx, wanted, now)
	if err != nil {
		return Memory{}, err
	}
	if contentUnchanged && sameEvidenceSet(evidence, currentEvidence) {
		return current, nil
	}

	if err := tx.UpdateMemory(ctx, candidate); err != nil {
		return Memory{}, err
	}
	if err := s.writeRevision(ctx, tx, candidate, operation, input.Reason, evidence); err != nil {
		return Memory{}, err
	}
	if err := s.syncSearchDocument(ctx, tx, candidate); err != nil {
		return Memory{}, err
	}
	return candidate, nil
}

func (s *Service) writeRevision(ctx context.Context, tx Tx, snapshot Memory, operation Operation, reason string, evidence []source.Evidence) error {
	revision := Revision{
		MemoryID:  snapshot.ID,
		Version:   snapshot.Version,
		Operation: operation,
		Reason:    reason,
		Snapshot:  snapshot,
		Evidence:  evidence,
	}
	if err := tx.InsertRevision(ctx, revision); err != nil {
		return err
	}
	ids := make([]string, 0, len(evidence))
	for _, item := range evidence {
		ids = append(ids, item.ID)
	}
	return tx.LinkRevisionEvidence(ctx, snapshot.ID, snapshot.Version, ids)
}

func (s *Service) syncSearchDocument(ctx context.Context, tx Tx, item Memory) error {
	if item.State == StateDeleted {
		return tx.DeleteSearchDocument(ctx, item.ID)
	}
	if s.documents == nil {
		return ErrDocumentBuilderMissing
	}
	document, err := s.documents.BuildDocument(item)
	if err != nil {
		return fmt.Errorf("构造记忆 %s 的搜索文档: %w", item.ID, err)
	}
	if document.MemoryID == "" {
		document.MemoryID = item.ID
	}
	return tx.UpsertSearchDocument(ctx, document)
}

func (s *Service) timestamp() time.Time {
	return s.now().UTC()
}

func normalizeObservedAt(value, fallback time.Time) time.Time {
	if value.IsZero() {
		value = fallback
	}
	return value.UTC()
}
