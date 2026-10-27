package memory

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/topic"
)

var (
	// ErrInvalidTemporal is returned for a malformed validity interval.
	ErrInvalidTemporal = errors.New("memory: invalid temporal interval")
	// ErrInvalidAssertion is returned for an unknown assertion status.
	ErrInvalidAssertion = errors.New("memory: invalid assertion status")
	// ErrConflictNotFound is returned when a conflict does not exist.
	ErrConflictNotFound = errors.New("memory conflict not found")
	// ErrInvalidResolution is returned for a malformed adjudication.
	ErrInvalidResolution = errors.New("memory: invalid conflict resolution")
	// ErrConflictStale is returned when the conflict or a member changed after
	// the client read it.
	ErrConflictStale = errors.New("memory conflict changed")
	// ErrRestoreConflict is returned when restoring an old version would create
	// an unmarked contradiction in its current fact group.
	ErrRestoreConflict = errors.New("memory restore would create a conflict")
)

// NormalizeAssertionStatus returns accepted for an empty status.
func NormalizeAssertionStatus(status AssertionStatus) (AssertionStatus, error) {
	switch status {
	case "":
		return AssertionAccepted, nil
	case AssertionAccepted, AssertionPending, AssertionRejected:
		return status, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidAssertion, status)
	}
}

// NormalizeTimeRange validates the half-open interval and its basis pair.
func NormalizeTimeRange(from, to *time.Time, fromBasis, toBasis TemporalBasis) (TimeRange, error) {
	normalized := TimeRange{
		From:      utcPointer(from),
		To:        utcPointer(to),
		FromBasis: fromBasis,
		ToBasis:   toBasis,
	}
	if err := normalizeBound(&normalized.From, &normalized.FromBasis, "valid_from"); err != nil {
		return TimeRange{}, err
	}
	if err := normalizeBound(&normalized.To, &normalized.ToBasis, "valid_to"); err != nil {
		return TimeRange{}, err
	}
	if normalized.From != nil && normalized.To != nil && !normalized.From.Before(*normalized.To) {
		return TimeRange{}, fmt.Errorf("%w: valid_from 必须早于 valid_to", ErrInvalidTemporal)
	}
	return normalized, nil
}

func normalizeBound(value **time.Time, basis *TemporalBasis, name string) error {
	if *value == nil {
		*basis = BasisUnknown
		return nil
	}
	switch *basis {
	case "":
		*basis = BasisInferred
	case BasisExplicit, BasisInferred:
	case BasisUnknown:
		return fmt.Errorf("%w: %s 非空但依据为 unknown", ErrInvalidTemporal, name)
	default:
		return fmt.Errorf("%w: %s 依据 %q 无效", ErrInvalidTemporal, name, *basis)
	}
	return nil
}

// MaxTopics bounds raw labels before canonical parent expansion.
const MaxTopics = 16

func rawTopics(values []string) []string {
	values = topic.Raw(values)
	if len(values) > MaxTopics {
		values = values[:MaxTopics]
	}
	return values
}

func sameRawTopics(left, right []string) bool {
	return slices.Equal(left, right)
}

// CanonicalTopics expands bilingual aliases and all parent topics.
func CanonicalTopics(values []string) []string {
	return topic.Canonical(values)
}

// NormalizeScopeKey trims a flat scope label. An empty value means the scope is
// unknown; it is never silently promoted to the user-global scope.
func NormalizeScopeKey(value string) string {
	return strings.TrimSpace(value)
}

// NormalizeTopics trims, lowercases, de-duplicates and sorts flat topic labels.
// It never interprets topics as a hierarchy.
func NormalizeTopics(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		topic := strings.ToLower(strings.TrimSpace(value))
		if topic == "" {
			continue
		}
		if _, duplicate := seen[topic]; duplicate {
			continue
		}
		seen[topic] = struct{}{}
		result = append(result, topic)
	}
	sort.Strings(result)
	return result
}

// SameTopics reports set equality in a stable, case-normalized form.
func SameTopics(left, right []string) bool {
	normalizedLeft := NormalizeTopics(left)
	normalizedRight := NormalizeTopics(right)
	if len(normalizedLeft) != len(normalizedRight) {
		return false
	}
	index := make(map[string]struct{}, len(normalizedLeft))
	for _, topic := range normalizedLeft {
		index[topic] = struct{}{}
	}
	for _, topic := range normalizedRight {
		if _, ok := index[topic]; !ok {
			return false
		}
	}
	return true
}

func NormalizeCardinality(value Cardinality) (Cardinality, error) {
	switch value {
	case "", CardinalityUnknown:
		return CardinalityUnknown, nil
	case CardinalitySingle, CardinalityMultiple:
		return value, nil
	default:
		return "", fmt.Errorf("memory: invalid cardinality %q", value)
	}
}

// Overlaps reports whether two possibly open intervals may overlap. Unknown
// boundaries are treated as potentially overlapping.
func Overlaps(fromA, toA, fromB, toB *time.Time) bool {
	if toA != nil && fromB != nil && !toA.After(*fromB) {
		return false
	}
	if toB != nil && fromA != nil && !toB.After(*fromA) {
		return false
	}
	return true
}

// Contains reports whether asOf is inside [from, to). Unknown boundaries are
// treated as matching.
func Contains(from, to *time.Time, asOf time.Time) bool {
	if from != nil && from.After(asOf) {
		return false
	}
	if to != nil && !asOf.Before(*to) {
		return false
	}
	return true
}

// UTCPointer returns a UTC copy of a non-nil time pointer.
func UTCPointer(value *time.Time) *time.Time {
	return utcPointer(value)
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func normalizeComparable(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}
