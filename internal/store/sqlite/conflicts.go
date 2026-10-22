package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

const conflictColumns = `id, COALESCE(fact_group_id, ''), status, reason_code,
	resolution, revision, created_at, updated_at`

// GetConflict reads one conflict with members and immutable audit events.
func (s *Store) GetConflict(ctx context.Context, id string) (memory.Conflict, error) {
	return getConflict(ctx, s.reader, id, true)
}

func (t *Tx) GetConflict(ctx context.Context, id string) (memory.Conflict, error) {
	return getConflict(ctx, t.tx, id, true)
}

// ListConflicts returns the management list without audit events.
func (s *Store) ListConflicts(ctx context.Context, options memory.ConflictListOptions) ([]memory.Conflict, error) {
	return listConflicts(ctx, s.reader, options)
}

type conflictQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getConflict(ctx context.Context, q conflictQueryer, id string, withEvents bool) (memory.Conflict, error) {
	conflict, err := scanConflict(q.QueryRowContext(ctx,
		`SELECT `+conflictColumns+` FROM memory_conflicts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Conflict{}, fmt.Errorf("%w: %s", memory.ErrConflictNotFound, id)
	}
	if err != nil {
		return memory.Conflict{}, fmt.Errorf("读取冲突 %s: %w", id, err)
	}
	if conflict.Members, err = listConflictMembers(ctx, q, id); err != nil {
		return memory.Conflict{}, err
	}
	if withEvents {
		if conflict.Events, err = listConflictEvents(ctx, q, id); err != nil {
			return memory.Conflict{}, err
		}
	}
	return conflict, nil
}

func listConflicts(ctx context.Context, q conflictQueryer, options memory.ConflictListOptions) ([]memory.Conflict, error) {
	conditions := []string{}
	args := []any{}
	if options.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(options.Status))
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	query := `SELECT ` + conflictColumns + ` FROM memory_conflicts`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY updated_at DESC, id ASC LIMIT ?`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取冲突列表: %w", err)
	}
	defer rows.Close()
	items := []memory.Conflict{}
	for rows.Next() {
		conflict, err := scanConflict(rows)
		if err != nil {
			return nil, fmt.Errorf("读取冲突列表: %w", err)
		}
		items = append(items, conflict)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取冲突列表: %w", err)
	}
	for index := range items {
		members, err := listConflictMembers(ctx, q, items[index].ID)
		if err != nil {
			return nil, err
		}
		items[index].Members = members
	}
	return items, nil
}

func scanConflict(row rowScanner) (memory.Conflict, error) {
	var conflict memory.Conflict
	var status, reason, createdAt, updatedAt string
	if err := row.Scan(&conflict.ID, &conflict.FactGroupID, &status, &reason,
		&conflict.Resolution, &conflict.Revision, &createdAt, &updatedAt); err != nil {
		return memory.Conflict{}, err
	}
	conflict.Status = memory.ConflictStatus(status)
	conflict.ReasonCode = memory.ConflictReason(reason)
	var err error
	if conflict.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.Conflict{}, err
	}
	if conflict.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return memory.Conflict{}, err
	}
	return conflict, nil
}

func listConflictMembers(ctx context.Context, q conflictQueryer, conflictID string) ([]memory.ConflictMember, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT m.memory_id, m.version, COALESCE(mem.version, 0)
		 FROM memory_conflict_members m
		 LEFT JOIN memories mem ON mem.id = m.memory_id
		 WHERE m.conflict_id = ? ORDER BY m.memory_id ASC`, conflictID)
	if err != nil {
		return nil, fmt.Errorf("读取冲突成员 %s: %w", conflictID, err)
	}
	defer rows.Close()
	items := []memory.ConflictMember{}
	for rows.Next() {
		var member memory.ConflictMember
		if err := rows.Scan(&member.MemoryID, &member.Version, &member.CurrentVersion); err != nil {
			return nil, fmt.Errorf("读取冲突成员 %s: %w", conflictID, err)
		}
		items = append(items, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取冲突成员 %s: %w", conflictID, err)
	}
	return items, nil
}

func listConflictEvents(ctx context.Context, q conflictQueryer, conflictID string) ([]memory.ConflictEvent, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, conflict_id, action, memory_id, version, created_at
		 FROM memory_conflict_events WHERE conflict_id = ?
		 ORDER BY created_at ASC, id ASC`, conflictID)
	if err != nil {
		return nil, fmt.Errorf("读取冲突事件 %s: %w", conflictID, err)
	}
	defer rows.Close()
	items := []memory.ConflictEvent{}
	for rows.Next() {
		var event memory.ConflictEvent
		var createdAt string
		if err := rows.Scan(&event.ID, &event.ConflictID, &event.Action,
			&event.MemoryID, &event.Version, &createdAt); err != nil {
			return nil, fmt.Errorf("读取冲突事件 %s: %w", conflictID, err)
		}
		var err error
		if event.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取冲突事件 %s: %w", conflictID, err)
	}
	return items, nil
}

// InsertConflict writes a conflict and its initial members.
func (t *Tx) InsertConflict(ctx context.Context, conflict memory.Conflict) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_conflicts(
		id, fact_group_id, status, reason_code, resolution, revision, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		conflict.ID, nullableString(conflict.FactGroupID), string(conflict.Status),
		string(conflict.ReasonCode), conflict.Resolution, conflict.Revision,
		formatTime(conflict.CreatedAt), formatTime(conflict.UpdatedAt))
	if err != nil {
		return fmt.Errorf("写入冲突 %s: %w", conflict.ID, err)
	}
	for _, member := range conflict.Members {
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO memory_conflict_members(
			conflict_id, memory_id, version) VALUES (?, ?, ?)
			ON CONFLICT(conflict_id, memory_id) DO UPDATE SET version = excluded.version`,
			conflict.ID, member.MemoryID, member.Version); err != nil {
			return fmt.Errorf("写入冲突成员 %s/%s: %w", conflict.ID, member.MemoryID, err)
		}
	}
	return t.BumpCatalogGeneration(ctx)
}

// UpdateConflict persists status, resolution text and revision.
func (t *Tx) UpdateConflict(ctx context.Context, conflict memory.Conflict) error {
	result, err := t.tx.ExecContext(ctx, `UPDATE memory_conflicts SET
		fact_group_id = ?, status = ?, reason_code = ?, resolution = ?, revision = ?, updated_at = ?
		WHERE id = ?`,
		nullableString(conflict.FactGroupID), string(conflict.Status),
		string(conflict.ReasonCode), conflict.Resolution, conflict.Revision,
		formatTime(conflict.UpdatedAt), conflict.ID)
	if err != nil {
		return fmt.Errorf("更新冲突 %s: %w", conflict.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新冲突 %s: %w", conflict.ID, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", memory.ErrConflictNotFound, conflict.ID)
	}
	return nil
}

// UpsertConflictMember extends an open conflict with a member or refreshes its
// detection version.
func (t *Tx) UpsertConflictMember(ctx context.Context, conflictID string, member memory.ConflictMember) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_conflict_members(
		conflict_id, memory_id, version) VALUES (?, ?, ?)
		ON CONFLICT(conflict_id, memory_id) DO UPDATE SET version = excluded.version`,
		conflictID, member.MemoryID, member.Version)
	if err != nil {
		return fmt.Errorf("写入冲突成员 %s/%s: %w", conflictID, member.MemoryID, err)
	}
	return nil
}

// InsertConflictEvent appends one immutable audit event.
func (t *Tx) InsertConflictEvent(ctx context.Context, event memory.ConflictEvent) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_conflict_events(
		id, conflict_id, action, memory_id, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID, event.ConflictID, event.Action,
		event.MemoryID, event.Version, formatTime(event.CreatedAt))
	if err != nil {
		return fmt.Errorf("写入冲突事件 %s: %w", event.ID, err)
	}
	return nil
}

// ListOpenConflictsForMemories loads every open conflict that references one of
// the supplied memories. It is used by retrieval conflict discovery.
func (t *Tx) ListOpenConflictsForMemories(ctx context.Context, memoryIDs []string) ([]memory.Conflict, error) {
	return listOpenConflictsForMemories(ctx, t.tx, memoryIDs)
}

func listOpenConflictsForMemories(ctx context.Context, q conflictQueryer, memoryIDs []string) ([]memory.Conflict, error) {
	unique := uniqueIDs(memoryIDs)
	if len(unique) == 0 {
		return []memory.Conflict{}, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT c.id FROM memory_conflicts c
		JOIN memory_conflict_members m ON m.conflict_id = c.id
		WHERE c.status = ? AND m.memory_id IN (`+placeholders(len(unique))+`)
		ORDER BY c.updated_at DESC, c.id ASC`,
		append([]any{string(memory.ConflictOpen)}, stringsToAny(unique)...)...)
	if err != nil {
		return nil, fmt.Errorf("读取相关未决冲突: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("读取相关未决冲突: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("读取相关未决冲突: %w", err)
	}
	_ = rows.Close()
	items := make([]memory.Conflict, 0, len(ids))
	for _, id := range ids {
		conflict, err := getConflict(ctx, q, id, false)
		if err != nil {
			return nil, err
		}
		items = append(items, conflict)
	}
	return items, nil
}

// PruneConflict removes a conflict whose member count dropped below two, e.g.
// after a permanent purge. Its events are removed by the cascade.
func (t *Tx) PruneConflict(ctx context.Context, id string) error {
	var count int
	if err := t.tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_conflict_members WHERE conflict_id = ?`, id).Scan(&count); err != nil {
		return fmt.Errorf("统计冲突成员 %s: %w", id, err)
	}
	if count >= 2 {
		return nil
	}
	if _, err := t.tx.ExecContext(ctx,
		`DELETE FROM memory_conflicts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("清理成员不足的冲突 %s: %w", id, err)
	}
	return nil
}
