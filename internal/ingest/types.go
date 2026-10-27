// Package ingest owns the unified submission path, persistent job state,
// two-stage extraction/decision orchestration and the atomic merge transaction.
package ingest

import (
	"encoding/json"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// JobStatus is the persistent task state.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobWaiting   JobStatus = "waiting"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Waiting reasons are fixed scheduling categories. Job.Stage and Job.ReasonCode
// provide finer diagnostics without containing provider or source text.
const (
	WaitReasonConfiguration = "configuration"
	WaitReasonTransient     = "transient"
	WaitReasonProtocol      = "protocol"
)

// Submission is arbitrary valid JSON content plus optional source metadata.
// AppID and ExternalID are informational only; they are not identities,
// authorization inputs or uniqueness constraints.
type Submission struct {
	AppID      string          `json:"app_id,omitempty"`
	ExternalID string          `json:"external_id,omitempty"`
	ScopeKey   string          `json:"scope_key,omitempty"`
	Content    json.RawMessage `json:"content"`
}

// Receipt is returned immediately after source and queued job are committed.
type Receipt struct {
	SourceID string    `json:"source_id"`
	JobID    string    `json:"job_id"`
	Status   JobStatus `json:"status"`
}

// Job is the durable processing task. The result fields are fixed summaries;
// raw model responses and reasoning are never stored here.
type Job struct {
	ID               string     `json:"id"`
	SourceID         string     `json:"source_id"`
	Status           JobStatus  `json:"status"`
	Attempts         int        `json:"attempts"`
	ProtocolFailures int        `json:"protocol_failures"`
	WaitReason       string     `json:"wait_reason,omitempty"`
	Stage            string     `json:"stage,omitempty"`
	ReasonCode       string     `json:"reason_code,omitempty"`
	NextAttemptAt    *time.Time `json:"next_attempt_at,omitempty"`
	Error            string     `json:"error,omitempty"`
	MemoryIDs        []string   `json:"memory_ids"`
	ConflictIDs      []string   `json:"conflict_ids"`
	PendingMemoryIDs []string   `json:"pending_memory_ids"`
	Warnings         []string   `json:"warnings"`
	// QualityOutcome describes the business result independently of the
	// scheduling status. It is empty before an attempt produces a result.
	QualityOutcome QualityOutcome `json:"quality_outcome,omitempty"`
	// ExtractionStats counts candidates and repair actions by stage.
	ExtractionStats ExtractionStats `json:"extraction_stats"`
	// Retention separates retained candidates from fixed non-retention reasons
	// (explicit exclusion, duplicate representation, capacity and invalid
	// candidates). It is diagnostic and never asserts semantic correctness.
	Retention RetentionStats `json:"retention"`
	// Timings measures one completed attempt by stage. Durations are wall-clock
	// milliseconds and may overlap only where explicitly documented.
	Timings ProcessingTimings `json:"timings"`
	// ModelUsage aggregates extraction, repair and decision token usage. Failed
	// calls that produced a response are included when the provider reported it.
	ModelUsage ModelUsage `json:"model_usage"`
	// Diagnostics hold fixed candidate/field/code tuples only. They never
	// contain source text or raw model responses.
	Diagnostics []ExtractionDiagnostic `json:"diagnostics"`
	// Convergence explains every attempted automatic update that stayed with
	// the decision model or pending review.
	Convergence []ConvergenceDiagnostic `json:"convergence"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
	StartedAt   *time.Time              `json:"started_at,omitempty"`
	FinishedAt  *time.Time              `json:"finished_at,omitempty"`
}

// ModelUsage counts remote calls and token usage by stage. Calls are counted
// even when the provider does not return token usage.
type ModelUsage struct {
	Calls        int `json:"calls"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Add merges one call/usage record into the aggregate.
func (u *ModelUsage) Add(other ModelUsage) {
	if u == nil {
		return
	}
	u.Calls += other.Calls
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
}

// TotalTokens returns the reported input plus output token count.
func (u ModelUsage) TotalTokens() int { return u.InputTokens + u.OutputTokens }

// ProcessingTimings is a source-free stage breakdown for one attempt.
// QueueAgeMillis runs from durable creation to this attempt, so a retry includes
// earlier waits; the other fields measure the current attempt.
type ProcessingTimings struct {
	QueueAgeMillis      int64 `json:"queue_age_ms"`
	ExtractionMillis    int64 `json:"extraction_ms"`
	NormalizationMillis int64 `json:"normalization_ms"`
	RepairMillis        int64 `json:"repair_ms"`
	ContextMillis       int64 `json:"context_ms"`
	DecisionMillis      int64 `json:"decision_ms"`
	CommitMillis        int64 `json:"commit_ms"`
	TotalMillis         int64 `json:"total_ms"`
}

// RetentionStats records retained candidates by service-derived reason and
// fixed non-retention codes. Counts never contain source text.
type RetentionStats struct {
	Retained            int            `json:"retained"`
	NotRetained         int            `json:"not_retained"`
	ByReason            map[string]int `json:"by_reason"`
	NotRetainedByReason map[string]int `json:"not_retained_by_reason"`
}

// Fixed non-retention codes.
const (
	RetentionNotExplicitExclusion   = "explicit_exclusion"
	RetentionNotCrossChunkContext   = "cross_chunk_context"
	RetentionNotCrossChunkDuplicate = "cross_chunk_duplicate"
	RetentionNotCandidateLimit      = "candidate_limit"
	RetentionNotInvalidCandidate    = "invalid_candidate"
	RetentionNotDecisionSkip        = "decision_skip"
	RetentionNotEmptyExtraction     = "empty_extraction"
	RetentionNotChunkFailure        = "chunk_failure"
)

func (s *RetentionStats) addReason(reason memory.RetentionReason) {
	if s == nil {
		return
	}
	if s.ByReason == nil {
		s.ByReason = make(map[string]int)
	}
	s.Retained++
	s.ByReason[string(reason)]++
}

func (s *RetentionStats) removeReason(reason memory.RetentionReason) {
	if s == nil || s.Retained <= 0 {
		return
	}
	s.Retained--
	if s.ByReason == nil {
		return
	}
	key := string(reason)
	if s.ByReason[key] <= 1 {
		delete(s.ByReason, key)
		return
	}
	s.ByReason[key]--
}

func (s *RetentionStats) addNotRetained(code string, count int) {
	if s == nil || count <= 0 {
		return
	}
	if s.NotRetainedByReason == nil {
		s.NotRetainedByReason = make(map[string]int)
	}
	s.NotRetained += count
	s.NotRetainedByReason[code] += count
}

func (s *RetentionStats) Normalize() {
	if s.ByReason == nil {
		s.ByReason = map[string]int{}
	}
	if s.NotRetainedByReason == nil {
		s.NotRetainedByReason = map[string]int{}
	}
}

// QualityOutcome is a fixed business-quality result for one completed attempt.
// It is deliberately separate from JobStatus so a technical failure cannot be
// presented as a normal empty extraction.
type QualityOutcome string

const (
	QualityEmpty    QualityOutcome = "empty"
	QualityComplete QualityOutcome = "complete"
	QualityPartial  QualityOutcome = "partial"
	QualityUnusable QualityOutcome = "unusable"
)

// ExtractionStats separates raw model candidates, normalized candidates,
// repaired/split candidates and final dispositions. Counts never include source
// text and are safe for logs and API responses.
type ExtractionStats struct {
	RawCandidates           int `json:"raw_candidates"`
	ParsedCandidates        int `json:"parsed_candidates"`
	NormalizedCandidates    int `json:"normalized_candidates"`
	Normalizations          int `json:"normalizations"`
	RepairAttempts          int `json:"repair_attempts"`
	RepairedCandidates      int `json:"repaired_candidates"`
	RepairSucceeded         int `json:"repair_succeeded"`
	RepairFailed            int `json:"repair_failed"`
	RepairTimedOut          int `json:"repair_timed_out"`
	RepairInvalidOutput     int `json:"repair_invalid_output"`
	ExcludedCandidates      int `json:"excluded_candidates"`
	IsolatedCandidates      int `json:"isolated_candidates"`
	AcceptedCandidates      int `json:"accepted_candidates"`
	SkippedCandidates       int `json:"skipped_candidates"`
	MemoryWrites            int `json:"memory_writes"`
	ExtractionChunks        int `json:"extraction_chunks"`
	ExtractionChunkFailures int `json:"extraction_chunk_failures"`
	DroppedCandidates       int `json:"dropped_candidates"`
	CrossChunkDuplicates    int `json:"cross_chunk_duplicates"`
	ContextOnlyCandidates   int `json:"context_only_candidates"`
}

// ExtractionDiagnostic is a fixed, source-free quality diagnostic. Stage is
// one of normalize, validate, repair or isolate; Disposition is one of
// normalized, repaired, accepted, isolated or excluded.
type ExtractionDiagnostic struct {
	CandidateID string `json:"candidate_id,omitempty"`
	Field       string `json:"field,omitempty"`
	Code        string `json:"code"`
	Stage       string `json:"stage"`
	Disposition string `json:"disposition"`
}

// FieldNormalization records a safe shape recovery performed by the parser.
type FieldNormalization struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// FieldIssue records a protocol field that could not be safely normalized.
type FieldIssue struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// ConvergenceDiagnostic explains why one candidate was not automatically
// converged. Codes are fixed and never contain source text.
type ConvergenceDiagnostic struct {
	CandidateID string `json:"candidate_id,omitempty"`
	GroupID     string `json:"group_id,omitempty"`
	Code        string `json:"code"`
}

// Extraction issue classes.
const (
	IssueHard       = "hard"
	IssueRepairable = "repairable"
	IssueQuality    = "quality"
)

// Action is one service-validated decision action.
type Action string

const (
	ActionAdd           Action = "add"
	ActionMergeEvidence Action = "merge_evidence"
	ActionEnrich        Action = "enrich"
	ActionChange        Action = "change"
	ActionCorrect       Action = "correct"
	ActionSeparate      Action = "separate"
	ActionConflict      Action = "conflict"
	ActionUnresolved    Action = "unresolved"
	ActionSkip          Action = "skip"
)

// FactGroupProposal is a model suggestion. The service assigns the actual group
// identifier, scope key and normalized identity and may ignore the textual
// metadata.
type FactGroupProposal struct {
	Subject     string             `json:"subject,omitempty"`
	Predicate   string             `json:"predicate,omitempty"`
	Scope       string             `json:"scope,omitempty"`
	Conditions  []string           `json:"conditions,omitempty"`
	Cardinality memory.Cardinality `json:"cardinality,omitempty"`
}

// Phase describes the lifecycle phase stated by an extracted assertion. It is
// deliberately separate from memory.State: a scheduled event is still an
// active memory, while a cancelled event is not a deleted memory.
type Phase = memory.Phase

const (
	PhaseUnknown    Phase = "unknown"
	PhaseScheduled  Phase = "scheduled"
	PhaseInProgress Phase = "in_progress"
	PhaseCompleted  Phase = "completed"
	PhaseCancelled  Phase = "cancelled"
	PhasePostponed  Phase = "postponed"
)

// Durability is the model's classification of how long an assertion should be
// useful. The service uses it as extraction metadata and never treats it as a
// lifecycle or truth decision.
type Durability = memory.Durability

const (
	DurabilityUnknown   Durability = "unknown"
	DurabilityDurable   Durability = "durable"
	DurabilityEpisodic  Durability = "episodic"
	DurabilityTemporary Durability = "temporary"
)

// ProcedureProposal keeps the structure that makes a procedure actionable. It
// is the same structured value persisted in the atomic memory revision.
type ProcedureProposal = memory.Procedure

// AssertionQualifiers are general qualifiers that must remain attached to the
// independent assertion.
type AssertionQualifiers = memory.AssertionQualifiers

// Candidate is one independently extracted assertion. CandidateID is assigned
// by the service; the model never chooses a memory, version, evidence or
// persistent-state identifier.
type Candidate struct {
	CandidateID string      `json:"candidate_id"`
	Kind        memory.Kind `json:"kind,omitempty"`
	// Diagnostics survive tolerant parsing for validation and repair.
	TemporalErrors      []string                   `json:"-"`
	ProtocolIssues      []FieldIssue               `json:"-"`
	Normalizations      []FieldNormalization       `json:"-"`
	Title               string                     `json:"title,omitempty"`
	Content             string                     `json:"content"`
	ObservedAt          *time.Time                 `json:"observed_at,omitempty"`
	ObservedAtBasis     memory.TemporalBasis       `json:"observed_at_basis,omitempty"`
	ObservedAtPrecision memory.TemporalPrecision   `json:"-"`
	EventTime           *time.Time                 `json:"event_time,omitempty"`
	EventTimeBasis      memory.TemporalBasis       `json:"event_time_basis,omitempty"`
	EventTimePrecision  memory.TemporalPrecision   `json:"event_time_precision,omitempty"`
	ValidFrom           *time.Time                 `json:"valid_from,omitempty"`
	ValidTo             *time.Time                 `json:"valid_to,omitempty"`
	ValidFromBasis      memory.TemporalBasis       `json:"valid_from_basis,omitempty"`
	ValidToBasis        memory.TemporalBasis       `json:"valid_to_basis,omitempty"`
	ValidFromPrecision  memory.TemporalPrecision   `json:"-"`
	ValidToPrecision    memory.TemporalPrecision   `json:"-"`
	Phase               Phase                      `json:"phase,omitempty"`
	Durability          Durability                 `json:"durability,omitempty"`
	Topics              []string                   `json:"topics,omitempty"`
	FactGroup           *FactGroupProposal         `json:"fact_group,omitempty"`
	Procedure           *ProcedureProposal         `json:"procedure,omitempty"`
	Qualifiers          *AssertionQualifiers       `json:"qualifiers,omitempty"`
	Evidence            []source.EvidenceReference `json:"evidence,omitempty"`
	// Temporal is service-derived source wording and resolution for each time
	// anchor. It is never accepted from the model.
	Temporal memory.TemporalDetails `json:"-"`
	// SourceEvidence is the resolved, service-owned evidence aligned one-to-one
	// with Evidence. It is never sent to the model.
	SourceEvidence []source.Evidence `json:"-"`
}

// ExtractionRequest asks only for independent facts in the current source. It
// contains no database targets. Segments and ContextSegments are populated for
// bounded long-source extraction; empty means the complete source view.
type ExtractionRequest struct {
	Source          source.Source    `json:"source"`
	Segments        []source.Segment `json:"-"`
	ContextSegments []source.Segment `json:"-"`
	ChunkIndex      int              `json:"-"`
	ChunkCount      int              `json:"-"`
}

// ExtractionResult is the provider's structured fact set. RawCandidates keeps
// the pre-normalization response count so job statistics do not conflate model
// output with repaired or split candidates. An empty candidate set is a
// successful result.
type ExtractionResult struct {
	Candidates    []Candidate `json:"candidates"`
	RawCandidates int         `json:"-"`
	Usage         ModelUsage  `json:"-"`
}

// ExtractionIssue is a safe, non-source-bearing description of a candidate
// validation failure. It is suitable for a targeted model repair request and
// for logs/metrics.
type ExtractionIssue struct {
	CandidateID string `json:"candidate_id,omitempty"`
	Field       string `json:"field,omitempty"`
	Code        string `json:"code"`
	Class       string `json:"class,omitempty"`
	Message     string `json:"message"`
}

// ExtractionRepairRequest asks an optional provider to repair only the
// extraction protocol. It does not expose memory ids or database state.
type ExtractionRepairRequest struct {
	Source     source.Source     `json:"source"`
	Candidates []Candidate       `json:"candidates"`
	Issues     []ExtractionIssue `json:"issues"`
	// Retry requests a fresh structured response after a structural protocol
	// failure. It shares the single repair-call budget with local repairs.
	Retry bool `json:"retry"`
	// Segments/ContextSegments keep a chunk repair bounded to the same stable
	// source view as the original extraction.
	Segments        []source.Segment `json:"-"`
	ContextSegments []source.Segment `json:"-"`
	ChunkIndex      int              `json:"-"`
	ChunkCount      int              `json:"-"`
}

// EvidenceRef references one evidence entry of the current extraction batch.
type EvidenceRef struct {
	CandidateID   string `json:"candidate_id"`
	EvidenceIndex int    `json:"evidence_index"`
}

// Decision is the model's proposed relationship to existing facts. The service
// validates it before any write.
type Decision struct {
	CandidateID     string               `json:"candidate_id"`
	Action          Action               `json:"action"`
	TargetMemoryIDs []string             `json:"target_memory_ids,omitempty"`
	TargetGroupID   string               `json:"target_group_id,omitempty"`
	Content         string               `json:"content,omitempty"`
	ValidFrom       *time.Time           `json:"valid_from,omitempty"`
	ValidTo         *time.Time           `json:"valid_to,omitempty"`
	ValidFromBasis  memory.TemporalBasis `json:"valid_from_basis,omitempty"`
	ValidToBasis    memory.TemporalBasis `json:"valid_to_basis,omitempty"`
	ReasonCode      string               `json:"reason_code,omitempty"`
	EvidenceRefs    []EvidenceRef        `json:"evidence_refs,omitempty"`
	FactGroup       *FactGroupProposal   `json:"fact_group,omitempty"`
}

// DecisionRequest is one decision batch of at most eight candidates plus bounded
// consistency context.
type DecisionRequest struct {
	Source          source.Source      `json:"source"`
	Candidates      []Candidate        `json:"candidates"`
	RelatedMemories []memory.Memory    `json:"related_memories"`
	FactGroups      []memory.FactGroup `json:"fact_groups"`
	Conflicts       []memory.Conflict  `json:"conflicts"`
	// SourceSegments bounds the decision payload to the segments the batch
	// actually cites. Empty keeps the complete source view.
	SourceSegments  []source.Segment `json:"-"`
	ContextComplete bool             `json:"context_complete"`
	// IncompleteCandidateIDs and IncompleteFactGroupIDs identify the exact
	// truncated context. The service still enforces the downgrade itself; the
	// model receives the list only so it can avoid asserting completeness.
	IncompleteCandidateIDs []string `json:"incomplete_candidate_ids,omitempty"`
	IncompleteFactGroupIDs []string `json:"incomplete_fact_group_ids,omitempty"`
}

// DecisionResult must contain exactly one decision per candidate.
type DecisionResult struct {
	Decisions []Decision `json:"decisions"`
	Usage     ModelUsage `json:"-"`
}

// ContextWarning codes are stable and contain no model data.
const (
	WarningContextTruncated         = "context_truncated"
	WarningConflictDetected         = "fact_conflict"
	WarningExtractionIsolated       = "extraction_candidates_isolated"
	WarningExtractionExcluded       = "extraction_content_excluded"
	WarningExtractionChunkOversized = "extraction_chunk_oversized"
	WarningExtractionChunkFailed    = "extraction_chunk_failed"
)

// ProcessResult reports one completed processing attempt.
type ProcessResult struct {
	Status           JobStatus
	MemoryIDs        []string
	ConflictIDs      []string
	PendingMemoryIDs []string
	Warnings         []string
	Quality          QualityOutcome
	Stats            ExtractionStats
	Retention        RetentionStats
	Timings          ProcessingTimings
	ModelUsage       ModelUsage
	Diagnostics      []ExtractionDiagnostic
	Convergence      []ConvergenceDiagnostic
}
