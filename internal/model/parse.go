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
	"github.com/N0rthHXD/dmemd/internal/temporal"
	"github.com/N0rthHXD/dmemd/internal/topic"
)

// ErrUnparseable marks a successful HTTP response whose assistant content is
// not a usable structured object. Worker retries it up to the task budget.
var ErrUnparseable = errors.New("model: response is not a parseable structured object")

// ParseCandidates parses the extraction protocol. It never parses target ids or
// actions: those belong to the decision protocol. RawCandidates keeps the
// pre-normalization count for job quality statistics.
func ParseCandidates(content string) (ingest.ExtractionResult, error) {
	root, err := decodeRoot(content)
	if err != nil {
		return ingest.ExtractionResult{}, err
	}
	rawCandidates, ok := root["candidates"]
	if !ok || isNullRaw(rawCandidates) {
		return ingest.ExtractionResult{}, ErrUnparseable
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawCandidates, &items); err != nil {
		return ingest.ExtractionResult{}, ErrUnparseable
	}
	result := make([]ingest.Candidate, 0, len(items))
	for _, raw := range items {
		candidate, keep := parseCandidate(raw)
		if keep {
			result = append(result, candidate)
		}
	}
	return ingest.ExtractionResult{Candidates: result, RawCandidates: len(items)}, nil
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
	return topic.Raw(topics), nil
}

// ParseAggregate parses the tolerant summary/profile protocol. An empty content
// string is unparseable, while a missing title and malformed auxiliary
// citations are auxiliary defaults that never discard otherwise usable body
// text. Citation validity is enforced by the service against the exact input
// revisions.
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
	result.Citations = citationField(root, "citations")
	result.OmittedInputs = omittedInputField(root, "omitted_inputs")
	return result, nil
}

func citationField(root map[string]json.RawMessage, field string) []artifact.Citation {
	raw, ok := root[field]
	if !ok || isNullRaw(raw) {
		return nil
	}
	var items []struct {
		Section  string `json:"section"`
		Point    string `json:"point"`
		MemoryID string `json:"memory_id"`
		Version  int    `json:"version"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	result := make([]artifact.Citation, 0, len(items))
	for _, item := range items {
		point := strings.TrimSpace(item.Point)
		memoryID := strings.TrimSpace(item.MemoryID)
		if point == "" || memoryID == "" || item.Version <= 0 {
			continue
		}
		result = append(result, artifact.Citation{
			Section: strings.TrimSpace(item.Section), Point: point,
			MemoryID: memoryID, Version: item.Version,
		})
	}
	return result
}

func omittedInputField(root map[string]json.RawMessage, field string) []artifact.OmittedInput {
	raw, ok := root[field]
	if !ok || isNullRaw(raw) {
		return nil
	}
	var items []struct {
		MemoryID string `json:"memory_id"`
		Version  int    `json:"version"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	result := make([]artifact.OmittedInput, 0, len(items))
	for _, item := range items {
		memoryID := strings.TrimSpace(item.MemoryID)
		if memoryID == "" || item.Version <= 0 {
			continue
		}
		result = append(result, artifact.OmittedInput{
			MemoryID: memoryID, Version: item.Version, Reason: strings.TrimSpace(item.Reason),
		})
	}
	return result
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
		return ingest.Candidate{ProtocolIssues: []ingest.FieldIssue{{Field: "candidate", Code: protocolInvalidShape}}}, true
	}
	candidate := ingest.Candidate{}
	content, contentPresent, contentValid := strictString(fields, "content")
	switch {
	case !contentValid:
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "content", Code: protocolInvalidFieldType})
	case !contentPresent:
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "content", Code: protocolInvalidShape})
	default:
		candidate.Content = content
	}
	title, _, titleValid := strictString(fields, "title")
	if !titleValid {
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "title", Code: protocolInvalidFieldType})
	}
	candidate.Title = strings.TrimSpace(title)
	if candidate.Title == "" {
		candidate.Title = firstRunes(content, 40)
	}
	kind, _, kindValid := strictString(fields, "kind")
	if !kindValid {
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "kind", Code: protocolInvalidFieldType})
	}
	phase, _, phaseValid := strictString(fields, "phase")
	if !phaseValid {
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "phase", Code: protocolInvalidFieldType})
	}
	durability, _, durabilityValid := strictString(fields, "durability")
	if !durabilityValid {
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "durability", Code: protocolInvalidFieldType})
	}
	fromBasis, _, fromBasisValid := strictString(fields, "valid_from_basis")
	if !fromBasisValid {
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "valid_from_basis", Code: protocolInvalidFieldType})
	}
	toBasis, _, toBasisValid := strictString(fields, "valid_to_basis")
	if !toBasisValid {
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "valid_to_basis", Code: protocolInvalidFieldType})
	}
	observedBasis, _, observedBasisValid := strictString(fields, "observed_at_basis")
	if !observedBasisValid {
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "observed_at_basis", Code: protocolInvalidFieldType})
	}
	eventBasis, _, eventBasisValid := strictString(fields, "event_time_basis")
	if !eventBasisValid {
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "event_time_basis", Code: protocolInvalidFieldType})
	}
	candidate.Kind = memory.Kind(kind)
	candidate.Phase = ingest.Phase(phase)
	candidate.Durability = ingest.Durability(durability)
	candidate.ValidFromBasis = memory.TemporalBasis(fromBasis)
	candidate.ValidToBasis = memory.TemporalBasis(toBasis)
	candidate.ObservedAtBasis = memory.TemporalBasis(observedBasis)
	candidate.EventTimeBasis = memory.TemporalBasis(eventBasis)
	candidateID, _, candidateIDValid := strictString(fields, "candidate_id")
	if !candidateIDValid {
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, ingest.FieldIssue{Field: "candidate_id", Code: protocolInvalidFieldType})
	}
	candidate.CandidateID = strings.TrimSpace(candidateID)
	observedAt, observedPrecision, observedValid := parseCandidateTime(fields, "observed_at")
	eventTime, eventPrecision, eventValid := parseCandidateTime(fields, "event_time")
	validFrom, fromPrecision, fromValid := parseCandidateTime(fields, "valid_from")
	validTo, toPrecision, toValid := parseCandidateTime(fields, "valid_to")
	candidate.ObservedAt = observedAt
	candidate.ObservedAtPrecision = observedPrecision
	candidate.EventTime = eventTime
	candidate.EventTimePrecision = eventPrecision
	candidate.ValidFrom = validFrom
	candidate.ValidFromPrecision = fromPrecision
	candidate.ValidTo = validTo
	candidate.ValidToPrecision = toPrecision

	topics, topicIssues, topicNormalizations := normalizeStringList(fields, "topics")
	candidate.ProtocolIssues = append(candidate.ProtocolIssues, topicIssues...)
	candidate.Normalizations = append(candidate.Normalizations, topicNormalizations...)
	candidate.Topics = topic.Raw(topics)

	evidence, evidenceIssues, evidenceNormalizations := parseCandidateEvidence(fields["evidence"])
	candidate.ProtocolIssues = append(candidate.ProtocolIssues, evidenceIssues...)
	candidate.Normalizations = append(candidate.Normalizations, evidenceNormalizations...)
	candidate.Evidence = evidence

	factGroupFields, factGroupIssues, factGroupNormalizations := normalizeSingleObject(fields, "fact_group")
	candidate.ProtocolIssues = append(candidate.ProtocolIssues, factGroupIssues...)
	candidate.Normalizations = append(candidate.Normalizations, factGroupNormalizations...)
	if factGroupFields != nil {
		proposal, proposalIssues, proposalNormalizations := parseCandidateFactGroup(factGroupFields)
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, proposalIssues...)
		candidate.Normalizations = append(candidate.Normalizations, proposalNormalizations...)
		candidate.FactGroup = proposal
	}

	procedureFields, procedureIssues, procedureNormalizations := normalizeSingleObject(fields, "procedure")
	candidate.ProtocolIssues = append(candidate.ProtocolIssues, procedureIssues...)
	candidate.Normalizations = append(candidate.Normalizations, procedureNormalizations...)
	if procedureFields != nil {
		proposal, proposalIssues, proposalNormalizations := parseCandidateProcedure(procedureFields)
		candidate.ProtocolIssues = append(candidate.ProtocolIssues, proposalIssues...)
		candidate.Normalizations = append(candidate.Normalizations, proposalNormalizations...)
		candidate.Procedure = proposal
	}

	qualifiers, qualifierIssues, qualifierNormalizations := parseCandidateQualifiers(fields["qualifiers"])
	candidate.ProtocolIssues = append(candidate.ProtocolIssues, qualifierIssues...)
	candidate.Normalizations = append(candidate.Normalizations, qualifierNormalizations...)
	candidate.Qualifiers = qualifiers

	if !observedValid {
		candidate.TemporalErrors = append(candidate.TemporalErrors, "observed_at")
	}
	if !eventValid {
		candidate.TemporalErrors = append(candidate.TemporalErrors, "event_time")
	}
	if !fromValid {
		candidate.TemporalErrors = append(candidate.TemporalErrors, "valid_from")
	}
	if !toValid {
		candidate.TemporalErrors = append(candidate.TemporalErrors, "valid_to")
	}
	return candidate, true
}

func parseCandidateTime(fields map[string]json.RawMessage, name string) (*time.Time, memory.TemporalPrecision, bool) {
	raw, exists := fields[name]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, memory.PrecisionUnknown, true
	}
	value, present := stringField(fields, name)
	if !present {
		return nil, memory.PrecisionUnknown, false
	}
	if strings.TrimSpace(value) == "" {
		return nil, memory.PrecisionUnknown, false
	}
	mention, ok := temporal.ParseValue(value)
	if !ok || mention.Time == nil {
		return nil, memory.PrecisionUnknown, false
	}
	utc := mention.Time.UTC()
	return &utc, mention.Precision, true
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
	conditions := stringSliceField(fields, "conditions")
	if len(conditions) == 0 {
		if single, ok := stringField(fields, "conditions"); ok && single != "" {
			conditions = []string{single}
		}
	}
	proposal := &ingest.FactGroupProposal{
		Subject:     optionalStringValue(fields, "subject"),
		Predicate:   optionalStringValue(fields, "predicate"),
		Scope:       optionalStringValue(fields, "scope"),
		Conditions:  conditions,
		Cardinality: memory.Cardinality(optionalStringValue(fields, "cardinality")),
	}
	if proposal.Subject == "" && proposal.Predicate == "" && proposal.Scope == "" &&
		len(proposal.Conditions) == 0 && proposal.Cardinality == "" {
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

func parseOptionalTime(fields map[string]json.RawMessage, name string) *time.Time {
	value, ok := stringField(fields, name)
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	mention, ok := temporal.ParseValue(value)
	if !ok || mention.Time == nil {
		return nil
	}
	utc := mention.Time.UTC()
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

func firstRunes(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	runes := []rune(trimmed)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes))
}
