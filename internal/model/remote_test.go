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

	"github.com/N0rthHXD/dmemd/internal/aggregation"
	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/extraction"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/skill"
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
		ExtractionTimeout:  180 * time.Second,
		GovernanceTimeout:  300 * time.Second,
		AggregationTimeout: 600 * time.Second,
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
	expected, reply = 180*time.Second, `{"items":[]}`
	if _, err := provider.Extract(context.Background(), extraction.Request{}); err != nil {
		t.Fatal(err)
	}
	expected, reply = 300*time.Second, `{"relations":[]}`
	if _, err := provider.Relate(context.Background(), governance.RelationRequest{}); err != nil {
		t.Fatal(err)
	}
	expected, reply = 600*time.Second, `{"content":"聚合正文"}`
	if _, err := provider.Aggregate(context.Background(), aggregation.Request{}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderMapsMinimalExtractionRequestsAndResponses(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"items":[
		{"span_ids":["seg-0001"],"kind":"preference"},
		{"span_ids":["seg-0002"],"content":"默认中文"}
	]}`)
	provider, err := NewProvider(Config{Name: "test-model", BaseURL: fake.URL() + "/v1", APIKey: "secret-key", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	src := source.Source{
		ID: "source-1", AppID: "app-1", ExternalID: "external-1", ScopeKey: "project:dmemd",
		Content:   json.RawMessage(`{"messages":[{"id":"m1","content":"默认中文","occurred_at":"2026-10-20T02:00:00Z"}]}`),
		CreatedAt: time.Date(2026, 10, 20, 2, 0, 1, 0, time.UTC),
	}
	result, err := provider.Extract(context.Background(), extraction.Request{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Drafts) != 2 ||
		result.Drafts[0].Kind != observation.KindPreference ||
		len(result.Drafts[0].SpanIDs) != 1 || result.Drafts[0].SpanIDs[0] != "seg-0001" ||
		result.Drafts[1].Content != "默认中文" {
		t.Fatalf("drafts = %+v", result.Drafts)
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
	if !strings.Contains(body.Messages[0].Content, "span_ids") ||
		!strings.Contains(body.Messages[0].Content, "current_source.segments") ||
		!strings.Contains(body.Messages[0].Content, `{"items":[]}`) ||
		strings.Contains(body.Messages[0].Content, "candidate_id") ||
		strings.Contains(body.Messages[0].Content, "target_memory_ids") ||
		strings.Contains(body.Messages[0].Content, "valid_from") {
		t.Fatalf("extraction prompt = %q", body.Messages[0].Content)
	}
	var payload extractionPayload
	if err := json.Unmarshal([]byte(body.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CurrentSource.ScopeKey != "project:dmemd" ||
		len(payload.CurrentSource.Segments) != 1 ||
		payload.CurrentSource.Segments[0].ID != "seg-0001" ||
		payload.CurrentSource.Segments[0].Text != "默认中文" ||
		payload.CurrentSource.Segments[0].Role != "" {
		t.Fatalf("current source payload = %+v", payload.CurrentSource)
	}
	for _, leaked := range []string{"source-1", "app-1", "external-1", "m1", "secret-key"} {
		if strings.Contains(body.Messages[1].Content, leaked) {
			t.Fatalf("request payload leaked %q: %s", leaked, body.Messages[1].Content)
		}
	}
}

func TestProviderMapsRelationAndAggregationProtocols(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"relations":[{"from_ref":"new","to_ref":"existing_1","kind":"supports"}]}`)
	fake.EnqueueContent(`{"content":"项目使用 SQLite。"}`)
	provider, err := NewProvider(Config{Name: "test-model", BaseURL: fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	relation, err := provider.Relate(context.Background(), governance.RelationRequest{
		NewObservation: governance.RelationCandidate{Ref: "new", Type: "observation", Kind: "fact", Content: "使用 SQLite"},
		Existing: []governance.RelationCandidate{
			{Ref: "existing_1", Type: "memory", Kind: "fact", Content: "使用 SQLite"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(relation.Relations) != 1 || relation.Relations[0].Kind != governance.RelationSupports ||
		relation.Relations[0].ToRef != "existing_1" {
		t.Fatalf("relation = %+v", relation)
	}

	aggregate, err := provider.Aggregate(context.Background(), aggregation.Request{
		Target: aggregation.Target{Layer: artifact.LayerL1, Kind: artifact.KindTopicSummary, ScopeKey: "project:dmemd"},
		Inputs: []aggregation.InputRef{
			{Type: artifact.DependencyObservation, ID: "obs-1", Content: "项目使用 SQLite。"},
		},
		InputCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Content != "项目使用 SQLite。" || aggregate.Title != "" {
		t.Fatalf("aggregate = %+v", aggregate)
	}

	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	var relationBody chatRequest
	if err := json.Unmarshal(requests[0].Body, &relationBody); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{
		"duplicate", "supports", "enriches", "supersedes", "contradicts",
		"separate", "independent", "unresolved",
	} {
		if !strings.Contains(relationBody.Messages[0].Content, kind) {
			t.Fatalf("relation prompt missing %q: %q", kind, relationBody.Messages[0].Content)
		}
	}
	for _, forbidden := range []string{"add", "change", "correct", "action", "valid_from", "evidence"} {
		if strings.Contains(relationBody.Messages[0].Content, forbidden) {
			t.Fatalf("relation prompt contains %q: %q", forbidden, relationBody.Messages[0].Content)
		}
	}
	var relationPayload governance.RelationRequest
	if err := json.Unmarshal([]byte(relationBody.Messages[1].Content), &relationPayload); err != nil {
		t.Fatal(err)
	}
	if relationPayload.NewObservation.Ref != "new" ||
		len(relationPayload.Existing) != 1 ||
		relationPayload.Existing[0].Ref != "existing_1" ||
		strings.Contains(relationBody.Messages[1].Content, `"id"`) {
		t.Fatalf("relation payload = %s", relationBody.Messages[1].Content)
	}
	var aggregateBody chatRequest
	if err := json.Unmarshal(requests[1].Body, &aggregateBody); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(aggregateBody.Messages[0].Content, `{"content":"派生正文"}`) ||
		strings.Contains(aggregateBody.Messages[0].Content, "citations") {
		t.Fatalf("aggregation prompt = %q", aggregateBody.Messages[0].Content)
	}
	for _, forbidden := range []string{`"id"`, `"version"`, `"target_key"`, "obs-1"} {
		if strings.Contains(aggregateBody.Messages[1].Content, forbidden) {
			t.Fatalf("aggregation payload leaked %q: %s", forbidden, aggregateBody.Messages[1].Content)
		}
	}
}

func TestProviderMapsL4GuidanceWithoutIdentifiers(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"content":"案例正文"}`)
	fake.EnqueueContent(`{"content":"## 步骤\n1. 运行 go test。"}`)
	provider, err := NewProvider(Config{Name: "test-model", BaseURL: fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	experience, err := provider.ComposeExperience(context.Background(), artifact.ExperienceRequest{
		Run: artifact.ExperienceInput{
			ID: "experience-secret", RunID: "run-secret",
			ScopeKey: "project:dmemd", Goal: "验证修改",
			Conditions: []string{"依赖已安装"}, Actions: []string{"运行 go test"},
			Outcome: "success", SkillID: "skill-secret", SkillVersion: 2,
		},
		ScopeKey: "project:dmemd",
		Inputs: []artifact.InputRef{
			{Type: artifact.DependencyMemory, ID: "memory-secret", Version: 3,
				Kind: "procedure", Title: "验证步骤", Content: "修改后运行 go test。"},
		},
	})
	if err != nil || experience.Content != "案例正文" {
		t.Fatalf("experience = %+v err=%v", experience, err)
	}
	draft, err := provider.ComposeSkill(context.Background(), skill.Request{
		ScopeKey: "project:dmemd",
		Inputs: []artifact.InputRef{
			{Type: artifact.DependencyMemory, ID: "memory-secret", Version: 3,
				Kind: "procedure", Content: "修改后运行 go test。"},
			{Type: artifact.DependencyExperience, ID: "experience-secret",
				Kind: "experience", Content: "应用报告结果：failure"},
		},
	})
	if err != nil || !draft.Usable() {
		t.Fatalf("skill = %+v err=%v", draft, err)
	}
	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	for index, request := range requests {
		var body chatRequest
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		for _, leaked := range []string{
			"experience-secret", "run-secret", "source-secret", "skill-secret",
			"memory-secret", `"id"`, `"version"`, "source_id", "skill_id",
		} {
			if strings.Contains(body.Messages[1].Content, leaked) {
				t.Fatalf("L4 request %d leaked %q: %s", index, leaked, body.Messages[1].Content)
			}
		}
	}
	if !strings.Contains(string(requests[0].Body), "application-reported") ||
		!strings.Contains(string(requests[0].Body), "never call it verified") {
		t.Fatalf("experience prompt = %s", requests[0].Body)
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
			_, err = provider.Extract(context.Background(), extraction.Request{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
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
	_, err = provider.Extract(context.Background(), extraction.Request{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
	var providerErr *ingest.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Operation != "protocol" ||
		providerErr.Retryable || !errors.Is(err, ErrUnparseable) {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderZeroDraftsIsSuccess(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueContent(`{"items":[]}`)
	provider, err := NewProvider(Config{Name: "test", BaseURL: fake.URL(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Extract(context.Background(), extraction.Request{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Drafts == nil || len(result.Drafts) != 0 {
		t.Fatalf("drafts = %#v", result.Drafts)
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
	_, err = provider.Extract(context.Background(), extraction.Request{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
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
	_, err = provider.Extract(context.Background(), extraction.Request{Source: source.Source{ID: "s", Content: []byte(`"x"`)}})
	var providerErr *ingest.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderTaskProfileAndTokenUsage(t *testing.T) {
	fake := testutil.NewFakeModel(t)
	fake.EnqueueRaw(http.StatusOK, `{"choices":[{"message":{"content":"{\"items\":[]}"}}],"usage":{"prompt_tokens":13,"completion_tokens":7}}`)
	provider, err := NewProvider(Config{
		Name: "base-model", BaseURL: fake.URL(), Timeout: time.Second,
		ExtractionModel: "extract-model", ExtractionReasoning: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Extract(context.Background(), extraction.Request{})
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
