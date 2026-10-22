package httptransport

import (
	"net/http"
	"strconv"
	"strings"
)

func (h *handler) getSkillContent(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireSkills(w)
	if !ok {
		return
	}
	version := 0
	if raw := strings.TrimSpace(request.URL.Query().Get("version")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			h.writeBadRequest(w)
			return
		}
		version = parsed
	}
	item, err := service.Get(request.Context(), request.PathValue("id"), version)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, skillContentFromDomain(item))
}
