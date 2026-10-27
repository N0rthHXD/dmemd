package ingest

import (
	"testing"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestDecisionContextCompletenessIsPerCandidateAndGroup(t *testing.T) {
	plan := &mergePlan{
		complete:             false,
		incompleteCandidates: map[string]struct{}{"c1": {}},
		incompleteGroups:     map[string]struct{}{"g1": {}},
		related: map[string]memory.Memory{
			"m1": {ID: "m1", FactGroupID: "g1"},
			"m2": {ID: "m2", FactGroupID: "g2"},
		},
	}
	if plan.candidateComplete("c1") {
		t.Fatal("truncated candidate must be incomplete")
	}
	if !plan.candidateComplete("c2") {
		t.Fatal("truncated unrelated candidate must not downgrade c2")
	}
	if !plan.groupComplete("g2") {
		t.Fatal("truncated unrelated group must not downgrade g2")
	}
	if plan.groupComplete("g1") {
		t.Fatal("truncated group must be incomplete")
	}
	if plan.decisionContextComplete(Decision{CandidateID: "c2", Action: ActionChange, TargetMemoryIDs: []string{"m1"}}) {
		t.Fatal("target in truncated group must be incomplete")
	}
	if !plan.decisionContextComplete(Decision{CandidateID: "c2", Action: ActionChange, TargetMemoryIDs: []string{"m2"}}) {
		t.Fatal("safe target in complete group must stay complete")
	}
	if !plan.transitionOrdered("c2") {
		t.Fatal("a single candidate source has no same-source ordering conflict")
	}
}
