package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

const relationColumns = `r.id, r.from_memory_id, r.from_version, r.to_memory_id,
	r.to_version, r.kind, r.created_at`

// InsertRelation writes one directed supersedes relation. Repeated relations
// are idempotent, so re-detecting the same pair does not accumulate rows.
func (t *Tx) InsertRelation(ctx context.Context, relation memory.Relation) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_relations(
		id, from_memory_id, from_version, to_memory_id, to_version, kind, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(from_memory_id, to_memory_id, kind) DO NOTHING`,
		relation.ID, relation.FromMemoryID, relation.FromVersion,
		relation.ToMemoryID, relation.ToVersion, string(relation.Kind),
		formatTime(relation.CreatedAt))
	if err != nil {
		return fmt.Errorf("写入记忆关系 %s: %w", relation.ID, err)
	}
	return nil
}

// ListRelationsForMemory returns relations in either direction together with
// whether each endpoint moved past the decision-time version.
type relationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listRelationsForMemory(ctx context.Context, q relationQueryer, memoryID string) ([]memory.Relation, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+relationColumns+`,
		COALESCE(f.version, 0), COALESCE(tg.version, 0)
		FROM memory_relations r
		LEFT JOIN memories f ON f.id = r.from_memory_id
		LEFT JOIN memories tg ON tg.id = r.to_memory_id
		WHERE r.from_memory_id = ? OR r.to_memory_id = ?
		ORDER BY r.created_at ASC, r.id ASC`, memoryID, memoryID)
	if err != nil {
		return nil, fmt.Errorf("读取记忆关系 %s: %w", memoryID, err)
	}
	defer rows.Close()
	items := []memory.Relation{}
	for rows.Next() {
		item, err := scanRelation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取记忆关系 %s: %w", memoryID, err)
	}
	return items, nil
}

func scanRelation(row rowScanner) (memory.Relation, error) {
	var relation memory.Relation
	var kind, createdAt string
	var fromCurrent, toCurrent int
	if err := row.Scan(&relation.ID, &relation.FromMemoryID, &relation.FromVersion,
		&relation.ToMemoryID, &relation.ToVersion, &kind,
		&createdAt, &fromCurrent, &toCurrent); err != nil {
		return memory.Relation{}, err
	}
	relation.Kind = memory.RelationKind(kind)
	var err error
	if relation.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.Relation{}, err
	}
	relation.FromVersionChanged = fromCurrent != relation.FromVersion
	relation.ToVersionChanged = toCurrent != relation.ToVersion
	return relation, nil
}
