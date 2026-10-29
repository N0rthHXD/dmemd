package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/idgen"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dmemd.db")
	store, err := Open(context.Background(), Config{
		Path:               path,
		BusyTimeout:        5 * time.Second,
		MaxReadConnections: 4,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}

func testTime(offset time.Duration) time.Time {
	return time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC).Add(offset)
}

func seedSource(t *testing.T, store *Store, id string) source.Source {
	t.Helper()
	item := source.Source{
		ID:        id,
		AppID:     "test-app",
		Content:   []byte(`{"message":"hello"}`),
		CreatedAt: testTime(0),
	}
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		return tx.InsertSource(ctx, item)
	}); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	return item
}

func seedMemory(t *testing.T, store *Store, sourceID string, item memory.Memory) {
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
	if item.OriginSourceID == "" {
		item.OriginSourceID = sourceID
	}
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		return tx.InsertMemory(ctx, item)
	}); err != nil {
		t.Fatalf("insert memory: %v", err)
	}
}

func seedEvidence(t *testing.T, store *Store, item source.Evidence) {
	t.Helper()
	if item.ID == "" {
		item.ID = idgen.NewUUID()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = testTime(0)
	}
	if err := store.WithTx(context.Background(), func(ctx context.Context, tx *Tx) error {
		return tx.InsertEvidence(ctx, item)
	}); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}
}
