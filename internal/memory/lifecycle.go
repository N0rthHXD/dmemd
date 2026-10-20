package memory

import (
	"context"
	"time"
)

// UpdateState changes lifecycle state as a new revision. Unknown or unchanged
// states are no-ops, matching the PATCH contract; deletion is also available
// through the dedicated Delete operation.
func (s *Service) UpdateState(ctx context.Context, id string, state State) (Memory, error) {
	var result Memory
	err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		result, err = s.ChangeState(ctx, tx, StateChangeInput{MemoryID: id, State: state})
		return err
	})
	return result, err
}

// ChangeState applies a lifecycle transition in an existing transaction. A
// state-only change keeps the complete current evidence set, because archiving
// or reactivating a memory does not invalidate its supporting sources.
func (s *Service) ChangeState(ctx context.Context, tx Tx, input StateChangeInput) (Memory, error) {
	current, err := tx.GetMemory(ctx, input.MemoryID)
	if err != nil {
		return Memory{}, err
	}
	if input.State == current.State || !knownState(input.State) {
		return current, nil
	}
	now := s.timestamp()
	if input.State == StateDeleted {
		return s.deleteInTx(ctx, tx, current, input.Reason, now)
	}

	evidence, err := tx.ListRevisionEvidence(ctx, current.ID, current.Version)
	if err != nil {
		return Memory{}, err
	}
	candidate := current
	candidate.State = input.State
	candidate.Version = current.Version + 1
	candidate.UpdatedAt = now

	operation := OperationActivate
	if input.State == StateArchived {
		operation = OperationArchive
	}
	if err := tx.UpdateMemory(ctx, candidate); err != nil {
		return Memory{}, err
	}
	if err := s.writeRevision(ctx, tx, candidate, operation, input.Reason, evidence); err != nil {
		return Memory{}, err
	}
	if err := s.syncSearchDocument(ctx, tx, candidate); err != nil {
		return Memory{}, err
	}
	return candidate, nil
}

// Delete creates a deleted tombstone revision and removes the derived search
// document while retaining history and evidence. Repeating a delete is a no-op.
func (s *Service) Delete(ctx context.Context, id string) (Memory, error) {
	var result Memory
	err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		current, err := tx.GetMemory(ctx, id)
		if err != nil {
			return err
		}
		result, err = s.deleteInTx(ctx, tx, current, "", s.timestamp())
		return err
	})
	return result, err
}

func (s *Service) deleteInTx(ctx context.Context, tx Tx, current Memory, reason string, now time.Time) (Memory, error) {
	if current.State == StateDeleted {
		return current, nil
	}
	evidence, err := tx.ListRevisionEvidence(ctx, current.ID, current.Version)
	if err != nil {
		return Memory{}, err
	}
	candidate := current
	candidate.State = StateDeleted
	candidate.Version = current.Version + 1
	candidate.UpdatedAt = now
	if err := tx.UpdateMemory(ctx, candidate); err != nil {
		return Memory{}, err
	}
	if err := s.writeRevision(ctx, tx, candidate, OperationDelete, reason, evidence); err != nil {
		return Memory{}, err
	}
	if err := s.syncSearchDocument(ctx, tx, candidate); err != nil {
		return Memory{}, err
	}
	return candidate, nil
}

// Purge permanently removes the memory and its dedicated data. Shared sources
// and shared evidence remain available to other memories.
func (s *Service) Purge(ctx context.Context, id string) error {
	return s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		return s.PurgeInTx(ctx, tx, id)
	})
}

// PurgeInTx is the transaction-scoped purge used by lifecycle and maintenance
// callers.
func (s *Service) PurgeInTx(ctx context.Context, tx Tx, id string) error {
	return tx.PurgeMemory(ctx, id)
}

// Restore copies a historical revision into a new active current version. The
// selected revision is never modified and redacted evidence stays redacted.
func (s *Service) Restore(ctx context.Context, input RestoreInput) (Memory, error) {
	var result Memory
	err := s.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		result, err = s.RestoreVersion(ctx, tx, input)
		return err
	})
	return result, err
}

// RestoreVersion applies a restoration inside an existing transaction.
func (s *Service) RestoreVersion(ctx context.Context, tx Tx, input RestoreInput) (Memory, error) {
	current, err := tx.GetMemory(ctx, input.MemoryID)
	if err != nil {
		return Memory{}, err
	}
	historical, err := tx.GetRevision(ctx, input.MemoryID, input.Version)
	if err != nil {
		return Memory{}, err
	}
	candidate := historical.Snapshot
	candidate.ID = current.ID
	candidate.Version = current.Version + 1
	candidate.State = StateActive
	candidate.UpdatedAt = s.timestamp()
	if candidate.CreatedAt.IsZero() {
		candidate.CreatedAt = current.CreatedAt
	}
	if err := tx.UpdateMemory(ctx, candidate); err != nil {
		return Memory{}, err
	}
	if err := s.writeRevision(ctx, tx, candidate, OperationRestore, input.Reason, historical.Evidence); err != nil {
		return Memory{}, err
	}
	if err := s.syncSearchDocument(ctx, tx, candidate); err != nil {
		return Memory{}, err
	}
	return candidate, nil
}

func knownState(state State) bool {
	switch state {
	case StateActive, StateArchived, StateDeleted:
		return true
	default:
		return false
	}
}
