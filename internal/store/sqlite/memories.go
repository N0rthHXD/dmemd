package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const memoryColumns = `id, kind, title, content, state, version, observed_at, created_at, updated_at, created_by_app, origin_source_id`

var _ memory.Reader = (*Store)(nil)

type rowScanner interface {
	Scan(dest ...any) error
}

// GetMemory reads the current snapshot, including a deleted tombstone.
func (s *Store) GetMemory(ctx context.Context, id string) (memory.Memory, error) {
	return getMemory(ctx, s.reader, id)
}

// ListMemories reads the management list from the read pool. A zero limit uses
// the contract default of 50, ordering is fixed and there is no paging.
func (s *Store) ListMemories(ctx context.Context, options memory.ListOptions) ([]memory.Memory, error) {
	query, args := buildMemoryListQuery(options)
	return queryMemories(ctx, s.reader, query, args...)
}

func (t *Tx) GetMemory(ctx context.Context, id string) (memory.Memory, error) {
	return getMemory(ctx, t.tx, id)
}

func getMemory(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (memory.Memory, error) {
	item, err := scanMemory(q.QueryRowContext(ctx, `SELECT `+memoryColumns+` FROM memories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Memory{}, fmt.Errorf("%w: %s", memory.ErrNotFound, id)
	}
	if err != nil {
		return memory.Memory{}, fmt.Errorf("读取记忆 %s: %w", id, err)
	}
	return item, nil
}

func scanMemory(row rowScanner) (memory.Memory, error) {
	var item memory.Memory
	var kind, state, observedAt, createdAt, updatedAt string
	var origin sql.NullString
	if err := row.Scan(
		&item.ID, &kind, &item.Title, &item.Content, &state, &item.Version,
		&observedAt, &createdAt, &updatedAt, &item.CreatedByApp, &origin,
	); err != nil {
		return memory.Memory{}, err
	}
	item.Kind = memory.Kind(kind)
	item.State = memory.State(state)
	if origin.Valid {
		item.OriginSourceID = origin.String
	}
	var err error
	if item.ObservedAt, err = parseTime(observedAt); err != nil {
		return memory.Memory{}, err
	}
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.Memory{}, err
	}
	if item.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return memory.Memory{}, err
	}
	return item, nil
}

// InsertMemory writes a current snapshot. Revision and search-document writes
// are separate calls so one transaction can include all of them.
func (t *Tx) InsertMemory(ctx context.Context, item memory.Memory) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memories(`+memoryColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, string(item.Kind), item.Title, item.Content, string(item.State), item.Version,
		formatTime(item.ObservedAt), formatTime(item.CreatedAt), formatTime(item.UpdatedAt),
		item.CreatedByApp, nullableString(item.OriginSourceID),
	)
	if err != nil {
		return fmt.Errorf("写入记忆 %s: %w", item.ID, err)
	}
	return nil
}

// UpdateMemory replaces the current snapshot for an existing memory ID.
func (t *Tx) UpdateMemory(ctx context.Context, item memory.Memory) error {
	result, err := t.tx.ExecContext(ctx, `UPDATE memories SET
		kind = ?, title = ?, content = ?, state = ?, version = ?,
		observed_at = ?, created_at = ?, updated_at = ?, created_by_app = ?, origin_source_id = ?
		WHERE id = ?`,
		string(item.Kind), item.Title, item.Content, string(item.State), item.Version,
		formatTime(item.ObservedAt), formatTime(item.CreatedAt), formatTime(item.UpdatedAt),
		item.CreatedByApp, nullableString(item.OriginSourceID), item.ID,
	)
	if err != nil {
		return fmt.Errorf("更新记忆 %s: %w", item.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新记忆 %s: %w", item.ID, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", memory.ErrNotFound, item.ID)
	}
	return nil
}

const revisionColumns = `memory_id, version, operation, reason, kind, title, content, state,
	observed_at, created_at, updated_at, created_by_app, origin_source_id, revision_at`

// InsertRevision stores one complete historical snapshot.
func (t *Tx) InsertRevision(ctx context.Context, revision memory.Revision) error {
	snapshot := revision.Snapshot
	revisionAt := revision.CreatedAt
	if revisionAt.IsZero() {
		revisionAt = snapshot.UpdatedAt
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_revisions(`+revisionColumns+`) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revision.MemoryID, revision.Version, string(revision.Operation), revision.Reason,
		string(snapshot.Kind), snapshot.Title, snapshot.Content, string(snapshot.State),
		formatTime(snapshot.ObservedAt), formatTime(snapshot.CreatedAt), formatTime(snapshot.UpdatedAt),
		snapshot.CreatedByApp, nullableString(snapshot.OriginSourceID), formatTime(revisionAt),
	)
	if err != nil {
		return fmt.Errorf("写入记忆版本 %s@%d: %w", revision.MemoryID, revision.Version, err)
	}
	return nil
}

// GetRevision reads one complete historical version from the read pool.
func (s *Store) GetRevision(ctx context.Context, memoryID string, version int) (memory.Revision, error) {
	return getRevision(ctx, s.reader, memoryID, version)
}

func (t *Tx) GetRevision(ctx context.Context, memoryID string, version int) (memory.Revision, error) {
	return getRevision(ctx, t.tx, memoryID, version)
}

type revisionQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getRevision(ctx context.Context, q revisionQueryer, memoryID string, version int) (memory.Revision, error) {
	row := q.QueryRowContext(ctx, `SELECT `+revisionColumns+` FROM memory_revisions
		WHERE memory_id = ? AND version = ?`, memoryID, version)
	revision, err := scanRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Revision{}, fmt.Errorf("%w: %s@%d", memory.ErrNotFound, memoryID, version)
	}
	if err != nil {
		return memory.Revision{}, fmt.Errorf("读取记忆版本 %s@%d: %w", memoryID, version, err)
	}
	revision.Evidence, err = listRevisionEvidence(ctx, q, memoryID, version)
	if err != nil {
		return memory.Revision{}, err
	}
	return revision, nil
}

// ListRevisions reads newest-first history from the read pool.
func (s *Store) ListRevisions(ctx context.Context, memoryID string, limit int) ([]memory.Revision, error) {
	return listRevisions(ctx, s.reader, memoryID, limit)
}

func (t *Tx) ListRevisions(ctx context.Context, memoryID string, limit int) ([]memory.Revision, error) {
	return listRevisions(ctx, t.tx, memoryID, limit)
}

func listRevisions(ctx context.Context, q revisionQueryer, memoryID string, limit int) ([]memory.Revision, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.QueryContext(ctx, `SELECT `+revisionColumns+` FROM memory_revisions
		WHERE memory_id = ? ORDER BY version DESC LIMIT ?`, memoryID, limit)
	if err != nil {
		return nil, fmt.Errorf("读取记忆版本列表 %s: %w", memoryID, err)
	}
	defer rows.Close()

	items := []memory.Revision{}
	for rows.Next() {
		revision, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("读取记忆版本列表 %s: %w", memoryID, err)
		}
		revision.Evidence, err = listRevisionEvidence(ctx, q, memoryID, revision.Version)
		if err != nil {
			return nil, err
		}
		items = append(items, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取记忆版本列表 %s: %w", memoryID, err)
	}
	return items, nil
}

func scanRevision(row rowScanner) (memory.Revision, error) {
	var item memory.Revision
	var operation, kind, state string
	var observedAt, createdAt, updatedAt, revisionAt string
	var origin sql.NullString
	if err := row.Scan(
		&item.MemoryID, &item.Version, &operation, &item.Reason,
		&kind, &item.Snapshot.Title, &item.Snapshot.Content, &state,
		&observedAt, &createdAt, &updatedAt, &item.Snapshot.CreatedByApp, &origin, &revisionAt,
	); err != nil {
		return memory.Revision{}, err
	}
	item.Operation = memory.Operation(operation)
	item.Snapshot.ID = item.MemoryID
	item.Snapshot.Version = item.Version
	item.Snapshot.Kind = memory.Kind(kind)
	item.Snapshot.State = memory.State(state)
	if origin.Valid {
		item.Snapshot.OriginSourceID = origin.String
	}
	var err error
	if item.Snapshot.ObservedAt, err = parseTime(observedAt); err != nil {
		return memory.Revision{}, err
	}
	if item.Snapshot.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.Revision{}, err
	}
	if item.Snapshot.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return memory.Revision{}, err
	}
	if item.CreatedAt, err = parseTime(revisionAt); err != nil {
		return memory.Revision{}, err
	}
	return item, nil
}

// LinkRevisionEvidence records the complete evidence set for one version.
func (t *Tx) LinkRevisionEvidence(ctx context.Context, memoryID string, version int, evidenceIDs []string) error {
	for _, evidenceID := range evidenceIDs {
		if _, err := t.tx.ExecContext(ctx,
			`INSERT INTO revision_evidence(memory_id, version, evidence_id) VALUES (?, ?, ?)`,
			memoryID, version, evidenceID,
		); err != nil {
			return fmt.Errorf("关联版本证据 %s@%d/%s: %w", memoryID, version, evidenceID, err)
		}
	}
	return nil
}

// ListRevisionEvidence returns one version's complete evidence set ordered by
// creation time and ID. It is also available through Reader for read-pool use.
func (s *Store) ListRevisionEvidence(ctx context.Context, memoryID string, version int) ([]source.Evidence, error) {
	return listRevisionEvidence(ctx, s.reader, memoryID, version)
}

func (t *Tx) ListRevisionEvidence(ctx context.Context, memoryID string, version int) ([]source.Evidence, error) {
	return listRevisionEvidence(ctx, t.tx, memoryID, version)
}

func listRevisionEvidence(ctx context.Context, q revisionQueryer, memoryID string, version int) ([]source.Evidence, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+evidenceColumns+`
		FROM revision_evidence re JOIN evidence e ON e.id = re.evidence_id
		WHERE re.memory_id = ? AND re.version = ?
		ORDER BY e.created_at ASC, e.id ASC`, memoryID, version)
	if err != nil {
		return nil, fmt.Errorf("读取版本证据 %s@%d: %w", memoryID, version, err)
	}
	defer rows.Close()
	return scanEvidenceRows(rows)
}

// UpsertSearchDocument writes the derived document. Triggers synchronize the
// external-content FTS index in the same statement.
func (t *Tx) UpsertSearchDocument(ctx context.Context, document memory.SearchDocument) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_search_docs
		(memory_id, state, title, content, title_terms, content_terms, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(memory_id) DO UPDATE SET
			state = excluded.state,
			title = excluded.title,
			content = excluded.content,
			title_terms = excluded.title_terms,
			content_terms = excluded.content_terms,
			updated_at = excluded.updated_at`,
		document.MemoryID, string(document.State), document.Title, document.Content,
		document.TitleTerms, document.ContentTerms, formatTime(document.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("写入搜索文档 %s: %w", document.MemoryID, err)
	}
	return nil
}

// DeleteSearchDocument removes a derived document and its FTS row.
func (t *Tx) DeleteSearchDocument(ctx context.Context, memoryID string) error {
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM memory_search_docs WHERE memory_id = ?`, memoryID); err != nil {
		return fmt.Errorf("删除搜索文档 %s: %w", memoryID, err)
	}
	return nil
}

// PurgeMemory permanently removes one memory and its dedicated data. Sources
// are never deleted. Evidence still linked to another memory's revision is
// shared and therefore retained. The search document is deleted explicitly so
// the FTS delete trigger runs inside the same transaction.
func (t *Tx) PurgeMemory(ctx context.Context, id string) error {
	var exists int
	err := t.tx.QueryRowContext(ctx, `SELECT 1 FROM memories WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", memory.ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("检查待清除记忆 %s: %w", id, err)
	}

	if _, err := t.tx.ExecContext(ctx, `DELETE FROM memory_search_docs WHERE memory_id = ?`, id); err != nil {
		return fmt.Errorf("清除记忆 %s 的搜索文档: %w", id, err)
	}
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM evidence
		WHERE id IN (SELECT evidence_id FROM revision_evidence WHERE memory_id = ?)
		AND NOT EXISTS (
			SELECT 1 FROM revision_evidence other
			WHERE other.evidence_id = evidence.id AND other.memory_id <> ?
		)`, id, id); err != nil {
		return fmt.Errorf("清除记忆 %s 的专属证据: %w", id, err)
	}
	result, err := t.tx.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("清除记忆 %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("清除记忆 %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", memory.ErrNotFound, id)
	}
	return nil
}

func buildMemoryListQuery(options memory.ListOptions) (string, []any) {
	var conditions []string
	var args []any
	if len(options.Kinds) > 0 {
		conditions = append(conditions, "kind IN ("+placeholders(len(options.Kinds))+")")
		for _, kind := range options.Kinds {
			args = append(args, string(kind))
		}
	}
	if len(options.States) > 0 {
		conditions = append(conditions, "state IN ("+placeholders(len(options.States))+")")
		for _, state := range options.States {
			args = append(args, string(state))
		}
	}
	if options.CreatedByApp != "" {
		conditions = append(conditions, "created_by_app = ?")
		args = append(args, options.CreatedByApp)
	}
	if options.UpdatedAfter != nil {
		conditions = append(conditions, "updated_at >= ?")
		args = append(args, formatTime(*options.UpdatedAfter))
	}
	if options.UpdatedBefore != nil {
		conditions = append(conditions, "updated_at <= ?")
		args = append(args, formatTime(*options.UpdatedBefore))
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)

	query := `SELECT ` + memoryColumns + ` FROM memories`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY updated_at DESC, id ASC LIMIT ?`
	return query, args
}

func queryMemories(ctx context.Context, db *sql.DB, query string, args ...any) ([]memory.Memory, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取记忆列表: %w", err)
	}
	defer rows.Close()
	items := []memory.Memory{}
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("读取记忆列表: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取记忆列表: %w", err)
	}
	return items, nil
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
