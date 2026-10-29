package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const memoryColumns = `id, kind, title, content, state, assertion_status, version,
	created_at, updated_at, origin_source_id, scope_key`

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

// ListMemoryCandidates returns a bounded, deterministic candidate set for
// observation governance. Only memories in the same scope that are still
// eligible as a current view participate; deleted and rejected rows are not
// offered to the relation model.
func (s *Store) ListMemoryCandidates(ctx context.Context, scopeKey string, limit int) ([]memory.Memory, error) {
	return listMemoryCandidates(ctx, s.reader, scopeKey, limit)
}

// ListMemoryCandidates is the transaction-scoped variant used by governance
// commit re-validation.
func (t *Tx) ListMemoryCandidates(ctx context.Context, scopeKey string, limit int) ([]memory.Memory, error) {
	return listMemoryCandidates(ctx, t.tx, scopeKey, limit)
}

func listMemoryCandidates(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, scopeKey string, limit int) ([]memory.Memory, error) {
	if limit <= 0 {
		limit = 32
	}
	if limit > 128 {
		limit = 128
	}
	rows, err := q.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memories
		WHERE scope_key = ? AND state IN ('active', 'archived')
			AND assertion_status IN ('accepted', 'pending')
		ORDER BY updated_at DESC, id ASC LIMIT ?`, scopeKey, limit)
	if err != nil {
		return nil, fmt.Errorf("读取作用域 %s 的治理候选记忆: %w", scopeKey, err)
	}
	defer rows.Close()
	items := []memory.Memory{}
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("读取作用域 %s 的治理候选记忆: %w", scopeKey, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取作用域 %s 的治理候选记忆: %w", scopeKey, err)
	}
	return items, nil
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
	var kind, state, assertion, createdAt, updatedAt string
	var origin sql.NullString
	if err := row.Scan(
		&item.ID, &kind, &item.Title, &item.Content, &state, &assertion, &item.Version,
		&createdAt, &updatedAt, &origin, &item.ScopeKey,
	); err != nil {
		return memory.Memory{}, err
	}
	item.Kind = memory.Kind(kind)
	item.State = memory.State(state)
	item.AssertionStatus = memory.AssertionStatus(assertion)
	if origin.Valid {
		item.OriginSourceID = origin.String
	}
	var err error
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
	item = normalizedStoredMemory(item)
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memories(
		id, kind, title, content, state, assertion_status, version,
		created_at, updated_at, origin_source_id, scope_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, string(item.Kind), item.Title, item.Content, string(item.State),
		string(item.AssertionStatus), item.Version,
		formatTime(item.CreatedAt), formatTime(item.UpdatedAt),
		nullableString(item.OriginSourceID), item.ScopeKey,
	)
	if err != nil {
		return fmt.Errorf("写入记忆 %s: %w", item.ID, err)
	}
	return t.BumpCatalogGeneration(ctx)
}

// UpdateMemory replaces the current canonical snapshot for an existing memory.
func (t *Tx) UpdateMemory(ctx context.Context, item memory.Memory) error {
	item = normalizedStoredMemory(item)
	result, err := t.tx.ExecContext(ctx, `UPDATE memories SET
		kind = ?, title = ?, content = ?, state = ?, assertion_status = ?, version = ?,
		created_at = ?, updated_at = ?, origin_source_id = ?, scope_key = ?
		WHERE id = ?`,
		string(item.Kind), item.Title, item.Content, string(item.State),
		string(item.AssertionStatus), item.Version, formatTime(item.CreatedAt), formatTime(item.UpdatedAt),
		nullableString(item.OriginSourceID), item.ScopeKey, item.ID,
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
	return t.BumpCatalogGeneration(ctx)
}

const revisionColumns = `r.memory_id, r.version, r.operation, r.reason, r.kind, r.title, r.content,
	r.state, r.assertion_status, r.created_at, r.origin_source_id, r.scope_key, m.created_at`

const revisionInsertColumns = `memory_id, version, operation, reason, kind, title, content,
	state, assertion_status, scope_key, origin_source_id, created_at`

// InsertRevision stores one complete historical snapshot. Its revision time is
// the revision's CreatedAt, falling back to the snapshot's UpdatedAt.
func (t *Tx) InsertRevision(ctx context.Context, revision memory.Revision) error {
	snapshot := normalizedStoredMemory(revision.Snapshot)
	createdAt := revision.CreatedAt
	if createdAt.IsZero() {
		createdAt = snapshot.UpdatedAt
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_revisions(`+revisionInsertColumns+`) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revision.MemoryID, revision.Version, string(revision.Operation), revision.Reason,
		string(snapshot.Kind), snapshot.Title, snapshot.Content, string(snapshot.State),
		string(snapshot.AssertionStatus), snapshot.ScopeKey,
		nullableString(snapshot.OriginSourceID), formatTime(createdAt),
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
	row := q.QueryRowContext(ctx, `SELECT `+revisionColumns+` FROM memory_revisions r
		JOIN memories m ON m.id = r.memory_id
		WHERE r.memory_id = ? AND r.version = ?`, memoryID, version)
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
	rows, err := q.QueryContext(ctx, `SELECT `+revisionColumns+` FROM memory_revisions r
		JOIN memories m ON m.id = r.memory_id
		WHERE r.memory_id = ? ORDER BY r.version DESC LIMIT ?`, memoryID, limit)
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
	var operation, kind, state, assertion, createdAt, memoryCreatedAt string
	var origin sql.NullString
	if err := row.Scan(
		&item.MemoryID, &item.Version, &operation, &item.Reason,
		&kind, &item.Snapshot.Title, &item.Snapshot.Content, &state, &assertion,
		&createdAt, &origin, &item.Snapshot.ScopeKey, &memoryCreatedAt,
	); err != nil {
		return memory.Revision{}, err
	}
	item.Operation = memory.Operation(operation)
	item.Snapshot.ID = item.MemoryID
	item.Snapshot.Version = item.Version
	item.Snapshot.Kind = memory.Kind(kind)
	item.Snapshot.State = memory.State(state)
	item.Snapshot.AssertionStatus = memory.AssertionStatus(assertion)
	if origin.Valid {
		item.Snapshot.OriginSourceID = origin.String
	}
	var err error
	if item.Snapshot.CreatedAt, err = parseTime(memoryCreatedAt); err != nil {
		return memory.Revision{}, err
	}
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.Revision{}, err
	}
	item.Snapshot.UpdatedAt = item.CreatedAt
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

// UpsertSearchDocument writes the derived document and maintains the embedding
// generation in the same transaction. Triggers synchronize the external-content
// FTS index in the same statement.
//
// Input changes delete the old vector and task, then create a fresh task with a
// reset attempt budget and no claim token. An unchanged input reuses a valid
// vector; a missing or profile-mismatched vector only gets a task when none with
// the same generation exists, so attempts and retry time are never reset.
func (t *Tx) UpsertSearchDocument(ctx context.Context, document memory.SearchDocument) error {
	var previousHash string
	err := t.tx.QueryRowContext(ctx,
		`SELECT content_hash FROM memory_search_docs WHERE memory_id = ?`,
		document.MemoryID,
	).Scan(&previousHash)
	exists := true
	if errors.Is(err, sql.ErrNoRows) {
		exists = false
	} else if err != nil {
		return fmt.Errorf("读取搜索文档 %s: %w", document.MemoryID, err)
	}

	_, err = t.tx.ExecContext(ctx, `INSERT INTO memory_search_docs
		(memory_id, title_terms, content_terms, content_hash)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(memory_id) DO UPDATE SET
			title_terms = excluded.title_terms,
			content_terms = excluded.content_terms,
			content_hash = excluded.content_hash`,
		document.MemoryID, document.TitleTerms, document.ContentTerms, document.ContentHash,
	)
	if err != nil {
		return fmt.Errorf("写入搜索文档 %s: %w", document.MemoryID, err)
	}

	changed := !exists || previousHash != document.ContentHash
	now := time.Now().UTC()
	if changed {
		if _, err := t.tx.ExecContext(ctx,
			`DELETE FROM memory_embeddings WHERE memory_id = ?`, document.MemoryID,
		); err != nil {
			return fmt.Errorf("清除记忆 %s 的过期向量: %w", document.MemoryID, err)
		}
		if _, err := t.tx.ExecContext(ctx,
			`DELETE FROM memory_embedding_jobs WHERE memory_id = ?`, document.MemoryID,
		); err != nil {
			return fmt.Errorf("清除记忆 %s 的过期向量任务: %w", document.MemoryID, err)
		}
	}

	enabled, profileID := false, ""
	if t.store != nil {
		enabled, profileID = t.store.embeddingConfig()
	}
	if !enabled || profileID == "" || document.ContentHash == "" {
		return nil
	}

	var valid int
	err = t.tx.QueryRowContext(ctx, `SELECT 1 FROM memory_embeddings
		WHERE memory_id = ? AND profile_id = ? AND content_hash = ?
		LIMIT 1`, document.MemoryID, profileID, document.ContentHash).Scan(&valid)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("检查记忆 %s 的向量: %w", document.MemoryID, err)
	}

	if changed {
		_, err = t.tx.ExecContext(ctx, `INSERT INTO memory_embedding_jobs
			(memory_id, profile_id, content_hash, status, attempts,
			 next_attempt_at, error_code, created_at, updated_at)
			VALUES (?, ?, ?, 'queued', 0, NULL, '', ?, ?)`,
			document.MemoryID, profileID, document.ContentHash, formatTime(now), formatTime(now),
		)
	} else {
		_, err = t.tx.ExecContext(ctx, `INSERT INTO memory_embedding_jobs
			(memory_id, profile_id, content_hash, status, attempts,
			 next_attempt_at, error_code, created_at, updated_at)
			VALUES (?, ?, ?, 'queued', 0, NULL, '', ?, ?)
			ON CONFLICT(memory_id) DO UPDATE SET
				profile_id = excluded.profile_id,
				content_hash = excluded.content_hash,
				status = 'queued',
				attempts = 0,
				next_attempt_at = NULL,
				error_code = '',
				created_at = excluded.created_at,
				updated_at = excluded.updated_at
			WHERE memory_embedding_jobs.profile_id <> excluded.profile_id
				OR memory_embedding_jobs.content_hash <> excluded.content_hash`,
			document.MemoryID, profileID, document.ContentHash, formatTime(now), formatTime(now),
		)
	}
	if err != nil {
		return fmt.Errorf("写入记忆 %s 的向量任务: %w", document.MemoryID, err)
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

	conflictRows, err := t.tx.QueryContext(ctx,
		`SELECT DISTINCT conflict_id FROM memory_conflict_members WHERE memory_id = ?`, id)
	if err != nil {
		return fmt.Errorf("读取记忆 %s 的冲突引用: %w", id, err)
	}
	var conflictIDs []string
	for conflictRows.Next() {
		var conflictID string
		if err := conflictRows.Scan(&conflictID); err != nil {
			_ = conflictRows.Close()
			return fmt.Errorf("读取记忆 %s 的冲突引用: %w", id, err)
		}
		conflictIDs = append(conflictIDs, conflictID)
	}
	if err := conflictRows.Err(); err != nil {
		_ = conflictRows.Close()
		return fmt.Errorf("读取记忆 %s 的冲突引用: %w", id, err)
	}
	_ = conflictRows.Close()

	if _, err := t.tx.ExecContext(ctx, `DELETE FROM memory_relations
		WHERE (from_type = 'memory' AND from_id = ?)
		   OR (to_type = 'memory' AND to_id = ?)`, id, id); err != nil {
		return fmt.Errorf("清除记忆 %s 的关系: %w", id, err)
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
	for _, conflictID := range conflictIDs {
		if err := t.PruneConflict(ctx, conflictID); err != nil {
			return err
		}
	}
	return t.BumpCatalogGeneration(ctx)
}

// GetCatalogGeneration reads the single-row semantic catalog version.
func (s *Store) GetCatalogGeneration(ctx context.Context) (int64, error) {
	return getCatalogGeneration(ctx, s.reader)
}

func (t *Tx) GetCatalogGeneration(ctx context.Context) (int64, error) {
	return getCatalogGeneration(ctx, t.tx)
}

type queryRowScanner interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getCatalogGeneration(ctx context.Context, q queryRowScanner) (int64, error) {
	var generation int64
	if err := q.QueryRowContext(ctx,
		`SELECT generation FROM memory_catalog WHERE id = 1`).Scan(&generation); err != nil {
		return 0, fmt.Errorf("读取语义目录版本: %w", err)
	}
	return generation, nil
}

// BumpCatalogGeneration increments the conservative single-process catalog
// version used to detect facts that arrived while a model was deciding.
func (t *Tx) BumpCatalogGeneration(ctx context.Context) error {
	if _, err := t.tx.ExecContext(ctx,
		`UPDATE memory_catalog SET generation = generation + 1 WHERE id = 1`); err != nil {
		return fmt.Errorf("递增语义目录版本: %w", err)
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
	if len(options.AssertionStatus) > 0 {
		conditions = append(conditions, "assertion_status IN ("+placeholders(len(options.AssertionStatus))+")")
		for _, status := range options.AssertionStatus {
			args = append(args, string(status))
		}
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

// normalizedStoredMemory applies the snapshot defaults required by the current
// storage contract. The memory write service already normalizes these fields;
// this guard keeps direct storage callers honest as well.
func normalizedStoredMemory(item memory.Memory) memory.Memory {
	item.ScopeKey = memory.NormalizeScopeKey(item.ScopeKey)
	if item.Kind == "" {
		item.Kind = memory.KindFact
	}
	if item.AssertionStatus == "" {
		item.AssertionStatus = memory.AssertionAccepted
	}
	if item.State == "" {
		item.State = memory.StateActive
	}
	return item
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
