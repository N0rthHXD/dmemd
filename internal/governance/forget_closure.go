package governance

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"golang.org/x/text/unicode/norm"
)

// expandForgetDuplicates computes a fixed point before any deletion. Unknown
// group identities never authorize deletion; same-group non-duplicates are
// reported rather than silently treated as copies.
func expandForgetDuplicates(ctx context.Context, tx Tx, ids map[string]struct{}) ([]string, error) {
	queue := sortedSet(ids)
	groups := make(map[string][]memory.Memory)
	identities := make(map[string]memory.FactGroup)
	related := make(map[string]struct{})
	for i := 0; i < len(queue); i++ {
		item, err := tx.GetMemory(ctx, queue[i])
		if err != nil {
			return nil, err
		}
		if item.FactGroupID == "" {
			continue
		}
		members, loaded := groups[item.FactGroupID]
		if !loaded {
			members, err = tx.ListForgetGroupMemories(ctx, item.FactGroupID)
			if err != nil {
				return nil, err
			}
			group, err := tx.GetFactGroup(ctx, item.FactGroupID)
			if err != nil {
				return nil, err
			}
			groups[item.FactGroupID], identities[item.FactGroupID] = members, group
		}
		group := identities[item.FactGroupID]
		for _, candidate := range members {
			if _, removed := ids[candidate.ID]; removed {
				continue
			}
			related[candidate.ID] = struct{}{}
			if strings.TrimSpace(group.Subject) == "" || strings.TrimSpace(group.Predicate) == "" ||
				!forgetDuplicate(item, candidate) {
				continue
			}
			ids[candidate.ID] = struct{}{}
			queue = append(queue, candidate.ID)
		}
	}
	for id := range ids {
		delete(related, id)
	}
	return sortedSet(related), nil
}

func forgetDuplicate(a, b memory.Memory) bool {
	if a.FactGroupID == "" || a.FactGroupID != b.FactGroupID ||
		memory.NormalizeScopeKey(a.ScopeKey) != memory.NormalizeScopeKey(b.ScopeKey) ||
		a.Kind != b.Kind || a.Phase != b.Phase {
		return false
	}
	// Both unknown intervals are equal; one unknown interval is not proof of
	// overlap. Otherwise use the service's half-open valid-time convention.
	unknownA := a.ValidFrom == nil && a.ValidTo == nil
	unknownB := b.ValidFrom == nil && b.ValidTo == nil
	equalTime := sameForgetBoundary(a.ValidFrom, b.ValidFrom) && sameForgetBoundary(a.ValidTo, b.ValidTo)
	if !equalTime && (unknownA != unknownB || !memory.Overlaps(a.ValidFrom, a.ValidTo, b.ValidFrom, b.ValidTo)) {
		return false
	}
	content := forgetComparable(a.Content)
	return content != "" && content == forgetComparable(b.Content)
}

func sameForgetBoundary(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// Treat formatting-only near duplicates as copies, not arbitrary semantic
// neighbours. Never use edit-distance thresholds: a one-character change can
// reverse a negation, subject, date or quantity. Preserve internal punctuation
// (notably decimal points and signs); normalize case, NFC and spacing only.
func forgetComparable(value string) string {
	value = strings.TrimSpace(strings.ToLower(norm.NFC.String(value)))
	value = strings.TrimRightFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("。.!！?？;；", r)
	})
	return strings.Join(strings.Fields(value), " ")
}
