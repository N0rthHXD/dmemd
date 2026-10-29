package source_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestBuildViewSplitsShortTextIntoClauseSpans(t *testing.T) {
	content := json.RawMessage(`{"messages":[{"id":"m1","role":"user","content":"项目使用 SQLite。团队用 Go 开发，部署在 Linux。\n第三行"}]}`)
	view := source.BuildView(content)
	want := []struct {
		text  string
		start int
		end   int
	}{
		{"项目使用 SQLite。", 0, 12},
		{"团队用 Go 开发，", 12, 22},
		{"部署在 Linux。", 22, 32},
		{"第三行", 33, 36},
	}
	if len(view.Segments) != len(want) {
		t.Fatalf("segments = %+v", view.Segments)
	}
	for index, expected := range want {
		segment := view.Segments[index]
		if segment.Text != expected.text || segment.Start != expected.start || segment.End != expected.end {
			t.Fatalf("segment %d = %+v, want %+v", index, segment, expected)
		}
		if segment.ID != fmt.Sprintf("seg-%04d", index+1) {
			t.Fatalf("segment %d ID = %q", index, segment.ID)
		}
		if segment.MessageID != "m1" || segment.Role != "user" || segment.Field != "messages.0.content" {
			t.Fatalf("segment %d context = %+v", index, segment)
		}
	}
	again := source.BuildView(content)
	if len(again.Segments) != len(view.Segments) {
		t.Fatalf("rebuild segments = %+v", again.Segments)
	}
	for index := range view.Segments {
		if again.Segments[index] != view.Segments[index] {
			t.Fatalf("rebuild segment %d changed: %+v vs %+v", index, view.Segments[index], again.Segments[index])
		}
	}
}

func TestBuildViewClauseBoundariesKeepDecimalsAndClosingQuotes(t *testing.T) {
	view := source.BuildView(json.RawMessage(`{"text":"Version 1.2 stays. “你好。” Then left!"}`))
	want := []string{"Version 1.2 stays.", "“你好。”", "Then left!"}
	if len(view.Segments) != len(want) {
		t.Fatalf("segments = %+v", view.Segments)
	}
	for index, text := range want {
		if view.Segments[index].Text != text {
			t.Fatalf("segment %d = %q, want %q", index, view.Segments[index].Text, text)
		}
	}
}

func TestResolveSegmentsDeduplicatesAndRejectsContext(t *testing.T) {
	view := source.BuildView(json.RawMessage(`{"text":"第一句。第二句，第三句。"}`))
	segments, err := source.ResolveSegments(view, []string{"seg-0002", "seg-0001", "seg-0002"})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 || segments[0].ID != "seg-0002" || segments[1].ID != "seg-0001" {
		t.Fatalf("segments = %+v", segments)
	}
	if _, err := source.ResolveSegments(view, nil); !errors.Is(err, source.ErrEmptySpans) {
		t.Fatalf("empty selection error = %v", err)
	}
	if _, err := source.ResolveSegments(view, []string{"seg-9999"}); !errors.Is(err, source.ErrUnknownSegment) {
		t.Fatalf("unknown selection error = %v", err)
	}
	withContext := view
	withContext.ContextSegments = []source.Segment{{ID: "seg-context", Text: "上一窗口的上下文"}}
	if _, err := source.ResolveSegments(withContext, []string{"seg-context"}); !errors.Is(err, source.ErrContextSegment) {
		t.Fatalf("context selection error = %v", err)
	}
}

func TestEvidenceForSegmentsCopiesCodeOwnedTruth(t *testing.T) {
	at := time.Date(2026, 10, 20, 2, 0, 0, 0, time.FixedZone("CST", 8*3600))
	src := source.Source{
		ID: "source-span",
		Content: []byte(
			`{"messages":[{"id":"m1","role":"user","content":"第一句。第二句。"}]}`,
		),
	}
	items, err := source.EvidenceForSegments(src, []string{"seg-0002", "seg-0001", "seg-0002"}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("evidence = %+v", items)
	}
	second := items[0]
	if second.ID == "" || second.ID == items[1].ID {
		t.Fatalf("evidence IDs = %q %q", second.ID, items[1].ID)
	}
	if second.SourceID != src.ID || second.SegmentID != "seg-0002" || second.MessageID != "m1" ||
		second.Quote != "第二句。" || second.Location == nil {
		t.Fatalf("second evidence = %+v", second)
	}
	if second.Location.Field != "messages.0.content" || second.Location.Start != 4 || second.Location.End != 8 {
		t.Fatalf("second location = %+v", second.Location)
	}
	if !second.CreatedAt.Equal(at.UTC()) {
		t.Fatalf("created_at = %v", second.CreatedAt)
	}
	first := items[1]
	if first.SegmentID != "seg-0001" || first.Quote != "第一句。" || first.Location == nil ||
		first.Location.Start != 0 || first.Location.End != 4 {
		t.Fatalf("first evidence = %+v", first)
	}

	if _, err := source.EvidenceForSegments(src, nil, at); !errors.Is(err, source.ErrEmptySpans) {
		t.Fatalf("empty selection error = %v", err)
	}
	if _, err := source.EvidenceForSegments(src, []string{"seg-9999"}, at); !errors.Is(err, source.ErrUnknownSegment) {
		t.Fatalf("unknown selection error = %v", err)
	}
}

func TestEvidenceForSegmentsRedactedSourceKeepsOnlyAssociation(t *testing.T) {
	at := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	redactedAt := at.Add(time.Hour)
	src := source.Source{ID: "source-redacted", Redacted: true, RedactedAt: &redactedAt}
	items, err := source.EvidenceForSegments(src, []string{"seg-0001", "seg-0001"}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("evidence = %+v", items)
	}
	item := items[0]
	if item.ID == "" || item.SourceID != src.ID || item.SegmentID != "seg-0001" ||
		item.Quote != "" || item.Location != nil || !item.Redacted || item.RedactedAt == nil ||
		!item.RedactedAt.Equal(redactedAt) {
		t.Fatalf("redacted evidence = %+v", item)
	}
}
