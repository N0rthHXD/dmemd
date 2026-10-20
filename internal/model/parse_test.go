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
				"evidence": [
					{"message_id": "m1", "quote": "默认使用中文", "field": "messages.0.content", "start": 2, "end": 8},
					{"message_id": 7, "quote": "bad message type"},
					{"message_id": "m2", "quote": 42},
					{"message_id": "m3", "quote": "保留这个证据", "start": "bad"}
				]
			},
			{"action": "unknown", "kind": "unknown", "title": "自定义", "content": "另一条事实"},
			{"action": "update", "target_memory_id": "target-1", "kind": "preference", "content": "偏好正文"},
			{"action": "skip"},
			"not-an-object",
			{"action": "add", "content": "   "}
		]
	}` + "\n```"

	candidates, err := ParseCandidates(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 4 {
		t.Fatalf("candidates = %d: %+v", len(candidates), candidates)
	}
	first := candidates[0]
	if first.Action != ingest.ActionAdd || first.Kind != memory.KindFact {
		t.Fatalf("defaults = %+v", first)
	}
	if !strings.HasPrefix(first.Title, "用户希望默认使用中文回答") || len([]rune(first.Title)) != 40 {
		t.Fatalf("default title = %q (%d runes)", first.Title, len([]rune(first.Title)))
	}
	if first.ObservedAt != nil {
		t.Fatalf("missing observed_at = %v", first.ObservedAt)
	}
	if len(first.Evidence) != 1 || first.Evidence[0].MessageID != "m1" || first.Evidence[0].Quote != "默认使用中文" {
		t.Fatalf("evidence = %+v", first.Evidence)
	}
	if first.Evidence[0].Location == nil || first.Evidence[0].Location.Field != "messages.0.content" ||
		first.Evidence[0].Location.Start != 2 || first.Evidence[0].Location.End != 8 {
		t.Fatalf("evidence location = %+v", first.Evidence[0].Location)
	}
	if candidates[1].Action != ingest.ActionAdd || candidates[1].Kind != memory.KindFact || candidates[1].Title != "自定义" {
		t.Fatalf("unknown values = %+v", candidates[1])
	}
	if candidates[2].Action != ingest.ActionUpdate || candidates[2].TargetMemoryID != "target-1" ||
		candidates[2].Kind != memory.KindPreference {
		t.Fatalf("update candidate = %+v", candidates[2])
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
