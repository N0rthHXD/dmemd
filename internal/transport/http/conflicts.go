package httptransport

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func (h *handler) listConflicts(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireConflicts(w)
	if !ok {
		return
	}
	query := request.URL.Query()
	status := memory.ConflictStatus(strings.TrimSpace(query.Get("status")))
	if status == "" {
		status = memory.ConflictOpen
	}
	if status != memory.ConflictOpen && status != memory.ConflictResolved {
		h.writeBadRequest(w)
		return
	}
	items, err := service.ListConflicts(request.Context(), memory.ConflictListOptions{
		Status: status,
		Limit:  parseLimit(query.Get("limit")),
	})
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, conflictListResponse{Items: conflictsFromDomain(items)})
}

func (h *handler) getConflict(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireConflicts(w)
	if !ok {
		return
	}
	item, err := service.GetConflict(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, conflictFromDomain(item))
}

func (h *handler) resolveConflict(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireConflicts(w)
	if !ok {
		return
	}
	object, err := decodeObject(request)
	if err != nil {
		h.writeBadRequest(w)
		return
	}
	input, valid := parseResolveInput(object)
	if !valid {
		h.writeBadRequest(w)
		return
	}
	input.ConflictID = request.PathValue("id")
	item, err := service.Resolve(request.Context(), input)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, conflictFromDomain(item))
}

func parseResolveInput(object map[string]json.RawMessage) (memory.ResolveInput, bool) {
	input := memory.ResolveInput{
		Action:   memory.ResolutionAction(rawString(object, "action")),
		Revision: 0,
		Reason:   rawString(object, "reason"),
	}
	revision, ok := rawInt(object, "revision")
	if !ok || revision <= 0 {
		return memory.ResolveInput{}, false
	}
	input.Revision = revision
	input.MemberIDs = rawStringSlice(object, "member_ids")
	switch input.Action {
	case memory.ResolutionChoose, memory.ResolutionRejectAll:
	default:
		return memory.ResolveInput{}, false
	}

	if raw, exists := object["member_versions"]; exists {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return memory.ResolveInput{}, false
		}
		for _, item := range items {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(item, &fields); err != nil || fields == nil {
				return memory.ResolveInput{}, false
			}
			memoryID := rawString(fields, "memory_id")
			version, ok := rawInt(fields, "version")
			if memoryID == "" || !ok || version <= 0 {
				return memory.ResolveInput{}, false
			}
			input.MemberVersions = append(input.MemberVersions, memory.ConflictMember{
				MemoryID: memoryID,
				Version:  version,
			})
		}
	}

	return input, true
}
