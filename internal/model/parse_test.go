package model

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestParseCandidatesDefaultsPartialAndEvidence(t *testing.T) {
	content := "```json\n" + `{
		"candidates": [
			{
				"content": "  用户希望默认使用中文回答，并且这是一个超过四十个字符的正文用于验证默认标题只取前四十个 Unicode 字符。  ",
				"topics": ["Go", " sqlite ", "go", ""],
				"evidence": [
					{"message_id": "m1", "quote": "默认使用中文", "field": "messages.0.content", "start": 2, "end": 8},
					{"message_id": 7, "quote": "bad message type"},
					{"message_id": "m2", "quote": 42},
					{"message_id": "m3", "quote": "保留这个证据", "start": "bad"}
				]
			},
			{"kind": "unknown", "title": "自定义", "content": "另一条事实"},
			{"kind": "preference", "content": "偏好正文", "valid_from": "2026-06-15T02:00:00Z",
			 "valid_from_basis": "explicit", "valid_to_basis": "unknown",
			 "fact_group": {"subject": "用户", "predicate": "回答语言", "cardinality": "single"}},
			"not-an-object",
			{"content": "   "}
		]
	}` + "\n```"

	candidates, err := ParseCandidates(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d: %+v", len(candidates), candidates)
	}
	first := candidates[0]
	if first.Kind != memory.KindFact {
		t.Fatalf("defaults = %+v", first)
	}
	if !strings.HasPrefix(first.Title, "用户希望默认使用中文回答") || len([]rune(first.Title)) != 40 {
		t.Fatalf("default title = %q (%d runes)", first.Title, len([]rune(first.Title)))
	}
	if first.ObservedAt != nil {
		t.Fatalf("missing observed_at = %v", first.ObservedAt)
	}
	if len(first.Topics) != 2 || first.Topics[0] != "go" || first.Topics[1] != "sqlite" {
		t.Fatalf("topics = %#v", first.Topics)
	}
	if len(first.Evidence) != 1 || first.Evidence[0].MessageID != "m1" || first.Evidence[0].Quote != "默认使用中文" {
		t.Fatalf("evidence = %+v", first.Evidence)
	}
	if first.Evidence[0].Location == nil || first.Evidence[0].Location.Field != "messages.0.content" ||
		first.Evidence[0].Location.Start != 2 || first.Evidence[0].Location.End != 8 {
		t.Fatalf("evidence location = %+v", first.Evidence[0].Location)
	}
	if candidates[1].Kind != memory.KindFact || candidates[1].Title != "自定义" {
		t.Fatalf("unknown values = %+v", candidates[1])
	}
	third := candidates[2]
	if third.Kind != memory.KindPreference || third.ValidFrom == nil || third.ValidFromBasis != memory.BasisExplicit ||
		third.ValidTo != nil || third.ValidToBasis != memory.BasisUnknown {
		t.Fatalf("temporal candidate = %+v", third)
	}
	if third.FactGroup == nil || third.FactGroup.Subject != "用户" || third.FactGroup.Cardinality != memory.CardinalitySingle {
		t.Fatalf("fact group = %+v", third.FactGroup)
	}
	_ = errors.Is(err, ErrUnparseable)
}

func TestParseCandidatesObservedAtAndRuneTitle(t *testing.T) {
	content := `{"candidates":[
		{"content":"正文","observed_at":"2026-10-20T02:00:00Z"},
		{"content":"正文二","observed_at":"not-a-time"},
		{"content":"正文三","observed_at":123}
	]}`
	candidates, err := ParseCandidates(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d", len(candidates))
	}
	want := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	if candidates[0].ObservedAt == nil || !candidates[0].ObservedAt.Equal(want) {
		t.Fatalf("observed_at = %v", candidates[0].ObservedAt)
	}
	if candidates[1].ObservedAt != nil || candidates[2].ObservedAt != nil {
		t.Fatalf("invalid observed_at should fall back later: %+v %+v", candidates[1].ObservedAt, candidates[2].ObservedAt)
	}
	chinese := strings.Repeat("记", 50)
	title, err := ParseCandidates(`{"candidates":[{"content":"` + chinese + `"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(title[0].Title)) != 40 {
		t.Fatalf("title runes = %d", len([]rune(title[0].Title)))
	}
}

func TestParseCandidatesZeroCandidates(t *testing.T) {
	for _, content := range []string{
		`{}`,
		`{"candidates":null}`,
		`{"candidates":[]}`,
		"```json\n{\"candidates\": []}\n```",
	} {
		candidates, err := ParseCandidates(content)
		if err != nil {
			t.Fatalf("%q: %v", content, err)
		}
		if candidates == nil || len(candidates) != 0 {
			t.Fatalf("%q candidates = %#v", content, candidates)
		}
	}
}

func TestParseCandidatesRejectsOverallUnparseable(t *testing.T) {
	for _, content := range []string{
		"",
		"I could not determine any memories.",
		`[]`,
		`null`,
		`{"candidates":{}}`,
		`{"candidates":[]} trailing`,
		"```json\n{\"candidates\":[]}\n```\ntrailing",
		"```text\n{\"candidates\":[]}\n```",
	} {
		if _, err := ParseCandidates(content); !errors.Is(err, ErrUnparseable) {
			t.Fatalf("%q error = %v, want ErrUnparseable", content, err)
		}
	}
}

func TestParseDecisionsRequiresCoverageShapeAndValidAction(t *testing.T) {
	content := `{"decisions":[
		{"candidate_id":"c1","action":"add"},
		{"candidate_id":"c2","action":"change","target_memory_ids":["m1"],"content":"常住北京。",
		 "valid_from":"2026-06-15T02:00:00Z","valid_from_basis":"explicit","valid_to_basis":"unknown",
		 "reason_code":"explicit_transition","evidence_refs":[{"candidate_id":"c2","evidence_index":0}]}
	]}`
	decisions, err := ParseDecisions(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 || decisions[0].Action != ingest.ActionAdd || decisions[1].Action != ingest.ActionChange {
		t.Fatalf("decisions = %+v", decisions)
	}
	if len(decisions[1].TargetMemoryIDs) != 1 || decisions[1].TargetMemoryIDs[0] != "m1" ||
		decisions[1].ValidFrom == nil || decisions[1].ValidFromBasis != memory.BasisExplicit {
		t.Fatalf("change = %+v", decisions[1])
	}
	if len(decisions[1].EvidenceRefs) != 1 || decisions[1].EvidenceRefs[0].EvidenceIndex != 0 {
		t.Fatalf("evidence refs = %+v", decisions[1].EvidenceRefs)
	}

	for _, invalid := range []string{
		`{}`,
		`{"decisions":{}}`,
		`{"decisions":[{"action":"add"}]}`,
		`{"decisions":[{"candidate_id":"c1"}]}`,
		`{"decisions":[{"candidate_id":"c1","action":"invent"}]}`,
	} {
		if _, err := ParseDecisions(invalid); !errors.Is(err, ErrUnparseable) {
			t.Fatalf("%q error = %v, want ErrUnparseable", invalid, err)
		}
	}
}

func TestParseExperienceAcceptsPartialStructuredCase(t *testing.T) {
	result, err := ParseExperience(`{
		"problem": "验证修改",
		"preconditions": ["模型可用", 7],
		"actions": ["运行 go test"],
		"lessons": ["先测试"],
		"failure_modes": ["跳过构建"],
		"outcome": "success"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Problem != "验证修改" || len(result.Actions) != 1 || len(result.Lessons) != 1 ||
		len(result.FailureModes) != 1 || result.Content != "" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := ParseExperience(`{"problem":"","actions":[],"lessons":[]}`); !errors.Is(err, ErrUnparseable) {
		t.Fatalf("empty experience error = %v", err)
	}
	if _, err := ParseExperience("not json"); !errors.Is(err, ErrUnparseable) {
		t.Fatalf("malformed experience error = %v", err)
	}
}

func TestParseSkillRequiresUsableGuidanceAndKeepsSections(t *testing.T) {
	draft, err := ParseSkill(`{
		"name": "验证工作流",
		"description": "修改后的验证",
		"preconditions": ["依赖安装完成"],
		"steps": ["运行 go test", "执行构建"],
		"stop_conditions": ["测试失败"],
		"completion_checks": ["全部命令通过"],
		"counterexamples": ["不要跳过测试"]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Name != "验证工作流" || draft.Content != "" || len(draft.Steps) != 2 ||
		len(draft.CompletionChecks) != 1 || len(draft.Counterexamples) != 1 || !draft.Usable() {
		t.Fatalf("draft = %+v", draft)
	}
	if _, err := ParseSkill(`{"name":"only name"}`); !errors.Is(err, ErrUnparseable) {
		t.Fatalf("empty skill error = %v", err)
	}
}

func TestParseAggregateKeepsUsableBodyAndDefaultsMissingTitle(t *testing.T) {
	result, err := ParseAggregate("```json\n" + `{"content":"  项目使用 SQLite。  "}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "项目使用 SQLite。" || result.Title != "" {
		t.Fatalf("aggregate = %+v", result)
	}
	for _, content := range []string{
		`{"title":"只有标题"}`,
		`{"content":"   "}`,
		`not json`,
	} {
		if _, err := ParseAggregate(content); err == nil {
			t.Fatalf("ParseAggregate(%q) succeeded", content)
		}
	}
}
