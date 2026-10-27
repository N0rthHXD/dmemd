package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
// memory, including whether each endpoint moved past decision time.
func (s *Service) Relations(ctx context.Context, id string) ([]Relation, error) {
	if _, err := s.reader.GetMemory(ctx, id); err != nil {
		return nil, err
	}
	return s.reader.ListRelationsForMemory(ctx, id)
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
	status, err := NormalizeAssertionStatus(input.AssertionStatus)
	if err != nil {
		return Memory{}, err
	}
	timeRange, err := NormalizeTimeRange(input.ValidFrom, input.ValidTo, input.ValidFromBasis, input.ValidToBasis)
	if err != nil {
		return Memory{}, err
	}
	item := Memory{
		ID:              idgen.NewUUID(),
		Kind:            kind,
		Phase:           normalizePhase(input.Phase),
		Durability:      normalizeDurability(input.Durability),
		Title:           input.Title,
		Content:         input.Content,
		State:           StateActive,
		AssertionStatus: status,
		Version:         1,
		ObservedAt:      observedAt,
		ValidFrom:       timeRange.From,
		ValidTo:         timeRange.To,
		ValidFromBasis:  timeRange.FromBasis,
		ValidToBasis:    timeRange.ToBasis,
		FactGroupID:     input.FactGroupID,
		CreatedAt:       now,
		UpdatedAt:       now,
		CreatedByApp:    input.CreatedByApp,
		OriginSourceID:  input.OriginSourceID,
		ScopeKey:        NormalizeScopeKey(input.ScopeKey),
		Topics:          CanonicalTopics(rawTopics(input.Topics)),
		RawTopics:       rawTopics(input.Topics),
	}
	if err := validateFactGroupReference(ctx, tx, item.FactGroupID); err != nil {
		return Memory{}, err
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
	if err := s.syncConflictReviews(ctx, tx, []string{item.ID}); err != nil {
		return Memory{}, err
	}
	return item, nil
}

// Revise creates a new content, time, assertion, group or evidence revision from
// the persisted current snapshot. The caller never supplies a version
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
		if input.Phase != "" {
			candidate.Phase = normalizePhase(input.Phase)
		}
		if input.Durability != "" {
			candidate.Durability = normalizeDurability(input.Durability)
		}
		candidate.Title = input.Title
		candidate.Content = input.Content
		candidate.ObservedAt = normalizeObservedAt(input.ObservedAt, current.ObservedAt)
		if input.ValidFrom != nil || input.ValidTo != nil || input.ValidFromBasis != "" ||
			input.ValidToBasis != "" {
			timeRange, err := NormalizeTimeRange(input.ValidFrom, input.ValidTo, input.ValidFromBasis, input.ValidToBasis)
			if err != nil {
				return Memory{}, err
			}
			candidate.ValidFrom = timeRange.From
			candidate.ValidTo = timeRange.To
			candidate.ValidFromBasis = timeRange.FromBasis
			candidate.ValidToBasis = timeRange.ToBasis
		}
		if input.AssertionStatus != "" {
			status, err := NormalizeAssertionStatus(input.AssertionStatus)
			if err != nil {
				return Memory{}, err
			}
			candidate.AssertionStatus = status
		}
		if input.FactGroupID != "" {
			if err := validateFactGroupReference(ctx, tx, input.FactGroupID); err != nil {
				return Memory{}, err
			}
			candidate.FactGroupID = input.FactGroupID
		}
		if scope := NormalizeScopeKey(input.ScopeKey); scope != "" {
			candidate.ScopeKey = scope
		}
		if input.Topics != nil {
			candidate.Topics = CanonicalTopics(rawTopics(input.Topics))
			candidate.RawTopics = rawTopics(input.Topics)
		}
	}

	// Content, kind, title or scope changes make the previous labels a claim
	// about different evidence. When extraction supplied no new labels, clear
	// them so the classify task is a real re-evaluation rather than a no-op.
	classificationInputChanged := candidate.Kind != current.Kind ||
		candidate.Title != current.Title ||
		candidate.Content != current.Content ||
		candidate.ScopeKey != current.ScopeKey
	if classificationInputChanged && input.Operation != OperationAddEvidence && input.Topics == nil {
		candidate.Topics = []string{}
		candidate.RawTopics = []string{}
	}

	contentUnchanged := candidate.Kind == current.Kind &&
		candidate.Phase == current.Phase &&
		candidate.Durability == current.Durability &&
		candidate.Title == current.Title &&
		candidate.Content == current.Content &&
		candidate.ObservedAt.Equal(current.ObservedAt) &&
		sameTimeRange(candidate, current) &&
		candidate.AssertionStatus == current.AssertionStatus &&
		candidate.FactGroupID == current.FactGroupID &&
		candidate.ScopeKey == current.ScopeKey &&
		SameTopics(candidate.Topics, current.Topics) &&
		sameRawTopics(candidate.RawTopics, current.RawTopics)

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
	if err := s.syncConflictReviews(ctx, tx, []string{candidate.ID}); err != nil {
		return Memory{}, err
	}
	return candidate, nil
}

func normalizePhase(value Phase) Phase {
	if value == "" {
		return PhaseUnknown
	}
	switch value {
	case PhaseUnknown, PhaseScheduled, PhaseInProgress, PhaseCompleted, PhaseCancelled, PhasePostponed:
		return value
	default:
		return PhaseUnknown
	}
}

func normalizeDurability(value Durability) Durability {
	if value == "" {
		return DurabilityUnknown
	}
	switch value {
	case DurabilityUnknown, DurabilityDurable, DurabilityEpisodic, DurabilityTemporary:
		return value
	default:
		return DurabilityUnknown
	}
}

// Classify applies a classification result without registering another
// classify task. Classification-only revisions are never an input change for
// themselves; aggregation tasks consume the new labels.
func (s *Service) Classify(ctx context.Context, tx Tx, input ClassificationInput) (Memory, error) {
	current, err := tx.GetMemory(ctx, input.MemoryID)
	if err != nil {
		return Memory{}, err
	}
	candidate := current
	if scope := NormalizeScopeKey(input.ScopeKey); scope != "" {
		candidate.ScopeKey = scope
	}
	candidate.Topics = CanonicalTopics(rawTopics(input.Topics))
	candidate.RawTopics = rawTopics(input.Topics)
	if candidate.ScopeKey == current.ScopeKey && SameTopics(candidate.Topics, current.Topics) && sameRawTopics(candidate.RawTopics, current.RawTopics) {
		return current, nil
	}
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
		Kind:          task.KindClassify,
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
	parts := []string{
		string(item.Kind),
		item.Title,
		item.Content,
		item.ScopeKey,
		strings.Join(NormalizeTopics(item.Topics), "\x00"),
		strings.Join(item.RawTopics, "\x00"),
		item.OriginSourceID,
	}
	return task.InputHash(parts...)
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

func normalizeObservedAt(value, fallback time.Time) time.Time {
	if value.IsZero() {
		value = fallback
	}
	return value.UTC()
}

func sameTimeRange(left, right Memory) bool {
	return equalTimePointer(left.ValidFrom, right.ValidFrom) &&
		equalTimePointer(left.ValidTo, right.ValidTo) &&
		left.ValidFromBasis == right.ValidFromBasis &&
		left.ValidToBasis == right.ValidToBasis
}

func equalTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func validateFactGroupReference(ctx context.Context, tx Tx, groupID string) error {
	if groupID == "" {
		return nil
	}
	if _, err := tx.GetFactGroup(ctx, groupID); err != nil {
		return fmt.Errorf("事实组 %s 不存在: %w", groupID, err)
	}
	return nil
}
