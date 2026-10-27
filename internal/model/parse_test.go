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

	result, err := ParseCandidates(content)
	if err != nil {
		t.Fatal(err)
	}
	candidates := result.Candidates
	if len(candidates) != 5 || result.RawCandidates != 5 {
		t.Fatalf("candidates = %d raw=%d: %+v", len(candidates), result.RawCandidates, candidates)
	}
	if len(candidates[0].ProtocolIssues) == 0 || len(candidates[3].ProtocolIssues) == 0 || strings.TrimSpace(candidates[4].Content) != "" {
		t.Fatalf("invalid candidate diagnostics were lost: %+v", candidates)
	}
	first := candidates[0]
	if first.Kind != "" {
		t.Fatalf("parser must not silently default kind = %+v", first)
	}
	if !strings.HasPrefix(first.Title, "用户希望默认使用中文回答") || len([]rune(first.Title)) != 40 {
		t.Fatalf("default title = %q (%d runes)", first.Title, len([]rune(first.Title)))
	}
	if first.ObservedAt != nil {
		t.Fatalf("missing observed_at = %v", first.ObservedAt)
	}
	if len(first.Topics) != 3 || first.Topics[0] != "Go" || first.Topics[1] != "sqlite" || first.Topics[2] != "go" {
		t.Fatalf("topics = %#v", first.Topics)
	}
	if len(first.Evidence) != 2 || first.Evidence[0].SegmentID != "" || first.Evidence[0].Quote != "默认使用中文" ||
		first.Evidence[1].Quote != "bad message type" {
		t.Fatalf("evidence = %+v", first.Evidence)
	}
	if first.Evidence[0].Start == nil || *first.Evidence[0].Start != 2 ||
		first.Evidence[0].End == nil || *first.Evidence[0].End != 8 {
		t.Fatalf("evidence offsets = %+v", first.Evidence[0])
	}
	if candidates[1].Kind != "unknown" || candidates[1].Title != "自定义" {
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

func TestParseCandidatesV2MetadataAndProcedure(t *testing.T) {
	result, err := ParseCandidates(`{"candidates":[{"candidate_id":"c1","kind":"procedure","content":"先检查配置，再运行部署。","phase":"scheduled","durability":"durable","procedure":{"steps":["检查配置","运行部署"],"preconditions":["配置存在"],"completion_conditions":["部署完成"],"failure_branches":["配置缺失时停止"]},"evidence":[{"message_id":"m1","quote":"先检查配置，再运行部署。"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	candidates := result.Candidates
	if len(candidates) != 1 || candidates[0].CandidateID != "c1" ||
		candidates[0].Phase != ingest.PhaseScheduled || candidates[0].Durability != ingest.DurabilityDurable {
		t.Fatalf("candidate = %+v", candidates)
	}
	if candidates[0].Procedure == nil || len(candidates[0].Procedure.Steps) != 2 ||
		candidates[0].Procedure.FailureBranches[0] != "配置缺失时停止" {
		t.Fatalf("procedure = %+v", candidates[0].Procedure)
	}
}

func TestParseCandidatesObservedAtAndRuneTitle(t *testing.T) {
	content := `{"candidates":[
		{"content":"正文","observed_at":"2026-10-20T02:00:00Z"},
		{"content":"正文二","observed_at":"not-a-time"},
		{"content":"正文三","observed_at":123}
	]}`
	result, err := ParseCandidates(content)
	if err != nil {
		t.Fatal(err)
	}
	candidates := result.Candidates
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d", len(candidates))
	}
	want := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	if candidates[0].ObservedAt == nil || !candidates[0].ObservedAt.Equal(want) {
		t.Fatalf("observed_at = %v", candidates[0].ObservedAt)
	}
	if candidates[1].ObservedAt != nil || candidates[2].ObservedAt != nil {
		t.Fatalf("invalid observed_at parsed: %+v %+v", candidates[1].ObservedAt, candidates[2].ObservedAt)
	}
	if len(candidates[1].TemporalErrors) != 1 || len(candidates[2].TemporalErrors) != 1 {
		t.Fatalf("invalid observed_at diagnostics lost: %+v", candidates)
	}
	chinese := strings.Repeat("记", 50)
	result, err = ParseCandidates(`{"candidates":[{"content":"` + chinese + `"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(result.Candidates[0].Title)) != 40 {
		t.Fatalf("title runes = %d", len([]rune(result.Candidates[0].Title)))
	}
}

func TestParseCandidatesZeroCandidates(t *testing.T) {
	for _, content := range []string{
		`{"candidates":[]}`,
		"```json\n{\"candidates\": []}\n```",
	} {
		result, err := ParseCandidates(content)
		if err != nil {
			t.Fatalf("%q: %v", content, err)
		}
		if result.Candidates == nil || len(result.Candidates) != 0 {
			t.Fatalf("%q candidates = %#v", content, result.Candidates)
		}
	}
}

func TestParseCandidatesRejectsOverallUnparseable(t *testing.T) {
	for _, content := range []string{
		`{}`,
		`{"candidates":null}`,
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

func TestParseCandidatesNormalizesWhitelistedShapes(t *testing.T) {
	result, err := ParseCandidates(`{"candidates":[{
		"content":"先检查配置，再运行部署。",
		"kind":"procedure",
		"topics":"部署",
		"fact_group":[{"subject":"用户","predicate":"部署方式","cardinality":"single"}],
		"procedure":[{"steps":"检查配置","preconditions":["配置存在"],"completion_conditions":"部署完成","failure_branches":"配置缺失时停止"}],
		"evidence":{"segment_id":"seg-0001","quote":"先检查配置，再运行部署。"}
	}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %+v", result.Candidates)
	}
	candidate := result.Candidates[0]
	if len(candidate.Evidence) != 1 || candidate.Evidence[0].SegmentID != "seg-0001" {
		t.Fatalf("evidence = %+v", candidate.Evidence)
	}
	if candidate.FactGroup == nil || candidate.FactGroup.Subject != "用户" || candidate.FactGroup.Cardinality != memory.CardinalitySingle {
		t.Fatalf("fact group = %+v", candidate.FactGroup)
	}
	if candidate.Procedure == nil || len(candidate.Procedure.Steps) != 1 || candidate.Procedure.Steps[0] != "检查配置" ||
		len(candidate.Procedure.CompletionConditions) != 1 || len(candidate.Procedure.FailureBranches) != 1 {
		t.Fatalf("procedure = %+v", candidate.Procedure)
	}
	if len(candidate.Topics) != 1 || candidate.Topics[0] != "部署" {
		t.Fatalf("topics = %+v", candidate.Topics)
	}
	if len(candidate.ProtocolIssues) != 0 {
		t.Fatalf("unexpected protocol issues = %+v", candidate.ProtocolIssues)
	}
	normalized := map[string]bool{}
	for _, normalization := range candidate.Normalizations {
		normalized[normalization.Field+":"+normalization.Code] = true
	}
	for _, want := range []string{
		"evidence:shape_recovered", "topics:shape_recovered", "fact_group:shape_recovered",
		"procedure:shape_recovered", "procedure.steps:shape_recovered",
		"procedure.completion_conditions:shape_recovered", "procedure.failure_branches:shape_recovered",
	} {
		if !normalized[want] {
			t.Fatalf("missing normalization %q in %+v", want, candidate.Normalizations)
		}
	}
}

func TestParseCandidatesRejectsUnsafeShapesWithoutSilentDefaults(t *testing.T) {
	result, err := ParseCandidates(`{"candidates":[{
		"content":"正文",
		"kind":7,
		"fact_group":[{"subject":"a"},{"subject":"b"}],
		"procedure":{"steps":7},
		"evidence":"fabricated"
	}]}`)
	if err != nil {
		t.Fatal(err)
	}
	candidate := result.Candidates[0]
	if candidate.Kind != "" || candidate.FactGroup != nil || candidate.Procedure != nil || len(candidate.Evidence) != 0 {
		t.Fatalf("unsafe shapes were accepted: %+v", candidate)
	}
	if len(candidate.ProtocolIssues) < 4 {
		t.Fatalf("protocol issues = %+v", candidate.ProtocolIssues)
	}
	found := map[string]bool{}
	for _, issue := range candidate.ProtocolIssues {
		found[issue.Field] = true
	}
	for _, field := range []string{"kind", "fact_group", "procedure.steps", "evidence"} {
		if !found[field] {
			t.Fatalf("missing issue for %s in %+v", field, candidate.ProtocolIssues)
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

func TestParseAggregateKeepsValidCitationsAndDropsMalformedAuxiliaryItems(t *testing.T) {
	result, err := ParseAggregate(`{"title":"摘要","content":"正文",
		"citations":[
			{"section":"现状","point":"使用 SQLite","memory_id":"m1","version":2},
			{"point":"","memory_id":"m1","version":2},
			{"point":"missing version","memory_id":"m2","version":0}
		],
		"omitted_inputs":[
			{"memory_id":"m3","version":1,"reason":"not covered"},
			{"memory_id":"","version":1}
		]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Citations) != 1 || result.Citations[0].MemoryID != "m1" ||
		result.Citations[0].Version != 2 || result.Citations[0].Section != "现状" {
		t.Fatalf("citations = %+v", result.Citations)
	}
	if len(result.OmittedInputs) != 1 || result.OmittedInputs[0].MemoryID != "m3" ||
		result.OmittedInputs[0].Reason != "not covered" {
		t.Fatalf("omissions = %+v", result.OmittedInputs)
	}
}

func TestParseFactGroupConditionsShape(t *testing.T) {
	result, err := ParseCandidates(`{"candidates":[{"content":"白天使用中文回答","fact_group":{"subject":"用户","predicate":"回答语言","conditions":"白天","cardinality":"single"}}]}`)
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].FactGroup == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	group := result.Candidates[0].FactGroup
	if len(group.Conditions) != 1 || group.Conditions[0] != "白天" {
		t.Fatalf("conditions=%+v", group.Conditions)
	}
}
