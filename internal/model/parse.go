package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/aggregation"
	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/extraction"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/topic"
)

// ErrUnparseable marks a successful HTTP response whose assistant content is
// not a usable structured object. Worker retries it up to the task budget.
var ErrUnparseable = errors.New("model: response is not a parseable structured object")

// ParseExtractionDraft parses the minimal extraction protocol. It produces only
// model-selected span IDs, an optional kind and an optional normalized
// expression. Evidence, identity, titles, time, topics, groups and database
// actions are never parsed here.
func ParseExtractionDraft(content string) (extraction.DraftResult, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return extraction.DraftResult{}, err
	}
	rawItems, ok := root["items"]
	if !ok || isNullRaw(rawItems) {
		return extraction.DraftResult{}, ErrUnparseable
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawItems, &items); err != nil {
		return extraction.DraftResult{}, ErrUnparseable
	}
	drafts := make([]observation.Draft, 0, len(items))
	for _, raw := range items {
		if draft, ok := parseExtractionItem(raw); ok {
			drafts = append(drafts, draft)
		}
	}
	return extraction.DraftResult{Drafts: drafts}, nil
}

func parseExtractionItem(raw json.RawMessage) (observation.Draft, bool) {
	var payload extractionItemPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return observation.Draft{}, false
	}
	spanIDs := make([]string, 0, len(payload.SpanIDs))
	seen := make(map[string]struct{}, len(payload.SpanIDs))
	for _, rawID := range payload.SpanIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		spanIDs = append(spanIDs, id)
	}
	if len(spanIDs) == 0 {
		return observation.Draft{}, false
	}
	return observation.Draft{
		SpanIDs: spanIDs,
		Kind:    observation.Kind(strings.TrimSpace(payload.Kind)),
		Content: strings.TrimSpace(payload.Content),
	}, true
}

// ParseRelations parses the minimal relation protocol. The model output only
// contains request-local references and a fixed relation kind; real IDs,
// versions, evidence and database actions are never accepted.
func ParseRelations(content string) ([]governance.RelationProposal, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return nil, err
	}
	rawRelations, ok := root["relations"]
	if !ok || isNullRaw(rawRelations) {
		return nil, ErrUnparseable
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawRelations, &items); err != nil {
		return nil, ErrUnparseable
	}
	relations := make([]governance.RelationProposal, 0, len(items))
	for _, raw := range items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, ErrUnparseable
		}
		fromRef := strings.TrimSpace(optionalStringValue(fields, "from_ref"))
		toRef := strings.TrimSpace(optionalStringValue(fields, "to_ref"))
		kind := governance.RelationKind(strings.TrimSpace(optionalStringValue(fields, "kind")))
		if fromRef == "" || !kind.Known() {
			return nil, ErrUnparseable
		}
		// duplicate/supports/enriches/supersedes/contradicts describe a
		// relationship to one existing object and must name its temporary
		// reference. separate/independent/unresolved may be targetless.
		if toRef == "" && kind.RequiresTarget() {
			return nil, ErrUnparseable
		}
		relations = append(relations, governance.RelationProposal{FromRef: fromRef, ToRef: toRef, Kind: kind})
	}
	return relations, nil
}

// ParseAggregation parses the minimal aggregation protocol. Only the body is
// accepted; title, dependencies, versions and omission metadata are generated
// by code.
func ParseAggregation(content string) (aggregation.Result, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return aggregation.Result{}, err
	}
	body, ok := stringField(root, "content")
	if !ok || strings.TrimSpace(body) == "" {
		return aggregation.Result{}, ErrUnparseable
	}
	return aggregation.Result{Content: strings.TrimSpace(body)}, nil
}

// ParseTopics parses the classification protocol. A missing topics array is a
// useful empty result; a malformed array is unparseable so the scheduler can
// isolate a repeatedly invalid protocol.
func ParseTopics(content string) ([]string, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return nil, err
	}
	rawTopics, ok := root["topics"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawTopics), []byte("null")) {
		return []string{}, nil
	}
	var topics []string
	if err := json.Unmarshal(rawTopics, &topics); err != nil {
		return nil, ErrUnparseable
	}
	return topic.Raw(topics), nil
}

// ParseExperience parses the minimal experience-case protocol. The model may
// return only the case body; title, outcome, verification, references and
// revision metadata are generated by the service.
func ParseExperience(content string) (artifact.ExperienceResult, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return artifact.ExperienceResult{}, err
	}
	result := artifact.ExperienceResult{
		Content: strings.TrimSpace(optionalStringValue(root, "content")),
	}
	if result.Content == "" {
		return artifact.ExperienceResult{}, ErrUnparseable
	}
	return result, nil
}

// ParseSkill parses the tolerant skill protocol. A readable body or a non-empty
// steps array is required; auxiliary section mistakes never discard usable
// guidance.
func ParseSkill(content string) (skill.Draft, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return skill.Draft{}, err
	}
	draft := skill.Draft{
		Name:             strings.TrimSpace(optionalStringValue(root, "name")),
		Description:      strings.TrimSpace(optionalStringValue(root, "description")),
		Content:          strings.TrimSpace(optionalStringValue(root, "content")),
		Preconditions:    stringSliceField(root, "preconditions"),
		Steps:            stringSliceField(root, "steps"),
		StopConditions:   stringSliceField(root, "stop_conditions"),
		CompletionChecks: stringSliceField(root, "completion_checks"),
		Counterexamples:  stringSliceField(root, "counterexamples"),
	}
	if !draft.Usable() {
		return skill.Draft{}, ErrUnparseable
	}
	return draft, nil
}

func decodeRoot(content string) (map[string]json.RawMessage, error) {
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
	return root, nil
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

func isNullRaw(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func stringField(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok || isNullRaw(raw) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func optionalStringValue(fields map[string]json.RawMessage, name string) string {
	value, _ := stringField(fields, name)
	return value
}

func stringSliceField(fields map[string]json.RawMessage, name string) []string {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}
