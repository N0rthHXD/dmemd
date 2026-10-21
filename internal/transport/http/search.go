package httptransport

import (
	"net/http"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

func (h *handler) searchMemories(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireSearch(w)
	if !ok {
		return
	}
	object, err := decodeObject(request)
	if err != nil {
		h.writeBadRequest(w)
		return
	}

	mode := retrieval.Mode(rawString(object, "mode"))
	switch mode {
	case "":
		mode = retrieval.ModeKeyword
	case retrieval.ModeKeyword, retrieval.ModeHybrid:
	default:
		h.writeBadRequest(w)
		return
	}
	topK, _ := rawInt(object, "top_k")
	if topK > retrieval.MaxTopK {
		h.writeBadRequest(w)
		return
	}
	searchRequest := retrieval.Request{
		Query:   rawString(object, "query"),
		Mode:    mode,
		Match:   retrieval.Match(rawString(object, "match")),
		TopK:    topK,
		Explain: rawBool(object, "explain"),
	}
	if filters, ok := rawObject(object, "filters"); ok {
		searchRequest.Filters = retrieval.Filters{
			Kinds:         searchKinds(rawStringSlice(filters, "kinds")),
			States:        searchStates(rawStringSlice(filters, "states")),
			CreatedByApp:  rawString(filters, "created_by_app"),
			UpdatedAfter:  rawTime(filters, "updated_after"),
			UpdatedBefore: rawTime(filters, "updated_before"),
		}
	}

	result, err := service.Search(request.Context(), searchRequest)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, searchResultFromDomain(result))
}

func searchKinds(values []string) []memory.Kind {
	result := make([]memory.Kind, 0, len(values))
	for _, value := range values {
		result = append(result, memory.Kind(value))
	}
	return result
}

func searchStates(values []string) []memory.State {
	result := make([]memory.State, 0, len(values))
	for _, value := range values {
		result = append(result, memory.State(value))
	}
	return result
}
