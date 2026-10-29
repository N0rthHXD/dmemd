package testutil

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// FakeResponse describes one scripted model reply.
type FakeResponse struct {
	Status  int
	Body    string
	Content string
	Delay   time.Duration
	Block   bool
}

// RecordedRequest is a captured model request, including its raw JSON body so a
// test can assert which source and related memories were sent.
type RecordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// FakeModel is a controllable OpenAI-compatible Chat Completions server. Tests
// can script success, failure, malformed payloads, delay and a block that starts
// after the request has been received and ends on Release.
type FakeModel struct {
	server    *httptest.Server
	mu        sync.Mutex
	responses []FakeResponse
	requests  []RecordedRequest
	started   chan struct{}
	gate      chan struct{}
	closed    bool
	closeOnce sync.Once
}

// NewFakeModel starts a local fake model and closes it during test cleanup.
func NewFakeModel(t testing.TB) *FakeModel {
	t.Helper()
	model := &FakeModel{
		started: make(chan struct{}, 32),
		gate:    make(chan struct{}),
	}
	model.server = httptest.NewServer(http.HandlerFunc(model.handle))
	t.Cleanup(model.Close)
	return model
}

// URL returns the fake model base URL.
func (f *FakeModel) URL() string { return f.server.URL }

// Close stops the server and releases any blocked handler.
func (f *FakeModel) Close() {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		close(f.gate)
		f.mu.Unlock()
		f.server.Close()
	})
}

// Enqueue appends one scripted response. When the queue is empty the fake model
// returns a successful completion with zero candidates.
func (f *FakeModel) Enqueue(response FakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, response)
}

// EnqueueContent wraps content as choices[0].message.content.
func (f *FakeModel) EnqueueContent(content string) {
	f.Enqueue(FakeResponse{Status: http.StatusOK, Content: content})
}

// EnqueueExtraction encodes the minimal extraction protocol from a draft list.
// It keeps only kind and content and uses the first source span as the
// model-selected reference; evidence, IDs and titles are never part of the
// model protocol.
func (f *FakeModel) EnqueueExtraction(drafts any) {
	items := make([]any, 0)
	if list, ok := drafts.([]any); ok {
		for _, raw := range list {
			object, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			item := map[string]any{"span_ids": []string{"seg-0001"}}
			if kind, ok := object["kind"]; ok {
				item["kind"] = kind
			}
			if content, ok := object["content"]; ok {
				item["content"] = content
			}
			items = append(items, item)
		}
	}
	data, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		panic("testutil: encode fake extraction items: " + err.Error())
	}
	f.EnqueueContent(string(data))
}

// EnqueueRaw appends a raw response body, which is useful for malformed model
// envelopes or protocol error examples.
func (f *FakeModel) EnqueueRaw(status int, body string) {
	f.Enqueue(FakeResponse{Status: status, Body: body})
}

// EnqueueStatus appends an HTTP error response with a generic JSON error body.
func (f *FakeModel) EnqueueStatus(status int) {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]string{"message": "scripted failure", "type": "test_error"},
	})
	f.EnqueueRaw(status, string(body))
}

// EnqueueMalformed appends a successful HTTP response whose assistant content
// is natural language rather than a JSON object.
func (f *FakeModel) EnqueueMalformed() {
	f.EnqueueContent("I could not determine any memories.")
}

// WaitStarted waits until a blocking response has received its request.
func (f *FakeModel) WaitStarted(ctx context.Context) error {
	select {
	case <-f.started:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release finishes every currently blocked response and enables the next block.
func (f *FakeModel) Release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	close(f.gate)
	f.gate = make(chan struct{})
}

// Requests returns a copy of captured requests in arrival order.
func (f *FakeModel) Requests() []RecordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := make([]RecordedRequest, len(f.requests))
	for i, item := range f.requests {
		items[i] = RecordedRequest{
			Method: item.Method,
			Path:   item.Path,
			Header: item.Header.Clone(),
			Body:   append([]byte(nil), item.Body...),
		}
	}
	return items
}

// RequestCount returns the number of captured requests.
func (f *FakeModel) RequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// LastRequest returns the most recent captured request.
func (f *FakeModel) LastRequest() (RecordedRequest, bool) {
	items := f.Requests()
	if len(items) == 0 {
		return RecordedRequest{}, false
	}
	return items[len(items)-1], true
}

func (f *FakeModel) handle(w http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	_ = request.Body.Close()

	f.mu.Lock()
	f.requests = append(f.requests, RecordedRequest{
		Method: request.Method,
		Path:   request.URL.Path,
		Header: request.Header.Clone(),
		Body:   body,
	})
	response := FakeResponse{Status: http.StatusOK}
	if len(f.responses) > 0 {
		response = f.responses[0]
		f.responses = f.responses[1:]
	}
	gate := f.gate
	f.mu.Unlock()

	if response.Delay > 0 {
		timer := time.NewTimer(response.Delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-request.Context().Done():
			return
		}
	}
	if response.Block {
		select {
		case f.started <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-request.Context().Done():
			return
		}
	}

	if response.Status == 0 {
		response.Status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.Status)
	if response.Body != "" {
		_, _ = io.WriteString(w, response.Body)
		return
	}
	content := response.Content
	if content == "" {
		content = `{"items":[]}`
	}
	_, _ = io.WriteString(w, completionEnvelope(content))
}

func completionEnvelope(content string) string {
	body, err := json.Marshal(map[string]any{
		"id":     "chatcmpl-test",
		"object": "chat.completion",
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": content},
				"finish_reason": "stop",
			},
		},
	})
	if err != nil {
		panic("testutil: encode fake completion: " + err.Error())
	}
	return string(body)
}
