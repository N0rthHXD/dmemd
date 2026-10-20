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
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/ingest"
)

const (
	// DefaultTimeout is the architecture default for one remote model call.
	DefaultTimeout   = 60 * time.Second
	maxResponseBytes = 8 << 20
)

// Config maps the local runtime model configuration to the first provider.
// APIKey is the remote model credential and is never read from HTTP requests or
// forwarded from the local API token.
type Config struct {
	Name       string
	BaseURL    string
	APIKey     string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Provider is the OpenAI-compatible Chat Completions ExtractionProvider.
type Provider struct {
	modelName string
	endpoint  string
	apiKey    string
	timeout   time.Duration
	client    *http.Client
}

// NewProvider validates the endpoint and assembles the remote adapter. No
// network request is made here.
func NewProvider(cfg Config) (*Provider, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, errors.New("model: base_url 不能为空")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("model: base_url %q 必须是 http(s) 绝对地址", cfg.BaseURL)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &Provider{
		modelName: cfg.Name,
		endpoint:  strings.TrimRight(base, "/") + "/chat/completions",
		apiKey:    cfg.APIKey,
		timeout:   timeout,
		client:    client,
	}, nil
}

// NewRemote is an alias for NewProvider used by runtime assembly code.
func NewRemote(cfg Config) (*Provider, error) { return NewProvider(cfg) }

var _ ingest.ExtractionProvider = (*Provider)(nil)

// Extract sends only the current source and the related active memories chosen
// by the service. The provider performs no internal retries and stores nothing.
func (p *Provider) Extract(ctx context.Context, request ingest.ExtractionRequest) (ingest.ExtractionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := buildChatRequest(p.modelName, request)
	if err != nil {
		return ingest.ExtractionResult{}, &ingest.ProviderError{Operation: "request", Retryable: false, Err: err}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ingest.ExtractionResult{}, &ingest.ProviderError{Operation: "request", Retryable: false, Err: err}
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return ingest.ExtractionResult{}, &ingest.ProviderError{Operation: "request", Retryable: false, Err: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	response, err := p.client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return ingest.ExtractionResult{}, &ingest.ProviderError{
				Operation: "request",
				Retryable: true,
				Err:       ctx.Err(),
			}
		}
		if callCtx.Err() != nil {
			return ingest.ExtractionResult{}, &ingest.ProviderError{
				Operation: "timeout",
				Retryable: true,
				Err:       fmt.Errorf("模型调用在 %s 后超时", p.timeout),
			}
		}
		return ingest.ExtractionResult{}, &ingest.ProviderError{
			Operation: "request",
			Retryable: true,
			Err:       errors.New("模型服务连接失败"),
		}
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	responseBody, readErr := io.ReadAll(limited)
	if readErr != nil {
		return ingest.ExtractionResult{}, &ingest.ProviderError{
			Operation: "response",
			Retryable: true,
			Err:       errors.New("读取模型响应失败"),
		}
	}
	if len(responseBody) > maxResponseBytes {
		return ingest.ExtractionResult{}, &ingest.ProviderError{
			Operation: "response",
			Retryable: true,
			Err:       errors.New("模型响应超过大小上限"),
		}
	}
	if response.StatusCode != http.StatusOK {
		return ingest.ExtractionResult{}, &ingest.ProviderError{
			Operation: "response",
			Retryable: retryableStatus(response.StatusCode),
			Err:       fmt.Errorf("HTTP %d", response.StatusCode),
		}
	}

	content, err := completionContent(responseBody)
	if err != nil {
		return ingest.ExtractionResult{}, &ingest.ProviderError{Operation: "response", Retryable: true, Err: ErrUnparseable}
	}
	candidates, err := ParseCandidates(content)
	if err != nil {
		return ingest.ExtractionResult{}, &ingest.ProviderError{Operation: "response", Retryable: true, Err: ErrUnparseable}
	}
	return ingest.ExtractionResult{Candidates: candidates}, nil
}

func completionContent(body []byte) (string, error) {
	var envelope completionResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", ErrUnparseable
	}
	if len(envelope.Choices) == 0 {
		return "", ErrUnparseable
	}
	content := envelope.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		return "", ErrUnparseable
	}
	return content, nil
}

func retryableStatus(status int) bool {
	switch {
	case status == http.StatusTooManyRequests:
		return true
	case status == http.StatusRequestTimeout, status == http.StatusTooEarly:
		return true
	case status >= 500:
		return true
	default:
		return false
	}
}
