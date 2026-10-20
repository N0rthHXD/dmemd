package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

var (
	_ retrieval.Searcher    = (*Store)(nil)
	_ retrieval.Reader      = (*Store)(nil)
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
		ORDER BY rank ASC, m.updated_at DESC, m.id ASC
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
// indexed; deleted memories are excluded. A builder or SQL failure rolls back
// the whole rebuild, so callers never observe partial success.
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
		if _, err := tx.tx.ExecContext(ctx, `DELETE FROM memory_search_docs`); err != nil {
			return fmt.Errorf("清空派生搜索文档: %w", err)
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
			if document.State == "" {
				document.State = item.State
			}
			if document.UpdatedAt.IsZero() {
				document.UpdatedAt = item.UpdatedAt
			}
			if err := tx.UpsertSearchDocument(ctx, document); err != nil {
				return err
			}
			indexed++
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
		FROM memories WHERE state <> ? ORDER BY id ASC`, string(memory.StateDeleted))
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
