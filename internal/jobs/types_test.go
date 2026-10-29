package jobs

import (
	"testing"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
)

func TestExtractionStageStopsAtL0(t *testing.T) {
	cases := []struct {
		job  ingest.Job
		want Stage
	}{
		{ingest.Job{Status: ingest.JobQueued}, StageReceived},
		{ingest.Job{Status: ingest.JobRunning}, StageExtracting},
		{ingest.Job{Status: ingest.JobWaiting}, StageExtracting},
		{ingest.Job{Status: ingest.JobSucceeded}, StageCompleted},
		{ingest.Job{Status: ingest.JobSucceeded, ObservationIDs: []string{"o1"}}, StageObservationsSaved},
		{ingest.Job{Status: ingest.JobFailed}, StageFailed},
		{ingest.Job{Status: ingest.JobCancelled}, StageFailed},
	}
	for _, test := range cases {
		if got := ExtractionStage(test.job); got != test.want {
			t.Fatalf("ExtractionStage(%+v) = %q, want %q", test.job, got, test.want)
		}
	}
}

func TestTaskStageSeparatesGovernanceAndAggregation(t *testing.T) {
	cases := []struct {
		job      task.Job
		category Category
		stage    Stage
	}{
		{task.Job{Kind: task.KindGovernObservation, Status: task.StatusQueued}, CategoryGovernance, StageGovernancePending},
		{task.Job{Kind: task.KindGovernObservation, Status: task.StatusSucceeded}, CategoryGovernance, StageMemoryMaterialized},
		{task.Job{Kind: task.KindReviewConflict, Status: task.StatusFailed}, CategoryGovernance, StageFailed},
		{task.Job{Kind: task.KindAggregateL1, Status: task.StatusRunning}, CategoryAggregation, StageAggregationPending},
		{task.Job{Kind: task.KindAggregateL3, Status: task.StatusSucceeded}, CategoryAggregation, StageCompleted},
		{task.Job{Kind: task.KindReconcile, Status: task.StatusSucceeded}, CategoryGovernance, StageCompleted},
	}
	for _, test := range cases {
		if got := TaskCategory(test.job.Kind); got != test.category {
			t.Fatalf("TaskCategory(%q) = %q, want %q", test.job.Kind, got, test.category)
		}
		if got := TaskStage(test.job); got != test.stage {
			t.Fatalf("TaskStage(%+v) = %q, want %q", test.job, got, test.stage)
		}
	}
}
