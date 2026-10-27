package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// DeleteForgetConflicts removes the entire dependent conflict, including
// resolved audit history. Pruning only its membership would retain an audit of
// the forgotten assertion and leave running review jobs able to commit.
func (t *Tx) DeleteForgetConflicts(ctx context.Context, memoryID string, now time.Time) error {
	rows, err := t.tx.QueryContext(ctx, `SELECT conflict_id FROM memory_conflict_members WHERE memory_id = ?
		UNION SELECT conflict_id FROM memory_conflict_events WHERE memory_id = ? ORDER BY 1`, memoryID, memoryID)
	if err != nil {
		return fmt.Errorf("读取遗忘冲突 %s: %w", memoryID, err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, id := range ids {
		if _, err := t.CancelJob(ctx, task.KindReviewConflict, task.ConflictKey(id), now); err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(ctx, `DELETE FROM memory_conflicts WHERE id = ?`, id); err != nil {
			return fmt.Errorf("清除遗忘冲突 %s: %w", id, err)
		}
	}
	return nil
}

// ListForgetGroupMemories deliberately includes deleted tombstones: permanent
// forget must not leave restorable copies behind. Unlike reasoning scans it has
// no visibility filter or result limit.
func (t *Tx) ListForgetGroupMemories(ctx context.Context, groupID string) ([]memory.Memory, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memories
		WHERE fact_group_id = ? ORDER BY id ASC`, groupID)
	if err != nil {
		return nil, fmt.Errorf("读取遗忘事实组 %s: %w", groupID, err)
	}
	defer rows.Close()
	items := []memory.Memory{}
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("读取遗忘事实组 %s: %w", groupID, err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListMemoryIDsForSource returns every atomic memory associated with a source,
// including memories that only keep the source in a historical revision or an
// evidence link. Permanent deletion removes the conservative whole set.
func (t *Tx) ListMemoryIDsForSource(ctx context.Context, sourceID string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT m.id FROM memories m
		WHERE m.origin_source_id = ?
		UNION
		SELECT DISTINCT r.memory_id FROM memory_revisions r
		WHERE r.origin_source_id = ?
		UNION
		SELECT DISTINCT re.memory_id FROM revision_evidence re
		JOIN evidence e ON e.id = re.evidence_id
		WHERE e.source_id = ?
		ORDER BY 1 ASC`, sourceID, sourceID, sourceID)
	if err != nil {
		return nil, fmt.Errorf("读取来源 %s 的关联记忆: %w", sourceID, err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("读取来源 %s 的关联记忆: %w", sourceID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取来源 %s 的关联记忆: %w", sourceID, err)
	}
	return ids, nil
}

// ListExperienceIDsForSource returns every application report stored from one
// source.
func (t *Tx) ListExperienceIDsForSource(ctx context.Context, sourceID string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx,
		`SELECT id FROM experience_runs WHERE source_id = ? ORDER BY id ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("读取来源 %s 的关联结果: %w", sourceID, err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("读取来源 %s 的关联结果: %w", sourceID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取来源 %s 的关联结果: %w", sourceID, err)
	}
	return ids, nil
}

// DeleteExperienceRun removes one application report. Dependent artifacts and
// their dependency edges must be enumerated and deleted first.
func (t *Tx) DeleteExperienceRun(ctx context.Context, experienceID string) error {
	result, err := t.tx.ExecContext(ctx, `DELETE FROM experience_runs WHERE id = ?`, experienceID)
	if err != nil {
		return fmt.Errorf("清除结果反馈 %s: %w", experienceID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("清除结果反馈 %s: %w", experienceID, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", governance.ErrExperienceNotFound, experienceID)
	}
	return nil
}

// DeleteIngestJobsBySource removes ingest job history that references a source.
// The source foreign key is RESTRICT, so jobs must go before the source row.
func (t *Tx) DeleteIngestJobsBySource(ctx context.Context, sourceID string) error {
	if _, err := t.tx.ExecContext(ctx,
		`DELETE FROM ingest_jobs WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("清除来源 %s 的接收任务: %w", sourceID, err)
	}
	return nil
}

// DeleteEvidenceBySource removes every evidence row copied from a source.
// Revision links cascade with the evidence; associated memories are purged
// before this call.
func (t *Tx) DeleteEvidenceBySource(ctx context.Context, sourceID string) error {
	if _, err := t.tx.ExecContext(ctx,
		`DELETE FROM evidence WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("清除来源 %s 的证据: %w", sourceID, err)
	}
	return nil
}

// DeleteSource permanently removes the source row. The service first removes
// jobs, evidence, linked memories and reports in the same transaction.
func (t *Tx) DeleteSource(ctx context.Context, sourceID string) error {
	result, err := t.tx.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, sourceID)
	if err != nil {
		return fmt.Errorf("清除来源 %s: %w", sourceID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("清除来源 %s: %w", sourceID, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", source.ErrNotFound, sourceID)
	}
	return nil
}
