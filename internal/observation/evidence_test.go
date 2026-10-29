package observation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestBuildFromDraftUsesCodeOwnedEvidenceAndExtractiveContent(t *testing.T) {
	src := source.Source{
		ID:       "source-1",
		ScopeKey: "project:dmemd",
		Content:  json.RawMessage(`{"messages":[{"id":"m1","role":"user","content":"项目使用 SQLite。团队用 Go。"}]}`),
	}
	view := source.BuildView(src.Content)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	item, evidence, err := BuildFromDraft(src, view, Draft{
		SpanIDs: []string{view.Segments[0].ID},
		Kind:    KindFact,
	}, "extraction-v6", now)
	if err != nil {
		t.Fatal(err)
	}
	if item.Content != "项目使用 SQLite。" || item.ContentMode != ContentExtractive ||
		item.Status != StatusObserved || item.OriginRole != "user" || item.ScopeKey != "project:dmemd" {
		t.Fatalf("observation = %+v", item)
	}
	if len(evidence) != 1 {
		t.Fatalf("evidence = %+v", evidence)
	}
	if evidence[0].Quote != "项目使用 SQLite。" || evidence[0].SegmentID != view.Segments[0].ID ||
		evidence[0].MessageID != "m1" || evidence[0].Location == nil {
		t.Fatalf("evidence = %+v", evidence[0])
	}
	if !item.CreatedAt.Equal(now) || !item.UpdatedAt.Equal(now) || item.ExtractionVersion != "extraction-v6" {
		t.Fatalf("timestamps/version = %+v", item)
	}
}

func TestBuildFromDraftNormalizedNameStillUsesSegmentQuote(t *testing.T) {
	src := source.Source{ID: "source-1", Content: json.RawMessage(`"数据库使用 SQLite。"`)}
	view := source.BuildView(src.Content)
	item, evidence, err := BuildFromDraft(src, view, Draft{
		SpanIDs: []string{view.Segments[0].ID},
		Kind:    KindPreference,
		Content: "项目数据库为 SQLite。",
	}, "extraction-v6", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if item.Content != "项目数据库为 SQLite。" || item.ContentMode != ContentNormalized {
		t.Fatalf("observation = %+v", item)
	}
	if len(evidence) != 1 || evidence[0].Quote != "数据库使用 SQLite。" {
		t.Fatalf("evidence = %+v", evidence)
	}
	if evidence[0].Quote == item.Content {
		t.Fatal("normalized model content leaked into evidence quote")
	}
}

func TestBuildFromDraftKeepsPendingObservationWhenSpansDoNotResolve(t *testing.T) {
	src := source.Source{ID: "source-1", Content: json.RawMessage(`"当前内容。"`)}
	view := source.BuildView(src.Content)
	item, evidence, err := BuildFromDraft(src, view, Draft{
		SpanIDs: []string{"seg-9999"},
		Content: "模型补充表达。",
	}, "extraction-v6", time.Now())
	if err != nil {
		t.Fatalf("pending observation returned error: %v", err)
	}
	if item.Content != "模型补充表达。" || item.Status != StatusPending || item.ContentMode != ContentNormalized {
		t.Fatalf("observation = %+v", item)
	}
	if len(evidence) != 0 {
		t.Fatalf("pending observation gained evidence: %+v", evidence)
	}
}

func TestBuildFromDraftRejectsEmptyDraftAndContextOnlySelection(t *testing.T) {
	src := source.Source{ID: "source-1", Content: json.RawMessage(`"当前内容。"`)}
	view := source.BuildView(src.Content)
	if _, _, err := BuildFromDraft(src, view, Draft{SpanIDs: []string{"seg-9999"}}, "v", time.Now()); err == nil {
		t.Fatal("empty invalid draft was accepted")
	}
	view.ContextSegments = []source.Segment{{ID: "seg-context", Text: "上下文"}}
	item, evidence, err := BuildFromDraft(src, view, Draft{
		SpanIDs: []string{"seg-context"}, Content: "上下文不能成为观察。",
	}, "v", time.Now())
	if err != nil || item.Status != StatusPending || item.Content == "" {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	if len(evidence) != 0 {
		t.Fatalf("context evidence = %+v", evidence)
	}
}

func TestBuildFromDraftInfersKindOnlyWhenModelOmitsIt(t *testing.T) {
	src := source.Source{ID: "source-1", Content: json.RawMessage(`"用户喜欢核桃。"`)}
	view := source.BuildView(src.Content)
	item, _, err := BuildFromDraft(src, view, Draft{SpanIDs: []string{view.Segments[0].ID}}, "v", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != KindPreference {
		t.Fatalf("kind = %q", item.Kind)
	}
	item, _, err = BuildFromDraft(src, view, Draft{
		SpanIDs: []string{view.Segments[0].ID}, Kind: KindEvent,
	}, "v", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != KindEvent {
		t.Fatalf("model kind was overwritten: %q", item.Kind)
	}
}

func TestBuildFromDraftRedactedSourceKeepsOnlyAssociation(t *testing.T) {
	src := source.Source{ID: "source-1", Redacted: true}
	view := source.View{Segments: []source.Segment{{ID: "seg-0001", Text: "原文", Role: "user"}}}
	item, evidence, err := BuildFromDraft(src, view, Draft{SpanIDs: []string{"seg-0001"}}, "v", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != StatusPending {
		t.Fatalf("redacted observation status = %q", item.Status)
	}
	if len(evidence) != 1 || evidence[0].Quote != "" || evidence[0].Location != nil || !evidence[0].Redacted {
		t.Fatalf("redacted evidence = %+v", evidence)
	}
}
