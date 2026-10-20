package idgen

import (
	"regexp"
	"testing"
)

func TestNewUUIDFormatAndVariation(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := make(map[string]struct{}, 128)
	for i := 0; i < 128; i++ {
		id := NewUUID()
		if !pattern.MatchString(id) {
			t.Fatalf("NewUUID() = %q, want RFC 4122 version 4 format", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("NewUUID() returned duplicate %q", id)
		}
		seen[id] = struct{}{}
	}
}
