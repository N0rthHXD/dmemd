package sqlite

import (
	"context"
	"fmt"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

const evolutionColumns = `id, COALESCE(fact_group_id, ''), from_memory_id, from_version,
	to_memory_id, to_version, action, reason_code, created_at`

const evolutionInsertColumns = `id, fact_group_id, from_memory_id, from_version,
	to_memory_id, to_version, action, reason_code, created_at`

// InsertEvolutionEvent appends one immutable deterministic-update audit row.
func (t *Tx) InsertEvolutionEvent(ctx context.Context, event memory.EvolutionEvent) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_evolution_events(`+evolutionInsertColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, nullableString(event.FactGroupID), event.FromMemoryID, event.FromVersion,
		event.ToMemoryID, event.ToVersion, event.Action, event.ReasonCode,
		formatTime(event.CreatedAt))
	if err != nil {
		return fmt.Errorf("写入演化事件 %s: %w", event.ID, err)
	}
	return nil
}

// ListEvolutionEvents returns the audit chain touching one memory, newest first.
func (s *Store) ListEvolutionEvents(ctx context.Context, memoryID string, limit int) ([]memory.EvolutionEvent, error) {
	return listEvolutionEvents(ctx, s.reader, memoryID, limit)
}

func (t *Tx) ListEvolutionEvents(ctx context.Context, memoryID string, limit int) ([]memory.EvolutionEvent, error) {
	return listEvolutionEvents(ctx, t.tx, memoryID, limit)
}

func listEvolutionEvents(ctx context.Context, q conflictQueryer, memoryID string, limit int) ([]memory.EvolutionEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.QueryContext(ctx,
		`SELECT `+evolutionColumns+` FROM memory_evolution_events
		 WHERE from_memory_id = ? OR to_memory_id = ?
		 ORDER BY created_at DESC, id DESC LIMIT ?`, memoryID, memoryID, limit)
	if err != nil {
		return nil, fmt.Errorf("读取演化事件 %s: %w", memoryID, err)
	}
	defer rows.Close()
	items := []memory.EvolutionEvent{}
	for rows.Next() {
		var event memory.EvolutionEvent
		var createdAt string
		if err := rows.Scan(&event.ID, &event.FactGroupID, &event.FromMemoryID, &event.FromVersion,
			&event.ToMemoryID, &event.ToVersion, &event.Action, &event.ReasonCode, &createdAt); err != nil {
			return nil, fmt.Errorf("读取演化事件 %s: %w", memoryID, err)
		}
		if event.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取演化事件 %s: %w", memoryID, err)
	}
	return items, nil
}
