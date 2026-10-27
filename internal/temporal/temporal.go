// Package temporal owns bounded, deterministic parsing and matching of source
// time wording. It never guesses a timezone, a month day or a relative date
// without a message-local reference.
package temporal

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

// Mention is one date or time expression found in source text.
type Mention struct {
	Text          string
	Time          *time.Time
	Precision     memory.TemporalPrecision
	TimezoneKnown bool
}

var (
	rfc3339Pattern  = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?(Z|[+-]\d{2}:\d{2})\b`)
	fullDatePattern = regexp.MustCompile(`(?:(\d{4})[-/](\d{1,2})[-/](\d{1,2})|(\d{4})年\s*(\d{1,2})月\s*(\d{1,2})[日号]?)`)
	monthDayPattern = regexp.MustCompile(`(\d{1,2})月\s*(\d{1,2})[日号]`)
	monthPattern    = regexp.MustCompile(`(?:(\d{4})[-/年]\s*)?(\d{1,2})月?`)
	relativePattern = regexp.MustCompile(`(?i)(?:今天|明天|后天|昨天|前天|本周|上周|下周|这个月|下个月|上个月|\d+\s*(?:天|周|个月|年)\s*(?:后|前|以后|之前)|\b(?:today|tomorrow|yesterday|next\s+week|last\s+week|next\s+month|last\s+month)\b)`)
	fuzzyPattern    = regexp.MustCompile(`(?i)(?:春节|除夕|国庆|中秋|端午|清明|元旦|圣诞|年底|年初|上半年|下半年|近期|最近|将来|以后|\b(?:christmas|new\s+year|holiday|soon|recently|end\s+of\s+the\s+year)\b)`)
)

// ParseValue parses one model-supplied time value. RFC 3339 values keep their
// timezone and sub-day precision. Date-only values are normalized to UTC
// midnight but explicitly marked as timezone-unknown and day-precision. Month
// and year-only values are rejected: callers must leave the boundary unknown
// rather than invent a day.
func ParseValue(raw string) (Mention, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Mention{}, false
	}
	if match := rfc3339Pattern.FindStringSubmatch(raw); match != nil && match[0] == raw {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Mention{}, false
		}
		precision := memory.PrecisionMinute
		if match[6] != "" {
			precision = memory.PrecisionSecond
		}
		utc := parsed.UTC()
		return Mention{Text: raw, Time: &utc, Precision: precision, TimezoneKnown: true}, true
	}
	if match := fullDatePattern.FindStringSubmatch(raw); match != nil && match[0] == raw {
		year, month, day, ok := dateParts(match)
		if !ok {
			return Mention{}, false
		}
		value := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		return Mention{Text: raw, Time: &value, Precision: memory.PrecisionDay}, true
	}
	return Mention{}, false
}

// Mentions returns every complete date or timestamp expression in text. It does
// not return month-only or year-only fragments as instant-bearing mentions.
func Mentions(text string) []Mention {
	result := make([]Mention, 0, 2)
	consumed := make([][2]int, 0, 2)
	for _, location := range rfc3339Pattern.FindAllStringIndex(text, -1) {
		span := [2]int{location[0], location[1]}
		if overlaps(span, consumed) {
			continue
		}
		value := strings.TrimSpace(text[location[0]:location[1]])
		if mention, ok := ParseValue(value); ok {
			result = append(result, mention)
			consumed = append(consumed, span)
		}
	}
	for _, location := range fullDatePattern.FindAllStringIndex(text, -1) {
		span := [2]int{location[0], location[1]}
		if overlaps(span, consumed) {
			continue
		}
		value := strings.TrimSpace(text[location[0]:location[1]])
		if mention, ok := ParseValue(value); ok {
			result = append(result, mention)
			consumed = append(consumed, span)
		}
	}
	for _, location := range monthDayPattern.FindAllStringIndex(text, -1) {
		span := [2]int{location[0], location[1]}
		if overlaps(span, consumed) {
			continue
		}
		value := strings.TrimSpace(text[location[0]:location[1]])
		match := monthDayPattern.FindStringSubmatch(value)
		month, _ := strconv.Atoi(match[1])
		day, _ := strconv.Atoi(match[2])
		if month < 1 || month > 12 || day < 1 || day > 31 {
			continue
		}
		result = append(result, Mention{Text: value, Precision: memory.PrecisionDay, TimezoneKnown: false})
		consumed = append(consumed, span)
	}
	return result
}

// FindMention matches one normalized value against the complete date wording in
// text. A date-only mention wins over an exact timestamp when the source did not
// state a time. Month-only wording never matches a boundary.
func FindMention(text string, value time.Time, precision memory.TemporalPrecision) (Mention, bool) {
	value = value.UTC()
	if precision == "" || precision == memory.PrecisionUnknown {
		precision = memory.PrecisionSecond
	}
	var best Mention
	found := false
	for _, mention := range Mentions(text) {
		if mention.Time == nil || mention.Precision == memory.PrecisionMonth {
			continue
		}
		if !sameDate(*mention.Time, value) {
			continue
		}
		if mention.Precision == memory.PrecisionDay {
			// A date-only source cannot support a time-of-day claim.
			return mention, true
		}
		if precision == memory.PrecisionDay {
			continue
		}
		if sameClock(*mention.Time, value) {
			return mention, true
		}
		if !found {
			best = mention
			found = true
		}
	}
	if found {
		return best, true
	}
	if fullDatePattern.MatchString(text) {
		// A complete date was present but none matched the claimed value. Do not
		// fall back to year-less month/day wording in that case.
		return Mention{}, false
	}
	// Month-day wording without a year is still a precise day within the
	// referenced year, but carries no timezone.
	for _, match := range monthDayPattern.FindAllStringSubmatch(text, -1) {
		month, _ := strconv.Atoi(match[1])
		day, _ := strconv.Atoi(match[2])
		if int(value.Month()) == month && value.Day() == day {
			return Mention{Text: match[0], Precision: memory.PrecisionDay}, true
		}
	}
	return Mention{}, false
}

// HasRelativeTime reports whether text contains a relative time expression.
func HasRelativeTime(text string) bool {
	return relativePattern.MatchString(text)
}

// RelativeText returns the first relative time expression in text.
func RelativeText(text string) string {
	return strings.TrimSpace(relativePattern.FindString(text))
}

// HasFuzzyTime reports whether text contains a season, holiday or relative
// expression that does not by itself identify a precise day.
func HasFuzzyTime(text string) bool {
	return HasMonthOnly(text) || fuzzyPattern.MatchString(text) || HasRelativeTime(text)
}

// StripInstants removes complete date and time expressions from text so a
// quantity check cannot mistake a date fragment for a measurement value.
func StripInstants(text string) string {
	text = rfc3339Pattern.ReplaceAllString(text, " ")
	text = fullDatePattern.ReplaceAllString(text, " ")
	text = monthDayPattern.ReplaceAllString(text, " ")
	return text
}

// HasMonthOnly reports whether text contains a month or year expression that is
// not backed by a complete day. It is used to reject invented day precision.
func HasMonthOnly(text string) bool {
	complete := make([][2]int, 0, 4)
	for _, location := range fullDatePattern.FindAllStringIndex(text, -1) {
		complete = append(complete, [2]int{location[0], location[1]})
	}
	for _, location := range monthDayPattern.FindAllStringIndex(text, -1) {
		complete = append(complete, [2]int{location[0], location[1]})
	}
	for _, location := range monthPattern.FindAllStringIndex(text, -1) {
		span := [2]int{location[0], location[1]}
		if overlaps(span, complete) {
			continue
		}
		if strings.Contains(text[location[0]:location[1]], "月") {
			return true
		}
	}
	return false
}

// ContextTime parses a message-local occurrence time. It only accepts absolute
// timestamps; a missing or malformed context remains unknown.
func ContextTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		date, dateErr := time.Parse("2006-01-02", raw)
		if dateErr != nil {
			return time.Time{}, false
		}
		return date.UTC(), true
	}
	return parsed.UTC(), true
}

func dateParts(match []string) (year, month, day int, ok bool) {
	parts := match[1:4]
	if parts[0] == "" {
		parts = match[4:7]
	}
	year, _ = strconv.Atoi(parts[0])
	month, _ = strconv.Atoi(parts[1])
	day, _ = strconv.Atoi(parts[2])
	if year < 1 || month < 1 || month > 12 || day < 1 || day > 31 {
		return 0, 0, 0, false
	}
	if time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).Day() != day {
		return 0, 0, 0, false
	}
	return year, month, day, true
}

func sameDate(left, right time.Time) bool {
	return left.Year() == right.Year() && left.Month() == right.Month() && left.Day() == right.Day()
}

func sameClock(left, right time.Time) bool {
	return left.Hour() == right.Hour() && left.Minute() == right.Minute() && left.Second() == right.Second()
}

func overlaps(candidate [2]int, consumed [][2]int) bool {
	for _, existing := range consumed {
		if candidate[0] < existing[1] && existing[0] < candidate[1] {
			return true
		}
	}
	return false
}
