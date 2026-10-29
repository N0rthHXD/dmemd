package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

const relationColumns = `r.id, r.kind, r.from_type, r.from_id, r.from_version,
	r.to_type, r.to_id, r.to_version, r.created_at`

// InsertRelation writes one typed relation. Repeated relations are idempotent,
// so re-detecting the same pair does not accumulate rows.
func (t *Tx) InsertRelation(ctx context.Context, relation memory.Relation) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_relations(
		id, kind, from_type, from_id, from_version, to_type, to_id, to_version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(from_type, from_id, from_version, to_type, to_id, to_version, kind) DO NOTHING`,
		relation.ID, string(relation.Kind),
		string(governance.EndpointMemory), relation.FromMemoryID, relation.FromVersion,
		string(governance.EndpointMemory), relation.ToMemoryID, relation.ToVersion,
		formatTime(relation.CreatedAt))
	if err != nil {
		return fmt.Errorf("写入记忆关系 %s: %w", relation.ID, err)
	}
	return nil
}

// ListMemoryRelationRefs reads the typed governance relation graph for one
// memory in either direction. It returns the neutral memory.RelationRef shape so
// the canonical detail view can expose relations without importing governance.
func (s *Store) ListMemoryRelationRefs(ctx context.Context, memoryID string) ([]memory.RelationRef, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT `+governanceRelationColumns+`
		FROM memory_relations r
		WHERE (r.from_type = 'memory' AND r.from_id = ?)
		   OR (r.to_type = 'memory' AND r.to_id = ?)
		ORDER BY r.created_at ASC, r.id ASC`, memoryID, memoryID)
	if err != nil {
		return nil, fmt.Errorf("读取记忆 %s 的治理关系: %w", memoryID, err)
	}
	defer rows.Close()
	items := []memory.RelationRef{}
	for rows.Next() {
		var item memory.RelationRef
		var createdAt string
		if err := rows.Scan(&item.ID, &item.Kind, &item.FromType, &item.FromID,
			&item.FromVersion, &item.ToType, &item.ToID, &item.ToVersion, &createdAt); err != nil {
			return nil, fmt.Errorf("读取记忆 %s 的治理关系: %w", memoryID, err)
		}
		parsed, err := parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("读取记忆 %s 的治理关系: %w", memoryID, err)
		}
		item.CreatedAt = parsed
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取记忆 %s 的治理关系: %w", memoryID, err)
	}
	return items, nil
}

// relationQueryer is the read capability shared by the read pool and a write
// transaction.
type relationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// listRelationsForMemory returns relations in either direction together with
// whether each memory endpoint moved past the decision-time version. Observation
// endpoints are exposed through the same memory-shaped DTO until the HTTP layer
// moves to the typed governance relation.
func listRelationsForMemory(ctx context.Context, q relationQueryer, memoryID string) ([]memory.Relation, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+relationColumns+`,
		COALESCE(f.version, 0), COALESCE(tg.version, 0)
		FROM memory_relations r
		LEFT JOIN memories f ON r.from_type = 'memory' AND f.id = r.from_id
		LEFT JOIN memories tg ON r.to_type = 'memory' AND tg.id = r.to_id
		WHERE (r.from_type = 'memory' AND r.from_id = ?)
		   OR (r.to_type = 'memory' AND r.to_id = ?)
		ORDER BY r.created_at ASC, r.id ASC`, memoryID, memoryID)
	if err != nil {
		return nil, fmt.Errorf("读取记忆关系 %s: %w", memoryID, err)
	}
	defer rows.Close()
	items := []memory.Relation{}
	for rows.Next() {
		item, err := scanRelation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取记忆关系 %s: %w", memoryID, err)
	}
	return items, nil
}

const governanceRelationColumns = `r.id, r.kind, r.from_type, r.from_id, r.from_version,
	r.to_type, r.to_id, r.to_version, r.created_at`

// InsertGovernanceRelation writes one typed relation between an observation,
// a canonical memory or both. It rejects unknown endpoint types and empty
// identifiers; the service validates object existence before calling it.
func (t *Tx) InsertGovernanceRelation(ctx context.Context, relation governance.Relation) error {
	if !relation.Kind.Known() {
		return fmt.Errorf("未知关系类型 %q", relation.Kind)
	}
	if !relation.FromType.Known() || !relation.ToType.Known() {
		return fmt.Errorf("未知关系端点类型 %q/%q", relation.FromType, relation.ToType)
	}
	if relation.ID == "" || relation.FromID == "" || relation.ToID == "" {
		return errors.New("关系标识和端点不能为空")
	}
	if relation.FromVersion < 0 || relation.ToVersion < 0 {
		return errors.New("关系端点版本不能为负")
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO memory_relations(
		id, kind, from_type, from_id, from_version, to_type, to_id, to_version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(from_type, from_id, from_version, to_type, to_id, to_version, kind) DO NOTHING`,
		relation.ID, string(relation.Kind), string(relation.FromType), relation.FromID,
		relation.FromVersion, string(relation.ToType), relation.ToID, relation.ToVersion,
		formatTime(relation.CreatedAt))
	if err != nil {
		return fmt.Errorf("写入治理关系 %s: %w", relation.ID, err)
	}
	return nil
}

// ListGovernanceRelationsForEndpoint returns every relation touching one typed
// endpoint. It is the read API used by observation governance and detail views.
func (s *Store) ListGovernanceRelationsForEndpoint(ctx context.Context, endpointType governance.EndpointType, id string) ([]governance.Relation, error) {
	return listGovernanceRelationsForEndpoint(ctx, s.reader, endpointType, id)
}

// ListGovernanceRelationsForEndpoint is the transaction-scoped variant.
func (t *Tx) ListGovernanceRelationsForEndpoint(ctx context.Context, endpointType governance.EndpointType, id string) ([]governance.Relation, error) {
	return listGovernanceRelationsForEndpoint(ctx, t.tx, endpointType, id)
}

func listGovernanceRelationsForEndpoint(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, endpointType governance.EndpointType, id string) ([]governance.Relation, error) {
	if !endpointType.Known() || id == "" {
		return nil, fmt.Errorf("无效关系端点 %q/%s", endpointType, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT `+governanceRelationColumns+`
		FROM memory_relations r
		WHERE (r.from_type = ? AND r.from_id = ?)
		   OR (r.to_type = ? AND r.to_id = ?)
		ORDER BY r.created_at ASC, r.id ASC`,
		string(endpointType), id, string(endpointType), id)
	if err != nil {
		return nil, fmt.Errorf("读取 %s %s 的关系: %w", endpointType, id, err)
	}
	defer rows.Close()
	items := []governance.Relation{}
	for rows.Next() {
		item, err := scanGovernanceRelation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取 %s %s 的关系: %w", endpointType, id, err)
	}
	return items, nil
}

// ListGovernanceRelationsBetween returns exact directed relations between two
// typed endpoints in stable creation order.
func (s *Store) ListGovernanceRelationsBetween(ctx context.Context, fromType governance.EndpointType, fromID string,
	toType governance.EndpointType, toID string) ([]governance.Relation, error) {
	return listGovernanceRelationsBetween(ctx, s.reader, fromType, fromID, toType, toID)
}

// ListGovernanceRelationsBetween is the transaction-scoped variant.
func (t *Tx) ListGovernanceRelationsBetween(ctx context.Context, fromType governance.EndpointType, fromID string,
	toType governance.EndpointType, toID string) ([]governance.Relation, error) {
	return listGovernanceRelationsBetween(ctx, t.tx, fromType, fromID, toType, toID)
}

func listGovernanceRelationsBetween(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, fromType governance.EndpointType, fromID string, toType governance.EndpointType, toID string) ([]governance.Relation, error) {
	if !fromType.Known() || !toType.Known() || fromID == "" || toID == "" {
		return nil, fmt.Errorf("无效关系端点 %q/%s -> %q/%s", fromType, fromID, toType, toID)
	}
	rows, err := q.QueryContext(ctx, `SELECT `+governanceRelationColumns+`
		FROM memory_relations r
		WHERE r.from_type = ? AND r.from_id = ?
		  AND r.to_type = ? AND r.to_id = ?
		ORDER BY r.created_at ASC, r.id ASC`,
		string(fromType), fromID, string(toType), toID)
	if err != nil {
		return nil, fmt.Errorf("读取关系 %s/%s -> %s/%s: %w", fromType, fromID, toType, toID, err)
	}
	defer rows.Close()
	items := []governance.Relation{}
	for rows.Next() {
		item, err := scanGovernanceRelation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取关系 %s/%s -> %s/%s: %w", fromType, fromID, toType, toID, err)
	}
	return items, nil
}

func scanGovernanceRelation(row rowScanner) (governance.Relation, error) {
	var item governance.Relation
	var kind, fromType, toType, createdAt string
	if err := row.Scan(&item.ID, &kind, &fromType, &item.FromID, &item.FromVersion,
		&toType, &item.ToID, &item.ToVersion, &createdAt); err != nil {
		return governance.Relation{}, err
	}
	item.Kind = governance.RelationKind(kind)
	item.FromType = governance.EndpointType(fromType)
	item.ToType = governance.EndpointType(toType)
	parsed, err := parseTime(createdAt)
	if err != nil {
		return governance.Relation{}, err
	}
	item.CreatedAt = parsed
	return item, nil
}

func scanRelation(row rowScanner) (memory.Relation, error) {
	var relation memory.Relation
	var kind, fromType, toType, createdAt string
	var fromCurrent, toCurrent int
	if err := row.Scan(&relation.ID, &kind, &fromType, &relation.FromMemoryID,
		&relation.FromVersion, &toType, &relation.ToMemoryID, &relation.ToVersion,
		&createdAt, &fromCurrent, &toCurrent); err != nil {
		return memory.Relation{}, err
	}
	relation.Kind = memory.RelationKind(kind)
	var err error
	if relation.CreatedAt, err = parseTime(createdAt); err != nil {
		return memory.Relation{}, err
	}
	relation.FromVersionChanged = fromType == string(governance.EndpointMemory) &&
		fromCurrent != relation.FromVersion
	relation.ToVersionChanged = toType == string(governance.EndpointMemory) &&
		toCurrent != relation.ToVersion
	return relation, nil
}
