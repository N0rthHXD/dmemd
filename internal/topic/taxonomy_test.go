package topic

import (
	"reflect"
	"testing"
)

func TestCanonicalAliasesAndAncestors(t *testing.T) {
	got := Canonical([]string{" K8s ", "节点内存耗尽", "kubernetes", "香港", "Custom-Project"})
	want := []string{"containers", "custom-project", "hong-kong", "kubernetes", "oom", "travel"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(Canonical(got), got) {
		t.Fatal("canonicalization is not idempotent")
	}
	if got := Raw([]string{" K8s ", "节点内存耗尽", "K8s"}); !reflect.DeepEqual(got, []string{"K8s", "节点内存耗尽"}) {
		t.Fatalf("raw = %v", got)
	}
}

func TestCanonicalSplitsOnlyKnownConceptPaths(t *testing.T) {
	known := Canonical([]string{"containers/docker"})
	if !reflect.DeepEqual(known, []string{"containers", "docker"}) {
		t.Fatalf("known path = %v", known)
	}
	unknown := Canonical([]string{"atlas/river"})
	if !reflect.DeepEqual(unknown, []string{"atlas/river"}) {
		t.Fatalf("unknown path was split: %v", unknown)
	}
	mixed := Canonical([]string{"containers/unknown-project"})
	if !reflect.DeepEqual(mixed, []string{"containers/unknown-project"}) {
		t.Fatalf("mixed path was split: %v", mixed)
	}
	if got := Raw([]string{"containers/docker"}); !reflect.DeepEqual(got, []string{"containers/docker"}) {
		t.Fatalf("raw path = %v", got)
	}
}
