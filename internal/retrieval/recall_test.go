package retrieval

import (
	"testing"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestContextBudgetKeepsConflictAndPendingLabel(t *testing.T) {
	result := RecallResult{
		Memories: []Item{{
			Memory: memory.Memory{
				ID: "m1", Title: "very long title", Content: "very long content",
				AssertionStatus: memory.AssertionPending,
			},
		}},
		Conflicts: []memory.ConflictSummary{{ID: "c1", ReasonCode: memory.ConflictContradictoryValue}},
		Warnings:  []string{},
	}
	applyContextBudget(&result, 1, nil)
	if len(result.Memories) != 0 {
		t.Fatalf("memory content was not truncated: %+v", result.Memories)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].ID != "c1" {
		t.Fatalf("conflict summary disappeared with budget truncation: %+v", result.Conflicts)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != WarningContextBudgetTruncated {
		t.Fatalf("warnings = %+v", result.Warnings)
	}

	// A pending item that fits retains its status; budget handling never
	// upgrades or rewrites the assertion.
	result = RecallResult{
		Memories: []Item{{Memory: memory.Memory{
			ID: "m2", Title: "t", Content: "c", AssertionStatus: memory.AssertionPending,
		}}},
		Conflicts: []memory.ConflictSummary{},
		Warnings:  []string{},
	}
	applyContextBudget(&result, 10, nil)
	if len(result.Memories) != 1 || result.Memories[0].Memory.AssertionStatus != memory.AssertionPending {
		t.Fatalf("pending label changed under budget: %+v", result.Memories)
	}
}
