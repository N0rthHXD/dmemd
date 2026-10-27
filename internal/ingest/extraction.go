package ingest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

var (
	fullDatePattern     = regexp.MustCompile(`(?:(\d{4})[-/](\d{1,2})[-/](\d{1,2})|(\d{4})年\s*(\d{1,2})月\s*(\d{1,2})[日号]?)`)
	monthDayPattern     = regexp.MustCompile(`(\d{1,2})月\s*(\d{1,2})[日号]`)
	negationPattern     = regexp.MustCompile(`(?i)(?:不|没|无|未|别|禁止|\b(?:not|never|no|without|cannot)\b|n't\b)`)
	actionPattern       = regexp.MustCompile(`(?i)(?:执行|运行|安装|配置|检查|创建|删除|更新|重启|验证|备份|恢复|打开|关闭|先|\b(?:run|install|configure|check|create|delete|update|restart|verify|back\s+up|restore|open|close)\b)`)
	parallelActions     = regexp.MustCompile(`(?:并且|同时|另外|此外|以及|；|;|\band\b)`)
	statePattern        = regexp.MustCompile(`(?:计划|安排|完成|取消|改期|推迟|当前进度|完成结果)`)
	clausePattern       = regexp.MustCompile(`[，,。；;]`)
	fuzzyTimePattern    = regexp.MustCompile(`(?:\d{1,2}月|春节|国庆|中秋|圣诞|年底|年初|近期|下半年|上半年|\b(?:january|february|march|april|may|june|july|august|september|october|november|december|christmas)\b)`)
	relativeTimePattern = regexp.MustCompile(`(?:今天|明天|昨天|后天|前天|\d+天后|\d+天前|\b(?:today|tomorrow|yesterday)\b)`)
)

// ValidateExtraction reports fixed, source-free diagnostics. It must run before
// normalization can hide invalid enum values or manufacture evidence ids.
func ValidateExtraction(src source.Source, candidates []Candidate) []ExtractionIssue {
	issues := make([]ExtractionIssue, 0)
	for index, candidate := range candidates {
		id := candidate.CandidateID
		if id == "" {
			id = fmt.Sprintf("c%d", index+1)
		}
		add := func(field, code, message string) {
			issues = append(issues, ExtractionIssue{CandidateID: id, Field: field, Code: code, Message: message})
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
		for _, field := range candidate.TemporalErrors {
			add(field, "invalid_time", "time value must be an RFC3339 timestamp")
		}
		for _, field := range candidate.ProtocolErrors {
			add(field, "invalid_field_shape", "field has an invalid extraction protocol shape")
		}
		if candidate.ValidFrom != nil && candidate.ValidTo != nil && !candidate.ValidFrom.Before(*candidate.ValidTo) {
			// Persistence uses half-open intervals, so equality is invalid too.
			add("valid_from", "invalid_interval", "valid_from must be earlier than valid_to for a half-open interval")
		}
		quotes := make([]string, 0, len(candidate.Evidence))
		for evidenceIndex, evidence := range candidate.Evidence {
			field := fmt.Sprintf("evidence[%d]", evidenceIndex)
			if strings.TrimSpace(evidence.Quote) == "" {
				add(field, "empty_quote", "evidence quote must not be empty")
				continue
			}
			if !quoteLocated(src, evidence.MessageID, evidence.Quote) {
				add(field, "evidence_not_found", "verbatim evidence must be locatable in the named source message")
				continue
			}
			quotes = append(quotes, evidence.Quote)
			if negationPattern.MatchString(evidence.Quote) && !negationPattern.MatchString(candidate.Content) && !hasExplicitExclusion(evidence.Quote) {
				add("content", "negation_lost", "candidate content must preserve negation in its evidence")
			}
		}
		quoteText := strings.Join(quotes, "\n")
		for _, bound := range []struct {
			name  string
			value *time.Time
			basis memory.TemporalBasis
		}{{"valid_from", candidate.ValidFrom, candidate.ValidFromBasis}, {"valid_to", candidate.ValidTo, candidate.ValidToBasis}} {
			if !validBasis(bound.basis) {
				add(bound.name+"_basis", "invalid_enum", "time basis must be explicit, inferred or unknown")
			}
			if bound.value == nil && bound.basis != "" && bound.basis != memory.BasisUnknown {
				add(bound.name, "missing_boundary", "a known basis requires a time boundary")
			}
			if bound.value != nil && bound.basis == memory.BasisUnknown {
				add(bound.name+"_basis", "invalid_temporal_basis", "a time boundary cannot use unknown basis")
			}
			if bound.value != nil && bound.basis == memory.BasisExplicit && !dateSupported(quoteText, *bound.value) {
				add(bound.name, "missing_date_evidence", "explicit boundary must match a date in verified evidence")
			}
			if bound.value != nil && !fullDatePattern.MatchString(quoteText) && !monthDayPattern.MatchString(quoteText) && fuzzyTimePattern.MatchString(strings.ToLower(quoteText)) && !relativeTimePattern.MatchString(strings.ToLower(quoteText)) {
				add(bound.name, "fabricated_precision", "months, holidays and fuzzy dates cannot imply a precise day")
			}
		}
		if fullDatePattern.MatchString(quoteText) && candidate.ValidFrom == nil && candidate.ValidTo == nil {
			add("valid_from", "missing_explicit_boundary", "a precise date in evidence must populate a validity boundary")
		}
		for _, match := range fullDatePattern.FindAllString(quoteText, -1) {
			if !strings.Contains(candidate.Content, match) {
				add("content", "date_anchor_lost", "preserve the original precise date anchor in content")
				break
			}
		}
		if candidate.Kind == memory.KindProcedure {
			if !procedureHasAction(candidate) {
				add("procedure", "procedure_without_action", "procedure must contain an action or non-empty step")
			}
			if !procedurePreserved(candidate) {
				add("content", "procedure_details_lost", "procedure content must retain ordered steps, preconditions, completion conditions and failure branches")
			}
		} else if compoundAssertion(candidate.Content) {
			add("content", "atomicity_violation", "split independent actions or lifecycle states into separate candidates")
		}
		if candidateExcluded(src, candidate) {
			add("content", "explicit_exclusion", "the source explicitly excludes this information from memory")
		}
	}
	return issues
}

func quoteLocated(src source.Source, messageID, quote string) bool {
	return strings.TrimSpace(quote) != "" && source.LocateQuote(src.Content, messageID, quote) != nil
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

func procedurePreserved(candidate Candidate) bool {
	if candidate.Procedure == nil {
		return true
	}
	position := 0
	for _, step := range candidate.Procedure.Steps {
		if step = strings.TrimSpace(step); step != "" {
			index := strings.Index(candidate.Content[position:], step)
			if index < 0 {
				return false
			}
			position += index + len(step)
		}
	}
	for _, values := range [][]string{candidate.Procedure.Preconditions, candidate.Procedure.CompletionConditions, candidate.Procedure.FailureBranches} {
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" && !strings.Contains(candidate.Content, value) {
				return false
			}
		}
	}
	return true
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

func dateSupported(text string, boundary time.Time) bool {
	for _, match := range fullDatePattern.FindAllStringSubmatch(text, -1) {
		parts := match[1:4]
		if parts[0] == "" {
			parts = match[4:7]
		}
		year, _ := strconv.Atoi(parts[0])
		month, _ := strconv.Atoi(parts[1])
		day, _ := strconv.Atoi(parts[2])
		if boundary.Year() == year && int(boundary.Month()) == month && boundary.Day() == day {
			return true
		}
	}
	if fullDatePattern.MatchString(text) {
		return false
	}
	for _, match := range monthDayPattern.FindAllStringSubmatch(text, -1) {
		month, _ := strconv.Atoi(match[1])
		day, _ := strconv.Atoi(match[2])
		if int(boundary.Month()) == month && boundary.Day() == day {
			return true
		}
	}
	return false
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

// Exclusions are checked against the containing source sentence, not just the
// quote: a model could otherwise omit "不要记：" from its evidence. We never
// let repair turn an excluded assertion into a positive memory.
func candidateExcluded(src source.Source, candidate Candidate) bool {
	if hasExplicitExclusion(candidate.Content) {
		return true
	}
	for _, evidence := range candidate.Evidence {
		if hasExplicitExclusion(evidence.Quote) {
			return true
		}
		location := source.LocateQuote(src.Content, evidence.MessageID, evidence.Quote)
		if location != nil && excludedAt(src.Content, location) {
			return true
		}
	}
	if location := source.LocateQuote(src.Content, "", candidate.Content); location != nil && excludedAt(src.Content, location) {
		return true
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

func normalizeExtractionMetadata(candidates []Candidate) {
	for index := range candidates {
		candidate := &candidates[index]
		candidate.Content = strings.TrimSpace(candidate.Content)
		candidate.Title = strings.TrimSpace(candidate.Title)
		if candidate.Phase == "" {
			candidate.Phase = PhaseUnknown
		}
		if candidate.Durability == "" {
			candidate.Durability = DurabilityUnknown
		}
		candidate.ValidFrom = memory.UTCPointer(candidate.ValidFrom)
		candidate.ValidTo = memory.UTCPointer(candidate.ValidTo)
	}
}
