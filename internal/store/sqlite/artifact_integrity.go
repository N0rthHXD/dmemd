package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/topic"
)

// ArtifactIntegrityCheck audits immutable provenance against DB dependency
// edges. Historical versions may reference superseded memories; only active
// current artifacts must still match the semantic source snapshots.
func (s *Store) ArtifactIntegrityCheck(ctx context.Context) (artifact.IntegrityReport, error) {
	report := artifact.IntegrityReport{Issues: []artifact.IntegrityIssue{}}
	// Use a read transaction so concurrent ingest cannot produce false alarms.
	tx, err := s.reader.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT artifact_id, version FROM artifact_revisions ORDER BY artifact_id, version`)
	if err != nil {
		return report, err
	}
	type key struct {
		id      string
		version int
	}
	keys := []key{}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.version); err != nil {
			rows.Close()
			return report, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, err
	}
	rows.Close()
	// A broken current pointer must not disappear simply because there is no
	// revision row to visit in the main loop.
	rows, err = tx.QueryContext(ctx, `SELECT a.id, a.current_version FROM artifacts a
		LEFT JOIN artifact_revisions r ON r.artifact_id = a.id AND r.version = a.current_version
		WHERE (a.current_version > 0 AND r.artifact_id IS NULL)
		OR (a.state = 'active' AND a.current_version = 0)`)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var issue artifact.IntegrityIssue
		if err := rows.Scan(&issue.ArtifactID, &issue.Version); err != nil {
			rows.Close()
			return report, err
		}
		issue.Code = "missing_current_revision"
		report.Issues = append(report.Issues, issue)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, err
	}
	rows.Close()
	for _, k := range keys {
		report.Checked++
		add := func(code string) {
			report.Issues = append(report.Issues, artifact.IntegrityIssue{ArtifactID: k.id, Version: k.version, Code: code})
		}
		r, err := getArtifactRevision(ctx, tx, k.id, k.version)
		if err != nil {
			return report, err
		}
		a, err := getArtifact(ctx, tx, k.id)
		if err != nil {
			return report, err
		}
		var p artifact.Provenance
		if json.Unmarshal(r.Provenance, &p) != nil || !json.Valid(r.Payload) {
			add("invalid_serialization")
		}
		if r.RunID == "" || r.TaxonomyVersion == "" || r.GeneratorVersion == "" || r.Generator == "" || r.ProtocolVersion == "" {
			add("missing_generation_metadata")
		}
		if p.RunID != r.RunID || p.TaxonomyVersion != r.TaxonomyVersion || p.GeneratorVersion != r.GeneratorVersion || p.ProtocolVersion != r.ProtocolVersion {
			add("metadata_mismatch")
		}
		if p.Truncated != r.Truncated || r.ReasonCode == "input_truncated" && !r.Truncated {
			add("truncation_mismatch")
		}
		if artifact.ContentHash(r.Title, r.Content, r.Payload, r.Provenance) != r.ContentHash {
			add("content_hash_mismatch")
		}
		if !reflect.DeepEqual(p.MemoryRefs, r.MemoryRefs) || !reflect.DeepEqual(p.RunRefs, r.RunRefs) {
			add("dependency_mismatch")
		}
		if a.State == artifact.StateActive && a.CurrentVersion == r.Version && r.TaxonomyVersion != topic.Version {
			add("taxonomy_version_mismatch")
		}
		var payload struct {
			Truncated  *bool `json:"truncated"`
			MemoryRefs []struct {
				ID      string `json:"memory_id"`
				Version int    `json:"version"`
			} `json:"memory_refs"`
			ExperienceRefs []string `json:"experience_refs"`
		}
		if json.Unmarshal(r.Payload, &payload) == nil && payload.Truncated != nil && *payload.Truncated != r.Truncated {
			add("truncation_mismatch")
		}
		if a.Kind == artifact.KindExperienceCase {
			refs := make([]artifact.MemoryRef, 0, len(payload.MemoryRefs))
			for _, ref := range payload.MemoryRefs {
				refs = append(refs, artifact.MemoryRef{MemoryID: ref.ID, Version: ref.Version})
			}
			runs := make([]artifact.RunRef, 0, len(payload.ExperienceRefs))
			for _, id := range payload.ExperienceRefs {
				runs = append(runs, artifact.RunRef{ExperienceID: id})
			}
			if !sameRefSet(refs, r.MemoryRefs) || !sameRunRefSet(runs, r.RunRefs) {
				add("payload_dependency_mismatch")
			}
		}
		for _, ref := range p.MemoryRefs {
			old, err := getRevision(ctx, tx, ref.MemoryID, ref.Version)
			if err != nil {
				if !errors.Is(err, memory.ErrNotFound) {
					return report, err
				}
				add("missing_memory_revision")
				continue
			}
			if a.State != artifact.StateActive || a.CurrentVersion != r.Version {
				continue
			}
			current, err := getMemory(ctx, tx, ref.MemoryID)
			if err != nil && !errors.Is(err, memory.ErrNotFound) {
				return report, err
			}
			if err != nil || current.State != memory.StateActive || current.AssertionStatus != memory.AssertionAccepted {
				add("ineligible_memory")
				continue
			}
			// Evidence-only revisions intentionally do not stale derived prose.
			before := old.Snapshot
			current.Version, before.Version = 0, 0
			current.UpdatedAt, before.UpdatedAt = current.CreatedAt, before.CreatedAt
			if !reflect.DeepEqual(current, before) {
				add("source_version_mismatch")
			}
		}
		for _, ref := range p.RunRefs {
			var state string
			err := tx.QueryRowContext(ctx, `SELECT state FROM experience_runs WHERE id = ?`, ref.ExperienceID).Scan(&state)
			if err != nil {
				if !errors.Is(err, sql.ErrNoRows) {
					return report, err
				}
				add("missing_run")
			} else if a.State == artifact.StateActive && a.CurrentVersion == r.Version && state != "recorded" {
				add("ineligible_run")
			}
		}
		if a.CurrentVersion == r.Version {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_search_docs WHERE artifact_id = ? AND artifact_version = ?`, a.ID, r.Version).Scan(&count); err != nil {
				return report, err
			}
			if (a.State == artifact.StateActive) != (count == 1) {
				add("search_document_mismatch")
			}
		}
	}
	return report, tx.Commit()
}

func sameRefSet(left, right []artifact.MemoryRef) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[artifact.MemoryRef]bool, len(right))
	for _, ref := range right {
		set[ref] = true
	}
	for _, ref := range left {
		if !set[ref] {
			return false
		}
		delete(set, ref)
	}
	return true
}

func sameRunRefSet(left, right []artifact.RunRef) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[artifact.RunRef]bool, len(right))
	for _, ref := range right {
		set[ref] = true
	}
	for _, ref := range left {
		if !set[ref] {
			return false
		}
		delete(set, ref)
	}
	return true
}
