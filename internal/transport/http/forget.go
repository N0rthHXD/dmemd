package httptransport

import (
	"net/http"

	"github.com/N0rthHXD/dmemd/internal/governance"
)

func (h *handler) forget(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireForget(w)
	if !ok {
		return
	}
	object, err := decodeObject(request)
	if err != nil {
		h.writeBadRequest(w)
		return
	}
	result, err := service.Forget(request.Context(), governance.ForgetRequest{
		MemoryIDs:     rawStringSlice(object, "memory_ids"),
		SourceIDs:     rawStringSlice(object, "source_ids"),
		ExperienceIDs: rawStringSlice(object, "experience_ids"),
	})
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, forgetResponse{
		MemoryIDs:     nonNilStrings(result.MemoryIDs),
		SourceIDs:     nonNilStrings(result.SourceIDs),
		ExperienceIDs: nonNilStrings(result.ExperienceIDs),
	})
}
