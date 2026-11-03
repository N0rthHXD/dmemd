package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersionExitsWithoutConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run --version: %v", err)
	}
	if !strings.Contains(stdout.String(), "dmemd") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(configHome, "dmemd", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("--version must not create a config file: %v", err)
	}
}

func TestRunCheckConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"listen":"127.0.0.1:0","data_dir":"` + filepath.Join(dir, "data") + `"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"--check-config", "--config", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run --check-config: %v (stderr %s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "configuration valid") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunCreatesDefaultConfigAndExplainsHowToEdit(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--check-config"}, &stdout, &stderr); err != nil {
		t.Fatalf("run --check-config: %v (stderr %s)", err, stderr.String())
	}
	configPath := filepath.Join(configHome, "dmemd", "config.json")
	if !strings.Contains(stderr.String(), configPath) || !strings.Contains(stderr.String(), "model.base_url") {
		t.Fatalf("stderr does not explain the generated config: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "configuration valid: "+configPath) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunRejectsMissingExplicitConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	missingPath := filepath.Join(t.TempDir(), "missing.json")
	var stdout, stderr bytes.Buffer
	err := run([]string{"--check-config", "--config", missingPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run accepted missing explicit config")
	}
	for _, path := range []string{missingPath, filepath.Join(configHome, "dmemd", "config.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("missing explicit config must not create %s: %v", path, err)
		}
	}
}

func TestArtifactIntegrityCheckExitsWithoutStartingServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"data_dir":"`+filepath.Join(dir, "data")+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--artifact-integrity-check", "--config", path}, &stdout, &stderr); err != nil {
		t.Fatalf("check: %v; %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"checked":0,"issues":[]`) {
		t.Fatalf("report = %s", stdout.String())
	}
	logPath := filepath.Join(dir, "cache", "dmemd.log")
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read default log file: %v", err)
	}
	if !strings.Contains(string(logged), "starting dmemd") || strings.Contains(string(logged), `"level":`) {
		t.Fatalf("default log file is not plain text: %s", logged)
	}
}
