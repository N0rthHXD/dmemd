package sqlite

import (
	"context"
	"fmt"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/source"
)

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
