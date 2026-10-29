package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfigMeetsArchitectureDefaults(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	cfg := DefaultConfig()

	if cfg.Listen != "127.0.0.1:18432" {
		t.Fatalf("Listen = %q", cfg.Listen)
	}
	if got := cfg.Model.Timeout.Std(); got != 60*time.Second {
		t.Fatalf("Model.Timeout = %v", got)
	}
	if cfg.Worker.Workers != 1 || cfg.Worker.MaxAttempts != 3 {
		t.Fatalf("worker defaults = %+v", cfg.Worker)
	}
	if cfg.Worker.ExtractionTimeout.Std() != 180*time.Second ||
		cfg.Worker.GovernanceTimeout.Std() != 60*time.Second ||
		cfg.Worker.AggregationTimeout.Std() != 60*time.Second {
		t.Fatalf("worker timeouts = %+v", cfg.Worker)
	}
	if len(cfg.Worker.RetryDelays) != 3 ||
		cfg.Worker.RetryDelays[0].Std() != 5*time.Second ||
		cfg.Worker.RetryDelays[1].Std() != 30*time.Second ||
		cfg.Worker.RetryDelays[2].Std() != 2*time.Minute {
		t.Fatalf("retry delays = %v", cfg.Worker.RetryDelays)
	}
	if !cfg.Governance.IsEnabled() ||
		cfg.Governance.Debounce.Std() != 30*time.Second ||
		cfg.Governance.ReconcileInterval.Std() != 15*time.Minute ||
		cfg.Governance.MinCallInterval.Std() != 2*time.Minute ||
		cfg.Governance.MaxInputs != 64 {
		t.Fatalf("governance defaults = %+v", cfg.Governance)
	}
	if !cfg.Aggregation.IsEnabled() ||
		cfg.Aggregation.Debounce.Std() != DefaultAggregationDebounce ||
		cfg.Aggregation.ReconcileInterval.Std() != DefaultAggregationReconcileInterval ||
		cfg.Aggregation.MaxInputs != DefaultAggregationMaxInputs ||
		cfg.Aggregation.L1BucketSize.Std() != 24*time.Hour {
		t.Fatalf("aggregation defaults = %+v", cfg.Aggregation)
	}
	if cfg.Database.BusyTimeout.Std() != 5*time.Second || cfg.Database.MaxReadConnections != 4 {
		t.Fatalf("database defaults = %+v", cfg.Database)
	}
	if cfg.Embedding.Enabled {
		t.Fatal("embedding is enabled by default")
	}
	if cfg.Embedding.Timeout.Std() != 30*time.Second {
		t.Fatalf("embedding timeout default = %v", cfg.Embedding.Timeout)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
}

func TestEmbeddingConfigValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = filepath.Join(cfg.DataDir, "dmemd.db")
	cfg.Embedding.Enabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("enabled embedding without endpoint/model/dimensions was accepted")
	}
	cfg.Embedding.BaseURL = "http://127.0.0.1:9999"
	cfg.Embedding.Model = "embedding-test"
	cfg.Embedding.Dimensions = 3
	cfg.Embedding.MinSimilarity = 0.25
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid embedding config rejected: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"bad endpoint":    func(value *Config) { value.Embedding.BaseURL = "not-a-url" },
		"zero dimensions": func(value *Config) { value.Embedding.Dimensions = 0 },
		"bad similarity":  func(value *Config) { value.Embedding.MinSimilarity = 1.5 },
	} {
		mutated := cfg
		mutate(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestLoadWorkerTimeoutsAndConfigurableAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, path, `{"worker":{"extraction_timeout":"200s","governance_timeout":"300s","aggregation_timeout":"400s","max_attempts":5}}`)
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	w := loaded.Config.Worker
	if w.ExtractionTimeout.Std() != 200*time.Second ||
		w.GovernanceTimeout.Std() != 300*time.Second ||
		w.AggregationTimeout.Std() != 400*time.Second || w.MaxAttempts != 5 {
		t.Fatalf("worker=%+v", w)
	}
}

func TestLoadAggregationConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, path, `{"aggregation":{"enabled":false,"debounce":"2s","reconcile_interval":"3m","max_inputs":8,"l1_bucket_size":"12h"}}`)
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	a := loaded.Config.Aggregation
	if a.IsEnabled() || a.Debounce.Std() != 2*time.Second ||
		a.ReconcileInterval.Std() != 3*time.Minute || a.MaxInputs != 8 ||
		a.L1BucketSize.Std() != 12*time.Hour {
		t.Fatalf("aggregation=%+v", a)
	}
}

func TestLoadConfigPartialFileUsesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigFile(t, path, `{
		"listen": "127.0.0.1:0",
		"data_dir": "`+filepath.Join(dir, "data")+`",
		"model": {"name": "test-model"},
		"log": {"level": "debug"}
	}`)

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.Path != path {
		t.Fatalf("Path = %q", loaded.Path)
	}
	if loaded.Config.Model.Timeout.Std() != 60*time.Second {
		t.Fatalf("model timeout default not applied: %v", loaded.Config.Model.Timeout)
	}
	if loaded.Config.Database.Path != filepath.Join(dir, "data", "dmemd.db") {
		t.Fatalf("database path = %q", loaded.Config.Database.Path)
	}
	if loaded.Config.Worker.MaxAttempts != 3 {
		t.Fatalf("max attempts = %d", loaded.Config.Worker.MaxAttempts)
	}
	if !loaded.Config.Governance.IsEnabled() || loaded.Config.Governance.MaxInputs != 64 {
		t.Fatalf("governance defaults = %+v", loaded.Config.Governance)
	}
}

func TestLoadConfigGovernanceDisabledExplicitly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigFile(t, path, `{
		"listen": "127.0.0.1:0",
		"data_dir": "`+filepath.Join(dir, "data")+`",
		"governance": {"enabled": false}
	}`)
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.Config.Governance.IsEnabled() {
		t.Fatal("explicit governance.enabled=false was ignored")
	}
	if loaded.Config.Governance.MaxInputs != 64 {
		t.Fatalf("max inputs = %d", loaded.Config.Governance.MaxInputs)
	}
}

func TestLoadConfigRejectsPublicPermissionsAndMissingExplicitFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "权限") {
		t.Fatalf("LoadConfig with 0644 error = %v, want permission error", err)
	}

	if _, err := LoadConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("LoadConfig with missing explicit path succeeded")
	}
}

func TestLoadConfigRejectsUnknownKeysAndTrailingJSON(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unknown":                `{"unknown_field": true}`,
		"trailing":               `{} {}`,
		"removed decision task":  `{"model":{"decision":{"name":"legacy"}}}`,
		"removed process budget": `{"worker":{"process_budget":"600s"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			writeConfigFile(t, path, body)
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("LoadConfig succeeded, want error")
			}
		})
	}
}

func TestConfigValidateRejectsArchitectureViolations(t *testing.T) {
	base := DefaultConfig()
	base.DataDir = t.TempDir()
	base.Database.Path = filepath.Join(base.DataDir, "dmemd.db")

	cases := map[string]func(*Config){
		"non-loopback listen":         func(cfg *Config) { cfg.Listen = "0.0.0.0:18432" },
		"multiple workers":            func(cfg *Config) { cfg.Worker.Workers = 2 },
		"zero max attempts":           func(cfg *Config) { cfg.Worker.MaxAttempts = 0 },
		"negative extraction timeout": func(cfg *Config) { cfg.Worker.ExtractionTimeout = -1 },
		"zero governance timeout":     func(cfg *Config) { cfg.Worker.GovernanceTimeout = 0 },
		"zero aggregation timeout":    func(cfg *Config) { cfg.Worker.AggregationTimeout = 0 },
		"zero aggregation inputs":     func(cfg *Config) { cfg.Aggregation.MaxInputs = 0 },
		"zero l1 bucket":              func(cfg *Config) { cfg.Aggregation.L1BucketSize = 0 },
		"zero model timeout":          func(cfg *Config) { cfg.Model.Timeout = 0 },
		"bad model URL":               func(cfg *Config) { cfg.Model.BaseURL = "not-a-url" },
		"bad log level":               func(cfg *Config) { cfg.Log.Level = "verbose" },
		"no data directory":           func(cfg *Config) { cfg.DataDir = ""; cfg.Database.Path = "x" },
		"zero max inputs":             func(cfg *Config) { cfg.Governance.MaxInputs = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			cfg.Worker.RetryDelays = append([]Duration(nil), base.Worker.RetryDelays...)
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate succeeded, want error")
			}
		})
	}

	base.Listen = "localhost:18432"
	if err := base.Validate(); err != nil {
		t.Fatalf("localhost loopback rejected: %v", err)
	}
}

func TestEnsurePrivateDirAndFileModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode = %04o, want 0700", got)
	}

	path := filepath.Join(dir, "state.db")
	file, err := OpenPrivateFile(path, os.O_RDWR)
	if err != nil {
		t.Fatalf("OpenPrivateFile: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %04o, want 0600", got)
	}
	if err := CheckPrivateFile(path); err != nil {
		t.Fatalf("CheckPrivateFile: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateFile(path); err == nil {
		t.Fatal("CheckPrivateFile accepted 0644")
	}
}

func TestPrepareCreatesPrivateDirectories(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.Database.Path = filepath.Join(cfg.DataDir, "db", "dmemd.db")
	cfg.Listen = "127.0.0.1:0"
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	if err := Prepare(cfg); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	for _, path := range []string{cfg.DataDir, filepath.Dir(cfg.Database.Path), filepath.Join(dir, "config", "dmemd")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %04o", path, info.Mode().Perm())
		}
	}
}

func TestDurationJSON(t *testing.T) {
	var value Duration
	if err := value.UnmarshalJSON([]byte(`"1m30s"`)); err != nil {
		t.Fatal(err)
	}
	if value.Std() != 90*time.Second {
		t.Fatalf("duration = %v", value.Std())
	}
	if err := value.UnmarshalJSON([]byte(`60`)); err == nil {
		t.Fatal("numeric duration accepted")
	}
}

func writeConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestModelTaskOverridesAndRuntimeBounds(t *testing.T) {
	base := DefaultConfig()
	base.DataDir = t.TempDir()
	base.Database.Path = filepath.Join(base.DataDir, "dmemd.db")
	cfg := base
	cfg.Model.Extraction = &ModelTaskConfig{Name: "fast-extract", Timeout: Duration(30 * time.Second), ReasoningEffort: "low"}
	cfg.Model.Governance = &ModelTaskConfig{Name: "governance", Timeout: Duration(90 * time.Second), ReasoningEffort: "high"}
	cfg.Model.Aggregation = &ModelTaskConfig{Name: "aggregation", ReasoningEffort: "minimal"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Model.TaskName(cfg.Model.Extraction); got != "fast-extract" {
		t.Fatalf("extraction task name = %q", got)
	}
	if got := cfg.Model.TaskTimeout(cfg.Model.Governance, time.Minute); got != 90*time.Second {
		t.Fatalf("governance task timeout = %v", got)
	}
	if got := cfg.Model.TaskReasoning(cfg.Model.Aggregation); got != "minimal" {
		t.Fatalf("aggregation reasoning = %q", got)
	}
	if base.Worker.ExtractionChunkRunes != DefaultExtractionChunkRunes {
		t.Fatalf("chunk runes = %d", base.Worker.ExtractionChunkRunes)
	}
	if base.Governance.MaxWait.Std() != DefaultGovernanceMaxWait {
		t.Fatalf("governance max wait = %v", base.Governance.MaxWait)
	}
	invalid := base
	invalid.Model.Extraction = &ModelTaskConfig{ReasoningEffort: "unbounded"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid reasoning effort accepted")
	}
	invalid = base
	invalid.Worker.ExtractionChunkRunes = -1
	if err := invalid.Validate(); err == nil {
		t.Fatal("negative chunk size accepted")
	}
}
