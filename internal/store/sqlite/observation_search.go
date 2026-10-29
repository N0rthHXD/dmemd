package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

// SearchObservations runs the compiled L0 keyword query. Only observed and
// pending observations are eligible; retracted and deleted rows never enter
// default recall.
func (s *Store) SearchObservations(ctx context.Context, query retrieval.ObservationQuery) ([]retrieval.ObservationHit, error) {
	if strings.TrimSpace(query.Expression) == "" {
		return []retrieval.ObservationHit{}, nil
	}
	statuses := observationSearchStatuses(query.Filters.ObservationStatuses)
	if len(statuses) == 0 {
		return []retrieval.ObservationHit{}, nil
	}
	conditions := []string{"observation_fts MATCH ?"}
	args := []any{1.0, query.Expression}
	conditions = append(conditions, "o.status IN ("+placeholders(len(statuses))+")")
	for _, status := range statuses {
		args = append(args, string(status))
	}
	if len(query.Filters.Kinds) > 0 {
		conditions = append(conditions, "o.kind IN ("+placeholders(len(query.Filters.Kinds))+")")
		for _, kind := range query.Filters.Kinds {
			args = append(args, string(kind))
		}
	}
	if query.Filters.ScopeKey != "" {
		conditions = append(conditions, "(o.scope_key = ? OR o.scope_key = 'user')")
		args = append(args, query.Filters.ScopeKey)
	}
	limit := query.TopK
	if limit <= 0 {
		limit = retrieval.DefaultTopK
	}
	args = append(args, limit)
	statement := `SELECT o.id, bm25(observation_fts, ?) AS rank
		FROM observation_fts
		JOIN observation_search_docs d ON d.row_id = observation_fts.rowid
		JOIN observations o ON o.id = d.observation_id
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY rank ASC, o.updated_at DESC, o.id ASC
		LIMIT ?`
	rows, err := s.reader.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("观察关键词检索: %w", err)
	}
	defer rows.Close()

	type ranked struct {
		id    string
		score float64
	}
	hits := []ranked{}
	for rows.Next() {
		var item ranked
		var rank float64
		if err := rows.Scan(&item.id, &rank); err != nil {
			return nil, fmt.Errorf("读取观察检索结果: %w", err)
		}
		item.score = -rank
		hits = append(hits, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取观察检索结果: %w", err)
	}

	result := make([]retrieval.ObservationHit, 0, len(hits))
	for _, hit := range hits {
		item, err := getObservation(ctx, s.reader, hit.id)
		if err != nil {
			return nil, err
		}
		refs, err := listObservationEvidenceRefs(ctx, s.reader, hit.id)
		if err != nil {
			return nil, err
		}
		result = append(result, retrieval.ObservationHit{
			Observation:  item,
			Score:        hit.score,
			EvidenceRefs: refs,
		})
	}
	return result, nil
}

func observationSearchStatuses(input []observation.Status) []observation.Status {
	if len(input) == 0 {
		return []observation.Status{observation.StatusObserved}
	}
	seen := make(map[observation.Status]struct{}, len(input))
	result := make([]observation.Status, 0, len(input))
	for _, status := range input {
		if status != observation.StatusObserved && status != observation.StatusPending {
			continue
		}
		if _, duplicate := seen[status]; duplicate {
			continue
		}
		seen[status] = struct{}{}
		result = append(result, status)
	}
	return result
}

func listObservationEvidenceRefs(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, observationID string) ([]retrieval.EvidenceRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.source_id
		FROM evidence e
		JOIN observation_evidence oe ON oe.evidence_id = e.id
		WHERE oe.observation_id = ?
		ORDER BY e.created_at ASC, e.id ASC`, observationID)
	if err != nil {
		return nil, fmt.Errorf("读取观察 %s 的证据引用: %w", observationID, err)
	}
	defer rows.Close()
	refs := []retrieval.EvidenceRef{}
	for rows.Next() {
		var ref retrieval.EvidenceRef
		if err := rows.Scan(&ref.ID, &ref.SourceID); err != nil {
			return nil, fmt.Errorf("读取观察 %s 的证据引用: %w", observationID, err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取观察 %s 的证据引用: %w", observationID, err)
	}
	return refs, nil
}

func upsertObservationSearchDocument(ctx context.Context, tx *sql.Tx, item observation.Observation) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO observation_search_docs(observation_id, content_terms)
		VALUES (?, ?)
		ON CONFLICT(observation_id) DO UPDATE SET content_terms = excluded.content_terms`,
		item.ID, retrieval.ObservationIndexText(item.Content))
	if err != nil {
		return fmt.Errorf("写入观察搜索文档 %s: %w", item.ID, err)
	}
	return nil
}

func rebuildObservationSearchDocuments(ctx context.Context, tx *Tx) error {
	rows, err := tx.tx.QueryContext(ctx, `SELECT `+observationColumns+`
		FROM observations WHERE status <> ? ORDER BY id ASC`, string(observation.StatusDeleted))
	if err != nil {
		return fmt.Errorf("读取待重建观察: %w", err)
	}
	defer rows.Close()
	items := []observation.Observation{}
	for rows.Next() {
		item, err := scanObservation(rows)
		if err != nil {
			return fmt.Errorf("读取待重建观察: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("读取待重建观察: %w", err)
	}
	for _, item := range items {
		if err := upsertObservationSearchDocument(ctx, tx.tx, item); err != nil {
			return err
		}
	}
	if _, err := tx.tx.ExecContext(ctx, `DELETE FROM observation_search_docs
		WHERE observation_id NOT IN (SELECT id FROM observations WHERE status <> ?)`,
		string(observation.StatusDeleted)); err != nil {
		return fmt.Errorf("清理无效观察搜索文档: %w", err)
	}
	if _, err := tx.tx.ExecContext(ctx, `INSERT INTO observation_fts(observation_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("重建观察全文索引: %w", err)
	}
	return nil
}
