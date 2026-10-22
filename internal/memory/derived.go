package memory

import (
	"context"

	"github.com/N0rthHXD/dmemd/internal/artifact"
)

// syncMemoryChanged invalidates direct derived dependencies and registers the
// affected summary/profile targets in the caller's transaction. Evidence-only
// revisions pass semantic=false and leave usable derived text untouched.
func (s *Service) syncMemoryChanged(ctx context.Context, tx Tx, old *Memory, next *Memory, semantic bool) error {
	if s.artifacts == nil || !semantic {
		return nil
	}
	change := artifact.MemoryChange{Semantic: semantic}
	if old != nil {
		change.MemoryID = old.ID
		snapshot := memorySnapshot(*old)
		change.Old = &snapshot
	}
	if next != nil {
		change.MemoryID = next.ID
		snapshot := memorySnapshot(*next)
		change.New = &snapshot
	}
	if change.MemoryID == "" {
		return nil
	}
	return s.artifacts.MemoryChanged(ctx, tx, change)
}

// syncMemoryPurged follows the purge sequence: invalidate and register affected
// targets first, then remove every directly dependent artifact. The atomic
// memories are deleted only after this helper returns.
func (s *Service) syncMemoryPurged(ctx context.Context, tx Tx, current Memory) error {
	if s.artifacts == nil {
		return nil
	}
	oldSnapshot := memorySnapshot(current)
	deleted := current
	deleted.State = StateDeleted
	newSnapshot := memorySnapshot(deleted)
	return s.artifacts.MemoryPurged(ctx, tx, artifact.MemoryChange{
		MemoryID: current.ID,
		Old:      &oldSnapshot,
		New:      &newSnapshot,
		Semantic: true,
	})
}

// syncConflictChanged re-evaluates summaries whose input eligibility changed
// because an open conflict was resolved. The memory versions do not move, so a
// fixed reason makes the target revision advance exactly once per resolution.
func (s *Service) syncConflictChanged(ctx context.Context, tx Tx, memoryIDs []string, reason string) error {
	if s.artifacts == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(memoryIDs))
	for _, id := range memoryIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		current, err := tx.GetMemory(ctx, id)
		if err != nil {
			continue
		}
		snapshot := memorySnapshot(current)
		if err := s.artifacts.MemoryChanged(ctx, tx, artifact.MemoryChange{
			MemoryID: id,
			Old:      &snapshot,
			New:      &snapshot,
			Semantic: true,
			Reason:   reason,
		}); err != nil {
			return err
		}
	}
	return nil
}

func memorySnapshot(item Memory) artifact.MemorySnapshot {
	return artifact.MemorySnapshot{
		ID:       item.ID,
		Version:  item.Version,
		Kind:     string(item.Kind),
		ScopeKey: item.ScopeKey,
		Topics:   NormalizeTopics(item.Topics),
		State:    string(item.State),
	}
}
