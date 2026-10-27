// Package artifact owns derived memory layers: immutable artifact revisions,
// their direct atomic-memory dependencies, current active state and the derived
// full-text index.
//
// Artifacts never depend on other artifacts in the first release. Atomic
// memories and saved experience runs are the only leaves; an artifact revision
// records the exact memory versions it was generated from so invalidation and
// permanent deletion can enumerate the affected set before cascades run.
package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

// Kind is one derived layer object.
type Kind string

const (
	KindTopicSummary   Kind = "topic_summary"
	KindProfile        Kind = "profile"
	KindExperienceCase Kind = "experience_case"
	KindSkill          Kind = "skill"
)

// Known reports whether kind belongs to the fixed first-release set.
func (k Kind) Known() bool {
	switch k {
	case KindTopicSummary, KindProfile, KindExperienceCase, KindSkill:
		return true
	default:
		return false
	}
}

// State controls default recall visibility for the current artifact version.
type State string

const (
	StateActive   State = "active"
	StateStale    State = "stale"
	StateDisabled State = "disabled"
)

// Known reports whether state is a valid persisted artifact state.
func (s State) Known() bool {
	switch s {
	case StateActive, StateStale, StateDisabled:
		return true
	default:
		return false
	}
}

// ErrNotFound is returned for an unknown artifact or revision.
var ErrNotFound = errors.New("artifact not found")

// ErrInvalidInput is returned when a derived object cannot be persisted because
// a required field is missing. Auxiliary malformed references are ignored by
// the caller before this error is produced.
var ErrInvalidInput = errors.New("invalid artifact input")

// Artifact is one current derived object. CurrentVersion is zero only when no
// usable version has ever been generated; history is still retained.
type Artifact struct {
	ID             string    `json:"id"`
	Kind           Kind      `json:"kind"`
	TargetKey      string    `json:"target_key"`
	ScopeKey       string    `json:"scope_key"`
	Topic          string    `json:"topic"`
	CurrentVersion int       `json:"current_version"`
	State          State     `json:"state"`
	ReasonCode     string    `json:"reason_code,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// MemoryRef identifies one exact atomic-memory revision.
type MemoryRef struct {
	MemoryID string `json:"memory_id"`
	Version  int    `json:"version"`
}

// RunRef identifies one recorded application result. The service-internal ID is
// authoritative; run_id supplied by a client is only a deduplication key.
type RunRef struct {
	ExperienceID string `json:"experience_id"`
}

// Revision is one immutable derived snapshot. Structured kinds (experience
// cases and skills) keep their fields in Payload; all kinds also expose
// Title/Content as the readable text used by recall and rendering.
type Revision struct {
	ArtifactID       string          `json:"artifact_id"`
	Version          int             `json:"version"`
	Title            string          `json:"title"`
	Content          string          `json:"content"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	ContentHash      string          `json:"content_hash"`
	Generator        string          `json:"generator"`
	ProtocolVersion  string          `json:"protocol_version"`
	TaxonomyVersion  string          `json:"taxonomy_version"`
	GeneratorVersion string          `json:"generator_version"`
	RunID            string          `json:"run_id"`
	// Provenance is an immutable serialized copy, independent of dependency
	// tables so integrity checking can detect missing/cascaded edges.
	Provenance json.RawMessage `json:"provenance"`
	ReasonCode string          `json:"reason_code,omitempty"`
	Truncated  bool            `json:"truncated"`
	MemoryRefs []MemoryRef     `json:"memory_refs"`
	RunRefs    []RunRef        `json:"run_refs"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Detail is one artifact plus its selected revision. Revision is nil when the
// artifact has never produced a usable version.
type Detail struct {
	Artifact Artifact  `json:"artifact"`
	Revision *Revision `json:"revision,omitempty"`
}

// Target is the precise derived-object target. ScopeKey must be non-empty for
// summaries and profiles; unknown scope values never become global conclusions.
// ExperienceID identifies one feedback run and is only used for experience
// cases.
type Target struct {
	Kind         Kind
	ScopeKey     string
	Topic        string
	ExperienceID string
}

// TargetKey returns the canonical governance-task and artifact target key.
func (t Target) TargetKey() string {
	switch t.Kind {
	case KindProfile:
		return task.ProfileKey(t.ScopeKey)
	case KindSkill:
		return task.SkillKey(t.ScopeKey, t.Topic)
	case KindExperienceCase:
		return task.ExperienceKey(t.ExperienceID)
	default:
		return task.AggregateKey(t.ScopeKey, t.Topic)
	}
}

// ParseTarget decodes one canonical target given its kind. An experience target
// carries only the run identifier; its scope is stored on the artifact itself.
func ParseTarget(kind Kind, targetKey string) (Target, error) {
	switch kind {
	case KindTopicSummary:
		scopeKey, topic, ok := task.ParseAggregateKey(targetKey)
		if !ok {
			return Target{}, fmt.Errorf("%w: aggregate target %q", ErrInvalidInput, targetKey)
		}
		return Target{Kind: kind, ScopeKey: scopeKey, Topic: topic}, nil
	case KindProfile:
		scopeKey, ok := task.ParseProfileKey(targetKey)
		if !ok {
			return Target{}, fmt.Errorf("%w: profile target %q", ErrInvalidInput, targetKey)
		}
		return Target{Kind: kind, ScopeKey: scopeKey}, nil
	case KindSkill:
		scopeKey, topic, ok := task.ParseSkillKey(targetKey)
		if !ok {
			return Target{}, fmt.Errorf("%w: skill target %q", ErrInvalidInput, targetKey)
		}
		return Target{Kind: kind, ScopeKey: scopeKey, Topic: topic}, nil
	case KindExperienceCase:
		experienceID, ok := task.ParseExperienceKey(targetKey)
		if !ok {
			return Target{}, fmt.Errorf("%w: experience target %q", ErrInvalidInput, targetKey)
		}
		return Target{Kind: kind, ExperienceID: experienceID}, nil
	default:
		return Target{}, fmt.Errorf("%w: kind %q", ErrInvalidInput, kind)
	}
}

// TargetForArtifact reconstructs the target of one persisted artifact. It is
// used by synchronous invalidation, where only the current artifact row is
// available.
func TargetForArtifact(item Artifact) (Target, error) {
	target, err := ParseTarget(item.Kind, item.TargetKey)
	if err != nil {
		return Target{}, err
	}
	if target.ScopeKey == "" {
		target.ScopeKey = item.ScopeKey
	}
	if item.Kind != KindExperienceCase && target.Topic == "" {
		target.Topic = item.Topic
	}
	return target, nil
}

// ExperienceTarget returns the case target for one recorded feedback run.
func ExperienceTarget(experienceID string) Target {
	return Target{Kind: KindExperienceCase, ExperienceID: experienceID}
}

// SkillTargets returns the per-topic skill targets for one scope. A procedure
// without topics still gets one scope-level skill instead of an unscoped or
// global object.
func SkillTargets(scopeKey string, topics []string) []Target {
	scopeKey = normalizeScope(scopeKey)
	if scopeKey == "" {
		return nil
	}
	normalized := normalizeTopics(topics)
	if len(normalized) == 0 {
		return []Target{{Kind: KindSkill, ScopeKey: scopeKey}}
	}
	result := make([]Target, 0, len(normalized))
	for _, topic := range normalized {
		result = append(result, Target{Kind: KindSkill, ScopeKey: scopeKey, Topic: topic})
	}
	return result
}

// AggregateTargets returns the summary targets for one atomic memory's scope
// and canonical topics (including parents) plus the scope profile.
func AggregateTargets(scopeKey string, topics []string) []Target {
	scopeKey = normalizeScope(scopeKey)
	if scopeKey == "" {
		return nil
	}
	normalized := normalizeTopics(topics)
	result := make([]Target, 0, len(normalized)+1)
	for _, topic := range normalized {
		result = append(result, Target{Kind: KindTopicSummary, ScopeKey: scopeKey, Topic: topic})
	}
	result = append(result, Target{Kind: KindProfile, ScopeKey: scopeKey})
	return result
}

// MemorySnapshot is the invalidation-relevant part of one atomic memory. It is
// intentionally independent from the memory package so artifact can be imported
// by the unified writer without a package cycle.
type MemorySnapshot struct {
	ID       string
	Version  int
	Kind     string
	ScopeKey string
	Topics   []string
	State    string
}

// MemoryChange describes one atomic write. Semantic is false for an
// evidence-only append: the existing derived text remains usable because its
// atomic assertions did not change.
type MemoryChange struct {
	MemoryID string
	Old      *MemorySnapshot
	New      *MemorySnapshot
	Semantic bool
	// Reason distinguishes changes that do not advance the memory version, such
	// as conflict adjudication, so the target revision still moves.
	Reason string
}

// MemoryInput is one bounded atomic input selected for aggregation.
type MemoryInput struct {
	ID         string    `json:"id"`
	Version    int       `json:"version"`
	Kind       string    `json:"kind"`
	Phase      string    `json:"phase"`
	Durability string    `json:"durability"`
	Title      string    `json:"title"`
	Content    string    `json:"content"`
	ScopeKey   string    `json:"scope_key,omitempty"`
	Topics     []string  `json:"topics"`
	ObservedAt time.Time `json:"observed_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// AggregateRequest is the complete bounded input for one summary or profile
// model call. It contains only memory text, never source payloads.
type AggregateRequest struct {
	Kind      Kind          `json:"kind"`
	ScopeKey  string        `json:"scope_key"`
	Topic     string        `json:"topic,omitempty"`
	Truncated bool          `json:"truncated"`
	Memories  []MemoryInput `json:"memories"`
}

// AggregateResult is the tolerant parsed model output. Empty content is not
// usable; an empty title receives a fixed default in the handler.
type AggregateResult struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// DocumentInput is the current artifact text used to build the derived FTS
// document. The original text stays in artifact_revisions.
type DocumentInput struct {
	ArtifactID string
	Version    int
	Title      string
	Content    string
}

// SearchDocument is the normalized FTS row. It is derived and rebuildable.
type SearchDocument struct {
	ArtifactID   string
	Version      int
	TitleTerms   string
	ContentTerms string
}

// DocumentBuilder is injected into the artifact write path so derived recall
// uses exactly the same normalization and dictionary segmentation as atomic
// recall.
type DocumentBuilder interface {
	BuildArtifactDocument(input DocumentInput) (SearchDocument, error)
}

// SearchQuery is one compiled derived FTS query. Expression is produced only by
// retrieval.Compile, never from raw user text.
type SearchQuery struct {
	Expression string
	ScopeKey   string
	Kinds      []Kind
	AsOf       time.Time
	Limit      int
}

// SearchHit is one active artifact with its current revision.
type SearchHit struct {
	Artifact Artifact `json:"artifact"`
	Revision Revision `json:"revision"`
	Score    float64  `json:"score"`
}

// GroupTarget is one distinct (scope, topic) group discovered by reconciliation
// plus its stable keyset cursor.
type GroupTarget struct {
	Cursor string
	Target Target
}

// CommitInput is one successful, usable generated version.
type CommitInput struct {
	Kind             Kind
	TargetKey        string
	ScopeKey         string
	Topic            string
	ExperienceID     string
	Title            string
	Content          string
	Payload          json.RawMessage
	ReasonCode       string
	Truncated        bool
	Generator        string
	ProtocolVersion  string
	TaxonomyVersion  string
	GeneratorVersion string
	RunID            string
	Memories         []MemoryInput
	Runs             []RunRef
}

// ExperienceInput is one bounded application result supplied as evidence for an
// experience case or skill. Outcome is always the application's report, never a
// verification result.
type ExperienceInput struct {
	ID           string    `json:"id"`
	RunID        string    `json:"run_id,omitempty"`
	ScopeKey     string    `json:"scope_key,omitempty"`
	Goal         string    `json:"goal"`
	Conditions   []string  `json:"conditions"`
	Actions      []string  `json:"actions"`
	Outcome      string    `json:"outcome"`
	SkillID      string    `json:"skill_id,omitempty"`
	SkillVersion int       `json:"skill_version,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// ExperienceRequest is the bounded input for one experience-case model call.
type ExperienceRequest struct {
	Run       ExperienceInput `json:"run"`
	ScopeKey  string          `json:"scope_key,omitempty"`
	Skill     *SkillContext   `json:"skill,omitempty"`
	Memories  []MemoryInput   `json:"memories"`
	Truncated bool            `json:"truncated"`
}

// SkillContext is the exact skill text a run reported against, when it still
// exists. It is context, not evidence.
type SkillContext struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// ExperienceResult is the tolerant parsed case output. The service attaches the
// real run and memory references and preserves the application-reported
// outcome; model-supplied references and verification claims are ignored.
type ExperienceResult struct {
	Problem       string   `json:"problem"`
	Preconditions []string `json:"preconditions"`
	Actions       []string `json:"actions"`
	Lessons       []string `json:"lessons"`
	FailureModes  []string `json:"failure_modes"`
	Content       string   `json:"content"`
}

// Reader is the artifact read capability backed by the read pool.
type Reader interface {
	GetArtifact(ctx context.Context, id string) (Artifact, error)
	GetArtifactByTarget(ctx context.Context, kind Kind, targetKey string) (Artifact, error)
	GetArtifactRevision(ctx context.Context, id string, version int) (Revision, error)
	ListArtifactRevisions(ctx context.Context, id string, limit int) ([]Revision, error)
}

// Searcher runs the derived FTS query and loads current active revisions.
type Searcher interface {
	SearchArtifacts(ctx context.Context, query SearchQuery) ([]SearchHit, error)
}

// ReconcileReader finds bounded missing or stale targets without scanning the
// whole catalog in one transaction.
type ReconcileReader interface {
	ListStaleTargets(ctx context.Context, after string, limit int) ([]Target, error)
	ListExpiredTargets(ctx context.Context, asOf time.Time, after string, limit int) ([]Target, error)
	ListAggregationGroups(ctx context.Context, after string, limit int) ([]GroupTarget, error)
	ListSkillGroups(ctx context.Context, after string, limit int) ([]GroupTarget, error)
}

// Backend is the read capability set consumed by Service.
type Backend interface {
	Reader
	Searcher
	ReconcileReader
}

// ServiceAPI is the artifact management read capability exposed by HTTP.
type ServiceAPI interface {
	Get(ctx context.Context, id string) (Detail, error)
	GetVersion(ctx context.Context, id string, version int) (Detail, error)
	ListVersions(ctx context.Context, id string, limit int) (Detail, []Revision, error)
}

// Tx is the artifact-scoped writable transaction. The first-release governance
// transaction embeds it so artifact state, dependencies, FTS rows and task
// completion commit together.
type Tx interface {
	task.Tx

	GetArtifact(ctx context.Context, id string) (Artifact, error)
	GetArtifactByTarget(ctx context.Context, kind Kind, targetKey string) (Artifact, error)
	GetArtifactRevision(ctx context.Context, id string, version int) (Revision, error)
	CreateArtifact(ctx context.Context, item Artifact) error
	UpdateArtifact(ctx context.Context, item Artifact) error
	InsertArtifactRevision(ctx context.Context, revision Revision) error
	LinkArtifactMemoryDependencies(ctx context.Context, artifactID string, version int, refs []MemoryRef) error
	LinkArtifactRunDependencies(ctx context.Context, artifactID string, version int, refs []RunRef) error
	UpsertArtifactSearchDocument(ctx context.Context, document SearchDocument) error
	DeleteArtifactSearchDocument(ctx context.Context, artifactID string) error

	ListArtifactsForMemory(ctx context.Context, memoryID string) ([]Artifact, error)
	ListAllArtifactsForMemory(ctx context.Context, memoryID string) ([]Artifact, error)
	ListArtifactsForRun(ctx context.Context, experienceID string) ([]Artifact, error)
	ListAllArtifactsForRun(ctx context.Context, experienceID string) ([]Artifact, error)
	ListArtifactsByScope(ctx context.Context, kind Kind, scopeKey string) ([]Artifact, error)
	DeleteArtifactsForMemory(ctx context.Context, memoryID string) error
	DeleteArtifactsForRun(ctx context.Context, experienceID string) error
	ListAggregationInputs(ctx context.Context, target Target, asOf time.Time, limit int) ([]MemoryInput, bool, error)
	HasProcedureInputs(ctx context.Context, scopeKey string, asOf time.Time) (bool, error)
}
