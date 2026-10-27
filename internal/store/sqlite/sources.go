package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const sourceColumns = `id, app_id, external_id, scope_key, content, redacted, redacted_at, created_at`

// GetSource reads a source directly from the read pool.
func (s *Store) GetSource(ctx context.Context, id string) (source.Source, error) {
	return getSource(ctx, s.reader, id)
}

func (t *Tx) GetSource(ctx context.Context, id string) (source.Source, error) {
	return getSource(ctx, t.tx, id)
}

func getSource(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (source.Source, error) {
	item, err := scanSource(q.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM sources WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return source.Source{}, fmt.Errorf("%w: %s", source.ErrNotFound, id)
	}
	if err != nil {
		return source.Source{}, fmt.Errorf("读取来源 %s: %w", id, err)
	}
	return item, nil
}

func scanSource(row rowScanner) (source.Source, error) {
	var item source.Source
	var content []byte
	var redacted int
	var redactedAt sql.NullString
	var createdAt string
	if err := row.Scan(
		&item.ID, &item.AppID, &item.ExternalID, &item.ScopeKey, &content, &redacted, &redactedAt, &createdAt,
	); err != nil {
		return source.Source{}, err
	}
	if content != nil {
		item.Content = append([]byte(nil), content...)
	}
	item.Redacted = redacted != 0
	var err error
	if item.RedactedAt, err = nullableTime(redactedAt); err != nil {
		return source.Source{}, err
	}
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return source.Source{}, err
	}
	return item, nil
}

func (t *Tx) InsertSource(ctx context.Context, item source.Source) error {
	content := any(nil)
	if !item.Redacted && item.Content != nil {
		content = []byte(item.Content)
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO sources(`+sourceColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.AppID, item.ExternalID, item.ScopeKey, content, boolInt(item.Redacted),
		timeValue(item.RedactedAt), formatTime(item.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("写入来源 %s: %w", item.ID, err)
	}
	return nil
}

func (t *Tx) RedactSource(ctx context.Context, id string, at time.Time) error {
	result, err := t.tx.ExecContext(ctx, `UPDATE sources SET
		content = NULL,
		redacted = 1,
		redacted_at = COALESCE(redacted_at, ?)
		WHERE id = ?`, formatTime(at), id)
	if err != nil {
		return fmt.Errorf("清除来源 %s 原文: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("清除来源 %s 原文: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", source.ErrNotFound, id)
	}
	return nil
}

const evidenceColumns = `id, source_id, message_id, quote, location_present,
	location_field, location_start, location_end, model_name, redacted, redacted_at, created_at`

func (t *Tx) InsertEvidence(ctx context.Context, item source.Evidence) error {
	quote := any(nil)
	if !item.Redacted {
		quote = item.Quote
	}
	locationPresent := 0
	var field, start, end any
	if !item.Redacted && item.Location != nil {
		locationPresent = 1
		field = nullableString(item.Location.Field)
		start = item.Location.Start
		end = item.Location.End
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO evidence(`+evidenceColumns+`) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.SourceID, item.MessageID, quote, locationPresent, field, start, end,
		item.ModelName, boolInt(item.Redacted), timeValue(item.RedactedAt), formatTime(item.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("写入证据 %s: %w", item.ID, err)
	}
	return nil
}

func (t *Tx) GetEvidence(ctx context.Context, id string) (source.Evidence, error) {
	item, err := scanEvidence(t.tx.QueryRowContext(ctx, `SELECT `+evidenceColumns+` FROM evidence WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return source.Evidence{}, fmt.Errorf("%w: %s", source.ErrNotFound, id)
	}
	if err != nil {
		return source.Evidence{}, fmt.Errorf("读取证据 %s: %w", id, err)
	}
	return item, nil
}

func (t *Tx) ListEvidenceBySource(ctx context.Context, sourceID string) ([]source.Evidence, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT `+evidenceColumns+` FROM evidence
		WHERE source_id = ? ORDER BY created_at ASC, id ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("读取来源证据 %s: %w", sourceID, err)
	}
	defer rows.Close()
	return scanEvidenceRows(rows)
}

func (t *Tx) RedactEvidenceBySource(ctx context.Context, sourceID string, at time.Time) error {
	if _, err := t.tx.ExecContext(ctx, `UPDATE evidence SET
		quote = NULL,
		location_present = 0,
		location_field = NULL,
		location_start = NULL,
		location_end = NULL,
		redacted = 1,
		redacted_at = COALESCE(redacted_at, ?)
		WHERE source_id = ? AND redacted = 0`, formatTime(at), sourceID); err != nil {
		return fmt.Errorf("清除来源 %s 的证据副本: %w", sourceID, err)
	}
	return nil
}

func scanEvidence(row rowScanner) (source.Evidence, error) {
	var item source.Evidence
	var quote sql.NullString
	var locationPresent int
	var field sql.NullString
	var start, end sql.NullInt64
	var redacted int
	var redactedAt sql.NullString
	var createdAt string
	if err := row.Scan(
		&item.ID, &item.SourceID, &item.MessageID, &quote, &locationPresent,
		&field, &start, &end, &item.ModelName, &redacted, &redactedAt, &createdAt,
	); err != nil {
		return source.Evidence{}, err
	}
	if quote.Valid {
		item.Quote = quote.String
	}
	if locationPresent != 0 {
		location := &source.Location{}
		if field.Valid {
			location.Field = field.String
		}
		if start.Valid {
			location.Start = int(start.Int64)
		}
		if end.Valid {
			location.End = int(end.Int64)
		}
		item.Location = location
	}
	item.Redacted = redacted != 0
	var err error
	if item.RedactedAt, err = nullableTime(redactedAt); err != nil {
		return source.Evidence{}, err
	}
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return source.Evidence{}, err
	}
	return item, nil
}

func scanEvidenceRows(rows *sql.Rows) ([]source.Evidence, error) {
	items := []source.Evidence{}
	for rows.Next() {
		item, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// ScrubJobErrorsBySource replaces non-empty terminal job error summaries for a
// redacted source. Status, attempts and result IDs are preserved; only text
// that could retain original content is replaced.
func (t *Tx) ScrubJobErrorsBySource(ctx context.Context, sourceID, reason string, at time.Time) (int64, error) {
	result, err := t.tx.ExecContext(ctx, `UPDATE ingest_jobs SET error = ?, updated_at = ?
		WHERE source_id = ? AND error <> '' AND error <> ? AND status IN (?, ?, ?)`,
		reason, formatTime(at), sourceID, reason,
		string(ingest.JobSucceeded), string(ingest.JobFailed), string(ingest.JobCancelled),
	)
	if err != nil {
		return 0, fmt.Errorf("清理来源 %s 的任务错误摘要: %w", sourceID, err)
	}
	scrubbed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("清理来源 %s 的任务错误摘要: %w", sourceID, err)
	}
	return scrubbed, nil
}

// CancelJobsBySource moves queued/running/waiting jobs for a source to cancelled
// while preserving attempts. Terminal fields other than a scrubbed error are
// untouched.
func (t *Tx) CancelJobsBySource(ctx context.Context, sourceID, reason string, at time.Time) (int64, error) {
	result, err := t.tx.ExecContext(ctx, `UPDATE ingest_jobs SET
		status = ?, error = ?, next_attempt_at = NULL, quality_outcome = '',
		extraction_stats = '{}', diagnostics = '[]', updated_at = ?, finished_at = ?
		WHERE source_id = ? AND status IN (?, ?, ?)`,
		string(ingest.JobCancelled), reason, formatTime(at), formatTime(at),
		sourceID, string(ingest.JobQueued), string(ingest.JobRunning), string(ingest.JobWaiting),
	)
	if err != nil {
		return 0, fmt.Errorf("取消来源 %s 的任务: %w", sourceID, err)
	}
	cancelled, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("取消来源 %s 的任务: %w", sourceID, err)
	}
	return cancelled, nil
}
