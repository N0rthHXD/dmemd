package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

const factGroupColumns = `id, scope_key, subject, predicate, scope, conditions_json,
	subject_key, predicate_key, scene_key, conditions_key,
	cardinality, generation, created_at, updated_at`

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

// FindFactGroup reads one exact normalized identity. ScopeKey, subject,
// predicate, scene and required conditions must all match.
func (t *Tx) FindFactGroup(ctx context.Context, identity memory.FactGroupIdentity) (memory.FactGroup, error) {
	key := memory.FactGroupKey(identity)
	group, err := scanFactGroup(t.tx.QueryRowContext(ctx,
		`SELECT `+factGroupColumns+` FROM memory_fact_groups
		 WHERE scope_key = ? AND subject_key = ? AND predicate_key = ?
		   AND scene_key = ? AND conditions_key = ?
		 ORDER BY created_at ASC, id ASC LIMIT 1`,
		key[0], key[1], key[2], key[3], key[4]))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.FactGroup{}, fmt.Errorf("%w: fact group not found", memory.ErrNotFound)
	}
	if err != nil {
		return memory.FactGroup{}, fmt.Errorf("查找事实组: %w", err)
	}
	return group, nil
}

// FindFactGroups returns every group for one scoped subject/predicate. The
// caller uses the count to distinguish a unique identity from an ambiguous
// scene/condition split; this path never relies on body-text recall.
func (s *Store) FindFactGroups(ctx context.Context, scopeKey, subject, predicate string) ([]memory.FactGroup, error) {
	return findFactGroups(ctx, s.reader, scopeKey, subject, predicate)
}

func (t *Tx) FindFactGroups(ctx context.Context, scopeKey, subject, predicate string) ([]memory.FactGroup, error) {
	return findFactGroups(ctx, t.tx, scopeKey, subject, predicate)
}

func findFactGroups(ctx context.Context, q conflictQueryer, scopeKey, subject, predicate string) ([]memory.FactGroup, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+factGroupColumns+` FROM memory_fact_groups
		 WHERE scope_key = ? AND subject_key = ? AND predicate_key = ?
		 ORDER BY created_at ASC, id ASC`,
		memory.NormalizeScopeKey(scopeKey), memory.NormalizeIdentityText(subject), memory.NormalizeIdentityText(predicate))
	if err != nil {
		return nil, fmt.Errorf("查找事实组: %w", err)
	}
	defer rows.Close()
	items := []memory.FactGroup{}
	for rows.Next() {
		group, err := scanFactGroup(rows)
		if err != nil {
			return nil, fmt.Errorf("查找事实组: %w", err)
		}
		items = append(items, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("查找事实组: %w", err)
	}
	return items, nil
}

// FindFactGroupsByScopeAndKind is not part of the storage contract. The
// service keeps identity lookup limited to explicit scope and text keys.

func scanFactGroup(row rowScanner) (memory.FactGroup, error) {
	var group memory.FactGroup
	var conditionsJSON, cardinality, createdAt, updatedAt string
	if err := row.Scan(&group.ID, &group.ScopeKey, &group.Subject, &group.Predicate, &group.Scope,
		&conditionsJSON, &group.SubjectKey, &group.PredicateKey, &group.SceneKey, &group.ConditionsKey,
		&cardinality, &group.Generation, &createdAt, &updatedAt); err != nil {
		return memory.FactGroup{}, err
	}
	group.Cardinality = memory.Cardinality(cardinality)
	if conditionsJSON == "" {
		conditionsJSON = "[]"
	}
	if err := json.Unmarshal([]byte(conditionsJSON), &group.Conditions); err != nil {
		return memory.FactGroup{}, fmt.Errorf("解析事实组条件: %w", err)
	}
	if group.Conditions == nil {
		group.Conditions = []string{}
	}
	group.SubjectKey = memory.NormalizeIdentityText(group.Subject)
	group.PredicateKey = memory.NormalizeIdentityText(group.Predicate)
	group.SceneKey = memory.NormalizeIdentityText(group.Scope)
	group.ConditionsKey = memory.ConditionsKey(group.Conditions)
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
	conditions, err := json.Marshal(memory.NormalizeConditions(group.Conditions))
	if err != nil {
		return fmt.Errorf("编码事实组条件: %w", err)
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO memory_fact_groups(`+factGroupColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		group.ID, memory.NormalizeScopeKey(group.ScopeKey), group.Subject, group.Predicate, group.Scope,
		string(conditions), memory.NormalizeIdentityText(group.Subject),
		memory.NormalizeIdentityText(group.Predicate), memory.NormalizeIdentityText(group.Scope),
		memory.ConditionsKey(group.Conditions), string(group.Cardinality),
		group.Generation, formatTime(group.CreatedAt), formatTime(group.UpdatedAt))
	if err != nil {
		return fmt.Errorf("写入事实组 %s: %w", group.ID, err)
	}
	return t.BumpCatalogGeneration(ctx)
}

// UpdateFactGroup persists metadata and increments generation when callers
// already did so.
func (t *Tx) UpdateFactGroup(ctx context.Context, group memory.FactGroup) error {
	conditions, err := json.Marshal(memory.NormalizeConditions(group.Conditions))
	if err != nil {
		return fmt.Errorf("编码事实组条件: %w", err)
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE memory_fact_groups SET
		scope_key = ?, subject = ?, predicate = ?, scope = ?, conditions_json = ?,
		subject_key = ?, predicate_key = ?, scene_key = ?, conditions_key = ?,
		cardinality = ?, generation = ?, updated_at = ?
		WHERE id = ?`,
		memory.NormalizeScopeKey(group.ScopeKey), group.Subject, group.Predicate, group.Scope,
		string(conditions), memory.NormalizeIdentityText(group.Subject),
		memory.NormalizeIdentityText(group.Predicate), memory.NormalizeIdentityText(group.Scope),
		memory.ConditionsKey(group.Conditions), string(group.Cardinality),
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
