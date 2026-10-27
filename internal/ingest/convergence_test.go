package ingest

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestConvergenceRequiresExplicitScopedSingleValueEvidence(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*mergePlan, *plannedAction, *memory.FactGroup, *memory.Memory)
		want   bool
	}{
		{name: "explicit correction", want: true},
		{name: "bare different value", mutate: func(p *mergePlan, a *plannedAction, _ *memory.FactGroup, _ *memory.Memory) {
			a.evidence[0].Quote = "血糖是5.6"
			p.source.Content = json.RawMessage(`"血糖是5.6"`)
		}},
		{name: "conditional update", mutate: func(p *mergePlan, a *plannedAction, _ *memory.FactGroup, _ *memory.Memory) {
			a.evidence[0].Quote = "如果更正血糖为5.6"
			p.source.Content = json.RawMessage(`"如果更正血糖为5.6"`)
		}},
		{name: "negated update", mutate: func(p *mergePlan, a *plannedAction, _ *memory.FactGroup, _ *memory.Memory) {
			a.evidence[0].Quote = "没有更正血糖为5.6"
			p.source.Content = json.RawMessage(`"没有更正血糖为5.6"`)
		}},
		{name: "unlocatable quote", mutate: func(_ *mergePlan, a *plannedAction, _ *memory.FactGroup, _ *memory.Memory) {
			a.evidence[0].Quote = "更正其他值"
		}},
		{name: "foreign evidence", mutate: func(_ *mergePlan, a *plannedAction, _ *memory.FactGroup, _ *memory.Memory) {
			a.evidence[0].SourceID = "other"
		}},
		{name: "redacted source", mutate: func(p *mergePlan, _ *plannedAction, _ *memory.FactGroup, _ *memory.Memory) { p.source.Redacted = true }},
		{name: "unknown cardinality", mutate: func(_ *mergePlan, _ *plannedAction, g *memory.FactGroup, _ *memory.Memory) {
			g.Cardinality = memory.CardinalityUnknown
		}},
		{name: "multiple values", mutate: func(_ *mergePlan, _ *plannedAction, g *memory.FactGroup, _ *memory.Memory) {
			g.Cardinality = memory.CardinalityMultiple
		}},
		{name: "different subject", mutate: func(_ *mergePlan, a *plannedAction, _ *memory.FactGroup, _ *memory.Memory) {
			a.candidate.FactGroup.Subject = "女儿"
		}},
		{name: "different predicate", mutate: func(_ *mergePlan, a *plannedAction, _ *memory.FactGroup, _ *memory.Memory) {
			a.candidate.FactGroup.Predicate = "血压"
		}},
		{name: "different scope", mutate: func(_ *mergePlan, _ *plannedAction, _ *memory.FactGroup, m *memory.Memory) { m.ScopeKey = "other" }},
		{name: "older assertion", mutate: func(_ *mergePlan, a *plannedAction, _ *memory.FactGroup, m *memory.Memory) {
			a.candidate.ObservedAt = &m.ObservedAt
		}},
		{name: "truncated context", mutate: func(p *mergePlan, _ *plannedAction, _ *memory.FactGroup, _ *memory.Memory) { p.complete = false }},
		{name: "disjoint reschedule", want: true, mutate: func(_ *mergePlan, a *plannedAction, _ *memory.FactGroup, m *memory.Memory) {
			oldEnd, start, end := base.Add(time.Hour), base.Add(48*time.Hour), base.Add(49*time.Hour)
			m.ValidFrom, m.ValidTo = &base, &oldEnd
			a.candidate.ValidFrom, a.candidate.ValidTo = &start, &end
			a.candidate.ValidFromBasis, a.candidate.ValidToBasis = memory.BasisExplicit, memory.BasisExplicit
		}},
		{name: "bounded overlap", mutate: func(_ *mergePlan, a *plannedAction, _ *memory.FactGroup, m *memory.Memory) {
			end := base.Add(72 * time.Hour)
			start := base.Add(time.Hour)
			m.ValidFrom, m.ValidTo = &base, &end
			a.candidate.ValidFrom, a.candidate.ValidTo = &start, &end
			a.candidate.ValidFromBasis, a.candidate.ValidToBasis = memory.BasisExplicit, memory.BasisExplicit
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			later := base.Add(time.Hour)
			group := memory.FactGroup{ID: "g", Subject: "用户", Predicate: "血糖", Cardinality: memory.CardinalitySingle}
			old := memory.Memory{ID: "old", FactGroupID: "g", Content: "血糖是6.5", State: memory.StateActive, AssertionStatus: memory.AssertionAccepted, ObservedAt: base}
			plan := &mergePlan{source: source.Source{ID: "s", Content: json.RawMessage(`"更正血糖为5.6"`)}, complete: true}
			action := plannedAction{candidate: Candidate{Content: "血糖是5.6", ObservedAt: &later, FactGroup: &FactGroupProposal{Subject: "用户", Predicate: "血糖", Cardinality: memory.CardinalitySingle}}, evidence: []source.Evidence{{SourceID: "s", Quote: "更正血糖为5.6"}}}
			if test.mutate != nil {
				test.mutate(plan, &action, &group, &old)
			}
			if got := convergenceEligible(plan, action, group, []memory.Memory{old}); got != test.want {
				t.Fatalf("eligible=%v want=%v", got, test.want)
			}
		})
	}
}
