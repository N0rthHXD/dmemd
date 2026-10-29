package sqlite

import (
	"container/heap"
	"context"
	"fmt"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

var _ retrieval.VectorSearcher = (*Store)(nil)

// SearchVectors applies the exact cosine scan over filtered current vectors. The
// SQL joins the current snapshot and derived document, rejects profile,
// dimension and content-hash mismatches, then streams decoded vectors into a
// fixed-size heap. No model or long computation happens inside a transaction.
func (s *Store) SearchVectors(ctx context.Context, query retrieval.VectorQuery) ([]retrieval.VectorHit, error) {
	if query.ProfileID == "" || query.Dimensions <= 0 || len(query.Query) != query.Dimensions {
		return []retrieval.VectorHit{}, nil
	}
	states := searchStates(query.Filters.States)
	if len(states) == 0 {
		return []retrieval.VectorHit{}, nil
	}
	assertions := searchAssertionStatuses(query.Filters.AssertionStatus)
	limit := query.Limit
	if limit <= 0 {
		limit = retrieval.CandidateLimitMin
	}

	conditions := []string{
		"e.profile_id = ?",
		"e.dimensions = ?",
		"e.content_hash = d.content_hash",
	}
	args := []any{query.ProfileID, query.Dimensions}
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
		// The semantic branch must honor exactly the same scope boundary as the
		// keyword branch; a cross-scope vector must never consume its budget.
		conditions = append(conditions, "(m.scope_key = ? OR m.scope_key = 'user')")
		args = append(args, query.Filters.ScopeKey)
	}
	// As-of and event-at filtering belongs to a future temporal annotation task.
	if query.Filters.UpdatedAfter != nil {
		conditions = append(conditions, "m.updated_at >= ?")
		args = append(args, formatTime(*query.Filters.UpdatedAfter))
	}
	if query.Filters.UpdatedBefore != nil {
		conditions = append(conditions, "m.updated_at <= ?")
		args = append(args, formatTime(*query.Filters.UpdatedBefore))
	}

	statement := `SELECT e.memory_id, e.content_hash, e.vector, m.updated_at
		FROM memory_embeddings e
		JOIN memory_search_docs d ON d.memory_id = e.memory_id
		JOIN memories m ON m.id = e.memory_id
		WHERE ` + strings.Join(conditions, " AND ")

	rows, err := s.reader.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("语义检索: %w", err)
	}
	defer rows.Close()

	candidates := &vectorCandidateHeap{}
	heap.Init(candidates)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var memoryID, contentHash, updatedAt string
		var encoded []byte
		if err := rows.Scan(&memoryID, &contentHash, &encoded, &updatedAt); err != nil {
			return nil, fmt.Errorf("读取语义检索结果: %w", err)
		}
		vector, decodeErr := embedding.DecodeVector(encoded, query.Dimensions)
		if decodeErr != nil {
			// A malformed BLOB cannot be used as a candidate; storage
			// constraints should make this unreachable.
			continue
		}
		cosine, dotErr := embedding.DotProduct(query.Query, vector)
		if dotErr != nil || cosine < query.MinSimilarity {
			continue
		}
		updated, parseErr := parseTime(updatedAt)
		if parseErr != nil {
			return nil, parseErr
		}
		candidate := retrieval.VectorHit{
			MemoryID:    memoryID,
			ContentHash: contentHash,
			Cosine:      cosine,
			UpdatedAt:   updated,
		}
		if candidates.Len() < limit {
			heap.Push(candidates, candidate)
			continue
		}
		if vectorHitBetter(candidate, (*candidates)[0]) {
			heap.Pop(candidates)
			heap.Push(candidates, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取语义检索结果: %w", err)
	}

	result := make([]retrieval.VectorHit, candidates.Len())
	for index := len(result) - 1; index >= 0; index-- {
		result[index] = heap.Pop(candidates).(retrieval.VectorHit)
	}
	return result, nil
}

func vectorHitBetter(left, right retrieval.VectorHit) bool {
	if left.Cosine != right.Cosine {
		return left.Cosine > right.Cosine
	}
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.After(right.UpdatedAt)
	}
	return left.MemoryID < right.MemoryID
}

// vectorCandidateHeap keeps the worst of the best candidates at the root so a
// streaming scan only retains Limit entries.
type vectorCandidateHeap []retrieval.VectorHit

func (h vectorCandidateHeap) Len() int { return len(h) }

func (h vectorCandidateHeap) Less(i, j int) bool {
	return vectorHitBetter(h[j], h[i])
}

func (h vectorCandidateHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *vectorCandidateHeap) Push(value any) {
	*h = append(*h, value.(retrieval.VectorHit))
}

func (h *vectorCandidateHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}
