package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type process struct {
	cmd    *exec.Cmd
	done   chan struct{}
	mu     sync.Mutex
	err    error
	stdout string
	stderr string
	logDir string
}

func startProcess(t *testing.T, binary, configPath string) *process {
	t.Helper()
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.log")
	stderrPath := filepath.Join(dir, "stderr.log")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(stderrPath)
	if err != nil {
		_ = stdout.Close()
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--config", configPath)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		t.Fatal(err)
	}
	_ = stdout.Close()
	_ = stderr.Close()

	result := &process{
		cmd:    cmd,
		done:   make(chan struct{}),
		stdout: stdoutPath,
		stderr: stderrPath,
		logDir: dir,
	}
	go func() {
		waitErr := cmd.Wait()
		result.mu.Lock()
		result.err = waitErr
		result.mu.Unlock()
		close(result.done)
	}()
	t.Cleanup(func() {
		select {
		case <-result.done:
		default:
			_ = cmd.Process.Kill()
			<-result.done
		}
	})
	return result
}

func (p *process) wait(timeout time.Duration) error {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	case <-time.After(timeout):
		return fmt.Errorf("process did not exit within %s", timeout)
	}
}

func (p *process) output() string {
	var builder strings.Builder
	for _, path := range []string{p.stdout, p.stderr} {
		data, err := os.ReadFile(path)
		if err == nil {
			builder.Write(data)
		}
	}
	return builder.String()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func buildBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	binary := filepath.Join(t.TempDir(), "dmemd")
	goBinary := filepath.Join(goruntime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBinary); err != nil {
		goBinary = "go"
	}
	command := exec.Command(goBinary, "build", "-trimpath", "-o", binary, "./cmd/dmemd")
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binary
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func writeConfig(t *testing.T, path, dataDir, databasePath, listen, modelURL string) {
	t.Helper()
	config := map[string]any{
		"listen":   listen,
		"data_dir": dataDir,
		"database": map[string]any{
			"path":                 databasePath,
			"busy_timeout":         "5s",
			"max_read_connections": 4,
		},
		"model": map[string]any{
			"name":     "fake-model",
			"base_url": modelURL,
			"api_key":  "",
			"timeout":  "5s",
		},
		"worker": map[string]any{
			"workers":      1,
			"max_attempts": 3,
			"retry_delays": []string{"10ms", "20ms"},
		},
		"log": map[string]any{"level": "info"},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitReady(t *testing.T, baseURL string, process *process) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-process.done:
			t.Fatalf("process exited before ready:\n%s", process.output())
		default:
		}
		response, err := client.Get(baseURL + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service did not become ready:\n%s", process.output())
}

func doJSON(t *testing.T, client *http.Client, method, url, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &object); err != nil {
			t.Fatalf("%s %s returned non-JSON: %v (%s)", method, url, err, data)
		}
	}
	return response.StatusCode, object
}

func waitJob(t *testing.T, client *http.Client, baseURL, jobID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, object := doJSON(t, client, http.MethodGet, baseURL+"/v1/ingest-jobs/"+jobID, "")
		if status != http.StatusOK {
			t.Fatalf("job query status = %d body=%#v", status, object)
		}
		switch object["status"] {
		case "succeeded", "failed", "cancelled":
			return object
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return nil
}

func TestP6ProcessSingleInstanceShutdownAndRestart(t *testing.T) {
	binary := buildBinary(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	databasePath := filepath.Join(dataDir, "dmemd.db")
	model := testutil.NewFakeModel(t)
	model.Enqueue(testutil.FakeResponse{
		Status:  http.StatusOK,
		Block:   true,
		Content: `{"candidates":[{"kind":"fact","title":"重启恢复","content":"重启后仍会处理这条记忆。","evidence":[{"quote":"进程重启后需要恢复任务。"}]}]}`,
	})

	listenOne := freeAddress(t)
	configOne := filepath.Join(root, "config-one.json")
	writeConfig(t, configOne, dataDir, databasePath, listenOne, model.URL())
	listenTwo := freeAddress(t)
	configTwo := filepath.Join(root, "config-two.json")
	writeConfig(t, configTwo, dataDir, databasePath, listenTwo, model.URL())

	first := startProcess(t, binary, configOne)
	waitReady(t, "http://"+listenOne, first)

	// A second process with a different port but the same data directory must
	// fail on the data-directory lock, not on a TCP port conflict.
	second := exec.CommandContext(context.Background(), binary, "--config", configTwo)
	output, err := second.CombinedOutput()
	if err == nil {
		t.Fatalf("second instance unexpectedly started:\n%s", output)
	}
	if !strings.Contains(string(output), "实例") {
		t.Fatalf("second instance failed without the instance-lock message:\n%s", output)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	started := time.Now()
	status, receipt := doJSON(t, client, http.MethodPost, "http://"+listenOne+"/v1/memories",
		`{"content":"进程重启后需要恢复任务。"}`)
	if status != http.StatusAccepted {
		t.Fatalf("submit status = %d body=%#v", status, receipt)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("submit waited for the blocked model: %s", time.Since(started))
	}
	jobID, _ := receipt["job_id"].(string)
	sourceID, _ := receipt["source_id"].(string)
	if jobID == "" || sourceID == "" {
		t.Fatalf("receipt = %#v", receipt)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := model.WaitStarted(waitCtx); err != nil {
		t.Fatalf("model call did not start: %v", err)
	}
	if err := first.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := first.wait(10 * time.Second); err != nil {
		t.Fatalf("first process did not stop cleanly: %v\n%s", err, first.output())
	}

	// Make the second attempt succeed after the graceful shutdown requeued the
	// running job without resetting its attempt count.
	model.Enqueue(testutil.FakeResponse{
		Status:  http.StatusOK,
		Content: `{"candidates":[{"kind":"fact","title":"重启恢复","content":"重启后仍会处理这条记忆。","evidence":[{"quote":"进程重启后需要恢复任务。"}]}]}`,
	})
	restarted := startProcess(t, binary, configOne)
	portURL := "http://" + listenOne
	waitReady(t, portURL, restarted)

	job := waitJob(t, client, portURL, jobID)
	if job["status"] != "succeeded" {
		t.Fatalf("recovered job = %#v", job)
	}
	memoryIDs, ok := job["memory_ids"].([]any)
	if !ok || len(memoryIDs) != 1 {
		t.Fatalf("recovered job memory_ids = %#v", job["memory_ids"])
	}
	if job["attempts"].(float64) < 2 {
		t.Fatalf("attempt count was reset: %#v", job)
	}
	status, memory := doJSON(t, client, http.MethodGet, portURL+"/v1/memories/"+memoryIDs[0].(string), "")
	if status != http.StatusOK || memory["content"] != "重启后仍会处理这条记忆。" {
		t.Fatalf("memory after restart = %d %#v", status, memory)
	}
	status, source := doJSON(t, client, http.MethodGet, portURL+"/v1/sources/"+sourceID, "")
	if status != http.StatusOK || source["redacted"] != false {
		t.Fatalf("source after restart = %d %#v", status, source)
	}

	// A killed process must not leave an unrecoverable lock: the OS releases the
	// advisory lock and the same data directory can start again.
	if err := restarted.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = restarted.wait(10 * time.Second)
	crashedRestart := startProcess(t, binary, configOne)
	waitReady(t, portURL, crashedRestart)
	status, memory = doJSON(t, client, http.MethodGet, portURL+"/v1/memories/"+memoryIDs[0].(string), "")
	if status != http.StatusOK || memory["content"] != "重启后仍会处理这条记忆。" {
		t.Fatalf("memory after crash restart = %d %#v", status, memory)
	}
	if err := crashedRestart.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := crashedRestart.wait(10 * time.Second); err != nil {
		t.Fatalf("process after crash restart did not stop cleanly: %v\n%s", err, crashedRestart.output())
	}
	if bytes.Contains([]byte(first.output()), []byte("进程重启后需要恢复任务")) {
		t.Fatal("original source content leaked into the process log")
	}
}
