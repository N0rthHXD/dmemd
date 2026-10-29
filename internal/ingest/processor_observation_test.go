package ingest_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func ingestSubmissionForTest(content, scopeKey string) ingest.Submission {
	return ingest.Submission{ScopeKey: scopeKey, Content: json.RawMessage(content)}
}

func openFixtureInspector(t *testing.T, f *processorFixture) *sql.DB {
	t.Helper()
	return testutil.OpenInspector(t, f.store.Path())
}

func TestProcessorPersistsL0ObservationsAndQueuesGovernance(t *testing.T) {
	f := newProcessorFixture(t)
	f.fake.EnqueueContent(`{"items":[
		{"span_ids":["seg-0001"],"kind":"fact"},
		{"span_ids":["seg-0001"],"kind":"preference","content":"团队偏好 Go。"},
		{"span_ids":["seg-0001"],"kind":"fact"}
	]}`)
	receipt, err := f.service.Submit(context.Background(), ingestSubmissionForTest(
		`"项目使用 SQLite。团队用 Go。"`, "project:dmemd",
	))
	if err != nil {
		t.Fatal(err)
	}
	job := f.process(t, f.claim(t))
	if job.Status != "succeeded" || len(job.ObservationIDs) != 2 {
		t.Fatalf("process result = %+v", job)
	}
	items, err := f.observations.ListBySource(context.Background(), receipt.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("observations = %+v", items)
	}
	seen := map[observation.ContentMode]bool{}
	for _, item := range items {
		seen[item.ContentMode] = true
		if item.ScopeKey != "project:dmemd" || item.Status != observation.StatusObserved {
			t.Fatalf("observation = %+v", item)
		}
		evidence, err := f.observations.Evidence(context.Background(), item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(evidence) != 1 || evidence[0].Quote != "项目使用 SQLite。" {
			t.Fatalf("evidence = %+v", evidence)
		}
		governanceJob, err := f.store.GetGovernanceJobByTarget(context.Background(),
			task.KindGovernObservation, task.ObservationKey(item.ID))
		if err != nil {
			t.Fatal(err)
		}
		if governanceJob.Status != task.StatusQueued {
			t.Fatalf("governance job = %+v", governanceJob)
		}
	}
	if !seen[observation.ContentExtractive] || !seen[observation.ContentNormalized] {
		t.Fatalf("content modes = %+v", seen)
	}

	var memories int
	if err := openFixtureInspector(t, f).QueryRow(`SELECT count(*) FROM memories`).Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if memories != 0 {
		t.Fatalf("extraction created %d canonical memories", memories)
	}
	if f.fake.RequestCount() != 1 {
		t.Fatalf("model calls = %d, want 1", f.fake.RequestCount())
	}
}

func TestProcessorKeepsPendingObservationWithoutGovernanceTask(t *testing.T) {
	f := newProcessorFixture(t)
	f.fake.EnqueueContent(`{"items":[{"span_ids":["seg-9999"],"content":"无法绑定证据的观察。"}]}`)
	receipt, err := f.service.Submit(context.Background(), ingestSubmissionForTest(
		`"当前来源。"`, "",
	))
	if err != nil {
		t.Fatal(err)
	}
	job := f.process(t, f.claim(t))
	if job.Status != "succeeded" || len(job.ObservationIDs) != 1 || job.Stats.PendingObservations != 1 {
		t.Fatalf("process result = %+v", job)
	}
	items, err := f.observations.ListBySource(context.Background(), receipt.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != observation.StatusPending ||
		items[0].Content != "无法绑定证据的观察。" {
		t.Fatalf("observations = %+v", items)
	}
	evidence, err := f.observations.Evidence(context.Background(), items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 0 {
		t.Fatalf("pending observation evidence = %+v", evidence)
	}
	if _, err := f.store.GetGovernanceJobByTarget(context.Background(),
		task.KindGovernObservation, task.ObservationKey(items[0].ID)); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("pending observation governance error = %v", err)
	}
}

func TestProcessorRedactionDuringExtractionCancelsCommit(t *testing.T) {
	f := newProcessorFixture(t)
	f.fake.Enqueue(ingestFakeBlockedDraft("红脱敏期间的结果。"))
	receipt, err := f.service.Submit(context.Background(), ingestSubmissionForTest(
		`"红脱敏期间的结果。"`, "",
	))
	if err != nil {
		t.Fatal(err)
	}
	job := f.claim(t)
	type outcome struct {
		result ingest.ProcessResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.processor.Process(context.Background(), job)
		done <- outcome{result: result, err: err}
	}()
	if err := f.fake.WaitStarted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.store.WithTx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.RedactSource(ctx, receipt.SourceID, f.clock)
	}); err != nil {
		t.Fatal(err)
	}
	f.fake.Release()
	processed := <-done
	if processed.err != nil || processed.result.Status != ingest.JobCancelled {
		t.Fatalf("process result = %+v err=%v", processed.result, processed.err)
	}
	items, err := f.observations.ListBySource(context.Background(), receipt.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("redacted source produced observations: %+v", items)
	}
}

func TestProcessorDoesNotReadExistingMemories(t *testing.T) {
	f := newProcessorFixture(t)
	f.fake.EnqueueContent(`{"items":[{"span_ids":["seg-0001"]}]}`)
	f.fake.EnqueueContent(`{"items":[{"span_ids":["seg-0001"]}]}`)
	_ = f.submit(t, `"同一条来源内容。"`)
	first := f.process(t, f.claim(t))
	_ = f.submit(t, `"同一条来源内容。"`)
	second := f.process(t, f.claim(t))
	memories := 0
	if err := openFixtureInspector(t, f).QueryRow(`SELECT count(*) FROM memories`).Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if memories != 0 {
		t.Fatalf("extraction read/wrote canonical memories: %d", memories)
	}
	if first.ObservationIDs[0] == second.ObservationIDs[0] ||
		first.ObservationIDs[0] == "" || second.ObservationIDs[0] == "" {
		t.Fatalf("cross-source observations collapsed: %+v %+v", first, second)
	}
}
