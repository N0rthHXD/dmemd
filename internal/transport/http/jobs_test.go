package httptransport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/jobs"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

type stubJobs struct {
	jobs.ServiceAPI
	get       func(context.Context, string) (jobs.ExtractionJob, error)
	list      func(context.Context, jobs.ListOptions) ([]jobs.ExtractionJob, error)
	getTask   func(context.Context, string) (jobs.TaskJob, error)
	listTasks func(context.Context, jobs.TaskListOptions) ([]jobs.TaskJob, error)
}

func (s stubJobs) Get(ctx context.Context, id string) (jobs.ExtractionJob, error) {
	return s.get(ctx, id)
}

func (s stubJobs) List(ctx context.Context, options jobs.ListOptions) ([]jobs.ExtractionJob, error) {
	return s.list(ctx, options)
}

func (s stubJobs) GetTask(ctx context.Context, id string) (jobs.TaskJob, error) {
	return s.getTask(ctx, id)
}

func (s stubJobs) ListTasks(ctx context.Context, options jobs.TaskListOptions) ([]jobs.TaskJob, error) {
	return s.listTasks(ctx, options)
}

func TestJobRoutesReportSeparatedStages(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	extraction := jobs.ExtractionJob{
		Job: ingest.Job{
			ID: "job-1", SourceID: "source-1", Status: ingest.JobSucceeded,
			ObservationIDs: []string{"observation-1"}, CreatedAt: now, UpdatedAt: now,
			ExtractionStats: ingest.ExtractionStats{Items: 1, Observations: 1},
		},
		Stage: jobs.StageObservationsSaved,
	}
	aggregation := jobs.TaskJob{
		Job: task.Job{
			ID: "task-1", Kind: task.KindAggregateL1, TargetKey: "l1:target",
			Status: task.StatusRunning, Attempts: 1, Revision: 2,
			CreatedAt: now, UpdatedAt: now,
		},
		Category: jobs.CategoryAggregation,
		Stage:    jobs.StageAggregationPending,
	}
	services := Services{Jobs: stubJobs{
		get: func(_ context.Context, id string) (jobs.ExtractionJob, error) {
			if id != "job-1" {
				return jobs.ExtractionJob{}, ingest.ErrNotFound
			}
			return extraction, nil
		},
		list: func(_ context.Context, options jobs.ListOptions) ([]jobs.ExtractionJob, error) {
			if options.Status != ingest.JobSucceeded || options.Limit != 10 {
				t.Fatalf("options = %+v", options)
			}
			return []jobs.ExtractionJob{extraction}, nil
		},
		listTasks: func(_ context.Context, options jobs.TaskListOptions) ([]jobs.TaskJob, error) {
			if options.Kind != task.KindAggregateL1 || options.Status != task.StatusRunning || options.Limit != 20 {
				t.Fatalf("task options = %+v", options)
			}
			return []jobs.TaskJob{aggregation}, nil
		},
		getTask: func(_ context.Context, id string) (jobs.TaskJob, error) {
			if id != "task-1" {
				return jobs.TaskJob{}, task.ErrNotFound
			}
			return aggregation, nil
		},
	}}

	response := serve(t, services, nil, httptest.NewRequest(http.MethodGet,
		"/v1/jobs?status=succeeded&limit=10", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"stage":"observations_saved"`) ||
		!strings.Contains(response.Body.String(), `"observation_ids":["observation-1"]`) {
		t.Fatalf("list jobs = %d %s", response.Code, response.Body.String())
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/jobs/job-1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"stage":"observations_saved"`) {
		t.Fatalf("get job = %d %s", response.Code, response.Body.String())
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/jobs/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing job = %d", response.Code)
	}

	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet,
		"/v1/governance-jobs?kind=aggregate_l1&status=running&limit=20", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"category":"aggregation"`) ||
		!strings.Contains(response.Body.String(), `"stage":"aggregation_pending"`) {
		t.Fatalf("list tasks = %d %s", response.Code, response.Body.String())
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/governance-jobs/task-1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"kind":"aggregate_l1"`) {
		t.Fatalf("get task = %d %s", response.Code, response.Body.String())
	}
	response = serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/governance-jobs/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing task = %d", response.Code)
	}
}

func TestJobRoutesRejectUnknownFilters(t *testing.T) {
	services := Services{Jobs: stubJobs{
		list: func(context.Context, jobs.ListOptions) ([]jobs.ExtractionJob, error) {
			t.Fatal("list called for invalid filter")
			return nil, nil
		},
		listTasks: func(context.Context, jobs.TaskListOptions) ([]jobs.TaskJob, error) {
			t.Fatal("task list called for invalid filter")
			return nil, nil
		},
	}}
	for _, path := range []string{"/v1/jobs?status=unknown", "/v1/governance-jobs?kind=unknown", "/v1/governance-jobs?status=unknown"} {
		response := serve(t, services, nil, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
}

type stubMemoryDetail struct {
	memory.MemoryService
	detail func(context.Context, string) (memory.Detail, error)
}

func (s stubMemoryDetail) Detail(ctx context.Context, id string) (memory.Detail, error) {
	return s.detail(ctx, id)
}

func TestMemoryDetailExposesCanonicalReferences(t *testing.T) {
	now := time.Date(2026, 10, 30, 13, 0, 0, 0, time.UTC)
	detail := memory.Detail{
		Memory: memory.Memory{
			ID: "memory-1", Kind: memory.KindFact, Title: "存储", Content: "项目使用 SQLite。",
			State: memory.StateActive, AssertionStatus: memory.AssertionPending, Version: 2,
			ScopeKey: "project:dmemd", CreatedAt: now, UpdatedAt: now,
		},
		ObservationIDs: []string{"observation-1"},
		Relations: []memory.RelationRef{{
			ID: "relation-1", Kind: "supports", FromType: "observation", FromID: "observation-1",
			ToType: "memory", ToID: "memory-1", ToVersion: 2, CreatedAt: now,
		}},
		ConflictIDs: []string{"conflict-1"},
		DerivedDependents: []memory.DerivedDependent{{
			ID: "artifact-1", Layer: "l1", Kind: "local_summary", TargetKey: "l1:target",
			ScopeKey: "project:dmemd", Version: 1, State: "active",
		}},
	}
	services := Services{Memories: stubMemoryDetail{
		detail: func(_ context.Context, id string) (memory.Detail, error) {
			if id != "memory-1" {
				return memory.Detail{}, memory.ErrNotFound
			}
			return detail, nil
		},
	}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/memories/memory-1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	for _, want := range []string{
		`"observation_ids":["observation-1"]`,
		`"conflict_ids":["conflict-1"]`,
		`"from_type":"observation"`,
		`"derived_dependents":[{"id":"artifact-1","layer":"l1"`,
		`"assertion_status":"pending"`,
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("body missing %s: %s", want, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), "evolution") || strings.Contains(response.Body.String(), "fact_group") {
		t.Fatalf("legacy fields leaked: %s", response.Body.String())
	}
}

func TestJobsNotFoundMapping(t *testing.T) {
	services := Services{Jobs: stubJobs{
		get: func(context.Context, string) (jobs.ExtractionJob, error) {
			return jobs.ExtractionJob{}, ingest.ErrNotFound
		},
	}}
	response := serve(t, services, nil, httptest.NewRequest(http.MethodGet, "/v1/ingest-jobs/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestRelationDTOKeepsTypedEndpoints(t *testing.T) {
	now := time.Now().UTC()
	dto := relationFromGovernance(governance.Relation{
		ID: "r1", Kind: governance.RelationDuplicate,
		FromType: governance.EndpointObservation, FromID: "o1",
		ToType: governance.EndpointMemory, ToID: "m1", ToVersion: 3, CreatedAt: now,
	})
	if dto.FromType != "observation" || dto.ToType != "memory" || dto.ToVersion != 3 {
		t.Fatalf("dto = %+v", dto)
	}
}
