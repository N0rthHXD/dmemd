package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersionExitsWithoutConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run --version: %v", err)
	}
	if !strings.Contains(stdout.String(), "dmemd") {
		t.Fatalf("stdout = %q", stdout.String())
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

func TestRunRejectsMissingExplicitConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"--check-config", "--config", filepath.Join(t.TempDir(), "missing.json")}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run accepted missing explicit config")
	}
}
