package artifact

import (
	"testing"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

func TestLayerTargetKeysAreCanonical(t *testing.T) {
	l1 := Target{
		Layer: LayerL1, Kind: KindLocalSummary,
		ScopeKey: "project:dmemd", Bucket: "2026-10-29T00:00",
	}
	if got, want := l1.TargetKey(), task.L1TargetKey("project:dmemd", "2026-10-29T00:00"); got != want {
		t.Fatalf("L1 target key = %q, want %q", got, want)
	}
	parsed, err := ParseTargetKey(l1.TargetKey())
	if err != nil || parsed.Layer != LayerL1 || parsed.Bucket != l1.Bucket ||
		parsed.ScopeKey != l1.ScopeKey {
		t.Fatalf("parsed L1 target = %+v err=%v", parsed, err)
	}

	l2 := Target{
		Layer: LayerL2, Kind: KindTopicSummary,
		ScopeKey: "project:dmemd", Topic: "storage",
	}
	if got, want := l2.TargetKey(), task.L2TargetKey("project:dmemd", "storage"); got != want {
		t.Fatalf("L2 target key = %q, want %q", got, want)
	}
	parsed, err = ParseTargetKey(l2.TargetKey())
	if err != nil || parsed.Layer != LayerL2 || parsed.Topic != "storage" {
		t.Fatalf("parsed L2 target = %+v err=%v", parsed, err)
	}

	l3 := Target{Layer: LayerL3, Kind: KindScopeSummary, ScopeKey: "project:dmemd"}
	if got, want := l3.TargetKey(), task.L3TargetKey("project:dmemd"); got != want {
		t.Fatalf("L3 target key = %q, want %q", got, want)
	}
	parsed, err = ParseTargetKey(l3.TargetKey())
	if err != nil || parsed.Layer != LayerL3 || parsed.ScopeKey != l3.ScopeKey {
		t.Fatalf("parsed L3 target = %+v err=%v", parsed, err)
	}

	l4 := Target{
		Layer: LayerL4, Kind: KindGuidance,
		ScopeKey: "project:dmemd", Key: "testing",
	}
	if got, want := l4.TargetKey(), task.DerivedKey(LayerL4, "project:dmemd", "testing"); got != want {
		t.Fatalf("L4 target key = %q, want %q", got, want)
	}
	parsed, err = ParseTargetKey(l4.TargetKey())
	if err != nil || parsed.Layer != LayerL4 || parsed.Key != "testing" {
		t.Fatalf("parsed L4 target = %+v err=%v", parsed, err)
	}
}

func TestParseTargetPreservesStoredKind(t *testing.T) {
	key := task.L2TargetKey("project:dmemd", "storage")
	target, err := ParseTarget(KindProfile, key)
	if err != nil {
		t.Fatal(err)
	}
	if target.Layer != LayerL2 || target.Kind != KindProfile || target.Topic != "storage" {
		t.Fatalf("target = %+v", target)
	}
	if _, err := ParseTarget(KindLocalSummary, task.L3TargetKey("project:dmemd")); err == nil {
		t.Fatal("cross-layer target was accepted")
	}
}

func TestDependencyRefsAreNormalized(t *testing.T) {
	refs := DependencyRefs([]InputRef{
		{Type: DependencyObservation, ID: "obs-1"},
		{Type: DependencyObservation, ID: "obs-1"},
		{Type: DependencyDerived, ID: "l1-1", Version: 2},
		{Type: DependencyType("bogus"), ID: "x"},
	})
	if len(refs) != 2 {
		t.Fatalf("refs = %+v", refs)
	}
	if !refs[0].Required {
		t.Fatalf("dependency is not required: %+v", refs[0])
	}
}
