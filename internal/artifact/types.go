// Package artifact owns derived memories and their immutable revisions. Derived
// memories are generated level by level from observations, canonical memories,
// lower derived layers or experience inputs; typed dependencies record the
// exact inputs of every revision so invalidation can propagate through the
// graph rather than only one level.
package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

// Layer is the aggregation level of one derived memory.
type Layer string

const (
	LayerL1 Layer = "l1"
	LayerL2 Layer = "l2"
	LayerL3 Layer = "l3"
	LayerL4 Layer = "l4"
)

// Known reports whether layer belongs to the fixed aggregation hierarchy.
func (l Layer) Known() bool {
	switch l {
	case LayerL1, LayerL2, LayerL3, LayerL4:
		return true
	default:
		return false
	}
}

// Kind is one derived layer object.
type Kind string

const (
	KindLocalSummary   Kind = "local_summary"
	KindTopicSummary   Kind = "topic_summary"
	KindScopeSummary   Kind = "scope_summary"
	KindProfile        Kind = "profile"
	KindExperienceCase Kind = "experience_case"
	KindSkill          Kind = "skill"
	KindGuidance       Kind = "guidance"
)

// LayerForKind maps derived kinds onto the fixed aggregation hierarchy. Layer is
// the scheduling and input-selection boundary; Kind remains the content shape.
func LayerForKind(kind Kind) Layer {
	switch kind {
	case KindLocalSummary:
		return LayerL1
	case KindExperienceCase, KindSkill, KindGuidance:
		return LayerL4
	case KindScopeSummary:
		return LayerL3
	case KindProfile, KindTopicSummary:
		return LayerL2
	default:
		return ""
	}
}

// DefaultKindForLayer returns the canonical content kind of one layer when no
// more specific kind is supplied by code.
func DefaultKindForLayer(layer Layer) Kind {
	switch layer {
	case LayerL1:
		return KindLocalSummary
	case LayerL2:
		return KindTopicSummary
	case LayerL3:
		return KindScopeSummary
	case LayerL4:
		return KindGuidance
	default:
		return ""
	}
}

// Known reports whether kind belongs to the fixed first-release set.
func (k Kind) Known() bool {
	switch k {
	case KindLocalSummary, KindTopicSummary, KindScopeSummary, KindProfile,
		KindExperienceCase, KindSkill, KindGuidance:
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

// DerivedMemory is one current derived object. CurrentVersion is zero only when
// no usable version has ever been generated; history is still retained.
type DerivedMemory struct {
	ID             string    `json:"id"`
	Layer          Layer     `json:"layer"`
	Kind           Kind      `json:"kind"`
	TargetKey      string    `json:"target_key"`
	ScopeKey       string    `json:"scope_key"`
	Topic          string    `json:"topic"`
	CurrentVersion int       `json:"current_version"`
	State          State     `json:"state"`
	ReasonCode     string    `json:"reason_code,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DependencyType identifies the layer of one derived-memory input.
type DependencyType string

const (
	DependencyObservation DependencyType = "observation"
	DependencyMemory      DependencyType = "memory"
	DependencyDerived     DependencyType = "derived"
	DependencyExperience  DependencyType = "experience"
)

// Known reports whether type belongs to the fixed dependency vocabulary.
func (t DependencyType) Known() bool {
	switch t {
	case DependencyObservation, DependencyMemory, DependencyDerived, DependencyExperience:
		return true
	default:
		return false
	}
}

// DependencyRef identifies one exact input revision. Version zero means the
// dependency is not versioned (for example an observation or experience run).
type DependencyRef struct {
	Type     DependencyType `json:"type"`
	ID       string         `json:"id"`
	Version  int            `json:"version"`
	Required bool           `json:"required"`
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

// Revision is one immutable derived snapshot. Structured kinds keep their
// fields in Payload; all kinds expose Title/Content as the readable text used by
// recall and rendering. Dependencies are stored separately as typed refs.
type Revision struct {
	ArtifactID      string          `json:"artifact_id"`
	Version         int             `json:"version"`
	Title           string          `json:"title"`
	Content         string          `json:"content"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	ContentHash     string          `json:"content_hash"`
	Generator       string          `json:"generator"`
	ProtocolVersion string          `json:"protocol_version"`
	Truncated       bool            `json:"truncated"`
	CreatedAt       time.Time       `json:"created_at"`
}

// Detail is one artifact plus its selected revision and that revision's exact
// typed dependencies. Revision is nil when the artifact has never produced a
// usable version.
type Detail struct {
	DerivedMemory DerivedMemory   `json:"artifact"`
	Revision      *Revision       `json:"revision,omitempty"`
	Dependencies  []DependencyRef `json:"dependencies,omitempty"`
}

// Target is one aggregation destination. Bucket, topic and guidance key are
// generated by code; the model never chooses a layer or target.
type Target struct {
	Layer    Layer  `json:"layer"`
	Kind     Kind   `json:"kind"`
	ScopeKey string `json:"scope_key"`
	Bucket   string `json:"bucket,omitempty"`
	Topic    string `json:"topic,omitempty"`
	Key      string `json:"key,omitempty"`

	// ExperienceID identifies one feedback run for the L4 case target. It is
	// never supplied by an aggregation model.
	ExperienceID string `json:"experience_id,omitempty"`
}

// LayerOrDefault returns the explicit layer, or the canonical layer of the
// target kind when callers construct a target from an artifact row.
func (t Target) LayerOrDefault() Layer {
	if t.Layer.Known() {
		return t.Layer
	}
	return LayerForKind(t.Kind)
}

// TargetKey returns the canonical governance-task and artifact target key. The
// layer is authoritative when supplied; otherwise it is inferred from Kind.
func (t Target) TargetKey() string {
	scopeKey := normalizeScope(t.ScopeKey)
	switch t.LayerOrDefault() {
	case LayerL1:
		bucket := strings.TrimSpace(t.Bucket)
		if bucket == "" {
			bucket = strings.TrimSpace(t.Key)
		}
		return task.L1TargetKey(scopeKey, bucket)
	case LayerL2:
		if t.Kind == KindProfile && strings.TrimSpace(t.Topic) == "" && strings.TrimSpace(t.Key) == "" {
			return task.L2TargetKey(scopeKey, "profile")
		}
		domain := strings.TrimSpace(t.Topic)
		if domain == "" {
			domain = strings.TrimSpace(t.Key)
		}
		return task.L2TargetKey(scopeKey, domain)
	case LayerL3:
		return task.L3TargetKey(scopeKey)
	case LayerL4:
		switch t.Kind {
		case KindExperienceCase:
			return task.ExperienceKey(t.ExperienceID)
		case KindSkill:
			key := strings.TrimSpace(t.Key)
			if key == "" {
				key = strings.TrimSpace(t.Topic)
			}
			return task.SkillKey(scopeKey, key)
		default:
			key := strings.TrimSpace(t.Key)
			if key == "" {
				key = strings.TrimSpace(t.Topic)
			}
			return task.DerivedKey(LayerL4, scopeKey, key)
		}
	default:
		return task.DerivedKey(t.Layer, scopeKey, strings.TrimSpace(t.Key))
	}
}

// ParseTargetKey decodes one canonical target key without requiring the caller
// to know the content kind first.
func ParseTargetKey(targetKey string) (Target, error) {
	if scopeKey, bucket, ok := task.ParseL1TargetKey(targetKey); ok {
		return Target{Layer: LayerL1, Kind: KindLocalSummary, ScopeKey: scopeKey, Bucket: bucket}, nil
	}
	if scopeKey, domain, ok := task.ParseL2TargetKey(targetKey); ok {
		return Target{Layer: LayerL2, Kind: KindTopicSummary, ScopeKey: scopeKey, Topic: domain}, nil
	}
	if scopeKey, ok := task.ParseL3TargetKey(targetKey); ok {
		return Target{Layer: LayerL3, Kind: KindScopeSummary, ScopeKey: scopeKey}, nil
	}
	if scopeKey, key, ok := task.ParseDerivedKey(LayerL4, targetKey); ok {
		return Target{Layer: LayerL4, Kind: KindGuidance, ScopeKey: scopeKey, Key: key}, nil
	}
	if scopeKey, topic, ok := task.ParseSkillKey(targetKey); ok {
		return Target{Layer: LayerL4, Kind: KindSkill, ScopeKey: scopeKey, Topic: topic}, nil
	}
	if experienceID, ok := task.ParseExperienceKey(targetKey); ok {
		return Target{Layer: LayerL4, Kind: KindExperienceCase, ExperienceID: experienceID}, nil
	}
	return Target{}, fmt.Errorf("%w: target key %q", ErrInvalidInput, targetKey)
}

// ParseTarget decodes one canonical target given its kind. A key produced by
// TargetKey is accepted regardless of whether the kind was known before the
// layer split; the stored kind always wins.
func ParseTarget(kind Kind, targetKey string) (Target, error) {
	parsed, err := ParseTargetKey(targetKey)
	if err != nil {
		return Target{}, fmt.Errorf("%w: kind %q target %q", ErrInvalidInput, kind, targetKey)
	}
	kindLayer := LayerForKind(kind)
	if kindLayer != "" && parsed.Layer != "" && kindLayer != parsed.Layer {
		return Target{}, fmt.Errorf("%w: kind %q does not belong to target %q", ErrInvalidInput, kind, targetKey)
	}
	parsed.Kind = kind
	if kind == KindExperienceCase {
		experienceID, ok := task.ParseExperienceKey(targetKey)
		if !ok {
			return Target{}, fmt.Errorf("%w: experience target %q", ErrInvalidInput, targetKey)
		}
		parsed.ExperienceID = experienceID
	}
	return parsed, nil
}

// TargetForArtifact reconstructs the target of one persisted artifact. It is
// used by synchronous invalidation, where only the current artifact row is
// available.
func TargetForArtifact(item DerivedMemory) (Target, error) {
	target, err := ParseTarget(item.Kind, item.TargetKey)
	if err != nil {
		return Target{}, err
	}
	if target.ScopeKey == "" {
		target.ScopeKey = item.ScopeKey
	}
	if target.Layer == "" {
		target.Layer = item.Layer
	}
	if target.Layer == "" {
		target.Layer = LayerForKind(item.Kind)
	}
	if item.Kind != KindExperienceCase && target.Topic == "" {
		target.Topic = item.Topic
	}
	return target, nil
}

// ExperienceTarget returns the case target for one recorded feedback run.
func ExperienceTarget(experienceID string) Target {
	return Target{Layer: LayerL4, Kind: KindExperienceCase, ExperienceID: experienceID}
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
		return []Target{{Layer: LayerL4, Kind: KindSkill, ScopeKey: scopeKey}}
	}
	result := make([]Target, 0, len(normalized))
	for _, topic := range normalized {
		result = append(result, Target{Layer: LayerL4, Kind: KindSkill, ScopeKey: scopeKey, Topic: topic})
	}
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
// retrieval.Compile, never from raw user text. Layers and Kinds narrow the same
// fixed derived hierarchy; States defaults to active. Terms is a sanitized,
// tokenized fallback used only for explicitly requested stale history whose FTS
// document is no longer current.
type SearchQuery struct {
	Expression string
	Terms      []string
	ScopeKey   string
	Kinds      []Kind
	Layers     []Layer
	States     []State
	Limit      int
}

// SearchHit is one current derived artifact revision. Dependencies are the
// exact typed inputs of this revision and provide bounded drill-down references.
type SearchHit struct {
	DerivedMemory DerivedMemory   `json:"artifact"`
	Revision      Revision        `json:"revision"`
	Dependencies  []DependencyRef `json:"dependencies,omitempty"`
	Score         float64         `json:"score"`
}

// TargetStatus is the externally visible state of one generation target. It
// separates work in progress, ineligible input, generation failure, a usable
// current version and stale/disabled history.
type TargetStatus string

const (
	TargetStatusQueued           TargetStatus = "queued"
	TargetStatusRunning          TargetStatus = "running"
	TargetStatusWaiting          TargetStatus = "waiting"
	TargetStatusActive           TargetStatus = "active"
	TargetStatusStale            TargetStatus = "stale"
	TargetStatusDisabled         TargetStatus = "disabled"
	TargetStatusNoEligibleInput  TargetStatus = "no_eligible_input"
	TargetStatusModelUnavailable TargetStatus = "model_unavailable"
	TargetStatusGenerationFailed TargetStatus = "generation_failed"
	TargetStatusInvalidOutput    TargetStatus = "invalid_output"
)

// Known reports whether status is part of the fixed target-status vocabulary.
func (s TargetStatus) Known() bool {
	switch s {
	case TargetStatusQueued, TargetStatusRunning, TargetStatusWaiting,
		TargetStatusActive, TargetStatusStale, TargetStatusDisabled,
		TargetStatusNoEligibleInput, TargetStatusModelUnavailable,
		TargetStatusGenerationFailed, TargetStatusInvalidOutput:
		return true
	default:
		return false
	}
}

// ListQuery filters current derived objects. A zero field means no filter.
type ListQuery struct {
	ScopeKey   string
	Kind       Kind
	State      State
	ReasonCode string
	Cursor     string
	Limit      int
}

// ListPage is one bounded artifact-management page.
type ListPage struct {
	Items      []DerivedMemory `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// TargetEvaluation records a target that was evaluated but produced no usable
// artifact. It is distinct from a disabled artifact with retained history.
type TargetEvaluation struct {
	Kind        Kind
	TargetKey   string
	ScopeKey    string
	Topic       string
	Status      TargetStatus
	ReasonCode  string
	EvaluatedAt time.Time
}

// TargetQuery filters generation targets, including targets that have no
// artifact yet. Status is the derived target status, not the artifact state.
type TargetQuery struct {
	ScopeKey   string
	Kind       Kind
	Status     TargetStatus
	ReasonCode string
	Cursor     string
	Limit      int
}

// TargetRecord is one generation target with its artifact, latest job and last
// no-artifact evaluation. DerivedMemory is nil when no content object exists yet.
type TargetRecord struct {
	Kind          Kind
	TargetKey     string
	ScopeKey      string
	Topic         string
	Status        TargetStatus
	ReasonCode    string
	DerivedMemory *DerivedMemory
	Job           *task.Job
	EvaluatedAt   *time.Time
	UpdatedAt     time.Time
}

// HasUsableVersion reports whether the target has produced at least one
// immutable revision, even if the current state is stale or disabled.
func (r TargetRecord) HasUsableVersion() bool {
	return r.DerivedMemory != nil && r.DerivedMemory.CurrentVersion > 0
}

// TargetPage is one bounded generation-target page.
type TargetPage struct {
	Items      []TargetRecord `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// TargetCursor returns the stable keyset cursor for one target.
func TargetCursor(targetKey string) string {
	return targetKey
}

// CommitInput is one successful, usable generated version. Dependencies are the
// exact typed inputs of this revision; the service never derives them from
// model output.
type CommitInput struct {
	Target          Target
	Title           string
	Content         string
	Payload         json.RawMessage
	ReasonCode      string
	Truncated       bool
	Generator       string
	ProtocolVersion string
	Dependencies    []DependencyRef
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

// ExperienceRequest is the bounded input for one L4 experience-case model
// call. The service sends only readable lower-layer content; the run itself is
// an application report, never a verified execution record.
type ExperienceRequest struct {
	Run       ExperienceInput `json:"run"`
	ScopeKey  string          `json:"scope_key,omitempty"`
	Inputs    []InputRef      `json:"inputs"`
	Truncated bool            `json:"truncated"`
}

// ExperienceResult is the generated case body. The service owns the title,
// application-reported outcome, verification statement, dependencies and
// revision metadata; the model cannot supply any of them.
type ExperienceResult struct {
	Content string `json:"content"`
}

// Reader is the artifact read capability backed by the read pool.
type Reader interface {
	GetArtifact(ctx context.Context, id string) (DerivedMemory, error)
	GetArtifactByTarget(ctx context.Context, kind Kind, targetKey string) (DerivedMemory, error)
	GetArtifactRevision(ctx context.Context, id string, version int) (Revision, error)
	ListArtifactRevisions(ctx context.Context, id string, limit int) ([]Revision, error)
	ListArtifactDependencies(ctx context.Context, id string, version int) ([]DependencyRef, error)
	ListArtifacts(ctx context.Context, query ListQuery) (ListPage, error)
}

// TargetReader lists generation targets that may not have an artifact yet.
type TargetReader interface {
	ListTargets(ctx context.Context, query TargetQuery) (TargetPage, error)
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
}

// Backend is the read capability set consumed by Service.
type Backend interface {
	Reader
	Searcher
	ReconcileReader
	TargetReader
}

// ServiceAPI is the artifact management read capability exposed by HTTP.
type ServiceAPI interface {
	Get(ctx context.Context, id string) (Detail, error)
	GetVersion(ctx context.Context, id string, version int) (Detail, error)
	ListVersions(ctx context.Context, id string, limit int) (Detail, []Revision, error)
	List(ctx context.Context, query ListQuery) (ListPage, error)
	ListTargets(ctx context.Context, query TargetQuery) (TargetPage, error)
}

// Tx is the artifact-scoped writable transaction. The first-release governance
// transaction embeds it so artifact state, dependencies, FTS rows and task
// completion commit together.
type Tx interface {
	task.Tx
	DependencyTx

	GetArtifact(ctx context.Context, id string) (DerivedMemory, error)
	GetArtifactByTarget(ctx context.Context, kind Kind, targetKey string) (DerivedMemory, error)
	GetArtifactRevision(ctx context.Context, id string, version int) (Revision, error)
	CreateArtifact(ctx context.Context, item DerivedMemory) error
	UpdateArtifact(ctx context.Context, item DerivedMemory) error
	InsertArtifactRevision(ctx context.Context, revision Revision) error
	UpsertArtifactSearchDocument(ctx context.Context, document SearchDocument) error
	DeleteArtifactSearchDocument(ctx context.Context, artifactID string) error

	ListArtifactsForMemory(ctx context.Context, memoryID string) ([]DerivedMemory, error)
	ListAllArtifactsForMemory(ctx context.Context, memoryID string) ([]DerivedMemory, error)
	ListArtifactsForRun(ctx context.Context, experienceID string) ([]DerivedMemory, error)
	ListAllArtifactsForRun(ctx context.Context, experienceID string) ([]DerivedMemory, error)
	ListArtifactsByScope(ctx context.Context, kind Kind, scopeKey string) ([]DerivedMemory, error)
	DeleteArtifactsForMemory(ctx context.Context, memoryID string) error
	DeleteArtifactsForRun(ctx context.Context, experienceID string) error
	UpsertTargetEvaluation(ctx context.Context, evaluation TargetEvaluation) error
	DeleteTargetEvaluation(ctx context.Context, kind Kind, targetKey string) error
}
