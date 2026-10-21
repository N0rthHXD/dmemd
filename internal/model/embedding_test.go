package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
)

func TestEmbeddingAdapterRequestAndOrdering(t *testing.T) {
	var captured struct {
		request embeddingRequest
		auth    string
		path    string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured.request)
		captured.auth = r.Header.Get("Authorization")
		captured.path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if len(captured.request.Input) == 1 {
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0,0]}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[
			{"index":1,"embedding":[0,1,0]},
			{"index":0,"embedding":[1,0,0]}
		]}`)
	}))
	defer server.Close()

	provider, err := NewEmbeddingProvider(EmbeddingConfig{
		Name: "embed-1", BaseURL: server.URL, APIKey: "secret", Timeout: time.Second,
		Dimensions: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := provider.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if captured.path != "/embeddings" {
		t.Fatalf("path = %q", captured.path)
	}
	if captured.auth != "Bearer secret" {
		t.Fatalf("auth = %q", captured.auth)
	}
	if captured.request.Model != "embed-1" || captured.request.EncodingFormat != "float" {
		t.Fatalf("request = %+v", captured.request)
	}
	if len(captured.request.Input) != 2 || captured.request.Input[0] != "first" || captured.request.Input[1] != "second" {
		t.Fatalf("input = %#v", captured.request.Input)
	}
	if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("vectors were not reordered by index: %v", vectors)
	}

	if _, err := provider.Embed(context.Background(), []string{"doc"}); err != nil {
		t.Fatal(err)
	}
	if captured.request.Input[0] != "doc" {
		t.Fatalf("document input = %q", captured.request.Input[0])
	}
}

func TestEmbeddingAdapterPermanentAndRetryableErrors(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		headers   map[string]string
		body      string
		wantCode  string
		wantRetry bool
		wantAfter time.Duration
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "bad request", status: http.StatusBadRequest, wantCode: "http_400"},
		{name: "rate limited", status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "2"}, wantCode: "http_429", wantRetry: true, wantAfter: 2 * time.Second},
		{name: "server error", status: http.StatusBadGateway, wantCode: "http_5xx", wantRetry: true},
		{name: "malformed", status: http.StatusOK, body: `{"data":`, wantCode: "protocol"},
		{name: "count mismatch", status: http.StatusOK, body: `{"data":[]}`, wantCode: "count_mismatch"},
		{name: "order mismatch", status: http.StatusOK, body: `{"data":[{"index":3,"embedding":[1,0,0]}]}`, wantCode: "order_mismatch"},
		{name: "dimension mismatch", status: http.StatusOK, body: `{"data":[{"index":0,"embedding":[1,0]}]}`, wantCode: "dimensions_mismatch"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for key, value := range testCase.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(testCase.status)
				if testCase.body != "" {
					_, _ = io.WriteString(w, testCase.body)
				}
			}))
			defer server.Close()
			provider, err := NewEmbeddingProvider(EmbeddingConfig{
				Name: "m", BaseURL: server.URL, Timeout: time.Second, Dimensions: 3,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Embed(context.Background(), []string{"one"})
			var providerError *embedding.ProviderError
			if !errors.As(err, &providerError) {
				t.Fatalf("error = %v", err)
			}
			if providerError.Code != testCase.wantCode || providerError.Retryable != testCase.wantRetry {
				t.Fatalf("error = %+v", providerError)
			}
			if providerError.RetryAfter != testCase.wantAfter {
				t.Fatalf("retry after = %v, want %v", providerError.RetryAfter, testCase.wantAfter)
			}
		})
	}
}

func TestEmbeddingAdapterNetworkAndTimeout(t *testing.T) {
	provider, err := NewEmbeddingProvider(EmbeddingConfig{
		Name: "m", BaseURL: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond, Dimensions: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Embed(context.Background(), []string{"one"}); !embedding.IsRetryable(err) {
		t.Fatalf("network error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	defer server.Close()
	provider, err = NewEmbeddingProvider(EmbeddingConfig{
		Name: "m", BaseURL: server.URL, Timeout: 20 * time.Millisecond, Dimensions: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Embed(context.Background(), []string{"one"})
	if embedding.ErrorCode(err) != "timeout" || !embedding.IsRetryable(err) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestEmbeddingAdapterRejectsInvalidEndpoint(t *testing.T) {
	for _, baseURL := range []string{"", "not-a-url", "ftp://example.com"} {
		if _, err := NewEmbeddingProvider(EmbeddingConfig{BaseURL: baseURL}); err == nil ||
			!strings.Contains(err.Error(), "base_url") {
			t.Fatalf("base_url %q error = %v", baseURL, err)
		}
	}
}
