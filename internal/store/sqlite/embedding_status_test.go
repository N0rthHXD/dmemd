package sqlite

import (
	"context"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

func TestEmbeddingRegistrationCoverageAndProfileChange(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	store.ConfigureEmbedding(true, "profile-a")
	item, document := seedEmbeddingMemory(t, store, memory.Memory{
		Kind: memory.KindFact, Title: "向量状态", Content: "向量覆盖检查。",
	})

	registration, err := store.RegisterEmbeddingConfiguration(ctx, "profile-a", embedding.IndexVersion, testTime(0))
	if err != nil {
		t.Fatal(err)
	}
	if !registration.ProfileChanged || !registration.IndexVersionChanged {
		t.Fatalf("initial registration = %+v", registration)
	}
	stats, err := store.EmbeddingStats(ctx, "profile-a", 3)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Documents != 1 || stats.CurrentVectors != 0 || stats.MissingVectors != 1 ||
		stats.PendingJobs != 1 || !stats.RebuildRequired {
		t.Fatalf("initial stats = %+v", stats)
	}
	if stats.RegisteredProfileID != "profile-a" || stats.RegisteredIndexVersion != embedding.IndexVersion {
		t.Fatalf("registered identity = %+v", stats)
	}
	if stats.IndexedProfiles == nil || stats.FailedCodes == nil {
		t.Fatalf("inventory maps must be non-nil: %+v", stats)
	}

	job := claimOne(t, store, "profile-a", testTime(0))
	if job.ContentHash != document.ContentHash {
		t.Fatalf("job content hash = %q", job.ContentHash)
	}
	commitVector(t, store, job, []float32{1, 0, 0})
	stats, err = store.EmbeddingStats(ctx, "profile-a", 3)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Documents != 1 || stats.CurrentVectors != 1 || stats.MissingVectors != 0 ||
		stats.StaleVectors != 0 || stats.PendingJobs != 0 || stats.RebuildRequired {
		t.Fatalf("completed stats = %+v", stats)
	}

	// A changed model or dimensions must leave old vectors out of the current
	// profile, persist a rebuild reason, and preserve the old profile count.
	registration, err = store.RegisterEmbeddingConfiguration(ctx, "profile-b", embedding.IndexVersion, testTime(2))
	if err != nil {
		t.Fatal(err)
	}
	if !registration.ProfileChanged || registration.IndexVersionChanged {
		t.Fatalf("profile change registration = %+v", registration)
	}
	stats, err = store.EmbeddingStats(ctx, "profile-b", 3)
	if err != nil {
		t.Fatal(err)
	}
	if stats.CurrentVectors != 0 || stats.StaleVectors != 1 || stats.MissingVectors != 0 ||
		!stats.RebuildRequired || stats.RebuildReason != "profile_changed" {
		t.Fatalf("profile-b stats = %+v", stats)
	}
	if stats.IndexedProfiles["profile-a"] != 1 {
		t.Fatalf("old profile inventory = %+v", stats.IndexedProfiles)
	}

	hits, err := store.SearchVectors(ctx, retrievalVectorQuery("profile-b", 3, []float32{1, 0, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("stale profile vectors entered recall: %+v", hits)
	}
	hits, err = store.SearchVectors(ctx, retrievalVectorQuery("profile-a", 3, []float32{1, 0, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].MemoryID != item.ID {
		t.Fatalf("current profile vectors = %+v", hits)
	}
}

func TestEmbeddingFailedJobsAreReportedBySanitizedCode(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	store.ConfigureEmbedding(true, "profile-a")
	seedEmbeddingMemory(t, store, memory.Memory{
		Kind: memory.KindFact, Title: "失败", Content: "失败任务诊断。",
	})
	if _, err := store.RegisterEmbeddingConfiguration(ctx, "profile-a", embedding.IndexVersion, testTime(0)); err != nil {
		t.Fatal(err)
	}
	job := claimOne(t, store, "profile-a", testTime(0))
	if err := store.FailJob(ctx, job, "http_429", nil, testTime(2)); err != nil {
		t.Fatal(err)
	}
	stats, err := store.EmbeddingStats(ctx, "profile-a", 3)
	if err != nil {
		t.Fatal(err)
	}
	if stats.FailedJobs != 1 || stats.FailedCodes["http_429"] != 1 {
		t.Fatalf("failed job stats = %+v", stats)
	}
}

func retrievalVectorQuery(profileID string, dimensions int, vector []float32) retrieval.VectorQuery {
	return retrieval.VectorQuery{
		ProfileID:     profileID,
		Dimensions:    dimensions,
		Query:         vector,
		Filters:       retrieval.Filters{States: []memory.State{memory.StateActive}},
		Limit:         10,
		MinSimilarity: -1,
	}
}
