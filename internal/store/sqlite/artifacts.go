package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
)

const artifactColumns = `id, kind, target_key, scope_key, topic, current_version, state, updated_at`

const artifactRevisionColumns = `artifact_id, version, title, content, payload_json,
	content_hash, generator, protocol_version, reason_code, truncated, created_at`

var (
	_ artifact.Backend = (*Store)(nil)
	_ artifact.Tx      = (*Tx)(nil)
)

// GetArtifact reads one derived object from the read pool.
func (s *Store) GetArtifact(ctx context.Context, id string) (artifact.Artifact, error) {
	return getArtifact(ctx, s.reader, id)
}

// GetArtifactByTarget reads the unique object for one kind and target key.
func (s *Store) GetArtifactByTarget(ctx context.Context, kind artifact.Kind, targetKey string) (artifact.Artifact, error) {
	return getArtifactByTarget(ctx, s.reader, kind, targetKey)
}

// GetArtifactRevision reads one immutable revision and its direct sources.
func (s *Store) GetArtifactRevision(ctx context.Context, id string, version int) (artifact.Revision, error) {
	return getArtifactRevision(ctx, s.reader, id, version)
}

// ListArtifactRevisions reads newest-first history.
func (s *Store) ListArtifactRevisions(ctx context.Context, id string, limit int) ([]artifact.Revision, error) {
	return listArtifactRevisions(ctx, s.reader, id, limit)
}

func (t *Tx) GetArtifact(ctx context.Context, id string) (artifact.Artifact, error) {
	return getArtifact(ctx, t.tx, id)
}

func (t *Tx) GetArtifactByTarget(ctx context.Context, kind artifact.Kind, targetKey string) (artifact.Artifact, error) {
	return getArtifactByTarget(ctx, t.tx, kind, targetKey)
}

func (t *Tx) GetArtifactRevision(ctx context.Context, id string, version int) (artifact.Revision, error) {
	return getArtifactRevision(ctx, t.tx, id, version)
}

type artifactQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getArtifact(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (artifact.Artifact, error) {
	item, err := scanArtifact(q.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.Artifact{}, fmt.Errorf("%w: %s", artifact.ErrNotFound, id)
	}
	if err != nil {
		return artifact.Artifact{}, fmt.Errorf("读取派生对象 %s: %w", id, err)
	}
	return item, nil
}

func getArtifactByTarget(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, kind artifact.Kind, targetKey string) (artifact.Artifact, error) {
	item, err := scanArtifact(q.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE kind = ? AND target_key = ?`,
		string(kind), targetKey))
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.Artifact{}, fmt.Errorf("%w: %s/%s", artifact.ErrNotFound, kind, targetKey)
	}
	if err != nil {
		return artifact.Artifact{}, fmt.Errorf("读取派生对象 %s/%s: %w", kind, targetKey, err)
	}
	return item, nil
}

func scanArtifact(row rowScanner) (artifact.Artifact, error) {
	var item artifact.Artifact
	var kind, state, updated string
	if err := row.Scan(&item.ID, &kind, &item.TargetKey, &item.ScopeKey, &item.Topic,
		&item.CurrentVersion, &state, &updated); err != nil {
		return artifact.Artifact{}, err
	}
	item.Kind = artifact.Kind(kind)
	item.State = artifact.State(state)
	parsed, err := parseTime(updated)
	if err != nil {
		return artifact.Artifact{}, err
	}
	item.UpdatedAt = parsed
	return item, nil
}

func getArtifactRevision(ctx context.Context, q artifactQueryer, id string, version int) (artifact.Revision, error) {
	revision, err := scanArtifactRevision(q.QueryRowContext(ctx,
		`SELECT `+artifactRevisionColumns+` FROM artifact_revisions
		 WHERE artifact_id = ? AND version = ?`, id, version))
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.Revision{}, fmt.Errorf("%w: %s@%d", artifact.ErrNotFound, id, version)
	}
	if err != nil {
		return artifact.Revision{}, fmt.Errorf("读取派生版本 %s@%d: %w", id, version, err)
	}
	revision.MemoryRefs, err = listArtifactMemoryRefs(ctx, q, id, version)
	if err != nil {
		return artifact.Revision{}, err
	}
	revision.RunRefs, err = listArtifactRunRefs(ctx, q, id, version)
	if err != nil {
		return artifact.Revision{}, err
	}
	return revision, nil
}

func listArtifactRevisions(ctx context.Context, q artifactQueryer, id string, limit int) ([]artifact.Revision, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.QueryContext(ctx,
		`SELECT `+artifactRevisionColumns+` FROM artifact_revisions
		 WHERE artifact_id = ? ORDER BY version DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("读取派生版本列表 %s: %w", id, err)
	}
	defer rows.Close()
	items := []artifact.Revision{}
	for rows.Next() {
		revision, err := scanArtifactRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("读取派生版本列表 %s: %w", id, err)
		}
		revision.MemoryRefs, err = listArtifactMemoryRefs(ctx, q, id, revision.Version)
		if err != nil {
			return nil, err
		}
		revision.RunRefs, err = listArtifactRunRefs(ctx, q, id, revision.Version)
		if err != nil {
			return nil, err
		}
		items = append(items, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取派生版本列表 %s: %w", id, err)
	}
	return items, nil
}

func scanArtifactRevision(row rowScanner) (artifact.Revision, error) {
	var item artifact.Revision
	var payload, created string
	var truncated int
	if err := row.Scan(&item.ArtifactID, &item.Version, &item.Title, &item.Content, &payload,
		&item.ContentHash, &item.Generator, &item.ProtocolVersion, &item.ReasonCode,
		&truncated, &created); err != nil {
		return artifact.Revision{}, err
	}
	item.Payload = json.RawMessage(payload)
	if item.Payload == nil {
		item.Payload = json.RawMessage("{}")
	}
	item.Truncated = truncated == 1
	parsed, err := parseTime(created)
	if err != nil {
		return artifact.Revision{}, err
	}
	item.CreatedAt = parsed
	return item, nil
}

func listArtifactRunRefs(ctx context.Context, q artifactQueryer, id string, version int) ([]artifact.RunRef, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT experience_id FROM artifact_run_dependencies
		 WHERE artifact_id = ? AND artifact_version = ?
		 ORDER BY experience_id ASC`, id, version)
	if err != nil {
		return nil, fmt.Errorf("读取派生运行来源 %s@%d: %w", id, version, err)
	}
	defer rows.Close()
	refs := []artifact.RunRef{}
	for rows.Next() {
		var ref artifact.RunRef
		if err := rows.Scan(&ref.ExperienceID); err != nil {
			return nil, fmt.Errorf("读取派生运行来源 %s@%d: %w", id, version, err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取派生运行来源 %s@%d: %w", id, version, err)
	}
	return refs, nil
}

func listArtifactMemoryRefs(ctx context.Context, q artifactQueryer, id string, version int) ([]artifact.MemoryRef, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT memory_id, memory_version FROM artifact_memory_dependencies
		 WHERE artifact_id = ? AND artifact_version = ?
		 ORDER BY memory_id ASC`, id, version)
	if err != nil {
		return nil, fmt.Errorf("读取派生来源 %s@%d: %w", id, version, err)
	}
	defer rows.Close()
	refs := []artifact.MemoryRef{}
	for rows.Next() {
		var ref artifact.MemoryRef
		if err := rows.Scan(&ref.MemoryID, &ref.Version); err != nil {
			return nil, fmt.Errorf("读取派生来源 %s@%d: %w", id, version, err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取派生来源 %s@%d: %w", id, version, err)
	}
	return refs, nil
}

// CreateArtifact inserts a new derivation target. CurrentVersion zero means no
// usable version has been generated yet.
func (t *Tx) CreateArtifact(ctx context.Context, item artifact.Artifact) error {
	if !item.Kind.Known() || item.TargetKey == "" || !item.State.Known() {
		return fmt.Errorf("%w: invalid artifact metadata", artifact.ErrInvalidInput)
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO artifacts(`+artifactColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, string(item.Kind), item.TargetKey, item.ScopeKey, item.Topic,
		item.CurrentVersion, string(item.State), formatTime(item.UpdatedAt)); err != nil {
		return fmt.Errorf("写入派生对象 %s: %w", item.ID, err)
	}
	return nil
}

// UpdateArtifact replaces the mutable current pointer and state.
func (t *Tx) UpdateArtifact(ctx context.Context, item artifact.Artifact) error {
	if !item.State.Known() {
		return fmt.Errorf("%w: state %q", artifact.ErrInvalidInput, item.State)
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE artifacts SET
		scope_key = ?, topic = ?, current_version = ?, state = ?, updated_at = ?
		WHERE id = ?`,
		item.ScopeKey, item.Topic, item.CurrentVersion, string(item.State),
		formatTime(item.UpdatedAt), item.ID)
	if err != nil {
		return fmt.Errorf("更新派生对象 %s: %w", item.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新派生对象 %s: %w", item.ID, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", artifact.ErrNotFound, item.ID)
	}
	return nil
}

// InsertArtifactRevision stores one immutable generated snapshot.
func (t *Tx) InsertArtifactRevision(ctx context.Context, revision artifact.Revision) error {
	payload := revision.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	truncated := 0
	if revision.Truncated {
		truncated = 1
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO artifact_revisions(`+artifactRevisionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revision.ArtifactID, revision.Version, revision.Title, revision.Content,
		string(payload), revision.ContentHash, revision.Generator, revision.ProtocolVersion,
		revision.ReasonCode, truncated, formatTime(revision.CreatedAt)); err != nil {
		return fmt.Errorf("写入派生版本 %s@%d: %w", revision.ArtifactID, revision.Version, err)
	}
	return nil
}

// LinkArtifactMemoryDependencies records the exact atomic snapshots used by one
// derived version.
func (t *Tx) LinkArtifactMemoryDependencies(ctx context.Context, artifactID string, version int, refs []artifact.MemoryRef) error {
	for _, ref := range refs {
		if ref.MemoryID == "" || ref.Version <= 0 {
			continue
		}
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO artifact_memory_dependencies
			(artifact_id, artifact_version, memory_id, memory_version) VALUES (?, ?, ?, ?)`,
			artifactID, version, ref.MemoryID, ref.Version); err != nil {
			return fmt.Errorf("关联派生来源 %s@%d/%s@%d: %w",
				artifactID, version, ref.MemoryID, ref.Version, err)
		}
	}
	return nil
}

// LinkArtifactRunDependencies records the exact application results used by one
// derived version. Revocation and purge enumerate this reverse index before any
// cascade deletes the edge.
func (t *Tx) LinkArtifactRunDependencies(ctx context.Context, artifactID string, version int, refs []artifact.RunRef) error {
	for _, ref := range refs {
		if ref.ExperienceID == "" {
			continue
		}
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO artifact_run_dependencies
			(artifact_id, artifact_version, experience_id) VALUES (?, ?, ?)`,
			artifactID, version, ref.ExperienceID); err != nil {
			return fmt.Errorf("关联派生运行来源 %s@%d/%s: %w",
				artifactID, version, ref.ExperienceID, err)
		}
	}
	return nil
}

// UpsertArtifactSearchDocument replaces the current derived FTS document.
func (t *Tx) UpsertArtifactSearchDocument(ctx context.Context, document artifact.SearchDocument) error {
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO artifact_search_docs
		(artifact_id, artifact_version, title_terms, content_terms) VALUES (?, ?, ?, ?)
		ON CONFLICT(artifact_id) DO UPDATE SET
			artifact_version = excluded.artifact_version,
			title_terms = excluded.title_terms,
			content_terms = excluded.content_terms`,
		document.ArtifactID, document.Version, document.TitleTerms, document.ContentTerms); err != nil {
		return fmt.Errorf("写入派生搜索文档 %s: %w", document.ArtifactID, err)
	}
	return nil
}

// DeleteArtifactSearchDocument removes the derived FTS row. The artifact and
// its history stay available through explicit management reads.
func (t *Tx) DeleteArtifactSearchDocument(ctx context.Context, artifactID string) error {
	if _, err := t.tx.ExecContext(ctx,
		`DELETE FROM artifact_search_docs WHERE artifact_id = ?`, artifactID); err != nil {
		return fmt.Errorf("删除派生搜索文档 %s: %w", artifactID, err)
	}
	return nil
}

// ListArtifactsForMemory returns derived objects whose current version directly
// depends on the memory. Historical-only dependencies still matter for purge,
// but they no longer make an unrelated current summary stale.
func (t *Tx) ListArtifactsForMemory(ctx context.Context, memoryID string) ([]artifact.Artifact, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT a.id, a.kind, a.target_key,
		a.scope_key, a.topic, a.current_version, a.state, a.updated_at
		FROM artifacts a
		JOIN artifact_memory_dependencies d
			ON d.artifact_id = a.id AND d.artifact_version = a.current_version
		WHERE d.memory_id = ? ORDER BY a.id ASC`, memoryID)
	if err != nil {
		return nil, fmt.Errorf("读取记忆 %s 的派生依赖: %w", memoryID, err)
	}
	defer rows.Close()
	items := []artifact.Artifact{}
	for rows.Next() {
		item, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("读取记忆 %s 的派生依赖: %w", memoryID, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取记忆 %s 的派生依赖: %w", memoryID, err)
	}
	return items, nil
}

// ListArtifactsForRun returns derived objects whose current version directly
// depends on one application result.
func (t *Tx) ListArtifactsForRun(ctx context.Context, experienceID string) ([]artifact.Artifact, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT a.id, a.kind, a.target_key,
		a.scope_key, a.topic, a.current_version, a.state, a.updated_at
		FROM artifacts a
		JOIN artifact_run_dependencies d
			ON d.artifact_id = a.id AND d.artifact_version = a.current_version
		WHERE d.experience_id = ? ORDER BY a.id ASC`, experienceID)
	if err != nil {
		return nil, fmt.Errorf("读取运行 %s 的派生依赖: %w", experienceID, err)
	}
	defer rows.Close()
	items := []artifact.Artifact{}
	for rows.Next() {
		item, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("读取运行 %s 的派生依赖: %w", experienceID, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取运行 %s 的派生依赖: %w", experienceID, err)
	}
	return items, nil
}

// ListArtifactsByScope returns current objects of one kind in a scope. It is used
// to invalidate the skills that can consume a changed experience case.
func (t *Tx) ListArtifactsByScope(ctx context.Context, kind artifact.Kind, scopeKey string) ([]artifact.Artifact, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT `+artifactColumns+` FROM artifacts
		WHERE kind = ? AND scope_key = ? ORDER BY id ASC`, string(kind), scopeKey)
	if err != nil {
		return nil, fmt.Errorf("读取作用域 %s 的派生对象: %w", scopeKey, err)
	}
	defer rows.Close()
	items := []artifact.Artifact{}
	for rows.Next() {
		item, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("读取作用域 %s 的派生对象: %w", scopeKey, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取作用域 %s 的派生对象: %w", scopeKey, err)
	}
	return items, nil
}

// ListAllArtifactsForRun returns every derived object whose current or
// historical revision directly depends on one application result. Permanent
// deletion must enumerate the whole set before any cascade removes the edges.
func (t *Tx) ListAllArtifactsForRun(ctx context.Context, experienceID string) ([]artifact.Artifact, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT a.id, a.kind, a.target_key,
		a.scope_key, a.topic, a.current_version, a.state, a.updated_at
		FROM artifacts a
		JOIN artifact_run_dependencies d ON d.artifact_id = a.id
		WHERE d.experience_id = ? ORDER BY a.id ASC`, experienceID)
	if err != nil {
		return nil, fmt.Errorf("读取运行 %s 的全部派生依赖: %w", experienceID, err)
	}
	defer rows.Close()
	items := []artifact.Artifact{}
	for rows.Next() {
		item, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("读取运行 %s 的全部派生依赖: %w", experienceID, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取运行 %s 的全部派生依赖: %w", experienceID, err)
	}
	return items, nil
}

// DeleteArtifactsForRun removes every object with any revision that directly
// depended on the result. The affected set is selected before deletes so a
// foreign-key cascade cannot hide a dependent object.
func (t *Tx) DeleteArtifactsForRun(ctx context.Context, experienceID string) error {
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT artifact_id
		FROM artifact_run_dependencies WHERE experience_id = ?`, experienceID)
	if err != nil {
		return fmt.Errorf("读取待清除派生对象 %s: %w", experienceID, err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("读取待清除派生对象 %s: %w", experienceID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("读取待清除派生对象 %s: %w", experienceID, err)
	}
	_ = rows.Close()
	return t.deleteArtifactIDs(ctx, ids)
}

// DeleteArtifactsForMemory conservatively removes every object that directly
// depends on the memory. The affected set is selected before any delete so the
// caller never relies on a foreign-key cascade to discover it.
func (t *Tx) DeleteArtifactsForMemory(ctx context.Context, memoryID string) error {
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT artifact_id
		FROM artifact_memory_dependencies WHERE memory_id = ?`, memoryID)
	if err != nil {
		return fmt.Errorf("读取待清除派生对象 %s: %w", memoryID, err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("读取待清除派生对象 %s: %w", memoryID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("读取待清除派生对象 %s: %w", memoryID, err)
	}
	_ = rows.Close()
	return t.deleteArtifactIDs(ctx, ids)
}

func (t *Tx) deleteArtifactIDs(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if _, err := t.tx.ExecContext(ctx,
			`DELETE FROM artifact_search_docs WHERE artifact_id = ?`, id); err != nil {
			return fmt.Errorf("清除派生搜索文档 %s: %w", id, err)
		}
	}
	for _, id := range ids {
		if _, err := t.tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ?`, id); err != nil {
			return fmt.Errorf("清除派生对象 %s: %w", id, err)
		}
	}
	return nil
}

// SearchArtifacts runs the derived FTS query and loads only active current
// versions. It is used by the unified recall assembler.
func (s *Store) SearchArtifacts(ctx context.Context, query artifact.SearchQuery) ([]artifact.SearchHit, error) {
	if strings.TrimSpace(query.Expression) == "" || len(query.Kinds) == 0 {
		return []artifact.SearchHit{}, nil
	}
	kinds := make([]artifact.Kind, 0, len(query.Kinds))
	for _, kind := range query.Kinds {
		if kind.Known() {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return []artifact.SearchHit{}, nil
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	asOf := query.AsOf.UTC()
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	args := []any{4.0, 1.0, query.Expression}
	conditions := []string{"artifact_fts MATCH ?"}
	conditions = append(conditions, "a.state = 'active'")
	conditions = append(conditions, "a.current_version = d.artifact_version")
	// A derived version remains traceable, but it leaves default recall as soon
	// as any of its known atomic dependencies is outside its validity interval.
	conditions = append(conditions, `NOT EXISTS (
		SELECT 1 FROM artifact_memory_dependencies dep
		JOIN memories m ON m.id = dep.memory_id
		WHERE dep.artifact_id = a.id AND dep.artifact_version = a.current_version
			AND (m.state <> 'active' OR m.assertion_status <> 'accepted'
				OR (m.valid_from IS NOT NULL AND m.valid_from > ?)
				OR (m.valid_to IS NOT NULL AND m.valid_to <= ?)
				OR EXISTS (
					SELECT 1 FROM memory_conflict_members cm
					JOIN memory_conflicts c ON c.id = cm.conflict_id
					WHERE cm.memory_id = m.id AND c.status = 'open'
				))
	)`)
	conditions = append(conditions, `NOT EXISTS (
		SELECT 1 FROM artifact_run_dependencies rd
		JOIN experience_runs r ON r.id = rd.experience_id
		WHERE rd.artifact_id = a.id AND rd.artifact_version = a.current_version
			AND r.state <> 'recorded'
	)`)
	args = append(args, formatTime(asOf), formatTime(asOf))
	if query.ScopeKey != "" {
		conditions = append(conditions, "(a.scope_key = ? OR a.scope_key = 'user')")
		args = append(args, query.ScopeKey)
	}
	conditions = append(conditions, "a.kind IN ("+placeholders(len(kinds))+")")
	for _, kind := range kinds {
		args = append(args, string(kind))
	}
	args = append(args, limit)
	statement := `SELECT a.id, bm25(artifact_fts, ?, ?) AS rank
		FROM artifact_fts
		JOIN artifact_search_docs d ON d.row_id = artifact_fts.rowid
		JOIN artifacts a ON a.id = d.artifact_id
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY rank ASC, a.updated_at DESC, a.id ASC LIMIT ?`
	rows, err := s.reader.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("派生关键词检索: %w", err)
	}
	defer rows.Close()
	type ranked struct {
		id    string
		score float64
	}
	hits := []ranked{}
	for rows.Next() {
		var item ranked
		var rank float64
		if err := rows.Scan(&item.id, &rank); err != nil {
			return nil, fmt.Errorf("读取派生检索结果: %w", err)
		}
		item.score = -rank
		hits = append(hits, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取派生检索结果: %w", err)
	}
	result := make([]artifact.SearchHit, 0, len(hits))
	for _, hit := range hits {
		item, err := getArtifact(ctx, s.reader, hit.id)
		if errors.Is(err, artifact.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if item.State != artifact.StateActive || item.CurrentVersion <= 0 {
			continue
		}
		revision, err := getArtifactRevision(ctx, s.reader, item.ID, item.CurrentVersion)
		if errors.Is(err, artifact.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, artifact.SearchHit{Artifact: item, Revision: revision, Score: hit.score})
	}
	return result, nil
}

// ListStaleTargets returns stale artifacts in stable target order.
func (s *Store) ListStaleTargets(ctx context.Context, after string, limit int) ([]artifact.Target, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT kind, target_key
		FROM artifacts WHERE state = 'stale' AND target_key > ?
		ORDER BY target_key ASC LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("扫描失效派生对象: %w", err)
	}
	defer rows.Close()
	result := []artifact.Target{}
	for rows.Next() {
		var kind, targetKey string
		if err := rows.Scan(&kind, &targetKey); err != nil {
			return nil, fmt.Errorf("扫描失效派生对象: %w", err)
		}
		target, err := artifact.ParseTarget(artifact.Kind(kind), targetKey)
		if err != nil {
			continue
		}
		result = append(result, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("扫描失效派生对象: %w", err)
	}
	return result, nil
}

// ListExpiredTargets returns active artifacts with at least one current
// dependency that is archived, rejected or outside its known validity interval.
func (s *Store) ListExpiredTargets(ctx context.Context, asOf time.Time, after string, limit int) ([]artifact.Target, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT a.kind, a.target_key
		FROM artifacts a
		WHERE a.state = 'active' AND a.target_key > ?
			AND (EXISTS (
				SELECT 1 FROM artifact_memory_dependencies dep
				JOIN memories m ON m.id = dep.memory_id
				WHERE dep.artifact_id = a.id AND dep.artifact_version = a.current_version
					AND (m.state <> 'active' OR m.assertion_status <> 'accepted'
						OR (m.valid_from IS NOT NULL AND m.valid_from > ?)
						OR (m.valid_to IS NOT NULL AND m.valid_to <= ?)
						OR EXISTS (
							SELECT 1 FROM memory_conflict_members cm
							JOIN memory_conflicts c ON c.id = cm.conflict_id
							WHERE cm.memory_id = m.id AND c.status = 'open'
						))
			) OR EXISTS (
				SELECT 1 FROM artifact_run_dependencies rd
				JOIN experience_runs r ON r.id = rd.experience_id
				WHERE rd.artifact_id = a.id AND rd.artifact_version = a.current_version
					AND r.state <> 'recorded'
			))
		ORDER BY a.target_key ASC LIMIT ?`, after, formatTime(asOf), formatTime(asOf), limit)
	if err != nil {
		return nil, fmt.Errorf("扫描过期派生对象: %w", err)
	}
	defer rows.Close()
	result := []artifact.Target{}
	for rows.Next() {
		var kind, targetKey string
		if err := rows.Scan(&kind, &targetKey); err != nil {
			return nil, fmt.Errorf("扫描过期派生对象: %w", err)
		}
		target, err := artifact.ParseTarget(artifact.Kind(kind), targetKey)
		if err != nil {
			continue
		}
		result = append(result, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("扫描过期派生对象: %w", err)
	}
	return result, nil
}

// ListAggregationGroups returns distinct non-empty scopes and topics from
// active accepted atomic memories, using a stable unified keyset cursor.
func (s *Store) ListAggregationGroups(ctx context.Context, after string, limit int) ([]artifact.GroupTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT cursor, kind, scope_key, topic FROM (
		SELECT 't:' || m.scope_key || char(0) || j.value AS cursor,
			'topic_summary' AS kind, m.scope_key AS scope_key, j.value AS topic
		FROM memories m, json_each(m.topics_json) j
		WHERE m.scope_key <> '' AND m.state = 'active' AND m.assertion_status = 'accepted'
			AND j.value <> ''
		GROUP BY m.scope_key, j.value
		UNION ALL
		SELECT 'p:' || m.scope_key AS cursor, 'profile' AS kind,
			m.scope_key AS scope_key, '' AS topic
		FROM memories m
		WHERE m.scope_key <> '' AND m.state = 'active' AND m.assertion_status = 'accepted'
		GROUP BY m.scope_key
	) WHERE cursor > ? ORDER BY cursor ASC LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("扫描待聚合分组: %w", err)
	}
	defer rows.Close()
	result := []artifact.GroupTarget{}
	for rows.Next() {
		var cursor, kind, scopeKey, topic string
		if err := rows.Scan(&cursor, &kind, &scopeKey, &topic); err != nil {
			return nil, fmt.Errorf("扫描待聚合分组: %w", err)
		}
		result = append(result, artifact.GroupTarget{
			Cursor: cursor,
			Target: artifact.Target{Kind: artifact.Kind(kind), ScopeKey: scopeKey, Topic: topic},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("扫描待聚合分组: %w", err)
	}
	return result, nil
}

// ListSkillGroups returns distinct scope/topic groups with at least one active,
// accepted procedure. Procedures without topic labels produce one scope-level
// skill target instead of an unscoped global skill.
func (s *Store) ListSkillGroups(ctx context.Context, after string, limit int) ([]artifact.GroupTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT cursor, scope_key, topic FROM (
		SELECT 's:' || m.scope_key || char(0) || j.value AS cursor,
			m.scope_key AS scope_key, j.value AS topic
		FROM memories m, json_each(m.topics_json) j
		WHERE m.scope_key <> '' AND m.state = 'active' AND m.assertion_status = 'accepted'
			AND m.kind = 'procedure' AND j.value <> ''
		GROUP BY m.scope_key, j.value
		UNION ALL
		SELECT 's:' || m.scope_key || char(0) AS cursor,
			m.scope_key AS scope_key, '' AS topic
		FROM memories m
		WHERE m.scope_key <> '' AND m.state = 'active' AND m.assertion_status = 'accepted'
			AND m.kind = 'procedure' AND json_array_length(m.topics_json) = 0
		GROUP BY m.scope_key
	) WHERE cursor > ? ORDER BY cursor ASC LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("扫描待生成 Skill 分组: %w", err)
	}
	defer rows.Close()
	result := []artifact.GroupTarget{}
	for rows.Next() {
		var cursor, scopeKey, topic string
		if err := rows.Scan(&cursor, &scopeKey, &topic); err != nil {
			return nil, fmt.Errorf("扫描待生成 Skill 分组: %w", err)
		}
		result = append(result, artifact.GroupTarget{
			Cursor: cursor,
			Target: artifact.Target{Kind: artifact.KindSkill, ScopeKey: scopeKey, Topic: topic},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("扫描待生成 Skill 分组: %w", err)
	}
	return result, nil
}

// ListAggregationInputs selects at most limit accepted, active, time-applicable
// memories in one scope/topic with no open conflict. The final boolean reports
// that additional eligible rows exist.
func (t *Tx) ListAggregationInputs(ctx context.Context, target artifact.Target, asOf time.Time, limit int) ([]artifact.MemoryInput, bool, error) {
	if limit <= 0 {
		limit = 64
	}
	conditions := []string{
		"m.state = 'active'",
		"m.assertion_status = 'accepted'",
		"m.scope_key = ?",
		"(m.valid_from IS NULL OR m.valid_from <= ?)",
		"(m.valid_to IS NULL OR ? < m.valid_to)",
		`NOT EXISTS (
			SELECT 1 FROM memory_conflict_members cm
			JOIN memory_conflicts c ON c.id = cm.conflict_id
			WHERE cm.memory_id = m.id AND c.status = 'open'
		)`,
	}
	args := []any{target.ScopeKey, formatTime(asOf), formatTime(asOf)}
	switch target.Kind {
	case artifact.KindTopicSummary:
		conditions = append(conditions, "EXISTS (SELECT 1 FROM json_each(m.topics_json) WHERE json_each.value = ?)")
		args = append(args, target.Topic)
	case artifact.KindSkill:
		conditions = append(conditions, "m.kind = 'procedure'")
		if target.Topic != "" {
			conditions = append(conditions, "EXISTS (SELECT 1 FROM json_each(m.topics_json) WHERE json_each.value = ?)")
			args = append(args, target.Topic)
		} else {
			conditions = append(conditions, "json_array_length(m.topics_json) = 0")
		}
	case artifact.KindProfile:
		conditions = append(conditions, "m.kind IN ('preference', 'procedure', 'fact')")
	case artifact.KindExperienceCase:
		conditions = append(conditions, "m.kind IN ('procedure', 'event')")
	default:
		return nil, false, fmt.Errorf("%w: kind %q", artifact.ErrInvalidInput, target.Kind)
	}
	args = append(args, limit+1)
	rows, err := t.tx.QueryContext(ctx, `SELECT m.id, m.version, m.kind, m.title, m.content,
		m.scope_key, m.topics_json, m.observed_at, m.updated_at
		FROM memories m
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY CASE m.kind WHEN 'preference' THEN 0 WHEN 'procedure' THEN 1
			WHEN 'fact' THEN 2 ELSE 3 END, m.updated_at DESC, m.id ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("选择聚合输入: %w", err)
	}
	defer rows.Close()
	items := []artifact.MemoryInput{}
	for rows.Next() {
		var item artifact.MemoryInput
		var topicsJSON, observedAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Version, &item.Kind, &item.Title, &item.Content,
			&item.ScopeKey, &topicsJSON, &observedAt, &updatedAt); err != nil {
			return nil, false, fmt.Errorf("选择聚合输入: %w", err)
		}
		if item.Topics, err = decodeTopics(topicsJSON); err != nil {
			return nil, false, fmt.Errorf("解析聚合输入主题: %w", err)
		}
		if item.ObservedAt, err = parseTime(observedAt); err != nil {
			return nil, false, err
		}
		if item.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("选择聚合输入: %w", err)
	}
	if len(items) > limit {
		return items[:limit], true, nil
	}
	return items, false, nil
}
