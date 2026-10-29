package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// InsertMemoryObservationRefs links observations to one exact canonical
// revision. The caller owns the transaction; repeated links are idempotent.
func (t *Tx) InsertMemoryObservationRefs(ctx context.Context, memoryID string, version int, observationIDs []string) error {
	for _, observationID := range observationIDs {
		if observationID == "" {
			continue
		}
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO memory_observation_refs(
			memory_id, memory_version, observation_id) VALUES (?, ?, ?)
			ON CONFLICT(memory_id, memory_version, observation_id) DO NOTHING`,
			memoryID, version, observationID); err != nil {
			return fmt.Errorf("关联记忆 %s@%d 与观察 %s: %w", memoryID, version, observationID, err)
		}
	}
	return nil
}

// ListMemoryIDsForObservation returns every canonical memory whose history
// references one observation. It intentionally includes previous revisions:
// a deleted memory keeps its tombstone revision but may no longer copy the
// observation ref set, and rule matching still needs to know that the
// observation was already governed.
func (s *Store) ListMemoryIDsForObservation(ctx context.Context, observationID string) ([]string, error) {
	return listMemoryIDsForObservation(ctx, s.reader, observationID)
}

// ListMemoryIDsForObservation is the transaction-scoped variant.
func (t *Tx) ListMemoryIDsForObservation(ctx context.Context, observationID string) ([]string, error) {
	return listMemoryIDsForObservation(ctx, t.tx, observationID)
}

func listMemoryIDsForObservation(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, observationID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.id FROM memories m
		WHERE EXISTS (
			SELECT 1 FROM memory_observation_refs r
			WHERE r.memory_id = m.id AND r.observation_id = ?
		)
		ORDER BY m.updated_at DESC, m.id ASC`, observationID)
	if err != nil {
		return nil, fmt.Errorf("读取观察 %s 关联的记忆: %w", observationID, err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var memoryID string
		if err := rows.Scan(&memoryID); err != nil {
			return nil, fmt.Errorf("读取观察 %s 关联的记忆: %w", observationID, err)
		}
		result = append(result, memoryID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取观察 %s 关联的记忆: %w", observationID, err)
	}
	return result, nil
}

// ListMemoryObservationRefs reads the observations supporting one exact
// revision in stable observation creation order.
func (s *Store) ListMemoryObservationRefs(ctx context.Context, memoryID string, version int) ([]string, error) {
	return listMemoryObservationRefs(ctx, s.reader, memoryID, version)
}

// ListMemoryObservationRefs reads the observations supporting one exact
// revision from a write transaction.
func (t *Tx) ListMemoryObservationRefs(ctx context.Context, memoryID string, version int) ([]string, error) {
	return listMemoryObservationRefs(ctx, t.tx, memoryID, version)
}

func listMemoryObservationRefs(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, memoryID string, version int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.observation_id
		FROM memory_observation_refs r
		LEFT JOIN observations o ON o.id = r.observation_id
		WHERE r.memory_id = ? AND r.memory_version = ?
		ORDER BY COALESCE(o.created_at, ''), r.observation_id ASC`, memoryID, version)
	if err != nil {
		return nil, fmt.Errorf("读取记忆 %s@%d 的观察引用: %w", memoryID, version, err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var observationID string
		if err := rows.Scan(&observationID); err != nil {
			return nil, fmt.Errorf("读取记忆 %s@%d 的观察引用: %w", memoryID, version, err)
		}
		result = append(result, observationID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取记忆 %s@%d 的观察引用: %w", memoryID, version, err)
	}
	return result, nil
}
