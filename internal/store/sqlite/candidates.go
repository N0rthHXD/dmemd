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
