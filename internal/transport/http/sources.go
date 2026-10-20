package httptransport

import "net/http"

func (h *handler) getSource(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireSources(w)
	if !ok {
		return
	}
	item, err := service.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, sourceFromDomain(item))
}

func (h *handler) redactSource(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireSources(w)
	if !ok {
		return
	}
	result, err := service.Redact(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, redactFromDomain(result))
}
