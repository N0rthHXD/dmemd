package jobs

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
)

// ErrTaskNotFound is returned for an unknown governance or aggregation task.
var ErrTaskNotFound = task.ErrNotFound

// JobReader is the extraction-job read capability. The concrete SQLite store
// implements both methods.
type JobReader interface {
	GetJob(ctx context.Context, id string) (ingest.Job, error)
	ListJobs(ctx context.Context, status ingest.JobStatus, limit int) ([]ingest.Job, error)
}

// TaskReader is the governance/aggregation task read capability.
type TaskReader interface {
	GetGovernanceJob(ctx context.Context, id string) (task.Job, error)
	ListGovernanceJobs(ctx context.Context, options TaskListOptions) ([]task.Job, error)
}

// ServiceAPI is the read-only job capability exposed to HTTP and the UI.
type ServiceAPI interface {
	Get(ctx context.Context, id string) (ExtractionJob, error)
	List(ctx context.Context, options ListOptions) ([]ExtractionJob, error)
	GetTask(ctx context.Context, id string) (TaskJob, error)
	ListTasks(ctx context.Context, options TaskListOptions) ([]TaskJob, error)
}

// Service assembles the extraction and governance job readers.
type Service struct {
	jobs  JobReader
	tasks TaskReader
}

// NewService assembles the job read service. A nil reader makes only the
// corresponding methods unavailable.
func NewService(jobs JobReader, tasks TaskReader) *Service {
	return &Service{jobs: jobs, tasks: tasks}
}

var _ ServiceAPI = (*Service)(nil)

// Get returns one extraction job with its extraction-only stage.
func (s *Service) Get(ctx context.Context, id string) (ExtractionJob, error) {
	if s == nil || s.jobs == nil {
		return ExtractionJob{}, errors.New("jobs: extraction reader not configured")
	}
	item, err := s.jobs.GetJob(ctx, id)
	if err != nil {
		return ExtractionJob{}, err
	}
	return ExtractionJob{Job: item, Stage: ExtractionStage(item)}, nil
}

// List returns extraction jobs in newest-first durable order.
func (s *Service) List(ctx context.Context, options ListOptions) ([]ExtractionJob, error) {
	if s == nil || s.jobs == nil {
		return nil, errors.New("jobs: extraction reader not configured")
	}
	items, err := s.jobs.ListJobs(ctx, options.Status, options.Limit)
	if err != nil {
		return nil, err
	}
	result := make([]ExtractionJob, 0, len(items))
	for _, item := range items {
		result = append(result, ExtractionJob{Job: item, Stage: ExtractionStage(item)})
	}
	return result, nil
}

// GetTask returns one governance or aggregation task with its own stage.
func (s *Service) GetTask(ctx context.Context, id string) (TaskJob, error) {
	if s == nil || s.tasks == nil {
		return TaskJob{}, errors.New("jobs: task reader not configured")
	}
	item, err := s.tasks.GetGovernanceJob(ctx, id)
	if err != nil {
		return TaskJob{}, err
	}
	return TaskJob{Job: item, Category: TaskCategory(item.Kind), Stage: TaskStage(item)}, nil
}

// ListTasks returns governance and aggregation tasks in newest-first order.
func (s *Service) ListTasks(ctx context.Context, options TaskListOptions) ([]TaskJob, error) {
	if s == nil || s.tasks == nil {
		return nil, errors.New("jobs: task reader not configured")
	}
	items, err := s.tasks.ListGovernanceJobs(ctx, options)
	if err != nil {
		return nil, err
	}
	result := make([]TaskJob, 0, len(items))
	for _, item := range items {
		result = append(result, TaskJob{
			Job:      item,
			Category: TaskCategory(item.Kind),
			Stage:    TaskStage(item),
		})
	}
	return result, nil
}
