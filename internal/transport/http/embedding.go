package httptransport

import (
	"net/http"
)

func (h *handler) embeddingStatus(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireEmbedding(w)
	if !ok {
		return
	}
	status, err := service.Status(request.Context())
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *handler) embeddingProbe(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireEmbedding(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, service.Probe(request.Context()))
}
