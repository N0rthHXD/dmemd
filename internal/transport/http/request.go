package httptransport

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func rawString(object map[string]json.RawMessage, key string) string {
	raw, ok := object[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func rawInt(object map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := object[key]
	if !ok {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, false
	}
	value, err := strconv.Atoi(number.String())
	if err != nil {
		return 0, false
	}
	return value, true
}

func rawBool(object map[string]json.RawMessage, key string) bool {
	raw, ok := object[key]
	if !ok {
		return false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	return value
}

func rawStringSlice(object map[string]json.RawMessage, key string) []string {
	raw, ok := object[key]
	if !ok {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

func rawObject(object map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	raw, ok := object[key]
	if !ok {
		return nil, false
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil || nested == nil {
		return nil, false
	}
	return nested, true
}

func rawTime(object map[string]json.RawMessage, key string) *time.Time {
	value := rawString(object, key)
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

func splitQueryValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				result = append(result, part)
			}
		}
	}
	return result
}

func parseKinds(values []string) ([]memory.Kind, bool) {
	raw := splitQueryValues(values)
	if len(raw) == 0 {
		return nil, false
	}
	result := make([]memory.Kind, 0, len(raw))
	seen := make(map[memory.Kind]struct{}, len(raw))
	for _, value := range raw {
		kind := memory.Kind(value)
		switch kind {
		case memory.KindFact, memory.KindPreference, memory.KindEvent, memory.KindProcedure:
		default:
			continue
		}
		if _, duplicate := seen[kind]; duplicate {
			continue
		}
		seen[kind] = struct{}{}
		result = append(result, kind)
	}
	if len(result) == 0 {
		return nil, true
	}
	return result, false
}

func parseStates(values []string) ([]memory.State, bool) {
	raw := splitQueryValues(values)
	if len(raw) == 0 {
		return nil, false
	}
	result := make([]memory.State, 0, len(raw))
	seen := make(map[memory.State]struct{}, len(raw))
	for _, value := range raw {
		state := memory.State(value)
		switch state {
		case memory.StateActive, memory.StateArchived, memory.StateDeleted:
		default:
			continue
		}
		if _, duplicate := seen[state]; duplicate {
			continue
		}
		seen[state] = struct{}{}
		result = append(result, state)
	}
	if len(result) == 0 {
		return nil, true
	}
	return result, false
}

func parseLimit(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func queryTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}
