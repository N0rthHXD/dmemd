package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestSearchDocumentTriggersStayInSync(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-search"
	seedSource(t, store, sourceID)
	item := memory.Memory{
		ID:             idgen.NewUUID(),
		Kind:           memory.KindFact,
		Title:          "Alpha title",
		Content:        "body about alpha",
		State:          memory.StateActive,
		Version:        1,
		ObservedAt:     testTime(0),
		CreatedAt:      testTime(0),
		UpdatedAt:      testTime(0),
		OriginSourceID: sourceID,
	}
	document := memory.SearchDocument{
		MemoryID:     item.ID,
		State:        item.State,
		Title:        item.Title,
		Content:      item.Content,
		TitleTerms:   "alpha title",
		ContentTerms: "body about alpha",
		UpdatedAt:    item.UpdatedAt,
	}
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertMemory(ctx, item); err != nil {
			return err
		}
		return tx.UpsertSearchDocument(ctx, document)
	}); err != nil {
		t.Fatal(err)
	}
	assertFTSMatches(t, store, "alpha", 1)

	document.Title = "Beta title"
	document.TitleTerms = "beta title"
	document.ContentTerms = "body about beta"
	document.UpdatedAt = testTime(time.Minute)
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		return tx.UpsertSearchDocument(ctx, document)
	}); err != nil {
		t.Fatal(err)
	}
	assertFTSMatches(t, store, "alpha", 0)
	assertFTSMatches(t, store, "beta", 1)

	// Archiving keeps the derived document and index; search filtering is a P4
	// concern, not a storage deletion.
	document.State = memory.StateArchived
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		return tx.UpsertSearchDocument(ctx, document)
	}); err != nil {
		t.Fatal(err)
	}
	assertFTSMatches(t, store, "beta", 1)
	var docs int
	if err := store.writer.QueryRow(`SELECT count(*) FROM memory_search_docs WHERE memory_id = ? AND state = 'archived'`, item.ID).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if docs != 1 {
		t.Fatalf("archived search docs = %d, want 1", docs)
	}

	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		return tx.DeleteSearchDocument(ctx, item.ID)
	}); err != nil {
		t.Fatal(err)
	}
	assertFTSMatches(t, store, "beta", 0)
	if err := store.writer.QueryRow(`SELECT count(*) FROM memory_search_docs WHERE memory_id = ?`, item.ID).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if docs != 0 {
		t.Fatalf("search docs after delete = %d, want 0", docs)
	}
}

func TestRebuildSearchIndexRestoresDerivedIndex(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-rebuild"
	seedSource(t, store, sourceID)
	item := memory.Memory{
		ID:             idgen.NewUUID(),
		Kind:           memory.KindPreference,
		Title:          "Rebuild probe",
		Content:        "rebuild content",
		State:          memory.StateActive,
		Version:        1,
		ObservedAt:     testTime(0),
		CreatedAt:      testTime(0),
		UpdatedAt:      testTime(0),
		OriginSourceID: sourceID,
	}
	document := memory.SearchDocument{
		MemoryID:     item.ID,
		State:        item.State,
		Title:        item.Title,
		Content:      item.Content,
		TitleTerms:   "rebuild probe",
		ContentTerms: "rebuild content",
		UpdatedAt:    item.UpdatedAt,
	}
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertMemory(ctx, item); err != nil {
			return err
		}
		return tx.UpsertSearchDocument(ctx, document)
	}); err != nil {
		t.Fatal(err)
	}
	assertFTSMatches(t, store, "rebuild", 1)

	if _, err := store.writer.Exec(`INSERT INTO memory_fts(memory_fts) VALUES('delete-all')`); err != nil {
		t.Fatal(err)
	}
	assertFTSMatches(t, store, "rebuild", 0)
	if err := store.RebuildSearchIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertFTSMatches(t, store, "rebuild", 1)
}

func assertFTSMatches(t *testing.T, store *Store, term string, want int) {
	t.Helper()
	var count int
	if err := store.writer.QueryRow(`SELECT count(*) FROM memory_fts WHERE memory_fts MATCH ?`, term).Scan(&count); err != nil {
		t.Fatalf("FTS query %q: %v", term, err)
	}
	if count != want {
		t.Fatalf("FTS matches for %q = %d, want %d", term, count, want)
	}
}
