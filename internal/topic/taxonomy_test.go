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
