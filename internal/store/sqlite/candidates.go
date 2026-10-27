package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

var _ retrieval.CandidateReader = (*Store)(nil)

// LoadSearchCandidates reads every current snapshot and derived content hash in
// one short read transaction. Missing or purged IDs are simply absent from the
// result.
func (s *Store) LoadSearchCandidates(ctx context.Context, ids []string) (map[string]retrieval.Candidate, error) {
	result := make(map[string]retrieval.Candidate, len(ids))
	unique := uniqueIDs(ids)
	if len(unique) == 0 {
		return result, nil
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("开始检索快照事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT `+memoryColumns+`
		FROM memories WHERE id IN (`+placeholders(len(unique))+`)`, stringsToAny(unique)...)
	if err != nil {
		return nil, fmt.Errorf("读取检索候选快照: %w", err)
	}
	for rows.Next() {
		item, scanErr := scanMemory(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("读取检索候选快照: %w", scanErr)
		}
		result[item.ID] = retrieval.Candidate{Memory: item}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("读取检索候选快照: %w", err)
	}
	_ = rows.Close()

	hashRows, err := tx.QueryContext(ctx, `SELECT memory_id, content_hash
		FROM memory_search_docs WHERE memory_id IN (`+placeholders(len(unique))+`)`,
		stringsToAny(unique)...)
	if err != nil {
		return nil, fmt.Errorf("读取检索候选内容哈希: %w", err)
	}
	defer hashRows.Close()
	for hashRows.Next() {
		var memoryID, contentHash string
		if err := hashRows.Scan(&memoryID, &contentHash); err != nil {
			return nil, fmt.Errorf("读取检索候选内容哈希: %w", err)
		}
		candidate, ok := result[memoryID]
		if !ok {
			continue
		}
		candidate.ContentHash = contentHash
		result[memoryID] = candidate
	}
	if err := hashRows.Err(); err != nil {
		return nil, fmt.Errorf("读取检索候选内容哈希: %w", err)
	}
	return result, nil
}

// LoadSearchAssemblies reads the final Top-K current snapshots and complete
// evidence sets in one short read transaction. A memory whose version changed
// after candidate selection is marked not current so the caller never pairs a
// stale snapshot with newer evidence.
func (s *Store) LoadSearchAssemblies(ctx context.Context, keys []retrieval.VersionKey) (map[retrieval.VersionKey]retrieval.Assembly, error) {
	result := make(map[retrieval.VersionKey]retrieval.Assembly, len(keys))
	if len(keys) == 0 {
		return result, nil
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("开始检索证据事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	seen := make(map[retrieval.VersionKey]struct{}, len(keys))
	for _, key := range keys {
		if key.MemoryID == "" || key.Version <= 0 {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		item, err := getMemory(ctx, tx, key.MemoryID)
		if errors.Is(err, memory.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if item.Version != key.Version {
			continue
		}
		evidence, err := listRevisionEvidence(ctx, tx, key.MemoryID, key.Version)
		if err != nil {
			return nil, err
		}
		result[key] = retrieval.Assembly{
			Memory:   item,
			Evidence: evidence,
			Current:  true,
		}
	}
	return result, nil
}

// LoadFactGroupMembers loads a bounded, per-group set of current candidates for
// same-fact association expansion. Deleted tombstones are excluded; every other
// filter is applied by the retrieval layer so diagnostics and the actual result
// share one filter predicate.
func (s *Store) LoadFactGroupMembers(ctx context.Context, groupIDs []string, limitPerGroup int) (map[string][]retrieval.Candidate, error) {
	result := make(map[string][]retrieval.Candidate, len(groupIDs))
	unique := uniqueIDs(groupIDs)
	if len(unique) == 0 {
		return result, nil
	}
	if limitPerGroup <= 0 {
		limitPerGroup = 5
	}
	args := stringsToAny(unique)
	args = append(args, string(memory.StateDeleted), limitPerGroup)
	rows, err := s.reader.QueryContext(ctx, `SELECT `+memoryColumns+` FROM (
		SELECT `+memoryColumns+`, ROW_NUMBER() OVER (
			PARTITION BY fact_group_id ORDER BY updated_at DESC, id ASC
		) AS association_rank
		FROM memories
		WHERE fact_group_id IN (`+placeholders(len(unique))+`) AND state <> ?
	) WHERE association_rank <= ?`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("读取事实组关联候选: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		item, scanErr := scanMemory(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("读取事实组关联候选: %w", scanErr)
		}
		candidate := retrieval.Candidate{Memory: item}
		result[item.FactGroupID] = append(result[item.FactGroupID], candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取事实组关联候选: %w", err)
	}
	return result, nil
}

// CurrentVectorCount reports eligible current documents and vectors that match
// the exact profile, dimensions and content hash. It is used only for
// degradation diagnostics, never to widen a search.
func (s *Store) CurrentVectorCount(ctx context.Context, profileID string, dimensions int) (int, int, error) {
	if profileID == "" || dimensions <= 0 {
		return 0, 0, nil
	}
	var documents, current int
	err := s.reader.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM memory_search_docs d
		 JOIN memories m ON m.id = d.memory_id
		 WHERE m.state <> ? AND m.assertion_status <> ? AND d.content_hash <> ''),
		(SELECT count(*) FROM memory_embeddings e
		 JOIN memory_search_docs d ON d.memory_id = e.memory_id
		 JOIN memories m ON m.id = e.memory_id
		 WHERE m.state <> ? AND m.assertion_status <> ?
		   AND e.profile_id = ? AND e.dimensions = ?
		   AND e.content_hash = d.content_hash)`,
		string(memory.StateDeleted), string(memory.AssertionRejected),
		string(memory.StateDeleted), string(memory.AssertionRejected),
		profileID, dimensions,
	).Scan(&documents, &current)
	if err != nil {
		return 0, 0, fmt.Errorf("读取语义索引就绪状态: %w", err)
	}
	return documents, current, nil
}

// ListRelationsForMemory is the read-pool relation lookup used by memory
// details.
func (s *Store) ListRelationsForMemory(ctx context.Context, memoryID string) ([]memory.Relation, error) {
	return listRelationsForMemory(ctx, s.reader, memoryID)
}

// OpenConflictsForMemories is the read-pool conflict discovery capability used
// by recall. It deliberately has no as-of filter; the retrieval layer decides
// whether a conflict's known time range touches the query instant.
func (s *Store) OpenConflictsForMemories(ctx context.Context, memoryIDs []string) ([]memory.Conflict, error) {
	return listOpenConflictsForMemories(ctx, s.reader, memoryIDs)
}

// CatalogGeneration is the read-pool semantic catalog version.
func (s *Store) CatalogGeneration(ctx context.Context) (int64, error) {
	return s.GetCatalogGeneration(ctx)
}

// FactTimeline reads all non-deleted members of a group, bounded by limit+1 so
// callers can detect truncation.
func (s *Store) FactTimeline(ctx context.Context, groupID string, limit int) ([]memory.Memory, error) {
	if groupID == "" {
		return []memory.Memory{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memories
		WHERE fact_group_id = ? AND state <> ?
		ORDER BY valid_from ASC, updated_at ASC, id ASC LIMIT ?`,
		groupID, string(memory.StateDeleted), limit+1)
	if err != nil {
		return nil, fmt.Errorf("读取事实组时间线 %s: %w", groupID, err)
	}
	defer rows.Close()
	items := []memory.Memory{}
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("读取事实组时间线 %s: %w", groupID, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取事实组时间线 %s: %w", groupID, err)
	}
	return items, nil
}

func uniqueIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func stringsToAny(values []string) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}
