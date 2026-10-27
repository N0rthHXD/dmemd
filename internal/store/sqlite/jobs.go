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

const jobColumns = `id, source_id, status, attempts, protocol_failures, wait_reason, stage, reason_code,
	next_attempt_at, error, memory_ids, conflict_ids, pending_memory_ids, warnings,
	quality_outcome, extraction_stats, retention_stats, timings, model_usage,
	diagnostics, convergence_diagnostics, created_at, updated_at, started_at, finished_at`

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
	var status, memoryIDs, conflictIDs, pendingIDs, warnings, createdAt, updatedAt string
	var extractionStats, retentionStats, timings, modelUsage, diagnostics, convergence string
	var nextAttempt, startedAt, finishedAt sql.NullString
	if err := row.Scan(
		&item.ID, &item.SourceID, &status, &item.Attempts, &item.ProtocolFailures, &item.WaitReason,
		&item.Stage, &item.ReasonCode,
		&nextAttempt, &item.Error,
		&memoryIDs, &conflictIDs, &pendingIDs, &warnings,
		&item.QualityOutcome, &extractionStats, &retentionStats, &timings, &modelUsage,
		&diagnostics, &convergence, &createdAt, &updatedAt, &startedAt, &finishedAt,
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
	if conflictIDs == "" {
		conflictIDs = "[]"
	}
	if err := json.Unmarshal([]byte(conflictIDs), &item.ConflictIDs); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的冲突结果: %w", item.ID, err)
	}
	if item.ConflictIDs == nil {
		item.ConflictIDs = []string{}
	}
	if pendingIDs == "" {
		pendingIDs = "[]"
	}
	if err := json.Unmarshal([]byte(pendingIDs), &item.PendingMemoryIDs); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的待确认结果: %w", item.ID, err)
	}
	if item.PendingMemoryIDs == nil {
		item.PendingMemoryIDs = []string{}
	}
	if warnings == "" {
		warnings = "[]"
	}
	if err := json.Unmarshal([]byte(warnings), &item.Warnings); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的警告: %w", item.ID, err)
	}
	if item.Warnings == nil {
		item.Warnings = []string{}
	}
	if extractionStats == "" {
		extractionStats = "{}"
	}
	if err := json.Unmarshal([]byte(extractionStats), &item.ExtractionStats); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的提取统计: %w", item.ID, err)
	}
	if retentionStats == "" {
		retentionStats = "{}"
	}
	if err := json.Unmarshal([]byte(retentionStats), &item.Retention); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的保留统计: %w", item.ID, err)
	}
	item.Retention.Normalize()
	if timings == "" {
		timings = "{}"
	}
	if err := json.Unmarshal([]byte(timings), &item.Timings); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的阶段耗时: %w", item.ID, err)
	}
	if modelUsage == "" {
		modelUsage = "{}"
	}
	if err := json.Unmarshal([]byte(modelUsage), &item.ModelUsage); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的模型用量: %w", item.ID, err)
	}
	if diagnostics == "" {
		diagnostics = "[]"
	}
	if err := json.Unmarshal([]byte(diagnostics), &item.Diagnostics); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的质量诊断: %w", item.ID, err)
	}
	if item.Diagnostics == nil {
		item.Diagnostics = []ingest.ExtractionDiagnostic{}
	}
	if convergence == "" {
		convergence = "[]"
	}
	if err := json.Unmarshal([]byte(convergence), &item.Convergence); err != nil {
		return ingest.Job{}, fmt.Errorf("解析任务 %s 的收敛诊断: %w", item.ID, err)
	}
	if item.Convergence == nil {
		item.Convergence = []ingest.ConvergenceDiagnostic{}
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
	memoryIDs, conflictIDs, pendingIDs, warnings, err := encodeJobResults(item)
	if err != nil {
		return err
	}
	extractionStats, retentionStats, timings, modelUsage, diagnostics, convergence, err := encodeJobQuality(item)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO ingest_jobs(`+jobColumns+`) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.SourceID, string(item.Status), item.Attempts, item.ProtocolFailures,
		item.WaitReason, item.Stage, item.ReasonCode, timeValue(item.NextAttemptAt),
		item.Error, memoryIDs, conflictIDs, pendingIDs, warnings,
		string(item.QualityOutcome), extractionStats, retentionStats, timings, modelUsage,
		diagnostics, convergence,
		formatTime(item.CreatedAt), formatTime(item.UpdatedAt),
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
	memoryIDs, conflictIDs, pendingIDs, warnings, err := encodeJobResults(item)
	if err != nil {
		return err
	}
	extractionStats, retentionStats, timings, modelUsage, diagnostics, convergence, err := encodeJobQuality(item)
	if err != nil {
		return err
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE ingest_jobs SET
		source_id = ?, status = ?, attempts = ?, protocol_failures = ?, wait_reason = ?, stage = ?, reason_code = ?,
		next_attempt_at = ?, error = ?,
		memory_ids = ?, conflict_ids = ?, pending_memory_ids = ?, warnings = ?,
		quality_outcome = ?, extraction_stats = ?, retention_stats = ?, timings = ?, model_usage = ?,
		diagnostics = ?, convergence_diagnostics = ?,
		updated_at = ?, started_at = ?, finished_at = ?
		WHERE id = ?`,
		item.SourceID, string(item.Status), item.Attempts, item.ProtocolFailures, item.WaitReason,
		item.Stage, item.ReasonCode,
		timeValue(item.NextAttemptAt), item.Error, memoryIDs, conflictIDs, pendingIDs, warnings,
		string(item.QualityOutcome), extractionStats, retentionStats, timings, modelUsage,
		diagnostics, convergence,
		formatTime(item.UpdatedAt), timeValue(item.StartedAt),
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
		WHERE (status = ? OR (status = ? AND wait_reason <> ?))
		AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY created_at ASC, id ASC
		LIMIT 1`, string(ingest.JobQueued), string(ingest.JobWaiting),
		ingest.WaitReasonConfiguration, formatTime(now))
	item, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ingest.Job{}, false, nil
	}
	if err != nil {
		return ingest.Job{}, false, fmt.Errorf("选择待执行任务: %w", err)
	}

	result, err := t.tx.ExecContext(ctx, `UPDATE ingest_jobs SET
		status = ?, attempts = attempts + 1, next_attempt_at = NULL, wait_reason = '', stage = '', reason_code = '',
		quality_outcome = '', extraction_stats = '{}', retention_stats = '{}', timings = '{}', model_usage = '{}', diagnostics = '[]', convergence_diagnostics = '[]',
		updated_at = ?, started_at = COALESCE(started_at, ?), finished_at = NULL
		WHERE id = ? AND (status = ? OR (status = ? AND wait_reason <> ?))`,
		string(ingest.JobRunning), formatTime(now), formatTime(now),
		item.ID, string(ingest.JobQueued), string(ingest.JobWaiting),
		ingest.WaitReasonConfiguration,
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
		status = ?, next_attempt_at = ?, wait_reason = '', quality_outcome = '',
		extraction_stats = '{}', retention_stats = '{}', timings = '{}', model_usage = '{}', diagnostics = '[]', convergence_diagnostics = '[]', updated_at = ?, finished_at = NULL
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

// RequeueWaitingConfigurationJobs retries jobs parked on configuration or
// credential errors after a restart, when the process may have loaded a
// different configuration. Transient backoff and terminal states are untouched.
func (t *Tx) RequeueWaitingConfigurationJobs(ctx context.Context, now time.Time) (int64, error) {
	result, err := t.tx.ExecContext(ctx, `UPDATE ingest_jobs SET
		status = ?, next_attempt_at = ?, wait_reason = '', quality_outcome = '',
		extraction_stats = '{}', retention_stats = '{}', timings = '{}', model_usage = '{}', diagnostics = '[]', convergence_diagnostics = '[]', updated_at = ?, finished_at = NULL
		WHERE status = ? AND wait_reason = ?`,
		string(ingest.JobQueued), formatTime(now), formatTime(now),
		string(ingest.JobWaiting), ingest.WaitReasonConfiguration,
	)
	if err != nil {
		return 0, fmt.Errorf("恢复配置等待任务: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("恢复配置等待任务: %w", err)
	}
	return count, nil
}

// RequeueProtocolFailedJobs gives jobs isolated after repeated protocol errors
// one fresh evaluation after a restart, when the model or protocol version may
// have changed. Transient waiting jobs keep their own schedule.
func (t *Tx) RequeueProtocolFailedJobs(ctx context.Context, now time.Time) (int64, error) {
	result, err := t.tx.ExecContext(ctx, `UPDATE ingest_jobs SET
		status = ?, protocol_failures = 0, next_attempt_at = ?, wait_reason = '',
		quality_outcome = '', extraction_stats = '{}', retention_stats = '{}', timings = '{}', model_usage = '{}', diagnostics = '[]', convergence_diagnostics = '[]',
		updated_at = ?, finished_at = NULL
		WHERE status = ?`,
		string(ingest.JobQueued), formatTime(now), formatTime(now), string(ingest.JobFailed),
	)
	if err != nil {
		return 0, fmt.Errorf("恢复协议失败任务: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("恢复协议失败任务: %w", err)
	}
	return count, nil
}

func encodeJobResults(item ingest.Job) (string, string, string, string, error) {
	memoryIDs, err := encodeStringList(item.MemoryIDs)
	if err != nil {
		return "", "", "", "", err
	}
	conflictIDs, err := encodeStringList(item.ConflictIDs)
	if err != nil {
		return "", "", "", "", err
	}
	pendingIDs, err := encodeStringList(item.PendingMemoryIDs)
	if err != nil {
		return "", "", "", "", err
	}
	warnings, err := encodeStringList(item.Warnings)
	if err != nil {
		return "", "", "", "", err
	}
	return memoryIDs, conflictIDs, pendingIDs, warnings, nil
}

func encodeJobQuality(item ingest.Job) (string, string, string, string, string, string, error) {
	stats, err := json.Marshal(item.ExtractionStats)
	if err != nil {
		return "", "", "", "", "", "", fmt.Errorf("序列化任务提取统计: %w", err)
	}
	item.Retention.Normalize()
	retention, err := json.Marshal(item.Retention)
	if err != nil {
		return "", "", "", "", "", "", fmt.Errorf("序列化任务保留统计: %w", err)
	}
	timings, err := json.Marshal(item.Timings)
	if err != nil {
		return "", "", "", "", "", "", fmt.Errorf("序列化任务阶段耗时: %w", err)
	}
	modelUsage, err := json.Marshal(item.ModelUsage)
	if err != nil {
		return "", "", "", "", "", "", fmt.Errorf("序列化任务模型用量: %w", err)
	}
	diagnostics := item.Diagnostics
	if diagnostics == nil {
		diagnostics = []ingest.ExtractionDiagnostic{}
	}
	encoded, err := json.Marshal(diagnostics)
	if err != nil {
		return "", "", "", "", "", "", fmt.Errorf("序列化任务质量诊断: %w", err)
	}
	convergence := item.Convergence
	if convergence == nil {
		convergence = []ingest.ConvergenceDiagnostic{}
	}
	encodedConvergence, err := json.Marshal(convergence)
	if err != nil {
		return "", "", "", "", "", "", fmt.Errorf("序列化任务收敛诊断: %w", err)
	}
	return string(stats), string(retention), string(timings), string(modelUsage),
		string(encoded), string(encodedConvergence), nil
}

func encodeStringList(values []string) (string, error) {
	if values == nil {
		return "[]", nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("序列化任务结果: %w", err)
	}
	return string(data), nil
}
