package artifact

import (
	"testing"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
)

func TestAggregateTargetKeysAreCanonicalAndUnambiguous(t *testing.T) {
	scope := "project:memory:test"
	topic := "go:sqlite"
	key := task.AggregateKey(scope, topic)
	parsedScope, parsedTopic, ok := task.ParseAggregateKey(key)
	if !ok || parsedScope != scope || parsedTopic != topic {
		t.Fatalf("aggregate key round trip = %q/%q/%v from %q", parsedScope, parsedTopic, ok, key)
	}
	profileKey := task.ProfileKey(scope)
	parsedProfile, ok := task.ParseProfileKey(profileKey)
	if !ok || parsedProfile != scope {
		t.Fatalf("profile key round trip = %q/%v from %q", parsedProfile, ok, profileKey)
	}
	if _, _, ok := task.ParseAggregateKey(profileKey); ok {
		t.Fatal("profile key parsed as an aggregate key")
	}
	if _, ok := task.ParseProfileKey(key); ok {
		t.Fatal("aggregate key parsed as a profile key")
	}
	if _, _, ok := task.ParseAggregateKey(task.AggregateKey("", topic)); ok {
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

func TestAggregateTargetsSkipUnknownScopeAndDeduplicateTopics(t *testing.T) {
	if targets := AggregateTargets("", []string{"go"}); len(targets) != 0 {
		t.Fatalf("empty scope targets = %+v", targets)
	}
	targets := AggregateTargets("project:dmemd", []string{"go", "GO", " sqlite ", ""})
	if len(targets) != 4 {
		t.Fatalf("targets = %+v", targets)
	}
	if targets[0].Kind != KindTopicSummary || targets[0].Topic != "go" ||
		targets[1].Kind != KindTopicSummary || targets[1].Topic != "sqlite" ||
		targets[2].Kind != KindScopeSummary || targets[2].Topic != "" ||
		targets[3].Kind != KindProfile {
		t.Fatalf("targets = %+v", targets)
	}
	if parsed, err := ParseTarget(KindTopicSummary, targets[0].TargetKey()); err != nil ||
		parsed.ScopeKey != "project:dmemd" || parsed.Topic != "go" {
		t.Fatalf("parsed target = %+v err=%v", parsed, err)
	}
	parsed, err := ParseTarget(KindScopeSummary, targets[2].TargetKey())
	if err != nil || parsed.ScopeKey != "project:dmemd" || parsed.Topic != "" {
		t.Fatalf("parsed scope summary = %+v err=%v", parsed, err)
	}
}
