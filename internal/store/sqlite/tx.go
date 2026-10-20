package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// ErrNestedTransaction is returned when business code tries to open a second
// transaction from inside a transaction callback. Statements inside a callback
// must use the supplied transaction scope instead.
var ErrNestedTransaction = errors.New("不能嵌套数据库事务")

type txContextKey struct{}

// Tx is the concrete transaction scope. It intentionally exposes no Commit or
// Rollback method: only Store.WithTx controls the transaction boundary.
type Tx struct {
	tx *sql.Tx
}

// WithTx runs one short write transaction. The callback must not perform model
// calls or long computations. Callback errors, SQL errors, cancellation and
// panic all roll the transaction back.
func (s *Store) WithTx(ctx context.Context, fn func(context.Context, *Tx) error) error {
	if ctx == nil {
		return errors.New("事务 context 为空")
	}
	if fn == nil {
		return errors.New("事务回调为空")
	}
	if _, nested := ctx.Value(txContextKey{}).(bool); nested {
		return ErrNestedTransaction
	}

	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始数据库事务: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	callbackCtx := context.WithValue(ctx, txContextKey{}, true)
	if err := fn(callbackCtx, &Tx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交数据库事务: %w", err)
	}
	committed = true
	return nil
}

// MemoryStore adapts Store to the memory module's transaction capability.
type MemoryStore struct{ *Store }

// WithTx implements memory.Store.
func (s MemoryStore) WithTx(ctx context.Context, fn func(context.Context, memory.Tx) error) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx *Tx) error { return fn(ctx, tx) })
}

// SourceStore adapts Store to the source module's transaction capability.
type SourceStore struct{ *Store }

// WithTx implements source.Store.
func (s SourceStore) WithTx(ctx context.Context, fn func(context.Context, source.Tx) error) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx *Tx) error { return fn(ctx, tx) })
}

// IngestStore adapts Store to the ingest module's complete transaction scope.
type IngestStore struct{ *Store }

// WithTx implements ingest.Store.
func (s IngestStore) WithTx(ctx context.Context, fn func(context.Context, ingest.Tx) error) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx *Tx) error { return fn(ctx, tx) })
}

var (
	_ memory.Store = MemoryStore{}
	_ source.Store = SourceStore{}
	_ ingest.Store = IngestStore{}
)
