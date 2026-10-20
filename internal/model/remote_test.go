package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/source"
	"github.com/N0rthHXD/dmemd/internal/testutil"
)

func TestProviderMapsRequestAndResponse(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"candidates":[{"action":"update","target_memory_id":"memory-1","kind":"preference","title":"回答语言","content":"默认中文","observed_at":"2026-10-20T02:00:00Z","evidence":[{"message_id":"m1","quote":"默认中文"}]}]}`)
	provider, err := NewProvider(Config{Name: "test-model", BaseURL: fake.URL() + "/v1", APIKey: "secret-key", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	src := source.Source{
		ID: "source-1", AppID: "app-1", ExternalID: "external-1",
		Content:   json.RawMessage(`{"messages":[{"id":"m1","content":"默认中文","occurred_at":"2026-10-20T02:00:00Z"}]}`),
		CreatedAt: time.Date(2026, 10, 20, 2, 0, 1, 0, time.UTC),
	}
	related := []memory.Memory{{
		ID: "memory-1", Kind: memory.KindPreference, Title: "回答语言", Content: "用户希望中文。",
		Version: 2, ObservedAt: time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 20, 1, 30, 0, 0, time.UTC),
	}}
	result, err := provider.Extract(context.Background(), ingest.ExtractionRequest{Source: src, RelatedMemories: related})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Action != ingest.ActionUpdate ||
		result.Candidates[0].TargetMemoryID != "memory-1" || result.Candidates[0].Content != "默认中文" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Candidates[0].Evidence) != 1 || result.Candidates[0].Evidence[0].Quote != "默认中文" {
		t.Fatalf("evidence = %+v", result.Candidates[0].Evidence)
	}

	requests := fake.Requests()
	if len(requests) != 1 {
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
	if !strings.Contains(body.Messages[0].Content, "content (the memory text)") ||
		!strings.Contains(body.Messages[0].Content, "target_memory_id") ||
		!strings.Contains(body.Messages[0].Content, "empty candidates array") {
		t.Fatalf("system prompt = %q", body.Messages[0].Content)
	}
	var payload extractionPayload
	if err := json.Unmarshal([]byte(body.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CurrentSource.ID != "source-1" || payload.CurrentSource.AppID != "app-1" ||
		payload.CurrentSource.ExternalID != "external-1" || string(payload.CurrentSource.Content) != string(src.Content) {
		t.Fatalf("current source payload = %+v", payload.CurrentSource)
	}
	if len(payload.RelatedMemories) != 1 || payload.RelatedMemories[0].ID != "memory-1" ||
		payload.RelatedMemories[0].Version != 2 {
		t.Fatalf("related payload = %+v", payload.RelatedMemories)
	}
	if strings.Contains(body.Messages[1].Content, "secret-key") {
		t.Fatal("request leaked the model credential")
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

func TestProviderMalformedCompletionIsRetryable(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueMalformed()
	provider, err := NewProvider(Config{Name: "test", BaseURL: fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Extract(context.Background(), ingest.ExtractionRequest{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
	var providerErr *ingest.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable || !errors.Is(err, ErrUnparseable) {
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
