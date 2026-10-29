package extraction

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/source"
)

type chunkProvider struct {
	responses map[int]Result
	requests  []Request
}

func (p *chunkProvider) Extract(_ context.Context, request Request) (Result, error) {
	p.requests = append(p.requests, request)
	return p.responses[request.ChunkIndex], nil
}

func TestServiceRejectsContextOnlySpanSelection(t *testing.T) {
	src := source.Source{ID: "source-1", Content: json.RawMessage(`"第一句。第二句。"`)}
	view := source.BuildView(src.Content)
	provider := &chunkProvider{responses: map[int]Result{
		1: {Drafts: []observation.Draft{{SpanIDs: []string{view.Segments[0].ID}}}},
		2: {Drafts: []observation.Draft{{
			SpanIDs: []string{view.Segments[0].ID},
			Content: "上下文不能成为证据。",
		}}},
	}}
	service := NewService(provider, WithChunkRunes(4))
	result, err := service.Extract(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if result.Chunks != 2 || len(provider.requests) != 2 {
		t.Fatalf("chunks=%d requests=%d", result.Chunks, len(provider.requests))
	}
	second := provider.requests[1]
	if len(second.ContextSegments) != 1 || second.ContextSegments[0].ID != view.Segments[0].ID {
		t.Fatalf("second request context = %+v", second.ContextSegments)
	}
	if len(result.Drafts) != 2 {
		t.Fatalf("drafts = %+v", result.Drafts)
	}
	if len(result.Drafts[1].SpanIDs) != 0 || result.Drafts[1].Content != "上下文不能成为证据。" {
		t.Fatalf("context-only draft = %+v", result.Drafts[1])
	}
	records, stats := BuildObservations(src, result.Drafts, ProtocolVersion, time.Now())
	if len(records) != 2 || stats.Pending != 1 || records[1].Observation.Status != observation.StatusPending {
		t.Fatalf("records=%+v stats=%+v", records, stats)
	}
}
