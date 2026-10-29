package artifact

import (
	"testing"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

func TestLayerTargetKeysAreCanonicalAndUnambiguous(t *testing.T) {
	scope := "project:memory:test"
	key := task.L2TargetKey(scope, "go:sqlite")
	parsedScope, parsedTopic, ok := task.ParseL2TargetKey(key)
	if !ok || parsedScope != scope || parsedTopic != "go:sqlite" {
		t.Fatalf("L2 key round trip = %q/%q/%v from %q", parsedScope, parsedTopic, ok, key)
	}
	profile := Target{Layer: LayerL2, Kind: KindProfile, ScopeKey: scope}
	if got := profile.TargetKey(); got != task.L2TargetKey(scope, "profile") {
		t.Fatalf("profile target key = %q", got)
	}
	if _, _, ok := task.ParseL2TargetKey(task.L2TargetKey("", "topic")); ok {
		t.Fatal("empty scope was accepted")
	}
}

func TestSkillAndExperienceTargetKeysRoundTrip(t *testing.T) {
	skillKey := task.SkillKey("project:dmemd", "")
	scope, topic, ok := task.ParseSkillKey(skillKey)
	if !ok || scope != "project:dmemd" || topic != "" {
		t.Fatalf("scope skill key = %q/%q/%v from %q", scope, topic, ok, skillKey)
	}
	target := Target{Kind: KindSkill, ScopeKey: "project:dmemd"}
	if target.TargetKey() != skillKey {
		t.Fatalf("skill target key = %q", target.TargetKey())
	}
	parsed, err := ParseTarget(KindSkill, skillKey)
	if err != nil || parsed.ScopeKey != "project:dmemd" || parsed.Topic != "" {
		t.Fatalf("parsed skill target = %+v err=%v", parsed, err)
	}
	experienceKey := task.ExperienceKey("experience-1")
	id, ok := task.ParseExperienceKey(experienceKey)
	if !ok || id != "experience-1" {
		t.Fatalf("experience key = %q/%v from %q", id, ok, experienceKey)
	}
	if got := ExperienceTarget("experience-1").TargetKey(); got != experienceKey {
		t.Fatalf("experience target key = %q", got)
	}
	if _, _, ok := task.ParseSkillKey(experienceKey); ok {
		t.Fatal("experience key parsed as a skill key")
	}
}

func TestScopeTargetUsesL3Key(t *testing.T) {
	target := Target{Layer: LayerL3, Kind: KindScopeSummary, ScopeKey: "project:dmemd"}
	if got, want := target.TargetKey(), task.L3TargetKey("project:dmemd"); got != want {
		t.Fatalf("scope target key = %q, want %q", got, want)
	}
	parsed, err := ParseTarget(KindScopeSummary, target.TargetKey())
	if err != nil || parsed.ScopeKey != "project:dmemd" || parsed.Layer != LayerL3 {
		t.Fatalf("parsed scope target = %+v err=%v", parsed, err)
	}
}

func TestLayerAndDependencyVocabulary(t *testing.T) {
	for _, layer := range []Layer{LayerL1, LayerL2, LayerL3, LayerL4} {
		if !layer.Known() {
			t.Fatalf("layer %q is not known", layer)
		}
	}
	if Layer("l5").Known() {
		t.Fatal("unknown layer was accepted")
	}
	for _, kind := range []DependencyType{
		DependencyObservation, DependencyMemory, DependencyDerived, DependencyExperience,
	} {
		if !kind.Known() {
			t.Fatalf("dependency type %q is not known", kind)
		}
	}
	if DependencyType("unknown").Known() {
		t.Fatal("unknown dependency type was accepted")
	}
	ref := DependencyRef{Type: DependencyDerived, ID: "derived-1", Version: 2, Required: true}
	if ref.Type != DependencyDerived || ref.Version != 2 || !ref.Required {
		t.Fatalf("dependency ref = %+v", ref)
	}
}
