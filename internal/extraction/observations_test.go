package extraction

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestBuildObservationsDeduplicatesOnlyIdenticalSpanSetAndBody(t *testing.T) {
	src := source.Source{ID: "source-1", Content: json.RawMessage(`"第一句。第二句。"`)}
	view := source.BuildView(src.Content)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	records, stats := BuildObservations(src, []observation.Draft{
		{SpanIDs: []string{view.Segments[0].ID}},
		{SpanIDs: []string{view.Segments[0].ID}},
		{SpanIDs: []string{view.Segments[0].ID}, Content: "不同正文。"},
		{SpanIDs: []string{view.Segments[1].ID}},
		{SpanIDs: []string{"seg-9999"}},
		{SpanIDs: []string{"seg-9999"}, Content: "保留的待定观察。"},
	}, ProtocolVersion, now)
	if len(records) != 4 {
		t.Fatalf("records = %+v", records)
	}
	if stats.Duplicates != 1 || stats.Invalid != 1 || stats.Pending != 1 || stats.Normalized != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if records[0].Observation.Content != "第一句。" || len(records[0].Evidence) != 1 {
		t.Fatalf("extractive record = %+v", records[0])
	}
	if records[1].Observation.Content != "不同正文。" || records[1].Observation.ContentMode != observation.ContentNormalized {
		t.Fatalf("normalized record = %+v", records[1])
	}
	if records[2].Observation.Content != "第二句。" {
		t.Fatalf("second span record = %+v", records[2])
	}
	if records[3].Observation.Status != observation.StatusPending || len(records[3].Evidence) != 0 {
		t.Fatalf("pending record = %+v", records[3])
	}
}

func TestBuildObservationsKeepsDifferentBodiesForSameSpan(t *testing.T) {
	src := source.Source{ID: "source-1", Content: json.RawMessage(`"甲。乙。"`)}
	view := source.BuildView(src.Content)
	records, _ := BuildObservations(src, []observation.Draft{
		{SpanIDs: []string{view.Segments[0].ID}},
		{SpanIDs: []string{view.Segments[0].ID}, Content: "甲的不同表达。"},
	}, ProtocolVersion, time.Now())
	if len(records) != 2 {
		t.Fatalf("different bodies were collapsed: %+v", records)
	}
}
