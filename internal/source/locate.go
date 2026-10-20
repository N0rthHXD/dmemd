package source

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/N0rthHXD/dmemd/internal/idgen"
)

// NewEvidence builds one evidence entry for a source. It generates the evidence
// ID, locates the quote when possible and keeps the source association even
// when the quote cannot be located. A redacted source never yields a new quote
// or location copy.
func NewEvidence(src Source, messageID, quote, modelName string, at time.Time) Evidence {
	at = at.UTC()
	item := Evidence{
		ID:        idgen.NewUUID(),
		SourceID:  src.ID,
		MessageID: messageID,
		Quote:     quote,
		ModelName: modelName,
		CreatedAt: at,
	}
	if src.Redacted {
		item.Quote = ""
		item.Redacted = true
		redactedAt := at
		if src.RedactedAt != nil {
			redactedAt = src.RedactedAt.UTC()
		}
		item.RedactedAt = &redactedAt
		return item
	}
	item.Location = LocateQuote(src.Content, messageID, quote)
	return item
}

// LocateQuote finds the first occurrence of quote inside the decoded source
// content and returns rune offsets into the containing string. When messageID
// is provided only that message object is searched; an unknown message or a
// quote that cannot be found simply returns nil, leaving the source association
// intact. JSON that cannot be decoded also returns nil.
func LocateQuote(content json.RawMessage, messageID, quote string) *Location {
	if len(bytes.TrimSpace(content)) == 0 || quote == "" {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil
	}
	if messageID == "" {
		return findQuote(root, nil, quote)
	}
	location, matched := findMessage(root, nil, messageID, quote)
	if !matched {
		return nil
	}
	return location
}

func findMessage(value any, path []string, messageID, quote string) (*Location, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if messageHasID(typed, messageID) {
			return findQuote(typed, path, quote), true
		}
		for _, key := range orderedKeys(typed, nil) {
			location, matched := findMessage(typed[key], appendPath(path, key), messageID, quote)
			if matched {
				return location, true
			}
		}
	case []any:
		for index, item := range typed {
			location, matched := findMessage(item, appendPath(path, strconv.Itoa(index)), messageID, quote)
			if matched {
				return location, true
			}
		}
	}
	return nil, false
}

func findQuote(value any, path []string, quote string) *Location {
	switch typed := value.(type) {
	case string:
		byteIndex := strings.Index(typed, quote)
		if byteIndex < 0 {
			return nil
		}
		start := utf8.RuneCountInString(typed[:byteIndex])
		return &Location{
			Field: strings.Join(path, "."),
			Start: start,
			End:   start + utf8.RuneCountInString(quote),
		}
	case map[string]any:
		for _, key := range orderedKeys(typed, []string{"content", "text", "message", "body"}) {
			if location := findQuote(typed[key], appendPath(path, key), quote); location != nil {
				return location
			}
		}
	case []any:
		for index, item := range typed {
			if location := findQuote(item, appendPath(path, strconv.Itoa(index)), quote); location != nil {
				return location
			}
		}
	}
	return nil
}

func messageHasID(value map[string]any, messageID string) bool {
	for _, key := range []string{"id", "message_id", "messageId"} {
		candidate, ok := value[key]
		if !ok {
			continue
		}
		switch typed := candidate.(type) {
		case string:
			if typed == messageID {
				return true
			}
		case json.Number:
			if typed.String() == messageID {
				return true
			}
		}
	}
	return false
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
