package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func TestProductionServicesUseSharedNormalization(t *testing.T) {
	store := testutil.NewStore(t)
	memories := NewMemoryService(store)
	search := NewSearchService(store, discardLogger())
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)

	var created memory.Memory
	if err := store.WithTx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		var err error
		created, err = memories.Create(ctx, tx, memory.CreateInput{
			Kind: memory.KindFact, Title: "AI记忆", Content: "记忆服务保留上下文。",
			ObservedAt: now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	result, err := search.Search(ctx, retrieval.Request{Query: "记忆"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Memory.ID != created.ID {
		t.Fatalf("production search result = %+v", result.Items)
	}
}
