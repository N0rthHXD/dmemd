// Package extraction owns the first stage of the memory pipeline: turning one
// source into L0 observation drafts. The model only selects stable source spans
// and optionally supplies one short normalized expression; identity, evidence,
// status and timestamps are always added by code.
package extraction

import (
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// Request is the bounded extraction input for one source or one source chunk.
// Segments and ContextSegments keep the same meaning as in the source view:
// only Segments are selectable, ContextSegments are read-only context.
type Request struct {
	Source          source.Source
	Segments        []source.Segment
	ContextSegments []source.Segment
	ChunkIndex      int
	ChunkCount      int
}

// ModelUsage is the source-free token accounting for one model call.
type ModelUsage struct {
	Calls        int `json:"calls"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Usage is the short alias used by request/result contracts.
type Usage = ModelUsage

// Add merges one call/usage record into the aggregate.
func (u *ModelUsage) Add(other ModelUsage) {
	if u == nil {
		return
	}
	u.Calls += other.Calls
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
}

// TotalTokens returns the reported input plus output token count.
func (u ModelUsage) TotalTokens() int { return u.InputTokens + u.OutputTokens }

// Result is one parsed extraction response. Drafts contain only model-selected
// span IDs, an optional kind and an optional normalized expression.
type Result struct {
	Drafts        []observation.Draft
	Usage         ModelUsage
	Chunks        int
	ChunkFailures int
}

// DraftResult is the parser output before usage is attached. It intentionally
// carries no evidence, title, time, topic, group or database identity.
type DraftResult struct {
	Drafts []observation.Draft
}
