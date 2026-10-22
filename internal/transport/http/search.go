package httptransport

import (
	"net/http"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

func (h *handler) recallMemories(w http.ResponseWriter, request *http.Request) {
	service, ok := h.requireRecall(w)
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
	maxContextTokens, _ := rawInt(object, "max_context_tokens")
	layers := make([]retrieval.Layer, 0)
	_, layersSpecified := object["layers"]
	for _, value := range rawStringSlice(object, "layers") {
		layer := retrieval.Layer(value)
		if !layer.Known() {
			h.writeBadRequest(w)
			return
		}
		layers = append(layers, layer)
	}
	recallRequest := retrieval.RecallRequest{
		Query:            rawString(object, "query"),
		ScopeKey:         rawString(object, "scope_key"),
		Layers:           layers,
		LayersSpecified:  layersSpecified,
		Mode:             mode,
		Match:            retrieval.Match(rawString(object, "match")),
		TopK:             topK,
		Explain:          rawBool(object, "explain"),
		MaxContextTokens: maxContextTokens,
	}
	if filters, ok := rawObject(object, "filters"); ok {
		asOf, valid := rawTimeStrict(filters, "as_of")
		if !valid {
			h.writeBadRequest(w)
			return
		}
		recallRequest.Filters = retrieval.Filters{
			Kinds:         searchKinds(rawStringSlice(filters, "kinds")),
			States:        searchStates(rawStringSlice(filters, "states")),
			AsOf:          asOf,
			CreatedByApp:  rawString(filters, "created_by_app"),
			UpdatedAfter:  rawTime(filters, "updated_after"),
			UpdatedBefore: rawTime(filters, "updated_before"),
		}
	}

	result, err := service.Recall(request.Context(), recallRequest)
	if err != nil {
		h.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, recallFromDomain(result))
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
