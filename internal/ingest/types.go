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

// Waiting reasons are fixed codes and never contain provider or source text.
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
	NextAttemptAt    *time.Time `json:"next_attempt_at,omitempty"`
	Error            string     `json:"error,omitempty"`
	MemoryIDs        []string   `json:"memory_ids"`
	ConflictIDs      []string   `json:"conflict_ids"`
	PendingMemoryIDs []string   `json:"pending_memory_ids"`
	Warnings         []string   `json:"warnings"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

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
// identifier and may ignore the textual metadata.
type FactGroupProposal struct {
	Subject     string             `json:"subject,omitempty"`
	Predicate   string             `json:"predicate,omitempty"`
	Scope       string             `json:"scope,omitempty"`
	Cardinality memory.Cardinality `json:"cardinality,omitempty"`
}

// Candidate is one independently extracted assertion. CandidateID is assigned
// by the service; the model never chooses a memory, version, evidence or
// persistent-state identifier.
type Candidate struct {
	CandidateID    string               `json:"candidate_id"`
	Kind           memory.Kind          `json:"kind,omitempty"`
	Title          string               `json:"title,omitempty"`
	Content        string               `json:"content"`
	ObservedAt     *time.Time           `json:"observed_at,omitempty"`
	ValidFrom      *time.Time           `json:"valid_from,omitempty"`
	ValidTo        *time.Time           `json:"valid_to,omitempty"`
	ValidFromBasis memory.TemporalBasis `json:"valid_from_basis,omitempty"`
	ValidToBasis   memory.TemporalBasis `json:"valid_to_basis,omitempty"`
	Topics         []string             `json:"topics,omitempty"`
	FactGroup      *FactGroupProposal   `json:"fact_group,omitempty"`
	Evidence       []source.Evidence    `json:"evidence,omitempty"`
}

// ExtractionRequest asks only for independent facts in the current source. It
// contains no database targets.
type ExtractionRequest struct {
	Source source.Source `json:"source"`
}

// ExtractionResult is the provider's structured fact set. An empty set is a
// successful result.
type ExtractionResult struct {
	Candidates []Candidate `json:"candidates"`
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
	ContextComplete bool               `json:"context_complete"`
}

// DecisionResult must contain exactly one decision per candidate.
type DecisionResult struct {
	Decisions []Decision `json:"decisions"`
}

// ContextWarning codes are stable and contain no model data.
const (
	WarningContextTruncated = "context_truncated"
	WarningConflictDetected = "fact_conflict"
)

// ProcessResult reports one completed processing attempt.
type ProcessResult struct {
	Status           JobStatus
	MemoryIDs        []string
	ConflictIDs      []string
	PendingMemoryIDs []string
	Warnings         []string
}
