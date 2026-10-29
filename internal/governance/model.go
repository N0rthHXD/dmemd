package governance

import (
	"context"

	"github.com/N0rthHXD/dmemd/internal/ingest"
)

// RelationProvider is the minimal semantic relation model capability. It
// receives request-local aliases and never receives real database identifiers.
type RelationProvider interface {
	Relate(ctx context.Context, request RelationRequest) (RelationResult, error)
}

// RelationCandidate is one bounded object offered to the relation model. Ref is
// a request-local alias such as "new" or "existing_1"; it is never a database
// identifier. The service maps aliases back to real objects after validation.
type RelationCandidate struct {
	Ref          string `json:"ref"`
	Type         string `json:"type"`
	Kind         string `json:"kind"`
	ScopeKey     string `json:"scope_key,omitempty"`
	Content      string `json:"content"`
	EvidenceHint string `json:"evidence_hint,omitempty"`
}

// RelationRequest asks only for semantic relation labels between the new
// observation and a small bounded candidate set.
type RelationRequest struct {
	NewObservation RelationCandidate   `json:"new_observation"`
	Existing       []RelationCandidate `json:"existing"`
	KnownRelations []string            `json:"known_relations,omitempty"`
}

// RelationProposal is the model-facing relation output. References are
// request-local aliases; the service validates them before persistence.
type RelationProposal struct {
	FromRef string       `json:"from_ref"`
	ToRef   string       `json:"to_ref,omitempty"`
	Kind    RelationKind `json:"kind"`
}

// RelationResult is one parsed relation response plus call accounting.
type RelationResult struct {
	Relations []RelationProposal `json:"relations"`
	Usage     ingest.ModelUsage  `json:"-"`
}
