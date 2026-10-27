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
	detail := Detail{Artifact: item}
	if item.CurrentVersion <= 0 {
		return detail, nil
	}
	revision, err := s.backend.GetArtifactRevision(ctx, id, item.CurrentVersion)
	if err != nil {
		return Detail{}, err
	}
	detail.Revision = &revision
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
	return Detail{Artifact: item, Revision: &revision}, nil
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
	return Detail{Artifact: item}, revisions, nil
}

// Search returns active current derived versions ordered by the derived FTS
// query.
func (s *Service) Search(ctx context.Context, query SearchQuery) ([]SearchHit, error) {
	if query.Limit <= 0 {
		return []SearchHit{}, nil
	}
	return s.backend.SearchArtifacts(ctx, query)
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

// ListAggregationGroups returns distinct scope/topic groups for missing-work
// reconciliation.
func (s *Service) ListAggregationGroups(ctx context.Context, after string, limit int) ([]GroupTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.backend.ListAggregationGroups(ctx, after, limit)
}

// ListSkillGroups returns distinct scope/topic groups that have an eligible
// procedure input. It feeds the bounded reconciliation of missing skills.
func (s *Service) ListSkillGroups(ctx context.Context, after string, limit int) ([]GroupTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.backend.ListSkillGroups(ctx, after, limit)
}

// Commit saves one usable generated revision, its direct sources, the current
// pointer and the derived FTS row in the caller's transaction.
func (s *Service) Commit(ctx context.Context, tx Tx, input CommitInput) (Artifact, error) {
	if !input.Kind.Known() {
		return Artifact{}, fmt.Errorf("%w: kind %q", ErrInvalidInput, input.Kind)
	}
	if err := validateCommitTarget(input); err != nil {
		return Artifact{}, err
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return Artifact{}, fmt.Errorf("%w: empty content", ErrInvalidInput)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = defaultTitle(input)
	}
	now := s.now().UTC()
	item, err := tx.GetArtifactByTarget(ctx, input.Kind, input.TargetKey)
	switch {
	case errors.Is(err, ErrNotFound):
		item = Artifact{
			ID:        idgen.NewUUID(),
			Kind:      input.Kind,
			TargetKey: input.TargetKey,
			ScopeKey:  normalizeScope(input.ScopeKey),
			Topic:     input.Topic,
			State:     StateDisabled,
			UpdatedAt: now,
		}
		if err := tx.CreateArtifact(ctx, item); err != nil {
			return Artifact{}, err
		}
	case err != nil:
		return Artifact{}, err
	}

	next := item.CurrentVersion + 1
	payload := input.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	refs := uniqueRefs(input.Memories)
	runRefs := uniqueRunRefs(input.Runs)
	if input.TaxonomyVersion == "" {
		input.TaxonomyVersion = topic.Version
	}
	if input.GeneratorVersion == "" {
		input.GeneratorVersion = "generator-v2"
	}
	if input.Generator == "" {
		input.Generator = "artifact:service"
	}
	if input.ProtocolVersion == "" {
		input.ProtocolVersion = string(input.Kind) + "-v2"
	}
	if input.RunID == "" {
		input.RunID = idgen.NewUUID()
	}
	provenance, err := json.Marshal(Provenance{
		MemoryRefs: refs, RunRefs: runRefs, RunID: input.RunID,
		TaxonomyVersion: input.TaxonomyVersion, GeneratorVersion: input.GeneratorVersion,
		ProtocolVersion: input.ProtocolVersion, Truncated: input.Truncated,
	})
	if err != nil || !json.Valid(payload) {
		return Artifact{}, fmt.Errorf("%w: invalid serialized payload", ErrInvalidInput)
	}
	revision := Revision{
		ArtifactID:       item.ID,
		Version:          next,
		Title:            title,
		Content:          content,
		Payload:          payload,
		ContentHash:      ContentHash(title, content, payload, provenance),
		Generator:        input.Generator,
		ProtocolVersion:  input.ProtocolVersion,
		TaxonomyVersion:  input.TaxonomyVersion,
		GeneratorVersion: input.GeneratorVersion,
		RunID:            input.RunID,
		Provenance:       provenance,
		ReasonCode:       input.ReasonCode,
		Truncated:        input.Truncated,
		MemoryRefs:       refs,
		RunRefs:          runRefs,
		CreatedAt:        now,
	}
	if err := tx.InsertArtifactRevision(ctx, revision); err != nil {
		return Artifact{}, err
	}
	if err := tx.LinkArtifactMemoryDependencies(ctx, item.ID, next, refs); err != nil {
		return Artifact{}, err
	}
	if err := tx.LinkArtifactRunDependencies(ctx, item.ID, next, runRefs); err != nil {
		return Artifact{}, err
	}
	document, err := s.buildDocument(DocumentInput{
		ArtifactID: item.ID,
		Version:    next,
		Title:      title,
		Content:    content,
	})
	if err != nil {
		return Artifact{}, err
	}
	if err := tx.UpsertArtifactSearchDocument(ctx, document); err != nil {
		return Artifact{}, err
	}

	item.CurrentVersion = next
	item.State = StateActive
	item.ReasonCode = input.ReasonCode
	item.UpdatedAt = now
	if err := tx.UpdateArtifact(ctx, item); err != nil {
		return Artifact{}, err
	}
	return item, nil
}

// Disable records that no usable input currently exists. The historical
// revisions remain readable, but the current object leaves default recall.
func (s *Service) Disable(ctx context.Context, tx Tx, target Target) (Artifact, error) {
	return s.DisableWithReason(ctx, tx, target, "no_eligible_input")
}

// DisableWithReason exposes diagnostic state even before a first revision exists.
func (s *Service) DisableWithReason(ctx context.Context, tx Tx, target Target, reason string) (Artifact, error) {
	now := s.now().UTC()
	item, err := tx.GetArtifactByTarget(ctx, target.Kind, target.TargetKey())
	switch {
	case errors.Is(err, ErrNotFound):
		item = Artifact{
			ID:         idgen.NewUUID(),
			Kind:       target.Kind,
			TargetKey:  target.TargetKey(),
			ScopeKey:   normalizeScope(target.ScopeKey),
			Topic:      target.Topic,
			State:      StateDisabled,
			ReasonCode: reason,
			UpdatedAt:  now,
		}
		if err := tx.CreateArtifact(ctx, item); err != nil {
			return Artifact{}, err
		}
		return item, nil
	case err != nil:
		return Artifact{}, err
	}
	if item.State != StateDisabled || item.ReasonCode != reason {
		item.State = StateDisabled
		item.ReasonCode = reason
		item.UpdatedAt = now
		if err := tx.UpdateArtifact(ctx, item); err != nil {
			return Artifact{}, err
		}
	}
	if err := tx.DeleteArtifactSearchDocument(ctx, item.ID); err != nil {
		return Artifact{}, err
	}
	return item, nil
}

// MemoryChanged applies synchronous invalidation and registers derived work in
// the same transaction as the atomic write. Only direct dependencies are
// consulted because the first release has no artifact-to-artifact edges.
func (s *Service) MemoryChanged(ctx context.Context, tx Tx, change MemoryChange) error {
	if !change.Semantic {
		return nil
	}
	now := s.now().UTC()
	affected, err := tx.ListArtifactsForMemory(ctx, change.MemoryID)
	if err != nil {
		return err
	}
	targets := make(map[string]Target)
	for _, item := range affected {
		target, err := TargetForArtifact(item)
		if err != nil {
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
	for _, snapshot := range []*MemorySnapshot{change.Old, change.New} {
		if snapshot == nil {
			continue
		}
		for _, target := range AggregateTargets(snapshot.ScopeKey, snapshot.Topics) {
			targets[target.TargetKey()] = target
		}
		if snapshot.Kind == "procedure" {
			for _, target := range SkillTargets(snapshot.ScopeKey, snapshot.Topics) {
				targets[target.TargetKey()] = target
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	// A new eligible memory may not have been a dependency of an existing
	// summary (especially a truncated one). Target membership still invalidates
	// it immediately, including old and new parent topics.
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
// own handler; this hook keeps skills from lagging behind a new counterexample.
func (s *Service) ExperienceChanged(ctx context.Context, tx Tx, scopeKey string, topics []string,
	experienceID, reason string, nextAttempt time.Time) error {
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
	existingByTarget := make(map[string]Artifact, len(existing))
	for _, item := range existing {
		existingByTarget[item.TargetKey] = item
	}
	// Candidate topic skills are registered so a new experience can create a
	// skill that does not exist yet. Existing stale work is left to its pending
	// task: its input selection reads recorded runs directly, so case generation
	// must not bump the same target a second time.
	for _, target := range SkillTargets(scopeKey, topics) {
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
		if item.State == StateStale {
			continue
		}
		target, err := TargetForArtifact(item)
		if err != nil {
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

func artifactHasRun(ctx context.Context, tx Tx, item Artifact, experienceID string) bool {
	if item.CurrentVersion <= 0 {
		return false
	}
	revision, err := tx.GetArtifactRevision(ctx, item.ID, item.CurrentVersion)
	if err != nil {
		return false
	}
	for _, ref := range revision.RunRefs {
		if ref.ExperienceID == experienceID {
			return true
		}
	}
	return false
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
		kind := task.KindAggregate
		if target.Kind == KindSkill {
			kind = task.KindBuildSkill
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

func validateCommitTarget(input CommitInput) error {
	if strings.TrimSpace(input.TargetKey) == "" {
		return fmt.Errorf("%w: missing target", ErrInvalidInput)
	}
	switch input.Kind {
	case KindTopicSummary, KindProfile:
		if strings.TrimSpace(input.ScopeKey) == "" {
			return fmt.Errorf("%w: missing scope", ErrInvalidInput)
		}
	case KindSkill:
		if strings.TrimSpace(input.ScopeKey) == "" {
			return fmt.Errorf("%w: missing scope", ErrInvalidInput)
		}
	case KindExperienceCase:
		if input.ExperienceID == "" {
			experienceID, ok := task.ParseExperienceKey(input.TargetKey)
			if !ok {
				return fmt.Errorf("%w: invalid experience target", ErrInvalidInput)
			}
			input.ExperienceID = experienceID
		}
	default:
		return fmt.Errorf("%w: kind %q", ErrInvalidInput, input.Kind)
	}
	expected := Target{
		Kind:         input.Kind,
		ScopeKey:     input.ScopeKey,
		Topic:        input.Topic,
		ExperienceID: input.ExperienceID,
	}.TargetKey()
	if input.TargetKey != expected {
		return fmt.Errorf("%w: target key does not match scope/topic", ErrInvalidInput)
	}
	return nil
}

func uniqueRefs(inputs []MemoryInput) []MemoryRef {
	seen := make(map[string]struct{}, len(inputs))
	result := make([]MemoryRef, 0, len(inputs))
	for _, item := range inputs {
		if item.ID == "" || item.Version <= 0 {
			continue
		}
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, MemoryRef{MemoryID: item.ID, Version: item.Version})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].MemoryID < result[j].MemoryID })
	return result
}

func uniqueRunRefs(inputs []RunRef) []RunRef {
	seen := make(map[string]struct{}, len(inputs))
	result := make([]RunRef, 0, len(inputs))
	for _, item := range inputs {
		if item.ExperienceID == "" {
			continue
		}
		if _, duplicate := seen[item.ExperienceID]; duplicate {
			continue
		}
		seen[item.ExperienceID] = struct{}{}
		result = append(result, RunRef{ExperienceID: item.ExperienceID})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ExperienceID < result[j].ExperienceID })
	return result
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
			strings.Join(normalizeTopics(snapshot.Topics), "\x00"),
		)
	}
	appendSnapshot(change.Old)
	appendSnapshot(change.New)
	return task.InputHash(parts...)
}

// ContentHash covers readable and structured content, including source audit.
func ContentHash(title, content string, payload, provenance json.RawMessage) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(title))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(content))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(provenance)
	return hex.EncodeToString(hash.Sum(nil))
}

func defaultTitle(input CommitInput) string {
	switch input.Kind {
	case KindProfile:
		return input.ScopeKey + " 画像"
	case KindExperienceCase:
		return "经验 " + input.ExperienceID
	case KindSkill:
		if input.Topic == "" {
			return input.ScopeKey + " 工作流"
		}
		return input.Topic + " 工作流"
	default:
		return input.Topic + " 摘要"
	}
}

func normalizeScope(value string) string {
	return strings.TrimSpace(value)
}

func normalizeTopics(values []string) []string {
	return topic.Canonical(values)
}
