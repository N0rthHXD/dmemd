package source

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Segment is one stable, addressable string fragment of a source. Segment IDs
// are deterministic: the same source always produces the same IDs, so an
// extraction retry, a repair request and the original validation all refer to
// the same fragment. The original JSON is never modified.
type Segment struct {
	ID         string `json:"segment_id"`
	MessageID  string `json:"message_id,omitempty"`
	Role       string `json:"role,omitempty"`
	OccurredAt string `json:"occurred_at,omitempty"`
	Field      string `json:"field,omitempty"`
	Start      int    `json:"-"`
	End        int    `json:"-"`
	Text       string `json:"text"`
}

// View is the deterministic model-facing projection of one source. It contains
// no internal IDs other than segment IDs and never replaces the stored source.
type View struct {
	Segments []Segment `json:"segments"`
}

// Chunk is a bounded extraction window. Segments keep their source-wide IDs;
// ContextSegments carry a small tail of the previous window so cross-chunk
// references can be resolved without re-extracting that text. Chunks are
// deterministic for one view and maximum rune budget, so retries and repairs see
// the same boundaries.
type Chunk struct {
	Index           int
	Segments        []Segment
	ContextSegments []Segment
	Oversized       bool
}

// Chunks splits a view into deterministic bounded windows without splitting an
// individual source string. A single oversized string stays in one window and
// is marked Oversized so the caller can emit an explicit diagnostic rather than
// silently truncate it.
func (v View) Chunks(maxRunes int) []Chunk {
	if len(v.Segments) == 0 {
		return nil
	}
	if maxRunes <= 0 {
		return []Chunk{{Segments: append([]Segment(nil), v.Segments...)}}
	}
	chunks := make([]Chunk, 0, len(v.Segments))
	current := make([]Segment, 0, 8)
	currentRunes := 0
	oversized := false
	flush := func() {
		if len(current) == 0 {
			return
		}
		chunks = append(chunks, Chunk{
			Index:     len(chunks),
			Segments:  append([]Segment(nil), current...),
			Oversized: oversized,
		})
		current = current[:0]
		currentRunes = 0
		oversized = false
	}
	for _, segment := range v.Segments {
		length := utf8.RuneCountInString(segment.Text)
		if len(current) > 0 && currentRunes+length > maxRunes {
			flush()
		}
		if len(current) == 0 && length > maxRunes {
			oversized = true
		}
		current = append(current, segment)
		currentRunes += length
	}
	flush()

	// Keep at most a quarter of the window as read-only context from the end of
	// the previous window. This is small enough to preserve the per-call budget
	// while still letting the model resolve a pronoun or "the previous item".
	contextBudget := maxRunes / 4
	if contextBudget < maxSegmentRunes {
		contextBudget = maxSegmentRunes
	}
	if contextBudget > maxRunes {
		contextBudget = maxRunes
	}
	if contextBudget > 4096 {
		contextBudget = 4096
	}
	for index := 1; index < len(chunks); index++ {
		previous := chunks[index-1].Segments
		used := 0
		start := len(previous)
		for start > 0 {
			length := utf8.RuneCountInString(previous[start-1].Text)
			if length > contextBudget || (used > 0 && used+length > contextBudget) {
				break
			}
			start--
			used += length
			if used >= contextBudget {
				break
			}
		}
		if start < len(previous) {
			chunks[index].ContextSegments = append([]Segment(nil), previous[start:]...)
		}
	}
	return chunks
}

// EvidenceReference is the model-facing evidence protocol. The model cites a
// visible segment ID and a verbatim quote. Start and End are optional rune
// offsets relative to the segment text and are only used to disambiguate a
// quote that occurs more than once in the same segment.
type EvidenceReference struct {
	SegmentID string `json:"segment_id,omitempty"`
	Quote     string `json:"quote,omitempty"`
	Start     *int   `json:"start,omitempty"`
	End       *int   `json:"end,omitempty"`
}

// Evidence error codes are fixed and safe to return in diagnostics.
const (
	EvidenceEmptyQuote     = "evidence_quote_empty"
	EvidenceSegmentUnknown = "evidence_segment_unknown"
	EvidenceQuoteNotFound  = "evidence_quote_not_found"
	EvidenceQuoteAmbiguous = "evidence_quote_ambiguous"
	EvidenceOffsetsInvalid = "evidence_offsets_invalid"
)

// EvidenceError describes why a model evidence reference cannot be resolved.
type EvidenceError struct {
	Code       string
	Segment    string
	Occurrence int
}

func (e *EvidenceError) Error() string {
	if e == nil {
		return "evidence reference invalid"
	}
	return fmt.Sprintf("evidence reference invalid: %s", e.Code)
}

// ResolvedEvidence is a reference mapped back to the stored source.
type ResolvedEvidence struct {
	Segment  Segment
	Location Location
	Recovery string
}

const maxSegmentRunes = 4000

// BuildView decodes the source and assigns deterministic segment IDs. Objects
// are traversed in key order, arrays in index order and strings become
// segments. A long string is split into stable sentence-aware subsegments so a
// single field cannot bypass the bounded extraction window; Start/End keep the
// rune offset in the original field. Message IDs and roles are copied as
// auxiliary context; when a client message ID is missing or duplicated the
// segment ID remains unique.
func BuildView(content json.RawMessage) View {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return View{Segments: []Segment{}}
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return View{Segments: []Segment{}}
	}
	segments := make([]Segment, 0, 8)
	walkView(root, nil, messageContext{}, &segments)
	for index := range segments {
		segments[index].ID = fmt.Sprintf("seg-%04d", index+1)
	}
	return View{Segments: segments}
}

type segmentPart struct {
	text  string
	start int
	end   int
}

func splitSegmentText(text string) []segmentPart {
	runes := []rune(text)
	if len(runes) == 0 {
		return []segmentPart{{text: "", start: 0, end: 0}}
	}
	parts := make([]segmentPart, 0, (len(runes)+maxSegmentRunes-1)/maxSegmentRunes)
	for start := 0; start < len(runes); {
		end := start + maxSegmentRunes
		if end > len(runes) {
			end = len(runes)
		} else {
			// Prefer a sentence boundary in the second half of the window.
			lower := start + maxSegmentRunes/2
			for index := end; index > lower; index-- {
				if strings.ContainsRune("。！？!?；;\n", runes[index-1]) {
					end = index
					break
				}
			}
		}
		parts = append(parts, segmentPart{text: string(runes[start:end]), start: start, end: end})
		start = end
	}
	return parts
}

type messageContext struct {
	messageID  string
	role       string
	occurredAt string
}

func walkView(value any, path []string, context messageContext, segments *[]Segment) {
	switch typed := value.(type) {
	case string:
		field := strings.Join(path, ".")
		for _, part := range splitSegmentText(typed) {
			*segments = append(*segments, Segment{
				MessageID:  context.messageID,
				Role:       context.role,
				OccurredAt: context.occurredAt,
				Field:      field,
				Start:      part.start,
				End:        part.end,
				Text:       part.text,
			})
		}
	case map[string]any:
		local := context
		if messageObject(typed, path) {
			if value, ok := messageIDValue(typed); ok {
				local.messageID = value
			}
			if role, ok := stringValue(typed["role"]); ok {
				local.role = role
			}
			if occurred, ok := occurredAtValue(typed); ok {
				local.occurredAt = occurred
			}
		}
		for _, key := range orderedKeys(typed, []string{"role", "content", "text", "message", "body"}) {
			if structuralViewKey(key) {
				continue
			}
			walkView(typed[key], appendPath(path, key), local, segments)
		}
	case []any:
		for index, item := range typed {
			walkView(item, appendPath(path, strconv.Itoa(index)), context, segments)
		}
	}
}

func messageObject(value map[string]any, path []string) bool {
	if _, ok := stringValue(value["role"]); ok {
		return true
	}
	if len(path) == 0 {
		_, hasContent := value["content"]
		_, hasText := value["text"]
		_, hasMessage := value["message"]
		_, hasBody := value["body"]
		return hasContent || hasText || hasMessage || hasBody
	}
	// Objects directly inside a messages array (or a root array of messages) are
	// messages. Nested content parts and entity objects inherit the enclosing
	// message context instead of replacing it with their own identifier.
	if len(path) == 1 {
		if _, err := strconv.Atoi(path[0]); err == nil {
			return true
		}
	}
	return len(path) >= 2 && path[len(path)-2] == "messages"
}

func structuralViewKey(key string) bool {
	switch key {
	case "id", "message_id", "messageId", "role", "occurred_at", "occurredAt",
		"timestamp", "created_at", "createdAt", "updated_at", "updatedAt":
		return true
	default:
		return false
	}
}

func messageIDValue(value map[string]any) (string, bool) {
	for _, key := range []string{"id", "message_id", "messageId"} {
		switch typed := value[key].(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return typed, true
			}
		case json.Number:
			return typed.String(), true
		}
	}
	return "", false
}

func stringValue(value any) (string, bool) {
	typed, ok := value.(string)
	if !ok {
		return "", false
	}
	return typed, true
}

func occurredAtValue(value map[string]any) (string, bool) {
	for _, key := range []string{"occurred_at", "occurredAt", "timestamp", "created_at", "createdAt"} {
		if text, ok := stringValue(value[key]); ok && strings.TrimSpace(text) != "" {
			return text, true
		}
	}
	return "", false
}

// Segment returns the visible segment with the exact ID. Unknown IDs are never
// resolved by text similarity.
func (v View) Segment(id string) (Segment, bool) {
	for _, segment := range v.Segments {
		if segment.ID == id {
			return segment, true
		}
	}
	return Segment{}, false
}

// ContainsQuote reports whether the quote occurs verbatim inside one segment.
// It never concatenates unrelated segments, so a match cannot span a message
// or field boundary.
func (v View) ContainsQuote(quote string) bool {
	if strings.TrimSpace(quote) == "" {
		return false
	}
	for _, segment := range v.Segments {
		if strings.Contains(segment.Text, quote) {
			return true
		}
	}
	return false
}

// Resolve maps a model evidence reference to a persisted-able location. A
// missing segment ID is recovered only when the quote has exactly one exact
// occurrence across the whole view. An unknown segment ID is never guessed.
func (v View) Resolve(reference EvidenceReference) (ResolvedEvidence, error) {
	if strings.TrimSpace(reference.Quote) == "" {
		return ResolvedEvidence{}, &EvidenceError{Code: EvidenceEmptyQuote, Segment: reference.SegmentID}
	}
	if reference.SegmentID == "" {
		segment, start, end, total := v.uniqueQuote(reference.Quote)
		if total == 0 {
			return ResolvedEvidence{}, &EvidenceError{Code: EvidenceQuoteNotFound}
		}
		if total > 1 {
			return ResolvedEvidence{}, &EvidenceError{Code: EvidenceQuoteAmbiguous}
		}
		return resolved(segment, start, end, "unique_quote"), nil
	}
	segment, ok := v.Segment(reference.SegmentID)
	if !ok {
		return ResolvedEvidence{}, &EvidenceError{Code: EvidenceSegmentUnknown, Segment: reference.SegmentID}
	}
	start, end, err := locateInSegment(segment, reference)
	if err != nil {
		return ResolvedEvidence{}, err
	}
	return resolved(segment, start, end, ""), nil
}

func (v View) uniqueQuote(quote string) (Segment, int, int, int) {
	var found Segment
	start, end, total := 0, 0, 0
	for _, segment := range v.Segments {
		for _, occurrence := range quoteOccurrences(segment.Text, quote) {
			total++
			if total == 1 {
				found = segment
				start = occurrence
				end = occurrence + utf8.RuneCountInString(quote)
			}
		}
	}
	return found, start, end, total
}

func locateInSegment(segment Segment, reference EvidenceReference) (int, int, error) {
	quoteRunes := []rune(reference.Quote)
	runeText := []rune(segment.Text)
	quoteLength := len(quoteRunes)
	switch {
	case reference.Start != nil && reference.End != nil:
		start, end := *reference.Start, *reference.End
		if start < 0 || end < start || end > len(runeText) || end-start != quoteLength ||
			string(runeText[start:end]) != reference.Quote {
			return 0, 0, &EvidenceError{Code: EvidenceOffsetsInvalid, Segment: segment.ID}
		}
		return start, end, nil
	case reference.Start != nil:
		start := *reference.Start
		end := start + quoteLength
		if start < 0 || end > len(runeText) || string(runeText[start:end]) != reference.Quote {
			return 0, 0, &EvidenceError{Code: EvidenceOffsetsInvalid, Segment: segment.ID}
		}
		return start, end, nil
	case reference.End != nil:
		end := *reference.End
		start := end - quoteLength
		if start < 0 || end > len(runeText) || string(runeText[start:end]) != reference.Quote {
			return 0, 0, &EvidenceError{Code: EvidenceOffsetsInvalid, Segment: segment.ID}
		}
		return start, end, nil
	default:
		occurrences := quoteOccurrences(segment.Text, reference.Quote)
		switch len(occurrences) {
		case 0:
			return 0, 0, &EvidenceError{Code: EvidenceQuoteNotFound, Segment: segment.ID}
		case 1:
			return occurrences[0], occurrences[0] + quoteLength, nil
		default:
			return 0, 0, &EvidenceError{Code: EvidenceQuoteAmbiguous, Segment: segment.ID, Occurrence: len(occurrences)}
		}
	}
}

func quoteOccurrences(text, quote string) []int {
	if quote == "" {
		return nil
	}
	runes := []rune(text)
	needle := []rune(quote)
	if len(needle) == 0 || len(needle) > len(runes) {
		return nil
	}
	result := make([]int, 0, 1)
	for start := 0; start+len(needle) <= len(runes); start++ {
		matched := true
		for offset := range needle {
			if runes[start+offset] != needle[offset] {
				matched = false
				break
			}
		}
		if matched {
			result = append(result, start)
		}
	}
	return result
}

func resolved(segment Segment, start, end int, recovery string) ResolvedEvidence {
	return ResolvedEvidence{
		Segment: segment,
		Location: Location{
			Field: segment.Field,
			Start: segment.Start + start,
			End:   segment.Start + end,
		},
		Recovery: recovery,
	}
}
