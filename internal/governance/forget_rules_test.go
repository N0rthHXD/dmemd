package governance

import (
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestForgetDuplicateRequiresSafeIdentityContentAndTime(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	base := memory.Memory{
		FactGroupID: "group", ScopeKey: "project:dmemd", Kind: memory.KindFact,
		Content: "用户血糖 5.6 mmol/L。", ValidFrom: &now, ValidTo: &later,
	}
	for _, test := range []struct {
		name   string
		change func(*memory.Memory)
		want   bool
	}{
		{"identical", func(m *memory.Memory) {}, true},
		{"formatting", func(m *memory.Memory) { m.Content = "  用户血糖   5.6 MMOL/l！  " }, true},
		{"quantity", func(m *memory.Memory) { m.Content = "用户血糖 5.7 mmol/L。" }, false},
		{"decimal", func(m *memory.Memory) { m.Content = "用户血糖 56 mmol/L。" }, false},
		{"negation", func(m *memory.Memory) { m.Content = "用户血糖不是 5.6 mmol/L。" }, false},
		{"subject", func(m *memory.Memory) { m.Content = "女儿血糖 5.6 mmol/L。" }, false},
		{"scope", func(m *memory.Memory) { m.ScopeKey = "project:other" }, false},
		{"group", func(m *memory.Memory) { m.FactGroupID = "other" }, false},
		{"missing-group", func(m *memory.Memory) { m.FactGroupID = "" }, false},
		{"kind", func(m *memory.Memory) { m.Kind = memory.KindEvent }, false},
		{"phase", func(m *memory.Memory) { m.Phase = memory.PhaseCancelled }, false},
		{"unknown-time", func(m *memory.Memory) { m.ValidFrom, m.ValidTo = nil, nil }, false},
		{"nonoverlap", func(m *memory.Memory) { m.ValidFrom, m.ValidTo = &later, nil }, false},
		{"overlap", func(m *memory.Memory) { m.ValidTo = nil }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.change(&candidate)
			if got := forgetDuplicate(base, candidate); got != test.want {
				t.Fatalf("duplicate = %v, want %v", got, test.want)
			}
		})
	}
	base.ValidTo = base.ValidFrom
	if !forgetDuplicate(base, base) {
		t.Fatal("equal point intervals must be duplicates")
	}
	base.ValidFrom, base.ValidTo = nil, nil
	if !forgetDuplicate(base, base) {
		t.Fatal("equal unknown intervals must be duplicates")
	}
}
