// Package testutil provides real temporary SQLite databases, deterministic
// document builders and a controllable fake model HTTP service. It is only
// imported by tests and never by production code.
package testutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/store/sqlite"
	_ "modernc.org/sqlite"
)

// DefaultBusyTimeout matches the runtime default used by real databases.
const DefaultBusyTimeout = 5 * time.Second

// ErrInjectedFault is returned by a FaultInjector checkpoint when configured.
var ErrInjectedFault = errors.New("testutil: injected transaction fault")

// NewStore creates an isolated temporary file database, applies every
// migration, verifies private file permissions and registers cleanup. Tests may
// run in parallel because each call owns a separate directory.
func NewStore(t testing.TB) *sqlite.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dmemd.db")
	return NewStoreAt(t, path)
}

// NewStoreAt opens and migrates a database at an explicit path. It is useful
// for restart tests; the caller still owns no cleanup beyond the registered one.
func NewStoreAt(t testing.TB, path string) *sqlite.Store {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Config{
		Path:               path,
		BusyTimeout:        DefaultBusyTimeout,
		MaxReadConnections: 4,
	})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	if err := store.CheckPermissions(); err != nil {
		t.Fatalf("database permissions: %v", err)
	}
	return store
}

// OpenInspector opens the same database file through a separate, query-only
// connection so a test can verify committed rows without using store APIs.
func OpenInspector(t testing.TB, path string) *sql.DB {
	t.Helper()
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "query_only(1)")
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatalf("open inspector: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping inspector: %v", err)
	}
	return db
}

// CheckPrivateDatabaseFiles asserts 0700 on the directory and 0600 on the
// database plus existing WAL/SHM companions.
func CheckPrivateDatabaseFiles(t testing.TB, path string) {
	t.Helper()
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat database directory: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("database directory mode = %04o, want 0700", mode)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", candidate, mode)
		}
	}
}

// DocumentBuilder is the deterministic pre-normalized builder used by tests.
// Production wiring supplies NFC/tokenization.
type DocumentBuilder struct{}

// BuildDocument implements memory.DocumentBuilder.
func (DocumentBuilder) BuildDocument(item memory.Memory) (memory.SearchDocument, error) {
	return memory.SearchDocument{
		MemoryID:     item.ID,
		TitleTerms:   strings.ToLower(item.Title),
		ContentTerms: strings.ToLower(item.Content),
		ContentHash:  embedding.DocumentHash(item),
	}, nil
}

// FaultInjector marks a named step and returns an error only when that step is
// the configured failure point. Steps make transaction rollback assertions
// precise without exposing fault switches in production code.
type FaultInjector struct {
	FailAt string
	Err    error
	steps  []string
	hit    bool
}

// Checkpoint records step and returns an injected error at the configured point.
func (f *FaultInjector) Checkpoint(step string) error {
	f.steps = append(f.steps, step)
	if f.FailAt != step {
		return nil
	}
	f.hit = true
	if f.Err != nil {
		return f.Err
	}
	return fmt.Errorf("%w at %s", ErrInjectedFault, step)
}

// Hit reports whether the configured failure point was reached.
func (f *FaultInjector) Hit() bool { return f.hit }

// Steps returns the checkpoints reached so far.
func (f *FaultInjector) Steps() []string {
	return append([]string(nil), f.steps...)
}
