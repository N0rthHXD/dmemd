package model

import (
	"errors"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/observation"
)

func TestParseExtractionDraftMinimalProtocol(t *testing.T) {
	content := "```json\n" + `{
		"items": [
			{"span_ids": ["seg-0001", "seg-0001", " seg-0002 "], "kind": "preference"},
			{"span_ids": ["seg-0003"], "content": "  默认使用中文回答。  "},
			{"span_ids": ["seg-0004"], "kind": "procedure", "content": "先检查配置，再运行部署。"}
		]
	}` + "\n```"

	result, err := ParseExtractionDraft(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Drafts) != 3 {
		t.Fatalf("drafts = %+v", result.Drafts)
	}
	first := result.Drafts[0]
	if len(first.SpanIDs) != 2 || first.SpanIDs[0] != "seg-0001" || first.SpanIDs[1] != "seg-0002" ||
		first.Kind != observation.KindPreference || first.Content != "" {
		t.Fatalf("first draft = %+v", first)
	}
	if second := result.Drafts[1]; second.Kind != "" || second.Content != "默认使用中文回答。" {
		t.Fatalf("normalized draft = %+v", second)
	}
	if third := result.Drafts[2]; third.Kind != observation.KindProcedure || third.Content != "先检查配置，再运行部署。" {
		t.Fatalf("procedure draft = %+v", third)
	}
}

func TestParseExtractionDraftIgnoresLegacyAndUnknownFields(t *testing.T) {
	result, err := ParseExtractionDraft(`{"items":[{
		"span_ids":["seg-0001"],
		"candidate_id":"c1",
		"title":"ignored",
		"evidence":[{"quote":"fabricated"}],
		"quote":"fabricated",
		"start":0,
		"end":3,
		"source_id":"source-1",
		"memory_id":"memory-1",
		"version":2,
		"observed_at":"2026-10-20T02:00:00Z",
		"valid_from":"2026-10-20",
		"phase":"completed",
		"durability":"durable",
		"topics":["ignored"],
		"fact_group":{"subject":"ignored"},
		"procedure":{"steps":["ignored"]},
		"qualifiers":{"polarity":"ignored"},
		"action":"change",
		"unknown":"ignored"
	}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Drafts) != 1 {
		t.Fatalf("drafts = %+v", result.Drafts)
	}
	draft := result.Drafts[0]
	if len(draft.SpanIDs) != 1 || draft.SpanIDs[0] != "seg-0001" || draft.Kind != "" || draft.Content != "" {
		t.Fatalf("draft = %+v", draft)
	}
}

func TestParseExtractionDraftSkipsItemsWithoutUsableSpans(t *testing.T) {
	result, err := ParseExtractionDraft(`{"items":[
		{"content":"缺少片段"},
		{"span_ids":[]},
		{"span_ids":["  "]},
		"not-an-object",
		{"span_ids":"seg-0001"},
		{"span_ids":["seg-0002"]}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Drafts) != 1 || result.Drafts[0].SpanIDs[0] != "seg-0002" {
		t.Fatalf("drafts = %+v", result.Drafts)
	}
}

func TestParseExtractionDraftZeroItems(t *testing.T) {
	for _, content := range []string{
		`{"items":[]}`,
		"```json\n{\"items\": []}\n```",
	} {
		result, err := ParseExtractionDraft(content)
		if err != nil {
			t.Fatalf("%q: %v", content, err)
		}
		if result.Drafts == nil || len(result.Drafts) != 0 {
			t.Fatalf("%q drafts = %#v", content, result.Drafts)
		}
	}
}

func TestParseExtractionDraftRejectsOverallUnparseable(t *testing.T) {
	for _, content := range []string{
		`{}`,
		`{"items":null}`,
		`{"candidates":[]}`,
		"",
		"I could not determine any memories.",
		`[]`,
		`null`,
		`{"items":{}}`,
		`{"items":[]} trailing`,
		"```json\n{\"items\":[]}\n```\ntrailing",
		"```text\n{\"items\":[]}\n```",
	} {
		if _, err := ParseExtractionDraft(content); !errors.Is(err, ErrUnparseable) {
			t.Fatalf("%q error = %v, want ErrUnparseable", content, err)
		}
	}
}

func TestParseRelationsUsesTemporaryRefsOnly(t *testing.T) {
	relations, err := ParseRelations(`{"relations":[
		{"from_ref":"new","to_ref":"existing_1","kind":"supersedes"},
		{"from_ref":"new","to_ref":"","kind":"unresolved"},
		{"from_ref":"new","to_ref":"","kind":"independent"},
		{"from_ref":"new","to_ref":"","kind":"separate"}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 4 || relations[0].FromRef != "new" || relations[0].ToRef != "existing_1" ||
		relations[0].Kind != governance.RelationSupersedes ||
		relations[1].Kind != governance.RelationUnresolved || relations[1].ToRef != "" ||
		relations[2].Kind != governance.RelationIndependent || relations[2].ToRef != "" ||
		relations[3].Kind != governance.RelationSeparate || relations[3].ToRef != "" {
		t.Fatalf("relations = %+v", relations)
	}
	for _, invalid := range []string{
		`{}`,
		`{"relations":null}`,
		`{"relations":[{"from_ref":"new","to_ref":"existing_1","kind":"change"}]}`,
		`{"relations":[{"from_ref":"","to_ref":"existing_1","kind":"supports"}]}`,
		`{"relations":[{"from_ref":"new","to_ref":"","kind":"supports"}]}`,
		`{"relations":[{"from_ref":"new","to_ref":"","kind":"duplicate"}]}`,
		`{"decisions":[{"candidate_id":"c1","action":"add"}]}`,
	} {
		if _, err := ParseRelations(invalid); !errors.Is(err, ErrUnparseable) {
			t.Fatalf("%q error = %v, want ErrUnparseable", invalid, err)
		}
	}
}

func TestParseRelationsIgnoresDecisionAndEvidenceFields(t *testing.T) {
	relations, err := ParseRelations(`{"relations":[{
		"from_ref":"new",
		"to_ref":"existing_2",
		"kind":"enriches",
		"action":"change",
		"content":"模型改写后的正文",
		"evidence":{"quote":"伪造证据"},
		"valid_from":"2026-10-20",
		"target_memory_ids":["mem-forged"],
		"candidate_id":"c1",
		"reason_code":"explicit_transition"
	}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 1 || relations[0].FromRef != "new" ||
		relations[0].ToRef != "existing_2" || relations[0].Kind != governance.RelationEnriches {
		t.Fatalf("relations = %+v", relations)
	}
}

func TestParseAggregationAcceptsOnlyBody(t *testing.T) {
	result, err := ParseAggregation(`{"content":"  项目使用 SQLite。  "}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "项目使用 SQLite。" || result.Title != "" {
		t.Fatalf("aggregation = %+v", result)
	}
	for _, content := range []string{
		`{"title":"只有标题"}`,
		`{"content":"   "}`,
		`{"content":"正文","citations":[{"memory_id":"m1","version":1}]}`,
		`not json`,
	} {
		if content == `{"content":"正文","citations":[{"memory_id":"m1","version":1}]}` {
			parsed, err := ParseAggregation(content)
			if err != nil || parsed.Content != "正文" {
				t.Fatalf("auxiliary fields must be ignored: result=%+v err=%v", parsed, err)
			}
			continue
		}
		if _, err := ParseAggregation(content); err == nil {
			t.Fatalf("ParseAggregation(%q) succeeded", content)
		}
	}
}

func TestParseExperienceAcceptsBodyOnly(t *testing.T) {
	result, err := ParseExperience(`{
		"content": "  问题：验证修改。\n行动：运行 go test。 ",
		"problem": "ignored",
		"outcome": "success",
		"memory_id": "forged",
		"skill_id": "forged"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "问题：验证修改。\n行动：运行 go test。" {
		t.Fatalf("result = %+v", result)
	}
	for _, content := range []string{
		`{"problem":"","actions":[],"lessons":[]}`,
		`{"content":""}`,
		`{}`,
		"not json",
	} {
		if _, err := ParseExperience(content); !errors.Is(err, ErrUnparseable) {
			t.Fatalf("ParseExperience(%q) error = %v", content, err)
		}
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
