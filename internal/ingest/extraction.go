package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/temporal"
)

var (
	negationPattern      = regexp.MustCompile(`(?i)(?:不|没|无|未|别|禁止|\b(?:not|never|no|without|cannot)\b|n't\b)`)
	actionPattern        = regexp.MustCompile(`(?i)(?:执行|运行|安装|配置|检查|创建|删除|更新|重启|验证|备份|恢复|打开|关闭|先|\b(?:run|install|configure|check|create|delete|update|restart|verify|back\s+up|restore|open|close)\b)`)
	parallelActions      = regexp.MustCompile(`(?:并且|同时|另外|此外|以及|；|;|\band\b)`)
	statePattern         = regexp.MustCompile(`(?:计划|安排|完成|取消|改期|推迟|当前进度|完成结果)`)
	clausePattern        = regexp.MustCompile(`[，,。；;！？!?\n]`)
	conditionPattern     = regexp.MustCompile(`(?i)(?:如果|假如|只要|当[^前]|在.+时|条件下|\b(?:if|when|unless|provided)\b)`)
	attributionPattern   = regexp.MustCompile(`(?i)(?:据说|听说|据.+称|认为|觉得|建议|可能|也许|大概|\b(?:reportedly|according\s+to|suggested|believes?|thinks?|might|may|perhaps|probably)\b)`)
	quantityPattern      = regexp.MustCompile(`\d+(?:\.\d+)?\s*(?:%|％|kg|g|mg|ml|l|km|m|cm|mm|小时|分钟|天|周|个月|年|元|块|次|岁|度|℃|°C)?`)
	leadingNumberPattern = regexp.MustCompile(`\d+(?:\.\d+)?`)
	quotedEntityPattern  = regexp.MustCompile(`[「『“"]([^」』”"]{1,40})[」』”"]`)
	preferenceCuePattern = regexp.MustCompile(`(?i)(?:喜欢|不喜欢|偏好|希望|要求|默认|更愿意|宁愿|必须|不要|prefer|like|dislike|want|should|must|default)`)
)

// ValidateExtraction reports fixed, source-free diagnostics. It builds the same
// deterministic source view used by extraction and repair, so evidence is
// checked against the exact segments the model was shown.
func ValidateExtraction(src source.Source, candidates []Candidate) []ExtractionIssue {
	return validateExtraction(source.BuildView(src.Content), src, candidates)
}

func validateExtraction(view source.View, src source.Source, candidates []Candidate) []ExtractionIssue {
	issues := make([]ExtractionIssue, 0)
	duplicates := duplicateCandidateIDs(candidates)
	for index, candidate := range candidates {
		id := candidate.CandidateID
		if id == "" {
			id = fmt.Sprintf("c%d", index+1)
		}
		add := func(field, code, message string) {
			issues = append(issues, ExtractionIssue{
				CandidateID: id, Field: field, Code: code, Class: extractionIssueClass(code), Message: message,
			})
		}
		if duplicates[id] {
			add("candidate_id", "duplicate_candidate_id", "candidate_id must be unique within one extraction result")
		}
		if strings.TrimSpace(candidate.Content) == "" {
			add("content", "empty_content", "candidate content must not be empty")
		}
		if !validKind(candidate.Kind) {
			add("kind", "invalid_enum", "kind must be fact, preference, event or procedure")
		}
		if !validPhase(candidate.Phase) {
			add("phase", "invalid_enum", "phase is not a supported extraction value")
		}
		if !validDurability(candidate.Durability) {
			add("durability", "invalid_enum", "durability is not a supported extraction value")
		}
		if candidate.FactGroup != nil && candidate.FactGroup.Cardinality != "" {
			switch candidate.FactGroup.Cardinality {
			case memory.CardinalitySingle, memory.CardinalityMultiple, memory.CardinalityUnknown:
			default:
				add("fact_group.cardinality", "invalid_enum", "fact_group cardinality must be single, multiple or unknown")
			}
		}
		for _, field := range candidate.TemporalErrors {
			add(field, "invalid_time", "time value must be a supported RFC3339 timestamp or complete calendar date")
		}
		for _, issue := range candidate.ProtocolIssues {
			add(issue.Field, "invalid_field_shape", "field has an invalid extraction protocol shape")
		}
		if candidate.ValidFrom != nil && candidate.ValidTo != nil && !candidate.ValidFrom.Before(*candidate.ValidTo) {
			// Persistence uses half-open intervals, so equality is invalid too.
			add("valid_from", "invalid_interval", "valid_from must be earlier than valid_to for a half-open interval")
		}
		quotes := make([]string, 0, len(candidate.Evidence))
		contextComplete := true
		for evidenceIndex, evidence := range candidate.Evidence {
			field := fmt.Sprintf("evidence[%d]", evidenceIndex)
			resolved, err := view.Resolve(evidence)
			if err != nil {
				code := source.EvidenceQuoteNotFound
				var evidenceErr *source.EvidenceError
				if errors.As(err, &evidenceErr) {
					code = evidenceErr.Code
				}
				add(field, code, "verbatim evidence must resolve to exactly one visible source segment")
				continue
			}
			if resolved.Recovery != "" {
				candidates[index].Normalizations = appendNormalizationOnce(candidates[index].Normalizations,
					FieldNormalization{Field: field + ".segment_id", Code: resolved.Recovery})
			}
			if temporal.HasRelativeTime(evidence.Quote) {
				if _, ok := temporal.ContextTime(resolved.Segment.OccurredAt); !ok {
					contextComplete = false
				}
			}
			quotes = append(quotes, evidence.Quote)
			if !hasExplicitExclusion(evidence.Quote) && negationLost(evidence.Quote, candidate.Content) {
				add("content", "negation_lost", "candidate content must preserve the negated assertion in its evidence")
			}
		}
		quoteText := strings.Join(quotes, "\n")
		validateObservedTime(candidate, quoteText, contextComplete, add)
		validateEventTime(candidate, quoteText, contextComplete, add)
		validateValidityBounds(candidate, quoteText, contextComplete, add)
		validateKindsAndQualifiers(candidate, quoteText, add)
		if candidate.Kind == memory.KindProcedure {
			if !procedureHasAction(candidate) {
				add("procedure", "procedure_without_action", "procedure must contain an action or non-empty step")
			}
		} else if compoundAssertion(candidate.Content) {
			add("content", "atomicity_violation", "split independent actions or lifecycle states into separate candidates")
		}
		if candidateExcluded(view, src, candidate) {
			add("content", "explicit_exclusion", "the source explicitly excludes this information from memory")
		}
	}
	return issues
}

func validateObservedTime(candidate Candidate, quoteText string, contextAvailable bool, add func(field, code, message string)) {
	if !validBasis(candidate.ObservedAtBasis) {
		add("observed_at_basis", "invalid_enum", "time basis must be explicit, inferred or unknown")
	}
	if candidate.ObservedAt == nil {
		if candidate.ObservedAtBasis != "" && candidate.ObservedAtBasis != memory.BasisUnknown {
			add("observed_at", "missing_boundary", "a known basis requires a time value")
		}
		return
	}
	if candidate.ObservedAtBasis == memory.BasisUnknown {
		add("observed_at_basis", "invalid_temporal_basis", "a time value cannot use unknown basis")
	}
	if candidate.ObservedAtBasis != "" && candidate.ObservedAtBasis != memory.BasisUnknown &&
		temporal.HasRelativeTime(quoteText) && !contextAvailable {
		add("observed_at", "relative_time_without_context", "relative time requires a message-local occurrence context")
	}
}

func validateEventTime(candidate Candidate, quoteText string, contextAvailable bool, add func(field, code, message string)) {
	if !validBasis(candidate.EventTimeBasis) {
		add("event_time_basis", "invalid_enum", "time basis must be explicit, inferred or unknown")
	}
	if candidate.EventTime == nil {
		if candidate.EventTimeBasis != "" && candidate.EventTimeBasis != memory.BasisUnknown {
			add("event_time", "missing_boundary", "a known basis requires a time value")
		}
		if candidate.Kind == memory.KindEvent && len(temporal.Mentions(quoteText)) > 0 &&
			candidate.ValidFrom == nil && candidate.ValidTo == nil {
			add("event_time", "event_time_missing", "a dated event should record an occurrence time instead of an unrelated validity boundary")
		}
		return
	}
	if candidate.EventTimeBasis == "" || candidate.EventTimeBasis == memory.BasisUnknown {
		add("event_time_basis", "invalid_temporal_basis", "a time value cannot use unknown basis")
	}
	if candidate.EventTimeBasis == memory.BasisExplicit {
		if _, ok := temporal.FindMention(quoteText, *candidate.EventTime, candidate.EventTimePrecision); !ok &&
			!(temporal.HasRelativeTime(quoteText) && contextAvailable) {
			add("event_time", "missing_date_evidence", "explicit event time must match a date in verified evidence")
		}
	}
	if temporal.HasRelativeTime(quoteText) && !contextAvailable {
		add("event_time", "relative_time_without_context", "relative time requires a message-local occurrence context")
	}
	if candidate.EventTimePrecision != "" && candidate.EventTimePrecision != memory.PrecisionUnknown {
		switch candidate.EventTimePrecision {
		case memory.PrecisionDay, memory.PrecisionHour, memory.PrecisionMinute, memory.PrecisionSecond:
		default:
			add("event_time_precision", "fabricated_precision", "event time must resolve to a day or finer supported precision")
		}
	}
	if temporal.HasMonthOnly(quoteText) && len(temporal.Mentions(quoteText)) == 0 {
		add("event_time", "fabricated_precision", "month-only wording cannot imply a precise day")
	}
}

func validateValidityBounds(candidate Candidate, quoteText string, contextAvailable bool, add func(field, code, message string)) {
	for _, bound := range []struct {
		name      string
		value     *time.Time
		basis     memory.TemporalBasis
		precision memory.TemporalPrecision
	}{
		{"valid_from", candidate.ValidFrom, candidate.ValidFromBasis, candidate.ValidFromPrecision},
		{"valid_to", candidate.ValidTo, candidate.ValidToBasis, candidate.ValidToPrecision},
	} {
		if !validBasis(bound.basis) {
			add(bound.name+"_basis", "invalid_enum", "time basis must be explicit, inferred or unknown")
		}
		if bound.value == nil && bound.basis != "" && bound.basis != memory.BasisUnknown {
			add(bound.name, "missing_boundary", "a known basis requires a time boundary")
		}
		if bound.value != nil && bound.basis == memory.BasisUnknown {
			add(bound.name+"_basis", "invalid_temporal_basis", "a time boundary cannot use unknown basis")
		}
		if bound.value != nil && bound.basis == memory.BasisExplicit {
			if _, ok := temporal.FindMention(quoteText, *bound.value, bound.precision); !ok &&
				!(temporal.HasRelativeTime(quoteText) && contextAvailable) {
				add(bound.name, "missing_date_evidence", "explicit boundary must match a date in verified evidence")
			}
			if temporal.HasMonthOnly(quoteText) && len(temporal.Mentions(quoteText)) == 0 {
				add(bound.name, "fabricated_precision", "month-only wording cannot imply a precise day")
			}
		}
		if bound.value != nil && temporal.HasFuzzyTime(quoteText) && len(temporal.Mentions(quoteText)) == 0 &&
			!(temporal.HasRelativeTime(quoteText) && contextAvailable) {
			add(bound.name, "fabricated_precision", "holidays, seasons and fuzzy dates cannot imply a precise day")
		}
		if bound.value != nil && temporal.HasRelativeTime(quoteText) && !contextAvailable {
			add(bound.name, "relative_time_without_context", "relative time requires a message-local occurrence context")
		}
	}
	if candidate.ValidFrom == nil && candidate.ValidTo == nil && candidate.EventTime == nil &&
		candidate.ObservedAt == nil && len(temporal.Mentions(quoteText)) > 0 {
		// The date may belong to an event, an observation or no stored boundary.
		// This is a review diagnostic, never a requirement to invent validity.
		add("valid_from", "missing_explicit_boundary", "precise date is present; assign it to the correct time role or leave it as text")
	}
}

func validateKindsAndQualifiers(candidate Candidate, quoteText string, add func(field, code, message string)) {
	switch candidate.Kind {
	case memory.KindFact, memory.KindPreference:
		if candidate.Phase != "" && candidate.Phase != PhaseUnknown {
			add("phase", "kind_phase_mismatch", "fact and preference assertions do not have an event lifecycle phase")
		}
	case memory.KindProcedure:
		if candidate.Phase != "" && candidate.Phase != PhaseUnknown {
			add("phase", "kind_phase_mismatch", "a reusable procedure does not have an event lifecycle phase")
		}
		if candidate.Durability == DurabilityEpisodic || candidate.Durability == DurabilityTemporary {
			add("durability", "kind_durability_mismatch", "a reusable procedure cannot be classified as episodic or temporary")
		}
	case memory.KindEvent:
		if candidate.Procedure != nil {
			add("procedure", "kind_procedure_mismatch", "a one-off event is not a reusable procedure")
		}
	}
	if candidate.Kind != memory.KindProcedure && candidate.Procedure != nil {
		add("procedure", "kind_procedure_mismatch", "structured steps require kind=procedure")
	}
	if candidate.Qualifiers != nil {
		switch candidate.Qualifiers.Polarity {
		case "", memory.PolarityUnknown, memory.PolarityPositive, memory.PolarityNegative, memory.PolarityMixed:
		default:
			add("qualifiers.polarity", "invalid_enum", "polarity must be positive, negative, mixed or unknown")
		}
		switch candidate.Qualifiers.Certainty {
		case "", memory.CertaintyUnknown, memory.CertaintyAsserted, memory.CertaintyUncertain,
			memory.CertaintyReported, memory.CertaintyHypothetical:
		default:
			add("qualifiers.certainty", "invalid_enum", "certainty is not a supported value")
		}
	}
	content := candidate.Content
	contentNorm := normalizeOverlapText(content)
	if candidate.Qualifiers != nil && candidate.Qualifiers.Polarity == memory.PolarityNegative &&
		!negationPattern.MatchString(content) {
		add("content", "polarity_lost", "a negative qualifier requires the assertion to preserve negation")
	}
	if conditionPattern.MatchString(quoteText) && !conditionPattern.MatchString(content) &&
		(candidate.Qualifiers == nil || len(candidate.Qualifiers.Conditions) == 0) {
		add("content", "condition_lost", "the assertion must retain its applicable condition")
	}
	for _, match := range quantityPattern.FindAllString(temporal.StripInstants(quoteText), -1) {
		number := strings.TrimSpace(match)
		if number == "" {
			continue
		}
		digits := leadingNumberPattern.FindString(number)
		if digits != "" && !strings.Contains(contentNorm, normalizeOverlapText(digits)) {
			add("content", "quantity_lost", "the assertion must retain the stated quantity and unit")
			break
		}
	}
	if candidate.Qualifiers != nil && candidate.Qualifiers.Quantity != nil {
		value := normalizeOverlapText(candidate.Qualifiers.Quantity.Value)
		if value != "" && !strings.Contains(contentNorm, value) {
			add("content", "quantity_lost", "the structured quantity is missing from the assertion text")
		}
	}
	if attributionPattern.MatchString(quoteText) && !attributionPattern.MatchString(content) &&
		(candidate.Qualifiers == nil ||
			((candidate.Qualifiers.Certainty == "" || candidate.Qualifiers.Certainty == memory.CertaintyUnknown) &&
				candidate.Qualifiers.Attribution == "")) {
		add("content", "attribution_lost", "uncertain or reported wording must remain attached to the assertion")
	}
	for _, match := range quotedEntityPattern.FindAllStringSubmatch(quoteText, -1) {
		entity := strings.TrimSpace(match[1])
		if entity != "" && !strings.Contains(contentNorm, normalizeOverlapText(entity)) {
			add("content", "entity_lost", "the assertion must retain a quoted essential entity")
		}
	}
}

func duplicateCandidateIDs(candidates []Candidate) map[string]bool {
	counts := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		if candidate.CandidateID != "" {
			counts[candidate.CandidateID]++
		}
	}
	result := make(map[string]bool)
	for id, count := range counts {
		if count > 1 {
			result[id] = true
		}
	}
	return result
}

func extractionIssueClass(code string) string {
	switch code {
	case "empty_content", "invalid_enum", "invalid_field_shape", "invalid_time",
		"missing_boundary", "procedure_without_action", "atomicity_violation", "duplicate_candidate_id",
		"kind_phase_mismatch", "kind_durability_mismatch", "kind_procedure_mismatch":
		return IssueRepairable
	case "explicit_exclusion", "invalid_interval", "invalid_temporal_basis",
		"missing_date_evidence", "fabricated_precision",
		"negation_lost", "relative_time_without_context",
		source.EvidenceEmptyQuote, source.EvidenceSegmentUnknown,
		source.EvidenceQuoteNotFound, source.EvidenceQuoteAmbiguous, source.EvidenceOffsetsInvalid:
		return IssueHard
	case "event_time_missing", "kind_missing", "missing_explicit_boundary",
		"polarity_lost", "condition_lost", "quantity_lost", "attribution_lost", "entity_lost":
		return IssueQuality
	default:
		return IssueRepairable
	}
}

func validKind(value memory.Kind) bool {
	switch value {
	case "", memory.KindFact, memory.KindPreference, memory.KindEvent, memory.KindProcedure:
		return true
	default:
		return false
	}
}

func validPhase(value Phase) bool {
	switch value {
	case "", PhaseUnknown, PhaseScheduled, PhaseInProgress, PhaseCompleted, PhaseCancelled, PhasePostponed:
		return true
	default:
		return false
	}
}

func validDurability(value Durability) bool {
	switch value {
	case "", DurabilityUnknown, DurabilityDurable, DurabilityEpisodic, DurabilityTemporary:
		return true
	default:
		return false
	}
}

func validBasis(value memory.TemporalBasis) bool {
	switch value {
	case "", memory.BasisExplicit, memory.BasisInferred, memory.BasisUnknown:
		return true
	default:
		return false
	}
}

func procedureHasAction(candidate Candidate) bool {
	if candidate.Procedure != nil {
		for _, step := range candidate.Procedure.Steps {
			if strings.TrimSpace(step) != "" {
				return true
			}
		}
	}
	return actionPattern.MatchString(candidate.Content)
}

func compoundAssertion(text string) bool {
	stateClauses := 0
	for _, clause := range clausePattern.Split(text, -1) {
		if statePattern.MatchString(clause) {
			stateClauses++
		}
	}
	if stateClauses > 1 {
		return true
	}
	parts := parallelActions.Split(strings.ToLower(text), -1)
	actions := 0
	for _, part := range parts {
		if actionPattern.MatchString(part) {
			actions++
		}
	}
	return actions > 1
}

func hasExplicitExclusion(value string) bool {
	value = strings.ToLower(value)
	for _, marker := range []string{"不要记", "别记", "不需要记忆", "不要保存", "无需保存", "do not remember", "don't remember", "do not store", "don't store", "do not record", "don't record"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// negationLost compares the negated clause with the extracted assertion instead
// of treating any negated character in the whole quote as applying to every
// candidate. A candidate that only expresses an unrelated, non-negated clause
// is not flagged.
func negationLost(quote, content string) bool {
	if negationPattern.MatchString(content) {
		return false
	}
	for _, clause := range clausePattern.Split(quote, -1) {
		if !negationPattern.MatchString(clause) || hasExplicitExclusion(clause) {
			continue
		}
		core := strings.TrimSpace(negationPattern.ReplaceAllString(clause, ""))
		if core == "" {
			continue
		}
		if assertionOverlap(core, content) {
			return true
		}
	}
	return false
}

func assertionOverlap(core, content string) bool {
	coreNorm := normalizeOverlapText(core)
	contentNorm := normalizeOverlapText(content)
	if coreNorm == "" || contentNorm == "" {
		return false
	}
	if strings.Contains(contentNorm, coreNorm) {
		return true
	}
	coreGrams := runeBigrams(coreNorm)
	contentGrams := runeBigrams(contentNorm)
	if len(coreGrams) == 0 {
		return false
	}
	matched := 0
	for gram := range coreGrams {
		if _, ok := contentGrams[gram]; ok {
			matched++
		}
	}
	return float64(matched)/float64(len(coreGrams)) >= 0.6
}

func normalizeOverlapText(value string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			return unicode.ToLower(char)
		}
		return -1
	}, value)
}

func runeBigrams(value string) map[string]struct{} {
	runes := []rune(value)
	if len(runes) < 2 {
		if len(runes) == 1 {
			return map[string]struct{}{string(runes): {}}
		}
		return nil
	}
	result := make(map[string]struct{}, len(runes)-1)
	for index := 0; index+1 < len(runes); index++ {
		result[string(runes[index:index+2])] = struct{}{}
	}
	return result
}

// Exclusions are checked against the containing source sentence, not just the
// quote: a model could otherwise omit "不要记：" from its evidence. We never
// let repair turn an excluded assertion into a positive memory.
func candidateExcluded(view source.View, src source.Source, candidate Candidate) bool {
	if hasExplicitExclusion(candidate.Content) {
		return true
	}
	for _, evidence := range candidate.Evidence {
		if hasExplicitExclusion(evidence.Quote) {
			return true
		}
		resolved, err := view.Resolve(evidence)
		if err == nil && excludedAt(src.Content, &resolved.Location) {
			return true
		}
	}
	if resolved, err := view.Resolve(source.EvidenceReference{Quote: candidate.Content}); err == nil {
		if excludedAt(src.Content, &resolved.Location) {
			return true
		}
	}
	return false
}

func excludedAt(content json.RawMessage, location *source.Location) bool {
	var value any
	if json.Unmarshal(content, &value) != nil {
		return false
	}
	if location.Field != "" {
		for _, part := range strings.Split(location.Field, ".") {
			switch typed := value.(type) {
			case map[string]any:
				value = typed[part]
			case []any:
				index, err := strconv.Atoi(part)
				if err != nil || index < 0 || index >= len(typed) {
					return false
				}
				value = typed[index]
			default:
				return false
			}
		}
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	runes := []rune(text)
	start, end := location.Start, location.End
	if start < 0 || end > len(runes) {
		return false
	}
	for start > 0 && !strings.ContainsRune("。！？!?；;\n", runes[start-1]) {
		start--
	}
	for end < len(runes) && !strings.ContainsRune("。！？!?；;\n", runes[end]) {
		end++
	}
	if hasExplicitExclusion(string(runes[start:end])) {
		return true
	}
	// A trailing "这件事不要记" in the same source message applies to its
	// preceding statement, but not to an unrelated message in the source.
	trailing := strings.TrimSpace(string(runes[end:]))
	return hasExplicitExclusion(trailing) && (strings.Contains(trailing, "这件事") || strings.Contains(trailing, "以上") || strings.Contains(trailing, "这些"))
}

func isolateInvalidCandidates(candidates []Candidate, issues []ExtractionIssue) []Candidate {
	bad := issueIDs(issues)
	result := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, invalid := bad[candidate.CandidateID]; !invalid {
			result = append(result, candidate)
		}
	}
	return result
}

func issueIDs(issues []ExtractionIssue) map[string]struct{} {
	bad := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		bad[issue.CandidateID] = struct{}{}
	}
	return bad
}

func blockingIssueIDs(issues []ExtractionIssue) map[string]struct{} {
	bad := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		if issue.Class == IssueQuality {
			continue
		}
		bad[issue.CandidateID] = struct{}{}
	}
	return bad
}

func appendNormalizationOnce(values []FieldNormalization, value FieldNormalization) []FieldNormalization {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func normalizeExtractionMetadata(candidates []Candidate, reference time.Time) {
	reference = reference.UTC()
	for index := range candidates {
		candidate := &candidates[index]
		candidate.Content = strings.TrimSpace(candidate.Content)
		candidate.Title = strings.TrimSpace(candidate.Title)
		if candidate.Kind == "" {
			candidate.Kind = inferKind(*candidate)
			candidate.Normalizations = append(candidate.Normalizations, FieldNormalization{Field: "kind", Code: "kind_inferred"})
		}
		if candidate.Phase == "" {
			candidate.Phase = PhaseUnknown
		}
		if candidate.Durability == "" {
			candidate.Durability = DurabilityUnknown
		}
		candidate.ValidFrom = memory.UTCPointer(candidate.ValidFrom)
		candidate.ValidTo = memory.UTCPointer(candidate.ValidTo)
		candidate.ObservedAt = memory.UTCPointer(candidate.ObservedAt)
		candidate.EventTime = memory.UTCPointer(candidate.EventTime)
		if candidate.ObservedAt != nil && (candidate.ObservedAtBasis == "" || candidate.ObservedAtBasis == memory.BasisUnknown) {
			candidate.ObservedAtBasis = memory.BasisInferred
		}
		if candidate.EventTime != nil && (candidate.EventTimeBasis == "" || candidate.EventTimeBasis == memory.BasisUnknown) {
			candidate.EventTimeBasis = memory.BasisInferred
		}
		normalizeTemporalRoles(candidate, reference)
	}
}

// inferKind supplies a diagnosed fallback when the model omitted kind. It uses
// structure first and generic assertion cues second, never a report-specific
// keyword list.
func inferKind(candidate Candidate) memory.Kind {
	if candidate.Procedure != nil && (candidate.Procedure.Goal != "" || len(candidate.Procedure.Steps) > 0) {
		return memory.KindProcedure
	}
	switch candidate.Phase {
	case PhaseScheduled, PhaseInProgress, PhaseCompleted, PhaseCancelled, PhasePostponed:
		return memory.KindEvent
	}
	if preferenceCuePattern.MatchString(candidate.Content) {
		return memory.KindPreference
	}
	return memory.KindFact
}

// normalizeTemporalRoles corrects the common, deterministic mis-role where a
// future scheduled event date was stored as fact validity. The date is not
// invented or discarded: it moves to event_time, while validity stays unknown.
func normalizeTemporalRoles(candidate *Candidate, reference time.Time) {
	if candidate.Kind != memory.KindEvent || candidate.ValidFrom == nil {
		return
	}
	if candidate.ObservedAt != nil && candidate.ObservedAtBasis != memory.BasisRecorded {
		reference = candidate.ObservedAt.UTC()
	}
	if candidate.Phase != PhaseScheduled && candidate.Phase != PhasePostponed &&
		candidate.Phase != PhaseInProgress && candidate.Phase != PhaseUnknown {
		return
	}
	if !reference.IsZero() && !candidate.ValidFrom.After(reference) {
		return
	}
	if candidate.EventTime != nil {
		candidate.ValidFrom = nil
		candidate.ValidFromBasis = memory.BasisUnknown
		candidate.Normalizations = appendNormalizationOnce(candidate.Normalizations,
			FieldNormalization{Field: "valid_from", Code: "time_role_corrected"})
		return
	}
	candidate.EventTime = candidate.ValidFrom
	if candidate.EventTimeBasis == "" || candidate.EventTimeBasis == memory.BasisUnknown {
		candidate.EventTimeBasis = candidate.ValidFromBasis
	}
	candidate.EventTimePrecision = inferPrecision(*candidate.EventTime)
	candidate.ValidFrom = nil
	candidate.ValidFromBasis = memory.BasisUnknown
	candidate.Normalizations = appendNormalizationOnce(candidate.Normalizations,
		FieldNormalization{Field: "valid_from", Code: "time_role_corrected"})
}

func inferPrecision(value time.Time) memory.TemporalPrecision {
	value = value.UTC()
	switch {
	case value.Hour() != 0 || value.Minute() != 0 || value.Second() != 0:
		return memory.PrecisionSecond
	default:
		return memory.PrecisionDay
	}
}
