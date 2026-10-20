package sqlite

import (
	"context"
	"fmt"
)

// RebuildSearchIndex rebuilds the external-content FTS index from
// memory_search_docs. The derived documents are not regenerated here; the
// production document builder is wired in P4. The operation is safe to repeat
// and never touches business snapshot/revision tables.
func (s *Store) RebuildSearchIndex(ctx context.Context) error {
	if _, err := s.writer.ExecContext(ctx, "INSERT INTO memory_fts(memory_fts) VALUES('rebuild')"); err != nil {
		return fmt.Errorf("重建搜索索引: %w", err)
	}
	return nil
}
