package httptransport

import "net/http"

func (h *handler) getJob(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireIngest(w)
	if !ok {
		return
	}
	item, err := service.GetJob(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, jobFromDomain(item))
}
