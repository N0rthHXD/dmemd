package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

const (
	embeddingProfileKey       = "embedding_profile_id"
	embeddingIndexVersionKey  = "embedding_index_version"
	embeddingRebuildKey       = "embedding_rebuild_required"
	embeddingRebuildReasonKey = "embedding_rebuild_reason"
	embeddingRebuildAtKey     = "embedding_rebuild_registered_at"
)

var _ embedding.StatsReader = (*Store)(nil)

// RegisterEmbeddingConfiguration persists the active credential-free profile
// and document contract. A change records a durable rebuild reason; it never
// deletes vectors or memory content.
func (s *Store) RegisterEmbeddingConfiguration(ctx context.Context, profileID, indexVersion string, now time.Time) (embedding.Registration, error) {
	registration := embedding.Registration{RegisteredAt: now.UTC()}
	if profileID == "" {
		return registration, errors.New("记录 Embedding 配置: profile 不能为空")
	}
	err := s.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		previousProfile, err := indexMetaValue(ctx, tx.tx, embeddingProfileKey)
		if err != nil {
			return err
		}
		previousVersion, err := indexMetaValue(ctx, tx.tx, embeddingIndexVersionKey)
		if err != nil {
			return err
		}
		var documents int
		if err := tx.tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_search_docs d
			JOIN memories m ON m.id = d.memory_id
			WHERE m.state <> ? AND m.assertion_status <> ? AND d.content_hash <> ''`,
			string(memory.StateDeleted), string(memory.AssertionRejected)).Scan(&documents); err != nil {
			return fmt.Errorf("统计 Embedding 文档: %w", err)
		}

		reason := ""
		if previousProfile != profileID {
			registration.ProfileChanged = true
			if previousProfile == "" {
				if documents > 0 {
					reason = "profile_registered"
				}
			} else {
				reason = "profile_changed"
			}
		}
		if previousVersion != indexVersion {
			registration.IndexVersionChanged = true
			if reason == "" {
				reason = "index_version_changed"
			}
		}
		if reason != "" && documents > 0 {
			if err := setIndexMetaValue(ctx, tx.tx, embeddingRebuildKey, "1"); err != nil {
				return err
			}
			if err := setIndexMetaValue(ctx, tx.tx, embeddingRebuildReasonKey, reason); err != nil {
				return err
			}
			if err := setIndexMetaValue(ctx, tx.tx, embeddingRebuildAtKey, formatTime(now)); err != nil {
				return err
			}
		}
		if err := setIndexMetaValue(ctx, tx.tx, embeddingProfileKey, profileID); err != nil {
			return err
		}
		if err := setIndexMetaValue(ctx, tx.tx, embeddingIndexVersionKey, indexVersion); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return embedding.Registration{}, err
	}
	return registration, nil
}

// EmbeddingStats reports coverage for the exact current profile and document
// hash. Stale vectors remain visible as a rebuild signal but never enter recall.
func (s *Store) EmbeddingStats(ctx context.Context, profileID string, dimensions int) (embedding.Stats, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return embedding.Stats{}, fmt.Errorf("开始 Embedding 状态事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stats := embedding.Stats{
		FailedCodes:     map[string]int{},
		IndexedProfiles: map[string]int{},
	}
	stats.RegisteredProfileID, err = indexMetaValue(ctx, tx, embeddingProfileKey)
	if err != nil {
		return embedding.Stats{}, err
	}
	stats.RegisteredIndexVersion, err = indexMetaValue(ctx, tx, embeddingIndexVersionKey)
	if err != nil {
		return embedding.Stats{}, err
	}
	rebuildRequired, err := indexMetaValue(ctx, tx, embeddingRebuildKey)
	if err != nil {
		return embedding.Stats{}, err
	}
	stats.RebuildRequired = rebuildRequired == "1"
	if stats.RebuildRequired {
		stats.RebuildReason, err = indexMetaValue(ctx, tx, embeddingRebuildReasonKey)
		if err != nil {
			return embedding.Stats{}, err
		}
		registeredAt, err := indexMetaValue(ctx, tx, embeddingRebuildAtKey)
		if err != nil {
			return embedding.Stats{}, err
		}
		if registeredAt != "" {
			parsed, parseErr := parseTime(registeredAt)
			if parseErr != nil {
				return embedding.Stats{}, parseErr
			}
			stats.RebuildRegisteredAt = &parsed
		}
	}

	if profileID == "" || dimensions <= 0 {
		return stats, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM memory_search_docs d
		 JOIN memories m ON m.id = d.memory_id
		 WHERE m.state <> ? AND m.assertion_status <> ? AND d.content_hash <> ''),
		(SELECT count(*) FROM memory_embeddings e
		 JOIN memory_search_docs d ON d.memory_id = e.memory_id
		 JOIN memories m ON m.id = e.memory_id
		 WHERE m.state <> ? AND m.assertion_status <> ?
		   AND e.profile_id = ? AND e.dimensions = ?
		   AND e.content_hash = d.content_hash),
		(SELECT count(*) FROM memory_embeddings e
		 JOIN memory_search_docs d ON d.memory_id = e.memory_id
		 JOIN memories m ON m.id = e.memory_id
		 WHERE m.state <> ? AND m.assertion_status <> ?
		   AND (e.profile_id <> ? OR e.dimensions <> ? OR e.content_hash <> d.content_hash)),
		(SELECT count(*) FROM memory_search_docs d
		 JOIN memories m ON m.id = d.memory_id
		 WHERE m.state <> ? AND m.assertion_status <> ? AND d.content_hash <> ''
		   AND NOT EXISTS (SELECT 1 FROM memory_embeddings e WHERE e.memory_id = d.memory_id))`,
		string(memory.StateDeleted), string(memory.AssertionRejected),
		string(memory.StateDeleted), string(memory.AssertionRejected), profileID, dimensions,
		string(memory.StateDeleted), string(memory.AssertionRejected), profileID, dimensions,
		string(memory.StateDeleted), string(memory.AssertionRejected),
	).Scan(&stats.Documents, &stats.CurrentVectors, &stats.StaleVectors, &stats.MissingVectors)
	if err != nil {
		return embedding.Stats{}, fmt.Errorf("读取 Embedding 覆盖率: %w", err)
	}

	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_embedding_jobs
		WHERE status IN ('queued', 'running')`).Scan(&stats.PendingJobs); err != nil {
		return embedding.Stats{}, fmt.Errorf("统计待生成向量: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_embedding_jobs
		WHERE status = 'failed'`).Scan(&stats.FailedJobs); err != nil {
		return embedding.Stats{}, fmt.Errorf("统计失败向量: %w", err)
	}
	if err := scanErrorCounts(ctx, tx, stats.FailedCodes); err != nil {
		return embedding.Stats{}, err
	}
	if err := scanProfileCounts(ctx, tx, stats.IndexedProfiles); err != nil {
		return embedding.Stats{}, err
	}

	if stats.RebuildRequired && stats.StaleVectors+stats.MissingVectors+stats.PendingJobs == 0 {
		// The live inventory is complete even though a rebuild was registered.
		// Keep the registration timestamp for auditability but stop reporting
		// outstanding work.
		stats.RebuildRequired = false
		stats.RebuildReason = ""
	}
	if !stats.RebuildRequired && stats.StaleVectors+stats.MissingVectors+stats.PendingJobs > 0 {
		stats.RebuildRequired = true
		switch {
		case stats.StaleVectors > 0:
			stats.RebuildReason = "vectors_stale"
		case stats.MissingVectors > 0:
			stats.RebuildReason = "vectors_missing"
		default:
			stats.RebuildReason = "jobs_pending"
		}
	}
	return stats, nil
}

func scanErrorCounts(ctx context.Context, tx *sql.Tx, target map[string]int) error {
	rows, err := tx.QueryContext(ctx, `SELECT error_code, count(*) FROM memory_embedding_jobs
		WHERE status = 'failed' GROUP BY error_code ORDER BY count(*) DESC, error_code ASC`)
	if err != nil {
		return fmt.Errorf("统计向量失败原因: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var count int
		if err := rows.Scan(&code, &count); err != nil {
			return err
		}
		if code == "" {
			code = "internal"
		}
		target[code] = count
	}
	return rows.Err()
}

func scanProfileCounts(ctx context.Context, tx *sql.Tx, target map[string]int) error {
	rows, err := tx.QueryContext(ctx, `SELECT e.profile_id, count(*) FROM memory_embeddings e
		JOIN memory_search_docs d ON d.memory_id = e.memory_id
		JOIN memories m ON m.id = e.memory_id
		WHERE m.state <> ? AND m.assertion_status <> ?
		GROUP BY e.profile_id ORDER BY count(*) DESC, e.profile_id ASC`,
		string(memory.StateDeleted), string(memory.AssertionRejected))
	if err != nil {
		return fmt.Errorf("统计向量配置: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var profileID string
		var count int
		if err := rows.Scan(&profileID, &count); err != nil {
			return err
		}
		target[profileID] = count
	}
	return rows.Err()
}

func indexMetaValue(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (string, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value FROM memory_index_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取索引元数据 %s: %w", key, err)
	}
	return value, nil
}

func setIndexMetaValue(ctx context.Context, tx *sql.Tx, key, value string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_index_meta(key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("写入索引元数据 %s: %w", key, err)
	}
	return nil
}
