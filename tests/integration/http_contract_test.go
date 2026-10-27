package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appruntime "github.com/N0rthHXD/dmemd/internal/runtime"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type apiClient struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newAPIClient(t *testing.T, base string) *apiClient {
	t.Helper()
	return &apiClient{t: t, base: base, client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *apiClient) do(method, path, body string, headers map[string]string) (*http.Response, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		c.t.Fatalf("%s %s read: %v", method, path, err)
	}
	return response, data
}

func (c *apiClient) expect(method, path, body string, headers map[string]string, status int) map[string]any {
	c.t.Helper()
	response, data := c.do(method, path, body, headers)
	if response.StatusCode != status {
		c.t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, response.StatusCode, status, data)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		c.t.Fatalf("%s %s Content-Type = %q", method, path, got)
	}
	if response.Header.Get("Location") != "" || response.Header.Get("ETag") != "" {
		c.t.Fatalf("%s %s returned extra location/version headers", method, path)
	}
	var object map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &object); err != nil {
			c.t.Fatalf("%s %s body is not JSON: %v (%s)", method, path, err, data)
		}
	}
	return object
}

func (c *apiClient) items(object map[string]any) []any {
	c.t.Helper()
	value, ok := object["items"].([]any)
	if !ok {
		c.t.Fatalf("response has no items array: %#v", object)
	}
	return value
}

func (c *apiClient) memories(object map[string]any) []any {
	c.t.Helper()
	value, ok := object["memories"].([]any)
	if !ok {
		c.t.Fatalf("response has no memories array: %#v", object)
	}
	return value
}

func waitForJob(t *testing.T, client *apiClient, jobID string) map[string]any {
	t.Helper()
	return waitForJobCondition(t, client, jobID, func(object map[string]any) bool {
		status, _ := object["status"].(string)
		return status == "succeeded" || status == "failed" || status == "cancelled"
	})
}

func waitForJobCondition(t *testing.T, client *apiClient, jobID string,
	done func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		object := client.expect(http.MethodGet, "/v1/ingest-jobs/"+jobID, "", nil, http.StatusOK)
		if done(object) {
			return object
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish: %#v", jobID, object)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newHTTPServer(t *testing.T, retryDelays []appruntime.Duration) (*httptest.Server, *testutil.FakeModel) {
	t.Helper()
	store := testutil.NewStore(t)
	fake := testutil.NewFakeModel(t)
	cfg := appruntime.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.Path = store.Path()
	cfg.Listen = "127.0.0.1:0"
	cfg.Model.Name = "test-model"
	cfg.Model.BaseURL = fake.URL()
	cfg.Model.Timeout = appruntime.Duration(5 * time.Second)
	if retryDelays != nil {
		cfg.Worker.RetryDelays = retryDelays
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stack, err := appruntime.NewStack(cfg, logger, store)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- stack.Worker.Run(workerCtx) }()
	t.Cleanup(func() {
		cancelWorker()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
	server := httptest.NewServer(stack.App.Handler())
	t.Cleanup(server.Close)
	return server, fake
}

func TestHTTPContract(t *testing.T) {
	server, fake := newHTTPServer(t, []appruntime.Duration{
		appruntime.Duration(0),
		appruntime.Duration(0),
		appruntime.Duration(time.Hour),
	})
	client := newAPIClient(t, server.URL)

	// Health is independent from the model and the business readiness probe.
	client.expect(http.MethodGet, "/healthz", "", nil, http.StatusOK)
	client.expect(http.MethodGet, "/readyz", "", nil, http.StatusOK)

	// The first model reply blocks until the test releases it. POST must still
	// return 202 immediately after the source and queued job commit.
	fake.Enqueue(testutil.FakeResponse{
		Status:  http.StatusOK,
		Block:   true,
		Content: `{"candidates":[{"action":"add","kind":"preference","title":"回答语言","content":"用户希望默认使用中文回答。","evidence":[{"quote":"以后请默认使用中文回答。"}]}]}`,
	})
	started := time.Now()
	receipt := client.expect(http.MethodPost, "/v1/ingestions",
		`{"app_id":"desktop-assistant","external_id":"conversation-42","content":{"messages":[{"role":"user","content":"以后请默认使用中文回答。"}]}}`,
		nil, http.StatusAccepted)
	if time.Since(started) > 2*time.Second {
		t.Fatalf("POST waited for the model: %s", time.Since(started))
	}
	sourceID, _ := receipt["source_id"].(string)
	jobID, _ := receipt["job_id"].(string)
	if sourceID == "" || jobID == "" || receipt["status"] != "queued" {
		t.Fatalf("receipt = %#v", receipt)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fake.WaitStarted(waitCtx); err != nil {
		t.Fatalf("model did not start: %v", err)
	}
	fake.Release()

	job := waitForJob(t, client, jobID)
	if job["status"] != "succeeded" || job["attempts"].(float64) != 1 {
		t.Fatalf("job = %#v", job)
	}
	if job["quality_outcome"] != "complete" {
		t.Fatalf("quality_outcome = %#v", job["quality_outcome"])
	}
	if convergence, ok := job["convergence"].([]any); !ok || len(convergence) != 0 {
		t.Fatalf("convergence = %#v", job["convergence"])
	}
	stats, ok := job["extraction_stats"].(map[string]any)
	if !ok || stats["raw_candidates"].(float64) != 1 || stats["accepted_candidates"].(float64) != 1 {
		t.Fatalf("extraction_stats = %#v", job["extraction_stats"])
	}
	memoryIDs, ok := job["memory_ids"].([]any)
	if !ok || len(memoryIDs) != 1 {
		t.Fatalf("memory_ids = %#v", job["memory_ids"])
	}
	memoryID := memoryIDs[0].(string)

	// No app_id and an arbitrary Authorization token are both accepted.
	second := client.expect(http.MethodPost, "/v1/ingestions", `{"content":"另一个独立事件。"}`,
		map[string]string{"Authorization": "Bearer arbitrary-local-token"}, http.StatusAccepted)
	if second["status"] != "queued" {
		t.Fatalf("second receipt = %#v", second)
	}
	secondJob := waitForJob(t, client, second["job_id"].(string))
	if secondJob["status"] != "succeeded" {
		t.Fatalf("second job = %#v", secondJob)
	}

	// Model failures are visible through the job query, not as a synchronous
	// HTTP error or a lost submission.
	for i := 0; i < 3; i++ {
		fake.EnqueueStatus(http.StatusInternalServerError)
	}
	failedReceipt := client.expect(http.MethodPost, "/v1/ingestions", `{"content":"模型失败时仍接收。"}`, nil, http.StatusAccepted)
	failedJob := waitForJobCondition(t, client, failedReceipt["job_id"].(string), func(object map[string]any) bool {
		status, _ := object["status"].(string)
		attempts, _ := object["attempts"].(float64)
		return status == "waiting" && attempts == 3
	})
	if failedJob["status"] != "waiting" || failedJob["wait_reason"] != "transient" {
		t.Fatalf("failed job = %#v", failedJob)
	}
	if quality, _ := failedJob["quality_outcome"].(string); quality != "" {
		t.Fatalf("transient failure reported business quality = %#v", failedJob)
	}
	if failedJob["stage"] != "extraction" || failedJob["reason_code"] != "transient" {
		t.Fatalf("waiting diagnostics = %#v", failedJob)
	}
	if message, _ := failedJob["error"].(string); !strings.Contains(message, "HTTP 500") {
		t.Fatalf("failed job error = %#v", failedJob["error"])
	}
	if ids, _ := failedJob["memory_ids"].([]any); len(ids) != 0 {
		t.Fatalf("failed job memory_ids = %#v", failedJob["memory_ids"])
	}

	memoryObject := client.expect(http.MethodGet, "/v1/memories/"+memoryID, "", nil, http.StatusOK)
	if memoryObject["id"] != memoryID || memoryObject["state"] != "active" || memoryObject["version"].(float64) != 1 {
		t.Fatalf("memory = %#v", memoryObject)
	}
	if memoryObject["content"] != "用户希望默认使用中文回答。" || memoryObject["created_by_app"] != "desktop-assistant" {
		t.Fatalf("memory fields = %#v", memoryObject)
	}
	if memoryObject["origin_source_id"] != sourceID {
		t.Fatalf("origin_source_id = %#v", memoryObject["origin_source_id"])
	}
	if evolution, ok := memoryObject["evolution"].([]any); !ok || len(evolution) != 0 {
		t.Fatalf("evolution = %#v", memoryObject["evolution"])
	}

	list := client.expect(http.MethodGet, "/v1/memories", "", nil, http.StatusOK)
	if len(client.items(list)) == 0 {
		t.Fatalf("default memory list is empty: %#v", list)
	}

	recall := client.expect(http.MethodPost, "/v1/memories/recall",
		`{"query":"中文","filters":{"kinds":["preference"],"states":["active"]},"top_k":20,"explain":true}`,
		nil, http.StatusOK)
	for _, partition := range []string{"memories", "summaries", "experiences", "skills", "conflicts", "warnings"} {
		if _, ok := recall[partition].([]any); !ok {
			t.Fatalf("recall partition %s missing: %#v", partition, recall)
		}
	}
	recallItems := client.memories(recall)
	if len(recallItems) != 1 {
		t.Fatalf("recall items = %#v", recallItems)
	}
	firstRecall := recallItems[0].(map[string]any)
	if firstRecall["memory"].(map[string]any)["id"] != memoryID {
		t.Fatalf("recall memory = %#v", firstRecall["memory"])
	}
	if _, ok := firstRecall["explanation"].(map[string]any); !ok {
		t.Fatalf("explanation missing: %#v", firstRecall)
	}
	if terms, _ := firstRecall["matched_terms"].([]any); len(terms) == 0 {
		t.Fatalf("matched_terms = %#v", firstRecall["matched_terms"])
	}

	// PATCH only changes state; a content field must not become a write path.
	patched := client.expect(http.MethodPatch, "/v1/memories/"+memoryID+"/state",
		`{"state":"archived","content":"不应成为新正文"}`, nil, http.StatusOK)
	if patched["state"] != "archived" || patched["content"] != "用户希望默认使用中文回答。" || patched["version"].(float64) != 2 {
		t.Fatalf("patched = %#v", patched)
	}
	archivedList := client.expect(http.MethodGet, "/v1/memories?states=archived", "", nil, http.StatusOK)
	foundArchived := false
	for _, item := range client.items(archivedList) {
		if item.(map[string]any)["id"] == memoryID {
			foundArchived = true
		}
	}
	if !foundArchived {
		t.Fatalf("archived memory missing: %#v", archivedList)
	}
	activeList := client.expect(http.MethodGet, "/v1/memories?states=active&created_by_app=desktop-assistant", "", nil, http.StatusOK)
	for _, item := range client.items(activeList) {
		if item.(map[string]any)["id"] == memoryID {
			t.Fatalf("archived memory incorrectly returned in active list")
		}
	}

	activated := client.expect(http.MethodPatch, "/v1/memories/"+memoryID+"/state", `{"state":"active"}`, nil, http.StatusOK)
	if activated["state"] != "active" || activated["version"].(float64) != 3 {
		t.Fatalf("activated = %#v", activated)
	}
	// deleted is not a PATCH value; DELETE is the only logical-delete path.
	ignoredDelete := client.expect(http.MethodPatch, "/v1/memories/"+memoryID+"/state", `{"state":"deleted"}`, nil, http.StatusOK)
	if ignoredDelete["state"] != "active" || ignoredDelete["version"].(float64) != 3 {
		t.Fatalf("PATCH deleted changed lifecycle: %#v", ignoredDelete)
	}

	versions := client.expect(http.MethodGet, "/v1/memories/"+memoryID+"/history", "", nil, http.StatusOK)
	versionItems := client.items(versions)
	if len(versionItems) < 3 {
		t.Fatalf("versions = %#v", versionItems)
	}
	firstVersion := versionItems[0].(map[string]any)
	if firstVersion["version"].(float64) != 3 || firstVersion["operation"] != "activate" {
		t.Fatalf("newest version = %#v", firstVersion)
	}
	if _, ok := firstVersion["evidence"].([]any); !ok {
		t.Fatalf("version evidence is not an array: %#v", firstVersion)
	}

	versionOne := client.expect(http.MethodGet, "/v1/memories/"+memoryID+"/history/1", "", nil, http.StatusOK)
	if versionOne["version"].(float64) != 1 || versionOne["operation"] != "create" {
		t.Fatalf("version one = %#v", versionOne)
	}
	if _, ok := versionOne["snapshot"].(map[string]any); !ok {
		t.Fatalf("version snapshot missing: %#v", versionOne)
	}

	client.expect(http.MethodPost, "/v1/memories/"+memoryID+"/history/999/restore", "", nil, http.StatusNotFound)
	client.expect(http.MethodGet, "/v1/memories/"+memoryID+"/history/999", "", nil, http.StatusNotFound)
	client.expect(http.MethodGet, "/v1/memories/"+memoryID+"/evidence?version=999", "", nil, http.StatusNotFound)

	restored := client.expect(http.MethodPost, "/v1/memories/"+memoryID+"/history/1/restore", "", nil, http.StatusOK)
	if restored["state"] != "active" || restored["version"].(float64) <= 3 || restored["content"] != "用户希望默认使用中文回答。" {
		t.Fatalf("restored = %#v", restored)
	}

	evidence := client.expect(http.MethodGet, "/v1/memories/"+memoryID+"/evidence", "", nil, http.StatusOK)
	evidenceItems := client.items(evidence)
	if len(evidenceItems) == 0 {
		t.Fatalf("evidence = %#v", evidence)
	}
	firstEvidence := evidenceItems[0].(map[string]any)
	if firstEvidence["quote"] == "" || firstEvidence["redacted"] != false {
		t.Fatalf("evidence fields = %#v", firstEvidence)
	}
	location, ok := firstEvidence["location"].(map[string]any)
	if !ok || location["field"] != "messages.0.content" {
		t.Fatalf("server-side location = %#v", firstEvidence)
	}

	source := client.expect(http.MethodGet, "/v1/sources/"+sourceID, "", nil, http.StatusOK)
	if source["redacted"] != false || source["app_id"] != "desktop-assistant" || source["external_id"] != "conversation-42" {
		t.Fatalf("source = %#v", source)
	}
	content, ok := source["content"].(map[string]any)
	if !ok || content["messages"] == nil {
		t.Fatalf("source content shape = %#v", source["content"])
	}

	redacted := client.expect(http.MethodDelete, "/v1/sources/"+sourceID, "", nil, http.StatusOK)
	if redacted["id"] != sourceID || redacted["redacted"] != true {
		t.Fatalf("redact = %#v", redacted)
	}
	redactedSource := client.expect(http.MethodGet, "/v1/sources/"+sourceID, "", nil, http.StatusOK)
	if redactedSource["redacted"] != true {
		t.Fatalf("redacted source = %#v", redactedSource)
	}
	if _, present := redactedSource["content"]; present {
		t.Fatalf("redacted source leaked content: %#v", redactedSource)
	}
	redactedEvidence := client.expect(http.MethodGet, "/v1/memories/"+memoryID+"/evidence", "", nil, http.StatusOK)
	firstRedacted := client.items(redactedEvidence)[0].(map[string]any)
	if firstRedacted["redacted"] != true {
		t.Fatalf("redacted evidence = %#v", firstRedacted)
	}
	if _, present := firstRedacted["quote"]; present {
		t.Fatalf("redacted evidence leaked quote: %#v", firstRedacted)
	}
	keptMemory := client.expect(http.MethodGet, "/v1/memories/"+memoryID, "", nil, http.StatusOK)
	if keptMemory["content"] != "用户希望默认使用中文回答。" {
		t.Fatalf("source redaction changed memory text: %#v", keptMemory)
	}

	deleted := client.expect(http.MethodDelete, "/v1/memories/"+memoryID, "", nil, http.StatusOK)
	if deleted["id"] != memoryID || deleted["state"] != "deleted" {
		t.Fatalf("delete = %#v", deleted)
	}
	tombstone := client.expect(http.MethodGet, "/v1/memories/"+memoryID, "", nil, http.StatusOK)
	if tombstone["state"] != "deleted" {
		t.Fatalf("tombstone = %#v", tombstone)
	}
	emptyRecall := client.expect(http.MethodPost, "/v1/memories/recall", `{"query":"中文"}`, nil, http.StatusOK)
	if len(client.memories(emptyRecall)) != 0 {
		t.Fatalf("deleted memory returned by recall: %#v", emptyRecall)
	}
	special := client.expect(http.MethodPost, "/v1/memories/recall", `{"query":"\"*( OR *"}`, nil, http.StatusOK)
	if len(client.memories(special)) != 0 {
		t.Fatalf("FTS syntax input returned items: %#v", special)
	}

	restoredAgain := client.expect(http.MethodPost, "/v1/memories/"+memoryID+"/history/1/restore", "", nil, http.StatusOK)
	if restoredAgain["state"] != "active" {
		t.Fatalf("restore after delete = %#v", restoredAgain)
	}

	purged := client.expect(http.MethodPost, "/v1/memories/"+memoryID+"/purge", "", nil, http.StatusOK)
	if purged["purged"] != true || purged["id"] != memoryID {
		t.Fatalf("purge = %#v", purged)
	}
	client.expect(http.MethodGet, "/v1/memories/"+memoryID, "", nil, http.StatusNotFound)
	sharedSource := client.expect(http.MethodGet, "/v1/sources/"+sourceID, "", nil, http.StatusOK)
	if sharedSource["redacted"] != true {
		t.Fatalf("shared source was removed by purge: %#v", sharedSource)
	}

	client.expect(http.MethodGet, "/v1/ingest-jobs/00000000-0000-4000-8000-000000000000", "", nil, http.StatusNotFound)
	client.expect(http.MethodGet, "/v1/memories/00000000-0000-4000-8000-000000000000", "", nil, http.StatusNotFound)
	client.expect(http.MethodPost, "/v1/ingestions", "{", nil, http.StatusBadRequest)

	// Unknown paths return the same simple JSON 404 rather than an HTML body.
	unknown := client.expect(http.MethodGet, "/v1/unknown", "", nil, http.StatusNotFound)
	if unknown["error"] != "资源不存在。" {
		t.Fatalf("unknown path body = %#v", unknown)
	}

}

func TestSourceRedactionCancelsRunningJob(t *testing.T) {
	server, fake := newHTTPServer(t, nil)
	client := newAPIClient(t, server.URL)
	fake.Enqueue(testutil.FakeResponse{
		Status:  http.StatusOK,
		Block:   true,
		Content: `{"candidates":[{"content":"不应在来源清除后落库。"}]}`,
	})

	receipt := client.expect(http.MethodPost, "/v1/ingestions", `{"content":"等待模型时清除来源。"}`, nil, http.StatusAccepted)
	sourceID := receipt["source_id"].(string)
	jobID := receipt["job_id"].(string)
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fake.WaitStarted(waitCtx); err != nil {
		t.Fatalf("model did not start: %v", err)
	}
	redacted := client.expect(http.MethodDelete, "/v1/sources/"+sourceID, "", nil, http.StatusOK)
	if redacted["redacted"] != true || redacted["cancelled_jobs"].(float64) != 1 {
		t.Fatalf("redaction = %#v", redacted)
	}
	fake.Release()
	job := waitForJob(t, client, jobID)
	if job["status"] != "cancelled" {
		t.Fatalf("job = %#v", job)
	}
	if ids, _ := job["memory_ids"].([]any); len(ids) != 0 {
		t.Fatalf("cancelled job stored memories: %#v", job["memory_ids"])
	}
	list := client.expect(http.MethodGet, "/v1/memories", "", nil, http.StatusOK)
	if len(client.items(list)) != 0 {
		t.Fatalf("redacted job produced memories: %#v", list)
	}
	source := client.expect(http.MethodGet, "/v1/sources/"+sourceID, "", nil, http.StatusOK)
	if source["redacted"] != true {
		t.Fatalf("source = %#v", source)
	}
	if _, present := source["content"]; present {
		t.Fatalf("source leaked content after redaction: %#v", source)
	}
}
