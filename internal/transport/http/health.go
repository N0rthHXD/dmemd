package httptransport

import "net/http"

type healthResponse struct {
	Status string `json:"status"`
}

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (h *handler) readyz(w http.ResponseWriter, request *http.Request) {
	if h.ready != nil {
		if err := h.ready(request.Context()); err != nil {
			h.logger.Warn("readiness check failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ready"})
}
