package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const observationColumns = `id, source_id, scope_key, kind, content, content_mode,
	origin_role, status, extraction_version, created_at, updated_at`

// GetObservation reads one L0 observation from the read pool.
func (s *Store) GetObservation(ctx context.Context, id string) (observation.Observation, error) {
	return getObservation(ctx, s.reader, id)
}

func (t *Tx) GetObservation(ctx context.Context, id string) (observation.Observation, error) {
	return getObservation(ctx, t.tx, id)
}

func getObservation(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (observation.Observation, error) {
	item, err := scanObservation(q.QueryRowContext(ctx,
		`SELECT `+observationColumns+` FROM observations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return observation.Observation{}, fmt.Errorf("%w: %s", observation.ErrNotFound, id)
	}
	if err != nil {
		return observation.Observation{}, fmt.Errorf("读取观察 %s: %w", id, err)
	}
	return item, nil
}

// ListObservations returns a bounded filtered L0 page. Deleted rows are only
// visible when explicitly requested through the status filter.
func (s *Store) ListObservations(ctx context.Context, options observation.ListOptions) ([]observation.Observation, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	conditions := []string{}
	args := []any{}
	if sourceID := strings.TrimSpace(options.SourceID); sourceID != "" {
		conditions = append(conditions, "source_id = ?")
		args = append(args, sourceID)
	}
	if scopeKey := strings.TrimSpace(options.ScopeKey); scopeKey != "" {
		conditions = append(conditions, "scope_key = ?")
		args = append(args, scopeKey)
	}
	if options.Kind != "" {
		conditions = append(conditions, "kind = ?")
		args = append(args, string(options.Kind))
	}
	if options.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(options.Status))
	}
	query := `SELECT ` + observationColumns + ` FROM observations`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("列出观察: %w", err)
	}
	defer rows.Close()
	items := []observation.Observation{}
	for rows.Next() {
		item, err := scanObservation(rows)
		if err != nil {
			return nil, fmt.Errorf("列出观察: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("列出观察: %w", err)
	}
	return items, nil
}

// ListObservationsBySource returns all L0 observations for one source in
// deterministic creation order.
func (s *Store) ListObservationsBySource(ctx context.Context, sourceID string) ([]observation.Observation, error) {
	return listObservationsBySource(ctx, s.reader, sourceID)
}

func (t *Tx) ListObservationsBySource(ctx context.Context, sourceID string) ([]observation.Observation, error) {
	return listObservationsBySource(ctx, t.tx, sourceID)
}

func listObservationsBySource(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, sourceID string) ([]observation.Observation, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+observationColumns+` FROM observations
		WHERE source_id = ? ORDER BY created_at ASC, id ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("读取来源 %s 的观察: %w", sourceID, err)
	}
	defer rows.Close()
	items := []observation.Observation{}
	for rows.Next() {
		item, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取来源 %s 的观察: %w", sourceID, err)
	}
	return items, nil
}

func scanObservation(row rowScanner) (observation.Observation, error) {
	var item observation.Observation
	var kind, mode, status, createdAt, updatedAt string
	if err := row.Scan(
		&item.ID, &item.SourceID, &item.ScopeKey, &kind, &item.Content, &mode,
		&item.OriginRole, &status, &item.ExtractionVersion, &createdAt, &updatedAt,
	); err != nil {
		return observation.Observation{}, err
	}
	item.Kind = observation.Kind(kind)
	item.ContentMode = observation.ContentMode(mode)
	item.Status = observation.Status(status)
	var err error
	if item.CreatedAt, err = parseTime(createdAt); err != nil {
		return observation.Observation{}, err
	}
	if item.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return observation.Observation{}, err
	}
	return item, nil
}

// InsertObservation persists one L0 observation. It never updates an existing
// row: observations are immutable source-derived records and corrections belong
// to governance questions, not an in-place edit.
func (t *Tx) InsertObservation(ctx context.Context, item observation.Observation) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO observations(`+observationColumns+`) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.SourceID, item.ScopeKey, string(item.Kind), item.Content,
		string(item.ContentMode), item.OriginRole, string(item.Status),
		item.ExtractionVersion, formatTime(item.CreatedAt), formatTime(item.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("写入观察 %s: %w", item.ID, err)
	}
	return upsertObservationSearchDocument(ctx, t.tx, item)
}

// UpdateObservationStatus applies one lifecycle transition without touching the
// observation body. Deleted and retracted rows leave the default observation
// index immediately; pending and observed rows remain searchable.
func (t *Tx) UpdateObservationStatus(ctx context.Context, id string, status observation.Status, now time.Time) error {
	if !status.Known() {
		return fmt.Errorf("observation: invalid status %q", status)
	}
	current, err := getObservation(ctx, t.tx, id)
	if err != nil {
		return err
	}
	now = now.UTC()
	if current.Status != status {
		if _, err := t.tx.ExecContext(ctx, `UPDATE observations SET status = ?, updated_at = ?
			WHERE id = ?`, string(status), formatTime(now), id); err != nil {
			return fmt.Errorf("更新观察 %s 状态: %w", id, err)
		}
		current.Status = status
		current.UpdatedAt = now
	}
	if status == observation.StatusDeleted || status == observation.StatusRetracted {
		if _, err := t.tx.ExecContext(ctx,
			`DELETE FROM observation_search_docs WHERE observation_id = ?`, id); err != nil {
			return fmt.Errorf("移除观察 %s 搜索文档: %w", id, err)
		}
		return nil
	}
	return upsertObservationSearchDocument(ctx, t.tx, current)
}

// InsertObservationEvidence links one observation to one code-generated
// evidence row. Repeating the exact link is idempotent.
func (t *Tx) InsertObservationEvidence(ctx context.Context, observationID, evidenceID string) error {
	_, err := t.tx.ExecContext(ctx, `INSERT OR IGNORE INTO observation_evidence(observation_id, evidence_id)
		VALUES (?, ?)`, observationID, evidenceID)
	if err != nil {
		return fmt.Errorf("关联观察 %s 与证据 %s: %w", observationID, evidenceID, err)
	}
	return nil
}

// ListObservationEvidence reads the evidence linked to one observation.
func (s *Store) ListObservationEvidence(ctx context.Context, observationID string) ([]source.Evidence, error) {
	return listObservationEvidence(ctx, s.reader, observationID)
}

func (t *Tx) ListObservationEvidence(ctx context.Context, observationID string) ([]source.Evidence, error) {
	return listObservationEvidence(ctx, t.tx, observationID)
}

func listObservationEvidence(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, observationID string) ([]source.Evidence, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+evidenceColumns+` FROM evidence e
		JOIN observation_evidence oe ON oe.evidence_id = e.id
		WHERE oe.observation_id = ?
		ORDER BY e.created_at ASC, e.id ASC`, observationID)
	if err != nil {
		return nil, fmt.Errorf("读取观察 %s 的证据: %w", observationID, err)
	}
	defer rows.Close()
	return scanEvidenceRows(rows)
}
