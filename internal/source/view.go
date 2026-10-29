package source

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
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
//
// ContextSegments are read-only fragments supplied from an earlier extraction
// window. They are visible for understanding only and are never valid span
// targets; ResolveSegments rejects them explicitly.
type View struct {
	Segments        []Segment `json:"segments"`
	ContextSegments []Segment `json:"-"`
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

// splitSegmentText turns one source string into stable span candidates.
//
// Ordinary text is split at sentence and clause boundaries so the model can
// select independent statements by ID instead of copying a quote. The boundary
// set follows both Chinese and English punctuation, including the Chinese
// comma, because it separates independently selectable clauses in the source
// language. Long text keeps the bounded-window strategy: a single field must
// never bypass the maximum window size.
func splitSegmentText(text string) []segmentPart {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	if len(runes) > maxSegmentRunes {
		return splitLongSegmentText(runes)
	}
	return splitClauses(runes)
}

// splitClauses splits a bounded string into sentence-level or clause-level
// parts. Start and End are rune offsets in the original field, so a selected
// span maps back to its exact source position.
func splitClauses(runes []rune) []segmentPart {
	parts := make([]segmentPart, 0, 8)
	start := 0
	for index := 0; index < len(runes); {
		if !isClauseBoundary(runes, index) {
			index++
			continue
		}
		end := index
		for end < len(runes) {
			if isClauseBoundary(runes, end) {
				end++
				continue
			}
			// Keep a closing quote or bracket with the clause it closes.
			if isClosingPunctuation(runes[end]) {
				end++
				continue
			}
			if unicode.IsSpace(runes[end]) {
				look := end
				for look < len(runes) && unicode.IsSpace(runes[look]) {
					look++
				}
				if look < len(runes) && isClosingPunctuation(runes[look]) {
					end = look
					continue
				}
			}
			break
		}
		if part, ok := trimSegmentPart(runes, start, end); ok {
			parts = append(parts, part)
		}
		start = end
		index = end
	}
	if part, ok := trimSegmentPart(runes, start, len(runes)); ok {
		parts = append(parts, part)
	}
	return parts
}

// splitLongSegmentText keeps the fixed maximum rune window. A boundary in the
// second half of the window is preferred so a sentence is not split without
// need, but the hard limit always wins.
func splitLongSegmentText(runes []rune) []segmentPart {
	parts := make([]segmentPart, 0, (len(runes)+maxSegmentRunes-1)/maxSegmentRunes)
	for start := 0; start < len(runes); {
		end := start + maxSegmentRunes
		if end > len(runes) {
			end = len(runes)
		} else {
			lower := start + maxSegmentRunes/2
			for index := end; index > lower; index-- {
				if isLongWindowBoundary(runes[index-1]) {
					end = index
					break
				}
			}
		}
		if part, ok := trimSegmentPart(runes, start, end); ok {
			parts = append(parts, part)
		}
		start = end
	}
	return parts
}

// isClauseBoundary reports whether the rune at index ends an independently
// selectable clause. An ASCII period only ends a clause at a real boundary, so
// decimals, versions and file names stay intact.
func isClauseBoundary(runes []rune, index int) bool {
	switch runes[index] {
	case '。', '！', '？', '；', '，', '\n', '\r', '!', '?', ';':
		return true
	case '.':
		if index+1 >= len(runes) {
			return true
		}
		next := runes[index+1]
		return unicode.IsSpace(next) || isClosingPunctuation(next)
	default:
		return false
	}
}

func isLongWindowBoundary(value rune) bool {
	switch value {
	case '。', '！', '？', '；', '，', '\n', '\r', '!', '?', ';':
		return true
	default:
		return false
	}
}

func isClosingPunctuation(value rune) bool {
	switch value {
	case '”', '’', '」', '』', '）', ')', ']', '}', '》', '〉', '】', '〕', '"', '\'':
		return true
	default:
		return false
	}
}

// trimSegmentPart removes surrounding whitespace while keeping rune offsets
// aligned with the original field. Fragments with no letters or digits are not
// useful span targets and are dropped rather than persisted as empty evidence.
func trimSegmentPart(runes []rune, start, end int) (segmentPart, bool) {
	for start < end && unicode.IsSpace(runes[start]) {
		start++
	}
	for end > start && unicode.IsSpace(runes[end-1]) {
		end--
	}
	if start >= end {
		return segmentPart{}, false
	}
	meaningful := false
	for _, value := range runes[start:end] {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			meaningful = true
			break
		}
	}
	if !meaningful {
		return segmentPart{}, false
	}
	return segmentPart{text: string(runes[start:end]), start: start, end: end}, true
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
