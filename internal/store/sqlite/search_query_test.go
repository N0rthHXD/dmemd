package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
)

func TestSearchChinesePhraseFiltersRankingAndLimits(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-search-query"
	seedSource(t, store, sourceID)

	early := testTime(0)
	late := testTime(10 * time.Minute)
	titleMatch := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "回答", Content: "无关正文", Kind: memory.KindPreference,
		CreatedByApp: "app-title", UpdatedAt: late,
	})
	contentMatch := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "无关标题", Content: "回答内容", Kind: memory.KindFact,
		CreatedByApp: "app-content", UpdatedAt: late.Add(time.Minute),
	})
	archived := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "回答归档", Content: "归档回答", Kind: memory.KindPreference,
		State: memory.StateArchived, CreatedByApp: "app-title", UpdatedAt: late.Add(2 * time.Minute),
	})
	deleted := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "回答删除", Content: "删除回答", Kind: memory.KindFact,
		State: memory.StateDeleted, CreatedByApp: "app-title", UpdatedAt: late.Add(3 * time.Minute),
	})
	other := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "记忆", Content: "中文记忆检索", Kind: memory.KindFact,
		CreatedByApp: "app-other", UpdatedAt: early,
	})

	expression, err := retrieval.Compile(retrieval.Groups("回答"), retrieval.MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	hits := storeHits(t, store, expression, retrieval.Filters{States: []memory.State{memory.StateActive}}, 20)
	if len(hits) != 2 || hits[0].MemoryID != titleMatch.ID || hits[1].MemoryID != contentMatch.ID {
		t.Fatalf("active ranking = %+v, want title match first", hits)
	}
	if !(hits[0].BM25 < hits[1].BM25) {
		t.Fatalf("bm25 direction = %v vs %v; title-weighted hit should be smaller", hits[0].BM25, hits[1].BM25)
	}

	if hits := storeHits(t, store, expression, retrieval.Filters{States: []memory.State{memory.StateArchived}}, 20); len(hits) != 1 || hits[0].MemoryID != archived.ID {
		t.Fatalf("archived hits = %+v", hits)
	}
	if hits := storeHits(t, store, expression, retrieval.Filters{States: []memory.State{memory.StateDeleted}}, 20); len(hits) != 0 {
		t.Fatalf("deleted hits = %+v", hits)
	}
	if hits := storeHits(t, store, expression, retrieval.Filters{States: []memory.State{memory.StateActive, memory.StateArchived}}, 20); len(hits) != 3 {
		t.Fatalf("active+archived hits = %+v", hits)
	}
	if hits := storeHits(t, store, expression, retrieval.Filters{
		States: []memory.State{memory.StateActive}, Kinds: []memory.Kind{memory.KindPreference},
	}, 20); len(hits) != 1 || hits[0].MemoryID != titleMatch.ID {
		t.Fatalf("kind filter hits = %+v", hits)
	}
	if hits := storeHits(t, store, expression, retrieval.Filters{
		States: []memory.State{memory.StateActive}, CreatedByApp: "app-content",
	}, 20); len(hits) != 1 || hits[0].MemoryID != contentMatch.ID {
		t.Fatalf("app filter hits = %+v", hits)
	}
	after := late.Add(30 * time.Second)
	if hits := storeHits(t, store, expression, retrieval.Filters{
		States: []memory.State{memory.StateActive}, UpdatedAfter: &after,
	}, 20); len(hits) != 1 || hits[0].MemoryID != contentMatch.ID {
		t.Fatalf("updated_after hits = %+v", hits)
	}
	if hits := storeHits(t, store, expression, retrieval.Filters{States: []memory.State{memory.StateActive}}, 1); len(hits) != 1 || hits[0].MemoryID != titleMatch.ID {
		t.Fatalf("top_k hits = %+v", hits)
	}

	phrase, err := retrieval.Compile(retrieval.Groups("记忆检索"), retrieval.MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	if hits := storeHits(t, store, phrase, retrieval.Filters{States: []memory.State{memory.StateActive}}, 20); len(hits) != 1 || hits[0].MemoryID != other.ID {
		t.Fatalf("Chinese phrase hits = %+v", hits)
	}
	notAdjacent, err := retrieval.Compile(retrieval.Groups("记忆 回答"), retrieval.MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	if hits := storeHits(t, store, notAdjacent, retrieval.Filters{States: []memory.State{memory.StateActive}}, 20); len(hits) != 0 {
		t.Fatalf("non-adjacent terms matched = %+v", hits)
	}

	// A deleted snapshot is excluded even if a stale derived document exists.
	var docs int
	if err := store.writer.QueryRow(`SELECT count(*) FROM memory_search_docs WHERE memory_id = ?`, deleted.ID).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if docs != 1 {
		t.Fatalf("test setup: deleted document count = %d", docs)
	}
}

func TestSearchOrdersTiesByUpdatedAtThenID(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-search-ties"
	seedSource(t, store, sourceID)
	sameTime := testTime(0)
	first := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "tie", Content: "tie", Kind: memory.KindFact, UpdatedAt: sameTime,
	})
	second := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "tie", Content: "tie", Kind: memory.KindFact, UpdatedAt: sameTime,
	})
	newer := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "tie", Content: "tie", Kind: memory.KindFact, UpdatedAt: sameTime.Add(time.Minute),
	})
	expression, err := retrieval.Compile(retrieval.Groups("tie"), retrieval.MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	hits := storeHits(t, store, expression, retrieval.Filters{States: []memory.State{memory.StateActive}}, 20)
	if len(hits) != 3 || hits[0].MemoryID != newer.ID {
		t.Fatalf("updated_at ordering = %+v", hits)
	}
	expected := []string{first.ID, second.ID}
	if second.ID < first.ID {
		expected = []string{second.ID, first.ID}
	}
	if hits[1].MemoryID != expected[0] || hits[2].MemoryID != expected[1] {
		t.Fatalf("id tie ordering = %+v, want %v", hits[1:], expected)
	}
}

func TestRebuildSearchDocumentsRegeneratesDocsAndFTS(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-search-rebuild-docs"
	seedSource(t, store, sourceID)
	active := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "回答语言", Content: "用户希望默认使用中文回答。", Kind: memory.KindPreference,
	})
	archived := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "归档记忆", Content: "归档后仍保留索引。", Kind: memory.KindFact,
		State: memory.StateArchived,
	})
	deleted := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "删除记忆", Content: "删除后不再索引。", Kind: memory.KindFact,
		State: memory.StateDeleted,
	})

	if _, err := store.writer.Exec(`UPDATE memory_search_docs SET title_terms = 'broken', content_terms = 'broken'`); err != nil {
		t.Fatal(err)
	}
	if hits := storeHits(t, store, mustCompile(t, "回答"), retrieval.Filters{States: []memory.State{memory.StateActive}}, 20); len(hits) != 0 {
		t.Fatalf("corrupted docs still matched: %+v", hits)
	}

	indexed, err := store.RebuildSearchDocuments(context.Background(), retrieval.NewDocumentBuilder())
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 2 {
		t.Fatalf("indexed = %d, want active+archived", indexed)
	}
	if hits := storeHits(t, store, mustCompile(t, "回答"), retrieval.Filters{States: []memory.State{memory.StateActive}}, 20); len(hits) != 1 || hits[0].MemoryID != active.ID {
		t.Fatalf("active hits after rebuild = %+v", hits)
	}
	if hits := storeHits(t, store, mustCompile(t, "归档"), retrieval.Filters{States: []memory.State{memory.StateArchived}}, 20); len(hits) != 1 || hits[0].MemoryID != archived.ID {
		t.Fatalf("archived hits after rebuild = %+v", hits)
	}
	if hits := storeHits(t, store, mustCompile(t, "删除"), retrieval.Filters{States: []memory.State{memory.StateActive, memory.StateArchived}}, 20); len(hits) != 0 {
		t.Fatalf("deleted memory reappeared after rebuild: %+v", hits)
	}
	if count := indexedDocuments(t, store); count != 2 {
		t.Fatalf("documents after rebuild = %d", count)
	}
	var deletedDocs int
	if err := store.writer.QueryRow(`SELECT count(*) FROM memory_search_docs WHERE memory_id = ?`, deleted.ID).Scan(&deletedDocs); err != nil {
		t.Fatal(err)
	}
	if deletedDocs != 0 {
		t.Fatalf("deleted docs after rebuild = %d", deletedDocs)
	}
}

func TestRebuildSearchDocumentsRollsBackOnBuilderFailure(t *testing.T) {
	store := newTestStore(t)
	sourceID := "source-search-rebuild-failure"
	seedSource(t, store, sourceID)
	first := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "第一条", Content: "回答内容", Kind: memory.KindFact,
	})
	second := seedIndexedMemory(t, store, sourceID, memory.Memory{
		Title: "第二条", Content: "记忆内容", Kind: memory.KindFact,
	})

	if _, err := store.RebuildSearchDocuments(context.Background(), &failingDocumentBuilder{failAfter: 1}); !errors.Is(err, errBuilderFailure) {
		t.Fatalf("rebuild error = %v", err)
	}
	if count := indexedDocuments(t, store); count != 2 {
		t.Fatalf("documents after failed rebuild = %d", count)
	}
	if hits := storeHits(t, store, mustCompile(t, "回答"), retrieval.Filters{States: []memory.State{memory.StateActive}}, 20); len(hits) != 1 || hits[0].MemoryID != first.ID {
		t.Fatalf("first hits after rollback = %+v", hits)
	}
	if hits := storeHits(t, store, mustCompile(t, "记忆"), retrieval.Filters{States: []memory.State{memory.StateActive}}, 20); len(hits) != 1 || hits[0].MemoryID != second.ID {
		t.Fatalf("second hits after rollback = %+v", hits)
	}
}

var errBuilderFailure = errors.New("injected builder failure")

type failingDocumentBuilder struct {
	failAfter int
	calls     int
}

func (b *failingDocumentBuilder) BuildDocument(item memory.Memory) (memory.SearchDocument, error) {
	b.calls++
	if b.calls > b.failAfter {
		return memory.SearchDocument{}, errBuilderFailure
	}
	return retrieval.NewDocumentBuilder().BuildDocument(item)
}

func seedIndexedMemory(t *testing.T, store *Store, sourceID string, item memory.Memory) memory.Memory {
	t.Helper()
	if item.ID == "" {
		item.ID = idgen.NewUUID()
	}
	if item.Version == 0 {
		item.Version = 1
	}
	if item.State == "" {
		item.State = memory.StateActive
	}
	if item.Kind == "" {
		item.Kind = memory.KindFact
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = testTime(0)
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	if item.ObservedAt.IsZero() {
		item.ObservedAt = item.CreatedAt
	}
	if item.OriginSourceID == "" {
		item.OriginSourceID = sourceID
	}
	document, err := retrieval.NewDocumentBuilder().BuildDocument(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		if err := tx.InsertMemory(ctx, item); err != nil {
			return err
		}
		return tx.UpsertSearchDocument(ctx, document)
	}); err != nil {
		t.Fatal(err)
	}
	return item
}

func storeHits(t *testing.T, store *Store, expression string, filters retrieval.Filters, topK int) []retrieval.Hit {
	t.Helper()
	hits, err := store.Search(context.Background(), retrieval.Query{
		Expression: expression,
		Filters:    filters,
		TopK:       topK,
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func mustCompile(t *testing.T, query string) string {
	t.Helper()
	expression, err := retrieval.Compile(retrieval.Groups(query), retrieval.MatchAll)
	if err != nil {
		t.Fatal(err)
	}
	return expression
}

func indexedDocuments(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.writer.QueryRow(`SELECT count(*) FROM memory_search_docs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
