package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
)

const (
	// DefaultListen is the local-only HTTP/1.1 address from the architecture.
	DefaultListen = "127.0.0.1:18432"

	// DefaultWorkerCount is fixed at one sequential worker for the first
	// release.
	DefaultWorkerCount = 1

	// DefaultMaxAttempts is the total attempt budget, including the first try.
	DefaultMaxAttempts = 3

	// DefaultModelTimeout is the per-call remote model timeout.
	DefaultModelTimeout = 60 * time.Second

	// DefaultBusyTimeout bounds SQLite lock waits.
	DefaultBusyTimeout = 5 * time.Second

	// DefaultMaxReadConnections is the initialized small read pool size.
	DefaultMaxReadConnections = 4
)

var defaultRetryDelays = []time.Duration{time.Second, 5 * time.Second}

// Duration is a JSON string duration such as "60s".
type Duration time.Duration

// Std returns the duration as a standard time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON encodes the duration in Go duration syntax.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON parses a Go duration string. Numeric values are rejected so
// configuration files stay self-describing.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("duration must be a string such as \"60s\"")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value, err)
	}
	*d = Duration(parsed)
	return nil
}

// DatabaseConfig contains SQLite runtime settings used from P2 onward.
type DatabaseConfig struct {
	Path               string   `json:"path"`
	BusyTimeout        Duration `json:"busy_timeout"`
	MaxReadConnections int      `json:"max_read_connections"`
}

// ModelConfig maps to the first remote provider protocol. Credentials belong
// only in this local, permission-restricted config file.
type ModelConfig struct {
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key"`
	Timeout Duration `json:"timeout"`
}

// EmbeddingConfig is the separate document/query Embedding provider. It is
// disabled by default and may carry its own credential, which is never exposed
// by HTTP, logs or the profile fingerprint.
type EmbeddingConfig struct {
	Enabled       bool     `json:"enabled"`
	BaseURL       string   `json:"base_url"`
	APIKey        string   `json:"api_key"`
	Model         string   `json:"model"`
	Dimensions    int      `json:"dimensions"`
	Timeout       Duration `json:"timeout"`
	MinSimilarity float64  `json:"min_similarity"`
}

// WorkerConfig fixes the single-worker and three-attempt runtime policy.
type WorkerConfig struct {
	Workers     int        `json:"workers"`
	MaxAttempts int        `json:"max_attempts"`
	RetryDelays []Duration `json:"retry_delays"`
}

// LogConfig controls structured logging.
type LogConfig struct {
	Level string `json:"level"`
}

// Config is the complete local configuration file shape.
type Config struct {
	Listen    string          `json:"listen"`
	DataDir   string          `json:"data_dir"`
	Database  DatabaseConfig  `json:"database"`
	Model     ModelConfig     `json:"model"`
	Embedding EmbeddingConfig `json:"embedding"`
	Worker    WorkerConfig    `json:"worker"`
	Log       LogConfig       `json:"log"`
}

// LoadedConfig records the effective config and the file it came from. Path is
// empty when built-in defaults were used.
type LoadedConfig struct {
	Config Config
	Path   string
}

// DefaultDataDir returns ${XDG_DATA_HOME:-$HOME/.local/share}/dmemd.
func DefaultDataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "dmemd")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "dmemd")
	}
	return ""
}

// DefaultConfigPath returns ${XDG_CONFIG_HOME:-$HOME/.config}/dmemd/config.json.
func DefaultConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "dmemd", "config.json")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "dmemd", "config.json")
	}
	return ""
}

// DefaultConfig returns the built-in baseline before any file is loaded.
func DefaultConfig() Config {
	dataDir := DefaultDataDir()
	return Config{
		Listen:  DefaultListen,
		DataDir: dataDir,
		Database: DatabaseConfig{
			Path:               defaultDatabasePath(dataDir),
			BusyTimeout:        Duration(DefaultBusyTimeout),
			MaxReadConnections: DefaultMaxReadConnections,
		},
		Model: ModelConfig{
			Timeout: Duration(DefaultModelTimeout),
		},
		Embedding: EmbeddingConfig{
			Timeout: Duration(embedding.DefaultTimeout),
		},
		Worker: WorkerConfig{
			Workers:     DefaultWorkerCount,
			MaxAttempts: DefaultMaxAttempts,
			RetryDelays: durations(defaultRetryDelays),
		},
		Log: LogConfig{Level: "info"},
	}
}

func durations(values []time.Duration) []Duration {
	result := make([]Duration, len(values))
	for i, value := range values {
		result[i] = Duration(value)
	}
	return result
}

func defaultDatabasePath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "dmemd.db")
}

// LoadConfig applies this precedence: explicit path, default path if it exists,
// built-in defaults. A missing explicit path is an error; a missing default
// path is not. Existing config files must not be group/world-accessible because
// they may contain the remote model credential.
func LoadConfig(explicitPath string) (LoadedConfig, error) {
	cfg := DefaultConfig()
	// The built-in database path is derived from the effective data directory.
	// Clear it before decoding so a file that changes data_dir but omits
	// database.path gets the derived path, not the original user's default.
	cfg.Database.Path = ""
	path := explicitPath
	required := path != ""
	if path == "" {
		path = DefaultConfigPath()
	}

	if path != "" {
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil:
			if info.IsDir() {
				return LoadedConfig{}, fmt.Errorf("配置路径 %s 是目录，不是文件", path)
			}
			if err := CheckPrivateFile(path); err != nil {
				return LoadedConfig{}, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return LoadedConfig{}, fmt.Errorf("读取配置 %s: %w", path, err)
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&cfg); err != nil {
				return LoadedConfig{}, fmt.Errorf("解析配置 %s: %w", path, err)
			}
			var trailing any
			if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				return LoadedConfig{}, fmt.Errorf("解析配置 %s: 文件只能包含一个 JSON 对象", path)
			}
		case errors.Is(statErr, os.ErrNotExist) && !required:
			path = ""
		case errors.Is(statErr, os.ErrNotExist):
			return LoadedConfig{}, fmt.Errorf("配置文件 %s 不存在", path)
		default:
			return LoadedConfig{}, fmt.Errorf("检查配置 %s: %w", path, statErr)
		}
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return LoadedConfig{}, err
	}
	return LoadedConfig{Config: cfg, Path: path}, nil
}

func (cfg *Config) applyDefaults() {
	defaults := DefaultConfig()
	if cfg.Listen == "" {
		cfg.Listen = defaults.Listen
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaults.DataDir
	}
	if cfg.Database.Path == "" && cfg.DataDir != "" {
		cfg.Database.Path = defaultDatabasePath(cfg.DataDir)
	}
	if time.Duration(cfg.Database.BusyTimeout) == 0 {
		cfg.Database.BusyTimeout = defaults.Database.BusyTimeout
	}
	if cfg.Database.MaxReadConnections == 0 {
		cfg.Database.MaxReadConnections = defaults.Database.MaxReadConnections
	}
	if time.Duration(cfg.Model.Timeout) == 0 {
		cfg.Model.Timeout = defaults.Model.Timeout
	}
	if time.Duration(cfg.Embedding.Timeout) == 0 {
		cfg.Embedding.Timeout = defaults.Embedding.Timeout
	}
	if cfg.Worker.Workers == 0 {
		cfg.Worker.Workers = defaults.Worker.Workers
	}
	if cfg.Worker.MaxAttempts == 0 {
		cfg.Worker.MaxAttempts = defaults.Worker.MaxAttempts
	}
	if cfg.Worker.RetryDelays == nil {
		cfg.Worker.RetryDelays = defaults.Worker.RetryDelays
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = defaults.Log.Level
	}
}

// Validate rejects configuration that would violate the architecture or make
// startup ambiguous. It performs no filesystem changes.
func (cfg Config) Validate() error {
	host, portText, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen 必须是 host:port: %w", err)
	}
	if host == "" {
		return errors.New("listen 必须绑定回环地址，不能省略 host")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("listen host %q 不是回环地址；本地 API 不提供认证，首版只允许回环监听", host)
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("listen 端口 %q 无效", portText)
	}
	if cfg.DataDir == "" {
		return errors.New("无法确定数据目录，请设置 data_dir 或 XDG_DATA_HOME/HOME")
	}
	if cfg.Database.Path == "" {
		return errors.New("database.path 不能为空")
	}
	if time.Duration(cfg.Database.BusyTimeout) <= 0 {
		return errors.New("database.busy_timeout 必须大于 0")
	}
	if cfg.Database.MaxReadConnections <= 0 {
		return errors.New("database.max_read_connections 必须大于 0")
	}
	if cfg.Worker.Workers != DefaultWorkerCount {
		return fmt.Errorf("worker.workers 首版必须为 %d", DefaultWorkerCount)
	}
	if cfg.Worker.MaxAttempts != DefaultMaxAttempts {
		return fmt.Errorf("worker.max_attempts 首版必须为 %d", DefaultMaxAttempts)
	}
	if len(cfg.Worker.RetryDelays) != 2 {
		return errors.New("worker.retry_delays 必须包含两个间隔")
	}
	for _, delay := range cfg.Worker.RetryDelays {
		if time.Duration(delay) < 0 {
			return errors.New("worker.retry_delays 不能为负数")
		}
	}
	if time.Duration(cfg.Model.Timeout) <= 0 {
		return errors.New("model.timeout 必须大于 0")
	}
	if cfg.Model.BaseURL != "" {
		parsed, err := url.Parse(cfg.Model.BaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("model.base_url %q 必须是 http(s) 绝对地址", cfg.Model.BaseURL)
		}
	}
	if err := cfg.Embedding.toEmbedding().Validate(); err != nil {
		return err
	}
	if _, err := ParseLogLevel(cfg.Log.Level); err != nil {
		return err
	}
	return nil
}

// toEmbedding maps the runtime shape to the embedding package's validated
// configuration. Credentials stay in this local struct.
func (c EmbeddingConfig) toEmbedding() embedding.Config {
	return embedding.Config{
		Enabled:       c.Enabled,
		BaseURL:       c.BaseURL,
		APIKey:        c.APIKey,
		Model:         c.Model,
		Dimensions:    c.Dimensions,
		Timeout:       c.Timeout.Std(),
		MinSimilarity: c.MinSimilarity,
	}
}

// Prepare creates the private data/config directories and enforces 0700 on
// them. It never creates the database or a config file; those are created by
// later stages with file mode 0600.
func Prepare(cfg Config) error {
	if err := EnsurePrivateDir(cfg.DataDir); err != nil {
		return err
	}
	if dir := filepath.Dir(cfg.Database.Path); dir != "" && dir != "." {
		if err := EnsurePrivateDir(dir); err != nil {
			return err
		}
	}
	if dir := filepath.Dir(DefaultConfigPath()); dir != "" && dir != "." {
		if err := EnsurePrivateDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// EnsurePrivateDir creates path if needed and forces mode 0700.
func EnsurePrivateDir(path string) error {
	if path == "" {
		return errors.New("目录路径为空")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("创建目录 %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("检查目录 %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s 不是目录", path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("设置目录 %s 权限为 0700: %w", path, err)
	}
	return nil
}

// CheckPrivateFile rejects files accessible by group or other users. The
// service never silently widens access; callers should chmod credentials to
// 0600.
func CheckPrivateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("检查文件 %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是普通文件", path)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("文件 %s 权限为 %04o，需去除组/其他用户权限（建议 0600）", path, mode)
	}
	return nil
}

// OpenPrivateFile opens or creates a credential/database file with mode 0600.
// Callers pass flags appropriate for the operation and must close the file.
func OpenPrivateFile(path string, flag int) (*os.File, error) {
	file, err := os.OpenFile(path, flag|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// String implements fmt.Stringer for diagnostics without exposing the API key.
func (cfg Config) String() string {
	embeddingState := "disabled"
	if cfg.Embedding.Enabled {
		embeddingState = "enabled"
	}
	return strings.Join([]string{
		"listen=" + cfg.Listen,
		"data_dir=" + cfg.DataDir,
		"database=" + cfg.Database.Path,
		"model=" + cfg.Model.Name,
		"embedding=" + embeddingState,
	}, " ")
}
