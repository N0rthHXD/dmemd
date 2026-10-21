package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/embedding"
)

const embeddingMaxResponseBytes = 8 << 20

// EmbeddingConfig maps the local runtime embedding configuration to the
// OpenAI-compatible Embeddings endpoint.
type EmbeddingConfig struct {
	Name       string
	BaseURL    string
	APIKey     string
	Timeout    time.Duration
	Dimensions int
	HTTPClient *http.Client
}

// Embedding is the HTTP adapter for the separate embedding provider. It stores
// no state and never retries internally.
type Embedding struct {
	endpoint   string
	modelName  string
	apiKey     string
	timeout    time.Duration
	dimensions int
	client     *http.Client
}

// NewEmbeddingProvider validates the endpoint and assembles the adapter.
func NewEmbeddingProvider(cfg EmbeddingConfig) (*Embedding, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, errors.New("model: embedding base_url 不能为空")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("model: embedding base_url %q 必须是 http(s) 绝对地址", cfg.BaseURL)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = embedding.DefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &Embedding{
		endpoint:   strings.TrimRight(base, "/") + "/embeddings",
		modelName:  cfg.Name,
		apiKey:     cfg.APIKey,
		timeout:    timeout,
		dimensions: cfg.Dimensions,
		client:     client,
	}, nil
}

var _ embedding.Provider = (*Embedding)(nil)

type embeddingRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed sends only the prepared text values and validates count, ordering and
// dimensions. Unknown or malformed envelopes become permanent protocol errors.
func (e *Embedding) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(texts) == 0 {
		return nil, embedding.NewProviderError("empty_input", false, nil)
	}
	body, err := json.Marshal(embeddingRequest{
		Model:          e.modelName,
		Input:          texts,
		EncodingFormat: "float",
	})
	if err != nil {
		return nil, embedding.NewProviderError("request", false, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, embedding.NewProviderError("request", false, err)
	}
	request.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	response, err := e.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, embedding.NewProviderError("cancelled", true, ctx.Err())
		}
		if callCtx.Err() != nil {
			return nil, embedding.NewProviderError("timeout", true, callCtx.Err())
		}
		return nil, embedding.NewProviderError("network", true, err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, embeddingMaxResponseBytes+1)
	responseBody, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, embedding.NewProviderError("response", true, readErr)
	}
	if len(responseBody) > embeddingMaxResponseBytes {
		return nil, embedding.NewProviderError("response_too_large", true, nil)
	}
	if response.StatusCode != http.StatusOK {
		return nil, statusError(response.StatusCode, response.Header.Get("Retry-After"))
	}

	var envelope embeddingResponse
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return nil, embedding.NewProviderError("protocol", false, err)
	}
	if len(envelope.Data) != len(texts) {
		return nil, embedding.NewProviderError("count_mismatch", false, nil)
	}
	vectors := make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	for _, item := range envelope.Data {
		if item.Index < 0 || item.Index >= len(texts) || seen[item.Index] {
			return nil, embedding.NewProviderError("order_mismatch", false, nil)
		}
		if e.dimensions > 0 && len(item.Embedding) != e.dimensions {
			return nil, embedding.NewProviderError("dimensions_mismatch", false, nil)
		}
		seen[item.Index] = true
		vectors[item.Index] = item.Embedding
	}
	for _, present := range seen {
		if !present {
			return nil, embedding.NewProviderError("order_mismatch", false, nil)
		}
	}
	return vectors, nil
}

func statusError(status int, retryAfterHeader string) error {
	code := "http_" + strconv.Itoa(status)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return embedding.NewProviderError("unauthorized", false, nil)
	case http.StatusBadRequest:
		return embedding.NewProviderError("http_400", false, nil)
	case http.StatusNotFound:
		return embedding.NewProviderError("http_404", false, nil)
	case http.StatusTooManyRequests:
		return &embedding.ProviderError{
			Code:       "http_429",
			Retryable:  true,
			RetryAfter: parseRetryAfter(retryAfterHeader),
		}
	case http.StatusRequestTimeout, http.StatusTooEarly:
		return embedding.NewProviderError(code, true, nil)
	}
	if status >= 500 {
		return embedding.NewProviderError("http_5xx", true, nil)
	}
	return embedding.NewProviderError(code, false, nil)
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay <= 0 {
			return 0
		}
		return delay
	}
	return 0
}
