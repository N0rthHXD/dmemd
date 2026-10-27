package httptransport

import (
	"net/http"
	"strconv"

	"github.com/N0rthHXD/dmemd/internal/artifact"
)

func (h *handler) listArtifacts(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireArtifacts(w)
	if !ok {
		return
	}
	query, ok := artifactListQuery(request)
	if !ok {
		h.writeBadRequest(w)
		return
	}
	page, err := service.List(request.Context(), query)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactListFromDomain(page))
}

func (h *handler) listArtifactTargets(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireArtifacts(w)
	if !ok {
		return
	}
	query, ok := artifactTargetQuery(request)
	if !ok {
		h.writeBadRequest(w)
		return
	}
	page, err := service.ListTargets(request.Context(), query)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactTargetListFromDomain(page))
}

func artifactListQuery(request *http.Request) (artifact.ListQuery, bool) {
	values := request.URL.Query()
	limit, ok := queryLimit(values.Get("limit"))
	if !ok {
		return artifact.ListQuery{}, false
	}
	query := artifact.ListQuery{
		ScopeKey:   values.Get("scope_key"),
		ReasonCode: values.Get("reason_code"),
		Cursor:     values.Get("cursor"),
		Limit:      limit,
	}
	if kind := values.Get("kind"); kind != "" {
		query.Kind = artifact.Kind(kind)
		if !query.Kind.Known() {
			return artifact.ListQuery{}, false
		}
	}
	if state := values.Get("state"); state != "" {
		query.State = artifact.State(state)
		if !query.State.Known() {
			return artifact.ListQuery{}, false
		}
	}
	return query, true
}

func artifactTargetQuery(request *http.Request) (artifact.TargetQuery, bool) {
	values := request.URL.Query()
	limit, ok := queryLimit(values.Get("limit"))
	if !ok {
		return artifact.TargetQuery{}, false
	}
	query := artifact.TargetQuery{
		ScopeKey:   values.Get("scope_key"),
		ReasonCode: values.Get("reason_code"),
		Cursor:     values.Get("cursor"),
		Limit:      limit,
	}
	if kind := values.Get("kind"); kind != "" {
		query.Kind = artifact.Kind(kind)
		if !query.Kind.Known() {
			return artifact.TargetQuery{}, false
		}
	}
	if status := values.Get("status"); status != "" {
		query.Status = artifact.TargetStatus(status)
		if !query.Status.Known() {
			return artifact.TargetQuery{}, false
		}
	}
	return query, true
}

func queryLimit(value string) (int, bool) {
	if value == "" {
		return 50, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 || parsed > 200 {
		return 0, false
	}
	return parsed, true
}

func (h *handler) getArtifact(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireArtifacts(w)
	if !ok {
		return
	}
	item, err := service.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactDetailFromDomain(item))
}

func (h *handler) getArtifactHistoryEntry(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireArtifacts(w)
	if !ok {
		return
	}
	version, err := strconv.Atoi(request.PathValue("version"))
	if err != nil || version <= 0 {
		h.writeNotFound(w)
		return
	}
	item, err := service.GetVersion(request.Context(), request.PathValue("id"), version)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactDetailFromDomain(item))
}
