package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

var (
	_ retrieval.Searcher    = (*Store)(nil)
	_ retrieval.IndexWriter = (*Store)(nil)
)

// Search runs the compiled FTS5 query with structured filters and the fixed
// weighted-BM25/time/ID ordering. The MATCH expression is bound as a parameter
// and is produced only by retrieval.Compile.
func (s *Store) Search(ctx context.Context, query retrieval.Query) ([]retrieval.Hit, error) {
	if strings.TrimSpace(query.Expression) == "" {
		return []retrieval.Hit{}, nil
	}
	states := searchStates(query.Filters.States)
	if len(states) == 0 {
		return []retrieval.Hit{}, nil
	}
	assertions := searchAssertionStatuses(query.Filters.AssertionStatus)

	conditions := []string{"memory_fts MATCH ?"}
	args := []any{
		float64(retrieval.FieldWeightTitle),
		float64(retrieval.FieldWeightContent),
		query.Expression,
	}
	if len(query.Filters.Kinds) > 0 {
		conditions = append(conditions, "m.kind IN ("+placeholders(len(query.Filters.Kinds))+")")
		for _, kind := range query.Filters.Kinds {
			args = append(args, string(kind))
		}
	}
	conditions = append(conditions, "m.state IN ("+placeholders(len(states))+")")
	for _, state := range states {
		args = append(args, string(state))
	}
	conditions = append(conditions, "m.assertion_status IN ("+placeholders(len(assertions))+")")
	for _, status := range assertions {
		args = append(args, string(status))
	}
	if query.Filters.ScopeKey != "" {
		conditions = append(conditions, "(m.scope_key = ? OR m.scope_key = 'user')")
		args = append(args, query.Filters.ScopeKey)
	}
	if query.Filters.AsOf != nil {
		conditions = append(conditions, "(m.valid_from IS NULL OR m.valid_from <= ?)")
		args = append(args, formatTime(*query.Filters.AsOf))
		conditions = append(conditions, "(m.valid_to IS NULL OR ? < m.valid_to)")
		args = append(args, formatTime(*query.Filters.AsOf))
	}
	if query.Filters.CreatedByApp != "" {
		conditions = append(conditions, "m.created_by_app = ?")
		args = append(args, query.Filters.CreatedByApp)
	}
	if query.Filters.UpdatedAfter != nil {
		conditions = append(conditions, "m.updated_at >= ?")
		args = append(args, formatTime(*query.Filters.UpdatedAfter))
	}
	if query.Filters.UpdatedBefore != nil {
		conditions = append(conditions, "m.updated_at <= ?")
		args = append(args, formatTime(*query.Filters.UpdatedBefore))
	}

	topK := query.TopK
	if topK <= 0 {
		topK = retrieval.DefaultTopK
	}
	args = append(args, topK)

	statement := `SELECT m.id, bm25(memory_fts, ?, ?) AS rank
		FROM memory_fts
		JOIN memory_search_docs d ON d.row_id = memory_fts.rowid
		JOIN memories m ON m.id = d.memory_id
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY CASE m.assertion_status WHEN 'accepted' THEN 0 ELSE 1 END,
			rank ASC, m.updated_at DESC, m.id ASC
		LIMIT ?`

	rows, err := s.reader.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("关键词检索: %w", err)
	}
	defer rows.Close()

	hits := []retrieval.Hit{}
	for rows.Next() {
		var hit retrieval.Hit
		if err := rows.Scan(&hit.MemoryID, &hit.BM25); err != nil {
			return nil, fmt.Errorf("读取关键词检索结果: %w", err)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取关键词检索结果: %w", err)
	}
	return hits, nil
}

// RebuildSearchDocuments regenerates every derived document from current memory
// snapshots and rebuilds FTS in one write transaction. Archived memories stay
// indexed; deleted memories are excluded. Rows are updated by memory ID and stale
// documents are deleted individually, so valid vectors are not cascaded away by
// a blanket DELETE. A builder or SQL failure rolls back the whole rebuild, so
// callers never observe partial success.
func (s *Store) RebuildSearchDocuments(ctx context.Context, builder memory.DocumentBuilder) (int, error) {
	if builder == nil {
		return 0, errors.New("重建搜索索引: 搜索文档构造器未配置")
	}
	indexed := 0
	err := s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		items, err := listMemoriesForIndex(ctx, tx)
		if err != nil {
			return err
		}
		for _, item := range items {
			document, err := builder.BuildDocument(item)
			if err != nil {
				return fmt.Errorf("构造记忆 %s 的搜索文档: %w", item.ID, err)
			}
			if document.MemoryID == "" {
				document.MemoryID = item.ID
			}
			if document.MemoryID != item.ID {
				return fmt.Errorf("搜索文档记忆 ID %s 与快照 %s 不一致", document.MemoryID, item.ID)
			}
			if err := tx.UpsertSearchDocument(ctx, document); err != nil {
				return err
			}
			indexed++
		}
		if _, err := tx.tx.ExecContext(ctx, `DELETE FROM memory_search_docs
			WHERE memory_id NOT IN (
				SELECT id FROM memories WHERE state <> ? AND assertion_status <> ?
			)`, string(memory.StateDeleted), string(memory.AssertionRejected)); err != nil {
			return fmt.Errorf("清理无效搜索文档: %w", err)
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO memory_fts(memory_fts) VALUES('rebuild')`); err != nil {
			return fmt.Errorf("重建全文索引: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("重建搜索索引: %w", err)
	}
	return indexed, nil
}

func listMemoriesForIndex(ctx context.Context, tx *Tx) ([]memory.Memory, error) {
	rows, err := tx.tx.QueryContext(ctx, `SELECT `+memoryColumns+`
		FROM memories WHERE state <> ? AND assertion_status <> ?
		ORDER BY id ASC`, string(memory.StateDeleted), string(memory.AssertionRejected))
	if err != nil {
		return nil, fmt.Errorf("读取待重建记忆: %w", err)
	}
	defer rows.Close()

	items := []memory.Memory{}
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("读取待重建记忆: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取待重建记忆: %w", err)
	}
	return items, nil
}

func searchStates(states []memory.State) []memory.State {
	if len(states) == 0 {
		return []memory.State{memory.StateActive}
	}
	seen := make(map[memory.State]struct{}, len(states))
	result := make([]memory.State, 0, len(states))
	for _, state := range states {
		if state != memory.StateActive && state != memory.StateArchived {
			continue
		}
		if _, duplicate := seen[state]; duplicate {
			continue
		}
		seen[state] = struct{}{}
		result = append(result, state)
	}
	return result
}

// RebuildSearchIndex rebuilds only the FTS index from existing derived
// documents. Full production rebuilds use RebuildSearchDocuments so documents
// are also regenerated from business snapshots.
func (s *Store) RebuildSearchIndex(ctx context.Context) error {
	if _, err := s.writer.ExecContext(ctx, "INSERT INTO memory_fts(memory_fts) VALUES('rebuild')"); err != nil {
		return fmt.Errorf("重建搜索索引: %w", err)
	}
	return nil
}

// HasUnhashedSearchDocuments reports whether any current document predates the
// Embedding content hash. Runtime enabling semantic retrieval uses this to run
// one full rebuild after migration instead of rebuilding on every startup.
func (s *Store) HasUnhashedSearchDocuments(ctx context.Context) (bool, error) {
	var present int
	err := s.reader.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM memory_search_docs d
		JOIN memories m ON m.id = d.memory_id
		WHERE m.state <> ? AND m.assertion_status <> ? AND d.content_hash = ''
		LIMIT 1
	)`, string(memory.StateDeleted), string(memory.AssertionRejected)).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("检查搜索文档哈希: %w", err)
	}
	return present == 1, nil
}

const indexVersionKey = "index_version"

// IndexVersion returns the embedding contract version that produced the
// persisted content hashes. An empty value means no marked rebuild has run.
func (s *Store) IndexVersion(ctx context.Context) (string, error) {
	var value string
	err := s.reader.QueryRowContext(ctx,
		`SELECT value FROM memory_index_meta WHERE key = ?`, indexVersionKey,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取索引版本: %w", err)
	}
	return value, nil
}

// SetIndexVersion records the contract version of a completed rebuild.
func (s *Store) SetIndexVersion(ctx context.Context, version string) error {
	return s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO memory_index_meta(key, value)
			VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			indexVersionKey, version,
		); err != nil {
			return fmt.Errorf("记录索引版本: %w", err)
		}
		return nil
	})
}

// searchAssertionStatuses defaults ordinary recall to accepted assertions only.
// Context selection passes accepted+pending explicitly.
func searchAssertionStatuses(statuses []memory.AssertionStatus) []memory.AssertionStatus {
	if len(statuses) == 0 {
		return []memory.AssertionStatus{memory.AssertionAccepted}
	}
	seen := make(map[memory.AssertionStatus]struct{}, len(statuses))
	result := make([]memory.AssertionStatus, 0, len(statuses))
	for _, status := range statuses {
		switch status {
		case memory.AssertionAccepted, memory.AssertionPending, memory.AssertionRejected:
		default:
			continue
		}
		if _, duplicate := seen[status]; duplicate {
			continue
		}
		seen[status] = struct{}{}
		result = append(result, status)
	}
	if len(result) == 0 {
		return []memory.AssertionStatus{memory.AssertionAccepted}
	}
	return result
}
