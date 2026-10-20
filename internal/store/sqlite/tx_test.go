package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestWithTxCommitsAndRollsBack(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	t.Run("commit", func(t *testing.T) {
		item := source.Source{ID: "source-commit", Content: []byte(`"committed"`), CreatedAt: testTime(0)}
		if err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
			return tx.InsertSource(ctx, item)
		}); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.GetSource(ctx, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(loaded.Content) != `"committed"` {
			t.Fatalf("content = %s", loaded.Content)
		}
	})

	t.Run("callback error", func(t *testing.T) {
		sentinel := errors.New("callback failed")
		item := source.Source{ID: "source-callback-error", Content: []byte(`"discarded"`), CreatedAt: testTime(0)}
		err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
			if err := tx.InsertSource(ctx, item); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("WithTx error = %v, want sentinel", err)
		}
		if _, err := store.GetSource(ctx, item.ID); !errors.Is(err, source.ErrNotFound) {
			t.Fatalf("GetSource error = %v, want not found", err)
		}
	})

	t.Run("sql failure", func(t *testing.T) {
		first := source.Source{ID: "source-sql-error", Content: []byte(`"first"`), CreatedAt: testTime(0)}
		err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
			if err := tx.InsertSource(ctx, first); err != nil {
				return err
			}
			return tx.InsertSource(ctx, first)
		})
		if err == nil {
			t.Fatal("duplicate source insert succeeded")
		}
		if _, err := store.GetSource(ctx, first.ID); !errors.Is(err, source.ErrNotFound) {
			t.Fatalf("GetSource error = %v, want not found", err)
		}
	})

	t.Run("cancel before commit", func(t *testing.T) {
		txCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		item := source.Source{ID: "source-cancel", Content: []byte(`"cancelled"`), CreatedAt: testTime(0)}
		err := store.WithTx(txCtx, func(ctx context.Context, tx *Tx) error {
			if err := tx.InsertSource(ctx, item); err != nil {
				return err
			}
			cancel()
			return nil
		})
		if err == nil {
			t.Fatal("WithTx committed after cancellation")
		}
		if _, err := store.GetSource(ctx, item.ID); !errors.Is(err, source.ErrNotFound) {
			t.Fatalf("GetSource error = %v, want not found", err)
		}
	})

	t.Run("pre-cancelled context", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		err := store.WithTx(cancelled, func(ctx context.Context, tx *Tx) error {
			return tx.InsertSource(ctx, source.Source{ID: "source-pre-cancelled", Content: []byte(`null`), CreatedAt: testTime(0)})
		})
		if err == nil {
			t.Fatal("WithTx succeeded with a cancelled context")
		}
	})

	t.Run("nested transaction", func(t *testing.T) {
		item := source.Source{ID: "source-nested", Content: []byte(`"outer"`), CreatedAt: testTime(0)}
		err := store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
			if err := tx.InsertSource(ctx, item); err != nil {
				return err
			}
			nestedErr := store.WithTx(ctx, func(context.Context, *Tx) error { return nil })
			if !errors.Is(nestedErr, ErrNestedTransaction) {
				t.Fatalf("nested WithTx error = %v, want ErrNestedTransaction", nestedErr)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSource(ctx, item.ID); err != nil {
			t.Fatalf("outer transaction did not commit: %v", err)
		}
	})

	t.Run("panic rolls back", func(t *testing.T) {
		item := source.Source{ID: "source-panic", Content: []byte(`"panic"`), CreatedAt: testTime(0)}
		func() {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("panic did not propagate")
				}
			}()
			_ = store.WithTx(ctx, func(ctx context.Context, tx *Tx) error {
				if err := tx.InsertSource(ctx, item); err != nil {
					t.Fatal(err)
				}
				panic("boom")
			})
		}()
		if _, err := store.GetSource(ctx, item.ID); !errors.Is(err, source.ErrNotFound) {
			t.Fatalf("GetSource error = %v, want not found", err)
		}
	})
}

func TestWithTxPropagatesDeadline(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	err := store.WithTx(ctx, func(context.Context, *Tx) error { return nil })
	if err == nil {
		t.Fatal("WithTx succeeded with an expired deadline")
	}
}
