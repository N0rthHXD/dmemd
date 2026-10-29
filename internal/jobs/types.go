// Package jobs owns the read-only presentation of extraction, governance and
// aggregation work. It never executes a task and never synchronizes one stage
// inside another: the durable task tables remain the source of truth.
package jobs

import (
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
)

// Stage is the externally visible lifecycle phase of one job. Extraction jobs
// stop at observations_saved; governance and aggregation jobs report their own
// downstream phase instead of pretending the extraction job completed it.
type Stage string

const (
	StageReceived           Stage = "received"
	StageExtracting         Stage = "extracting"
	StageObservationsSaved  Stage = "observations_saved"
	StageGovernancePending  Stage = "governance_pending"
	StageMemoryMaterialized Stage = "memory_materialized"
	StageAggregationPending Stage = "aggregation_pending"
	StageCompleted          Stage = "completed"
	StageFailed             Stage = "failed"
)

// Known reports whether stage belongs to the fixed job-stage vocabulary.
func (s Stage) Known() bool {
	switch s {
	case StageReceived, StageExtracting, StageObservationsSaved,
		StageGovernancePending, StageMemoryMaterialized,
		StageAggregationPending, StageCompleted, StageFailed:
		return true
	default:
		return false
	}
}

// Category separates the three asynchronous stages in listings.
type Category string

const (
	CategoryExtraction  Category = "extraction"
	CategoryGovernance  Category = "governance"
	CategoryAggregation Category = "aggregation"
)

// Known reports whether category belongs to the fixed job-category set.
func (c Category) Known() bool {
	switch c {
	case CategoryExtraction, CategoryGovernance, CategoryAggregation:
		return true
	default:
		return false
	}
}

// ListOptions filters the extraction-job page. Zero values mean no filter.
type ListOptions struct {
	Status ingest.JobStatus
	Limit  int
}

// TaskListOptions filters the governance/aggregation task page. Zero values
// mean no filter.
type TaskListOptions struct {
	Category  Category
	Status    task.Status
	Kind      task.Kind
	TargetKey string
	Limit     int
}

// ExtractionJob is one durable extraction job plus its computed stage. The
// stage is presentation state; it is never persisted or written back.
type ExtractionJob struct {
	ingest.Job
	Stage Stage `json:"stage"`
}

// TaskJob is one governance or aggregation task plus its computed category and
// stage.
type TaskJob struct {
	task.Job
	Category Category `json:"category"`
	Stage    Stage    `json:"stage"`
}

// ExtractionStage derives the extraction-only lifecycle stage. Governance and
// aggregation progress is deliberately absent: those are queried through their
// own task targets.
func ExtractionStage(job ingest.Job) Stage {
	switch job.Status {
	case ingest.JobQueued:
		return StageReceived
	case ingest.JobRunning, ingest.JobWaiting:
		return StageExtracting
	case ingest.JobSucceeded:
		if len(job.ObservationIDs) > 0 {
			return StageObservationsSaved
		}
		return StageCompleted
	case ingest.JobFailed, ingest.JobCancelled:
		return StageFailed
	default:
		return StageReceived
	}
}

// TaskCategory maps one governance task kind onto the stage that owns it.
func TaskCategory(kind task.Kind) Category {
	switch kind {
	case task.KindAggregateL1, task.KindAggregateL2, task.KindAggregateL3,
		task.KindBuildGuidance:
		return CategoryAggregation
	default:
		return CategoryGovernance
	}
}

// TaskStage derives the stage of one governance or aggregation task.
func TaskStage(job task.Job) Stage {
	category := TaskCategory(job.Kind)
	terminal := job.Status.Terminal()
	if !terminal {
		if category == CategoryAggregation {
			return StageAggregationPending
		}
		return StageGovernancePending
	}
	if job.Status != task.StatusSucceeded {
		return StageFailed
	}
	switch job.Kind {
	case task.KindReconcile, task.KindInvalidateDependency,
		task.KindAggregateL1, task.KindAggregateL2, task.KindAggregateL3,
		task.KindBuildGuidance:
		return StageCompleted
	default:
		return StageMemoryMaterialized
	}
}

// Job is retained as an explicit alias for callers that only need the stage
// helpers without the wrapping type.
type Job = ExtractionJob
