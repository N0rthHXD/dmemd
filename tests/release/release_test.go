// Package release_test verifies the actual archive produced by make release.
// Normal Go tests skip it; release.sh supplies an archive directory and version.
package release_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReleaseArchive(t *testing.T) {
	dir := os.Getenv("DMEMD_RELEASE_DIR")
	if dir == "" {
		t.Skip("run make release VERSION=1.2.3 to verify a release archive")
	}
	version := os.Getenv("DMEMD_RELEASE_VERSION")
	if version == "" {
		t.Fatal("DMEMD_RELEASE_VERSION is required")
	}
	name := "dmemd_" + version + "_linux_amd64"
	archive, err := os.ReadFile(filepath.Join(dir, name+".tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	wantSum := hex.EncodeToString(digest[:]) + "  " + name + ".tar.gz\n"
	if string(sums) != wantSum {
		t.Fatalf("SHA256SUMS = %q, want %q", sums, wantSum)
	}

	extracted := t.TempDir()
	gunzip, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gunzip.Close()
	reader := tar.NewReader(gunzip)
	// Verify an exact allowlist, including permissions; reject extra files and links.
	want := map[string]int64{
		name + "/":                           0o755,
		name + "/dmemd":                      0o755,
		name + "/README.md":                  0o644,
		name + "/LICENSE":                    0o644,
		name + "/configs/":                   0o755,
		name + "/configs/dmemd.example.json": 0o644,
	}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		mode, ok := want[header.Name]
		if !ok {
			t.Fatalf("unexpected or duplicate archive member: %s", header.Name)
		}
		delete(want, header.Name)
		if header.Mode != mode || header.Uid != 0 || header.Gid != 0 {
			t.Fatalf("unexpected metadata for %s: mode=%o uid=%d gid=%d", header.Name, header.Mode, header.Uid, header.Gid)
		}
		destination := filepath.Join(extracted, filepath.FromSlash(header.Name))
		switch header.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(destination, os.FileMode(mode))
		case tar.TypeReg:
			var file *os.File
			file, err = os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(mode))
			if err == nil {
				_, err = io.Copy(file, reader)
				closeErr := file.Close()
				if err == nil {
					err = closeErr
				}
			}
		default:
			t.Fatalf("unexpected entry type for %s: %d", header.Name, header.Typeflag)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(want) != 0 {
		t.Fatalf("archive is missing entries: %v", want)
	}
	// Read through the gzip trailer to verify its checksum as well.
	if _, err := io.Copy(io.Discard, gunzip); err != nil {
		t.Fatal(err)
	}

	payload := filepath.Join(extracted, name)
	binary := filepath.Join(payload, "dmemd")
	executable, err := elf.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	if executable.Machine != elf.EM_X86_64 {
		t.Fatalf("ELF architecture = %s, want x86-64", executable.Machine)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil || string(output) != "dmemd "+version+"\n" {
		t.Fatalf("--version: %v, output=%q", err, output)
	}
	example, err := os.ReadFile(filepath.Join(payload, "configs", "dmemd.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(private, "example.json")
	if err := os.WriteFile(configPath, example, 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(ctx, binary, "--config", configPath, "--check-config").CombinedOutput(); err != nil {
		t.Fatalf("example config rejected: %v\n%s", err, output)
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
