package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
)

const experienceRunColumns = `id, run_id, source_id, skill_id, skill_version, scope_key,
	goal, conditions_json, actions_json, outcome, state, created_at, updated_at, revoked_at`

// GetExperienceRun reads one saved application result from the read pool.
func (s *Store) GetExperienceRun(ctx context.Context, id string) (governance.ExperienceRun, error) {
	return getExperienceRun(ctx, s.reader, id)
}

func (t *Tx) GetExperienceRun(ctx context.Context, id string) (governance.ExperienceRun, error) {
	return getExperienceRun(ctx, t.tx, id)
}

func getExperienceRun(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (governance.ExperienceRun, error) {
	item, err := scanExperienceRun(q.QueryRowContext(ctx,
		`SELECT `+experienceRunColumns+` FROM experience_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return governance.ExperienceRun{}, fmt.Errorf("%w: %s", governance.ErrExperienceNotFound, id)
	}
	if err != nil {
		return governance.ExperienceRun{}, fmt.Errorf("读取结果反馈 %s: %w", id, err)
	}
	return item, nil
}

func (t *Tx) GetExperienceRunByRunID(ctx context.Context, runID string) (governance.ExperienceRun, error) {
	item, err := scanExperienceRun(t.tx.QueryRowContext(ctx,
		`SELECT `+experienceRunColumns+` FROM experience_runs WHERE run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return governance.ExperienceRun{}, fmt.Errorf("%w: run_id %s", governance.ErrExperienceNotFound, runID)
	}
	if err != nil {
		return governance.ExperienceRun{}, fmt.Errorf("读取结果反馈 run_id %s: %w", runID, err)
	}
	return item, nil
}

func scanExperienceRun(row rowScanner) (governance.ExperienceRun, error) {
	var item governance.ExperienceRun
	var runID, revokedAt sql.NullString
	var conditions, actions, outcome, state, created, updated string
	if err := row.Scan(
		&item.ID, &runID, &item.SourceID, &item.SkillID, &item.SkillVersion,
		&item.ScopeKey, &item.Goal, &conditions, &actions, &outcome, &state,
		&created, &updated, &revokedAt,
	); err != nil {
		return governance.ExperienceRun{}, err
	}
	item.RunID = runID.String
	item.Outcome = governance.ExperienceOutcome(outcome)
	item.State = governance.ExperienceState(state)
	if err := json.Unmarshal([]byte(conditions), &item.Conditions); err != nil {
		return governance.ExperienceRun{}, fmt.Errorf("解析结果反馈条件 %s: %w", item.ID, err)
	}
	if err := json.Unmarshal([]byte(actions), &item.Actions); err != nil {
		return governance.ExperienceRun{}, fmt.Errorf("解析结果反馈动作 %s: %w", item.ID, err)
	}
	var err error
	if item.CreatedAt, err = parseTime(created); err != nil {
		return governance.ExperienceRun{}, err
	}
	if item.UpdatedAt, err = parseTime(updated); err != nil {
		return governance.ExperienceRun{}, err
	}
	if item.RevokedAt, err = nullableTime(revokedAt); err != nil {
		return governance.ExperienceRun{}, err
	}
	return item, nil
}

// InsertExperienceRun stores one application report. A non-empty run_id is
// protected by a unique index; the service checks first so a duplicate returns
// the existing receipt without creating an unused source.
func (t *Tx) InsertExperienceRun(ctx context.Context, item governance.ExperienceRun) error {
	conditions, err := json.Marshal(nonNil(item.Conditions))
	if err != nil {
		return fmt.Errorf("序列化结果反馈条件: %w", err)
	}
	actions, err := json.Marshal(nonNil(item.Actions))
	if err != nil {
		return fmt.Errorf("序列化结果反馈动作: %w", err)
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO experience_runs(`+experienceRunColumns+`) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, nullableRunID(item.RunID), item.SourceID, item.SkillID, item.SkillVersion,
		item.ScopeKey, item.Goal, string(conditions), string(actions),
		string(item.Outcome), string(item.State), formatTime(item.CreatedAt),
		formatTime(item.UpdatedAt), timeValue(item.RevokedAt),
	)
	if err != nil {
		return fmt.Errorf("写入结果反馈 %s: %w", item.ID, err)
	}
	return nil
}

func (t *Tx) MarkExperienceRunRevoked(ctx context.Context, id string, at time.Time) error {
	result, err := t.tx.ExecContext(ctx, `UPDATE experience_runs SET
		state = ?, revoked_at = COALESCE(revoked_at, ?), updated_at = ?
		WHERE id = ?`, string(governance.ExperienceRevoked), formatTime(at), formatTime(at), id)
	if err != nil {
		return fmt.Errorf("撤销结果反馈 %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("撤销结果反馈 %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", governance.ErrExperienceNotFound, id)
	}
	return nil
}

func (t *Tx) ListExperienceRunsByScope(ctx context.Context, scopeKey string, limit int) ([]governance.ExperienceRun, error) {
	if limit <= 0 {
		limit = 50
	}
	return listExperienceRuns(ctx, t.tx, `WHERE scope_key = ? AND state = ?`, scopeKey, limit)
}

func (t *Tx) ListExperienceRunsBySkill(ctx context.Context, skillID string, limit int) ([]governance.ExperienceRun, error) {
	if limit <= 0 {
		limit = 50
	}
	return listExperienceRuns(ctx, t.tx, `WHERE skill_id = ? AND state = ?`, skillID, limit)
}

func listExperienceRuns(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, condition string, value any, limit int) ([]governance.ExperienceRun, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+experienceRunColumns+` FROM experience_runs `+
		condition+` ORDER BY created_at DESC, id ASC LIMIT ?`,
		value, string(governance.ExperienceRecorded), limit)
	if err != nil {
		return nil, fmt.Errorf("读取结果反馈列表: %w", err)
	}
	defer rows.Close()
	items := []governance.ExperienceRun{}
	for rows.Next() {
		item, err := scanExperienceRun(rows)
		if err != nil {
			return nil, fmt.Errorf("读取结果反馈列表: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取结果反馈列表: %w", err)
	}
	return items, nil
}

func nullableRunID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
