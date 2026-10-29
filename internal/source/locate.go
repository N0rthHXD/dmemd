package source

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
)

// Span error codes are fixed, content-free diagnostics.
const (
	SpanIDsEmpty       = "span_ids_empty"
	SpanSegmentUnknown = "span_segment_unknown"
	SpanContextOnly    = "span_context_only"
)

// Sentinel errors identify the three span-binding failures callers must handle.
var (
	ErrEmptySpans     = errors.New("span ids empty")
	ErrUnknownSegment = errors.New("source segment unknown")
	ErrContextSegment = errors.New("context segment is not selectable")
)

// SpanError describes why a span selection cannot be bound to the current
// source view. SegmentID is safe to log; quote text is never included.
type SpanError struct {
	Code      string
	SegmentID string
}

func (e *SpanError) Error() string {
	if e == nil {
		return "span selection invalid"
	}
	if e.SegmentID == "" {
		return fmt.Sprintf("span selection invalid: %s", e.Code)
	}
	return fmt.Sprintf("span selection invalid: %s (%s)", e.Code, e.SegmentID)
}

// Is maps the detailed error onto the stable sentinel errors.
func (e *SpanError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrEmptySpans:
		return e.Code == SpanIDsEmpty
	case ErrUnknownSegment:
		return e.Code == SpanSegmentUnknown
	case ErrContextSegment:
		return e.Code == SpanContextOnly
	default:
		return false
	}
}

// ResolveSegments maps model-selected span IDs to the current source segments.
// Only IDs present in view.Segments are accepted; context segments are visible
// for understanding but can never become evidence. Duplicate IDs are collapsed
// while the caller's first-selection order is preserved.
func ResolveSegments(view View, ids []string) ([]Segment, error) {
	unique, err := dedupeSpanIDs(ids)
	if err != nil {
		return nil, err
	}
	current := make(map[string]Segment, len(view.Segments))
	for _, segment := range view.Segments {
		current[segment.ID] = segment
	}
	context := make(map[string]struct{}, len(view.ContextSegments))
	for _, segment := range view.ContextSegments {
		context[segment.ID] = struct{}{}
	}
	segments := make([]Segment, 0, len(unique))
	for _, id := range unique {
		segment, ok := current[id]
		if !ok {
			if _, isContext := context[id]; isContext {
				return nil, &SpanError{Code: SpanContextOnly, SegmentID: id}
			}
			return nil, &SpanError{Code: SpanSegmentUnknown, SegmentID: id}
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

// EvidenceForSegments builds one code-owned evidence entry per selected segment.
// It never accepts model-supplied quote or offset values: the quote, message ID
// and location are copied from the deterministic segment. A redacted source
// keeps only the source/segment association, because its original text must not
// be copied again.
func EvidenceForSegments(src Source, ids []string, at time.Time) ([]Evidence, error) {
	unique, err := dedupeSpanIDs(ids)
	if err != nil {
		return nil, err
	}
	if src.Redacted {
		return EvidenceForResolvedSegments(src, nil, unique, at), nil
	}
	segments, err := ResolveSegments(BuildView(src.Content), unique)
	if err != nil {
		return nil, err
	}
	return EvidenceForResolvedSegments(src, segments, unique, at), nil
}

// EvidenceForResolvedSegments builds evidence from segments that the caller
// already resolved against the active source view. It exists so extraction can
// bind evidence to exactly the selectable current-window segments; context-only
// segments can never reach persistence through this path.
//
// Quotes, message identifiers and locations are copied verbatim from the
// resolved segment. A redacted source keeps only the source/segment
// association. Duplicate segment IDs are collapsed.
func EvidenceForResolvedSegments(src Source, segments []Segment, ids []string, at time.Time) []Evidence {
	at = at.UTC()
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	byID := make(map[string]Segment, len(segments))
	for _, segment := range segments {
		byID[segment.ID] = segment
	}
	redactedAt := at
	if src.RedactedAt != nil {
		redactedAt = src.RedactedAt.UTC()
	}
	items := make([]Evidence, 0, len(unique))
	for _, id := range unique {
		if src.Redacted {
			items = append(items, Evidence{
				ID:         idgen.NewUUID(),
				SourceID:   src.ID,
				SegmentID:  id,
				Redacted:   true,
				RedactedAt: &redactedAt,
				CreatedAt:  at,
			})
			continue
		}
		segment, ok := byID[id]
		if !ok {
			continue
		}
		location := Location{Field: segment.Field, Start: segment.Start, End: segment.End}
		items = append(items, Evidence{
			ID:        idgen.NewUUID(),
			SourceID:  src.ID,
			SegmentID: segment.ID,
			MessageID: segment.MessageID,
			Quote:     segment.Text,
			Location:  &location,
			CreatedAt: at,
		})
	}
	return items
}

func dedupeSpanIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, &SpanError{Code: SpanIDsEmpty}
	}
	result := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, &SpanError{Code: SpanIDsEmpty}
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	if len(result) == 0 {
		return nil, &SpanError{Code: SpanIDsEmpty}
	}
	return result, nil
}

func orderedKeys(value map[string]any, preferred []string) []string {
	seen := make(map[string]struct{}, len(value))
	keys := make([]string, 0, len(value))
	for _, key := range preferred {
		if _, ok := value[key]; ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	remaining := make([]string, 0, len(value)-len(keys))
	for key := range value {
		if _, ok := seen[key]; !ok {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	return append(keys, remaining...)
}

func appendPath(path []string, part string) []string {
	result := make([]string, len(path)+1)
	copy(result, path)
	result[len(path)] = part
	return result
}
