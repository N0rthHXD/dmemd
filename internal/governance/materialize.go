package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

const (
	maxMemoryTitleRunes  = 160
	maxEnrichedBodyRunes = 12000
)

// MaterializeInput is one validated relation outcome. Target is nil for
// relations that do not point at an existing canonical memory.
type MaterializeInput struct {
	Observation observation.Observation
	Relation    Relation
	Target      *memory.Memory
}

// MaterializeResult reports the canonical memories created or updated by one
// governance decision.
type MaterializeResult struct {
	MemoryIDs []string
}

// MemoryMaterializer applies a semantic relation to the current canonical
// view. It never calls a model and never rewrites an Observation.
type MemoryMaterializer interface {
	Apply(ctx context.Context, tx Tx, input MaterializeInput) (MaterializeResult, error)
}

// Materializer is the conservative first-release relation materializer. It
// creates a current memory for independent observations, attaches source
// observations to duplicate/supporting memories, appends an enriching revision,
// and keeps contradictory/unresolved observations pending instead of silently
// overwriting an accepted view.
type Materializer struct {
	writer memory.Writer
}

// NewMaterializer assembles the canonical-view writer. The memory service owns
// snapshots, revisions, evidence and search-document maintenance.
func NewMaterializer(writer memory.Writer) *Materializer {
	return &Materializer{writer: writer}
}

var _ MemoryMaterializer = (*Materializer)(nil)

// Apply implements MemoryMaterializer.
func (m *Materializer) Apply(ctx context.Context, tx Tx, input MaterializeInput) (MaterializeResult, error) {
	if m == nil || m.writer == nil {
		return MaterializeResult{}, errors.New("governance: memory materializer not configured")
	}
	if input.Observation.ID == "" {
		return MaterializeResult{}, errors.New("governance: observation is missing")
	}
	switch input.Relation.Kind {
	case RelationDuplicate, RelationSupports:
		if input.Target == nil {
			return m.create(ctx, tx, input, memory.AssertionPending)
		}
		return m.attach(ctx, tx, input)
	case RelationEnriches:
		if input.Target == nil {
			return m.create(ctx, tx, input, memory.AssertionPending)
		}
		return m.enrich(ctx, tx, input)
	case RelationContradicts, RelationUnresolved:
		return m.create(ctx, tx, input, memory.AssertionPending)
	case RelationSupersedes, RelationIndependent, RelationSeparate:
		return m.create(ctx, tx, input, assertionForObservation(input.Observation))
	default:
		return m.create(ctx, tx, input, memory.AssertionPending)
	}
}

// attach adds a supporting or duplicate observation to the current memory
// version without changing its body, version or assertion status. Provenance is
// attached even when the observation itself is still pending: a duplicate or
// supporting observation never creates a second canonical view nor promotes the
// observation to accepted.
func (m *Materializer) attach(ctx context.Context, tx Tx, input MaterializeInput) (MaterializeResult, error) {
	target := *input.Target
	if target.State == memory.StateDeleted || target.AssertionStatus == memory.AssertionRejected {
		return m.create(ctx, tx, input, memory.AssertionPending)
	}
	if target.Version <= 0 {
		return MaterializeResult{}, fmt.Errorf("governance: memory %s has no current version", target.ID)
	}
	if err := tx.InsertMemoryObservationRefs(ctx, target.ID, target.Version, []string{input.Observation.ID}); err != nil {
		return MaterializeResult{}, err
	}
	return MaterializeResult{MemoryIDs: []string{target.ID}}, nil
}

// enrich appends one governance revision. The old body and old observation
// refs remain in history; the new revision records the complete ref set.
func (m *Materializer) enrich(ctx context.Context, tx Tx, input MaterializeInput) (MaterializeResult, error) {
	target := *input.Target
	if input.Observation.Status != observation.StatusObserved ||
		target.State == memory.StateDeleted || target.AssertionStatus == memory.AssertionRejected {
		return m.create(ctx, tx, input, memory.AssertionPending)
	}
	currentRefs, err := tx.ListMemoryObservationRefs(ctx, target.ID, target.Version)
	if err != nil {
		return MaterializeResult{}, err
	}
	oldEvidence, err := tx.ListRevisionEvidence(ctx, target.ID, target.Version)
	if err != nil {
		return MaterializeResult{}, err
	}
	newEvidence, err := tx.ListObservationEvidence(ctx, input.Observation.ID)
	if err != nil {
		return MaterializeResult{}, err
	}
	content := input.Observation.Content
	if strings.TrimSpace(content) == "" {
		return MaterializeResult{}, errors.New("governance: observation content is empty")
	}
	content = joinUniqueBody(target.Content, content)
	status := target.AssertionStatus
	if status != memory.AssertionAccepted || input.Observation.Status != observation.StatusObserved {
		status = memory.AssertionPending
	}
	revised, err := m.writer.Revise(ctx, tx, memory.ReviseInput{
		MemoryID:        target.ID,
		Kind:            target.Kind,
		Title:           target.Title,
		Content:         content,
		AssertionStatus: status,
		ScopeKey:        target.ScopeKey,
		Evidence:        mergeEvidence(oldEvidence, newEvidence),
		Operation:       memory.OperationUpdate,
		Reason:          "governance:" + string(input.Relation.Kind),
	})
	if err != nil {
		return MaterializeResult{}, err
	}
	refs := append(append([]string(nil), currentRefs...), input.Observation.ID)
	if err := tx.InsertMemoryObservationRefs(ctx, revised.ID, revised.Version, refs); err != nil {
		return MaterializeResult{}, err
	}
	return MaterializeResult{MemoryIDs: []string{revised.ID}}, nil
}

func (m *Materializer) create(ctx context.Context, tx Tx, input MaterializeInput, status memory.AssertionStatus) (MaterializeResult, error) {
	content := strings.TrimSpace(input.Observation.Content)
	if content == "" {
		return MaterializeResult{}, errors.New("governance: observation content is empty")
	}
	evidence, err := tx.ListObservationEvidence(ctx, input.Observation.ID)
	if err != nil {
		return MaterializeResult{}, err
	}
	created, err := m.writer.Create(ctx, tx, memory.CreateInput{
		Kind:            input.Observation.Kind,
		Title:           observationTitle(input.Observation),
		Content:         content,
		AssertionStatus: status,
		OriginSourceID:  input.Observation.SourceID,
		ScopeKey:        input.Observation.ScopeKey,
		Evidence:        evidence,
		Operation:       memory.OperationCreate,
		Reason:          "governance:" + string(input.Relation.Kind),
	})
	if err != nil {
		return MaterializeResult{}, err
	}
	if err := tx.InsertMemoryObservationRefs(ctx, created.ID, created.Version, []string{input.Observation.ID}); err != nil {
		return MaterializeResult{}, err
	}
	return MaterializeResult{MemoryIDs: []string{created.ID}}, nil
}

func assertionForObservation(item observation.Observation) memory.AssertionStatus {
	if item.Status == observation.StatusObserved {
		return memory.AssertionAccepted
	}
	return memory.AssertionPending
}

func observationTitle(item observation.Observation) string {
	content := strings.TrimSpace(item.Content)
	if content == "" {
		if item.Kind == "" {
			return "观察"
		}
		return string(item.Kind)
	}
	if index := strings.IndexAny(content, "\r\n"); index >= 0 {
		content = content[:index]
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return string(item.Kind)
	}
	return truncateRunes(content, maxMemoryTitleRunes)
}

func joinUniqueBody(base, addition string) string {
	base = strings.TrimSpace(base)
	addition = strings.TrimSpace(addition)
	switch {
	case base == "":
		return truncateRunes(addition, maxEnrichedBodyRunes)
	case addition == "":
		return truncateRunes(base, maxEnrichedBodyRunes)
	case normalizeBody(base) == normalizeBody(addition) || strings.Contains(normalizeBody(base), normalizeBody(addition)):
		return truncateRunes(base, maxEnrichedBodyRunes)
	default:
		return truncateRunes(base+"\n"+addition, maxEnrichedBodyRunes)
	}
}

func normalizeBody(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func mergeEvidence(left, right []source.Evidence) []source.Evidence {
	seen := make(map[string]struct{}, len(left)+len(right))
	result := make([]source.Evidence, 0, len(left)+len(right))
	for _, item := range append(append([]source.Evidence(nil), left...), right...) {
		if item.ID == "" {
			continue
		}
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, item)
	}
	return result
}
