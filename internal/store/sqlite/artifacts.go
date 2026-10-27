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
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/topic"
)

const artifactColumns = `id, kind, target_key, scope_key, topic, current_version, state, reason_code, updated_at`

const artifactRevisionColumns = `artifact_id, version, title, content, payload_json,
	content_hash, generator, protocol_version, taxonomy_version, generator_version, run_id, provenance_json, reason_code, truncated, created_at`

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
		&item.CurrentVersion, &state, &item.ReasonCode, &updated); err != nil {
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
	var payload, provenance, created string
	var truncated int
	if err := row.Scan(&item.ArtifactID, &item.Version, &item.Title, &item.Content, &payload,
		&item.ContentHash, &item.Generator, &item.ProtocolVersion, &item.TaxonomyVersion, &item.GeneratorVersion, &item.RunID, &provenance, &item.ReasonCode,
		&truncated, &created); err != nil {
		return artifact.Revision{}, err
	}
	item.Payload = json.RawMessage(payload)
	item.Provenance = json.RawMessage(provenance)
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, string(item.Kind), item.TargetKey, item.ScopeKey, item.Topic,
		item.CurrentVersion, string(item.State), item.ReasonCode, formatTime(item.UpdatedAt)); err != nil {
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
		scope_key = ?, topic = ?, current_version = ?, state = ?, reason_code = ?, updated_at = ?
		WHERE id = ?`,
		item.ScopeKey, item.Topic, item.CurrentVersion, string(item.State),
		item.ReasonCode, formatTime(item.UpdatedAt), item.ID)
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revision.ArtifactID, revision.Version, revision.Title, revision.Content,
		string(payload), revision.ContentHash, revision.Generator, revision.ProtocolVersion,
		revision.TaxonomyVersion, revision.GeneratorVersion, revision.RunID, string(revision.Provenance),
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
	return t.listArtifactsForMemory(ctx, memoryID, true)
}

// ListAllArtifactsForMemory includes historical-only dependencies so purge can
// invalidate every affected task before removing the artifact and its history.
func (t *Tx) ListAllArtifactsForMemory(ctx context.Context, memoryID string) ([]artifact.Artifact, error) {
	return t.listArtifactsForMemory(ctx, memoryID, false)
}

func (t *Tx) listArtifactsForMemory(ctx context.Context, memoryID string, currentOnly bool) ([]artifact.Artifact, error) {
	versionFilter := ""
	if currentOnly {
		versionFilter = " AND d.artifact_version = a.current_version"
	}
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT a.id, a.kind, a.target_key,
		a.scope_key, a.topic, a.current_version, a.state, a.reason_code, a.updated_at
		FROM artifacts a
		JOIN artifact_memory_dependencies d
			ON d.artifact_id = a.id`+versionFilter+`
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
		a.scope_key, a.topic, a.current_version, a.state, a.reason_code, a.updated_at
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
		a.scope_key, a.topic, a.current_version, a.state, a.reason_code, a.updated_at
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

// ListArtifacts returns one bounded keyset page of current artifacts. Cursor is
// the stable target key, never an opaque body-derived value.
func (s *Store) ListArtifacts(ctx context.Context, query artifact.ListQuery) (artifact.ListPage, error) {
	if query.Limit <= 0 {
		query.Limit = 50
	}
	if query.Limit > 200 {
		query.Limit = 200
	}
	conditions := []string{"target_key > ?"}
	args := []any{query.Cursor}
	if query.ScopeKey != "" {
		conditions = append(conditions, "scope_key = ?")
		args = append(args, query.ScopeKey)
	}
	if query.Kind != "" {
		conditions = append(conditions, "kind = ?")
		args = append(args, string(query.Kind))
	}
	if query.State != "" {
		conditions = append(conditions, "state = ?")
		args = append(args, string(query.State))
	}
	if query.ReasonCode != "" {
		conditions = append(conditions, "reason_code = ?")
		args = append(args, query.ReasonCode)
	}
	args = append(args, query.Limit+1)
	rows, err := s.reader.QueryContext(ctx, `SELECT `+artifactColumns+` FROM artifacts
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY target_key ASC LIMIT ?`, args...)
	if err != nil {
		return artifact.ListPage{}, fmt.Errorf("列出派生对象: %w", err)
	}
	defer rows.Close()
	items := []artifact.Artifact{}
	for rows.Next() {
		item, err := scanArtifact(rows)
		if err != nil {
			return artifact.ListPage{}, fmt.Errorf("列出派生对象: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return artifact.ListPage{}, fmt.Errorf("列出派生对象: %w", err)
	}
	page := artifact.ListPage{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = artifact.TargetCursor(last.TargetKey)
	}
	return page, nil
}

// ListTargets returns artifacts and evaluation-only targets in one stable
// keyset order. It distinguishes targets that never had eligible input from
// content objects that failed or were disabled.
func (s *Store) ListTargets(ctx context.Context, query artifact.TargetQuery) (artifact.TargetPage, error) {
	if query.Limit <= 0 {
		query.Limit = 50
	}
	if query.Limit > 200 {
		query.Limit = 200
	}
	page := artifact.TargetPage{Items: []artifact.TargetRecord{}}
	cursor := query.Cursor
	// Status is derived from artifact, evaluation and job state, so filter in
	// Go while keeping each storage scan bounded and keyset-stable.
	for rounds := 0; rounds < 8 && len(page.Items) < query.Limit; rounds++ {
		remaining := query.Limit - len(page.Items)
		records, next, exhausted, err := s.scanTargets(ctx, cursor, remaining*3+1)
		if err != nil {
			return artifact.TargetPage{}, err
		}
		for _, record := range records {
			if !matchesTargetQuery(record, query) {
				continue
			}
			page.Items = append(page.Items, record)
			if len(page.Items) >= query.Limit {
				break
			}
		}
		cursor = next
		if exhausted || len(page.Items) >= query.Limit {
			break
		}
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
	}
	if len(page.Items) == query.Limit {
		last := page.Items[len(page.Items)-1]
		probe := artifact.TargetCursor(last.TargetKey)
		for rounds := 0; rounds < 8; rounds++ {
			records, next, exhausted, err := s.scanTargets(ctx, probe, 50)
			if err != nil {
				return artifact.TargetPage{}, err
			}
			found := false
			for _, record := range records {
				if matchesTargetQuery(record, query) {
					found = true
					break
				}
			}
			if found {
				page.NextCursor = probe
				break
			}
			if exhausted {
				break
			}
			probe = next
		}
	}
	return page, nil
}

func matchesTargetQuery(record artifact.TargetRecord, query artifact.TargetQuery) bool {
	if query.ScopeKey != "" && record.ScopeKey != query.ScopeKey {
		return false
	}
	if query.Kind != "" && record.Kind != query.Kind {
		return false
	}
	if query.ReasonCode != "" && record.ReasonCode != query.ReasonCode {
		return false
	}
	if query.Status != "" && record.Status != query.Status {
		return false
	}
	return true
}

func (s *Store) scanTargets(ctx context.Context, cursor string, limit int) ([]artifact.TargetRecord, string, bool, error) {
	if limit <= 0 {
		limit = 1
	}
	conditions := []string{"cursor > ?"}
	args := []any{cursor}
	args = append(args, limit)
	statement := `SELECT kind, target_key, scope_key, topic, artifact_id, current_version,
		artifact_state, artifact_reason, artifact_updated, evaluation_status,
		evaluation_reason, evaluated_at, job_status, job_wait_reason, job_error_code,
		job_updated_at, cursor FROM (
		SELECT a.kind AS kind, a.target_key AS target_key, a.scope_key AS scope_key,
			a.topic AS topic, a.id AS artifact_id, a.current_version AS current_version,
			a.state AS artifact_state, a.reason_code AS artifact_reason,
			a.updated_at AS artifact_updated,
			'' AS evaluation_status, '' AS evaluation_reason, '' AS evaluated_at,
			COALESCE(j.status, '') AS job_status, COALESCE(j.wait_reason, '') AS job_wait_reason,
			COALESCE(j.last_error_code, '') AS job_error_code, COALESCE(j.updated_at, '') AS job_updated_at,
			a.target_key AS cursor
		FROM artifacts a
		LEFT JOIN governance_jobs j ON j.target_key = a.target_key
			AND j.kind = CASE WHEN a.kind = 'skill' THEN 'build_skill' ELSE 'aggregate' END
		UNION ALL
		SELECT e.kind, e.target_key, e.scope_key, e.topic, '', 0, '', '',
			'', e.status, e.reason_code, e.evaluated_at,
			COALESCE(j.status, ''), COALESCE(j.wait_reason, ''),
			COALESCE(j.last_error_code, ''), COALESCE(j.updated_at, ''),
			e.target_key
		FROM derived_target_evaluations e
		LEFT JOIN governance_jobs j ON j.target_key = e.target_key
			AND j.kind = CASE WHEN e.kind = 'skill' THEN 'build_skill' ELSE 'aggregate' END
		WHERE NOT EXISTS (
			SELECT 1 FROM artifacts a WHERE a.kind = e.kind AND a.target_key = e.target_key
		)
		UNION ALL
		SELECT '', j.target_key, '', '', '', 0, '', '', '', '', '', '',
			j.status, j.wait_reason, j.last_error_code, j.updated_at,
			j.target_key
		FROM governance_jobs j
		WHERE j.kind IN ('aggregate', 'build_skill')
			AND NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.target_key = j.target_key)
			AND NOT EXISTS (SELECT 1 FROM derived_target_evaluations e WHERE e.target_key = j.target_key)
	) WHERE ` + strings.Join(conditions, " AND ") + ` ORDER BY cursor ASC LIMIT ?`
	rows, err := s.reader.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, cursor, true, fmt.Errorf("列出派生目标: %w", err)
	}
	defer rows.Close()
	records := []artifact.TargetRecord{}
	next := cursor
	for rows.Next() {
		var kind, targetKey, scopeKey, topic, artifactID, artifactState, artifactReason, artifactUpdated string
		var currentVersion int
		var evaluationStatus, evaluationReason, evaluatedAt string
		var jobStatus, jobWaitReason, jobErrorCode, jobUpdatedAt, rowCursor string
		if err := rows.Scan(&kind, &targetKey, &scopeKey, &topic, &artifactID, &currentVersion,
			&artifactState, &artifactReason, &artifactUpdated, &evaluationStatus,
			&evaluationReason, &evaluatedAt, &jobStatus, &jobWaitReason, &jobErrorCode,
			&jobUpdatedAt, &rowCursor); err != nil {
			return nil, cursor, true, fmt.Errorf("列出派生目标: %w", err)
		}
		record := artifact.TargetRecord{
			Kind: artifact.Kind(kind), TargetKey: targetKey, ScopeKey: scopeKey, Topic: topic,
		}
		if record.Kind == "" {
			parsed, ok := parseTargetRecordKey(targetKey)
			if !ok {
				continue
			}
			record.Kind, record.ScopeKey, record.Topic = parsed.Kind, parsed.ScopeKey, parsed.Topic
		}
		if artifactID != "" {
			item := artifact.Artifact{
				ID: artifactID, Kind: artifact.Kind(kind), TargetKey: targetKey,
				ScopeKey: scopeKey, Topic: topic, CurrentVersion: currentVersion,
				State: artifact.State(artifactState), ReasonCode: artifactReason,
			}
			if artifactUpdated != "" {
				if parsed, err := parseTime(artifactUpdated); err == nil {
					item.UpdatedAt = parsed
					record.UpdatedAt = parsed
				}
			}
			record.Artifact = &item
		}
		if evaluatedAt != "" {
			if parsed, err := parseTime(evaluatedAt); err == nil {
				record.EvaluatedAt = &parsed
				if parsed.After(record.UpdatedAt) {
					record.UpdatedAt = parsed
				}
			}
		}
		if jobStatus != "" {
			job := &task.Job{Kind: skillJobKind(artifact.Kind(kind)), TargetKey: targetKey,
				Status: task.Status(jobStatus), WaitReason: jobWaitReason, LastErrorCode: jobErrorCode}
			if jobUpdatedAt != "" {
				if parsed, err := parseTime(jobUpdatedAt); err == nil {
					job.UpdatedAt = parsed
					if parsed.After(record.UpdatedAt) {
						record.UpdatedAt = parsed
					}
				}
			}
			record.Job = job
		}
		record.Status, record.ReasonCode = targetStatusFromRecord(record, evaluationStatus, evaluationReason)
		records = append(records, record)
		next = rowCursor
	}
	if err := rows.Err(); err != nil {
		return nil, cursor, true, fmt.Errorf("列出派生目标: %w", err)
	}
	return records, next, len(records) < limit, nil
}

func skillJobKind(kind artifact.Kind) task.Kind {
	if kind == artifact.KindSkill {
		return task.KindBuildSkill
	}
	return task.KindAggregate
}

func parseTargetRecordKey(targetKey string) (artifact.Target, bool) {
	for _, kind := range []artifact.Kind{
		artifact.KindSkill, artifact.KindScopeSummary, artifact.KindProfile,
		artifact.KindExperienceCase, artifact.KindTopicSummary,
	} {
		if target, err := artifact.ParseTarget(kind, targetKey); err == nil {
			return target, true
		}
	}
	return artifact.Target{}, false
}

func targetStatusFromRecord(record artifact.TargetRecord, evaluationStatus, evaluationReason string) (artifact.TargetStatus, string) {
	reason := record.ReasonCode
	if record.Artifact != nil && record.Artifact.ReasonCode != "" {
		reason = record.Artifact.ReasonCode
	}
	jobStatus := ""
	if record.Job != nil {
		jobStatus = string(record.Job.Status)
	}
	switch jobStatus {
	case string(task.StatusQueued):
		return artifact.TargetStatusQueued, reason
	case string(task.StatusRunning):
		return artifact.TargetStatusRunning, reason
	case string(task.StatusWaiting):
		return artifact.TargetStatusWaiting, reason
	}
	if record.Artifact != nil {
		switch record.Artifact.State {
		case artifact.StateActive:
			return artifact.TargetStatusActive, reason
		case artifact.StateStale:
			return artifact.TargetStatusStale, reason
		default:
			if reason != "" {
				return reasonStatus(reason), reason
			}
			return artifact.TargetStatusDisabled, reason
		}
	}
	if reason != "" {
		return reasonStatus(reason), reason
	}
	if evaluationStatus != "" {
		status := artifact.TargetStatus(evaluationStatus)
		if !status.Known() {
			status = artifact.TargetStatusNoEligibleInput
		}
		if evaluationReason == "" {
			evaluationReason = string(status)
		}
		return status, evaluationReason
	}
	if record.Job != nil {
		switch record.Job.Status {
		case task.StatusFailed:
			return artifact.TargetStatusGenerationFailed, record.Job.LastErrorCode
		case task.StatusCancelled:
			return artifact.TargetStatusDisabled, record.Job.LastErrorCode
		}
	}
	if record.Artifact != nil {
		return artifact.TargetStatusDisabled, reason
	}
	return artifact.TargetStatusDisabled, ""
}

func reasonStatus(reason string) artifact.TargetStatus {
	switch reason {
	case "no_procedure_input", "no_eligible_input", "topic_mismatch":
		return artifact.TargetStatusNoEligibleInput
	case "model_unavailable":
		return artifact.TargetStatusModelUnavailable
	case "invalid_model_output", "invalid_profile_content":
		return artifact.TargetStatusInvalidOutput
	default:
		return artifact.TargetStatusGenerationFailed
	}
}

// UpsertTargetEvaluation records a target evaluation that produced no content
// object. Successful commits remove the row.
func (t *Tx) UpsertTargetEvaluation(ctx context.Context, evaluation artifact.TargetEvaluation) error {
	if !evaluation.Kind.Known() || evaluation.TargetKey == "" || !evaluation.Status.Known() {
		return fmt.Errorf("%w: invalid target evaluation", artifact.ErrInvalidInput)
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO derived_target_evaluations
		(kind, target_key, scope_key, topic, status, reason_code, evaluated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, target_key) DO UPDATE SET
			scope_key = excluded.scope_key,
			topic = excluded.topic,
			status = excluded.status,
			reason_code = excluded.reason_code,
			evaluated_at = excluded.evaluated_at`,
		string(evaluation.Kind), evaluation.TargetKey, evaluation.ScopeKey, evaluation.Topic,
		string(evaluation.Status), evaluation.ReasonCode, formatTime(evaluation.EvaluatedAt)); err != nil {
		return fmt.Errorf("写入派生目标评估 %s/%s: %w", evaluation.Kind, evaluation.TargetKey, err)
	}
	return nil
}

// DeleteTargetEvaluation clears a stale evaluation after a usable revision is
// committed.
func (t *Tx) DeleteTargetEvaluation(ctx context.Context, kind artifact.Kind, targetKey string) error {
	if _, err := t.tx.ExecContext(ctx,
		`DELETE FROM derived_target_evaluations WHERE kind = ? AND target_key = ?`,
		string(kind), targetKey); err != nil {
		return fmt.Errorf("删除派生目标评估 %s/%s: %w", kind, targetKey, err)
	}
	return nil
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
			) OR EXISTS (
				SELECT 1 FROM artifact_revisions r
				WHERE r.artifact_id = a.id AND r.version = a.current_version
					AND r.taxonomy_version <> ?
			))
		ORDER BY a.target_key ASC LIMIT ?`, after, formatTime(asOf), formatTime(asOf), topic.Version, limit)
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
// active accepted atomic memories, using a stable unified keyset cursor. Every
// scope also has one overview and one profile target; profiles are filtered
// again by their durable-input gate when they run.
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
		SELECT 'o:' || m.scope_key AS cursor, 'scope_summary' AS kind,
			m.scope_key AS scope_key, '' AS topic
		FROM memories m
		WHERE m.scope_key <> '' AND m.state = 'active' AND m.assertion_status = 'accepted'
		GROUP BY m.scope_key
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
// memories for one derivation target with no open conflict. The final boolean
// reports that additional eligible rows exist.
func (t *Tx) ListAggregationInputs(ctx context.Context, target artifact.Target, asOf time.Time, limit int) ([]artifact.MemoryInput, bool, error) {
	if limit <= 0 {
		limit = 64
	}
	conditions, args, err := aggregationInputConditions(target, asOf)
	if err != nil {
		return nil, false, err
	}
	args = append(args, limit+1)
	rows, err := t.tx.QueryContext(ctx, `SELECT m.id, m.version, m.kind, m.phase, m.durability, m.title, m.content,
		m.scope_key, m.topics_json, m.procedure_json, m.qualifiers_json, m.observed_at, m.updated_at
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
		var topicsJSON, procedureJSON, qualifiersJSON, observedAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Version, &item.Kind, &item.Phase, &item.Durability,
			&item.Title, &item.Content, &item.ScopeKey, &topicsJSON, &procedureJSON,
			&qualifiersJSON, &observedAt, &updatedAt); err != nil {
			return nil, false, fmt.Errorf("选择聚合输入: %w", err)
		}
		if item.Topics, err = decodeTopics(topicsJSON); err != nil {
			return nil, false, fmt.Errorf("解析聚合输入主题: %w", err)
		}
		if strings.TrimSpace(procedureJSON) != "" && json.Valid([]byte(procedureJSON)) {
			item.Procedure = json.RawMessage(procedureJSON)
		}
		if strings.TrimSpace(qualifiersJSON) != "" && json.Valid([]byte(qualifiersJSON)) {
			item.Qualifiers = json.RawMessage(qualifiersJSON)
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

// CountAggregationInputs reports how many eligible inputs exist. It is separate
// from selection so payloads can record input-selection completeness instead of
// inferring it from the returned page.
func (t *Tx) CountAggregationInputs(ctx context.Context, target artifact.Target, asOf time.Time) (int, error) {
	conditions, args, err := aggregationInputConditions(target, asOf)
	if err != nil {
		return 0, err
	}
	var total int
	if err := t.tx.QueryRowContext(ctx, `SELECT count(*) FROM memories m WHERE `+
		strings.Join(conditions, " AND "), args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("统计聚合输入: %w", err)
	}
	return total, nil
}

func aggregationInputConditions(target artifact.Target, asOf time.Time) ([]string, []any, error) {
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
	case artifact.KindScopeSummary:
		// The scope overview deliberately reads every eligible kind in the
		// scope; it is not a profile and not a topic detail view.
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
		conditions = append(conditions, "m.durability = 'durable'", "m.phase = 'unknown'", "m.valid_to IS NULL")
	case artifact.KindExperienceCase:
		conditions = append(conditions, "m.kind IN ('procedure', 'event')")
	default:
		return nil, nil, fmt.Errorf("%w: kind %q", artifact.ErrInvalidInput, target.Kind)
	}
	return conditions, args, nil
}

// HasProcedureInputs distinguishes an upstream extraction gap from a topic
// mismatch without relaxing the eligibility gate used for generation.
func (t *Tx) HasProcedureInputs(ctx context.Context, scopeKey string, asOf time.Time) (bool, error) {
	var found bool
	err := t.tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM memories m WHERE m.scope_key = ? AND m.kind = 'procedure'
		AND m.state = 'active' AND m.assertion_status = 'accepted'
		AND (m.valid_from IS NULL OR m.valid_from <= ?)
		AND (m.valid_to IS NULL OR ? < m.valid_to)
		AND NOT EXISTS (SELECT 1 FROM memory_conflict_members cm
			JOIN memory_conflicts c ON c.id = cm.conflict_id
			WHERE cm.memory_id = m.id AND c.status = 'open'))`,
		scopeKey, formatTime(asOf), formatTime(asOf)).Scan(&found)
	return found, err
}
