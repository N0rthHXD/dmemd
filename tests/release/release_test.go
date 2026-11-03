// Package release_test verifies the actual binaries produced by make release.
// Normal Go tests skip it; release.sh supplies an artifact directory and version.
package release_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReleaseArtifacts(t *testing.T) {
	dir := os.Getenv("DMEMD_RELEASE_DIR")
	if dir == "" {
		t.Skip("run make release VERSION=1.2.3 to verify release binaries")
	}
	version := os.Getenv("DMEMD_RELEASE_VERSION")
	if version == "" {
		t.Fatal("DMEMD_RELEASE_VERSION is required")
	}
	wantSums := make([]string, 0, 2)
	wantFiles := map[string]bool{"SHA256SUMS": true}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "dmemd_" + version + "_linux_" + arch
		wantFiles[name] = true
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode = %s, want regular executable 0755", name, info.Mode())
		}
		binary, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(binary)
		wantSums = append(wantSums, hex.EncodeToString(digest[:])+"  "+name+"\n")
		t.Run(arch, func(t *testing.T) {
			verifyReleaseBinary(t, dir, version, arch)
		})
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sums) != strings.Join(wantSums, "") {
		t.Fatalf("SHA256SUMS = %q, want %q", sums, strings.Join(wantSums, ""))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !wantFiles[entry.Name()] {
			t.Fatalf("unexpected release artifact: %s", entry.Name())
		}
		delete(wantFiles, entry.Name())
	}
	if len(wantFiles) != 0 {
		t.Fatalf("release is missing artifacts: %v", wantFiles)
	}
}

func verifyReleaseBinary(t *testing.T, dir, version, arch string) {
	t.Helper()
	binary := filepath.Join(dir, "dmemd_"+version+"_linux_"+arch)
	executable, err := elf.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	wantMachine := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
	}[arch]
	if executable.Machine != wantMachine {
		t.Fatalf("ELF architecture = %s, want %s", executable.Machine, wantMachine)
	}
	for _, program := range executable.Progs {
		if program.Type == elf.PT_INTERP || program.Type == elf.PT_DYNAMIC {
			t.Fatal("release binary must not depend on a dynamic loader or shared libraries")
		}
	}

	// Keep config, database and logs away from the caller's XDG directories.
	private := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(private, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(private, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(private, "cache"))
	if runtime.GOOS != "linux" || runtime.GOARCH != arch {
		t.Logf("binary verified; skipping execution on %s/%s host", runtime.GOOS, runtime.GOARCH)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil || string(output) != "dmemd "+version+"\n" {
		t.Fatalf("--version: %v, output=%q", err, output)
	}
	verifyService(t, binary, private)
}

func verifyService(t *testing.T, binary, private string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{
		"listen":      address,
		"data_dir":    filepath.Join(private, "service"),
		"governance":  map[string]any{"enabled": false},
		"aggregation": map[string]any{"enabled": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(private, "smoke.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	command := exec.Command(binary, "--config", configPath)
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = command.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Logf("release process logs:\n%s", logs.String())
		}
	})
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	baseURL := "http://" + address
	ready := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("service exited before ready: %v", err)
		default:
		}
		response, err := client.Get(baseURL + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("release service did not become ready")
	}
	get := func(path, contentType string) string {
		t.Helper()
		response, err := client.Get(baseURL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), contentType) {
			t.Fatalf("GET %s: status=%d content-type=%s", path, response.StatusCode, response.Header.Get("Content-Type"))
		}
		return string(body)
	}
	get("/healthz", "application/json")
	index := get("/ui/", "text/html")
	if !strings.Contains(index, `id="root"`) {
		t.Fatal("embedded UI index is missing the root element")
	}
	if nested := get("/ui/memories", "text/html"); nested != index {
		t.Fatal("UI deep link did not return the SPA index")
	}
	asset := regexp.MustCompile(`src="(/ui/assets/[^"<>]+\.js)"`).FindStringSubmatch(index)
	if len(asset) != 2 {
		t.Fatal("embedded UI index is missing its JavaScript asset")
	}
	if body := get(asset[1], "javascript"); len(body) == 0 {
		t.Fatal("embedded JavaScript is empty")
	}
	get("/v1/memories?limit=1", "application/json")
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatalf("release service shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("release service did not shut down on SIGTERM")
	}
}
