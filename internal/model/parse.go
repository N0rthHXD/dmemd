package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrUnparseable marks a successful HTTP response whose assistant content is
// not a usable structured object. Worker retries it up to the task budget.
var ErrUnparseable = errors.New("model: response is not a parseable structured object")

// ParseCandidates parses the extraction protocol. It never parses target ids or
// actions: those belong to the decision protocol.
func ParseCandidates(content string) ([]ingest.Candidate, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return nil, err
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

// ParseDecisions parses the strict decision protocol. Structural problems are
// unparseable; business validation remains in the ingest service.
func ParseDecisions(content string) ([]ingest.Decision, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return nil, err
	}
	rawDecisions, ok := root["decisions"]
	if !ok {
		return nil, ErrUnparseable
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawDecisions, &items); err != nil {
		return nil, ErrUnparseable
	}
	result := make([]ingest.Decision, 0, len(items))
	for _, raw := range items {
		decision, err := parseDecision(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, decision)
	}
	return result, nil
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
	return memory.NormalizeTopics(topics), nil
}

// ParseAggregate parses the tolerant summary/profile protocol. An empty content
// string is unparseable, while a missing title is a usable auxiliary default.
// Auxiliary shape mistakes never discard otherwise usable body text.
func ParseAggregate(content string) (artifact.AggregateResult, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return artifact.AggregateResult{}, err
	}
	result := artifact.AggregateResult{}
	if title, ok := stringField(root, "title"); ok {
		result.Title = strings.TrimSpace(title)
	}
	if body, ok := stringField(root, "content"); ok {
		result.Content = strings.TrimSpace(body)
	}
	if result.Content == "" {
		return artifact.AggregateResult{}, ErrUnparseable
	}
	return result, nil
}

// ParseExperience parses the tolerant experience-case protocol. A missing
// auxiliary array is empty; an object with no usable problem, actions or body is
// unparseable so the worker can apply its normal protocol-failure isolation.
func ParseExperience(content string) (artifact.ExperienceResult, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return artifact.ExperienceResult{}, err
	}
	result := artifact.ExperienceResult{
		Problem:       strings.TrimSpace(optionalStringValue(root, "problem")),
		Preconditions: stringSliceField(root, "preconditions"),
		Actions:       stringSliceField(root, "actions"),
		Lessons:       stringSliceField(root, "lessons"),
		FailureModes:  stringSliceField(root, "failure_modes"),
		Content:       strings.TrimSpace(optionalStringValue(root, "content")),
	}
	if result.Content == "" && result.Problem == "" && len(result.Actions) == 0 &&
		len(result.Lessons) == 0 && len(result.FailureModes) == 0 {
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

func parseCandidate(raw json.RawMessage) (ingest.Candidate, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ingest.Candidate{}, false
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
	kind := memory.KindFact
	if value, ok := stringField(fields, "kind"); ok {
		kind = normalizeKind(memory.Kind(value))
	}
	candidate := ingest.Candidate{
		Kind:           kind,
		Title:          title,
		Content:        content,
		ObservedAt:     parseOptionalTime(fields, "observed_at"),
		ValidFrom:      parseOptionalTime(fields, "valid_from"),
		ValidTo:        parseOptionalTime(fields, "valid_to"),
		ValidFromBasis: memory.TemporalBasis(optionalStringValue(fields, "valid_from_basis")),
		ValidToBasis:   memory.TemporalBasis(optionalStringValue(fields, "valid_to_basis")),
		Topics:         memory.NormalizeTopics(stringSliceField(fields, "topics")),
		Evidence:       parseEvidence(fields["evidence"]),
	}
	candidate.FactGroup = parseFactGroup(fields["fact_group"])
	return candidate, true
}

func parseDecision(raw json.RawMessage) (ingest.Decision, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ingest.Decision{}, ErrUnparseable
	}
	candidateID, ok := stringField(fields, "candidate_id")
	if !ok || strings.TrimSpace(candidateID) == "" {
		return ingest.Decision{}, ErrUnparseable
	}
	action, ok := stringField(fields, "action")
	if !ok || strings.TrimSpace(action) == "" {
		return ingest.Decision{}, ErrUnparseable
	}
	switch ingest.Action(action) {
	case ingest.ActionAdd, ingest.ActionMergeEvidence, ingest.ActionEnrich, ingest.ActionChange,
		ingest.ActionCorrect, ingest.ActionSeparate, ingest.ActionConflict,
		ingest.ActionUnresolved, ingest.ActionSkip:
	default:
		return ingest.Decision{}, ErrUnparseable
	}
	decision := ingest.Decision{
		CandidateID:     candidateID,
		Action:          ingest.Action(action),
		TargetMemoryIDs: stringSliceField(fields, "target_memory_ids"),
		TargetGroupID:   optionalStringValue(fields, "target_group_id"),
		Content:         optionalStringValue(fields, "content"),
		ValidFrom:       parseOptionalTime(fields, "valid_from"),
		ValidTo:         parseOptionalTime(fields, "valid_to"),
		ValidFromBasis:  memory.TemporalBasis(optionalStringValue(fields, "valid_from_basis")),
		ValidToBasis:    memory.TemporalBasis(optionalStringValue(fields, "valid_to_basis")),
		ReasonCode:      optionalStringValue(fields, "reason_code"),
		EvidenceRefs:    parseEvidenceRefs(fields["evidence_refs"]),
		FactGroup:       parseFactGroup(fields["fact_group"]),
	}
	return decision, nil
}

func parseFactGroup(raw json.RawMessage) *ingest.FactGroupProposal {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil
	}
	proposal := &ingest.FactGroupProposal{
		Subject:     optionalStringValue(fields, "subject"),
		Predicate:   optionalStringValue(fields, "predicate"),
		Scope:       optionalStringValue(fields, "scope"),
		Cardinality: memory.Cardinality(optionalStringValue(fields, "cardinality")),
	}
	if proposal.Subject == "" && proposal.Predicate == "" && proposal.Scope == "" && proposal.Cardinality == "" {
		return nil
	}
	return proposal
}

func parseEvidenceRefs(raw json.RawMessage) []ingest.EvidenceRef {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	result := make([]ingest.EvidenceRef, 0, len(items))
	for _, item := range items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil || fields == nil {
			continue
		}
		candidateID, ok := stringField(fields, "candidate_id")
		if !ok {
			continue
		}
		index, _, ok := optionalInt(fields, "evidence_index")
		if !ok {
			continue
		}
		result = append(result, ingest.EvidenceRef{CandidateID: candidateID, EvidenceIndex: index})
	}
	return result
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

func parseOptionalTime(fields map[string]json.RawMessage, name string) *time.Time {
	value, ok := stringField(fields, name)
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
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
