package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
)

const jobColumns = `id, source_id, status, attempts, next_attempt_at, error, memory_ids,
	created_at, updated_at, started_at, finished_at`

// GetJob reads a job directly from the read pool.
func (s *Store) GetJob(ctx context.Context, id string) (ingest.Job, error) {
	return getJob(ctx, s.reader, id)
}

func (t *Tx) GetJob(ctx context.Context, id string) (ingest.Job, error) {
	return getJob(ctx, t.tx, id)
}

func getJob(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (ingest.Job, error) {
	item, err := scanJob(q.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM ingest_jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ingest.Job{}, fmt.Errorf("%w: %s", ingest.ErrNotFound, id)
	}
	if err != nil {
		return ingest.Job{}, fmt.Errorf("读取任务 %s: %w", id, err)
	}
	return item, nil
}

func scanJob(row rowScanner) (ingest.Job, error) {
	var item ingest.Job
	var status, memoryIDs, createdAt, updatedAt string
	var nextAttempt, startedAt, finishedAt sql.NullString
	if err := row.Scan(
		&item.ID, &item.SourceID, &status, &item.Attempts, &nextAttempt, &item.Error,
		&memoryIDs, &createdAt, &updatedAt, &startedAt, &finishedAt,
	); err != nil {
		return ingest.Job{}, err
	}
	item.Status = ingest.JobStatus(status)
	if memoryIDs == "" {
		memoryIDs = "[]"
	}
	if err := json.Unmarshal([]byte(memoryIDs), &item.MemoryIDs); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的结果: %w", item.ID, err)
	}
	if item.MemoryIDs == nil {
		item.MemoryIDs = []string{}
	}
	var err error
	if item.NextAttemptAt, err = nullableTime(nextAttempt); err != nil {
		return ingest.Job{}, err
	}
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return ingest.Job{}, err
	}
	if item.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return ingest.Job{}, err
	}
	if item.StartedAt, err = nullableTime(startedAt); err != nil {
		return ingest.Job{}, err
	}
	if item.FinishedAt, err = nullableTime(finishedAt); err != nil {
		return ingest.Job{}, err
	}
	return item, nil
}

func (t *Tx) InsertJob(ctx context.Context, item ingest.Job) error {
	memoryIDs, err := encodeMemoryIDs(item.MemoryIDs)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO ingest_jobs(`+jobColumns+`) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.SourceID, string(item.Status), item.Attempts, timeValue(item.NextAttemptAt),
		item.Error, memoryIDs, formatTime(item.CreatedAt), formatTime(item.UpdatedAt),
		timeValue(item.StartedAt), timeValue(item.FinishedAt),
	)
	if err != nil {
		return fmt.Errorf("写入任务 %s: %w", item.ID, err)
	}
	return nil
}

// UpdateJob persists the complete mutable job state, including terminal status,
// attempt count, sanitized error and result summary.
func (t *Tx) UpdateJob(ctx context.Context, item ingest.Job) error {
	memoryIDs, err := encodeMemoryIDs(item.MemoryIDs)
	if err != nil {
		return err
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE ingest_jobs SET
		source_id = ?, status = ?, attempts = ?, next_attempt_at = ?, error = ?,
		memory_ids = ?, updated_at = ?, started_at = ?, finished_at = ?
		WHERE id = ?`,
		item.SourceID, string(item.Status), item.Attempts, timeValue(item.NextAttemptAt),
		item.Error, memoryIDs, formatTime(item.UpdatedAt), timeValue(item.StartedAt),
		timeValue(item.FinishedAt), item.ID,
	)
	if err != nil {
		return fmt.Errorf("更新任务 %s: %w", item.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新任务 %s: %w", item.ID, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", ingest.ErrNotFound, item.ID)
	}
	return nil
}

// ClaimNextJob selects the oldest due queued job, moves it to running and
// increments attempts. Terminal states are never selected. The caller already
// holds the short write transaction.
func (t *Tx) ClaimNextJob(ctx context.Context, now time.Time) (ingest.Job, bool, error) {
	row := t.tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM ingest_jobs
		WHERE status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY created_at ASC, id ASC
		LIMIT 1`, string(ingest.JobQueued), formatTime(now))
	item, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ingest.Job{}, false, nil
	}
	if err != nil {
		return ingest.Job{}, false, fmt.Errorf("选择待执行任务: %w", err)
	}

	result, err := t.tx.ExecContext(ctx, `UPDATE ingest_jobs SET
		status = ?, attempts = attempts + 1, next_attempt_at = NULL,
		updated_at = ?, started_at = COALESCE(started_at, ?), finished_at = NULL
		WHERE id = ? AND status = ?`,
		string(ingest.JobRunning), formatTime(now), formatTime(now),
		item.ID, string(ingest.JobQueued),
	)
	if err != nil {
		return ingest.Job{}, false, fmt.Errorf("领取任务 %s: %w", item.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ingest.Job{}, false, fmt.Errorf("领取任务 %s: %w", item.ID, err)
	}
	if affected == 0 {
		return ingest.Job{}, false, nil
	}
	claimed, err := t.GetJob(ctx, item.ID)
	if err != nil {
		return ingest.Job{}, false, err
	}
	return claimed, true, nil
}

// RequeueRunningJobs moves leftover running jobs back to queued after a
// restart. Attempts and started_at are preserved; only the next execution time
// is reset.
func (t *Tx) RequeueRunningJobs(ctx context.Context, now time.Time) (int64, error) {
	result, err := t.tx.ExecContext(ctx, `UPDATE ingest_jobs SET
		status = ?, next_attempt_at = ?, updated_at = ?, finished_at = NULL
		WHERE status = ?`,
		string(ingest.JobQueued), formatTime(now), formatTime(now), string(ingest.JobRunning),
	)
	if err != nil {
		return 0, fmt.Errorf("恢复遗留任务: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("恢复遗留任务: %w", err)
	}
	return count, nil
}

func encodeMemoryIDs(ids []string) (string, error) {
	if ids == nil {
		return "[]", nil
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("序列化任务结果: %w", err)
	}
	return string(data), nil
}
