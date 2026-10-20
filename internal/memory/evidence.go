package memory

import (
	"context"
	"errors"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// prepareEvidence makes an incoming evidence set persistable. Missing IDs and
// timestamps are generated, duplicate IDs collapse to one association, and an
// already stored evidence row is reused unchanged so later revisions cannot
// rewrite evidence history.
func (s *Service) prepareEvidence(ctx context.Context, tx Tx, input []source.Evidence, now time.Time) ([]source.Evidence, error) {
	seen := make(map[string]struct{}, len(input))
	result := make([]source.Evidence, 0, len(input))
	for _, item := range input {
		if item.ID == "" {
			item.ID = idgen.NewUUID()
		}
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}

		existing, err := tx.GetEvidence(ctx, item.ID)
		if err == nil {
			result = append(result, existing)
			continue
		}
		if !errors.Is(err, source.ErrNotFound) {
			return nil, err
		}

		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		} else {
			item.CreatedAt = item.CreatedAt.UTC()
		}
		if item.Redacted {
			item.Quote = ""
			item.Location = nil
			if item.RedactedAt == nil {
				redactedAt := now
				item.RedactedAt = &redactedAt
			} else {
				redactedAt := item.RedactedAt.UTC()
				item.RedactedAt = &redactedAt
			}
		}
		if err := tx.InsertEvidence(ctx, item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func unionEvidence(left, right []source.Evidence) []source.Evidence {
	combined := make([]source.Evidence, 0, len(left)+len(right))
	combined = append(combined, left...)
	combined = append(combined, right...)
	return combined
}

func sameEvidenceSet(left, right []source.Evidence) bool {
	if len(left) != len(right) {
		return false
	}
	ids := make(map[string]struct{}, len(left))
	for _, item := range left {
		ids[item.ID] = struct{}{}
	}
	for _, item := range right {
		if _, ok := ids[item.ID]; !ok {
			return false
		}
	}
	return true
}
