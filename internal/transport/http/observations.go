package httptransport

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/observation"
)

// listObservations returns L0 raw observations. It is a read-only view: clients
// cannot edit observation content, which is fixed by the extraction stage.
func (h *handler) listObservations(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireObservations(w)
	if !ok {
		return
	}
	query := request.URL.Query()
	options := observation.ListOptions{
		SourceID: strings.TrimSpace(query.Get("source_id")),
		ScopeKey: strings.TrimSpace(query.Get("scope_key")),
		Limit:    queryLimitOrZero(query.Get("limit")),
	}
	if kind := observation.Kind(strings.TrimSpace(query.Get("kind"))); kind != "" {
		if !kind.Known() {
			h.writeBadRequest(w)
			return
		}
		options.Kind = kind
	}
	if status := observation.Status(strings.TrimSpace(query.Get("status"))); status != "" {
		if !status.Known() {
			h.writeBadRequest(w)
			return
		}
		options.Status = status
	}
	items, err := service.List(request.Context(), options)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	result := make([]observationDTO, 0, len(items))
	for _, item := range items {
		result = append(result, observationFromDomain(item))
	}
	writeJSON(w, http.StatusOK, observationListResponse{Items: result})
}

// getObservation returns one L0 raw observation.
func (h *handler) getObservation(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireObservations(w)
	if !ok {
		return
	}
	item, err := service.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, observationFromDomain(item))
}

// getObservationEvidence returns the code-generated evidence bound to one
// observation. Models never supply these quotes or offsets.
func (h *handler) getObservationEvidence(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireObservations(w)
	if !ok {
		return
	}
	items, err := service.Evidence(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, evidenceListResponse{Items: evidenceFromDomainList(items)})
}

// getObservationRelations returns the typed relations touching one observation.
func (h *handler) getObservationRelations(w http.ResponseWriter, request *http.Request) {
	observations, ok := h.requireObservations(w)
	if !ok {
		return
	}
	relations, ok := h.requireRelations(w)
	if !ok {
		return
	}
	id := request.PathValue("id")
	if _, err := observations.Get(request.Context(), id); err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	items, err := relations.ListForEndpoint(request.Context(),
		governance.EndpointObservation, id)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, relationListResponse{Items: relationsFromGovernance(items)})
}

func queryLimitOrZero(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}
