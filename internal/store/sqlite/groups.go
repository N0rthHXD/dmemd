package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

const factGroupColumns = `id, subject, predicate, scope, cardinality, generation, created_at, updated_at`

// GetFactGroup reads one group from the read pool.
func (s *Store) GetFactGroup(ctx context.Context, id string) (memory.FactGroup, error) {
	return getFactGroup(ctx, s.reader, id)
}

func (t *Tx) GetFactGroup(ctx context.Context, id string) (memory.FactGroup, error) {
	return getFactGroup(ctx, t.tx, id)
}

func getFactGroup(ctx context.Context, q queryRowScanner, id string) (memory.FactGroup, error) {
	group, err := scanFactGroup(q.QueryRowContext(ctx,
		`SELECT `+factGroupColumns+` FROM memory_fact_groups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.FactGroup{}, fmt.Errorf("%w: fact group %s", memory.ErrNotFound, id)
	}
	if err != nil {
		return memory.FactGroup{}, fmt.Errorf("读取事实组 %s: %w", id, err)
	}
	return group, nil
}

// FindFactGroup reuses an exact service-visible identity when one exists. Model
// strings are never a global uniqueness constraint; this lookup only helps the
// service avoid splitting identical metadata.
func (t *Tx) FindFactGroup(ctx context.Context, subject, predicate, scope string) (memory.FactGroup, error) {
	group, err := scanFactGroup(t.tx.QueryRowContext(ctx,
		`SELECT `+factGroupColumns+` FROM memory_fact_groups
		 WHERE subject = ? AND predicate = ? AND scope = ?
		 ORDER BY created_at ASC, id ASC LIMIT 1`, subject, predicate, scope))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.FactGroup{}, fmt.Errorf("%w: fact group not found", memory.ErrNotFound)
	}
	if err != nil {
		return memory.FactGroup{}, fmt.Errorf("查找事实组: %w", err)
	}
	return group, nil
}

func scanFactGroup(row rowScanner) (memory.FactGroup, error) {
	var group memory.FactGroup
	var cardinality, createdAt, updatedAt string
	if err := row.Scan(&group.ID, &group.Subject, &group.Predicate, &group.Scope,
		&cardinality, &group.Generation, &createdAt, &updatedAt); err != nil {
		return memory.FactGroup{}, err
	}
	group.Cardinality = memory.Cardinality(cardinality)
	var err error
	if group.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.FactGroup{}, err
	}
	if group.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return memory.FactGroup{}, err
	}
	return group, nil
}

// InsertFactGroup writes service-owned group metadata.
func (t *Tx) InsertFactGroup(ctx context.Context, group memory.FactGroup) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_fact_groups(`+factGroupColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		group.ID, group.Subject, group.Predicate, group.Scope, string(group.Cardinality),
		group.Generation, formatTime(group.CreatedAt), formatTime(group.UpdatedAt))
	if err != nil {
		return fmt.Errorf("写入事实组 %s: %w", group.ID, err)
	}
	return t.BumpCatalogGeneration(ctx)
}

// UpdateFactGroup persists metadata and increments generation when callers
// already did so.
func (t *Tx) UpdateFactGroup(ctx context.Context, group memory.FactGroup) error {
	result, err := t.tx.ExecContext(ctx, `UPDATE memory_fact_groups SET
		subject = ?, predicate = ?, scope = ?, cardinality = ?, generation = ?, updated_at = ?
		WHERE id = ?`,
		group.Subject, group.Predicate, group.Scope, string(group.Cardinality),
		group.Generation, formatTime(group.UpdatedAt), group.ID)
	if err != nil {
		return fmt.Errorf("更新事实组 %s: %w", group.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新事实组 %s: %w", group.ID, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: fact group %s", memory.ErrNotFound, group.ID)
	}
	return t.BumpCatalogGeneration(ctx)
}

// ListFactGroupMemories returns every current member of a group, including
// archived, pending and rejected snapshots. Deleted tombstones are excluded
// from automatic reasoning.
func (t *Tx) ListFactGroupMemories(ctx context.Context, groupID string) ([]memory.Memory, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memories
		WHERE fact_group_id = ? AND state <> ? ORDER BY valid_from ASC, updated_at ASC, id ASC`,
		groupID, string(memory.StateDeleted))
	if err != nil {
		return nil, fmt.Errorf("读取事实组成员 %s: %w", groupID, err)
	}
	defer rows.Close()
	items := []memory.Memory{}
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("读取事实组成员 %s: %w", groupID, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取事实组成员 %s: %w", groupID, err)
	}
	return items, nil
}
