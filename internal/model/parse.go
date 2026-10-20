package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrUnparseable marks a successful HTTP response whose assistant content is
// not a usable JSON candidate object. Worker retries it up to the task budget.
var ErrUnparseable = errors.New("model: response is not a parseable candidate object")

// ParseCandidates applies the tolerant parsing rules from the provider
// contract. Invalid individual candidates and evidence entries are skipped;
// only an unusable top-level payload is returned as an error.
func ParseCandidates(content string) ([]ingest.Candidate, error) {
	text, err := unwrapFence(content)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var root map[string]json.RawMessage
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, ErrUnparseable
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrUnparseable
	}

	rawCandidates, ok := root["candidates"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawCandidates), []byte("null")) {
		return []ingest.Candidate{}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawCandidates, &items); err != nil {
		return nil, ErrUnparseable
	}

	result := make([]ingest.Candidate, 0, len(items))
	for _, raw := range items {
		candidate, keep := parseCandidate(raw)
		if keep {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func unwrapFence(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", ErrUnparseable
	}
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed, nil
	}
	rest := trimmed[3:]
	newline := strings.IndexByte(rest, '\n')
	if newline < 0 {
		return "", ErrUnparseable
	}
	language := strings.TrimSpace(rest[:newline])
	if language != "" && !strings.EqualFold(language, "json") {
		return "", ErrUnparseable
	}
	block := rest[newline+1:]
	closing := strings.Index(block, "```")
	if closing < 0 {
		return "", ErrUnparseable
	}
	if strings.TrimSpace(block[closing+3:]) != "" {
		return "", ErrUnparseable
	}
	inner := strings.TrimSpace(block[:closing])
	if inner == "" {
		return "", ErrUnparseable
	}
	return inner, nil
}

func parseCandidate(raw json.RawMessage) (ingest.Candidate, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ingest.Candidate{}, false
	}

	action := ingest.ActionAdd
	if value, ok := stringField(fields, "action"); ok {
		action = normalizeAction(ingest.Action(value))
	}
	if action == ingest.ActionSkip {
		reason, _ := stringField(fields, "reason")
		return ingest.Candidate{Action: ingest.ActionSkip, Reason: reason}, true
	}

	content, ok := stringField(fields, "content")
	if !ok || strings.TrimSpace(content) == "" {
		return ingest.Candidate{}, false
	}
	title, _ := stringField(fields, "title")
	title = strings.TrimSpace(title)
	if title == "" {
		title = firstRunes(content, 40)
	}
	reason, _ := stringField(fields, "reason")
	target, _ := stringField(fields, "target_memory_id")

	kind := memory.KindFact
	if value, ok := stringField(fields, "kind"); ok {
		kind = normalizeKind(memory.Kind(value))
	}

	var observed *time.Time
	if value, ok := stringField(fields, "observed_at"); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			utc := parsed.UTC()
			observed = &utc
		}
	}

	return ingest.Candidate{
		Action:         action,
		TargetMemoryID: target,
		Kind:           kind,
		Title:          title,
		Content:        content,
		ObservedAt:     observed,
		Reason:         reason,
		Evidence:       parseEvidence(fields["evidence"]),
	}, true
}

func parseEvidence(raw json.RawMessage) []source.Evidence {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	result := make([]source.Evidence, 0, len(items))
	for _, item := range items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil || fields == nil {
			continue
		}
		messageID, ok := optionalString(fields, "message_id")
		if !ok {
			continue
		}
		quote, ok := optionalString(fields, "quote")
		if !ok {
			continue
		}
		field, ok := optionalString(fields, "field")
		if !ok {
			continue
		}
		start, hasStart, ok := optionalInt(fields, "start")
		if !ok {
			continue
		}
		end, hasEnd, ok := optionalInt(fields, "end")
		if !ok {
			continue
		}
		evidence := source.Evidence{MessageID: messageID, Quote: quote}
		if field != "" && hasStart && hasEnd && start >= 0 && end >= start {
			evidence.Location = &source.Location{Field: field, Start: start, End: end}
		}
		result = append(result, evidence)
	}
	return result
}

func stringField(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func optionalString(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, exists := fields[name]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func optionalInt(fields map[string]json.RawMessage, name string) (int, bool, bool) {
	raw, exists := fields[name]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false, true
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, false, false
	}
	value, err := strconv.ParseInt(number.String(), 10, 32)
	if err != nil {
		return 0, false, false
	}
	return int(value), true, true
}

func normalizeAction(action ingest.Action) ingest.Action {
	switch action {
	case ingest.ActionAdd, ingest.ActionUpdate, ingest.ActionMergeEvidence, ingest.ActionSkip:
		return action
	default:
		return ingest.ActionAdd
	}
}

func normalizeKind(kind memory.Kind) memory.Kind {
	switch kind {
	case memory.KindFact, memory.KindPreference, memory.KindEvent, memory.KindProcedure:
		return kind
	default:
		return memory.KindFact
	}
}

func firstRunes(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	runes := []rune(trimmed)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes))
}
