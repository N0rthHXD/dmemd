package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

func seedEmbeddingMemory(t *testing.T, store *Store, item memory.Memory) (memory.Memory, memory.SearchDocument) {
	t.Helper()
	ctx := context.Background()
	if item.ID == "" {
		item.ID = idgen.NewUUID()
	}
	if item.Kind == "" {
		item.Kind = memory.KindFact
	}
	if item.State == "" {
		item.State = memory.StateActive
	}
	if item.Version == 0 {
		item.Version = 1
	}
	if item.ObservedAt.IsZero() {
		item.ObservedAt = testTime(0)
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = testTime(0)
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	document, err := retrieval.NewDocumentBuilder().BuildDocument(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertMemory(ctx, item); err != nil {
			return err
		}
		return tx.UpsertSearchDocument(ctx, document)
	}); err != nil {
		t.Fatal(err)
	}
	return item, document
}

func tableCount(t *testing.T, store *Store, table, where string, args ...any) int {
	t.Helper()
	query := `SELECT count(*) FROM ` + table
	if where != "" {
		query += ` WHERE ` + where
	}
	var count int
	if err := store.writer.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func claimOne(t *testing.T, store *Store, profileID string, now time.Time) embedding.Job {
	t.Helper()
	job, found, err := store.ClaimJob(context.Background(), profileID, now)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no embedding job claimed")
	}
	return job
}

func commitVector(t *testing.T, store *Store, job embedding.Job, vector []float32) {
	t.Helper()
	encoded, err := embedding.EncodeVector(vector, len(vector))
	if err != nil {
		t.Fatal(err)
	}
	committed, err := store.CommitJob(context.Background(), job, encoded, len(vector), testTime(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("vector commit was rejected")
	}
}

func TestEmbeddingGenerationLifecycleAndStaleCommit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	store.ConfigureEmbedding(true, "profile-1")

	item, document := seedEmbeddingMemory(t, store, memory.Memory{
		Kind: memory.KindPreference, Title: "饮食偏好", Content: "用户偏好清淡饮食。",
	})
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ?", item.ID); count != 1 {
		t.Fatalf("job count = %d", count)
	}
	job := claimOne(t, store, "profile-1", testTime(0))
	if job.MemoryID != item.ID || job.Attempts != 1 || job.ContentHash != document.ContentHash {
		t.Fatalf("job = %+v", job)
	}
	commitVector(t, store, job, []float32{1, 0, 0})
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", item.ID); count != 1 {
		t.Fatalf("embedding count = %d", count)
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ?", item.ID); count != 0 {
		t.Fatalf("job after commit = %d", count)
	}

	// Evidence/state-only writes reuse the current vector and create no task.
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.UpsertSearchDocument(ctx, document)
	}); err != nil {
		t.Fatal(err)
	}
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", item.ID); count != 1 {
		t.Fatalf("unchanged input lost its vector: %d", count)
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ?", item.ID); count != 0 {
		t.Fatalf("unchanged input created a task: %d", count)
	}

	// A content change invalidates the vector and creates a fresh generation.
	changed := item
	changed.Content = "用户偏好辛辣饮食。"
	changedDoc, err := retrieval.NewDocumentBuilder().BuildDocument(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.UpsertSearchDocument(ctx, changedDoc)
	}); err != nil {
		t.Fatal(err)
	}
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("content change kept the old vector")
	}
	jobB := claimOne(t, store, "profile-1", testTime(0))
	if jobB.Attempts != 1 || jobB.ContentHash != changedDoc.ContentHash {
		t.Fatalf("job B = %+v", jobB)
	}

	// A -> B -> A must reset the generation: the old B claim cannot commit.
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.UpsertSearchDocument(ctx, document)
	}); err != nil {
		t.Fatal(err)
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ? AND attempts = 0", item.ID); count != 1 {
		t.Fatalf("A -> B -> A did not reset attempts: %d", count)
	}
	encoded, err := embedding.EncodeVector([]float32{0, 1, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := store.CommitJob(ctx, jobB, encoded, 3, testTime(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Fatal("stale B claim committed into the A generation")
	}
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("stale commit wrote a vector")
	}

	jobA := claimOne(t, store, "profile-1", testTime(0))
	if jobA.ContentHash != document.ContentHash || jobA.Attempts != 1 {
		t.Fatalf("job A = %+v", jobA)
	}
	next := testTime(time.Minute)
	if err := store.FailJob(ctx, jobA, "network", &next, testTime(0)); err != nil {
		t.Fatal(err)
	}
	var status, code string
	var attempts int
	if err := store.writer.QueryRow(`SELECT status, attempts, error_code FROM memory_embedding_jobs WHERE memory_id = ?`, item.ID).
		Scan(&status, &attempts, &code); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || attempts != 1 || code != "network" {
		t.Fatalf("retry state = %s/%d/%s", status, attempts, code)
	}
	jobA = claimOne(t, store, "profile-1", testTime(time.Minute))
	if jobA.Attempts != 2 {
		t.Fatalf("attempts after retry = %d", jobA.Attempts)
	}
	commitVector(t, store, jobA, []float32{1, 0, 0})

	// Deleting the derived document cascades the vector and any task.
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.DeleteSearchDocument(ctx, item.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("search-document delete kept the vector")
	}
}

func TestEmbeddingDisabledMaintainsHashAndClearsStaleVectors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	store.ConfigureEmbedding(true, "profile-1")
	item, document := seedEmbeddingMemory(t, store, memory.Memory{Title: "T", Content: "old"})
	job := claimOne(t, store, "profile-1", testTime(0))
	commitVector(t, store, job, []float32{1, 0, 0})

	store.ConfigureEmbedding(false, "")
	changed := item
	changed.Content = "new"
	changedDoc, err := retrieval.NewDocumentBuilder().BuildDocument(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.UpsertSearchDocument(ctx, changedDoc)
	}); err != nil {
		t.Fatal(err)
	}
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("disabled mode kept a stale vector after input change")
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("disabled mode created a task")
	}
	var hash string
	if err := store.writer.QueryRow(`SELECT content_hash FROM memory_search_docs WHERE memory_id = ?`, item.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != changedDoc.ContentHash {
		t.Fatalf("content hash = %q", hash)
	}

	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.UpsertSearchDocument(ctx, document)
	}); err != nil {
		t.Fatal(err)
	}
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("disabled mode restored a cleared vector")
	}
}

func TestCommitAfterDocumentDeleteCannotReinsert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	store.ConfigureEmbedding(true, "profile-1")
	item, _ := seedEmbeddingMemory(t, store, memory.Memory{Title: "deleted", Content: "content"})
	job := claimOne(t, store, "profile-1", testTime(0))
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.DeleteSearchDocument(ctx, item.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("delete cascade kept the task")
	}
	encoded, err := embedding.EncodeVector([]float32{1, 0, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := store.CommitJob(ctx, job, encoded, 3, testTime(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Fatal("commit reinserted a vector after document deletion")
	}
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("deleted document received a vector")
	}
}

func TestSearchVectorsFiltersReadinessAndThreshold(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	store.ConfigureEmbedding(true, "profile-1")

	type fixture struct {
		item   memory.Memory
		vector []float32
	}
	early := testTime(0)
	late := testTime(time.Minute)
	fixtures := []fixture{
		{item: memory.Memory{Kind: memory.KindPreference, Title: "A", Content: "a", CreatedByApp: "app-a", UpdatedAt: early}, vector: []float32{1, 0, 0}},
		{item: memory.Memory{Kind: memory.KindFact, Title: "B", Content: "b", CreatedByApp: "app-b", UpdatedAt: late}, vector: []float32{0.8, 0.6, 0}},
		{item: memory.Memory{Kind: memory.KindPreference, Title: "C", Content: "c", State: memory.StateArchived, CreatedByApp: "app-a", UpdatedAt: late}, vector: []float32{0.9, 0.1, 0}},
	}
	for index := range fixtures {
		fixtures[index].item, _ = seedEmbeddingMemory(t, store, fixtures[index].item)
		job := claimOne(t, store, "profile-1", testTime(0))
		commitVector(t, store, job, fixtures[index].vector)
	}

	query := retrieval.VectorQuery{
		ProfileID: "profile-1", Dimensions: 3, Query: []float32{1, 0, 0},
		Filters: retrieval.Filters{States: []memory.State{memory.StateActive}}, Limit: 10,
	}
	hits, err := store.SearchVectors(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].MemoryID != fixtures[0].item.ID || hits[1].MemoryID != fixtures[1].item.ID {
		t.Fatalf("active vector hits = %+v", hits)
	}
	if hits[0].ContentHash == "" || hits[0].Cosine != 1 {
		t.Fatalf("first hit = %+v", hits[0])
	}

	query.Filters = retrieval.Filters{States: []memory.State{memory.StateActive}, Kinds: []memory.Kind{memory.KindPreference}}
	hits, err = store.SearchVectors(ctx, query)
	if err != nil || len(hits) != 1 || hits[0].MemoryID != fixtures[0].item.ID {
		t.Fatalf("kind filter = %+v, %v", hits, err)
	}
	query.Filters = retrieval.Filters{States: []memory.State{memory.StateActive}, CreatedByApp: "app-b"}
	hits, err = store.SearchVectors(ctx, query)
	if err != nil || len(hits) != 1 || hits[0].MemoryID != fixtures[1].item.ID {
		t.Fatalf("app filter = %+v, %v", hits, err)
	}
	query.Filters = retrieval.Filters{States: []memory.State{memory.StateArchived}}
	hits, err = store.SearchVectors(ctx, query)
	if err != nil || len(hits) != 1 || hits[0].MemoryID != fixtures[2].item.ID {
		t.Fatalf("archived filter = %+v, %v", hits, err)
	}
	query.Filters = retrieval.Filters{States: []memory.State{memory.StateActive}}
	query.MinSimilarity = 0.85
	hits, err = store.SearchVectors(ctx, query)
	if err != nil || len(hits) != 1 || hits[0].MemoryID != fixtures[0].item.ID {
		t.Fatalf("threshold = %+v, %v", hits, err)
	}

	// A stale content hash is never accepted.
	if _, err := store.writer.Exec(`UPDATE memory_search_docs SET content_hash = 'changed' WHERE memory_id = ?`, fixtures[0].item.ID); err != nil {
		t.Fatal(err)
	}
	query.MinSimilarity = 0
	hits, err = store.SearchVectors(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.MemoryID == fixtures[0].item.ID {
			t.Fatal("stale vector was returned")
		}
	}
}

func TestRebuildPreservesVectorsAndEnqueueMissingDoesNotReset(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	store.ConfigureEmbedding(true, "profile-1")
	kept, _ := seedEmbeddingMemory(t, store, memory.Memory{Title: "kept", Content: "kept content"})
	keptJob := claimOne(t, store, "profile-1", testTime(0))
	commitVector(t, store, keptJob, []float32{1, 0, 0})
	missing, _ := seedEmbeddingMemory(t, store, memory.Memory{Title: "missing", Content: "missing content"})
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ?", missing.ID); count != 1 {
		t.Fatalf("missing job = %d", count)
	}

	indexed, err := store.RebuildSearchDocuments(ctx, retrieval.NewDocumentBuilder())
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 2 {
		t.Fatalf("indexed = %d", indexed)
	}
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", kept.ID); count != 1 {
		t.Fatal("full rebuild deleted a valid vector")
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ?", missing.ID); count != 1 {
		t.Fatal("full rebuild lost a queued task")
	}

	// A claimed and retried task is not reset by a missing-vector scan.
	job := claimOne(t, store, "profile-1", testTime(0))
	next := testTime(time.Minute)
	if err := store.FailJob(ctx, job, "network", &next, testTime(0)); err != nil {
		t.Fatal(err)
	}
	created, err := store.EnqueueMissingJobs(ctx, "profile-1", testTime(0))
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("enqueue reset an existing task: %d", created)
	}
	var attempts int
	var status string
	if err := store.writer.QueryRow(`SELECT attempts, status FROM memory_embedding_jobs WHERE memory_id = ?`, job.MemoryID).
		Scan(&attempts, &status); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || status != "queued" {
		t.Fatalf("existing task = %s/%d", status, attempts)
	}

	// A terminal failure is retried by the next startup scan.
	job = claimOne(t, store, "profile-1", testTime(time.Minute))
	if err := store.FailJob(ctx, job, "http_401", nil, testTime(time.Minute)); err != nil {
		t.Fatal(err)
	}
	created, err = store.EnqueueMissingJobs(ctx, "profile-1", testTime(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("startup scan did not requeue a failed task: %d", created)
	}
	var retriedAttempts int
	if err := store.writer.QueryRow(`SELECT attempts FROM memory_embedding_jobs WHERE memory_id = ?`, job.MemoryID).
		Scan(&retriedAttempts); err != nil {
		t.Fatal(err)
	}
	if retriedAttempts != 0 {
		t.Fatalf("requeued attempts = %d", retriedAttempts)
	}
}

func TestRebuildRemovesStaleDocumentAndCascadesDerivedRows(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	store.ConfigureEmbedding(true, "profile-1")
	item, _ := seedEmbeddingMemory(t, store, memory.Memory{Title: "stale", Content: "stale content"})
	job := claimOne(t, store, "profile-1", testTime(0))
	commitVector(t, store, job, []float32{1, 0, 0})

	// Simulate a snapshot removed without a derived-index write.
	if _, err := store.writer.Exec(`UPDATE memories SET state = ? WHERE id = ?`, string(memory.StateDeleted), item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RebuildSearchDocuments(ctx, retrieval.NewDocumentBuilder()); err != nil {
		t.Fatal(err)
	}
	if count := tableCount(t, store, "memory_search_docs", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("stale document survived rebuild")
	}
	if count := tableCount(t, store, "memory_embeddings", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("stale vector survived rebuild")
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ?", item.ID); count != 0 {
		t.Fatal("stale task survived rebuild")
	}
}

func TestHasUnhashedDocumentsTriggersOneFullRebuild(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	item := memory.Memory{
		ID: idgen.NewUUID(), Kind: memory.KindFact, Title: "legacy", Content: "legacy content",
		State: memory.StateActive, Version: 1, ObservedAt: testTime(0),
		CreatedAt: testTime(0), UpdatedAt: testTime(0),
	}
	if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertMemory(ctx, item); err != nil {
			return err
		}
		return tx.UpsertSearchDocument(ctx, memory.SearchDocument{
			MemoryID: item.ID, TitleTerms: "legacy", ContentTerms: "legacy content",
		})
	}); err != nil {
		t.Fatal(err)
	}
	needs, err := store.HasUnhashedSearchDocuments(ctx)
	if err != nil || !needs {
		t.Fatalf("legacy document = %v, %v", needs, err)
	}
	store.ConfigureEmbedding(true, "profile-1")
	if _, err := store.RebuildSearchDocuments(ctx, retrieval.NewDocumentBuilder()); err != nil {
		t.Fatal(err)
	}
	needs, err = store.HasUnhashedSearchDocuments(ctx)
	if err != nil || needs {
		t.Fatalf("rebuilt document = %v, %v", needs, err)
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "memory_id = ?", item.ID); count != 1 {
		t.Fatalf("rebuild did not enqueue: %d", count)
	}
	version, err := store.IndexVersion(ctx)
	if err != nil || version != "" {
		t.Fatalf("initial index version = %q, %v", version, err)
	}
	if err := store.SetIndexVersion(ctx, embedding.IndexVersion); err != nil {
		t.Fatal(err)
	}
	version, err = store.IndexVersion(ctx)
	if err != nil || version != embedding.IndexVersion {
		t.Fatalf("recorded index version = %q, %v", version, err)
	}
}

func TestClaimJobOrdersDueTasksAndKeepsFailedOut(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	store.ConfigureEmbedding(true, "profile-1")
	first, _ := seedEmbeddingMemory(t, store, memory.Memory{Title: "first", Content: "one"})
	second, _ := seedEmbeddingMemory(t, store, memory.Memory{Title: "second", Content: "two"})

	job := claimOne(t, store, "profile-1", testTime(0))
	if job.MemoryID != first.ID && job.MemoryID != second.ID {
		t.Fatalf("unexpected claim %+v", job)
	}
	if err := store.FailJob(ctx, job, "protocol", nil, testTime(0)); err != nil {
		t.Fatal(err)
	}
	other := claimOne(t, store, "profile-1", testTime(0))
	if other.MemoryID == job.MemoryID {
		t.Fatal("failed task was claimed again")
	}
	if _, found, err := store.ClaimJob(ctx, "profile-1", testTime(0)); err != nil || found {
		t.Fatalf("terminal task claim = %v, %v", found, err)
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "status = 'failed'"); count != 1 {
		t.Fatalf("failed jobs = %d", count)
	}
	requeued, err := store.RecoverJobs(ctx, "profile-1", testTime(time.Minute))
	if err != nil || requeued != 1 {
		t.Fatalf("recovered = %d, %v", requeued, err)
	}
	if count := tableCount(t, store, "memory_embedding_jobs", "status = 'queued' AND attempts = 1"); count != 1 {
		t.Fatalf("recovered job state = %d", count)
	}
}
