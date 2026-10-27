package model

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// Normalization codes are fixed and safe for job diagnostics and repair
// requests. They never contain source text.
const (
	normalizationShapeRecovered = "shape_recovered"
	protocolInvalidShape        = "invalid_shape"
	protocolInvalidItem         = "invalid_item"
	protocolUnexpectedArray     = "unexpected_array"
	protocolInvalidFieldType    = "invalid_field_type"
)

func isNullRaw(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// strictString reports the value, whether the field was present and whether its
// type was valid. A wrong type is never silently treated as a missing field.
func strictString(fields map[string]json.RawMessage, name string) (string, bool, bool) {
	raw, exists := fields[name]
	if !exists || isNullRaw(raw) {
		return "", false, true
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, false
	}
	return value, true, true
}

func normalizeStringList(fields map[string]json.RawMessage, name string) ([]string, []ingest.FieldIssue, []ingest.FieldNormalization) {
	raw, exists := fields[name]
	if !exists || isNullRaw(raw) {
		return nil, nil, nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		if strings.TrimSpace(single) == "" {
			return nil, []ingest.FieldIssue{{Field: name, Code: protocolInvalidShape}}, nil
		}
		return []string{single}, nil, []ingest.FieldNormalization{{Field: name, Code: normalizationShapeRecovered}}
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, []ingest.FieldIssue{{Field: name, Code: protocolInvalidShape}}, nil
	}
	values := make([]string, 0, len(items))
	issues := make([]ingest.FieldIssue, 0)
	for index, item := range items {
		var value string
		if json.Unmarshal(item, &value) != nil {
			issues = append(issues, ingest.FieldIssue{Field: name + "[" + itoa(index) + "]", Code: protocolInvalidItem})
			continue
		}
		values = append(values, value)
	}
	return values, issues, nil
}

func normalizeSingleObject(fields map[string]json.RawMessage, name string) (map[string]json.RawMessage, []ingest.FieldIssue, []ingest.FieldNormalization) {
	raw, exists := fields[name]
	if !exists || isNullRaw(raw) {
		return nil, nil, nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		return object, nil, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, []ingest.FieldIssue{{Field: name, Code: protocolInvalidShape}}, nil
	}
	if len(items) != 1 {
		return nil, []ingest.FieldIssue{{Field: name, Code: protocolUnexpectedArray}}, nil
	}
	if json.Unmarshal(items[0], &object) != nil || object == nil {
		return nil, []ingest.FieldIssue{{Field: name, Code: protocolInvalidShape}}, nil
	}
	return object, nil, []ingest.FieldNormalization{{Field: name, Code: normalizationShapeRecovered}}
}

func parseCandidateEvidence(raw json.RawMessage) ([]source.EvidenceReference, []ingest.FieldIssue, []ingest.FieldNormalization) {
	if isNullRaw(raw) {
		return nil, nil, nil
	}
	items := make([]json.RawMessage, 0, 1)
	normalizations := make([]ingest.FieldNormalization, 0, 1)
	singleObject := false
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		items = append(items, raw)
		singleObject = true
	} else if json.Unmarshal(raw, &items) != nil {
		return nil, []ingest.FieldIssue{{Field: "evidence", Code: protocolInvalidShape}}, nil
	}
	result := make([]source.EvidenceReference, 0, len(items))
	issues := make([]ingest.FieldIssue, 0)
	for index, item := range items {
		field := "evidence[" + itoa(index) + "]"
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil || fields == nil {
			issues = append(issues, ingest.FieldIssue{Field: field, Code: protocolInvalidItem})
			continue
		}
		segmentID, _, segmentValid := strictString(fields, "segment_id")
		quote, _, quoteValid := strictString(fields, "quote")
		if !segmentValid {
			issues = append(issues, ingest.FieldIssue{Field: field + ".segment_id", Code: protocolInvalidFieldType})
			continue
		}
		if !quoteValid || strings.TrimSpace(quote) == "" {
			issues = append(issues, ingest.FieldIssue{Field: field + ".quote", Code: protocolInvalidItem})
			continue
		}
		reference := source.EvidenceReference{SegmentID: strings.TrimSpace(segmentID), Quote: quote}
		start, startPresent, startValid := optionalInt(fields, "start")
		end, endPresent, endValid := optionalInt(fields, "end")
		if !startValid {
			issues = append(issues, ingest.FieldIssue{Field: field + ".start", Code: protocolInvalidFieldType})
			continue
		}
		if !endValid {
			issues = append(issues, ingest.FieldIssue{Field: field + ".end", Code: protocolInvalidFieldType})
			continue
		}
		if startPresent {
			if start < 0 {
				issues = append(issues, ingest.FieldIssue{Field: field + ".start", Code: protocolInvalidItem})
				continue
			}
			reference.Start = &start
		}
		if endPresent {
			if end < 0 {
				issues = append(issues, ingest.FieldIssue{Field: field + ".end", Code: protocolInvalidItem})
				continue
			}
			reference.End = &end
		}
		if reference.Start != nil && reference.End != nil && *reference.End < *reference.Start {
			issues = append(issues, ingest.FieldIssue{Field: field, Code: protocolInvalidItem})
			continue
		}
		result = append(result, reference)
	}
	if singleObject && len(result) > 0 {
		normalizations = append(normalizations, ingest.FieldNormalization{Field: "evidence", Code: normalizationShapeRecovered})
	}
	return result, issues, normalizations
}

func parseCandidateFactGroup(fields map[string]json.RawMessage) (*ingest.FactGroupProposal, []ingest.FieldIssue, []ingest.FieldNormalization) {
	subject, _, subjectValid := strictString(fields, "subject")
	predicate, _, predicateValid := strictString(fields, "predicate")
	scope, _, scopeValid := strictString(fields, "scope")
	cardinality, _, cardinalityValid := strictString(fields, "cardinality")
	conditions, conditionIssues, conditionNormalizations := normalizeStringList(fields, "conditions")
	for index := range conditionIssues {
		conditionIssues[index].Field = "fact_group." + conditionIssues[index].Field
	}
	for index := range conditionNormalizations {
		conditionNormalizations[index].Field = "fact_group." + conditionNormalizations[index].Field
	}
	issues := append([]ingest.FieldIssue(nil), conditionIssues...)
	for _, field := range []struct {
		name  string
		valid bool
	}{
		{"fact_group.subject", subjectValid},
		{"fact_group.predicate", predicateValid},
		{"fact_group.scope", scopeValid},
		{"fact_group.cardinality", cardinalityValid},
	} {
		if !field.valid {
			issues = append(issues, ingest.FieldIssue{Field: field.name, Code: protocolInvalidFieldType})
		}
	}
	proposal := &ingest.FactGroupProposal{
		Subject:     strings.TrimSpace(subject),
		Predicate:   strings.TrimSpace(predicate),
		Scope:       strings.TrimSpace(scope),
		Conditions:  conditions,
		Cardinality: memory.Cardinality(strings.TrimSpace(cardinality)),
	}
	if proposal.Subject == "" && proposal.Predicate == "" && proposal.Scope == "" &&
		len(proposal.Conditions) == 0 && proposal.Cardinality == "" {
		return nil, issues, conditionNormalizations
	}
	return proposal, issues, conditionNormalizations
}

func parseCandidateProcedure(fields map[string]json.RawMessage) (*ingest.ProcedureProposal, []ingest.FieldIssue, []ingest.FieldNormalization) {
	proposal := &ingest.ProcedureProposal{}
	issues := make([]ingest.FieldIssue, 0)
	normalizations := make([]ingest.FieldNormalization, 0)
	goal, _, goalValid := strictString(fields, "goal")
	if !goalValid {
		issues = append(issues, ingest.FieldIssue{Field: "procedure.goal", Code: protocolInvalidFieldType})
	}
	proposal.Goal = strings.TrimSpace(goal)
	for _, field := range []struct {
		name   string
		target *[]string
	}{
		{"steps", &proposal.Steps},
		{"preconditions", &proposal.Preconditions},
		{"completion_conditions", &proposal.CompletionConditions},
		{"failure_branches", &proposal.FailureBranches},
	} {
		values, fieldIssues, fieldNormalizations := normalizeStringList(fields, field.name)
		for _, issue := range fieldIssues {
			issue.Field = "procedure." + issue.Field
			issues = append(issues, issue)
		}
		for _, normalization := range fieldNormalizations {
			normalization.Field = "procedure." + normalization.Field
			normalizations = append(normalizations, normalization)
		}
		*field.target = values
	}
	if proposal.Goal == "" && len(proposal.Steps) == 0 && len(proposal.Preconditions) == 0 &&
		len(proposal.CompletionConditions) == 0 && len(proposal.FailureBranches) == 0 {
		return nil, issues, normalizations
	}
	return proposal, issues, normalizations
}

func parseCandidateQualifiers(raw json.RawMessage) (*ingest.AssertionQualifiers, []ingest.FieldIssue, []ingest.FieldNormalization) {
	if isNullRaw(raw) {
		return nil, nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, []ingest.FieldIssue{{Field: "qualifiers", Code: protocolInvalidShape}}, nil
	}
	issues := make([]ingest.FieldIssue, 0)
	normalizations := make([]ingest.FieldNormalization, 0)
	qualifiers := &ingest.AssertionQualifiers{}
	entities, entityIssues, entityNormalizations := normalizeStringList(fields, "entities")
	for _, issue := range entityIssues {
		issue.Field = "qualifiers." + issue.Field
		issues = append(issues, issue)
	}
	for _, normalization := range entityNormalizations {
		normalization.Field = "qualifiers." + normalization.Field
		normalizations = append(normalizations, normalization)
	}
	conditions, conditionIssues, conditionNormalizations := normalizeStringList(fields, "conditions")
	for _, issue := range conditionIssues {
		issue.Field = "qualifiers." + issue.Field
		issues = append(issues, issue)
	}
	for _, normalization := range conditionNormalizations {
		normalization.Field = "qualifiers." + normalization.Field
		normalizations = append(normalizations, normalization)
	}
	qualifiers.Entities = entities
	qualifiers.Conditions = conditions
	polarity, _, polarityValid := strictString(fields, "polarity")
	if !polarityValid {
		issues = append(issues, ingest.FieldIssue{Field: "qualifiers.polarity", Code: protocolInvalidFieldType})
	}
	certainty, _, certaintyValid := strictString(fields, "certainty")
	if !certaintyValid {
		issues = append(issues, ingest.FieldIssue{Field: "qualifiers.certainty", Code: protocolInvalidFieldType})
	}
	attribution, _, attributionValid := strictString(fields, "attribution")
	if !attributionValid {
		issues = append(issues, ingest.FieldIssue{Field: "qualifiers.attribution", Code: protocolInvalidFieldType})
	}
	qualifiers.Polarity = memory.Polarity(strings.TrimSpace(polarity))
	qualifiers.Certainty = memory.Certainty(strings.TrimSpace(certainty))
	qualifiers.Attribution = strings.TrimSpace(attribution)
	if quantityRaw, exists := fields["quantity"]; exists && !isNullRaw(quantityRaw) {
		var quantityFields map[string]json.RawMessage
		if json.Unmarshal(quantityRaw, &quantityFields) != nil || quantityFields == nil {
			issues = append(issues, ingest.FieldIssue{Field: "qualifiers.quantity", Code: protocolInvalidShape})
		} else {
			value, _, valueValid := strictString(quantityFields, "value")
			unit, _, unitValid := strictString(quantityFields, "unit")
			if !valueValid {
				issues = append(issues, ingest.FieldIssue{Field: "qualifiers.quantity.value", Code: protocolInvalidFieldType})
			}
			if !unitValid {
				issues = append(issues, ingest.FieldIssue{Field: "qualifiers.quantity.unit", Code: protocolInvalidFieldType})
			}
			if strings.TrimSpace(value) != "" {
				qualifiers.Quantity = &memory.Quantity{Value: strings.TrimSpace(value), Unit: strings.TrimSpace(unit)}
			}
		}
	}
	if len(qualifiers.Entities) == 0 && qualifiers.Quantity == nil && len(qualifiers.Conditions) == 0 &&
		qualifiers.Polarity == "" && qualifiers.Certainty == "" && qualifiers.Attribution == "" {
		return nil, issues, normalizations
	}
	return qualifiers, issues, normalizations
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
