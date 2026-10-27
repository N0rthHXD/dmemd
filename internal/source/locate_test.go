package source_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestBuildViewAndResolveEvidence(t *testing.T) {
	content := json.RawMessage(`{
		"messages": [
			{"id": "m1", "role": "user", "content": "你好世界"},
			{"id": "m2", "role": "assistant", "content": "second message"}
		]
	}`)
	view := source.BuildView(content)
	if len(view.Segments) != 2 {
		t.Fatalf("segments = %+v", view.Segments)
	}
	first := view.Segments[0]
	if first.ID != "seg-0001" || first.MessageID != "m1" || first.Role != "user" ||
		first.Field != "messages.0.content" || first.Text != "你好世界" {
		t.Fatalf("first segment = %+v", first)
	}
	resolved, err := view.Resolve(source.EvidenceReference{SegmentID: "seg-0001", Quote: "世界"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Location.Field != "messages.0.content" || resolved.Location.Start != 2 || resolved.Location.End != 4 {
		t.Fatalf("location = %+v", resolved.Location)
	}
	if resolved.Segment.MessageID != "m1" || resolved.Recovery != "" {
		t.Fatalf("resolved = %+v", resolved)
	}
	second, err := view.Resolve(source.EvidenceReference{SegmentID: "seg-0002", Quote: "second"})
	if err != nil || second.Location.Field != "messages.1.content" || second.Location.Start != 0 || second.Location.End != 6 {
		t.Fatalf("second = %+v err=%v", second, err)
	}
	// Missing segment IDs are recovered only when the quote is globally unique.
	recovered, err := view.Resolve(source.EvidenceReference{Quote: "message"})
	if err != nil || recovered.Recovery != "unique_quote" || recovered.Location.Field != "messages.1.content" ||
		recovered.Location.Start != 7 || recovered.Location.End != 14 {
		t.Fatalf("recovered = %+v err=%v", recovered, err)
	}
	if _, err := view.Resolve(source.EvidenceReference{SegmentID: "missing", Quote: "世界"}); !hasEvidenceCode(err, source.EvidenceSegmentUnknown) {
		t.Fatalf("unknown segment error = %v", err)
	}
	if _, err := view.Resolve(source.EvidenceReference{SegmentID: "seg-0001", Quote: "absent"}); !hasEvidenceCode(err, source.EvidenceQuoteNotFound) {
		t.Fatalf("missing quote error = %v", err)
	}
}

func TestBuildViewIsStableWithMissingAndDuplicateMessageIDs(t *testing.T) {
	content := json.RawMessage(`{"messages":[
		{"id":"dup","role":"user","content":"first"},
		{"id":"dup","role":"assistant","content":"second"},
		{"role":"user","content":"third"},
		{"content":"fourth"}
	]}`)
	first := source.BuildView(content)
	second := source.BuildView(content)
	if len(first.Segments) != 4 || len(second.Segments) != 4 {
		t.Fatalf("views = %+v %+v", first, second)
	}
	seen := map[string]bool{}
	for index := range first.Segments {
		if first.Segments[index] != second.Segments[index] {
			t.Fatalf("rebuild mismatch: %+v vs %+v", first.Segments[index], second.Segments[index])
		}
		if seen[first.Segments[index].ID] {
			t.Fatalf("duplicate segment ID: %+v", first.Segments)
		}
		seen[first.Segments[index].ID] = true
	}
	if first.Segments[0].MessageID != "dup" || first.Segments[1].MessageID != "dup" ||
		first.Segments[0].Role != "user" || first.Segments[1].Role != "assistant" ||
		first.Segments[2].MessageID != "" || first.Segments[3].MessageID != "" {
		t.Fatalf("auxiliary context = %+v", first.Segments)
	}
}

func TestBuildViewKeepsEnclosingMessageContextForNestedObjects(t *testing.T) {
	view := source.BuildView(json.RawMessage(`{"messages":[{"id":"m1","role":"user","content":{"items":[{"id":"item-1","text":"条目正文"}]}}]}`))
	if len(view.Segments) != 1 {
		t.Fatalf("segments = %+v", view.Segments)
	}
	segment := view.Segments[0]
	if segment.MessageID != "m1" || segment.Role != "user" || segment.Text != "条目正文" ||
		segment.Field != "messages.0.content.items.0.text" {
		t.Fatalf("segment context = %+v", segment)
	}
}

func TestBuildViewHandlesRootStringInvalidJSONAndMetadata(t *testing.T) {
	view := source.BuildView(json.RawMessage(`"记忆服务"`))
	if len(view.Segments) != 1 || view.Segments[0].ID != "seg-0001" || view.Segments[0].Field != "" ||
		view.Segments[0].Text != "记忆服务" {
		t.Fatalf("root view = %+v", view)
	}
	resolved, err := view.Resolve(source.EvidenceReference{Quote: "记忆"})
	if err != nil || resolved.Location.Field != "" || resolved.Location.Start != 0 || resolved.Location.End != 2 {
		t.Fatalf("root resolution = %+v err=%v", resolved, err)
	}
	if view := source.BuildView(json.RawMessage(`{"unterminated"`)); len(view.Segments) != 0 {
		t.Fatalf("invalid JSON view = %+v", view)
	}
	if view := source.BuildView(nil); len(view.Segments) != 0 {
		t.Fatalf("empty view = %+v", view)
	}
	view = source.BuildView(json.RawMessage(`{"id":"outer","role":"user","occurred_at":"2026-10-20T02:00:00Z","messages":[{"id":"m1","content":"same same"},{"id":"m2","content":"same"}]}`))
	if len(view.Segments) != 2 || view.Segments[0].MessageID != "m1" || view.Segments[0].Role != "user" ||
		view.Segments[0].OccurredAt != "2026-10-20T02:00:00Z" {
		t.Fatalf("context view = %+v", view.Segments)
	}
	// The same quote in two segments is ambiguous without a segment ID.
	if _, err := view.Resolve(source.EvidenceReference{Quote: "same"}); !hasEvidenceCode(err, source.EvidenceQuoteAmbiguous) {
		t.Fatalf("ambiguous global quote = %v", err)
	}
	// Inside one segment, repeated text requires explicit offsets.
	if _, err := view.Resolve(source.EvidenceReference{SegmentID: "seg-0001", Quote: "same"}); !hasEvidenceCode(err, source.EvidenceQuoteAmbiguous) {
		t.Fatalf("ambiguous segment quote = %v", err)
	}
	start, end := 5, 9
	if resolved, err := view.Resolve(source.EvidenceReference{SegmentID: "seg-0001", Quote: "same", Start: &start, End: &end}); err != nil ||
		resolved.Location.Start != 5 || resolved.Location.End != 9 {
		t.Fatalf("offset resolution = %+v err=%v", resolved, err)
	}
	badStart := 1
	if _, err := view.Resolve(source.EvidenceReference{SegmentID: "seg-0001", Quote: "same", Start: &badStart}); !hasEvidenceCode(err, source.EvidenceOffsetsInvalid) {
		t.Fatalf("bad offset error = %v", err)
	}
}

func hasEvidenceCode(err error, code string) bool {
	var evidenceErr *source.EvidenceError
	return errors.As(err, &evidenceErr) && evidenceErr.Code == code
}

func TestViewChunksAreStableAndCarryBoundedContext(t *testing.T) {
	content := json.RawMessage(`{"messages":[
		{"id":"m1","content":"第一段内容足够长"},
		{"id":"m2","content":"第二段内容也足够长"},
		{"id":"m3","content":"第三段内容同样长"}
	]}`)
	view := source.BuildView(content)
	chunks := view.Chunks(8)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	if len(chunks[0].ContextSegments) != 0 {
		t.Fatalf("first chunk context = %+v", chunks[0].ContextSegments)
	}
	if chunks[0].Segments[0].ID != "seg-0001" || chunks[1].Segments[0].ID != "seg-0002" ||
		chunks[2].Segments[0].ID != "seg-0003" {
		t.Fatalf("segment IDs changed across chunks: %+v", chunks)
	}
	if len(chunks[1].ContextSegments) != 1 || chunks[1].ContextSegments[0].ID != "seg-0001" {
		t.Fatalf("second chunk context = %+v", chunks[1].ContextSegments)
	}
	again := view.Chunks(8)
	if len(again) != len(chunks) || again[1].ContextSegments[0].ID != chunks[1].ContextSegments[0].ID {
		t.Fatalf("chunks are not deterministic")
	}
	oversized := source.BuildView(json.RawMessage(`"` + strings.Repeat("长", 20) + `"`)).Chunks(8)
	if len(oversized) != 1 || !oversized[0].Oversized {
		t.Fatalf("oversized chunk = %+v", oversized)
	}
}

func TestBuildViewSplitsLongFieldIntoStableOffsetSegments(t *testing.T) {
	text := strings.Repeat("前一句。", 1200) + "结尾标记"
	view := source.BuildView(json.RawMessage(`{"messages":[{"id":"m1","content":` + strconv.Quote(text) + `}]}`))
	if len(view.Segments) < 2 {
		t.Fatalf("long field produced %d segments", len(view.Segments))
	}
	last := view.Segments[len(view.Segments)-1]
	if last.MessageID != "m1" || last.Field != "messages.0.content" || last.End <= last.Start {
		t.Fatalf("last segment = %+v", last)
	}
	resolved, err := view.Resolve(source.EvidenceReference{SegmentID: last.ID, Quote: "结尾标记"})
	if err != nil {
		t.Fatal(err)
	}
	runes := []rune(text)
	if resolved.Location.Start != len(runes)-len([]rune("结尾标记")) ||
		resolved.Location.End != len(runes) {
		t.Fatalf("location = %+v, want original field offsets", resolved.Location)
	}
	again := source.BuildView(json.RawMessage(`{"messages":[{"id":"m1","content":` + strconv.Quote(text) + `}]}`))
	if len(again.Segments) != len(view.Segments) || again.Segments[0].ID != view.Segments[0].ID ||
		again.Segments[len(again.Segments)-1].ID != last.ID {
		t.Fatal("long-field segmentation is not stable")
	}
}
