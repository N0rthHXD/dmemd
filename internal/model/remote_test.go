package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

type deadlineTransport func(*http.Request) (*http.Response, error)

func (f deadlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestProviderOperationTimeoutsOverrideGenericTimeout(t *testing.T) {
	var expected time.Duration
	var reply string
	provider, err := NewProvider(Config{
		BaseURL: "http://model.invalid/v1", Timeout: 10 * time.Millisecond,
		ExtractionTimeout: 180 * time.Second, DecisionTimeout: 300 * time.Second,
		HTTPClient: &http.Client{Transport: deadlineTransport(func(request *http.Request) (*http.Response, error) {
			deadline, ok := request.Context().Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining < expected-time.Second || remaining > expected {
				t.Errorf("remaining=%v expected=%v", remaining, expected)
			}
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected, reply = 180*time.Second, `{"candidates":[]}`
	if _, err := provider.Extract(context.Background(), ingest.ExtractionRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RepairExtraction(context.Background(), ingest.ExtractionRepairRequest{}); err != nil {
		t.Fatal(err)
	}
	expected, reply = 300*time.Second, `{"decisions":[]}`
	if _, err := provider.Decide(context.Background(), ingest.DecisionRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RepairDecision(context.Background(), ingest.DecisionRepairRequest{}); err != nil {
		t.Fatal(err)
	}
	expected, reply = time.Minute, `{"decisions":[]}`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := provider.Decide(ctx, ingest.DecisionRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionRepairRequestIncludesTargetedDiagnostics(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"decisions":[{"candidate_id":"c2","action":"unresolved"}]}`)
	provider, err := NewProvider(Config{BaseURL: fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.RepairDecision(context.Background(), ingest.DecisionRepairRequest{
		DecisionRequest: ingest.DecisionRequest{Candidates: []ingest.Candidate{{CandidateID: "c2"}}},
		Decisions:       []ingest.Decision{{CandidateID: "c2", Action: ingest.ActionChange, TargetMemoryIDs: []string{"a", "b"}}},
		Issues:          []ingest.DecisionIssue{{CandidateID: "c2", Code: "change_target_cardinality"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(fake.Requests()[0].Body)
	if !strings.Contains(body, "change_target_cardinality") || !strings.Contains(body, "invalid_decisions") || !strings.Contains(body, "never guess a target") {
		t.Fatalf("missing repair constraints: %s", body)
	}
}

func TestProviderMapsRequestsAndResponses(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"candidates":[{"kind":"preference","title":"回答语言","content":"默认中文","observed_at":"2026-10-20T02:00:00Z","evidence":[{"message_id":"m1","quote":"默认中文"}]}]}`)
	fake.EnqueueContent(`{"decisions":[{"candidate_id":"c1","action":"correct","target_memory_ids":["memory-1"],"content":"默认中文","evidence_refs":[{"candidate_id":"c1","evidence_index":0}]}]}`)
	provider, err := NewProvider(Config{Name: "test-model", BaseURL: fake.URL() + "/v1", APIKey: "secret-key", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	src := source.Source{
		ID: "source-1", AppID: "app-1", ExternalID: "external-1",
		Content:   json.RawMessage(`{"messages":[{"id":"m1","content":"默认中文","occurred_at":"2026-10-20T02:00:00Z"}]}`),
		CreatedAt: time.Date(2026, 10, 20, 2, 0, 1, 0, time.UTC),
	}
	result, err := provider.Extract(context.Background(), ingest.ExtractionRequest{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Content != "默认中文" ||
		result.Candidates[0].Kind != memory.KindPreference {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Candidates[0].Evidence) != 1 || result.Candidates[0].Evidence[0].Quote != "默认中文" {
		t.Fatalf("evidence = %+v", result.Candidates[0].Evidence)
	}

	related := []memory.Memory{{
		ID: "memory-1", Kind: memory.KindPreference, Title: "回答语言", Content: "用户希望中文。",
		Version: 2, AssertionStatus: memory.AssertionAccepted,
		ObservedAt: time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 10, 20, 1, 30, 0, 0, time.UTC),
	}}
	decision, err := provider.Decide(context.Background(), ingest.DecisionRequest{
		Source:          src,
		Candidates:      result.Candidates,
		RelatedMemories: related,
		ContextComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Decisions) != 1 || decision.Decisions[0].Action != ingest.ActionCorrect ||
		decision.Decisions[0].TargetMemoryIDs[0] != "memory-1" {
		t.Fatalf("decision = %+v", decision)
	}

	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodPost || request.Path != "/v1/chat/completions" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer secret-key" {
		t.Fatalf("Authorization = %q", got)
	}
	var body chatRequest
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "test-model" || body.Temperature != 0 || body.ResponseFormat.Type != "json_object" {
		t.Fatalf("request body = %+v", body)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v", body.Messages)
	}
	if !strings.Contains(body.Messages[0].Content, "candidates array") ||
		strings.Contains(body.Messages[0].Content, "target_memory_ids") {
		t.Fatalf("extraction prompt = %q", body.Messages[0].Content)
	}
	var payload extractionPayload
	if err := json.Unmarshal([]byte(body.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CurrentSource.ID != "source-1" || payload.CurrentSource.AppID != "app-1" ||
		payload.CurrentSource.ExternalID != "external-1" || payload.CurrentSource.ProtocolVersion != extractionProtocolVersion ||
		len(payload.CurrentSource.Segments) != 1 || payload.CurrentSource.Segments[0].Text != "默认中文" ||
		payload.CurrentSource.Segments[0].MessageID != "m1" || payload.CurrentSource.Segments[0].Role != "" {
		t.Fatalf("current source payload = %+v", payload.CurrentSource)
	}
	if strings.Contains(body.Messages[1].Content, "secret-key") {
		t.Fatal("request leaked the model credential")
	}

	var decisionBody chatRequest
	if err := json.Unmarshal(requests[1].Body, &decisionBody); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decisionBody.Messages[0].Content, "decisions array") {
		t.Fatalf("decision prompt = %q", decisionBody.Messages[0].Content)
	}
	var decisionPayload decisionContextPayload
	if err := json.Unmarshal([]byte(decisionBody.Messages[1].Content), &decisionPayload); err != nil {
		t.Fatal(err)
	}
	if len(decisionPayload.RelatedMemories) != 1 || decisionPayload.RelatedMemories[0].ID != "memory-1" ||
		decisionPayload.RelatedMemories[0].Version != 2 {
		t.Fatalf("decision payload = %+v", decisionPayload)
	}
}

func TestProviderStatusAndMalformedErrors(t *testing.T) {
	for _, tc := range []struct {
		status    int
		retryable bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			fake := testutil.NewFakeModel(t)
			fake.EnqueueStatus(tc.status)
			provider, err := NewProvider(Config{Name: "test", BaseURL: fake.URL(), APIKey: "secret", Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Extract(context.Background(), ingest.ExtractionRequest{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
			var providerErr *ingest.ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %v", err)
			}
			if providerErr.Retryable != tc.retryable {
				t.Fatalf("retryable = %v, want %v", providerErr.Retryable, tc.retryable)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "scripted failure") {
				t.Fatalf("error leaked request/response detail: %v", err)
			}
		})
	}
}

func TestProviderMalformedCompletionIsProtocolError(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueMalformed()
	provider, err := NewProvider(Config{Name: "test", BaseURL: fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Extract(context.Background(), ingest.ExtractionRequest{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
	var providerErr *ingest.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Operation != "protocol" ||
		providerErr.Retryable || !errors.Is(err, ErrUnparseable) {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderZeroCandidatesIsSuccess(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	provider, err := NewProvider(Config{Name: "test", BaseURL: fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Extract(context.Background(), ingest.ExtractionRequest{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidates == nil || len(result.Candidates) != 0 {
		t.Fatalf("candidates = %#v", result.Candidates)
	}
}

func TestProviderTimeoutIsRetryable(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.Enqueue(testutil.FakeResponse{Delay: 500 * time.Millisecond})
	provider, err := NewProvider(Config{Name: "test", BaseURL: fake.URL(), Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = provider.Extract(context.Background(), ingest.ExtractionRequest{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
	var providerErr *ingest.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestProviderConnectionFailureIsRetryable(t *testing.T) {
	provider, err := NewProvider(Config{Name: "test", BaseURL: "http://127.0.0.1:1/v1", Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Extract(context.Background(), ingest.ExtractionRequest{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
	var providerErr *ingest.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderTaskProfileAndTokenUsage(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueRaw(http.StatusOK, `{"choices":[{"message":{"content":"{\"candidates\":[]}"}}],"usage":{"prompt_tokens":13,"completion_tokens":7}}`)
	provider, err := NewProvider(Config{
		Name: "base-model", BaseURL: fake.URL(), Timeout: time.Second,
		ExtractionModel: "extract-model", ExtractionReasoning: "low",
		DecisionModel: "decision-model", DecisionReasoning: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Extract(context.Background(), ingest.ExtractionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.Calls != 1 || result.Usage.InputTokens != 13 || result.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	var request chatRequest
	if err := json.Unmarshal(requests[0].Body, &request); err != nil {
		t.Fatal(err)
	}
	if request.Model != "extract-model" || request.ReasoningEffort != "low" {
		t.Fatalf("extraction profile = %+v", request)
	}
}
