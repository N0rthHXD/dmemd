package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

const artifactColumns = `id, layer, kind, target_key, scope_key, topic, current_version, state, reason_code, updated_at`

const artifactRevisionColumns = `artifact_id, version, title, content, payload_json,
	content_hash, generator, protocol_version, truncated, created_at`

var (
	_ artifact.Backend = (*Store)(nil)
	_ artifact.Tx      = (*Tx)(nil)
)

// GetArtifact reads one derived object from the read pool.
func (s *Store) GetArtifact(ctx context.Context, id string) (artifact.DerivedMemory, error) {
	return getArtifact(ctx, s.reader, id)
}

// GetArtifactByTarget reads the unique object for one kind and target key.
func (s *Store) GetArtifactByTarget(ctx context.Context, kind artifact.Kind, targetKey string) (artifact.DerivedMemory, error) {
	return getArtifactByTarget(ctx, s.reader, kind, targetKey)
}

// GetArtifactRevision reads one immutable revision.
func (s *Store) GetArtifactRevision(ctx context.Context, id string, version int) (artifact.Revision, error) {
	return getArtifactRevision(ctx, s.reader, id, version)
}

// ListArtifactRevisions reads newest-first history.
func (s *Store) ListArtifactRevisions(ctx context.Context, id string, limit int) ([]artifact.Revision, error) {
	return listArtifactRevisions(ctx, s.reader, id, limit)
}

// ListArtifactDependencies reads the exact typed inputs of one immutable
// revision.
func (s *Store) ListArtifactDependencies(ctx context.Context, id string, version int) ([]artifact.DependencyRef, error) {
	return listArtifactDependencies(ctx, s.reader, id, version)
}

func (t *Tx) GetArtifact(ctx context.Context, id string) (artifact.DerivedMemory, error) {
	return getArtifact(ctx, t.tx, id)
}

func (t *Tx) GetArtifactByTarget(ctx context.Context, kind artifact.Kind, targetKey string) (artifact.DerivedMemory, error) {
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
}, id string) (artifact.DerivedMemory, error) {
	item, err := scanArtifact(q.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.DerivedMemory{}, fmt.Errorf("%w: %s", artifact.ErrNotFound, id)
	}
	if err != nil {
		return artifact.DerivedMemory{}, fmt.Errorf("读取派生对象 %s: %w", id, err)
	}
	return item, nil
}

func getArtifactByTarget(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, kind artifact.Kind, targetKey string) (artifact.DerivedMemory, error) {
	item, err := scanArtifact(q.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE kind = ? AND target_key = ?`,
		string(kind), targetKey))
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.DerivedMemory{}, fmt.Errorf("%w: %s/%s", artifact.ErrNotFound, kind, targetKey)
	}
	if err != nil {
		return artifact.DerivedMemory{}, fmt.Errorf("读取派生对象 %s/%s: %w", kind, targetKey, err)
	}
	return item, nil
}

func scanArtifact(row rowScanner) (artifact.DerivedMemory, error) {
	var item artifact.DerivedMemory
	var layer, kind, state, updated string
	if err := row.Scan(&item.ID, &layer, &kind, &item.TargetKey, &item.ScopeKey, &item.Topic,
		&item.CurrentVersion, &state, &item.ReasonCode, &updated); err != nil {
		return artifact.DerivedMemory{}, err
	}
	item.Layer = artifact.Layer(layer)
	if !item.Layer.Known() {
		item.Layer = artifact.LayerForKind(artifact.Kind(kind))
	}
	item.Kind = artifact.Kind(kind)
	item.State = artifact.State(state)
	parsed, err := parseTime(updated)
	if err != nil {
		return artifact.DerivedMemory{}, err
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
		&item.ContentHash, &item.Generator, &item.ProtocolVersion, &truncated, &created); err != nil {
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

// CreateArtifact inserts a new derivation target. CurrentVersion zero means no
// usable version has been generated yet.
func (t *Tx) CreateArtifact(ctx context.Context, item artifact.DerivedMemory) error {
	if item.Layer == "" {
		item.Layer = artifact.LayerForKind(item.Kind)
	}
	if !item.Kind.Known() || !item.Layer.Known() || item.TargetKey == "" || !item.State.Known() {
		return fmt.Errorf("%w: invalid artifact metadata", artifact.ErrInvalidInput)
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO artifacts(`+artifactColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, string(item.Layer), string(item.Kind), item.TargetKey, item.ScopeKey, item.Topic,
		item.CurrentVersion, string(item.State), item.ReasonCode, formatTime(item.UpdatedAt)); err != nil {
		return fmt.Errorf("写入派生对象 %s: %w", item.ID, err)
	}
	return nil
}

// UpdateArtifact replaces the mutable current pointer and state.
func (t *Tx) UpdateArtifact(ctx context.Context, item artifact.DerivedMemory) error {
	if !item.State.Known() {
		return fmt.Errorf("%w: state %q", artifact.ErrInvalidInput, item.State)
	}
	layer := item.Layer
	if !layer.Known() {
		layer = artifact.LayerForKind(item.Kind)
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE artifacts SET
		layer = ?, scope_key = ?, topic = ?, current_version = ?, state = ?, reason_code = ?, updated_at = ?
		WHERE id = ?`,
		string(layer), item.ScopeKey, item.Topic, item.CurrentVersion, string(item.State),
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revision.ArtifactID, revision.Version, revision.Title, revision.Content,
		string(payload), revision.ContentHash, revision.Generator, revision.ProtocolVersion,
		truncated, formatTime(revision.CreatedAt)); err != nil {
		return fmt.Errorf("写入派生版本 %s@%d: %w", revision.ArtifactID, revision.Version, err)
	}
	return nil
}

// LinkDerivedDependencies is the standalone convenience form for callers
// outside a transaction.
func (s *Store) LinkDerivedDependencies(ctx context.Context, derivedID string, version int, refs []artifact.DependencyRef) error {
	return s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.LinkDerivedDependencies(ctx, derivedID, version, refs)
	})
}

// LinkDerivedDependencies records the exact typed inputs of one derived
// revision. Repeated refs and repeated calls are idempotent. The derived object
// identity and version are always service-generated.
func (t *Tx) LinkDerivedDependencies(ctx context.Context, derivedID string, version int, refs []artifact.DependencyRef) error {
	if strings.TrimSpace(derivedID) == "" || version <= 0 {
		return fmt.Errorf("%w: derived %q version %d", artifact.ErrInvalidInput, derivedID, version)
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		ref.ID = strings.TrimSpace(ref.ID)
		if ref.ID == "" || !ref.Type.Known() || ref.Version < 0 {
			continue
		}
		key := string(ref.Type) + "\x00" + ref.ID + "\x00" + strconv.Itoa(ref.Version)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		required := 0
		if ref.Required {
			required = 1
		}
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO derived_dependencies
			(derived_id, derived_version, input_type, input_id, input_version, required)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(derived_id, derived_version, input_type, input_id, input_version) DO NOTHING`,
			derivedID, version, string(ref.Type), ref.ID, ref.Version, required); err != nil {
			return fmt.Errorf("关联派生输入 %s@%d/%s:%s@%d: %w",
				derivedID, version, ref.Type, ref.ID, ref.Version, err)
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
func (t *Tx) ListArtifactsForMemory(ctx context.Context, memoryID string) ([]artifact.DerivedMemory, error) {
	return t.listArtifactsForMemory(ctx, memoryID, true)
}

// ListAllArtifactsForMemory includes historical-only dependencies so purge can
// invalidate every affected task before removing the artifact and its history.
func (t *Tx) ListAllArtifactsForMemory(ctx context.Context, memoryID string) ([]artifact.DerivedMemory, error) {
	return t.listArtifactsForMemory(ctx, memoryID, false)
}

func (t *Tx) listArtifactsForMemory(ctx context.Context, memoryID string, currentOnly bool) ([]artifact.DerivedMemory, error) {
	versionFilter := ""
	if currentOnly {
		versionFilter = " AND d.derived_version = a.current_version"
	}
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT a.id, a.layer, a.kind, a.target_key,
		a.scope_key, a.topic, a.current_version, a.state, a.reason_code, a.updated_at
		FROM artifacts a
		JOIN derived_dependencies d
			ON d.derived_id = a.id AND d.input_type = 'memory'`+versionFilter+`
		WHERE d.input_id = ? ORDER BY a.id ASC`, memoryID)
	if err != nil {
		return nil, fmt.Errorf("读取记忆 %s 的派生依赖: %w", memoryID, err)
	}
	defer rows.Close()
	items := []artifact.DerivedMemory{}
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
func (t *Tx) ListArtifactsForRun(ctx context.Context, experienceID string) ([]artifact.DerivedMemory, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT a.id, a.layer, a.kind, a.target_key,
		a.scope_key, a.topic, a.current_version, a.state, a.reason_code, a.updated_at
		FROM artifacts a
		JOIN derived_dependencies d
			ON d.derived_id = a.id AND d.input_type = 'experience'
			AND d.derived_version = a.current_version
		WHERE d.input_id = ? ORDER BY a.id ASC`, experienceID)
	if err != nil {
		return nil, fmt.Errorf("读取运行 %s 的派生依赖: %w", experienceID, err)
	}
	defer rows.Close()
	items := []artifact.DerivedMemory{}
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
func (t *Tx) ListArtifactsByScope(ctx context.Context, kind artifact.Kind, scopeKey string) ([]artifact.DerivedMemory, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT `+artifactColumns+` FROM artifacts
		WHERE kind = ? AND scope_key = ? ORDER BY id ASC`, string(kind), scopeKey)
	if err != nil {
		return nil, fmt.Errorf("读取作用域 %s 的派生对象: %w", scopeKey, err)
	}
	defer rows.Close()
	items := []artifact.DerivedMemory{}
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
func (t *Tx) ListAllArtifactsForRun(ctx context.Context, experienceID string) ([]artifact.DerivedMemory, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT a.id, a.layer, a.kind, a.target_key,
		a.scope_key, a.topic, a.current_version, a.state, a.reason_code, a.updated_at
		FROM artifacts a
		JOIN derived_dependencies d
			ON d.derived_id = a.id AND d.input_type = 'experience'
		WHERE d.input_id = ? ORDER BY a.id ASC`, experienceID)
	if err != nil {
		return nil, fmt.Errorf("读取运行 %s 的全部派生依赖: %w", experienceID, err)
	}
	defer rows.Close()
	items := []artifact.DerivedMemory{}
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
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT derived_id
		FROM derived_dependencies WHERE input_type = 'experience' AND input_id = ?`, experienceID)
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
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT derived_id
		FROM derived_dependencies WHERE input_type = 'memory' AND input_id = ?`, memoryID)
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
		if _, err := t.tx.ExecContext(ctx,
			`DELETE FROM derived_dependencies WHERE derived_id = ?`, id); err != nil {
			return fmt.Errorf("清除派生依赖 %s: %w", id, err)
		}
	}
	for _, id := range ids {
		if _, err := t.tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ?`, id); err != nil {
			return fmt.Errorf("清除派生对象 %s: %w", id, err)
		}
	}
	return nil
}

type artifactRankedHit struct {
	id    string
	score float64
}

// SearchArtifacts runs the derived FTS query and loads current versions whose
// state is requested. Default recall requests only active rows; stale history is
// returned only when explicitly selected. A stale artifact whose FTS document
// was removed from the default index is still discoverable by the sanitized term
// fallback, so an explicit historical query never silently loses its result.
func (s *Store) SearchArtifacts(ctx context.Context, query artifact.SearchQuery) ([]artifact.SearchHit, error) {
	if strings.TrimSpace(query.Expression) == "" {
		return []artifact.SearchHit{}, nil
	}
	layers, layersOK := artifactSearchLayers(query.Layers)
	if !layersOK {
		return []artifact.SearchHit{}, nil
	}
	kinds, kindsOK := artifactSearchKinds(query.Kinds)
	if !kindsOK {
		return []artifact.SearchHit{}, nil
	}
	states := artifactSearchStates(query.States)
	if len(states) == 0 {
		return []artifact.SearchHit{}, nil
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}

	hits := []artifactRankedHit{}
	seen := make(map[string]struct{})
	appendHits := func(items []artifactRankedHit) {
		for _, item := range items {
			if _, duplicate := seen[item.id]; duplicate {
				continue
			}
			seen[item.id] = struct{}{}
			hits = append(hits, item)
		}
	}
	activeHits, err := s.searchArtifactFTS(ctx, query, layers, kinds, states, limit)
	if err != nil {
		return nil, err
	}
	appendHits(activeHits)
	if hasArtifactState(states, artifact.StateStale) && len(query.Terms) > 0 {
		staleHits, err := s.searchArtifactText(ctx, query, layers, kinds,
			[]artifact.State{artifact.StateStale}, limit)
		if err != nil {
			return nil, err
		}
		appendHits(staleHits)
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
		if !hasArtifactState(states, item.State) || item.CurrentVersion <= 0 {
			continue
		}
		revision, err := getArtifactRevision(ctx, s.reader, item.ID, item.CurrentVersion)
		if errors.Is(err, artifact.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		dependencies, err := listArtifactDependencies(ctx, s.reader, item.ID, item.CurrentVersion)
		if err != nil {
			return nil, err
		}
		result = append(result, artifact.SearchHit{
			DerivedMemory: item,
			Revision:      revision,
			Dependencies:  dependencies,
			Score:         hit.score,
		})
	}
	return result, nil
}

func (s *Store) searchArtifactFTS(ctx context.Context, query artifact.SearchQuery,
	layers []artifact.Layer, kinds []artifact.Kind, states []artifact.State,
	limit int) ([]artifactRankedHit, error) {
	args := []any{4.0, 1.0, query.Expression}
	conditions := []string{"artifact_fts MATCH ?", "a.current_version = d.artifact_version"}
	conditions = append(conditions, "a.state IN ("+placeholders(len(states))+")")
	for _, state := range states {
		args = append(args, string(state))
	}
	if len(layers) > 0 {
		conditions = append(conditions, "a.layer IN ("+placeholders(len(layers))+")")
		for _, layer := range layers {
			args = append(args, string(layer))
		}
	}
	if len(kinds) > 0 {
		conditions = append(conditions, "a.kind IN ("+placeholders(len(kinds))+")")
		for _, kind := range kinds {
			args = append(args, string(kind))
		}
	}
	if query.ScopeKey != "" {
		conditions = append(conditions, "(a.scope_key = ? OR a.scope_key = 'user')")
		args = append(args, query.ScopeKey)
	}
	// A derived version remains traceable, but it leaves default recall as soon
	// as any known lower-layer input is no longer eligible.
	conditions = append(conditions, `NOT EXISTS (
		SELECT 1 FROM derived_dependencies dep
		JOIN observations o ON o.id = dep.input_id
		WHERE dep.derived_id = a.id AND dep.derived_version = a.current_version
			AND dep.input_type = 'observation'
			AND o.status IN ('deleted', 'retracted')
	)`)
	conditions = append(conditions, `NOT EXISTS (
		SELECT 1 FROM derived_dependencies dep
		JOIN memories m ON m.id = dep.input_id
		WHERE dep.derived_id = a.id AND dep.derived_version = a.current_version
			AND dep.input_type = 'memory'
			AND (m.state <> 'active' OR m.assertion_status <> 'accepted'
				OR EXISTS (
					SELECT 1 FROM memory_conflict_members cm
					JOIN memory_conflicts c ON c.id = cm.conflict_id
					WHERE cm.memory_id = m.id AND c.status = 'open'
				))
	)`)
	conditions = append(conditions, `NOT EXISTS (
		SELECT 1 FROM derived_dependencies rd
		JOIN experience_runs r ON r.id = rd.input_id
		WHERE rd.derived_id = a.id AND rd.derived_version = a.current_version
			AND rd.input_type = 'experience'
			AND r.state <> 'recorded'
	)`)
	conditions = append(conditions, `NOT EXISTS (
		SELECT 1 FROM derived_dependencies dd
		JOIN artifacts lower ON lower.id = dd.input_id
		WHERE dd.derived_id = a.id AND dd.derived_version = a.current_version
			AND dd.input_type = 'derived'
			AND (lower.state <> 'active' OR lower.current_version <> dd.input_version)
	)`)
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
	hits := []artifactRankedHit{}
	for rows.Next() {
		var item artifactRankedHit
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
	return hits, nil
}

// searchArtifactText is the bounded stale-history fallback. It matches the same
// sanitized query terms against the current revision text without using raw FTS
// syntax and never feeds model output back into evidence or identity fields.
func (s *Store) searchArtifactText(ctx context.Context, query artifact.SearchQuery,
	layers []artifact.Layer, kinds []artifact.Kind, states []artifact.State,
	limit int) ([]artifactRankedHit, error) {
	conditions := []string{"a.current_version = r.version"}
	args := []any{}
	conditions = append(conditions, "a.state IN ("+placeholders(len(states))+")")
	for _, state := range states {
		args = append(args, string(state))
	}
	if len(layers) > 0 {
		conditions = append(conditions, "a.layer IN ("+placeholders(len(layers))+")")
		for _, layer := range layers {
			args = append(args, string(layer))
		}
	}
	if len(kinds) > 0 {
		conditions = append(conditions, "a.kind IN ("+placeholders(len(kinds))+")")
		for _, kind := range kinds {
			args = append(args, string(kind))
		}
	}
	if query.ScopeKey != "" {
		conditions = append(conditions, "(a.scope_key = ? OR a.scope_key = 'user')")
		args = append(args, query.ScopeKey)
	}
	terms := make([]string, 0, len(query.Terms))
	for _, term := range query.Terms {
		trimmed := strings.ToLower(strings.TrimSpace(term))
		if trimmed == "" {
			continue
		}
		terms = append(terms, trimmed)
	}
	if len(terms) == 0 {
		return nil, nil
	}
	termConditions := make([]string, 0, len(terms))
	for _, term := range terms {
		termConditions = append(termConditions,
			"(instr(lower(r.title), ?) > 0 OR instr(lower(r.content), ?) > 0)")
		args = append(args, term, term)
	}
	conditions = append(conditions, "("+strings.Join(termConditions, " OR ")+")")
	args = append(args, limit)
	rows, err := s.reader.QueryContext(ctx, `SELECT a.id
		FROM artifacts a
		JOIN artifact_revisions r ON r.artifact_id = a.id AND r.version = a.current_version
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY a.updated_at DESC, a.id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("派生历史检索: %w", err)
	}
	defer rows.Close()
	hits := []artifactRankedHit{}
	for rows.Next() {
		var item artifactRankedHit
		if err := rows.Scan(&item.id); err != nil {
			return nil, fmt.Errorf("读取派生历史检索结果: %w", err)
		}
		hits = append(hits, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取派生历史检索结果: %w", err)
	}
	return hits, nil
}

func artifactSearchStates(input []artifact.State) []artifact.State {
	if len(input) == 0 {
		return []artifact.State{artifact.StateActive}
	}
	seen := make(map[artifact.State]struct{}, len(input))
	result := make([]artifact.State, 0, len(input))
	for _, state := range input {
		if state != artifact.StateActive && state != artifact.StateStale {
			continue
		}
		if _, duplicate := seen[state]; duplicate {
			continue
		}
		seen[state] = struct{}{}
		result = append(result, state)
	}
	return result
}

func hasArtifactState(states []artifact.State, target artifact.State) bool {
	for _, state := range states {
		if state == target {
			return true
		}
	}
	return false
}

func artifactSearchLayers(input []artifact.Layer) ([]artifact.Layer, bool) {
	if len(input) == 0 {
		return nil, true
	}
	result := make([]artifact.Layer, 0, len(input))
	for _, layer := range input {
		if layer.Known() {
			result = append(result, layer)
		}
	}
	return result, len(result) > 0
}

func artifactSearchKinds(input []artifact.Kind) ([]artifact.Kind, bool) {
	if len(input) == 0 {
		return nil, true
	}
	result := make([]artifact.Kind, 0, len(input))
	for _, kind := range input {
		if kind.Known() {
			result = append(result, kind)
		}
	}
	return result, len(result) > 0
}

func listArtifactDependencies(ctx context.Context, q artifactQueryer, artifactID string,
	version int) ([]artifact.DependencyRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT input_type, input_id, input_version, required
		FROM derived_dependencies
		WHERE derived_id = ? AND derived_version = ?
		ORDER BY input_type ASC, input_id ASC, input_version ASC`,
		artifactID, version)
	if err != nil {
		return nil, fmt.Errorf("读取派生依赖 %s@%d: %w", artifactID, version, err)
	}
	defer rows.Close()
	items := []artifact.DependencyRef{}
	for rows.Next() {
		var item artifact.DependencyRef
		var inputType string
		var required int
		if err := rows.Scan(&inputType, &item.ID, &item.Version, &required); err != nil {
			return nil, fmt.Errorf("读取派生依赖 %s@%d: %w", artifactID, version, err)
		}
		item.Type = artifact.DependencyType(inputType)
		item.Required = required == 1
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取派生依赖 %s@%d: %w", artifactID, version, err)
	}
	return items, nil
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
	items := []artifact.DerivedMemory{}
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
			AND j.kind = CASE
				WHEN a.kind IN ('skill', 'experience_case', 'guidance') THEN 'build_guidance'
				WHEN a.kind = 'local_summary' THEN 'aggregate_l1'
				WHEN a.kind = 'scope_summary' THEN 'aggregate_l3'
				ELSE 'aggregate_l2' END
		UNION ALL
		SELECT e.kind, e.target_key, e.scope_key, e.topic, '', 0, '', '',
			'', e.status, e.reason_code, e.evaluated_at,
			COALESCE(j.status, ''), COALESCE(j.wait_reason, ''),
			COALESCE(j.last_error_code, ''), COALESCE(j.updated_at, ''),
			e.target_key
		FROM derived_target_evaluations e
		LEFT JOIN governance_jobs j ON j.target_key = e.target_key
			AND j.kind = CASE
				WHEN e.kind IN ('skill', 'experience_case', 'guidance') THEN 'build_guidance'
				WHEN e.kind = 'local_summary' THEN 'aggregate_l1'
				WHEN e.kind = 'scope_summary' THEN 'aggregate_l3'
				ELSE 'aggregate_l2' END
		WHERE NOT EXISTS (
			SELECT 1 FROM artifacts a WHERE a.kind = e.kind AND a.target_key = e.target_key
		)
		UNION ALL
		SELECT '', j.target_key, '', '', '', 0, '', '', '', '', '', '',
			j.status, j.wait_reason, j.last_error_code, j.updated_at,
			j.target_key
		FROM governance_jobs j
		WHERE j.kind IN ('aggregate_l1', 'aggregate_l2', 'aggregate_l3', 'build_guidance')
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
			item := artifact.DerivedMemory{
				ID: artifactID, Layer: artifact.LayerForKind(artifact.Kind(kind)),
				Kind: artifact.Kind(kind), TargetKey: targetKey,
				ScopeKey: scopeKey, Topic: topic, CurrentVersion: currentVersion,
				State: artifact.State(artifactState), ReasonCode: artifactReason,
			}
			if artifactUpdated != "" {
				if parsed, err := parseTime(artifactUpdated); err == nil {
					item.UpdatedAt = parsed
					record.UpdatedAt = parsed
				}
			}
			record.DerivedMemory = &item
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
	switch kind {
	case artifact.KindSkill, artifact.KindExperienceCase, artifact.KindGuidance:
		return task.KindBuildGuidance
	case artifact.KindLocalSummary:
		return task.KindAggregateL1
	case artifact.KindScopeSummary:
		return task.KindAggregateL3
	default:
		return task.KindAggregateL2
	}
}

func parseTargetRecordKey(targetKey string) (artifact.Target, bool) {
	target, err := artifact.ParseTargetKey(targetKey)
	if err != nil {
		return artifact.Target{}, false
	}
	return target, true
}

func targetStatusFromRecord(record artifact.TargetRecord, evaluationStatus, evaluationReason string) (artifact.TargetStatus, string) {
	reason := record.ReasonCode
	if record.DerivedMemory != nil && record.DerivedMemory.ReasonCode != "" {
		reason = record.DerivedMemory.ReasonCode
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
	if record.DerivedMemory != nil {
		switch record.DerivedMemory.State {
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
	if record.DerivedMemory != nil {
		return artifact.TargetStatusDisabled, reason
	}
	return artifact.TargetStatusDisabled, ""
}

func reasonStatus(reason string) artifact.TargetStatus {
	switch reason {
	case "no_procedure_input", "no_eligible_input", "topic_mismatch",
		"source_redacted", "observation_deleted", "observation_retracted",
		"input_invalidated":
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
				SELECT 1 FROM derived_dependencies dep
				JOIN memories m ON m.id = dep.input_id
				WHERE dep.derived_id = a.id AND dep.derived_version = a.current_version
					AND dep.input_type = 'memory'
					AND (m.state <> 'active' OR m.assertion_status <> 'accepted'
						OR EXISTS (
							SELECT 1 FROM memory_conflict_members cm
							JOIN memory_conflicts c ON c.id = cm.conflict_id
							WHERE cm.memory_id = m.id AND c.status = 'open'
						))
			) OR EXISTS (
				SELECT 1 FROM derived_dependencies rd
				JOIN experience_runs r ON r.id = rd.input_id
				WHERE rd.derived_id = a.id AND rd.derived_version = a.current_version
					AND rd.input_type = 'experience'
					AND r.state <> 'recorded'
			))
		ORDER BY a.target_key ASC LIMIT ?`, after, limit)
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
