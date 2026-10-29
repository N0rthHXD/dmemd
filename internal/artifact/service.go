package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/topic"
)

const (
	defaultDebounce = 30 * time.Second
	defaultHistory  = 50
)

// Service implements derived artifact reads, commit, synchronous invalidation
// and target reconciliation. Model work never happens here.
type Service struct {
	backend   Backend
	documents DocumentBuilder
	now       func() time.Time
	debounce  time.Duration
	logger    *slog.Logger
}

// Option customizes a Service for runtime and deterministic tests.
type Option func(*Service)

// WithClock overrides the service clock. Values are normalized to UTC.
func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

// WithDebounce sets the ordinary aggregation delay. Deletion and purge are
// always immediate.
func WithDebounce(delay time.Duration) Option {
	return func(service *Service) {
		if delay < 0 {
			delay = 0
		}
		service.debounce = delay
	}
}

// WithLogger installs the structured logger. It never receives artifact text.
func WithLogger(logger *slog.Logger) Option {
	return func(service *Service) {
		if logger != nil {
			service.logger = logger
		}
	}
}

// NewService assembles the artifact service. The backend implements both the
// read pool capabilities and reconciliation scans.
func NewService(backend Backend, documents DocumentBuilder, options ...Option) *Service {
	service := &Service{
		backend:   backend,
		documents: documents,
		now:       time.Now,
		debounce:  defaultDebounce,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

// Get returns one artifact and its current revision. A disabled or never
// generated object has no revision content.
func (s *Service) Get(ctx context.Context, id string) (Detail, error) {
	item, err := s.backend.GetArtifact(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{DerivedMemory: item}
	if item.CurrentVersion <= 0 {
		return detail, nil
	}
	revision, err := s.backend.GetArtifactRevision(ctx, id, item.CurrentVersion)
	if err != nil {
		return Detail{}, err
	}
	dependencies, err := s.backend.ListArtifactDependencies(ctx, id, item.CurrentVersion)
	if err != nil {
		return Detail{}, err
	}
	detail.Revision = &revision
	detail.Dependencies = dependencies
	return detail, nil
}

// GetVersion returns one historical revision and its owning artifact state.
func (s *Service) GetVersion(ctx context.Context, id string, version int) (Detail, error) {
	item, err := s.backend.GetArtifact(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if version <= 0 {
		return Detail{}, fmt.Errorf("%w: version %d", ErrNotFound, version)
	}
	revision, err := s.backend.GetArtifactRevision(ctx, id, version)
	if err != nil {
		return Detail{}, err
	}
	dependencies, err := s.backend.ListArtifactDependencies(ctx, id, version)
	if err != nil {
		return Detail{}, err
	}
	return Detail{DerivedMemory: item, Revision: &revision, Dependencies: dependencies}, nil
}

// ListVersions returns newest-first immutable history.
func (s *Service) ListVersions(ctx context.Context, id string, limit int) (Detail, []Revision, error) {
	item, err := s.backend.GetArtifact(ctx, id)
	if err != nil {
		return Detail{}, nil, err
	}
	if limit <= 0 {
		limit = defaultHistory
	}
	revisions, err := s.backend.ListArtifactRevisions(ctx, id, limit)
	if err != nil {
		return Detail{}, nil, err
	}
	return Detail{DerivedMemory: item}, revisions, nil
}

// Search returns active current derived versions ordered by the derived FTS
// query.
func (s *Service) Search(ctx context.Context, query SearchQuery) ([]SearchHit, error) {
	if query.Limit <= 0 {
		return []SearchHit{}, nil
	}
	return s.backend.SearchArtifacts(ctx, query)
}

// List returns a bounded keyset page of current derived objects.
func (s *Service) List(ctx context.Context, query ListQuery) (ListPage, error) {
	if query.Limit <= 0 {
		query.Limit = 50
	}
	return s.backend.ListArtifacts(ctx, query)
}

// ListTargets returns a bounded page that also includes evaluated targets with
// no artifact yet, so callers can distinguish "not eligible" from failure.
func (s *Service) ListTargets(ctx context.Context, query TargetQuery) (TargetPage, error) {
	if query.Limit <= 0 {
		query.Limit = 50
	}
	return s.backend.ListTargets(ctx, query)
}

// ListStaleTargets returns stale targets for bounded reconciliation.
func (s *Service) ListStaleTargets(ctx context.Context, after string, limit int) ([]Target, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.backend.ListStaleTargets(ctx, after, limit)
}

// ListExpiredTargets returns active artifacts whose current dependencies are no
// longer accepted, active or time-applicable.
func (s *Service) ListExpiredTargets(ctx context.Context, asOf time.Time, after string, limit int) ([]Target, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.backend.ListExpiredTargets(ctx, asOf.UTC(), after, limit)
}

// Commit saves one usable generated revision, its exact typed inputs, the
// current pointer and the derived FTS row in the caller's transaction.
func (s *Service) Commit(ctx context.Context, tx Tx, input CommitInput) (DerivedMemory, error) {
	target := input.Target
	if !target.Kind.Known() {
		return DerivedMemory{}, fmt.Errorf("%w: kind %q", ErrInvalidInput, target.Kind)
	}
	if err := validateCommitTarget(target); err != nil {
		return DerivedMemory{}, err
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return DerivedMemory{}, fmt.Errorf("%w: empty content", ErrInvalidInput)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = defaultTitle(target)
	}
	now := s.now().UTC()
	targetKey := target.TargetKey()
	item, err := tx.GetArtifactByTarget(ctx, target.Kind, targetKey)
	switch {
	case errors.Is(err, ErrNotFound):
		item = DerivedMemory{
			ID:        idgen.NewUUID(),
			Layer:     target.LayerOrDefault(),
			Kind:      target.Kind,
			TargetKey: targetKey,
			ScopeKey:  normalizeScope(target.ScopeKey),
			Topic:     target.Topic,
			State:     StateDisabled,
			UpdatedAt: now,
		}
		if err := tx.CreateArtifact(ctx, item); err != nil {
			return DerivedMemory{}, err
		}
	case err != nil:
		return DerivedMemory{}, err
	}

	next := item.CurrentVersion + 1
	payload := input.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if input.Generator == "" {
		input.Generator = "artifact:service"
	}
	if input.ProtocolVersion == "" {
		input.ProtocolVersion = string(target.Kind) + "-v2"
	}
	if !json.Valid(payload) {
		return DerivedMemory{}, fmt.Errorf("%w: invalid serialized payload", ErrInvalidInput)
	}
	revision := Revision{
		ArtifactID:      item.ID,
		Version:         next,
		Title:           title,
		Content:         content,
		Payload:         payload,
		ContentHash:     ContentHash(title, content, payload),
		Generator:       input.Generator,
		ProtocolVersion: input.ProtocolVersion,
		Truncated:       input.Truncated,
		CreatedAt:       now,
	}
	if err := tx.InsertArtifactRevision(ctx, revision); err != nil {
		return DerivedMemory{}, err
	}
	if err := tx.LinkDerivedDependencies(ctx, item.ID, next, input.Dependencies); err != nil {
		return DerivedMemory{}, err
	}
	document, err := s.buildDocument(DocumentInput{
		ArtifactID: item.ID,
		Version:    next,
		Title:      title,
		Content:    content,
	})
	if err != nil {
		return DerivedMemory{}, err
	}
	if err := tx.UpsertArtifactSearchDocument(ctx, document); err != nil {
		return DerivedMemory{}, err
	}

	item.Layer = target.LayerOrDefault()
	item.CurrentVersion = next
	item.State = StateActive
	item.ReasonCode = input.ReasonCode
	item.ScopeKey = normalizeScope(target.ScopeKey)
	item.Topic = target.Topic
	item.UpdatedAt = now
	if err := tx.UpdateArtifact(ctx, item); err != nil {
		return DerivedMemory{}, err
	}
	if err := tx.DeleteTargetEvaluation(ctx, target.Kind, targetKey); err != nil {
		return DerivedMemory{}, err
	}
	return item, nil
}

// Disable records that no usable input currently exists. The historical
// revisions remain readable, but the current object leaves default recall.
func (s *Service) Disable(ctx context.Context, tx Tx, target Target) (DerivedMemory, error) {
	return s.DisableWithReason(ctx, tx, target, "no_eligible_input")
}

// DisableWithReason exposes diagnostic state even before a first revision exists.
func (s *Service) DisableWithReason(ctx context.Context, tx Tx, target Target, reason string) (DerivedMemory, error) {
	now := s.now().UTC()
	item, err := tx.GetArtifactByTarget(ctx, target.Kind, target.TargetKey())
	switch {
	case errors.Is(err, ErrNotFound):
		item = DerivedMemory{
			ID:         idgen.NewUUID(),
			Layer:      target.LayerOrDefault(),
			Kind:       target.Kind,
			TargetKey:  target.TargetKey(),
			ScopeKey:   normalizeScope(target.ScopeKey),
			Topic:      target.Topic,
			State:      StateDisabled,
			ReasonCode: reason,
			UpdatedAt:  now,
		}
		if err := tx.CreateArtifact(ctx, item); err != nil {
			return DerivedMemory{}, err
		}
		return item, nil
	case err != nil:
		return DerivedMemory{}, err
	}
	if item.State != StateDisabled || item.ReasonCode != reason {
		item.State = StateDisabled
		item.ReasonCode = reason
		item.UpdatedAt = now
		if err := tx.UpdateArtifact(ctx, item); err != nil {
			return DerivedMemory{}, err
		}
	}
	if err := tx.DeleteArtifactSearchDocument(ctx, item.ID); err != nil {
		return DerivedMemory{}, err
	}
	return item, nil
}

// DisableExisting updates an existing content object to disabled without
// manufacturing an empty artifact for a target that never had a usable
// revision. The boolean reports whether the object existed.
func (s *Service) DisableExisting(ctx context.Context, tx Tx, target Target, reason string) (DerivedMemory, bool, error) {
	now := s.now().UTC()
	item, err := tx.GetArtifactByTarget(ctx, target.Kind, target.TargetKey())
	if errors.Is(err, ErrNotFound) {
		return DerivedMemory{}, false, nil
	}
	if err != nil {
		return DerivedMemory{}, false, err
	}
	if item.State != StateDisabled || item.ReasonCode != reason {
		item.State = StateDisabled
		item.ReasonCode = reason
		item.UpdatedAt = now
		if err := tx.UpdateArtifact(ctx, item); err != nil {
			return DerivedMemory{}, false, err
		}
	}
	if err := tx.DeleteArtifactSearchDocument(ctx, item.ID); err != nil {
		return DerivedMemory{}, false, err
	}
	return item, true, nil
}

// MemoryChanged applies synchronous invalidation and registers derived work in
// the same transaction as the atomic write. Exact dependency edges are
// supplemented by procedure-derived L4 guidance targets.
func (s *Service) MemoryChanged(ctx context.Context, tx Tx, change MemoryChange) error {
	if !change.Semantic {
		return nil
	}
	now := s.now().UTC()
	dependents, err := tx.ListDependents(ctx, DependencyRef{
		Type: DependencyMemory, ID: change.MemoryID,
	})
	if err != nil {
		return err
	}
	targets := make(map[string]Target)
	for _, dependent := range dependents {
		if dependent.TargetKey == "" {
			continue
		}
		target, err := ParseTarget(dependent.Kind, dependent.TargetKey)
		if err != nil {
			continue
		}
		if target.Layer == "" {
			target.Layer = dependent.Layer
		}
		if target.ScopeKey == "" {
			target.ScopeKey = dependent.ScopeKey
		}
		if target.Topic == "" {
			target.Topic = dependent.Topic
		}
		targets[dependent.TargetKey] = target
		if dependent.State != StateActive {
			continue
		}
		if err := tx.MarkDerivedStale(ctx, dependent.ID, change.Reason); err != nil {
			return err
		}
	}
	for _, snapshot := range []*MemorySnapshot{change.Old, change.New} {
		if snapshot == nil || snapshot.Kind != "procedure" {
			continue
		}
		// Stable procedure memories feed L4 guidance. L2/L3 are rebuilt from
		// their own lower derived layers, never directly from canonical memory.
		for _, target := range SkillTargets(snapshot.ScopeKey, nil) {
			targets[target.TargetKey()] = target
		}
	}
	if len(targets) == 0 {
		return nil
	}
	// A new eligible memory may not have been a dependency of an existing
	// derived object. Target membership still invalidates it immediately.
	for _, target := range targets {
		item, err := tx.GetArtifactByTarget(ctx, target.Kind, target.TargetKey())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if item.State == StateStale {
			continue
		}
		item.State, item.UpdatedAt = StateStale, now
		if err := tx.UpdateArtifact(ctx, item); err != nil {
			return err
		}
		if err := tx.DeleteArtifactSearchDocument(ctx, item.ID); err != nil {
			return err
		}
	}

	deleted := change.New == nil || change.New.State == "deleted"
	next := now.Add(s.debounce)
	if deleted {
		next = now
	}
	return enqueueTargets(ctx, tx, change, targets, next, now)
}

// ExperienceChanged queues the skill targets that can consume a new, changed or
// retracted application result. The case itself is already invalidated by its
// own handler; this hook keeps related skills from lagging behind a new
// counterexample. Only the same workflow topic or an explicit skill reference
// is related; sharing a scope alone is insufficient.
func (s *Service) ExperienceChanged(ctx context.Context, tx Tx, scopeKey string, topics []string,
	skillID, experienceID, reason string, nextAttempt time.Time) error {
	scopeKey = normalizeScope(scopeKey)
	if scopeKey == "" {
		return nil
	}
	now := s.now().UTC()
	targets := make(map[string]Target)
	existing, err := tx.ListArtifactsByScope(ctx, KindSkill, scopeKey)
	if err != nil {
		return err
	}
	existingByTarget := make(map[string]DerivedMemory, len(existing))
	for _, item := range existing {
		existingByTarget[item.TargetKey] = item
	}
	relatedTopics := map[string]bool{}
	for _, topic := range normalizeTopics(topics) {
		if topic != "" {
			relatedTopics[topic] = true
		}
	}
	workflowTopics := len(relatedTopics) == 1
	// Candidate topic skills are registered so a new experience can create a
	// skill that does not exist yet. Existing stale work is left to its pending
	// task: its input selection reads recorded runs directly, so case generation
	// must not bump the same target a second time.
	for _, target := range SkillTargets(scopeKey, topics) {
		if !workflowTopics && len(relatedTopics) > 0 {
			// An experience spanning several workflows is not enough evidence
			// to attach itself to a new skill target.
			break
		}
		if target.Topic == "" && len(existing) > 0 {
			// A scope-level fallback skill is only useful when the scope has no
			// topic-specific guidance to consume the experience.
			continue
		}
		item, found := existingByTarget[target.TargetKey()]
		if found && item.State != StateDisabled {
			continue
		}
		targets[target.TargetKey()] = target
	}
	for _, item := range existing {
		target, err := TargetForArtifact(item)
		if err != nil {
			continue
		}
		explicit := skillID != "" && item.ID == skillID
		sameWorkflow := workflowTopics && target.Topic != "" && relatedTopics[target.Topic]
		scopeFallback := target.Topic == "" && len(relatedTopics) == 0
		if !explicit && !sameWorkflow && !scopeFallback {
			continue
		}
		if item.State == StateActive && experienceID != "" && artifactHasRun(ctx, tx, item, experienceID) {
			continue
		}
		targets[item.TargetKey] = target
		if item.State != StateActive {
			continue
		}
		item.State = StateStale
		item.UpdatedAt = now
		if err := tx.UpdateArtifact(ctx, item); err != nil {
			return err
		}
		if err := tx.DeleteArtifactSearchDocument(ctx, item.ID); err != nil {
			return err
		}
	}
	if len(targets) == 0 {
		return nil
	}
	change := MemoryChange{Reason: reason}
	return enqueueTargets(ctx, tx, change, targets, nextAttempt, now)
}

func artifactHasRun(ctx context.Context, tx Tx, item DerivedMemory, experienceID string) bool {
	if item.CurrentVersion <= 0 || experienceID == "" {
		return false
	}
	items, err := tx.ListArtifactsForRun(ctx, experienceID)
	if err != nil {
		return false
	}
	for _, dependent := range items {
		if dependent.ID == item.ID {
			return true
		}
	}
	return false
}

func aggregationJobKind(target Target) (task.Kind, bool) {
	if target.LayerOrDefault() == LayerL4 {
		return task.KindBuildGuidance, true
	}
	return task.AggregateKindForTargetKey(target.TargetKey())
}

func enqueueTargets(ctx context.Context, tx Tx, change MemoryChange, targets map[string]Target,
	nextAttempt, now time.Time) error {
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		target := targets[key]
		kind, ok := aggregationJobKind(target)
		if !ok {
			continue
		}
		if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
			Kind:          kind,
			TargetKey:     target.TargetKey(),
			InputHash:     memoryChangeHash(change, target),
			NextAttemptAt: &nextAttempt,
			Now:           now,
		}); err != nil {
			return fmt.Errorf("登记派生任务 %s: %w", target.TargetKey(), err)
		}
	}
	return nil
}

// MemoryPurged invalidates and removes every artifact that directly references
// the memory, before the caller deletes the atomic revisions. The whole
// operation is one transaction; a failure leaves both sides intact.
func (s *Service) MemoryPurged(ctx context.Context, tx Tx, change MemoryChange) error {
	affected, err := tx.ListAllArtifactsForMemory(ctx, change.MemoryID)
	if err != nil {
		return err
	}
	if err := s.MemoryChanged(ctx, tx, change); err != nil {
		return err
	}
	// Historical dependencies may belong to targets no longer represented by
	// the memory's current topics. Bump those jobs too, rejecting late results
	// and rebuilding only from the remaining inputs.
	targets := make(map[string]Target)
	for _, item := range affected {
		target, err := TargetForArtifact(item)
		if err != nil {
			return err
		}
		targets[target.TargetKey()] = target
	}
	now := s.now().UTC()
	if err := enqueueTargets(ctx, tx, change, targets, now, now); err != nil {
		return err
	}
	return tx.DeleteArtifactsForMemory(ctx, change.MemoryID)
}

func (s *Service) buildDocument(input DocumentInput) (SearchDocument, error) {
	if s.documents != nil {
		document, err := s.documents.BuildArtifactDocument(input)
		if err != nil {
			return SearchDocument{}, fmt.Errorf("构造派生文档 %s: %w", input.ArtifactID, err)
		}
		if document.ArtifactID == "" {
			document.ArtifactID = input.ArtifactID
		}
		if document.Version == 0 {
			document.Version = input.Version
		}
		return document, nil
	}
	return SearchDocument{
		ArtifactID:   input.ArtifactID,
		Version:      input.Version,
		TitleTerms:   strings.ToLower(strings.TrimSpace(input.Title)),
		ContentTerms: strings.ToLower(strings.TrimSpace(input.Content)),
	}, nil
}

func validateCommitTarget(target Target) error {
	if !target.Kind.Known() {
		return fmt.Errorf("%w: kind %q", ErrInvalidInput, target.Kind)
	}
	layer := target.LayerOrDefault()
	if !layer.Known() {
		return fmt.Errorf("%w: layer %q", ErrInvalidInput, target.Layer)
	}
	if kindLayer := LayerForKind(target.Kind); kindLayer != "" && target.Layer.Known() && kindLayer != layer {
		return fmt.Errorf("%w: kind %q does not belong to layer %q", ErrInvalidInput, target.Kind, layer)
	}
	if strings.TrimSpace(target.ScopeKey) == "" && !(layer == LayerL4 && target.Kind == KindExperienceCase) {
		return fmt.Errorf("%w: missing scope", ErrInvalidInput)
	}
	switch layer {
	case LayerL1:
		bucket := strings.TrimSpace(target.Bucket)
		if bucket == "" {
			bucket = strings.TrimSpace(target.Key)
		}
		if bucket == "" {
			return fmt.Errorf("%w: missing L1 bucket", ErrInvalidInput)
		}
	case LayerL2:
		domain := strings.TrimSpace(target.Topic)
		if domain == "" {
			domain = strings.TrimSpace(target.Key)
		}
		if domain == "" && target.Kind != KindProfile {
			return fmt.Errorf("%w: missing L2 domain", ErrInvalidInput)
		}
	case LayerL3:
	case LayerL4:
		switch target.Kind {
		case KindExperienceCase:
			if target.ExperienceID == "" {
				return fmt.Errorf("%w: missing experience identifier", ErrInvalidInput)
			}
		case KindSkill:
		default:
			if strings.TrimSpace(target.Key) == "" && strings.TrimSpace(target.Topic) == "" {
				return fmt.Errorf("%w: missing L4 guidance key", ErrInvalidInput)
			}
		}
	default:
		return fmt.Errorf("%w: layer %q", ErrInvalidInput, layer)
	}
	return nil
}

func memoryChangeHash(change MemoryChange, target Target) string {
	parts := []string{"artifact-memory-change", target.TargetKey(), change.MemoryID, change.Reason}
	appendSnapshot := func(snapshot *MemorySnapshot) {
		if snapshot == nil {
			parts = append(parts, "nil")
			return
		}
		parts = append(parts,
			snapshot.ID,
			strconv.Itoa(snapshot.Version),
			snapshot.Kind,
			snapshot.ScopeKey,
			snapshot.State,
		)
	}
	appendSnapshot(change.Old)
	appendSnapshot(change.New)
	return task.InputHash(parts...)
}

// ContentHash covers readable and structured content.
func ContentHash(title, content string, payload json.RawMessage) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(title))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(content))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return hex.EncodeToString(hash.Sum(nil))
}

func defaultTitle(target Target) string {
	switch target.Kind {
	case KindLocalSummary:
		if target.Bucket != "" {
			return target.ScopeKey + " " + target.Bucket + " 局部聚合"
		}
		return target.ScopeKey + " 局部聚合"
	case KindScopeSummary:
		return target.ScopeKey + " 概览"
	case KindProfile:
		return target.ScopeKey + " 画像"
	case KindExperienceCase:
		return "经验 " + target.ExperienceID
	case KindSkill:
		if target.Topic == "" {
			return target.ScopeKey + " 工作流"
		}
		return target.Topic + " 工作流"
	default:
		if target.Topic != "" {
			return target.Topic + " 摘要"
		}
		return target.ScopeKey + " 摘要"
	}
}

func normalizeScope(value string) string {
	return strings.TrimSpace(value)
}

func normalizeTopics(values []string) []string {
	return topic.Canonical(values)
}
