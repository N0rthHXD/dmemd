package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

var _ embedding.Backend = (*Store)(nil)

const embeddingJobColumns = `memory_id, profile_id, content_hash, attempts`

// ClaimJob selects one due queued generation and increments its attempts in one
// short write transaction.
func (s *Store) ClaimJob(ctx context.Context, profileID string, now time.Time) (embedding.Job, bool, error) {
	if profileID == "" {
		return embedding.Job{}, false, nil
	}
	var claimed embedding.Job
	var found bool
	err := s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		var job embedding.Job
		err := tx.tx.QueryRowContext(ctx, `SELECT `+embeddingJobColumns+`
			FROM memory_embedding_jobs
			WHERE profile_id = ? AND status = 'queued'
				AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
			ORDER BY COALESCE(next_attempt_at, created_at) ASC, created_at ASC, memory_id ASC
			LIMIT 1`, profileID, formatTime(now)).Scan(
			&job.MemoryID, &job.ProfileID, &job.ContentHash, &job.Attempts,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("选择向量任务: %w", err)
		}

		result, err := tx.tx.ExecContext(ctx, `UPDATE memory_embedding_jobs SET
			status = 'running', attempts = attempts + 1, next_attempt_at = NULL, updated_at = ?
			WHERE memory_id = ? AND status = 'queued'`,
			formatTime(now), job.MemoryID,
		)
		if err != nil {
			return fmt.Errorf("领取向量任务 %s: %w", job.MemoryID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("领取向量任务 %s: %w", job.MemoryID, err)
		}
		if affected == 0 {
			return nil
		}
		job.Attempts++
		claimed = job
		found = true
		return nil
	})
	if err != nil {
		return embedding.Job{}, false, err
	}
	return claimed, found, nil
}

// LoadDocument reads the current snapshot and its derived content hash in one
// short read transaction.
func (s *Store) LoadDocument(ctx context.Context, memoryID string) (embedding.Document, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return embedding.Document{}, fmt.Errorf("开始向量输入事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	item, err := getMemory(ctx, tx, memoryID)
	if errors.Is(err, memory.ErrNotFound) {
		return embedding.Document{}, embedding.ErrInputMissing
	}
	if err != nil {
		return embedding.Document{}, err
	}
	if item.State == memory.StateDeleted {
		return embedding.Document{}, embedding.ErrInputMissing
	}
	var contentHash string
	err = tx.QueryRowContext(ctx,
		`SELECT content_hash FROM memory_search_docs WHERE memory_id = ?`, memoryID,
	).Scan(&contentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return embedding.Document{}, embedding.ErrInputMissing
	}
	if err != nil {
		return embedding.Document{}, fmt.Errorf("读取记忆 %s 的内容哈希: %w", memoryID, err)
	}
	return embedding.Document{Memory: item, ContentHash: contentHash}, nil
}

// CommitJob conditionally writes the vector and deletes the task. The profile
// and content hash must still match, and the derived document must still exist
// with the same hash.
func (s *Store) CommitJob(ctx context.Context, job embedding.Job, vector []byte, dimensions int, embeddedAt time.Time) (bool, error) {
	if dimensions <= 0 || len(vector) != dimensions*4 {
		return false, fmt.Errorf("提交向量 %s: 维数或 BLOB 长度无效", job.MemoryID)
	}
	committed := false
	err := s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		var present int
		err := tx.tx.QueryRowContext(ctx, `SELECT 1
			FROM memory_embedding_jobs j
			JOIN memory_search_docs d ON d.memory_id = j.memory_id
			WHERE j.memory_id = ? AND j.profile_id = ?
				AND j.content_hash = ? AND d.content_hash = j.content_hash
			LIMIT 1`,
			job.MemoryID, job.ProfileID, job.ContentHash,
		).Scan(&present)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("校验向量任务 %s: %w", job.MemoryID, err)
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO memory_embeddings
			(memory_id, profile_id, content_hash, dimensions, vector, embedded_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(memory_id) DO UPDATE SET
				profile_id = excluded.profile_id,
				content_hash = excluded.content_hash,
				dimensions = excluded.dimensions,
				vector = excluded.vector,
				embedded_at = excluded.embedded_at`,
			job.MemoryID, job.ProfileID, job.ContentHash, dimensions, vector, formatTime(embeddedAt),
		); err != nil {
			return fmt.Errorf("写入记忆 %s 的向量: %w", job.MemoryID, err)
		}
		result, err := tx.tx.ExecContext(ctx, `DELETE FROM memory_embedding_jobs
			WHERE memory_id = ? AND profile_id = ? AND content_hash = ?`,
			job.MemoryID, job.ProfileID, job.ContentHash,
		)
		if err != nil {
			return fmt.Errorf("完成向量任务 %s: %w", job.MemoryID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("完成向量任务 %s: %w", job.MemoryID, err)
		}
		committed = affected == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return committed, nil
}

// FailJob conditionally records a sanitized error for the running generation. A
// nil nextAttempt marks the generation failed; otherwise it returns to queued
// for another try.
func (s *Store) FailJob(ctx context.Context, job embedding.Job, code string, nextAttempt *time.Time, now time.Time) error {
	code = sanitizeErrorCode(code)
	status := "failed"
	if nextAttempt != nil {
		status = "queued"
	}
	return s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `UPDATE memory_embedding_jobs SET
			status = ?, error_code = ?, next_attempt_at = ?, updated_at = ?
			WHERE memory_id = ? AND status = 'running' AND profile_id = ? AND content_hash = ?`,
			status, code, timeValue(nextAttempt), formatTime(now),
			job.MemoryID, job.ProfileID, job.ContentHash,
		)
		if err != nil {
			return fmt.Errorf("记录向量任务失败 %s: %w", job.MemoryID, err)
		}
		return nil
	})
}

// RecoverJobs requeues running tasks left by a previous process while keeping
// their attempt counts.
func (s *Store) RecoverJobs(ctx context.Context, profileID string, now time.Time) (int64, error) {
	if profileID == "" {
		return 0, nil
	}
	var count int64
	err := s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		result, err := tx.tx.ExecContext(ctx, `UPDATE memory_embedding_jobs SET
			status = 'queued', next_attempt_at = ?, updated_at = ?
			WHERE profile_id = ? AND status = 'running'`,
			formatTime(now), formatTime(now), profileID,
		)
		if err != nil {
			return fmt.Errorf("恢复向量任务: %w", err)
		}
		count, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("恢复向量任务: %w", err)
		}
		return nil
	})
	return count, err
}

// EnqueueMissingJobs creates or requeues tasks for current documents that lack a
// valid vector for profileID. Queued and running tasks are never reset; failed
// tasks are retried on the next startup scan.
func (s *Store) EnqueueMissingJobs(ctx context.Context, profileID string, now time.Time) (int64, error) {
	if profileID == "" {
		return 0, nil
	}
	var count int64
	err := s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		rows, err := tx.tx.QueryContext(ctx, `SELECT d.memory_id, d.content_hash
			FROM memory_search_docs d
			JOIN memories m ON m.id = d.memory_id
			WHERE m.state <> ? AND d.content_hash <> ''
				AND NOT EXISTS (
					SELECT 1 FROM memory_embeddings e
					WHERE e.memory_id = d.memory_id
						AND e.profile_id = ? AND e.content_hash = d.content_hash
				)
			ORDER BY d.memory_id ASC`, string(memory.StateDeleted), profileID)
		if err != nil {
			return fmt.Errorf("扫描缺失向量: %w", err)
		}
		type missing struct {
			memoryID    string
			contentHash string
		}
		items := []missing{}
		for rows.Next() {
			var item missing
			if err := rows.Scan(&item.memoryID, &item.contentHash); err != nil {
				_ = rows.Close()
				return fmt.Errorf("读取缺失向量: %w", err)
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("读取缺失向量: %w", err)
		}
		_ = rows.Close()

		for _, item := range items {
			result, err := tx.tx.ExecContext(ctx, `INSERT INTO memory_embedding_jobs
				(memory_id, profile_id, content_hash, status, attempts,
				 next_attempt_at, error_code, created_at, updated_at)
				VALUES (?, ?, ?, 'queued', 0, NULL, '', ?, ?)
				ON CONFLICT(memory_id) DO UPDATE SET
					profile_id = excluded.profile_id,
					content_hash = excluded.content_hash,
					status = 'queued',
					attempts = 0,
					next_attempt_at = NULL,
					error_code = '',
					created_at = excluded.created_at,
					updated_at = excluded.updated_at
				WHERE memory_embedding_jobs.profile_id <> excluded.profile_id
					OR memory_embedding_jobs.content_hash <> excluded.content_hash
					OR memory_embedding_jobs.status = 'failed'`,
				item.memoryID, profileID, item.contentHash, formatTime(now), formatTime(now),
			)
			if err != nil {
				return fmt.Errorf("补充向量任务 %s: %w", item.memoryID, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("补充向量任务 %s: %w", item.memoryID, err)
			}
			count += affected
		}
		return nil
	})
	return count, err
}

func sanitizeErrorCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return "internal"
	}
	var builder strings.Builder
	for _, value := range code {
		switch {
		case value >= 'a' && value <= 'z':
			builder.WriteRune(value)
		case value >= '0' && value <= '9':
			builder.WriteRune(value)
		case value == '_' || value == '-':
			builder.WriteRune(value)
		}
		if builder.Len() >= 64 {
			break
		}
	}
	result := builder.String()
	if result == "" {
		return "internal"
	}
	return result
}
