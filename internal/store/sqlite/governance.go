package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/jobs"
)

const governanceJobColumns = `id, kind, target_key, revision, status, attempts,
	consecutive_failures, next_attempt_at, last_error_code, wait_reason, input_hash,
	created_at, updated_at, started_at, finished_at`

// GovernanceStore adapts Store to the governance module's short transaction.
type GovernanceStore struct{ *Store }

// WithTx implements governance.Store.
func (s GovernanceStore) WithTx(ctx context.Context, fn func(context.Context, governance.Tx) error) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx *Tx) error { return fn(ctx, tx) })
}

var _ governance.Store = GovernanceStore{}

// GetGovernanceJob reads one task directly from the read pool.
func (s *Store) GetGovernanceJob(ctx context.Context, id string) (task.Job, error) {
	return getGovernanceJob(ctx, s.reader, id)
}

// GetGovernanceJobByTarget reads the unique task for one kind and target.
func (s *Store) GetGovernanceJobByTarget(ctx context.Context, kind task.Kind, targetKey string) (task.Job, error) {
	item, err := getGovernanceJobByTarget(ctx, s.reader, kind, targetKey)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Job{}, fmt.Errorf("%w: %s/%s", task.ErrNotFound, kind, targetKey)
	}
	if err != nil {
		return task.Job{}, fmt.Errorf("读取治理任务 %s/%s: %w", kind, targetKey, err)
	}
	return item, nil
}

func (t *Tx) GetGovernanceJob(ctx context.Context, id string) (task.Job, error) {
	return getGovernanceJob(ctx, t.tx, id)
}

func (t *Tx) GetGovernanceJobByTarget(ctx context.Context, kind task.Kind, targetKey string) (task.Job, error) {
	item, err := getGovernanceJobByTarget(ctx, t.tx, kind, targetKey)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Job{}, fmt.Errorf("%w: %s/%s", task.ErrNotFound, kind, targetKey)
	}
	if err != nil {
		return task.Job{}, fmt.Errorf("读取治理任务 %s/%s: %w", kind, targetKey, err)
	}
	return item, nil
}

func getGovernanceJob(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (task.Job, error) {
	item, err := scanGovernanceJob(q.QueryRowContext(ctx,
		`SELECT `+governanceJobColumns+` FROM governance_jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Job{}, fmt.Errorf("%w: %s", task.ErrNotFound, id)
	}
	if err != nil {
		return task.Job{}, fmt.Errorf("读取治理任务 %s: %w", id, err)
	}
	return item, nil
}

// ListGovernanceJobs returns governance and aggregation tasks in newest-first
// order. It is a bounded local operations view; the input hash and revision stay
// available for diagnostics but are never used to fake stage progress.
func (s *Store) ListGovernanceJobs(ctx context.Context, options jobs.TaskListOptions) ([]task.Job, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	conditions := []string{}
	args := []any{}
	if options.Category != "" {
		switch options.Category {
		case jobs.CategoryAggregation:
			conditions = append(conditions, "kind IN ('aggregate_l1', 'aggregate_l2', 'aggregate_l3', 'build_guidance')")
		case jobs.CategoryGovernance:
			conditions = append(conditions, "kind NOT IN ('aggregate_l1', 'aggregate_l2', 'aggregate_l3', 'build_guidance')")
		}
	}
	if options.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(options.Status))
	}
	if options.Kind != "" {
		conditions = append(conditions, "kind = ?")
		args = append(args, string(options.Kind))
	}
	if options.TargetKey != "" {
		conditions = append(conditions, "target_key = ?")
		args = append(args, options.TargetKey)
	}
	query := `SELECT ` + governanceJobColumns + ` FROM governance_jobs`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("列出治理任务: %w", err)
	}
	defer rows.Close()
	items := []task.Job{}
	for rows.Next() {
		item, err := scanGovernanceJob(rows)
		if err != nil {
			return nil, fmt.Errorf("列出治理任务: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("列出治理任务: %w", err)
	}
	return items, nil
}

func scanGovernanceJob(row rowScanner) (task.Job, error) {
	var item task.Job
	var kind, status string
	var created, updated string
	var nextAttempt, startedAt, finishedAt sql.NullString
	if err := row.Scan(
		&item.ID, &kind, &item.TargetKey, &item.Revision, &status, &item.Attempts,
		&item.ConsecutiveFailures, &nextAttempt, &item.LastErrorCode, &item.WaitReason,
		&item.InputHash, &created, &updated, &startedAt, &finishedAt,
	); err != nil {
		return task.Job{}, err
	}
	item.Kind = task.Kind(kind)
	item.Status = task.Status(status)
	var err error
	if item.NextAttemptAt, err = nullableTime(nextAttempt); err != nil {
		return task.Job{}, err
	}
	if item.CreatedAt, err = parseTime(created); err != nil {
		return task.Job{}, err
	}
	if item.UpdatedAt, err = parseTime(updated); err != nil {
		return task.Job{}, err
	}
	if item.StartedAt, err = nullableTime(startedAt); err != nil {
		return task.Job{}, err
	}
	if item.FinishedAt, err = nullableTime(finishedAt); err != nil {
		return task.Job{}, err
	}
	return item, nil
}

// UpsertJob registers or refreshes one (kind,target_key) target. An identical
// input hash is a no-op, which makes repeated writes and reconcile scans
// converge without creating duplicate work or new derivative versions.
func (t *Tx) UpsertJob(ctx context.Context, input task.UpsertInput) (task.Job, bool, error) {
	if input.Kind == "" || !input.Kind.Known() {
		return task.Job{}, false, fmt.Errorf("未知治理任务类型 %q", input.Kind)
	}
	if input.TargetKey == "" {
		return task.Job{}, false, errors.New("治理任务 target_key 不能为空")
	}
	now := input.Now.UTC()
	existing, err := getGovernanceJobByTarget(ctx, t.tx, input.Kind, input.TargetKey)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		job := task.Job{
			ID:            idgen.NewUUID(),
			Kind:          input.Kind,
			TargetKey:     input.TargetKey,
			Revision:      1,
			Status:        task.StatusQueued,
			Attempts:      0,
			NextAttemptAt: input.NextAttemptAt,
			InputHash:     input.InputHash,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO governance_jobs(`+governanceJobColumns+`) VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			job.ID, string(job.Kind), job.TargetKey, job.Revision, string(job.Status), job.Attempts,
			job.ConsecutiveFailures, timeValue(job.NextAttemptAt), job.LastErrorCode, job.WaitReason,
			job.InputHash, formatTime(job.CreatedAt), formatTime(job.UpdatedAt),
			timeValue(job.StartedAt), timeValue(job.FinishedAt),
		); err != nil {
			return task.Job{}, false, fmt.Errorf("登记治理任务: %w", err)
		}
		return job, true, nil
	case err != nil:
		return task.Job{}, false, err
	}
	if existing.InputHash == input.InputHash {
		return existing, false, nil
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE governance_jobs SET
		revision = revision + 1, status = ?, next_attempt_at = ?, input_hash = ?,
		last_error_code = '', wait_reason = '', consecutive_failures = 0,
		updated_at = ?, finished_at = NULL
		WHERE id = ?`, string(task.StatusQueued), timeValue(input.NextAttemptAt),
		input.InputHash, formatTime(now), existing.ID,
	); err != nil {
		return task.Job{}, false, fmt.Errorf("更新治理任务 %s: %w", existing.ID, err)
	}
	updated, err := getGovernanceJob(ctx, t.tx, existing.ID)
	if err != nil {
		return task.Job{}, false, err
	}
	return updated, true, nil
}

func getGovernanceJobByTarget(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, kind task.Kind, targetKey string) (task.Job, error) {
	return scanGovernanceJob(q.QueryRowContext(ctx,
		`SELECT `+governanceJobColumns+` FROM governance_jobs WHERE kind = ? AND target_key = ?`,
		string(kind), targetKey))
}

func (t *Tx) CancelJob(ctx context.Context, kind task.Kind, targetKey string, now time.Time) (bool, error) {
	existing, err := getGovernanceJobByTarget(ctx, t.tx, kind, targetKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if existing.Status == task.StatusCancelled {
		return false, nil
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE governance_jobs SET
		revision = revision + 1, status = ?, next_attempt_at = NULL, wait_reason = '',
		input_hash = '', updated_at = ?, finished_at = ? WHERE id = ?`,
		string(task.StatusCancelled), formatTime(now.UTC()), formatTime(now.UTC()), existing.ID,
	); err != nil {
		return false, fmt.Errorf("取消治理任务 %s: %w", existing.ID, err)
	}
	return true, nil
}

func (t *Tx) ClaimNextGovernanceJob(ctx context.Context, now time.Time) (task.Job, bool, error) {
	return t.claimNextGovernanceJob(ctx, now, nil)
}

// ClaimNextGovernanceJobForKinds claims only task types owned by one worker.
// An empty kind list keeps the original all-types behavior.
func (t *Tx) ClaimNextGovernanceJobForKinds(ctx context.Context, now time.Time, kinds []task.Kind) (task.Job, bool, error) {
	return t.claimNextGovernanceJob(ctx, now, kinds)
}

func (t *Tx) claimNextGovernanceJob(ctx context.Context, now time.Time, kinds []task.Kind) (task.Job, bool, error) {
	kindClause, kindArgs := kindFilter(kinds)
	// Check in the same write transaction as the claim. Configuration-blocked
	// and future-backoff ingestion must not starve governance. Already executing
	// governance calls are not interrupted, but no new task starts ahead of ingest.
	var busy bool
	bypassPriority := 0
	if err := t.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ingest_jobs
		WHERE status = 'running' OR
		((status = 'queued' OR (status = 'waiting' AND wait_reason <> 'configuration'))
		AND (next_attempt_at IS NULL OR next_attempt_at <= ?)))`, formatTime(now)).Scan(&busy); err != nil {
		return task.Job{}, false, fmt.Errorf("检查摄取优先级: %w", err)
	}
	if busy {
		// A due target older than the fairness limit gets one slot even while
		// ingest keeps arriving. This is a bounded anti-starvation guard, not a
		// change to ordinary ingest priority.
		threshold := formatTime(now.UTC().Add(-t.store.governanceFairnessLimit()))
		args := []any{
			string(task.StatusQueued), string(task.StatusWaiting), task.WaitConfiguration,
			formatTime(now),
		}
		args = append(args, kindArgs...)
		args = append(args, threshold)
		var starved bool
		if err := t.tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM governance_jobs
			WHERE (status = ? OR (status = ? AND wait_reason <> ?))
			AND (next_attempt_at IS NULL OR next_attempt_at <= ?)`+kindClause+`
			AND created_at <= ?)`, args...).Scan(&starved); err != nil {
			return task.Job{}, false, fmt.Errorf("检查治理任务饥饿保护: %w", err)
		}
		if !starved {
			return task.Job{}, false, nil
		}
		bypassPriority = 1
	}
	args := []any{
		string(task.StatusQueued), string(task.StatusWaiting), task.WaitConfiguration,
		formatTime(now),
	}
	args = append(args, kindArgs...)
	args = append(args, bypassPriority)
	row := t.tx.QueryRowContext(ctx, `SELECT `+governanceJobColumns+` FROM governance_jobs
		WHERE (status = ? OR (status = ? AND wait_reason <> ?))
		AND (next_attempt_at IS NULL OR next_attempt_at <= ?)`+kindClause+`
		ORDER BY CASE WHEN ? = 1 THEN 0
			WHEN kind IN ('aggregate_l1', 'aggregate_l2', 'aggregate_l3', 'build_guidance') THEN 1
			ELSE 0 END,
			created_at ASC, id ASC LIMIT 1`, args...)
	item, err := scanGovernanceJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Job{}, false, nil
	}
	if err != nil {
		return task.Job{}, false, fmt.Errorf("选择待执行治理任务: %w", err)
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE governance_jobs SET
		status = ?, attempts = attempts + 1, next_attempt_at = NULL, wait_reason = '',
		updated_at = ?, started_at = COALESCE(started_at, ?), finished_at = NULL
		WHERE id = ? AND (status = ? OR (status = ? AND wait_reason <> ?))`,
		string(task.StatusRunning), formatTime(now), formatTime(now),
		item.ID, string(task.StatusQueued), string(task.StatusWaiting), task.WaitConfiguration,
	)
	if err != nil {
		return task.Job{}, false, fmt.Errorf("领取治理任务 %s: %w", item.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return task.Job{}, false, fmt.Errorf("领取治理任务 %s: %w", item.ID, err)
	}
	if affected == 0 {
		return task.Job{}, false, nil
	}
	claimed, err := getGovernanceJob(ctx, t.tx, item.ID)
	if err != nil {
		return task.Job{}, false, err
	}
	return claimed, true, nil
}

// kindFilter returns a SQL fragment that restricts a query to the supplied task
// kinds. Empty kinds mean no restriction.
func kindFilter(kinds []task.Kind) (string, []any) {
	if len(kinds) == 0 {
		return "", nil
	}
	placeholders := make([]string, 0, len(kinds))
	args := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		placeholders = append(placeholders, "?")
		args = append(args, string(kind))
	}
	return " AND kind IN (" + strings.Join(placeholders, ", ") + ")", args
}

func (t *Tx) RequeueRunningGovernanceJobs(ctx context.Context, now time.Time) (int64, error) {
	return t.requeueJobs(ctx, `status = ?`, []any{string(task.StatusRunning)}, now)
}

func (t *Tx) RequeueRunningGovernanceJobsForKinds(ctx context.Context, now time.Time, kinds []task.Kind) (int64, error) {
	return t.requeueJobsForKinds(ctx, `status = ?`, []any{string(task.StatusRunning)}, now, kinds)
}

func (t *Tx) RequeueWaitingConfigurationGovernanceJobs(ctx context.Context, now time.Time) (int64, error) {
	return t.requeueJobs(ctx, `status = ? AND wait_reason = ?`,
		[]any{string(task.StatusWaiting), task.WaitConfiguration}, now)
}

func (t *Tx) RequeueWaitingConfigurationGovernanceJobsForKinds(ctx context.Context, now time.Time, kinds []task.Kind) (int64, error) {
	return t.requeueJobsForKinds(ctx, `status = ? AND wait_reason = ?`,
		[]any{string(task.StatusWaiting), task.WaitConfiguration}, now, kinds)
}

// RequeueProtocolFailedGovernanceJobs gives tasks isolated after repeated
// protocol errors one fresh evaluation after a restart, when the model or
// protocol version may have changed.
func (t *Tx) RequeueProtocolFailedGovernanceJobs(ctx context.Context, now time.Time) (int64, error) {
	return t.requeueProtocolFailed(ctx, now, nil)
}

func (t *Tx) RequeueProtocolFailedGovernanceJobsForKinds(ctx context.Context, now time.Time, kinds []task.Kind) (int64, error) {
	return t.requeueProtocolFailed(ctx, now, kinds)
}

func (t *Tx) requeueProtocolFailed(ctx context.Context, now time.Time, kinds []task.Kind) (int64, error) {
	kindClause, kindArgs := kindFilter(kinds)
	condition := `status = ?`
	args := []any{string(task.StatusFailed)}
	if kindClause != "" {
		condition = strings.TrimPrefix(kindClause, " AND ") + " AND " + condition
		args = append(kindArgs, args...)
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE governance_jobs SET
		status = ?, next_attempt_at = ?, last_error_code = '', wait_reason = '',
		consecutive_failures = 0, updated_at = ?, finished_at = NULL
		WHERE `+condition,
		append([]any{string(task.StatusQueued), formatTime(now), formatTime(now)}, args...)...,
	)
	if err != nil {
		return 0, fmt.Errorf("恢复协议失败治理任务: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("恢复协议失败治理任务: %w", err)
	}
	return count, nil
}

func (t *Tx) RequeueStaleRunningGovernanceJobs(ctx context.Context, olderThan, now time.Time) (int64, error) {
	return t.requeueJobs(ctx, `status = ? AND updated_at < ?`,
		[]any{string(task.StatusRunning), formatTime(olderThan)}, now)
}

func (t *Tx) requeueJobsForKinds(ctx context.Context, condition string, args []any, now time.Time, kinds []task.Kind) (int64, error) {
	kindClause, kindArgs := kindFilter(kinds)
	if kindClause != "" {
		condition = strings.TrimPrefix(kindClause, " AND ") + " AND " + condition
		args = append(kindArgs, args...)
	}
	return t.requeueJobs(ctx, condition, args, now)
}

func (t *Tx) requeueJobs(ctx context.Context, condition string, args []any, now time.Time) (int64, error) {
	queryArgs := append([]any{string(task.StatusQueued), formatTime(now), formatTime(now)}, args...)
	result, err := t.tx.ExecContext(ctx, `UPDATE governance_jobs SET
		status = ?, next_attempt_at = ?, wait_reason = '', updated_at = ?, finished_at = NULL
		WHERE `+condition, queryArgs...)
	if err != nil {
		return 0, fmt.Errorf("恢复治理任务: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("恢复治理任务: %w", err)
	}
	return count, nil
}

// BumpSkillJobsForScope invalidates every queued, waiting or running skill task
// in one scope. It closes the race where an in-flight skill generation selected
// a run that is retracted before its result transaction commits.
func (t *Tx) BumpSkillJobsForScope(ctx context.Context, scopeKey string, now time.Time) (int64, error) {
	prefix := task.SkillTargetPrefix(scopeKey)
	result, err := t.tx.ExecContext(ctx, `UPDATE governance_jobs SET
		revision = revision + 1, status = ?, next_attempt_at = ?, last_error_code = '',
		wait_reason = '', consecutive_failures = 0, input_hash = '', updated_at = ?,
		finished_at = NULL
		WHERE kind = ? AND substr(target_key, 1, length(?)) = ?
			AND status IN (?, ?, ?)`,
		string(task.StatusQueued), formatTime(now), formatTime(now),
		string(task.KindBuildGuidance), prefix, prefix,
		string(task.StatusQueued), string(task.StatusWaiting), string(task.StatusRunning),
	)
	if err != nil {
		return 0, fmt.Errorf("使作用域 %s 的 Skill 任务失效: %w", scopeKey, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("使作用域 %s 的 Skill 任务失效: %w", scopeKey, err)
	}
	return count, nil
}

func (t *Tx) ListMemoryIDsMissingJob(ctx context.Context, kind task.Kind, afterID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := t.tx.QueryContext(ctx, `SELECT m.id FROM memories m
		WHERE m.id > ? AND m.state <> 'deleted'
		AND NOT EXISTS (
			SELECT 1 FROM governance_jobs g
			WHERE g.kind = ? AND g.target_key = m.id
		)
		ORDER BY m.id ASC LIMIT ?`, afterID, string(kind), limit)
	if err != nil {
		return nil, fmt.Errorf("扫描缺少治理任务的记忆: %w", err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("扫描缺少治理任务的记忆: %w", err)
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("扫描缺少治理任务的记忆: %w", err)
	}
	return result, nil
}

func (t *Tx) FinishJob(ctx context.Context, id string, revision int64, now time.Time) (bool, error) {
	result, err := t.tx.ExecContext(ctx, `UPDATE governance_jobs SET
		status = ?, next_attempt_at = NULL, last_error_code = '', wait_reason = '',
		consecutive_failures = 0, updated_at = ?, finished_at = ?
		WHERE id = ? AND revision = ? AND status = ?`,
		string(task.StatusSucceeded), formatTime(now), formatTime(now),
		id, revision, string(task.StatusRunning),
	)
	if err != nil {
		return false, fmt.Errorf("完成治理任务 %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("完成治理任务 %s: %w", id, err)
	}
	return affected == 1, nil
}

func (t *Tx) RescheduleJob(ctx context.Context, id string, revision int64, input task.RescheduleInput) (bool, error) {
	now := input.Now.UTC()
	finished := any(nil)
	if input.Status.Terminal() {
		finished = formatTime(now)
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE governance_jobs SET
		status = ?, next_attempt_at = ?, last_error_code = ?, wait_reason = ?,
		consecutive_failures = ?, input_hash = CASE WHEN ? = ? THEN '' ELSE input_hash END,
		updated_at = ?, finished_at = ?
		WHERE id = ? AND revision = ? AND status = ?`,
		string(input.Status), timeValue(input.NextAttemptAt), input.LastErrorCode,
		input.WaitReason, input.ConsecutiveFailures, string(input.Status),
		string(task.StatusCancelled), formatTime(now), finished,
		id, revision, string(task.StatusRunning),
	)
	if err != nil {
		return false, fmt.Errorf("更新治理任务 %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("更新治理任务 %s: %w", id, err)
	}
	return affected == 1, nil
}
