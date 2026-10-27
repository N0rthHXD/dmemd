package ingest

import (
	"strings"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

// factGroupIdentity builds the service-owned identity tuple. The model may
// suggest text, but the source scope and extracted conditions are mandatory
// inputs the model cannot replace.
func factGroupIdentity(sourceScope string, proposal FactGroupProposal, qualifiers *memory.AssertionQualifiers) memory.FactGroupIdentity {
	conditions := proposal.Conditions
	if len(conditions) == 0 && qualifiers != nil {
		conditions = qualifiers.Conditions
	}
	return memory.FactGroupIdentity{
		ScopeKey:    sourceScope,
		Subject:     strings.TrimSpace(proposal.Subject),
		Predicate:   strings.TrimSpace(proposal.Predicate),
		Scope:       strings.TrimSpace(proposal.Scope),
		Conditions:  memory.NormalizeConditions(conditions),
		Cardinality: proposal.Cardinality,
	}
}

func candidateGroupIdentity(sourceScope string, candidate Candidate) (memory.FactGroupIdentity, bool) {
	if candidate.FactGroup == nil {
		return memory.FactGroupIdentity{}, false
	}
	identity := factGroupIdentity(sourceScope, *candidate.FactGroup, candidate.Qualifiers)
	if memory.NormalizeIdentityText(identity.Subject) == "" || memory.NormalizeIdentityText(identity.Predicate) == "" {
		return memory.FactGroupIdentity{}, false
	}
	return identity, true
}

func sameFactGroupIdentity(left, right memory.FactGroupIdentity) bool {
	l := memory.FactGroupKey(left)
	r := memory.FactGroupKey(right)
	return l == r
}

// compatibleGroup is the only fallback used when optional identity text is
// absent. It never merges two non-empty, different scenes or condition sets.
func compatibleGroup(groups []memory.FactGroup, identity memory.FactGroupIdentity) *memory.FactGroup {
	for index := range groups {
		group := groups[index]
		if group.ScopeKey != memory.NormalizeScopeKey(identity.ScopeKey) ||
			group.SubjectKey != memory.NormalizeIdentityText(identity.Subject) ||
			group.PredicateKey != memory.NormalizeIdentityText(identity.Predicate) {
			continue
		}
		if identity.Scope != "" && group.Scope != "" && group.SceneKey != memory.NormalizeIdentityText(identity.Scope) {
			continue
		}
		if len(identity.Conditions) > 0 && len(group.Conditions) > 0 &&
			group.ConditionsKey != memory.ConditionsKey(identity.Conditions) {
			continue
		}
		copied := group
		return &copied
	}
	return nil
}
