package httptransport

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const (
	badRequestMessage  = "请求体不是有效的 JSON。"
	notFoundMessage    = "资源不存在。"
	internalMessage    = "内部执行失败。"
	unavailableMessage = "服务暂不可用。"
	conflictMessage    = "冲突或版本已变化，请重新读取。"
)

// Stable conflict error codes for the management adjudication contract.
const (
	codeInvalidResolution = "invalid_resolution"
	codeConflictStale     = "conflict_stale"
	codeRestoreConflict   = "restore_conflict"
)

type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// writeJSON writes one JSON response and sets the contract content type. It
// deliberately adds no Location, ETag or version-control headers.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func writeErrorCode(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, errorResponse{Error: message, Code: code})
}

func (h *handler) writeBadRequest(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, badRequestMessage)
}

func (h *handler) writeNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, notFoundMessage)
}

func (h *handler) writeUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, unavailableMessage)
}

// writeServiceError maps the small, fixed set of synchronous errors from the
// architecture. Model failures never reach this path: they are stored on the
// ingest job and returned by the job query endpoint.
func (h *handler) writeServiceError(w http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, retrieval.ErrInvalidRequest),
		errors.Is(err, governance.ErrInvalidExperience),
		errors.Is(err, governance.ErrInvalidForget):
		h.writeBadRequest(w)
	case errors.Is(err, memory.ErrInvalidResolution),
		errors.Is(err, memory.ErrInvalidTemporal):
		writeErrorCode(w, http.StatusBadRequest, badRequestMessage, codeInvalidResolution)
	case errors.Is(err, memory.ErrRestoreConflict):
		writeErrorCode(w, http.StatusConflict, conflictMessage, codeRestoreConflict)
	case errors.Is(err, memory.ErrConflictStale):
		writeErrorCode(w, http.StatusConflict, conflictMessage, codeConflictStale)
	case errors.Is(err, memory.ErrNotFound),
		errors.Is(err, memory.ErrConflictNotFound),
		errors.Is(err, artifact.ErrNotFound),
		errors.Is(err, governance.ErrExperienceNotFound),
		errors.Is(err, source.ErrNotFound),
		errors.Is(err, ingest.ErrNotFound):
		h.writeNotFound(w)
	default:
		h.logger.Error("http handler failed",
			"method", request.Method,
			"path", request.URL.Path,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, internalMessage)
	}
}

// decodeObject reads exactly one JSON object. Unknown fields remain available to
// the caller and are ignored by the individual handlers. A scalar, null,
// malformed or multi-value body is a JSON decoding failure.
func decodeObject(request *http.Request) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(request.Body)
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("request body is not a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("request body must contain one JSON object")
	}
	return object, nil
}

func loggerOrDiscard(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
