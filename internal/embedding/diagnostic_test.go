package embedding

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStatsReader struct {
	stats Stats
	err   error
	calls int
}

func (f *fakeStatsReader) EmbeddingStats(context.Context, string, int) (Stats, error) {
	f.calls++
	return f.stats, f.err
}

type fakeProbeEncoder struct {
	profileID  string
	dimensions int
	vector     []float32
	err        error
	calls      int
}

func (f *fakeProbeEncoder) ProfileID() string      { return f.profileID }
func (f *fakeProbeEncoder) Dimensions() int        { return f.dimensions }
func (f *fakeProbeEncoder) MinSimilarity() float64 { return 0.4 }
func (f *fakeProbeEncoder) EmbedQuery(context.Context, string) ([]float32, error) {
	f.calls++
	return f.vector, f.err
}

func TestDiagnosticStatusCoverageAndDegradation(t *testing.T) {
	reader := &fakeStatsReader{stats: Stats{
		Documents: 4, CurrentVectors: 2, StaleVectors: 1, MissingVectors: 1,
		PendingJobs: 2, FailedJobs: 1,
		FailedCodes:         map[string]int{"http_429": 2, "unauthorized": 1},
		IndexedProfiles:     map[string]int{"old": 1, "current": 2},
		RegisteredProfileID: "current", RegisteredIndexVersion: IndexVersion,
		RebuildRequired: true, RebuildReason: "vectors_stale",
	}}
	encoder := &fakeProbeEncoder{profileID: "current", dimensions: 3, vector: []float32{1, 0, 0}}
	cfg := Config{Enabled: true, BaseURL: "https://user:secret@example.test/v1?token=secret", Model: "m", Dimensions: 3}
	service := NewDiagnosticService(reader, encoder, cfg)
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Endpoint != "https://example.test/v1" {
		t.Fatalf("endpoint leaked credentials or query: %q", status.Endpoint)
	}
	if !status.Enabled || status.ProfileID != "current" || !status.ConfigFingerprintMatch {
		t.Fatalf("status identity = %+v", status)
	}
	if status.Documents != 4 || status.CurrentVectors != 2 || status.Coverage != 0.5 {
		t.Fatalf("status coverage = %+v", status)
	}
	if status.Ready || status.ReasonCode != StatusBacklog || status.RebuildReason != "vectors_stale" {
		t.Fatalf("status readiness = %+v", status)
	}
	if len(status.FailedCodes) != 2 || status.FailedCodes[0].Code != "http_429" || status.FailedCodes[0].Count != 2 {
		t.Fatalf("failed codes = %+v", status.FailedCodes)
	}
	if len(status.IndexedProfiles) != 2 || status.IndexedProfiles[0].ProfileID != "current" {
		t.Fatalf("profiles = %+v", status.IndexedProfiles)
	}

	reader.stats.RegisteredProfileID = "other"
	status, err = service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.ConfigFingerprintMatch || status.Ready || status.ReasonCode != StatusConfigMismatch {
		t.Fatalf("mismatched status = %+v", status)
	}
}

func TestDiagnosticStatusDistinguishesEmptyIndexAndNoDocuments(t *testing.T) {
	encoder := &fakeProbeEncoder{profileID: "current", dimensions: 3}
	cfg := Config{Enabled: true, BaseURL: "https://example.test", Model: "m", Dimensions: 3}

	reader := &fakeStatsReader{stats: Stats{
		Documents: 2, CurrentVectors: 0, MissingVectors: 2,
		RegisteredProfileID: "current", RegisteredIndexVersion: IndexVersion,
	}}
	service := NewDiagnosticService(reader, encoder, cfg)
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || status.ReasonCode != StatusIndexNotReady {
		t.Fatalf("empty index status = %+v", status)
	}

	reader.stats = Stats{RegisteredProfileID: "current", RegisteredIndexVersion: IndexVersion}
	status, err = service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.ReasonCode != StatusNoDocuments {
		t.Fatalf("no-document status = %+v", status)
	}
}

func TestDiagnosticDisabledAndProbeCodes(t *testing.T) {
	disabled := NewDiagnosticService(nil, nil, Config{})
	status, err := disabled.Status(context.Background())
	if err != nil || status.Enabled || status.ReasonCode != StatusDisabled {
		t.Fatalf("disabled status = %+v, %v", status, err)
	}
	probe := disabled.Probe(context.Background())
	if probe.Attempted || probe.Success || probe.Code != StatusDisabled {
		t.Fatalf("disabled probe = %+v", probe)
	}

	encoder := &fakeProbeEncoder{profileID: "current", dimensions: 3, vector: []float32{1, 0, 0}}
	cfg := Config{Enabled: true, BaseURL: "https://example.test", Model: "m", Dimensions: 3}
	service := NewDiagnosticService(&fakeStatsReader{}, encoder, cfg)
	probe = service.Probe(context.Background())
	if !probe.Attempted || !probe.Success || probe.Code != "ok" || probe.Dimensions != 3 {
		t.Fatalf("successful probe = %+v", probe)
	}

	for _, test := range []struct {
		err  error
		code string
	}{
		{NewProviderError("unauthorized", false, nil), "unauthorized"},
		{NewProviderError("http_429", true, nil), "rate_limited"},
		{NewProviderError("timeout", true, nil), "timeout"},
		{NewProviderError("invalid_vector", false, nil), "invalid_response"},
		{NewProviderError("http_5xx", true, nil), "unavailable"},
		{errors.New("unexpected"), "internal"},
	} {
		encoder.err = test.err
		probe = service.Probe(context.Background())
		if probe.Success || probe.Code != test.code {
			t.Fatalf("probe error %v = %+v", test.err, probe)
		}
	}
	encoder.err = nil
	encoder.vector = []float32{1, 0}
	probe = service.Probe(context.Background())
	if probe.Success || probe.Code != "invalid_response" {
		t.Fatalf("wrong-dimension probe = %+v", probe)
	}
}

func TestDiagnosticProbeHonorsDeadline(t *testing.T) {
	encoder := &fakeProbeEncoder{profileID: "current", dimensions: 3}
	encoder.err = context.DeadlineExceeded
	service := NewDiagnosticService(&fakeStatsReader{}, encoder, Config{
		Enabled: true, BaseURL: "https://example.test", Model: "m", Dimensions: 3,
	})
	service.timeout = time.Millisecond
	probe := service.Probe(context.Background())
	if probe.Success || probe.Code != "timeout" {
		t.Fatalf("deadline probe = %+v", probe)
	}
}
