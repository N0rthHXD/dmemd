package memory

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidAssertion is returned for an unknown assertion status.
	ErrInvalidAssertion = errors.New("memory: invalid assertion status")
	// ErrConflictNotFound is returned when a conflict does not exist.
	ErrConflictNotFound = errors.New("memory conflict not found")
	// ErrInvalidResolution is returned for a malformed adjudication.
	ErrInvalidResolution = errors.New("memory: invalid conflict resolution")
	// ErrConflictStale is returned when the conflict or a member changed after
	// the client read it.
	ErrConflictStale = errors.New("memory conflict changed")
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

// NormalizeScopeKey trims a flat scope label. An empty value means the scope is
// unknown; it is never silently promoted to the user-global scope.
func NormalizeScopeKey(value string) string {
	return strings.TrimSpace(value)
}
