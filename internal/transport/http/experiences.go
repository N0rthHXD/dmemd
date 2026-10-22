package httptransport

import (
	"net/http"

	"github.com/N0rthHXD/dmemd/internal/governance"
)

func (h *handler) createExperience(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireExperiences(w)
	if !ok {
		return
	}
	object, err := decodeObject(request)
	if err != nil {
		h.writeBadRequest(w)
		return
	}
	skillVersion, _ := rawInt(object, "skill_version")
	submission := governance.ExperienceSubmission{
		RunID:        rawString(object, "run_id"),
		AppID:        rawString(object, "app_id"),
		ExternalID:   rawString(object, "external_id"),
		ScopeKey:     rawString(object, "scope_key"),
		Goal:         rawString(object, "goal"),
		Conditions:   rawStringSlice(object, "conditions"),
		Actions:      rawStringSlice(object, "actions"),
		Outcome:      governance.ExperienceOutcome(rawString(object, "outcome")),
		SkillID:      rawString(object, "skill_id"),
		SkillVersion: skillVersion,
		Content:      object["content"],
	}
	receipt, err := service.Submit(request.Context(), submission)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusAccepted, experienceReceiptFromDomain(receipt))
}

func (h *handler) retractExperience(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireExperiences(w)
	if !ok {
		return
	}
	receipt, err := service.Retract(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, experienceReceiptFromDomain(receipt))
}
