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

const memoryColumns = `id, kind, phase, durability, title, content, state, assertion_status, version, observed_at,
	valid_from, valid_to, valid_from_basis, valid_to_basis, fact_group_id,
	created_at, updated_at, created_by_app, origin_source_id, scope_key, topics_json, raw_topics_json`

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
	var kind, phase, durability, state, assertion, observedAt, createdAt, updatedAt string
	var fromBasis, toBasis string
	var validFrom, validTo, origin, group sql.NullString
	var topicsJSON, rawTopicsJSON string
	if err := row.Scan(
		&item.ID, &kind, &phase, &durability, &item.Title, &item.Content, &state, &assertion, &item.Version,
		&observedAt, &validFrom, &validTo, &fromBasis, &toBasis, &group,
		&createdAt, &updatedAt, &item.CreatedByApp, &origin, &item.ScopeKey, &topicsJSON, &rawTopicsJSON,
	); err != nil {
		return memory.Memory{}, err
	}
	item.Kind = memory.Kind(kind)
	item.Phase = memory.Phase(phase)
	item.Durability = memory.Durability(durability)
	item.State = memory.State(state)
	item.AssertionStatus = memory.AssertionStatus(assertion)
	item.ValidFromBasis = memory.TemporalBasis(fromBasis)
	item.ValidToBasis = memory.TemporalBasis(toBasis)
	if origin.Valid {
		item.OriginSourceID = origin.String
	}
	if group.Valid {
		item.FactGroupID = group.String
	}
	var err error
	if item.Topics, err = decodeTopics(topicsJSON); err != nil {
		return memory.Memory{}, fmt.Errorf("读取记忆 %s 的主题: %w", item.ID, err)
	}
	if item.RawTopics, err = decodeTopics(rawTopicsJSON); err != nil {
		return memory.Memory{}, err
	}
	if item.ObservedAt, err = parseTime(observedAt); err != nil {
		return memory.Memory{}, err
	}
	if item.ValidFrom, err = nullableTime(validFrom); err != nil {
		return memory.Memory{}, err
	}
	if item.ValidTo, err = nullableTime(validTo); err != nil {
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
	item = normalizedStoredMemory(item)
	topics, err := encodeTopics(item.Topics)
	if err != nil {
		return err
	}
	rawTopics, err := encodeTopics(item.RawTopics)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO memories(`+memoryColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, string(item.Kind), string(item.Phase), string(item.Durability), item.Title, item.Content, string(item.State),
		string(item.AssertionStatus), item.Version, formatTime(item.ObservedAt),
		timeValue(item.ValidFrom), timeValue(item.ValidTo),
		string(item.ValidFromBasis), string(item.ValidToBasis), nullableString(item.FactGroupID),
		formatTime(item.CreatedAt), formatTime(item.UpdatedAt),
		item.CreatedByApp, nullableString(item.OriginSourceID), item.ScopeKey, topics, rawTopics,
	)
	if err != nil {
		return fmt.Errorf("写入记忆 %s: %w", item.ID, err)
	}
	if err := t.bumpFactGroupGeneration(ctx, item.FactGroupID, item.UpdatedAt); err != nil {
		return err
	}
	return t.BumpCatalogGeneration(ctx)
}

// UpdateMemory replaces the current snapshot for an existing memory ID.
func (t *Tx) UpdateMemory(ctx context.Context, item memory.Memory) error {
	item = normalizedStoredMemory(item)
	topics, err := encodeTopics(item.Topics)
	if err != nil {
		return err
	}
	var previousGroup sql.NullString
	rawTopics, err := encodeTopics(item.RawTopics)
	if err != nil {
		return err
	}
	if err := t.tx.QueryRowContext(ctx,
		`SELECT fact_group_id FROM memories WHERE id = ?`, item.ID).Scan(&previousGroup); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", memory.ErrNotFound, item.ID)
		}
		return fmt.Errorf("读取记忆 %s 的旧事实组: %w", item.ID, err)
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE memories SET
		kind = ?, phase = ?, durability = ?, title = ?, content = ?, state = ?, assertion_status = ?, version = ?,
		observed_at = ?, valid_from = ?, valid_to = ?, valid_from_basis = ?, valid_to_basis = ?,
		fact_group_id = ?, created_at = ?, updated_at = ?, created_by_app = ?, origin_source_id = ?,
		scope_key = ?, topics_json = ?, raw_topics_json = ?
		WHERE id = ?`,
		string(item.Kind), string(item.Phase), string(item.Durability), item.Title, item.Content, string(item.State),
		string(item.AssertionStatus), item.Version, formatTime(item.ObservedAt),
		timeValue(item.ValidFrom), timeValue(item.ValidTo),
		string(item.ValidFromBasis), string(item.ValidToBasis), nullableString(item.FactGroupID),
		formatTime(item.CreatedAt), formatTime(item.UpdatedAt),
		item.CreatedByApp, nullableString(item.OriginSourceID), item.ScopeKey, topics, rawTopics, item.ID,
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
	if err := t.bumpFactGroupGeneration(ctx, item.FactGroupID, item.UpdatedAt); err != nil {
		return err
	}
	if previousGroup.Valid && previousGroup.String != item.FactGroupID {
		if err := t.bumpFactGroupGeneration(ctx, previousGroup.String, item.UpdatedAt); err != nil {
			return err
		}
	}
	return t.BumpCatalogGeneration(ctx)
}

// bumpFactGroupGeneration records a member or metadata change on one group.
func (t *Tx) bumpFactGroupGeneration(ctx context.Context, groupID string, now time.Time) error {
	if groupID == "" {
		return nil
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE memory_fact_groups
		SET generation = generation + 1, updated_at = ? WHERE id = ?`,
		formatTime(now), groupID); err != nil {
		return fmt.Errorf("递增事实组 %s 的 generation: %w", groupID, err)
	}
	return nil
}

const revisionColumns = `memory_id, version, operation, reason, kind, title, content, state, phase, durability,
	assertion_status, observed_at, valid_from, valid_to, valid_from_basis, valid_to_basis,
	fact_group_id, created_at, updated_at, created_by_app, origin_source_id, scope_key, topics_json, raw_topics_json`

// InsertRevision stores one complete historical snapshot. Its revision time is
// the snapshot's updated_at.
func (t *Tx) InsertRevision(ctx context.Context, revision memory.Revision) error {
	snapshot := normalizedStoredMemory(revision.Snapshot)
	topics, err := encodeTopics(snapshot.Topics)
	if err != nil {
		return err
	}
	rawTopics, err := encodeTopics(snapshot.RawTopics)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO memory_revisions(`+revisionColumns+`) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revision.MemoryID, revision.Version, string(revision.Operation), revision.Reason,
		string(snapshot.Kind), snapshot.Title, snapshot.Content, string(snapshot.State), string(snapshot.Phase), string(snapshot.Durability),
		string(snapshot.AssertionStatus), formatTime(snapshot.ObservedAt),
		timeValue(snapshot.ValidFrom), timeValue(snapshot.ValidTo),
		string(snapshot.ValidFromBasis), string(snapshot.ValidToBasis),
		nullableString(snapshot.FactGroupID), formatTime(snapshot.CreatedAt),
		formatTime(snapshot.UpdatedAt), snapshot.CreatedByApp, nullableString(snapshot.OriginSourceID),
		snapshot.ScopeKey, topics, rawTopics,
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
	var operation, kind, state, phase, durability, assertion, fromBasis, toBasis string
	var observedAt, createdAt, updatedAt, topicsJSON, rawTopicsJSON string
	var validFrom, validTo, origin, group sql.NullString
	if err := row.Scan(
		&item.MemoryID, &item.Version, &operation, &item.Reason,
		&kind, &item.Snapshot.Title, &item.Snapshot.Content, &state, &phase, &durability, &assertion,
		&observedAt, &validFrom, &validTo, &fromBasis, &toBasis, &group,
		&createdAt, &updatedAt, &item.Snapshot.CreatedByApp, &origin,
		&item.Snapshot.ScopeKey, &topicsJSON, &rawTopicsJSON,
	); err != nil {
		return memory.Revision{}, err
	}
	item.Operation = memory.Operation(operation)
	item.Snapshot.ID = item.MemoryID
	item.Snapshot.Version = item.Version
	item.Snapshot.Kind = memory.Kind(kind)
	item.Snapshot.Phase = memory.Phase(phase)
	item.Snapshot.Durability = memory.Durability(durability)
	item.Snapshot.State = memory.State(state)
	item.Snapshot.AssertionStatus = memory.AssertionStatus(assertion)
	item.Snapshot.ValidFromBasis = memory.TemporalBasis(fromBasis)
	item.Snapshot.ValidToBasis = memory.TemporalBasis(toBasis)
	if origin.Valid {
		item.Snapshot.OriginSourceID = origin.String
	}
	if group.Valid {
		item.Snapshot.FactGroupID = group.String
	}
	var err error
	if item.Snapshot.Topics, err = decodeTopics(topicsJSON); err != nil {
		return memory.Revision{}, fmt.Errorf("读取记忆版本 %s@%d 的主题: %w", item.MemoryID, item.Version, err)
	}
	if item.Snapshot.RawTopics, err = decodeTopics(rawTopicsJSON); err != nil {
		return memory.Revision{}, err
	}
	if item.Snapshot.ObservedAt, err = parseTime(observedAt); err != nil {
		return memory.Revision{}, err
	}
	if item.Snapshot.ValidFrom, err = nullableTime(validFrom); err != nil {
		return memory.Revision{}, err
	}
	if item.Snapshot.ValidTo, err = nullableTime(validTo); err != nil {
		return memory.Revision{}, err
	}
	if item.Snapshot.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.Revision{}, err
	}
	if item.Snapshot.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return memory.Revision{}, err
	}
	item.CreatedAt = item.Snapshot.UpdatedAt
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
	if options.FactGroupID != "" {
		conditions = append(conditions, "fact_group_id = ?")
		args = append(args, options.FactGroupID)
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

// normalizedStoredMemory applies the snapshot defaults required by the CHECK
// constraints. The memory write service already normalizes these fields; this
// guard keeps direct storage callers honest as well.
func normalizedStoredMemory(item memory.Memory) memory.Memory {
	if item.Phase == "" {
		item.Phase = memory.PhaseUnknown
	}
	if item.Durability == "" {
		item.Durability = memory.DurabilityUnknown
	}
	item.ScopeKey = memory.NormalizeScopeKey(item.ScopeKey)
	if item.RawTopics == nil {
		item.RawTopics = item.Topics
	}
	item.Topics = memory.CanonicalTopics(item.Topics)
	if item.AssertionStatus == "" {
		item.AssertionStatus = memory.AssertionAccepted
	}
	if item.ValidFrom == nil {
		item.ValidFromBasis = memory.BasisUnknown
	} else if item.ValidFromBasis == "" || item.ValidFromBasis == memory.BasisUnknown {
		item.ValidFromBasis = memory.BasisInferred
	}
	if item.ValidTo == nil {
		item.ValidToBasis = memory.BasisUnknown
	} else if item.ValidToBasis == "" || item.ValidToBasis == memory.BasisUnknown {
		item.ValidToBasis = memory.BasisInferred
	}
	return item
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
