package httptransport

import (
	"net/http"
	"strconv"
)

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
