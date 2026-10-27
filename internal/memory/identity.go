package memory

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NormalizeIdentityText produces the scoped-equality form used for subject,
// predicate and scene lookup. It folds compatibility variants, case and
// whitespace, but never performs fuzzy or semantic matching. Two distinct
// people, projects or scenes therefore remain distinct identities.
func NormalizeIdentityText(value string) string {
	value = strings.ToLower(norm.NFKC.String(strings.TrimSpace(value)))
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

// NormalizeConditions returns sorted, de-duplicated condition text. Conditions
// are a necessary part of a fact identity; an empty list is a valid identity.
func NormalizeConditions(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := NormalizeIdentityText(value)
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return NormalizeIdentityText(result[i]) < NormalizeIdentityText(result[j])
	})
	return result
}

// ConditionsKey is the stable identity component for conditions.
func ConditionsKey(values []string) string {
	normalized := NormalizeConditions(values)
	keys := make([]string, 0, len(normalized))
	for _, value := range normalized {
		keys = append(keys, NormalizeIdentityText(value))
	}
	return strings.Join(keys, "\x1f")
}

// FactGroupKey returns the normalized identity tuple for one group. It is used
// by deterministic lookup, never as a display value.
func FactGroupKey(input FactGroupIdentity) [5]string {
	return [5]string{
		NormalizeScopeKey(input.ScopeKey),
		NormalizeIdentityText(input.Subject),
		NormalizeIdentityText(input.Predicate),
		NormalizeIdentityText(input.Scope),
		ConditionsKey(input.Conditions),
	}
}
