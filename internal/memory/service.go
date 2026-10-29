package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
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
	// governanceDebounce delays ordinary classify work so a burst of writes to
	// one target converges into one model call. Zero keeps tests synchronous.
	governanceDebounce time.Duration
	// artifacts is optional. Runtime supplies it to keep derived summaries in
	// sync; unit tests that only exercise atomic writes may omit it.
	artifacts ArtifactSync
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

// WithGovernanceDebounce sets the delay before newly registered classify work
// becomes due. The runtime supplies the configured value; tests may use zero.
func WithGovernanceDebounce(delay time.Duration) Option {
	return func(service *Service) {
		if delay < 0 {
			delay = 0
		}
		service.governanceDebounce = delay
	}
}

// WithArtifactSync installs the derived-layer invalidation and aggregation hook.
// A nil hook keeps atomic-only tests independent from derived persistence.
func WithArtifactSync(sync ArtifactSync) Option {
	return func(service *Service) {
		if sync != nil {
			service.artifacts = sync
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
	_ MemoryService   = (*Service)(nil)
	_ DetailService   = (*Service)(nil)
	_ Writer          = (*Service)(nil)
	_ ConflictService = (*Service)(nil)
)

// Get returns the current snapshot, including a deleted tombstone.
func (s *Service) Get(ctx context.Context, id string) (Memory, error) {
	return s.reader.GetMemory(ctx, id)
}

// List returns the management list. Empty states default to active, archived
// and all assertion statuses; the list never hides pending work by default.
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

// Relations returns both directions of the current relation graph for one
// memory, including whether each endpoint moved past the recorded version.
func (s *Service) Relations(ctx context.Context, id string) ([]Relation, error) {
	if _, err := s.reader.GetMemory(ctx, id); err != nil {
		return nil, err
	}
	return s.reader.ListRelationsForMemory(ctx, id)
}

// Detail returns the canonical current view together with the exact
// observations that support its current version, typed relation refs, open
// conflict refs and directly derived dependents.
func (s *Service) Detail(ctx context.Context, id string) (Detail, error) {
	item, err := s.reader.GetMemory(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	observations, err := s.reader.ListMemoryObservationRefs(ctx, id, item.Version)
	if err != nil {
		return Detail{}, err
	}
	relations, err := s.reader.ListMemoryRelationRefs(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	conflicts, err := s.reader.ListConflictIDsForMemory(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	dependents, err := s.reader.ListDependents(ctx, artifact.DependencyRef{
		Type: artifact.DependencyMemory,
		ID:   id,
	})
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{
		Memory:            item,
		ObservationIDs:    nonNilStrings(observations),
		Relations:         nonNilRelationRefs(relations),
		ConflictIDs:       nonNilStrings(conflicts),
		DerivedDependents: derivedDependents(dependents),
	}
	return detail, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilRelationRefs(values []RelationRef) []RelationRef {
	if values == nil {
		return []RelationRef{}
	}
	return values
}

func derivedDependents(items []artifact.DerivedRef) []DerivedDependent {
	result := make([]DerivedDependent, 0, len(items))
	for _, item := range items {
		result = append(result, DerivedDependent{
			ID:        item.ID,
			Layer:     string(item.Layer),
			Kind:      string(item.Kind),
			TargetKey: item.TargetKey,
			ScopeKey:  item.ScopeKey,
			Topic:     item.Topic,
			Version:   item.Version,
			State:     string(item.State),
		})
	}
	return result
}

// Create writes the first snapshot, revision, complete evidence set and search
// document in the caller's transaction.
func (s *Service) Create(ctx context.Context, tx Tx, input CreateInput) (Memory, error) {
	now := s.timestamp()
	kind := input.Kind
	if kind == "" {
		kind = KindFact
	}
	status, err := NormalizeAssertionStatus(input.AssertionStatus)
	if err != nil {
		return Memory{}, err
	}
	item := Memory{
		ID:              idgen.NewUUID(),
		Kind:            kind,
		Title:           input.Title,
		Content:         input.Content,
		State:           StateActive,
		AssertionStatus: status,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
		OriginSourceID:  input.OriginSourceID,
		ScopeKey:        NormalizeScopeKey(input.ScopeKey),
	}
	evidence, err := s.prepareEvidence(ctx, tx, input.Evidence, now)
	if err != nil {
		return Memory{}, err
	}
	if err := tx.InsertMemory(ctx, item); err != nil {
		return Memory{}, err
	}
	operation := input.Operation
	if operation == "" {
		operation = OperationCreate
	}
	if err := s.writeRevision(ctx, tx, item, operation, input.Reason, evidence); err != nil {
		return Memory{}, err
	}
	if err := s.syncSearchDocument(ctx, tx, item); err != nil {
		return Memory{}, err
	}
	if err := s.enqueueClassify(ctx, tx, item); err != nil {
		return Memory{}, err
	}
	if err := s.syncMemoryChanged(ctx, tx, nil, &item, true); err != nil {
		return Memory{}, err
	}
	return item, nil
}

// Revise creates a new content, assertion or evidence revision from the
// persisted current snapshot. The caller never supplies a version
// precondition. Content changes do not inherit the previous conclusion's
// evidence unless the caller explicitly includes it; add_evidence appends to
// the current complete set.
func (s *Service) Revise(ctx context.Context, tx Tx, input ReviseInput) (Memory, error) {
	current, err := tx.GetMemory(ctx, input.MemoryID)
	if err != nil {
		return Memory{}, err
	}
	now := s.timestamp()
	candidate := current
	candidate.Version = current.Version + 1
	candidate.UpdatedAt = now
	if input.Operation != OperationAddEvidence {
		candidate.Kind = input.Kind
		if candidate.Kind == "" {
			candidate.Kind = current.Kind
		}
		candidate.Title = input.Title
		candidate.Content = input.Content
		if input.AssertionStatus != "" {
			status, err := NormalizeAssertionStatus(input.AssertionStatus)
			if err != nil {
				return Memory{}, err
			}
			candidate.AssertionStatus = status
		}
		if scope := NormalizeScopeKey(input.ScopeKey); scope != "" {
			candidate.ScopeKey = scope
		}
	}

	contentUnchanged := candidate.Kind == current.Kind &&
		candidate.Title == current.Title &&
		candidate.Content == current.Content &&
		candidate.AssertionStatus == current.AssertionStatus &&
		candidate.ScopeKey == current.ScopeKey

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
	if err := s.enqueueClassify(ctx, tx, candidate); err != nil {
		return Memory{}, err
	}
	// Evidence-only additions leave the derived text usable: the atomic
	// assertions did not change and current dependencies still point at a real
	// historical revision.
	if err := s.syncMemoryChanged(ctx, tx, &current, &candidate, !contentUnchanged); err != nil {
		return Memory{}, err
	}
	// Evidence-only additions still count as new material for conflict review
	// even though they leave derived prose usable.
	return candidate, nil
}

// Classify applies scope-only classification. Topics are a separate
// classification result and are never embedded in the canonical memory.
func (s *Service) Classify(ctx context.Context, tx Tx, input ClassificationInput) (Memory, error) {
	current, err := tx.GetMemory(ctx, input.MemoryID)
	if err != nil {
		return Memory{}, err
	}
	scope := NormalizeScopeKey(input.ScopeKey)
	if scope == "" || scope == current.ScopeKey {
		return current, nil
	}
	candidate := current
	candidate.ScopeKey = scope
	candidate.Version = current.Version + 1
	candidate.UpdatedAt = s.timestamp()
	evidence, err := tx.ListRevisionEvidence(ctx, current.ID, current.Version)
	if err != nil {
		return Memory{}, err
	}
	if err := tx.UpdateMemory(ctx, candidate); err != nil {
		return Memory{}, err
	}
	if err := s.writeRevision(ctx, tx, candidate, OperationClassify, "governance:classify", evidence); err != nil {
		return Memory{}, err
	}
	if err := s.syncMemoryChanged(ctx, tx, &current, &candidate, true); err != nil {
		return Memory{}, err
	}
	return candidate, nil
}

func (s *Service) enqueueClassify(ctx context.Context, tx Tx, item Memory) error {
	if item.State == StateDeleted {
		return nil
	}
	now := s.timestamp()
	next := now.Add(s.governanceDebounce)
	_, _, err := tx.UpsertJob(ctx, task.UpsertInput{
		Kind:          task.KindClassifyObservation,
		TargetKey:     task.MemoryKey(item.ID),
		InputHash:     classifyInputHash(item),
		NextAttemptAt: &next,
		Now:           now,
	})
	if err != nil {
		return fmt.Errorf("登记记忆 %s 的分类任务: %w", item.ID, err)
	}
	return nil
}

func classifyInputHash(item Memory) string {
	return task.InputHash(
		string(item.Kind),
		item.Title,
		item.Content,
		item.ScopeKey,
		item.OriginSourceID,
	)
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
	if item.State == StateDeleted || item.AssertionStatus == AssertionRejected {
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
