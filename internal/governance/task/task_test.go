package task_test

import (
	"strings"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

func TestObservationTargetKeyRoundTrip(t *testing.T) {
	key := task.ObservationKey("observation/with:separators")
	decoded, ok := task.ParseObservationKey(key)
	if !ok || decoded != "observation/with:separators" {
		t.Fatalf("decoded = %q, ok=%v", decoded, ok)
	}
	if _, ok := task.ParseObservationKey("raw-id"); ok {
		t.Fatal("raw identifier parsed as observation key")
	}
}

func TestAggregateTargetKeysAreUnambiguous(t *testing.T) {
	scope := "project:a"
	bucket := "2026-10-29"
	key := task.L1TargetKey(scope, bucket)
	decodedScope, decodedBucket, ok := task.ParseL1TargetKey(key)
	if !ok || decodedScope != scope || decodedBucket != bucket {
		t.Fatalf("L1 decoded = %q/%q ok=%v", decodedScope, decodedBucket, ok)
	}
	key = task.L2TargetKey(scope, "go")
	decodedScope, domain, ok := task.ParseL2TargetKey(key)
	if !ok || decodedScope != scope || domain != "go" {
		t.Fatalf("L2 decoded = %q/%q ok=%v", decodedScope, domain, ok)
	}
	key = task.L3TargetKey(scope)
	decodedScope, ok = task.ParseL3TargetKey(key)
	if !ok || decodedScope != scope {
		t.Fatalf("L3 decoded = %q ok=%v", decodedScope, ok)
	}
	if strings.Contains(key, scope) {
		t.Fatalf("scope leaked into raw key %q", key)
	}
}

func TestAggregateKindForTargetKey(t *testing.T) {
	for _, tc := range []struct {
		key  string
		kind task.Kind
	}{
		{key: task.L1TargetKey("scope", "bucket"), kind: task.KindAggregateL1},
		{key: task.L2TargetKey("scope", "domain"), kind: task.KindAggregateL2},
		{key: task.L3TargetKey("scope"), kind: task.KindAggregateL3},
	} {
		kind, ok := task.AggregateKindForTargetKey(tc.key)
		if !ok || kind != tc.kind {
			t.Fatalf("key %q = %q/%v, want %q", tc.key, kind, ok, tc.kind)
		}
	}
	if kind, ok := task.AggregateKindForTargetKey("summary:scope:topic"); ok || kind != "" {
		t.Fatalf("unknown key = %q/%v", kind, ok)
	}
}

func TestDerivedKeyAcceptsArtifactLayer(t *testing.T) {
	scope := "project:dmemd"
	if got := task.DerivedKey(artifact.LayerL1, scope, "bucket"); got != task.L1TargetKey(scope, "bucket") {
		t.Fatalf("L1 derived key = %q", got)
	}
	if got := task.DerivedKey(artifact.LayerL2, scope, "domain"); got != task.L2TargetKey(scope, "domain") {
		t.Fatalf("L2 derived key = %q", got)
	}
	if got := task.DerivedKey(artifact.LayerL3, scope, "ignored"); got != task.L3TargetKey(scope) {
		t.Fatalf("L3 derived key = %q", got)
	}
	if got := task.DerivedKey(artifact.LayerL4, scope, "guidance:go"); !strings.HasPrefix(got, "l4:") {
		t.Fatalf("L4 derived key = %q", got)
	}
	if got := task.DerivedKey("", scope, "key"); !strings.HasPrefix(got, "derived:") {
		t.Fatalf("empty-layer derived key = %q", got)
	}
}
