package embedding

import (
	"context"
	"errors"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

// Encoder validates, normalizes and fingerprints every model input and output.
// It is immutable after construction and safe for concurrent queries.
type Encoder struct {
	provider      Provider
	profile       Profile
	minSimilarity float64
}

// NewEncoder assembles one enabled Embedding profile.
func NewEncoder(provider Provider, cfg Config) (*Encoder, error) {
	cfg = cfg.WithDefaults()
	if !cfg.Enabled {
		return nil, errors.New("embedding: 功能未启用")
	}
	if provider == nil {
		return nil, errors.New("embedding: provider 未配置")
	}
	profile, err := cfg.BuildProfile()
	if err != nil {
		return nil, err
	}
	return &Encoder{
		provider:      provider,
		profile:       profile,
		minSimilarity: cfg.MinSimilarity,
	}, nil
}

// ProfileID returns the credential-free configuration fingerprint.
func (e *Encoder) ProfileID() string { return e.profile.ID }

// Dimensions returns the required vector length.
func (e *Encoder) Dimensions() int { return e.profile.Dimensions }

// MinSimilarity returns the calibrated semantic admission threshold.
func (e *Encoder) MinSimilarity() float64 { return e.minSimilarity }

// EmbedQuery embeds one normalized query. Unlike a document, a query is never
// wrapped in the document template.
func (e *Encoder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	normalized := NormalizeQuery(text)
	if normalized == "" {
		return nil, &ProviderError{Code: "empty_query"}
	}
	vectors, err := e.provider.Embed(ctx, []string{normalized})
	if err != nil {
		return nil, err
	}
	if err := ValidateVectors(vectors, 1, e.profile.Dimensions); err != nil {
		return nil, &ProviderError{Code: "invalid_vector", Err: err}
	}
	normalizedVector, err := NormalizeL2(vectors[0])
	if err != nil {
		return nil, &ProviderError{Code: "invalid_vector", Err: err}
	}
	return normalizedVector, nil
}

// DocumentVector is the exact model input and normalized, encoded output for
// one current memory snapshot.
type DocumentVector struct {
	ContentHash string
	Dimensions  int
	Vector      []byte
}

// EmbedDocument renders the fixed document template and returns a normalized
// little-endian float32 vector together with the content hash that must match
// the persisted search document.
func (e *Encoder) EmbedDocument(ctx context.Context, item memory.Memory) (DocumentVector, error) {
	text, hash := DocumentInput(item)
	vectors, err := e.provider.Embed(ctx, []string{text})
	if err != nil {
		return DocumentVector{}, err
	}
	if err := ValidateVectors(vectors, 1, e.profile.Dimensions); err != nil {
		return DocumentVector{}, &ProviderError{Code: "invalid_vector", Err: err}
	}
	encoded, err := EncodeVector(vectors[0], e.profile.Dimensions)
	if err != nil {
		return DocumentVector{}, &ProviderError{Code: "invalid_vector", Err: err}
	}
	return DocumentVector{
		ContentHash: hash,
		Dimensions:  e.profile.Dimensions,
		Vector:      encoded,
	}, nil
}
