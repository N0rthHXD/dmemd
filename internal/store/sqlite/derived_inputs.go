package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
)

// ListDerivedInputs selects the lower-layer inputs of one aggregation target
// from the read pool.
func (s *Store) ListDerivedInputs(ctx context.Context, target artifact.Target, limit int) ([]artifact.InputRef, bool, error) {
	return listDerivedInputs(ctx, s.reader, target, limit)
}

// ListDerivedInputs selects the lower-layer inputs inside the caller's
// transaction so aggregation commits observe one consistent snapshot.
func (t *Tx) ListDerivedInputs(ctx context.Context, target artifact.Target, limit int) ([]artifact.InputRef, bool, error) {
	return listDerivedInputs(ctx, t.tx, target, limit)
}

// CountDerivedInputs reports how many eligible lower-layer inputs exist before
// the bounded selection. It is evaluated separately so a truncated selection is
// never mistaken for the complete eligible set.
func (s *Store) CountDerivedInputs(ctx context.Context, target artifact.Target) (int, error) {
	return countDerivedInputs(ctx, s.reader, target)
}

// CountDerivedInputs is the transaction-scoped form used inside an aggregation
// commit.
func (t *Tx) CountDerivedInputs(ctx context.Context, target artifact.Target) (int, error) {
	return countDerivedInputs(ctx, t.tx, target)
}

// ListDependents returns current derived objects whose current or historical
// revision consumes the input edge.
func (s *Store) ListDependents(ctx context.Context, input artifact.DependencyRef) ([]artifact.DerivedRef, error) {
	return listDependents(ctx, s.reader, input)
}

// ListDependents is the transaction-scoped form used by invalidation.
func (t *Tx) ListDependents(ctx context.Context, input artifact.DependencyRef) ([]artifact.DerivedRef, error) {
	return listDependents(ctx, t.tx, input)
}

// MarkDerivedStale removes one current derived revision from default recall
// immediately. The immutable revision history is retained.
func (t *Tx) MarkDerivedStale(ctx context.Context, derivedID, reason string) error {
	if strings.TrimSpace(derivedID) == "" {
		return fmt.Errorf("%w: missing derived identifier", artifact.ErrInvalidInput)
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE artifacts
		SET state = 'stale', reason_code = ?, updated_at = ?
		WHERE id = ?`, strings.TrimSpace(reason), formatTime(time.Now().UTC()), derivedID)
	if err != nil {
		return fmt.Errorf("标记派生对象失效 %s: %w", derivedID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("标记派生对象失效 %s: %w", derivedID, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", artifact.ErrNotFound, derivedID)
	}
	if err := t.DeleteArtifactSearchDocument(ctx, derivedID); err != nil {
		return err
	}
	return nil
}

// MarkDerivedStale is the standalone convenience form for callers outside a
// transaction. Invalidation paths should use the transaction-scoped method so
// state and queued work commit together.
func (s *Store) MarkDerivedStale(ctx context.Context, derivedID, reason string) error {
	return s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.MarkDerivedStale(ctx, derivedID, reason)
	})
}

// MarkDerivedDisabled records that a current derived object has no legal input
// left. Its immutable history stays readable, but the object leaves the default
// index and no rebuild is expected.
func (t *Tx) MarkDerivedDisabled(ctx context.Context, derivedID, reason string) error {
	if strings.TrimSpace(derivedID) == "" {
		return fmt.Errorf("%w: missing derived identifier", artifact.ErrInvalidInput)
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE artifacts
		SET state = 'disabled', reason_code = ?, updated_at = ?
		WHERE id = ?`, strings.TrimSpace(reason), formatTime(time.Now().UTC()), derivedID)
	if err != nil {
		return fmt.Errorf("禁用派生对象 %s: %w", derivedID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("禁用派生对象 %s: %w", derivedID, err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", artifact.ErrNotFound, derivedID)
	}
	if err := t.DeleteArtifactSearchDocument(ctx, derivedID); err != nil {
		return err
	}
	return nil
}

// MarkDerivedDisabled is the standalone convenience form for callers outside a
// transaction.
func (s *Store) MarkDerivedDisabled(ctx context.Context, derivedID, reason string) error {
	return s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.MarkDerivedDisabled(ctx, derivedID, reason)
	})
}

func listDerivedInputs(ctx context.Context, q artifactQueryer, target artifact.Target, limit int) ([]artifact.InputRef, bool, error) {
	if limit <= 0 {
		limit = 64
	}
	if strings.TrimSpace(target.ScopeKey) == "" {
		return nil, false, fmt.Errorf("%w: missing scope", artifact.ErrInvalidInput)
	}
	switch target.LayerOrDefault() {
	case artifact.LayerL1:
		return listObservationInputs(ctx, q, target, limit)
	case artifact.LayerL2:
		return listDerivedLayerInputs(ctx, q, target, artifact.LayerL1, limit)
	case artifact.LayerL3:
		return listDerivedLayerInputs(ctx, q, target, artifact.LayerL2, limit)
	case artifact.LayerL4:
		return listGuidanceInputs(ctx, q, target, limit)
	default:
		return nil, false, fmt.Errorf("%w: layer %q", artifact.ErrInvalidInput, target.Layer)
	}
}

func countDerivedInputs(ctx context.Context, q artifactQueryer, target artifact.Target) (int, error) {
	if strings.TrimSpace(target.ScopeKey) == "" {
		return 0, fmt.Errorf("%w: missing scope", artifact.ErrInvalidInput)
	}
	switch target.LayerOrDefault() {
	case artifact.LayerL1:
		conditions, args := observationInputConditions(target)
		return countDerivedRows(ctx, q, `SELECT COUNT(*) FROM observations WHERE `+
			strings.Join(conditions, " AND "), args)
	case artifact.LayerL2:
		return countDerivedLayerInputs(ctx, q, target, artifact.LayerL1)
	case artifact.LayerL3:
		return countDerivedLayerInputs(ctx, q, target, artifact.LayerL2)
	case artifact.LayerL4:
		return countGuidanceInputs(ctx, q, target)
	default:
		return 0, fmt.Errorf("%w: layer %q", artifact.ErrInvalidInput, target.Layer)
	}
}

// CountDerivedInputCandidates reports lower-layer objects that can still make a
// target legal after rebuild. Unlike CountDerivedInputs, a stale lower-layer
// object counts as a candidate: an upper layer must be marked stale rather than
// disabled while its inputs are scheduled to be regenerated.
func (s *Store) CountDerivedInputCandidates(ctx context.Context, target artifact.Target) (int, error) {
	return countDerivedInputCandidates(ctx, s.reader, target)
}

// CountDerivedInputCandidates is the transaction-scoped form used by
// dependency invalidation.
func (t *Tx) CountDerivedInputCandidates(ctx context.Context, target artifact.Target) (int, error) {
	return countDerivedInputCandidates(ctx, t.tx, target)
}

func countDerivedInputCandidates(ctx context.Context, q artifactQueryer, target artifact.Target) (int, error) {
	if strings.TrimSpace(target.ScopeKey) == "" {
		return 0, fmt.Errorf("%w: missing scope", artifact.ErrInvalidInput)
	}
	switch target.LayerOrDefault() {
	case artifact.LayerL1:
		conditions, args := observationInputConditions(target)
		return countDerivedRows(ctx, q, `SELECT COUNT(*) FROM observations WHERE `+
			strings.Join(conditions, " AND "), args)
	case artifact.LayerL2:
		return countDerivedLayerCandidates(ctx, q, target, artifact.LayerL1)
	case artifact.LayerL3:
		return countDerivedLayerCandidates(ctx, q, target, artifact.LayerL2)
	case artifact.LayerL4:
		return countGuidanceCandidates(ctx, q, target)
	default:
		return 0, fmt.Errorf("%w: layer %q", artifact.ErrInvalidInput, target.Layer)
	}
}

func countDerivedLayerCandidates(ctx context.Context, q artifactQueryer, target artifact.Target,
	lower artifact.Layer) (int, error) {
	if !lower.Known() {
		return 0, fmt.Errorf("%w: layer %q", artifact.ErrInvalidInput, lower)
	}
	conditions, args := derivedLayerCandidateConditions(target, lower)
	return countDerivedRows(ctx, q, `SELECT COUNT(*) FROM artifacts a WHERE `+
		strings.Join(conditions, " AND "), args)
}

func countGuidanceCandidates(ctx context.Context, q artifactQueryer, target artifact.Target) (int, error) {
	selection := guidanceSelectionFor(target)
	count, err := countStableMemoryInputs(ctx, q, target.ScopeKey, selection.memoryKinds)
	if err != nil {
		return 0, err
	}
	for _, lower := range selection.layers {
		next, err := countDerivedLayerCandidates(ctx, q, target, lower)
		if err != nil {
			return 0, err
		}
		count += next
	}
	if selection.includeExperience {
		experiences, err := countDerivedRows(ctx, q, `SELECT COUNT(*) FROM experience_runs
			WHERE scope_key = ? AND state = 'recorded'`, []any{strings.TrimSpace(target.ScopeKey)})
		if err != nil {
			return 0, err
		}
		count += experiences
	}
	return count, nil
}

func countDerivedLayerInputs(ctx context.Context, q artifactQueryer, target artifact.Target,
	lower artifact.Layer) (int, error) {
	if !lower.Known() {
		return 0, fmt.Errorf("%w: layer %q", artifact.ErrInvalidInput, lower)
	}
	conditions, args := derivedLayerInputConditions(target, lower)
	return countDerivedRows(ctx, q, `SELECT COUNT(*) FROM artifacts a WHERE `+
		strings.Join(conditions, " AND "), args)
}

func countGuidanceInputs(ctx context.Context, q artifactQueryer, target artifact.Target) (int, error) {
	selection := guidanceSelectionFor(target)
	count, err := countStableMemoryInputs(ctx, q, target.ScopeKey, selection.memoryKinds)
	if err != nil {
		return 0, err
	}
	for _, lower := range selection.layers {
		next, err := countDerivedLayerInputs(ctx, q, target, lower)
		if err != nil {
			return 0, err
		}
		count += next
	}
	if selection.includeExperience {
		experiences, err := countDerivedRows(ctx, q, `SELECT COUNT(*) FROM experience_runs
			WHERE scope_key = ? AND state = 'recorded'`, []any{strings.TrimSpace(target.ScopeKey)})
		if err != nil {
			return 0, err
		}
		count += experiences
	}
	return count, nil
}

// guidanceSelection is the fixed lower-layer input vocabulary of one L4 target.
// Skills require at least one stable procedure and may read L1/L2 aggregates and
// application reports. Experience cases read only stable procedures/events, L2
// context and the run supplied separately. Raw observations are never selected.
type guidanceSelection struct {
	memoryKinds       []string
	layers            []artifact.Layer
	includeExperience bool
}

func guidanceSelectionFor(target artifact.Target) guidanceSelection {
	switch target.Kind {
	case artifact.KindSkill:
		return guidanceSelection{
			memoryKinds:       []string{"procedure"},
			layers:            []artifact.Layer{artifact.LayerL1, artifact.LayerL2},
			includeExperience: true,
		}
	case artifact.KindExperienceCase:
		return guidanceSelection{
			memoryKinds: []string{"procedure", "event"},
			layers:      []artifact.Layer{artifact.LayerL2},
		}
	default:
		return guidanceSelection{
			layers:            []artifact.Layer{artifact.LayerL1, artifact.LayerL2, artifact.LayerL3},
			includeExperience: true,
		}
	}
}

func countDerivedRows(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, statement string, args []any) (int, error) {
	var count int
	if err := q.QueryRowContext(ctx, statement, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计派生输入: %w", err)
	}
	return count, nil
}

func listObservationInputs(ctx context.Context, q artifactQueryer, target artifact.Target, limit int) ([]artifact.InputRef, bool, error) {
	conditions, args := observationInputConditions(target)
	args = append(args, limit+1)
	rows, err := q.QueryContext(ctx, `SELECT id, kind, content FROM observations
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY updated_at DESC, id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("选择 L1 观察输入: %w", err)
	}
	defer rows.Close()
	items := []artifact.InputRef{}
	for rows.Next() {
		var item artifact.InputRef
		var kind string
		if err := rows.Scan(&item.ID, &kind, &item.Content); err != nil {
			return nil, false, fmt.Errorf("选择 L1 观察输入: %w", err)
		}
		item.Type = artifact.DependencyObservation
		item.Kind = kind
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("选择 L1 观察输入: %w", err)
	}
	return boundDerivedInputs(items, limit), len(items) > limit, nil
}

func observationInputConditions(target artifact.Target) ([]string, []any) {
	conditions := []string{"scope_key = ?", "status IN ('observed', 'pending')"}
	args := []any{strings.TrimSpace(target.ScopeKey)}
	bucket := strings.TrimSpace(target.Bucket)
	if bucket == "" {
		bucket = strings.TrimSpace(target.Key)
	}
	if start, end, ok := artifact.L1BucketRange(bucket); ok {
		conditions = append(conditions, "created_at >= ?", "created_at < ?")
		args = append(args, formatTime(start), formatTime(end))
	}
	return conditions, args
}

func listDerivedLayerInputs(ctx context.Context, q artifactQueryer, target artifact.Target,
	lower artifact.Layer, limit int) ([]artifact.InputRef, bool, error) {
	if !lower.Known() {
		return nil, false, fmt.Errorf("%w: layer %q", artifact.ErrInvalidInput, lower)
	}
	conditions, args := derivedLayerInputConditions(target, lower)
	args = append(args, limit+1)
	items := []artifact.InputRef{}
	query := `SELECT a.id, a.kind, a.current_version, r.title, r.content
		FROM artifacts a
		JOIN artifact_revisions r ON r.artifact_id = a.id AND r.version = a.current_version
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY a.updated_at DESC, a.id ASC LIMIT ?`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("选择 %s 派生输入: %w", lower, err)
	}
	defer rows.Close()
	for rows.Next() {
		var item artifact.InputRef
		var kind string
		if err := rows.Scan(&item.ID, &kind, &item.Version, &item.Title, &item.Content); err != nil {
			return nil, false, fmt.Errorf("选择 %s 派生输入: %w", lower, err)
		}
		item.Type = artifact.DependencyDerived
		item.Kind = kind
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("选择 %s 派生输入: %w", lower, err)
	}
	stale, err := hasStaleDerivedLayer(ctx, q, target, lower)
	if err != nil {
		return nil, false, err
	}
	truncated := len(items) > limit || stale
	return boundDerivedInputs(items, limit), truncated, nil
}

func derivedLayerInputConditions(target artifact.Target, lower artifact.Layer) ([]string, []any) {
	return derivedLayerConditions(target, lower, false)
}

func derivedLayerCandidateConditions(target artifact.Target, lower artifact.Layer) ([]string, []any) {
	return derivedLayerConditions(target, lower, true)
}

func derivedLayerConditions(target artifact.Target, lower artifact.Layer,
	includeStale bool) ([]string, []any) {
	conditions := []string{"a.scope_key = ?", "a.current_version > 0", "a.layer = ?"}
	if includeStale {
		conditions = append(conditions, "a.state IN ('active', 'stale')")
	} else {
		conditions = append(conditions, "a.state = 'active'")
	}
	args := []any{strings.TrimSpace(target.ScopeKey), string(lower)}
	// L1 local aggregates carry the code-assigned domain label. L2 selects only
	// the matching domain when a target names one; L3 selects every L2 in the
	// scope because its target has no narrower domain.
	if lower == artifact.LayerL1 && target.LayerOrDefault() == artifact.LayerL2 &&
		strings.TrimSpace(target.Topic) != "" {
		conditions = append(conditions, "(a.topic = ? OR a.topic = '')")
		args = append(args, strings.TrimSpace(target.Topic))
	}
	return conditions, args
}

func listGuidanceInputs(ctx context.Context, q artifactQueryer, target artifact.Target, limit int) ([]artifact.InputRef, bool, error) {
	selection := guidanceSelectionFor(target)
	items := []artifact.InputRef{}
	truncated := false

	memories, more, err := listStableMemoryInputs(ctx, q, target.ScopeKey, selection.memoryKinds, limit+1)
	if err != nil {
		return nil, false, err
	}
	truncated = truncated || more
	items = append(items, memories...)

	for _, lower := range selection.layers {
		derived, more, err := listDerivedLayerInputs(ctx, q, target, lower, limit+1)
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || more
		items = append(items, derived...)
	}

	if selection.includeExperience {
		experiences, more, err := listExperienceInputs(ctx, q, target.ScopeKey, limit+1)
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || more
		items = append(items, experiences...)
	}

	items = uniqueInputRefs(items)
	if len(items) > limit {
		truncated = true
	}
	return boundDerivedInputs(items, limit), truncated, nil
}

func countStableMemoryInputs(ctx context.Context, q artifactQueryer, scopeKey string,
	kinds []string) (int, error) {
	conditions, args := stableMemoryConditions(scopeKey, kinds)
	var count int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories m WHERE `+
		strings.Join(conditions, " AND "), args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计 L4 稳定记忆输入: %w", err)
	}
	return count, nil
}

func listStableMemoryInputs(ctx context.Context, q artifactQueryer, scopeKey string,
	kinds []string, limit int) ([]artifact.InputRef, bool, error) {
	conditions, args := stableMemoryConditions(scopeKey, kinds)
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, `SELECT m.id, m.kind, m.version, m.title, m.content
		FROM memories m
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY m.updated_at DESC, m.id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("选择 L4 稳定记忆输入: %w", err)
	}
	defer rows.Close()
	items := []artifact.InputRef{}
	for rows.Next() {
		var item artifact.InputRef
		var kind string
		if err := rows.Scan(&item.ID, &kind, &item.Version, &item.Title, &item.Content); err != nil {
			return nil, false, fmt.Errorf("选择 L4 稳定记忆输入: %w", err)
		}
		item.Type = artifact.DependencyMemory
		item.Kind = kind
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("选择 L4 稳定记忆输入: %w", err)
	}
	if len(items) > limit-1 {
		return items[:limit-1], true, nil
	}
	return items, false, nil
}

func stableMemoryConditions(scopeKey string, kinds []string) ([]string, []any) {
	conditions := []string{
		"m.scope_key = ?",
		"m.state = 'active'",
		"m.assertion_status = 'accepted'",
		`NOT EXISTS (
			SELECT 1 FROM memory_conflict_members cm
			JOIN memory_conflicts c ON c.id = cm.conflict_id
			WHERE cm.memory_id = m.id AND c.status = 'open'
		)`,
	}
	args := []any{strings.TrimSpace(scopeKey)}
	if len(kinds) > 0 {
		placeholders := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			placeholders = append(placeholders, "?")
			args = append(args, kind)
		}
		conditions = append(conditions, "m.kind IN ("+strings.Join(placeholders, ", ")+")")
	}
	return conditions, args
}

func listExperienceInputs(ctx context.Context, q artifactQueryer, scopeKey string, limit int) ([]artifact.InputRef, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, goal, conditions_json, actions_json, outcome
		FROM experience_runs
		WHERE scope_key = ? AND state = 'recorded'
		ORDER BY created_at DESC, id ASC LIMIT ?`, strings.TrimSpace(scopeKey), limit)
	if err != nil {
		return nil, false, fmt.Errorf("选择 L4 经验输入: %w", err)
	}
	defer rows.Close()
	items := []artifact.InputRef{}
	for rows.Next() {
		var item artifact.InputRef
		var goal, conditions, actions, outcome string
		if err := rows.Scan(&item.ID, &goal, &conditions, &actions, &outcome); err != nil {
			return nil, false, fmt.Errorf("选择 L4 经验输入: %w", err)
		}
		item.Type = artifact.DependencyExperience
		item.Kind = "experience"
		item.Title = goal
		item.Content = experienceInputContent(goal, conditions, actions, outcome)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("选择 L4 经验输入: %w", err)
	}
	return items, len(items) > limit-1, nil
}

func hasStaleDerivedLayer(ctx context.Context, q artifactQueryer, target artifact.Target,
	layer artifact.Layer) (bool, error) {
	if !layer.Known() {
		return false, nil
	}
	conditions := []string{"state = 'stale'", "scope_key = ?", "layer = ?"}
	args := []any{strings.TrimSpace(target.ScopeKey), string(layer)}
	if layer == artifact.LayerL1 && target.LayerOrDefault() == artifact.LayerL2 &&
		strings.TrimSpace(target.Topic) != "" {
		conditions = append(conditions, "(topic = ? OR topic = '')")
		args = append(args, strings.TrimSpace(target.Topic))
	}
	var stale bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM artifacts WHERE `+strings.Join(conditions, " AND ")+`
	)`, args...).Scan(&stale); err != nil {
		return false, fmt.Errorf("检查 %s 失效输入: %w", layer, err)
	}
	return stale, nil
}

func uniqueInputRefs(inputs []artifact.InputRef) []artifact.InputRef {
	seen := make(map[string]struct{}, len(inputs))
	result := make([]artifact.InputRef, 0, len(inputs))
	for _, item := range inputs {
		key := string(item.Type) + "\x00" + item.ID
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}

func boundDerivedInputs(items []artifact.InputRef, limit int) []artifact.InputRef {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

func experienceInputContent(goal, conditionsJSON, actionsJSON, outcome string) string {
	parts := []string{}
	if goal = strings.TrimSpace(goal); goal != "" {
		parts = append(parts, "目标："+goal)
	}
	if values := decodeStringList(conditionsJSON); len(values) > 0 {
		parts = append(parts, "条件："+strings.Join(values, "；"))
	}
	if values := decodeStringList(actionsJSON); len(values) > 0 {
		parts = append(parts, "动作："+strings.Join(values, "；"))
	}
	if outcome = strings.TrimSpace(outcome); outcome != "" {
		parts = append(parts, "应用报告结果："+outcome)
	}
	return strings.Join(parts, "\n")
}

func decodeStringList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return values
}

func listDependents(ctx context.Context, q artifactQueryer, input artifact.DependencyRef) ([]artifact.DerivedRef, error) {
	if input.ID == "" || !input.Type.Known() {
		return nil, fmt.Errorf("%w: dependency %q", artifact.ErrInvalidInput, input.ID)
	}
	conditions := []string{"d.input_type = ?", "d.input_id = ?"}
	args := []any{string(input.Type), input.ID}
	if input.Version > 0 {
		conditions = append(conditions, "d.input_version = ?")
		args = append(args, input.Version)
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT a.id, a.current_version, a.layer, a.kind,
		a.target_key, a.scope_key, a.topic, a.state
		FROM artifacts a
		JOIN derived_dependencies d ON d.derived_id = a.id
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY a.id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("读取依赖 %s:%s 的派生对象: %w", input.Type, input.ID, err)
	}
	defer rows.Close()
	items := []artifact.DerivedRef{}
	for rows.Next() {
		var item artifact.DerivedRef
		var layer, kind, targetKey, scopeKey, topic, state string
		if err := rows.Scan(&item.ID, &item.Version, &layer, &kind, &targetKey,
			&scopeKey, &topic, &state); err != nil {
			return nil, fmt.Errorf("读取依赖 %s:%s 的派生对象: %w", input.Type, input.ID, err)
		}
		item.Kind = artifact.Kind(kind)
		item.Layer = artifact.Layer(layer)
		if !item.Layer.Known() {
			item.Layer = artifact.LayerForKind(item.Kind)
		}
		item.TargetKey = targetKey
		item.ScopeKey = scopeKey
		item.Topic = topic
		item.State = artifact.State(state)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取依赖 %s:%s 的派生对象: %w", input.Type, input.ID, err)
	}
	return items, nil
}
