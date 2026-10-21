package embedding

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dmemd/internal/memory"
)

func TestDocumentTemplateAndHash(t *testing.T) {
	item := memory.Memory{
		ID:         "m1",
		Kind:       memory.KindPreference,
		Title:      "  饮食偏好 ",
		Content:    " 用户偏好清淡饮食。 ",
		State:      memory.StateActive,
		Version:    3,
		ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}
	text := DocumentText(item)
	want := "kind: preference\ntitle: 饮食偏好\ncontent: 用户偏好清淡饮食。"
	if text != want {
		t.Fatalf("DocumentText = %q, want %q", text, want)
	}
	hash := DocumentHash(item)
	if hash != DocumentHash(item) || len(hash) != 64 {
		t.Fatalf("hash = %q", hash)
	}
	if text, hash2 := DocumentInput(item); text != want || hash2 != hash {
		t.Fatalf("DocumentInput = %q %q", text, hash2)
	}

	// Metadata and lifecycle changes do not alter the embedding input.
	for _, mutate := range []func(*memory.Memory){
		func(value *memory.Memory) { value.State = memory.StateArchived },
		func(value *memory.Memory) { value.Version = 99 },
		func(value *memory.Memory) { value.ObservedAt = value.ObservedAt.Add(time.Hour) },
		func(value *memory.Memory) { value.CreatedByApp = "app" },
		func(value *memory.Memory) { value.OriginSourceID = "source" },
	} {
		candidate := item
		mutate(&candidate)
		if got := DocumentHash(candidate); got != hash {
			t.Fatalf("metadata mutation changed hash: %q", got)
		}
	}

	for _, mutate := range []func(*memory.Memory){
		func(value *memory.Memory) { value.Kind = memory.KindFact },
		func(value *memory.Memory) { value.Title = "饮食偏好2" },
		func(value *memory.Memory) { value.Content = "用户偏好清淡饮食" },
		func(value *memory.Memory) { value.Title = "饮食偏好。" },
	} {
		candidate := item
		mutate(&candidate)
		if got := DocumentHash(candidate); got == hash {
			t.Fatalf("input mutation did not change hash")
		}
	}

	// Case and internal whitespace are preserved.
	upper := item
	upper.Title = "Diet Preference"
	upper.Content = "Light  meals"
	if !strings.Contains(DocumentText(upper), "Diet Preference") {
		t.Fatal("document normalization lowercased input")
	}
	if !strings.Contains(DocumentText(upper), "Light  meals") {
		t.Fatal("document normalization collapsed internal whitespace")
	}

	// NFC equivalence and query normalization.
	decomposed := memory.Memory{Kind: memory.KindFact, Title: "Cafe\u0301", Content: "x"}
	composed := memory.Memory{Kind: memory.KindFact, Title: "Café", Content: "x"}
	if DocumentHash(decomposed) != DocumentHash(composed) {
		t.Fatal("NFC-equivalent documents have different hashes")
	}
	if NormalizeQuery("  查询  ") != "查询" {
		t.Fatalf("NormalizeQuery = %q", NormalizeQuery("  查询  "))
	}
}

func TestProfileFingerprintExcludesCredentials(t *testing.T) {
	base := Config{
		Enabled:    true,
		BaseURL:    "https://embedding.example/v1",
		APIKey:     "secret-one",
		Model:      "embed-1",
		Dimensions: 3,
		Timeout:    time.Second,
	}
	profile, err := base.BuildProfile()
	if err != nil {
		t.Fatal(err)
	}
	withOtherKey := base
	withOtherKey.APIKey = "secret-two"
	otherProfile, err := withOtherKey.BuildProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != otherProfile.ID {
		t.Fatal("API key changed the profile fingerprint")
	}

	withQuery := base
	withQuery.BaseURL = "https://embedding.example/v1?api-key=leak"
	queryProfile, err := withQuery.BuildProfile()
	if err != nil {
		t.Fatal(err)
	}
	if queryProfile.ID != profile.ID {
		t.Fatal("endpoint query changed the profile fingerprint")
	}

	for name, mutate := range map[string]func(*Config){
		"model":      func(cfg *Config) { cfg.Model = "embed-2" },
		"dimensions": func(cfg *Config) { cfg.Dimensions = 4 },
		"endpoint":   func(cfg *Config) { cfg.BaseURL = "https://other.example/v1" },
	} {
		mutated := base
		mutate(&mutated)
		other, err := mutated.BuildProfile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if other.ID == profile.ID {
			t.Fatalf("%s did not change the profile", name)
		}
	}

	disabled := base
	disabled.Enabled = false
	if _, err := disabled.BuildProfile(); err == nil {
		t.Fatal("disabled config produced a profile")
	}
}

func TestConfigValidation(t *testing.T) {
	valid := Config{
		Enabled: true, BaseURL: "http://127.0.0.1:1234", Model: "m",
		Dimensions: 8, Timeout: time.Second,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := map[string]func(*Config){
		"missing endpoint": func(cfg *Config) { cfg.BaseURL = "" },
		"zero dimensions":  func(cfg *Config) { cfg.Dimensions = 0 },
		"missing model":    func(cfg *Config) { cfg.Model = " " },
		"bad similarity":   func(cfg *Config) { cfg.MinSimilarity = 2 },
	}
	for name, mutate := range cases {
		cfg := valid
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	disabled := Config{}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled defaults rejected: %v", err)
	}
}

func TestVectorEncodingValidationAndDot(t *testing.T) {
	encoded, err := EncodeVector([]float32{3, 4}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 8 {
		t.Fatalf("encoded length = %d", len(encoded))
	}
	if got := math.Float32frombits(binary.LittleEndian.Uint32(encoded[:4])); math.Abs(float64(got)-0.6) > 1e-6 {
		t.Fatalf("first component = %v", got)
	}
	decoded, err := DecodeVector(encoded, 2)
	if err != nil {
		t.Fatal(err)
	}
	dot, err := DotProduct(decoded, decoded)
	if err != nil || math.Abs(dot-1) > 1e-6 {
		t.Fatalf("self dot = %v, %v", dot, err)
	}

	if err := ValidateVectors([][]float32{{1, 0}}, 2, 2); err == nil {
		t.Fatal("count mismatch accepted")
	}
	if err := ValidateVectors([][]float32{{1}}, 1, 2); err == nil {
		t.Fatal("dimension mismatch accepted")
	}
	if err := ValidateVectors([][]float32{{0, 0}}, 1, 2); err == nil {
		t.Fatal("zero norm accepted")
	}
	if err := ValidateVectors([][]float32{{float32(math.NaN()), 1}}, 1, 2); err == nil {
		t.Fatal("NaN accepted")
	}
	if _, err := DecodeVector([]byte{1, 2}, 2); err == nil {
		t.Fatal("short BLOB accepted")
	}
	zeroBlob := make([]byte, 8)
	if _, err := DecodeVector(zeroBlob, 2); err == nil {
		t.Fatal("zero-norm BLOB accepted")
	}
	if _, err := EncodeVector([]float32{1}, 2); err == nil {
		t.Fatal("wrong encode dimensions accepted")
	}
}

type recordingProvider struct {
	texts   []string
	vectors [][]float32
	err     error
}

func (p *recordingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	p.texts = append(p.texts, texts...)
	if p.err != nil {
		return nil, p.err
	}
	return p.vectors, nil
}

func TestEncoderQueryAndDocument(t *testing.T) {
	provider := &recordingProvider{vectors: [][]float32{{3, 4}}}
	cfg := Config{
		Enabled: true, BaseURL: "http://localhost:1", Model: "m", Dimensions: 2,
		Timeout: time.Second,
	}
	encoder, err := NewEncoder(provider, cfg)
	if err != nil {
		t.Fatal(err)
	}
	vector, err := encoder.EmbedQuery(context.Background(), "  查询 ")
	if err != nil {
		t.Fatal(err)
	}
	if provider.texts[0] != "查询" {
		t.Fatalf("query input = %#v", provider.texts)
	}
	if math.Abs(float64(vector[0])-0.6) > 1e-6 || math.Abs(float64(vector[1])-0.8) > 1e-6 {
		t.Fatalf("query vector = %v", vector)
	}

	provider.vectors = [][]float32{{1, 0}}
	item := memory.Memory{Kind: memory.KindFact, Title: "标题", Content: "正文"}
	document, err := encoder.EmbedDocument(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if provider.texts[1] != "kind: fact\ntitle: 标题\ncontent: 正文" {
		t.Fatalf("document input = %q", provider.texts[1])
	}
	if document.ContentHash != DocumentHash(item) || document.Dimensions != 2 || len(document.Vector) != 8 {
		t.Fatalf("document vector = %+v", document)
	}

	provider.err = &ProviderError{Code: "network", Retryable: true}
	if _, err := encoder.EmbedQuery(context.Background(), "查询"); !IsRetryable(err) || ErrorCode(err) != "network" {
		t.Fatalf("provider error = %v", err)
	}
	provider.err = nil
	provider.vectors = [][]float32{{0, 0}}
	_, err = encoder.EmbedQuery(context.Background(), "查询")
	if ErrorCode(err) != "invalid_vector" {
		t.Fatalf("zero vector error = %v", err)
	}
	if !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("zero vector does not wrap ErrInvalidVector: %v", err)
	}
}
