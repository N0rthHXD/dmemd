package memory

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

// ConflictReviewInputHash covers the conflict revision plus every member's
// detection and current version. A related atomic change or conflict extension
// therefore advances the review target, while an unchanged conflict does not.
func ConflictReviewInputHash(conflict Conflict) string {
	parts := []string{"conflict-review-v1", conflict.ID, strconv.Itoa(conflict.Revision), string(conflict.ReasonCode)}
	members := append([]ConflictMember(nil), conflict.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].MemoryID < members[j].MemoryID })
	for _, member := range members {
		parts = append(parts, member.MemoryID, strconv.Itoa(member.Version), strconv.Itoa(member.CurrentVersion))
	}
	return task.InputHash(parts...)
}

// QueueConflictReview registers one bounded, debounced review target. The
// review itself applies only deterministic rules; registering it never calls a
// model and does not require user confirmation.
func QueueConflictReview(ctx context.Context, tx Tx, conflict Conflict, debounce time.Duration, now time.Time) error {
	if conflict.Status != ConflictOpen {
		return nil
	}
	now = now.UTC()
	if debounce < 0 {
		debounce = 0
	}
	next := now.Add(debounce)
	if _, _, err := tx.UpsertJob(ctx, task.UpsertInput{
		Kind:          task.KindReviewConflict,
		TargetKey:     task.ConflictKey(conflict.ID),
		InputHash:     ConflictReviewInputHash(conflict),
		NextAttemptAt: &next,
		Now:           now,
	}); err != nil {
		return fmt.Errorf("登记冲突 %s 的复查任务: %w", conflict.ID, err)
	}
	return nil
}

// syncConflictReviews refreshes review targets for every open conflict that
// contains one of the changed memories. New evidence, revisions and lifecycle
// changes use the same path, so a queued review always covers the current
// member set.
func (s *Service) syncConflictReviews(ctx context.Context, tx Tx, memoryIDs []string) error {
	if len(memoryIDs) == 0 {
		return nil
	}
	conflicts, err := tx.ListOpenConflictsForMemories(ctx, memoryIDs)
	if err != nil {
		return err
	}
	now := s.timestamp()
	for _, conflict := range conflicts {
		if err := QueueConflictReview(ctx, tx, conflict, s.governanceDebounce, now); err != nil {
			return err
		}
	}
	return nil
}
