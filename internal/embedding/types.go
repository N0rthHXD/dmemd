// Package embedding owns document encoding, configuration fingerprints,
// content hashes, vector validation and the background embedding task
// orchestration. It never writes memory facts.
package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

const (
	// IndexVersion changes whenever the document template, normalization rules
	// or profile fingerprint shape change. Vectors generated under another
	// version are rebuilt instead of silently reused.
	IndexVersion = "dmemd-embedding-v1"

	// DefaultTimeout bounds one embedding HTTP call.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxAttempts is the total attempt budget including the first try.
	DefaultMaxAttempts = 3
	// DefaultRetryBase is the first retry delay; later delays double it.
	DefaultRetryBase = time.Second
	// DefaultRetryMax caps every retry delay.
	DefaultRetryMax = 60 * time.Second
)

// Provider is the Embedding HTTP adapter capability. The caller always batches
// one text per call, but the interface makes batch counting and ordering
// explicit.
type Provider interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Profile is the stable, credential-free identity of one Embedding
// configuration. Queries and documents must use the same Profile.
type Profile struct {
	ID         string
	Dimensions int
}

// Config is the embedding runtime configuration after applying defaults.
type Config struct {
	Enabled       bool
	BaseURL       string
	APIKey        string
	Model         string
	Dimensions    int
	Timeout       time.Duration
	MinSimilarity float64
}

// WithDefaults returns a copy with every optional value filled in.
func (c Config) WithDefaults() Config {
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	return c
}

// Validate checks the complete embedding configuration. Disabled configuration
// may omit the endpoint and dimensions because no model call can happen, but
// every supplied value must still be coherent.
func (c Config) Validate() error {
	c = c.WithDefaults()
	if c.Timeout <= 0 {
		return errors.New("embedding.timeout 必须大于 0")
	}
	if c.MinSimilarity < -1 || c.MinSimilarity > 1 {
		return errors.New("embedding.min_similarity 必须在 [-1, 1] 之间")
	}
	if !c.Enabled {
		return nil
	}
	if c.Dimensions <= 0 {
		return errors.New("embedding.dimensions 必须大于 0")
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("embedding.model 不能为空")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("embedding.base_url 不能为空")
	}
	endpoint, err := endpointIdentity(c.BaseURL)
	if err != nil {
		return err
	}
	if endpoint == "" {
		return errors.New("embedding.base_url 无效")
	}
	return nil
}

// BuildProfile derives the credential-free configuration fingerprint.
func (c Config) BuildProfile() (Profile, error) {
	c = c.WithDefaults()
	if err := c.Validate(); err != nil {
		return Profile{}, err
	}
	if !c.Enabled {
		return Profile{}, errors.New("embedding 功能未启用")
	}
	endpoint, err := endpointIdentity(c.BaseURL)
	if err != nil {
		return Profile{}, err
	}
	seed := strings.Join([]string{
		IndexVersion,
		c.Model,
		endpoint,
		fmt.Sprintf("%d", c.Dimensions),
	}, "\n")
	sum := sha256.Sum256([]byte(seed))
	return Profile{
		ID:         hex.EncodeToString(sum[:]),
		Dimensions: c.Dimensions,
	}, nil
}

func endpointIdentity(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("embedding.base_url %q 必须是 http(s) 绝对地址", raw)
	}
	// Credentials and query parameters are deliberately excluded from the
	// profile: Azure-style deployments usually carry the API version, not the
	// secret, but an explicit credential query must never leak into a hash.
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

// NormalizeDocument applies exactly the document rule: NFC plus leading and
// trailing whitespace removal. Case, punctuation and internal whitespace are
// preserved.
func NormalizeDocument(text string) string {
	return strings.TrimSpace(norm.NFC.String(text))
}

// NormalizeQuery applies the same NFC/trim rule as documents without the
// document template.
func NormalizeQuery(text string) string {
	return strings.TrimSpace(norm.NFC.String(text))
}

// DocumentText renders the fixed embedding input template. FTS tokenization
// must never be sent to the model. The canonical view contains only the fields
// that are part of its identity and current content.
func DocumentText(item memory.Memory) string {
	parts := []string{
		"kind: " + string(item.Kind),
		"title: " + NormalizeDocument(item.Title),
		"content: " + NormalizeDocument(item.Content),
	}
	return strings.Join(parts, "\n")
}

// DocumentHash hashes the input contract version and the exact document bytes.
func DocumentHash(item memory.Memory) string {
	sum := sha256.Sum256([]byte(IndexVersion + "\x00" + DocumentText(item)))
	return hex.EncodeToString(sum[:])
}

// DocumentInput returns the normalized model input and its content hash for one
// current memory snapshot.
func DocumentInput(item memory.Memory) (string, string) {
	text := DocumentText(item)
	sum := sha256.Sum256([]byte(IndexVersion + "\x00" + text))
	return text, hex.EncodeToString(sum[:])
}
