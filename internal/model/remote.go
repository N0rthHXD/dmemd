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

	"github.com/N0rthHXD/dmemd/internal/aggregation"
	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/extraction"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/skill"
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
	Name                 string
	BaseURL              string
	APIKey               string
	Timeout              time.Duration
	ExtractionTimeout    time.Duration
	GovernanceTimeout    time.Duration
	AggregationTimeout   time.Duration
	ExtractionModel      string
	GovernanceModel      string
	AggregationModel     string
	ExtractionReasoning  string
	GovernanceReasoning  string
	AggregationReasoning string
	HTTPClient           *http.Client
}

// CallMetrics is a source-free observation of one remote model call.
type CallMetrics struct {
	Operation string
	Model     string
	Duration  time.Duration
	Usage     ingest.ModelUsage
}

// CallObserver receives one metrics record after each completed provider call.
type CallObserver func(CallMetrics)

// Provider is the OpenAI-compatible Chat Completions adapter. It implements the
// extraction, relation and aggregation capabilities over the same endpoint and
// credential, with an optional per-task model, reasoning and timeout profile.
type Provider struct {
	extraction         modelProfile
	governance         modelProfile
	aggregation        modelProfile
	endpoint           string
	apiKey             string
	client             *http.Client
	extractionTimeout  time.Duration
	governanceTimeout  time.Duration
	aggregationTimeout time.Duration
	observer           CallObserver
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
	if cfg.ExtractionTimeout <= 0 {
		cfg.ExtractionTimeout = timeout
	}
	if cfg.GovernanceTimeout <= 0 {
		cfg.GovernanceTimeout = timeout
	}
	if cfg.AggregationTimeout <= 0 {
		cfg.AggregationTimeout = timeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &Provider{
		extraction:         newModelProfile(cfg.Name, cfg.ExtractionModel, cfg.ExtractionReasoning),
		governance:         newModelProfile(cfg.Name, cfg.GovernanceModel, cfg.GovernanceReasoning),
		aggregation:        newModelProfile(cfg.Name, cfg.AggregationModel, cfg.AggregationReasoning),
		endpoint:           strings.TrimRight(base, "/") + "/chat/completions",
		apiKey:             cfg.APIKey,
		client:             client,
		extractionTimeout:  cfg.ExtractionTimeout,
		governanceTimeout:  cfg.GovernanceTimeout,
		aggregationTimeout: cfg.AggregationTimeout,
	}, nil
}

func newModelProfile(base, override, reasoning string) modelProfile {
	name := strings.TrimSpace(override)
	if name == "" {
		name = strings.TrimSpace(base)
	}
	return modelProfile{Name: name, ReasoningEffort: strings.TrimSpace(reasoning)}
}

// SetObserver installs an optional metrics observer. It must be called before
// the background workers start.
func (p *Provider) SetObserver(observer CallObserver) {
	if p != nil {
		p.observer = observer
	}
}

// NewRemote is an alias for NewProvider used by runtime assembly code.
func NewRemote(cfg Config) (*Provider, error) { return NewProvider(cfg) }

// ExtractionProvider turns one bounded source view into L0 observation drafts.
// It never receives memory state and never writes storage.
type ExtractionProvider interface {
	Extract(ctx context.Context, request extraction.Request) (extraction.Result, error)
}

// GovernanceProvider judges only the semantic relation between one observation
// and a bounded candidate set. It never returns database actions or real IDs.
type GovernanceProvider interface {
	Relate(ctx context.Context, request governance.RelationRequest) (governance.RelationResult, error)
}

// AggregationProvider writes only the body of one derived layer target. Code
// owns the target, title, dependencies, version and stale state.
type AggregationProvider interface {
	Aggregate(ctx context.Context, request aggregation.Request) (aggregation.Result, error)
}

var (
	_ ExtractionProvider           = (*Provider)(nil)
	_ GovernanceProvider           = (*Provider)(nil)
	_ AggregationProvider          = (*Provider)(nil)
	_ aggregation.GuidanceProvider = (*Provider)(nil)
)

// Extract sends only the current source segment view. The provider performs no
// internal retries and stores nothing.
func (p *Provider) Extract(ctx context.Context, request extraction.Request) (extraction.Result, error) {
	payload, err := buildExtractionRequest(p.extraction, request)
	if err != nil {
		return extraction.Result{}, &ingest.ProviderError{Operation: "request", Retryable: false, Err: err}
	}
	started := time.Now()
	completion, err := p.completeWithTimeout(ctx, payload, p.extractionTimeout)
	p.observe("extraction", p.extraction, started, completion.Usage)
	usage := extraction.ModelUsage{
		Calls:        completion.Usage.Calls,
		InputTokens:  completion.Usage.InputTokens,
		OutputTokens: completion.Usage.OutputTokens,
	}
	if err != nil {
		return extraction.Result{Usage: usage}, err
	}
	parsed, err := ParseExtractionDraft(completion.Content)
	result := extraction.Result{Drafts: parsed.Drafts, Usage: usage}
	if err != nil {
		return result, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ErrUnparseable}
	}
	return result, nil
}

// Relate sends one bounded relation case. It never writes storage and never
// receives real database identifiers.
func (p *Provider) Relate(ctx context.Context, request governance.RelationRequest) (governance.RelationResult, error) {
	payload, err := buildRelationRequest(p.governance, request)
	if err != nil {
		return governance.RelationResult{}, &ingest.ProviderError{Operation: "request", Retryable: false, Err: err}
	}
	started := time.Now()
	completion, err := p.completeWithTimeout(ctx, payload, p.governanceTimeout)
	p.observe("relation", p.governance, started, completion.Usage)
	if err != nil {
		return governance.RelationResult{Usage: completion.Usage}, err
	}
	relations, err := ParseRelations(completion.Content)
	result := governance.RelationResult{Relations: relations, Usage: completion.Usage}
	if err != nil {
		return result, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ErrUnparseable}
	}
	return result, nil
}

// Aggregate writes one derived body for the supplied target. It performs no
// writes and never receives raw source payloads or real identifiers.
func (p *Provider) Aggregate(ctx context.Context, request aggregation.Request) (aggregation.Result, error) {
	payload, err := buildAggregationRequest(p.aggregation, request)
	if err != nil {
		return aggregation.Result{}, &ingest.ProviderError{Operation: "request", Retryable: false, Err: err}
	}
	started := time.Now()
	completion, err := p.completeWithTimeout(ctx, payload, p.aggregationTimeout)
	p.observe("aggregation", p.aggregation, started, completion.Usage)
	if err != nil {
		return aggregation.Result{Usage: completion.Usage}, err
	}
	result, err := ParseAggregation(completion.Content)
	result.Usage = completion.Usage
	if err != nil {
		return result, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ErrUnparseable}
	}
	return result, nil
}

// SuggestTopics asks for flat topic labels for one already-stored atomic
// memory. It performs no writes and receives only the selected snapshot.
func (p *Provider) SuggestTopics(ctx context.Context, title, content, scopeKey string) ([]string, error) {
	payload, err := buildClassificationRequest(p.governance, title, content, scopeKey)
	if err != nil {
		return nil, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: err}
	}
	started := time.Now()
	completion, err := p.completeWithTimeout(ctx, payload, p.governanceTimeout)
	p.observe("classification", p.governance, started, completion.Usage)
	if err != nil {
		return nil, err
	}
	topics, err := ParseTopics(completion.Content)
	if err != nil {
		return nil, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ErrUnparseable}
	}
	return topics, nil
}

// ComposeExperience asks for only the readable body of one experience case. It
// performs no writes and never receives source payloads or real identifiers; the
// caller attaches the stored run, preserves the application-reported outcome
// and generates all metadata.
func (p *Provider) ComposeExperience(ctx context.Context, request artifact.ExperienceRequest) (artifact.ExperienceResult, error) {
	payload, err := buildExperienceRequest(p.aggregation, request)
	if err != nil {
		return artifact.ExperienceResult{}, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: err}
	}
	started := time.Now()
	completion, err := p.completeWithTimeout(ctx, payload, p.aggregationTimeout)
	p.observe("experience", p.aggregation, started, completion.Usage)
	if err != nil {
		return artifact.ExperienceResult{}, err
	}
	result, err := ParseExperience(completion.Content)
	if err != nil {
		return artifact.ExperienceResult{}, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ErrUnparseable}
	}
	return result, nil
}

// ComposeSkill asks for one guidance-only workflow. It performs no writes and
// cannot grant tools or change service configuration.
func (p *Provider) ComposeSkill(ctx context.Context, request skill.Request) (skill.Draft, error) {
	payload, err := buildSkillRequest(p.aggregation, request)
	if err != nil {
		return skill.Draft{}, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: err}
	}
	started := time.Now()
	completion, err := p.completeWithTimeout(ctx, payload, p.aggregationTimeout)
	p.observe("skill", p.aggregation, started, completion.Usage)
	if err != nil {
		return skill.Draft{}, err
	}
	result, err := ParseSkill(completion.Content)
	if err != nil {
		return skill.Draft{}, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ErrUnparseable}
	}
	return result, nil
}

type completionResult struct {
	Content string
	Usage   ingest.ModelUsage
}

func (p *Provider) completeWithTimeout(ctx context.Context, payload chatRequest, timeout time.Duration) (completionResult, error) {
	usage := ingest.ModelUsage{Calls: 1}
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return completionResult{Usage: usage}, &ingest.ProviderError{Operation: "request", Retryable: false, Err: err}
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return completionResult{Usage: usage}, &ingest.ProviderError{Operation: "request", Retryable: false, Err: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	response, err := p.client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return completionResult{Usage: usage}, &ingest.ProviderError{
				Operation: "request",
				Retryable: true,
				Err:       ctx.Err(),
			}
		}
		if callCtx.Err() != nil {
			return completionResult{Usage: usage}, &ingest.ProviderError{
				Operation: "timeout",
				Retryable: true,
				Err:       context.DeadlineExceeded,
			}
		}
		return completionResult{Usage: usage}, &ingest.ProviderError{
			Operation: "request",
			Retryable: true,
			Err:       errors.New("模型服务连接失败"),
		}
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	responseBody, readErr := io.ReadAll(limited)
	if readErr != nil {
		return completionResult{Usage: usage}, &ingest.ProviderError{
			Operation: "response",
			Retryable: true,
			Err:       errors.New("读取模型响应失败"),
		}
	}
	if len(responseBody) > maxResponseBytes {
		return completionResult{Usage: usage}, &ingest.ProviderError{
			Operation: "response",
			Retryable: true,
			Err:       errors.New("模型响应超过大小上限"),
		}
	}
	if response.StatusCode != http.StatusOK {
		operation := "response"
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			operation = "credentials"
		} else if !retryableStatus(response.StatusCode) {
			operation = "configuration"
		}
		return completionResult{Usage: usage}, &ingest.ProviderError{
			Operation: operation,
			Retryable: retryableStatus(response.StatusCode),
			Err:       fmt.Errorf("HTTP %d", response.StatusCode),
		}
	}

	result, err := completionContent(responseBody)
	result.Usage.Calls = 1
	if err != nil {
		return result, &ingest.ProviderError{Operation: "protocol", Retryable: false, Err: ErrUnparseable}
	}
	return result, nil
}

func completionContent(body []byte) (completionResult, error) {
	var envelope completionResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return completionResult{}, ErrUnparseable
	}
	if len(envelope.Choices) == 0 {
		return completionResult{}, ErrUnparseable
	}
	content := envelope.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		return completionResult{}, ErrUnparseable
	}
	return completionResult{
		Content: content,
		Usage: ingest.ModelUsage{
			InputTokens:  envelope.Usage.PromptTokens,
			OutputTokens: envelope.Usage.CompletionTokens,
		},
	}, nil
}

func (p *Provider) observe(operation string, profile modelProfile, started time.Time, usage ingest.ModelUsage) {
	if p.observer == nil {
		return
	}
	p.observer(CallMetrics{
		Operation: operation,
		Model:     profile.Name,
		Duration:  time.Since(started),
		Usage:     usage,
	})
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
