package sqlite

import (
	"context"

	"github.com/N0rthHXD/dmemd/internal/artifact"
)

// ArtifactIntegrityCheck audits the current derived pointer and the immutable
// revision content hash. Dependency edges are intentionally validated by the
// generic dependency store rather than by serialized provenance copies.
func (s *Store) ArtifactIntegrityCheck(ctx context.Context) (artifact.IntegrityReport, error) {
	report := artifact.IntegrityReport{Issues: []artifact.IntegrityIssue{}}
	tx, err := s.reader.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.current_version FROM artifacts a
		LEFT JOIN artifact_revisions r ON r.artifact_id = a.id AND r.version = a.current_version
		WHERE (a.current_version > 0 AND r.artifact_id IS NULL)
		OR (a.state = 'active' AND a.current_version = 0)`)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var issue artifact.IntegrityIssue
		if err := rows.Scan(&issue.ArtifactID, &issue.Version); err != nil {
			return report, err
		}
		issue.Code = "missing_current_revision"
		report.Issues = append(report.Issues, issue)
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	return report, nil
}
