// Package ingest owns the unified submission path, persistent job state,
// extraction orchestration and automatic merge decisions.
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
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Submission is arbitrary valid JSON content plus optional source metadata.
// AppID and ExternalID are informational only; they are not identities,
// authorization inputs or uniqueness constraints.
type Submission struct {
	AppID      string          `json:"app_id,omitempty"`
	ExternalID string          `json:"external_id,omitempty"`
	Content    json.RawMessage `json:"content"`
}

// Receipt is returned immediately after source and queued job are committed.
type Receipt struct {
	SourceID string    `json:"source_id"`
	JobID    string    `json:"job_id"`
	Status   JobStatus `json:"status"`
}

// Job is the durable processing task. MemoryIDs is the result summary; raw
// model responses are never stored here.
type Job struct {
	ID            string     `json:"id"`
	SourceID      string     `json:"source_id"`
	Status        JobStatus  `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	Error         string     `json:"error,omitempty"`
	MemoryIDs     []string   `json:"memory_ids"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// Action is the merge decision proposed by the extraction provider.
type Action string

const (
	ActionAdd           Action = "add"
	ActionUpdate        Action = "update"
	ActionMergeEvidence Action = "merge_evidence"
	ActionSkip          Action = "skip"
)

// Candidate is one extracted memory proposal. The service validates the
// target against persisted state; the provider does not receive or set a
// version precondition.
type Candidate struct {
	Action         Action            `json:"action"`
	TargetMemoryID string            `json:"target_memory_id,omitempty"`
	Kind           memory.Kind       `json:"kind,omitempty"`
	Title          string            `json:"title,omitempty"`
	Content        string            `json:"content,omitempty"`
	ObservedAt     *time.Time        `json:"observed_at,omitempty"`
	Reason         string            `json:"reason,omitempty"`
	Evidence       []source.Evidence `json:"evidence,omitempty"`
}

// ExtractionRequest is the complete provider input: the current submission and
// only the relevant active memory context selected by the service.
type ExtractionRequest struct {
	Source          source.Source   `json:"source"`
	RelatedMemories []memory.Memory `json:"related_memories"`
}

// ExtractionResult is the provider's structured proposal set. An empty set is
// a successful result.
type ExtractionResult struct {
	Candidates []Candidate `json:"candidates"`
}
