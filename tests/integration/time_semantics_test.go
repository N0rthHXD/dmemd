package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

func TestPlannedEventUsesEventTimeAndProcedureRoundTrips(t *testing.T) {
	f := newIngestStack(t)
	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "event", "title": "复诊", "content": "2026年11月20日计划复诊。",
		"valid_from": "2026-11-20T00:00:00Z", "valid_from_basis": "explicit",
		"phase": "scheduled", "durability": "episodic",
		"evidence": []any{map[string]any{"quote": "2026年11月20日计划复诊。"}},
	}})
	receipt := f.submit(t, `"2026年11月20日计划复诊。"`)
	if !f.processOne(t) {
		t.Fatal("event job was not processed")
	}
	job := f.job(t, receipt.JobID)
	if job.Status != "succeeded" || len(job.MemoryIDs) != 1 {
		t.Fatalf("job = %+v", job)
	}
	item, err := f.memories.Get(context.Background(), job.MemoryIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if item.EventTime == nil || !item.EventTime.Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("event_time = %v", item.EventTime)
	}
	if item.ValidFrom != nil {
		t.Fatalf("future event date leaked into valid_from: %v", item.ValidFrom)
	}
	if item.EventTimePrecision != memory.PrecisionDay {
		t.Fatalf("event precision = %q", item.EventTimePrecision)
	}
	if item.Temporal.Event.Text != "2026年11月20日" || item.Temporal.Event.TimezoneKnown {
		t.Fatalf("event temporal detail = %+v", item.Temporal.Event)
	}

	onDay, err := f.search.Search(context.Background(), retrieval.Request{
		Query: "复诊", Filters: retrieval.Filters{EventAt: timePtr(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))}, TopK: 10,
	})
	if err != nil || len(onDay.Items) != 1 {
		t.Fatalf("event_at same day = %+v err=%v", onDay, err)
	}
	nextDay, err := f.search.Search(context.Background(), retrieval.Request{
		Query: "复诊", Filters: retrieval.Filters{EventAt: timePtr(time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))}, TopK: 10,
	})
	if err != nil || len(nextDay.Items) != 0 {
		t.Fatalf("event_at next day = %+v err=%v", nextDay, err)
	}
	current, err := f.search.Search(context.Background(), retrieval.Request{Query: "复诊", TopK: 10})
	if err != nil || len(current.Items) != 1 {
		t.Fatalf("current plan query = %+v err=%v", current, err)
	}

	f.fake.EnqueueCandidates([]any{map[string]any{
		"kind": "procedure", "title": "部署检查", "content": "先安装再检查配置，检查失败时停止。",
		"procedure": map[string]any{
			"goal": "部署", "steps": []any{"安装软件", "检查配置"},
			"preconditions": []any{"有权限"}, "completion_conditions": []any{"检查通过"},
			"failure_branches": []any{"检查失败时停止"},
		},
		"evidence": []any{map[string]any{"quote": "先安装再检查配置，检查失败时停止。"}},
	}})
	procedureReceipt := f.submit(t, `"先安装再检查配置，检查失败时停止。"`)
	if !f.processOne(t) {
		t.Fatal("procedure job was not processed")
	}
	procedureJob := f.job(t, procedureReceipt.JobID)
	procedure, err := f.memories.Get(context.Background(), procedureJob.MemoryIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if procedure.Procedure == nil || procedure.Procedure.Goal != "部署" ||
		len(procedure.Procedure.Steps) != 2 || len(procedure.Procedure.FailureBranches) != 1 {
		t.Fatalf("procedure = %+v", procedure.Procedure)
	}
	versions, err := f.memories.ListVersions(context.Background(), procedure.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) == 0 || versions[0].Snapshot.Procedure == nil || versions[0].Snapshot.Procedure.Steps[1] != "检查配置" {
		t.Fatalf("procedure revision = %+v", versions)
	}
}
