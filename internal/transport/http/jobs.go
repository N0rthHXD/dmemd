package httptransport

import (
	"net/http"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/jobs"
)

// listJobs returns recent extraction jobs. Their stage stops at
// observations_saved/completed by design; governance and aggregation progress
// is queried through the governance-jobs endpoints instead.
func (h *handler) listJobs(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireJobs(w)
	if !ok {
		return
	}
	query := request.URL.Query()
	options := jobs.ListOptions{Limit: queryLimitOrZero(query.Get("limit"))}
	if status := ingest.JobStatus(strings.TrimSpace(query.Get("status"))); status != "" {
		switch status {
		case ingest.JobQueued, ingest.JobRunning, ingest.JobWaiting,
			ingest.JobSucceeded, ingest.JobFailed, ingest.JobCancelled:
			options.Status = status
		default:
			h.writeBadRequest(w)
			return
		}
	}
	items, err := service.List(request.Context(), options)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, jobListFromDomain(items))
}

// getJob returns one extraction job with its extraction-only stage.
func (h *handler) getJob(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireJobs(w)
	if !ok {
		return
	}
	item, err := service.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, jobFromDomain(item))
}

// listGovernanceJobs returns governance and aggregation tasks. Category and
// stage are computed from the task kind/status and never claim that another
// worker ran synchronously.
func (h *handler) listGovernanceJobs(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireJobs(w)
	if !ok {
		return
	}
	query := request.URL.Query()
	options := jobs.TaskListOptions{
		Category:  jobs.Category(strings.TrimSpace(query.Get("category"))),
		TargetKey: strings.TrimSpace(query.Get("target_key")),
		Limit:     queryLimitOrZero(query.Get("limit")),
	}
	if options.Category != "" && !options.Category.Known() {
		h.writeBadRequest(w)
		return
	}
	if kind := task.Kind(strings.TrimSpace(query.Get("kind"))); kind != "" {
		if !kind.Known() {
			h.writeBadRequest(w)
			return
		}
		options.Kind = kind
	}
	if status := task.Status(strings.TrimSpace(query.Get("status"))); status != "" {
		switch status {
		case task.StatusQueued, task.StatusRunning, task.StatusWaiting,
			task.StatusSucceeded, task.StatusFailed, task.StatusCancelled:
			options.Status = status
		default:
			h.writeBadRequest(w)
			return
		}
	}
	items, err := service.ListTasks(request.Context(), options)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, governanceJobListFromDomain(items))
}

// getGovernanceJob returns one governance or aggregation task.
func (h *handler) getGovernanceJob(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireJobs(w)
	if !ok {
		return
	}
	item, err := service.GetTask(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, governanceJobFromDomain(item))
}
