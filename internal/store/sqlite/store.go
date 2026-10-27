// Package sqlite implements the persistence capabilities consumed by the
// memory, source, ingest and retrieval modules. All SQL in the service lives
// here; business packages never see *sql.Tx or a database handle.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultBusyTimeout        = 5 * time.Second
	defaultMaxReadConnections = 4
	defaultGovernanceMaxWait  = 30 * time.Minute
)

// Config is the physical database configuration derived from runtime.Config.
type Config struct {
	Path               string
	BusyTimeout        time.Duration
	MaxReadConnections int
}

// Store owns one SQLite database file. The writer is deliberately limited to a
// single connection; readers use a small pool. Every physical connection gets
// its PRAGMAs from the DSN, so replacing or recreating a pooled connection
// cannot silently lose them.
type Store struct {
	path     string
	writer   *sql.DB
	reader   *sql.DB
	migrate  sync.Mutex
	ready    atomic.Bool
	close    sync.Once
	closeErr error

	embeddingMu        sync.RWMutex
	embeddingEnabled   bool
	embeddingProfileID string

	governanceMu      sync.RWMutex
	governanceMaxWait time.Duration
}

// ConfigureGovernanceFairness sets the maximum age a due governance target may
// reach while continuous ingest keeps the worker busy. A non-positive value
// restores the default.
func (s *Store) ConfigureGovernanceFairness(maxWait time.Duration) error {
	if s == nil {
		return errors.New("database store is nil")
	}
	if maxWait <= 0 {
		maxWait = defaultGovernanceMaxWait
	}
	s.governanceMu.Lock()
	s.governanceMaxWait = maxWait
	s.governanceMu.Unlock()
	return nil
}

func (s *Store) governanceFairnessLimit() time.Duration {
	s.governanceMu.RLock()
	defer s.governanceMu.RUnlock()
	if s.governanceMaxWait <= 0 {
		return defaultGovernanceMaxWait
	}
	return s.governanceMaxWait
}

// ConfigureEmbedding installs the single Embedding profile used by the memory
// write path. It must be called before serving writes. Disabling the feature
// keeps content hashes and stale-vector cleanup active but creates no tasks.
func (s *Store) ConfigureEmbedding(enabled bool, profileID string) {
	s.embeddingMu.Lock()
	defer s.embeddingMu.Unlock()
	s.embeddingEnabled = enabled
	if enabled {
		s.embeddingProfileID = profileID
	} else {
		s.embeddingProfileID = ""
	}
}

func (s *Store) embeddingConfig() (bool, string) {
	s.embeddingMu.RLock()
	defer s.embeddingMu.RUnlock()
	return s.embeddingEnabled, s.embeddingProfileID
}

// Open validates the path, creates a private database file when needed, opens
// the single writer and read pool, and verifies the connection settings. It
// does not apply migrations; callers run Migrate before serving requests.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, errors.New("database path 不能为空")
	}
	if cfg.BusyTimeout <= 0 {
		cfg.BusyTimeout = defaultBusyTimeout
	}
	if cfg.MaxReadConnections <= 0 {
		cfg.MaxReadConnections = defaultMaxReadConnections
	}

	path, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("解析数据库路径: %w", err)
	}
	if err := prepareDatabaseFile(path); err != nil {
		return nil, err
	}

	writer, err := sql.Open("sqlite", writerDSN(path, cfg.BusyTimeout))
	if err != nil {
		return nil, fmt.Errorf("打开数据库写连接: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)
	if err := writer.PingContext(ctx); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("连接数据库: %w", err)
	}
	if err := verifyConnectionSettings(ctx, writer, cfg.BusyTimeout, false); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := enforceSidecarPermissions(path); err != nil {
		_ = writer.Close()
		return nil, err
	}

	reader, err := sql.Open("sqlite", readerDSN(path, cfg.BusyTimeout))
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("打开数据库读连接: %w", err)
	}
	reader.SetMaxOpenConns(cfg.MaxReadConnections)
	reader.SetMaxIdleConns(cfg.MaxReadConnections)
	reader.SetConnMaxLifetime(0)
	if err := reader.PingContext(ctx); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("连接数据库读池: %w", err)
	}
	if err := verifyConnectionSettings(ctx, reader, cfg.BusyTimeout, true); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}

	return &Store{path: path, writer: writer, reader: reader}, nil
}

// Path returns the absolute database path.
func (s *Store) Path() string { return s.path }

// Close closes both pools. It is safe to call more than once.
func (s *Store) Close() error {
	s.close.Do(func() {
		s.ready.Store(false)
		var errs []error
		if s.reader != nil {
			if err := s.reader.Close(); err != nil {
				errs = append(errs, fmt.Errorf("关闭数据库读池: %w", err))
			}
		}
		if s.writer != nil {
			if err := s.writer.Close(); err != nil {
				errs = append(errs, fmt.Errorf("关闭数据库写连接: %w", err))
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// Ready checks that migrations completed and both pools still answer. A model
// outage is deliberately outside this probe.
func (s *Store) Ready(ctx context.Context) error {
	if !s.ready.Load() {
		return errors.New("数据库迁移尚未完成")
	}
	if err := s.writer.PingContext(ctx); err != nil {
		return fmt.Errorf("数据库写连接不可用: %w", err)
	}
	if err := s.reader.PingContext(ctx); err != nil {
		return fmt.Errorf("数据库读连接不可用: %w", err)
	}
	return nil
}

// CheckPermissions verifies the database file, WAL/SHM sidecars and containing
// directory are not group/world accessible.
func (s *Store) CheckPermissions() error {
	var errs []error
	if info, err := os.Stat(filepath.Dir(s.path)); err != nil {
		errs = append(errs, fmt.Errorf("检查数据库目录: %w", err))
	} else if info.Mode().Perm()&0o077 != 0 {
		errs = append(errs, fmt.Errorf("数据库目录 %s 权限为 %04o，需为 0700", filepath.Dir(s.path), info.Mode().Perm()))
	}
	for _, path := range databaseFiles(s.path) {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("检查数据库文件 %s: %w", path, err))
			continue
		}
		if !info.Mode().IsRegular() {
			errs = append(errs, fmt.Errorf("数据库文件 %s 不是普通文件", path))
			continue
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			errs = append(errs, fmt.Errorf("数据库文件 %s 权限为 %04o，需为 0600", path, mode))
		}
	}
	return errors.Join(errs...)
}

func writerDSN(path string, busy time.Duration) string {
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "journal_mode(WAL)")
	return fileDSN(path, query)
}

func readerDSN(path string, busy time.Duration) string {
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "query_only(1)")
	return fileDSN(path, query)
}

func fileDSN(path string, query url.Values) string {
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query.Encode()}
	return uri.String()
}

func prepareDatabaseFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建数据库目录 %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("设置数据库目录权限: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("创建数据库文件 %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("设置数据库文件权限: %w", err)
	}
	return file.Close()
}

func enforceSidecarPermissions(path string) error {
	for _, sidecar := range databaseFiles(path) {
		info, err := os.Stat(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("检查数据库伴随文件 %s: %w", sidecar, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("数据库伴随文件 %s 不是普通文件", sidecar)
		}
		if info.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(sidecar, 0o600); err != nil {
				return fmt.Errorf("收窄数据库伴随文件 %s 权限: %w", sidecar, err)
			}
		}
	}
	return nil
}

func databaseFiles(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

type queryRowContexter interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func verifyConnectionSettings(ctx context.Context, db queryRowContexter, busy time.Duration, readOnly bool) error {
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("读取 journal_mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("journal_mode = %q，需要 wal", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("读取 foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys = %d，需要 1", foreignKeys)
	}

	var synchronous int
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("读取 synchronous: %w", err)
	}
	if synchronous != 2 {
		return fmt.Errorf("synchronous = %d，需要 FULL(2)", synchronous)
	}

	var busyTimeout int64
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("读取 busy_timeout: %w", err)
	}
	if busyTimeout != busy.Milliseconds() {
		return fmt.Errorf("busy_timeout = %d，需要 %d", busyTimeout, busy.Milliseconds())
	}

	if readOnly {
		var queryOnly int
		if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
			return fmt.Errorf("读取 query_only: %w", err)
		}
		if queryOnly != 1 {
			return fmt.Errorf("读连接 query_only = %d，需要 1", queryOnly)
		}
	}
	return nil
}
