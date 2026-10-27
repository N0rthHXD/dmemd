package embedding

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultProbeTimeout bounds one explicit connection/response legality
	// check. It never participates in online recall.
	DefaultProbeTimeout = 5 * time.Second

	// Status reason codes are stable and expose no provider payload.
	StatusReady          = "ready"
	StatusDisabled       = "disabled"
	StatusNoDocuments    = "no_documents"
	StatusConfigMismatch = "config_mismatch"
	StatusIndexNotReady  = "index_not_ready"
	StatusBacklog        = "backlog"
	StatusFailedJobs     = "failed_jobs"
)

// Registration reports how one startup configuration relates to the last
// persisted Embedding profile. A changed profile or document contract registers
// a durable rebuild reason before any vector is deleted or regenerated.
type Registration struct {
	ProfileChanged      bool
	IndexVersionChanged bool
	RegisteredAt        time.Time
}

// Stats is the durable embedding-index inventory used by status reporting. It
// contains counts, profile fingerprints and sanitized error codes only.
type Stats struct {
	Documents              int
	CurrentVectors         int
	StaleVectors           int
	MissingVectors         int
	PendingJobs            int
	FailedJobs             int
	FailedCodes            map[string]int
	IndexedProfiles        map[string]int
	RegisteredProfileID    string
	RegisteredIndexVersion string
	RebuildRequired        bool
	RebuildReason          string
	RebuildRegisteredAt    *time.Time
}

// StatsReader is implemented by the persistence layer.
type StatsReader interface {
	EmbeddingStats(ctx context.Context, profileID string, dimensions int) (Stats, error)
}

// ProbeEncoder is the online query capability used by an explicit probe. It is
// the same encoder as recall, so success proves the configured profile and
// response shape, not merely that a TCP port is open.
type ProbeEncoder interface {
	ProfileID() string
	Dimensions() int
	MinSimilarity() float64
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// ProfileCount is one persisted vector profile and its current row count.
type ProfileCount struct {
	ProfileID string `json:"profile_id"`
	Vectors   int    `json:"vectors"`
}

// ErrorCount is one sanitized embedding task error.
type ErrorCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// Status is the explicit Embedding configuration, coverage and degradation
// view. It never contains an API key, source text, query text, vector values or
// raw provider response.
type Status struct {
	Enabled                bool           `json:"enabled"`
	Model                  string         `json:"model,omitempty"`
	Endpoint               string         `json:"endpoint,omitempty"`
	Dimensions             int            `json:"dimensions,omitempty"`
	MinSimilarity          float64        `json:"min_similarity"`
	ProfileID              string         `json:"profile_id,omitempty"`
	IndexVersion           string         `json:"index_version"`
	RegisteredProfileID    string         `json:"registered_profile_id,omitempty"`
	RegisteredIndexVersion string         `json:"registered_index_version,omitempty"`
	ConfigFingerprintMatch bool           `json:"config_fingerprint_match"`
	RebuildRequired        bool           `json:"rebuild_required"`
	RebuildReason          string         `json:"rebuild_reason,omitempty"`
	RebuildRegisteredAt    *time.Time     `json:"rebuild_registered_at,omitempty"`
	Documents              int            `json:"documents"`
	CurrentVectors         int            `json:"current_vectors"`
	StaleVectors           int            `json:"stale_vectors"`
	MissingVectors         int            `json:"missing_vectors"`
	PendingJobs            int            `json:"pending_jobs"`
	FailedJobs             int            `json:"failed_jobs"`
	FailedCodes            []ErrorCount   `json:"failed_codes"`
	IndexedProfiles        []ProfileCount `json:"indexed_profiles"`
	Coverage               float64        `json:"coverage"`
	Ready                  bool           `json:"ready"`
	ReasonCode             string         `json:"reason_code"`
	CheckedAt              time.Time      `json:"checked_at"`
}

// Probe is one explicit connection and response-shape check. Attempted=false
// means the feature is disabled and no model call was made.
type Probe struct {
	Attempted  bool      `json:"attempted"`
	Success    bool      `json:"success"`
	Code       string    `json:"code"`
	Dimensions int       `json:"dimensions,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	CheckedAt  time.Time `json:"checked_at"`
}

// DiagnosticAPI is the read-only diagnostics capability exposed over HTTP.
type DiagnosticAPI interface {
	Status(ctx context.Context) (Status, error)
	Probe(ctx context.Context) Probe
}

// DiagnosticService assembles status and probe responses from the same encoder
// and persistence layer used by recall and the background worker.
type DiagnosticService struct {
	reader     StatsReader
	encoder    ProbeEncoder
	config     Config
	profile    Profile
	hasProfile bool
	timeout    time.Duration
	now        func() time.Time
}

// NewDiagnosticService builds the read-only diagnostics capability. A nil
// reader or encoder is valid and produces an explicitly unavailable view.
func NewDiagnosticService(reader StatsReader, encoder ProbeEncoder, cfg Config) *DiagnosticService {
	cfg = cfg.WithDefaults()
	if encoder != nil {
		value := reflect.ValueOf(encoder)
		if value.Kind() == reflect.Ptr && value.IsNil() {
			encoder = nil
		}
	}
	service := &DiagnosticService{
		reader:  reader,
		encoder: encoder,
		config:  cfg,
		timeout: DefaultProbeTimeout,
		now:     time.Now,
	}
	if encoder != nil {
		service.profile = Profile{ID: encoder.ProfileID(), Dimensions: encoder.Dimensions()}
		service.hasProfile = service.profile.ID != ""
	}
	return service
}

// Status returns the current coverage and degradation state. It performs no
// model call and never changes recall or durable state.
func (s *DiagnosticService) Status(ctx context.Context) (Status, error) {
	now := s.now().UTC()
	status := Status{
		Enabled:         s.config.Enabled && s.encoder != nil,
		Model:           strings.TrimSpace(s.config.Model),
		Dimensions:      s.config.Dimensions,
		MinSimilarity:   s.config.MinSimilarity,
		IndexVersion:    IndexVersion,
		FailedCodes:     []ErrorCount{},
		IndexedProfiles: []ProfileCount{},
		CheckedAt:       now,
	}
	if endpoint, err := endpointIdentity(s.config.BaseURL); err == nil {
		status.Endpoint = endpoint
	}
	if s.hasProfile {
		status.ProfileID = s.profile.ID
		status.Dimensions = s.profile.Dimensions
		status.MinSimilarity = s.encoder.MinSimilarity()
	}
	if !status.Enabled {
		status.ReasonCode = StatusDisabled
		return status, nil
	}
	if s.reader == nil {
		status.ReasonCode = StatusIndexNotReady
		return status, nil
	}
	stats, err := s.reader.EmbeddingStats(ctx, status.ProfileID, status.Dimensions)
	if err != nil {
		return Status{}, fmt.Errorf("读取 Embedding 状态: %w", err)
	}
	status.Documents = stats.Documents
	status.CurrentVectors = stats.CurrentVectors
	status.StaleVectors = stats.StaleVectors
	status.MissingVectors = stats.MissingVectors
	status.PendingJobs = stats.PendingJobs
	status.FailedJobs = stats.FailedJobs
	status.RegisteredProfileID = stats.RegisteredProfileID
	status.RegisteredIndexVersion = stats.RegisteredIndexVersion
	status.ConfigFingerprintMatch = stats.RegisteredProfileID != "" &&
		stats.RegisteredProfileID == status.ProfileID &&
		stats.RegisteredIndexVersion == IndexVersion
	status.RebuildRequired = stats.RebuildRequired
	status.RebuildReason = stats.RebuildReason
	status.RebuildRegisteredAt = stats.RebuildRegisteredAt
	status.FailedCodes = sortedErrorCounts(stats.FailedCodes)
	status.IndexedProfiles = sortedProfileCounts(stats.IndexedProfiles)
	if stats.Documents > 0 {
		status.Coverage = float64(stats.CurrentVectors) / float64(stats.Documents)
		if status.Coverage > 1 {
			status.Coverage = 1
		}
	}
	status.Ready, status.ReasonCode = embeddingReadiness(status, stats)
	return status, nil
}

func embeddingReadiness(status Status, stats Stats) (bool, string) {
	if !status.ConfigFingerprintMatch {
		return false, StatusConfigMismatch
	}
	if status.StaleVectors > 0 || status.MissingVectors > 0 || status.PendingJobs > 0 {
		if status.CurrentVectors == 0 {
			return false, StatusIndexNotReady
		}
		return false, StatusBacklog
	}
	if status.FailedJobs > 0 {
		return false, StatusFailedJobs
	}
	if status.Documents == 0 {
		return true, StatusNoDocuments
	}
	if status.CurrentVectors == 0 {
		return false, StatusIndexNotReady
	}
	return true, StatusReady
}

func sortedErrorCounts(values map[string]int) []ErrorCount {
	result := make([]ErrorCount, 0, len(values))
	for code, count := range values {
		result = append(result, ErrorCount{Code: code, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Code < result[j].Code
	})
	return result
}

func sortedProfileCounts(values map[string]int) []ProfileCount {
	result := make([]ProfileCount, 0, len(values))
	for profileID, count := range values {
		result = append(result, ProfileCount{ProfileID: profileID, Vectors: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Vectors != result[j].Vectors {
			return result[i].Vectors > result[j].Vectors
		}
		return result[i].ProfileID < result[j].ProfileID
	})
	return result
}

// Probe makes one explicit, bounded model call and classifies the result into a
// stable code. It is never used by online recall.
func (s *DiagnosticService) Probe(ctx context.Context) Probe {
	now := s.now().UTC()
	result := Probe{Code: StatusDisabled, CheckedAt: now, Dimensions: s.profile.Dimensions}
	if !s.config.Enabled || s.encoder == nil {
		return result
	}
	result.Attempted = true
	probeCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	started := time.Now()
	vector, err := s.encoder.EmbedQuery(probeCtx, "dmemd embedding diagnostic probe")
	result.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Code = probeCode(err)
		return result
	}
	if len(vector) != s.encoder.Dimensions() {
		result.Code = "invalid_response"
		return result
	}
	result.Success = true
	result.Code = "ok"
	return result
}

func probeCode(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	switch ErrorCode(err) {
	case "unauthorized":
		return "unauthorized"
	case "http_400", "http_404", "configuration", "empty_query":
		return "config_error"
	case "http_429":
		return "rate_limited"
	case "timeout":
		return "timeout"
	case "cancelled":
		return "cancelled"
	case "dimensions_mismatch", "invalid_vector", "protocol", "count_mismatch",
		"order_mismatch", "response_too_large", "empty_input":
		return "invalid_response"
	case "network", "http_5xx":
		return "unavailable"
	default:
		return ErrorCode(err)
	}
}
