// Package ingest owns the unified submission path, durable extraction-job state
// and the extraction worker that saves L0 observations. Governance and
// aggregation are separate asynchronous stages reached through the task table.
package ingest

import (
	"encoding/json"
	"time"

	"github.com/N0rthHXD/dmemd/internal/extraction"
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

// Job is the durable extraction task. It reports only the source, the saved L0
// observation IDs, extraction status and a source-free error summary. Governance
// and aggregation progress is queried through their own task targets.
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
	ObservationIDs   []string   `json:"observation_ids"`
	Warnings         []string   `json:"warnings"`
	// QualityOutcome describes the business result independently of the
	// scheduling status. It is empty before an attempt produces a result.
	QualityOutcome QualityOutcome `json:"quality_outcome,omitempty"`
	// ExtractionStats counts model drafts and their deterministic L0 outcomes.
	ExtractionStats ExtractionStats `json:"extraction_stats"`
	// Timings measures one completed attempt by stage. Durations are wall-clock
	// milliseconds and may overlap only where explicitly documented.
	Timings ProcessingTimings `json:"timings"`
	// ModelUsage aggregates extraction calls. Failed calls that produced a
	// response are included when the provider reported it.
	ModelUsage ModelUsage `json:"model_usage"`
	// Diagnostics hold fixed field/code tuples only. They never contain source
	// text or raw model responses.
	Diagnostics []ExtractionDiagnostic `json:"diagnostics"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	StartedAt   *time.Time             `json:"started_at,omitempty"`
	FinishedAt  *time.Time             `json:"finished_at,omitempty"`
}

// ModelUsage counts remote calls and token usage. It aliases the shared
// extraction accounting type so the model protocol and job state use one value.
type ModelUsage = extraction.ModelUsage

// ProcessingTimings is a source-free stage breakdown for one attempt.
// QueueAgeMillis runs from durable creation to this attempt, so a retry includes
// earlier waits; the other fields measure the current attempt.
type ProcessingTimings struct {
	QueueAgeMillis      int64 `json:"queue_age_ms"`
	ExtractionMillis    int64 `json:"extraction_ms"`
	NormalizationMillis int64 `json:"normalization_ms"`
	CommitMillis        int64 `json:"commit_ms"`
	TotalMillis         int64 `json:"total_ms"`
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

// ExtractionStats separates raw model drafts, normalized drafts and final L0
// outcomes. Counts never include source text and are safe for logs and API
// responses.
type ExtractionStats struct {
	Items                  int `json:"items"`
	Observations           int `json:"observations"`
	NormalizedObservations int `json:"normalized_observations"`
	PendingObservations    int `json:"pending_observations"`
	InvalidItems           int `json:"invalid_items"`
	DuplicateObservations  int `json:"duplicate_observations"`
	Chunks                 int `json:"chunks"`
	ChunkFailures          int `json:"chunk_failures"`
}

// ExtractionDiagnostic is a fixed, source-free quality diagnostic. Stage is one
// of extract, validate or isolate; Disposition is one of accepted, pending,
// isolated or excluded.
type ExtractionDiagnostic struct {
	Field       string `json:"field,omitempty"`
	Code        string `json:"code"`
	Stage       string `json:"stage"`
	Disposition string `json:"disposition"`
}

// Warning codes are stable and contain no model data.
const (
	WarningExtractionChunkFailed = "extraction_chunk_failed"
	WarningObservationPending    = "observation_evidence_unbound"
)

// ProcessResult reports one completed extraction attempt.
type ProcessResult struct {
	Status         JobStatus
	ObservationIDs []string
	Warnings       []string
	Quality        QualityOutcome
	Stats          ExtractionStats
	Timings        ProcessingTimings
	ModelUsage     ModelUsage
	Diagnostics    []ExtractionDiagnostic
}
