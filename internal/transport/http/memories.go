package httptransport

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

func (h *handler) createIngestion(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireIngest(w)
	if !ok {
		return
	}
	object, err := decodeObject(request)
	if err != nil {
		h.writeBadRequest(w)
		return
	}
	submission := ingest.Submission{
		AppID:      rawString(object, "app_id"),
		ExternalID: rawString(object, "external_id"),
		ScopeKey:   rawString(object, "scope_key"),
		Content:    object["content"],
	}
	receipt, err := service.Submit(request.Context(), submission)
	if err != nil {
		if errors.Is(err, ingest.ErrInvalidContent) {
			h.writeBadRequest(w)
			return
		}
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusAccepted, submitResponse{
		SourceID: receipt.SourceID,
		JobID:    receipt.JobID,
		Status:   string(receipt.Status),
	})
}

func (h *handler) listMemories(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireMemories(w)
	if !ok {
		return
	}
	query := request.URL.Query()
	updatedAfter, validAfter := queryTimeStrict(query.Get("updated_after"))
	updatedBefore, validBefore := queryTimeStrict(query.Get("updated_before"))
	if !validAfter || !validBefore {
		h.writeBadRequest(w)
		return
	}
	options := memory.ListOptions{
		UpdatedAfter:  updatedAfter,
		UpdatedBefore: updatedBefore,
		Limit:         parseLimit(query.Get("limit")),
	}
	kinds, emptyKinds := parseKinds(query["kinds"])
	states, emptyStates := parseStates(query["states"])
	assertions, emptyAssertions := parseAssertionStatuses(query["assertion_status"])
	if emptyKinds || emptyStates || emptyAssertions {
		writeJSON(w, http.StatusOK, memoryListResponse{Items: []memoryDTO{}})
		return
	}
	options.Kinds = kinds
	options.States = states
	options.AssertionStatus = assertions

	items, err := service.List(request.Context(), options)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, memoryListResponse{Items: memoriesFromDomain(items)})
}

func (h *handler) getMemory(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireMemories(w)
	if !ok {
		return
	}
	id := request.PathValue("id")
	if detailService, ok := service.(memory.DetailService); ok {
		item, err := detailService.Detail(request.Context(), id)
		if err != nil {
			h.writeServiceError(w, request, err)
			return
		}
		writeJSON(w, http.StatusOK, memoryDetailFromDomain(item))
		return
	}
	item, err := service.Get(request.Context(), id)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, memoryDetailDTO{
		memoryDTO:         memoryFromDomain(item),
		ObservationIDs:    []string{},
		Relations:         []relationDTO{},
		ConflictIDs:       []string{},
		DerivedDependents: []derivedDependentDTO{},
	})
}

func (h *handler) setMemoryState(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireMemories(w)
	if !ok {
		return
	}
	object, err := decodeObject(request)
	if err != nil {
		h.writeBadRequest(w)
		return
	}
	// Only active/archived are accepted. Content, title, kind and version fields
	// are ignored, and deleted must use DELETE, so PATCH can never become a
	// content-write or delete shortcut.
	state := memory.State(rawString(object, "state"))
	if state != memory.StateActive && state != memory.StateArchived {
		state = ""
	}
	item, err := service.UpdateState(request.Context(), request.PathValue("id"), state)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, memoryFromDomain(item))
}

func (h *handler) deleteMemory(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireMemories(w)
	if !ok {
		return
	}
	item, err := service.Delete(request.Context(), request.PathValue("id"))
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, deleteResponse{ID: item.ID, State: string(item.State)})
}

func (h *handler) purgeMemory(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireMemories(w)
	if !ok {
		return
	}
	id := request.PathValue("id")
	if err := service.Purge(request.Context(), id); err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, purgeResponse{ID: id, Purged: true})
}

func (h *handler) listMemoryHistory(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireMemories(w)
	if !ok {
		return
	}
	limit := parseLimit(request.URL.Query().Get("limit"))
	items, err := service.ListVersions(request.Context(), request.PathValue("id"), limit)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, revisionListResponse{Items: revisionsFromDomain(items)})
}

func (h *handler) getMemoryHistoryEntry(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireMemories(w)
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
	writeJSON(w, http.StatusOK, revisionFromDomain(item))
}

func (h *handler) restoreMemoryVersion(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireMemories(w)
	if !ok {
		return
	}
	version, err := strconv.Atoi(request.PathValue("version"))
	if err != nil || version <= 0 {
		h.writeNotFound(w)
		return
	}
	item, err := service.Restore(request.Context(), memory.RestoreInput{
		MemoryID: request.PathValue("id"),
		Version:  version,
	})
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, memoryFromDomain(item))
}

func (h *handler) getMemoryEvidence(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireMemories(w)
	if !ok {
		return
	}
	version := 0
	if raw := strings.TrimSpace(request.URL.Query().Get("version")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			version = parsed
		}
	}
	items, err := service.GetEvidence(request.Context(), request.PathValue("id"), version)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, evidenceListResponse{Items: evidenceFromDomainList(items)})
}
