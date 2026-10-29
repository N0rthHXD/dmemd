package httptransport

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/jobs"
	"github.com/N0rthHXD/dmemd/internal/memory"
	"github.com/N0rthHXD/dmemd/internal/observation"
	"github.com/N0rthHXD/dmemd/internal/retrieval"
	"github.com/N0rthHXD/dmemd/internal/skill"
	"github.com/N0rthHXD/dmemd/internal/source"
)

// The HTTP DTOs mirror the fixed OpenAPI shapes and keep transport concerns out
// of domain packages. All times are encoded in UTC RFC 3339 by encoding/json.

type submitResponse struct {
	SourceID string `json:"source_id"`
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
}

type memoryDTO struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	Title           string    `json:"title"`
	Content         string    `json:"content"`
	State           string    `json:"state"`
	AssertionStatus string    `json:"assertion_status"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	OriginSourceID  string    `json:"origin_source_id,omitempty"`
	ScopeKey        string    `json:"scope_key,omitempty"`
}

type memoryListResponse struct {
	Items []memoryDTO `json:"items"`
}

type relationDTO struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	FromType    string    `json:"from_type"`
	FromID      string    `json:"from_id"`
	FromVersion int       `json:"from_version"`
	ToType      string    `json:"to_type"`
	ToID        string    `json:"to_id"`
	ToVersion   int       `json:"to_version"`
	CreatedAt   time.Time `json:"created_at"`
}

type relationListResponse struct {
	Items []relationDTO `json:"items"`
}

type derivedDependentDTO struct {
	ID        string `json:"id"`
	Layer     string `json:"layer"`
	Kind      string `json:"kind"`
	TargetKey string `json:"target_key"`
	ScopeKey  string `json:"scope_key,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Version   int    `json:"version"`
	State     string `json:"state"`
}

type memoryDetailDTO struct {
	memoryDTO
	ObservationIDs    []string              `json:"observation_ids"`
	Relations         []relationDTO         `json:"relations"`
	ConflictIDs       []string              `json:"conflict_ids"`
	DerivedDependents []derivedDependentDTO `json:"derived_dependents"`
}

type deleteResponse struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type purgeResponse struct {
	ID     string `json:"id"`
	Purged bool   `json:"purged"`
}

type locationDTO struct {
	Field string `json:"field,omitempty"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type evidenceDTO struct {
	ID         string       `json:"id"`
	SourceID   string       `json:"source_id"`
	SegmentID  string       `json:"segment_id,omitempty"`
	MessageID  string       `json:"message_id,omitempty"`
	Quote      string       `json:"quote,omitempty"`
	Location   *locationDTO `json:"location,omitempty"`
	Redacted   bool         `json:"redacted"`
	RedactedAt *time.Time   `json:"redacted_at,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
}

type evidenceListResponse struct {
	Items []evidenceDTO `json:"items"`
}

type revisionDTO struct {
	MemoryID  string        `json:"memory_id"`
	Version   int           `json:"version"`
	Operation string        `json:"operation"`
	Reason    string        `json:"reason,omitempty"`
	Snapshot  memoryDTO     `json:"snapshot"`
	Evidence  []evidenceDTO `json:"evidence"`
	CreatedAt time.Time     `json:"created_at"`
}

type revisionListResponse struct {
	Items []revisionDTO `json:"items"`
}

type evidenceRefDTO struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
}

type keywordExplanationDTO struct {
	Rank int     `json:"rank"`
	BM25 float64 `json:"bm25"`
}

type semanticExplanationDTO struct {
	Rank   int     `json:"rank"`
	Cosine float64 `json:"cosine"`
}

type explanationDTO struct {
	Ranking      string                  `json:"ranking"`
	FieldWeights *[2]int                 `json:"field_weights,omitempty"`
	Keyword      *keywordExplanationDTO  `json:"keyword,omitempty"`
	Semantic     *semanticExplanationDTO `json:"semantic,omitempty"`
}

type conflictRefDTO struct {
	ID         string `json:"id"`
	ReasonCode string `json:"reason_code"`
}

type searchDiagnosticsDTO struct {
	KeywordCandidates      int    `json:"keyword_candidates"`
	ObservationCandidates  int    `json:"observation_candidates"`
	MemoryCandidates       int    `json:"memory_candidates"`
	DerivedCandidates      int    `json:"derived_candidates"`
	SemanticCandidates     int    `json:"semantic_candidates"`
	SemanticExcluded       int    `json:"semantic_excluded"`
	TermCandidates         int    `json:"term_candidates"`
	KindFilterExcluded     int    `json:"kind_filter_excluded"`
	StateFilterExcluded    int    `json:"state_filter_excluded"`
	RankingCandidates      int    `json:"ranking_candidates"`
	ReturnedItems          int    `json:"returned_items"`
	ContextBudgetTruncated int    `json:"context_budget_truncated"`
	SemanticDegradation    string `json:"semantic_degradation,omitempty"`
}

type searchItemDTO struct {
	Memory       memoryDTO        `json:"memory"`
	Score        float64          `json:"score"`
	Snippet      string           `json:"snippet"`
	MatchedTerms []string         `json:"matched_terms"`
	EvidenceRefs []evidenceRefDTO `json:"evidence_refs"`
	Explanation  *explanationDTO  `json:"explanation,omitempty"`
	ConflictRefs []conflictRefDTO `json:"conflict_refs"`
}

type observationDTO struct {
	ID                string    `json:"id"`
	SourceID          string    `json:"source_id"`
	ScopeKey          string    `json:"scope_key,omitempty"`
	Kind              string    `json:"kind"`
	Content           string    `json:"content"`
	ContentMode       string    `json:"content_mode"`
	OriginRole        string    `json:"origin_role,omitempty"`
	Status            string    `json:"status"`
	ExtractionVersion string    `json:"extraction_version,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type observationListResponse struct {
	Items []observationDTO `json:"items"`
}

type recallObservationItemDTO struct {
	Observation  observationDTO   `json:"observation"`
	Score        float64          `json:"score"`
	Snippet      string           `json:"snippet"`
	MatchedTerms []string         `json:"matched_terms"`
	EvidenceRefs []evidenceRefDTO `json:"evidence_refs"`
	Explanation  *explanationDTO  `json:"explanation,omitempty"`
}

type artifactDependencyDTO struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Version  int    `json:"version"`
	Required bool   `json:"required"`
}

type recallDerivedItemDTO struct {
	Artifact     artifactMetaDTO         `json:"artifact"`
	Revision     artifactRevisionDTO     `json:"revision"`
	Score        float64                 `json:"score"`
	Snippet      string                  `json:"snippet"`
	MatchedTerms []string                `json:"matched_terms"`
	Dependencies []artifactDependencyDTO `json:"dependencies"`
	Explanation  *explanationDTO         `json:"explanation,omitempty"`
}

type recallRelationDTO struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	FromType    string    `json:"from_type"`
	FromID      string    `json:"from_id"`
	FromVersion int       `json:"from_version"`
	ToType      string    `json:"to_type"`
	ToID        string    `json:"to_id"`
	ToVersion   int       `json:"to_version"`
	CreatedAt   time.Time `json:"created_at"`
}

type conflictSummaryDTO struct {
	ID          string   `json:"id"`
	ReasonCode  string   `json:"reason_code"`
	MemberIDs   []string `json:"member_ids"`
	HasAccepted bool     `json:"has_accepted"`
}

type retrievalDTO struct {
	Mode string `json:"mode"`
}

type searchResponse struct {
	Items       []searchItemDTO       `json:"items"`
	Retrieval   retrievalDTO          `json:"retrieval"`
	Warnings    []string              `json:"warnings,omitempty"`
	Conflicts   []conflictSummaryDTO  `json:"conflicts"`
	Diagnostics *searchDiagnosticsDTO `json:"diagnostics,omitempty"`
}

type artifactMemoryRefDTO struct {
	MemoryID string `json:"memory_id"`
	Version  int    `json:"version"`
}

type artifactRunRefDTO struct {
	ExperienceID string `json:"experience_id"`
}

type artifactMetaDTO struct {
	ReasonCode     string    `json:"reason_code,omitempty"`
	ID             string    `json:"id"`
	Layer          string    `json:"layer"`
	Kind           string    `json:"kind"`
	TargetKey      string    `json:"target_key"`
	ScopeKey       string    `json:"scope_key,omitempty"`
	Topic          string    `json:"topic,omitempty"`
	CurrentVersion int       `json:"current_version"`
	State          string    `json:"state"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type artifactRevisionDTO struct {
	ArtifactID      string          `json:"artifact_id"`
	Version         int             `json:"version"`
	Title           string          `json:"title"`
	Content         string          `json:"content"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	ContentHash     string          `json:"content_hash"`
	Generator       string          `json:"generator"`
	ProtocolVersion string          `json:"protocol_version"`
	Truncated       bool            `json:"truncated"`
	CreatedAt       time.Time       `json:"created_at"`
}

type artifactDetailResponse struct {
	Artifact     artifactMetaDTO         `json:"artifact"`
	Revision     *artifactRevisionDTO    `json:"revision,omitempty"`
	Dependencies []artifactDependencyDTO `json:"dependencies"`
}

type artifactListResponse struct {
	Items      []artifactMetaDTO `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type artifactTargetJobDTO struct {
	Status              string     `json:"status"`
	WaitReason          string     `json:"wait_reason,omitempty"`
	LastErrorCode       string     `json:"last_error_code,omitempty"`
	RetryReason         string     `json:"retry_reason,omitempty"`
	Attempts            int        `json:"attempts"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	AgeSeconds          int64      `json:"age_seconds"`
	StaleSeconds        int64      `json:"stale_seconds"`
	UpdatedAt           time.Time  `json:"updated_at,omitempty"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
}

type artifactTargetDTO struct {
	Kind             string                `json:"kind"`
	TargetKey        string                `json:"target_key"`
	ScopeKey         string                `json:"scope_key,omitempty"`
	Topic            string                `json:"topic,omitempty"`
	Status           string                `json:"status"`
	ReasonCode       string                `json:"reason_code,omitempty"`
	HasUsableVersion bool                  `json:"has_usable_version"`
	Artifact         *artifactMetaDTO      `json:"artifact,omitempty"`
	Job              *artifactTargetJobDTO `json:"job,omitempty"`
	EvaluatedAt      *time.Time            `json:"evaluated_at,omitempty"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

type artifactTargetListResponse struct {
	Items      []artifactTargetDTO `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type recallResponse struct {
	QueryMode    string                     `json:"query_mode,omitempty"`
	Observations []recallObservationItemDTO `json:"observations"`
	Memories     []searchItemDTO            `json:"memories"`
	L1           []recallDerivedItemDTO     `json:"l1"`
	L2           []recallDerivedItemDTO     `json:"l2"`
	L3           []recallDerivedItemDTO     `json:"l3"`
	L4           []recallDerivedItemDTO     `json:"l4"`
	Relations    []recallRelationDTO        `json:"relations"`
	Conflicts    []conflictSummaryDTO       `json:"conflicts"`
	Warnings     []string                   `json:"warnings"`
	Diagnostics  *searchDiagnosticsDTO      `json:"diagnostics,omitempty"`
}

type experienceReceiptResponse struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"source_id"`
	RunID     string    `json:"run_id,omitempty"`
	Outcome   string    `json:"outcome"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type skillContentResponse struct {
	ID          string                 `json:"id"`
	Version     int                    `json:"version"`
	State       string                 `json:"state"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Content     string                 `json:"content"`
	Truncated   bool                   `json:"truncated"`
	MemoryRefs  []artifactMemoryRefDTO `json:"memory_refs"`
	RunRefs     []artifactRunRefDTO    `json:"run_refs"`
}

type sourceSegmentDTO struct {
	SegmentID  string `json:"segment_id"`
	MessageID  string `json:"message_id,omitempty"`
	Role       string `json:"role,omitempty"`
	OccurredAt string `json:"occurred_at,omitempty"`
	Field      string `json:"field,omitempty"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Text       string `json:"text"`
}

type sourceDTO struct {
	ID         string             `json:"id"`
	AppID      string             `json:"app_id,omitempty"`
	ExternalID string             `json:"external_id,omitempty"`
	ScopeKey   string             `json:"scope_key,omitempty"`
	Content    json.RawMessage    `json:"content,omitempty"`
	Segments   []sourceSegmentDTO `json:"segments"`
	Redacted   bool               `json:"redacted"`
	RedactedAt *time.Time         `json:"redacted_at,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
}

type redactResponse struct {
	ID                    string   `json:"id"`
	Redacted              bool     `json:"redacted"`
	CancelledJobs         int64    `json:"cancelled_jobs"`
	RetractedObservations int      `json:"retracted_observations"`
	InvalidatedDerived    []string `json:"invalidated_derived_ids"`
}

type forgetResponse struct {
	MemoryIDs     []string `json:"memory_ids"`
	SourceIDs     []string `json:"source_ids"`
	ExperienceIDs []string `json:"experience_ids"`
}

type jobDTO struct {
	ID               string                       `json:"id"`
	SourceID         string                       `json:"source_id"`
	Status           string                       `json:"status"`
	Stage            string                       `json:"stage"`
	DiagnosticStage  string                       `json:"diagnostic_stage,omitempty"`
	Attempts         int                          `json:"attempts"`
	ProtocolFailures int                          `json:"protocol_failures"`
	WaitReason       string                       `json:"wait_reason,omitempty"`
	ReasonCode       string                       `json:"reason_code,omitempty"`
	NextAttemptAt    *time.Time                   `json:"next_attempt_at,omitempty"`
	Error            string                       `json:"error,omitempty"`
	ObservationIDs   []string                     `json:"observation_ids"`
	Warnings         []string                     `json:"warnings"`
	QualityOutcome   string                       `json:"quality_outcome,omitempty"`
	ExtractionStats  jobExtractionStatsDTO        `json:"extraction_stats"`
	Timings          jobTimingsDTO                `json:"timings"`
	ModelUsage       jobModelUsageDTO             `json:"model_usage"`
	Diagnostics      []jobExtractionDiagnosticDTO `json:"diagnostics"`
	QueueAgeSeconds  int64                        `json:"queue_age_seconds"`
	StaleSeconds     int64                        `json:"stale_seconds"`
	RetryReason      string                       `json:"retry_reason,omitempty"`
	CreatedAt        time.Time                    `json:"created_at"`
	UpdatedAt        time.Time                    `json:"updated_at"`
	StartedAt        *time.Time                   `json:"started_at,omitempty"`
	FinishedAt       *time.Time                   `json:"finished_at,omitempty"`
}

type jobListResponse struct {
	Items []jobDTO `json:"items"`
}

type governanceJobDTO struct {
	ID                  string     `json:"id"`
	Category            string     `json:"category"`
	Stage               string     `json:"stage"`
	Kind                string     `json:"kind"`
	TargetKey           string     `json:"target_key"`
	Revision            int64      `json:"revision"`
	Status              string     `json:"status"`
	Attempts            int        `json:"attempts"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	NextAttemptAt       *time.Time `json:"next_attempt_at,omitempty"`
	LastErrorCode       string     `json:"last_error_code,omitempty"`
	WaitReason          string     `json:"wait_reason,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
}

type governanceJobListResponse struct {
	Items []governanceJobDTO `json:"items"`
}

type jobExtractionStatsDTO struct {
	Items                  int `json:"items"`
	Observations           int `json:"observations"`
	NormalizedObservations int `json:"normalized_observations"`
	PendingObservations    int `json:"pending_observations"`
	InvalidItems           int `json:"invalid_items"`
	DuplicateObservations  int `json:"duplicate_observations"`
	Chunks                 int `json:"chunks"`
	ChunkFailures          int `json:"chunk_failures"`
}

type jobTimingsDTO struct {
	QueueAgeMillis      int64 `json:"queue_age_ms"`
	ExtractionMillis    int64 `json:"extraction_ms"`
	NormalizationMillis int64 `json:"normalization_ms"`
	CommitMillis        int64 `json:"commit_ms"`
	TotalMillis         int64 `json:"total_ms"`
}

type jobModelUsageDTO struct {
	Calls        int `json:"calls"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type jobExtractionDiagnosticDTO struct {
	Field       string `json:"field,omitempty"`
	Code        string `json:"code"`
	Stage       string `json:"stage"`
	Disposition string `json:"disposition"`
}

type conflictMemberDTO struct {
	MemoryID       string `json:"memory_id"`
	Version        int    `json:"version"`
	CurrentVersion int    `json:"current_version,omitempty"`
}

type conflictEventDTO struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	MemoryID  string    `json:"memory_id,omitempty"`
	Version   int       `json:"version,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type conflictDTO struct {
	ID         string              `json:"id"`
	Status     string              `json:"status"`
	ReasonCode string              `json:"reason_code"`
	Resolution string              `json:"resolution,omitempty"`
	Revision   int                 `json:"revision"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
	Members    []conflictMemberDTO `json:"members"`
	Events     []conflictEventDTO  `json:"events,omitempty"`
}

type conflictListResponse struct {
	Items []conflictDTO `json:"items"`
}

func memoryFromDomain(item memory.Memory) memoryDTO {
	return memoryDTO{
		ID:              item.ID,
		Kind:            string(item.Kind),
		Title:           item.Title,
		Content:         item.Content,
		State:           string(item.State),
		AssertionStatus: string(item.AssertionStatus),
		Version:         item.Version,
		CreatedAt:       item.CreatedAt.UTC(),
		UpdatedAt:       item.UpdatedAt.UTC(),
		OriginSourceID:  item.OriginSourceID,
		ScopeKey:        item.ScopeKey,
	}
}

func relationFromGovernance(item governance.Relation) relationDTO {
	return relationDTO{
		ID:          item.ID,
		Kind:        string(item.Kind),
		FromType:    string(item.FromType),
		FromID:      item.FromID,
		FromVersion: item.FromVersion,
		ToType:      string(item.ToType),
		ToID:        item.ToID,
		ToVersion:   item.ToVersion,
		CreatedAt:   item.CreatedAt.UTC(),
	}
}

func relationsFromGovernance(items []governance.Relation) []relationDTO {
	result := make([]relationDTO, 0, len(items))
	for _, item := range items {
		result = append(result, relationFromGovernance(item))
	}
	return result
}

func relationFromMemoryRef(item memory.RelationRef) relationDTO {
	return relationDTO{
		ID:          item.ID,
		Kind:        item.Kind,
		FromType:    item.FromType,
		FromID:      item.FromID,
		FromVersion: item.FromVersion,
		ToType:      item.ToType,
		ToID:        item.ToID,
		ToVersion:   item.ToVersion,
		CreatedAt:   item.CreatedAt.UTC(),
	}
}

func relationRefsFromDomain(items []memory.RelationRef) []relationDTO {
	result := make([]relationDTO, 0, len(items))
	for _, item := range items {
		result = append(result, relationFromMemoryRef(item))
	}
	return result
}

func derivedDependentsFromDomain(items []memory.DerivedDependent) []derivedDependentDTO {
	result := make([]derivedDependentDTO, 0, len(items))
	for _, item := range items {
		result = append(result, derivedDependentDTO{
			ID: item.ID, Layer: item.Layer, Kind: item.Kind, TargetKey: item.TargetKey,
			ScopeKey: item.ScopeKey, Topic: item.Topic, Version: item.Version, State: item.State,
		})
	}
	return result
}

func memoryDetailFromDomain(item memory.Detail) memoryDetailDTO {
	return memoryDetailDTO{
		memoryDTO:         memoryFromDomain(item.Memory),
		ObservationIDs:    nonNilStrings(item.ObservationIDs),
		Relations:         relationRefsFromDomain(item.Relations),
		ConflictIDs:       nonNilStrings(item.ConflictIDs),
		DerivedDependents: derivedDependentsFromDomain(item.DerivedDependents),
	}
}

func memoriesFromDomain(items []memory.Memory) []memoryDTO {
	result := make([]memoryDTO, 0, len(items))
	for _, item := range items {
		result = append(result, memoryFromDomain(item))
	}
	return result
}

func evidenceFromDomain(item source.Evidence) evidenceDTO {
	result := evidenceDTO{
		ID:        item.ID,
		SourceID:  item.SourceID,
		SegmentID: item.SegmentID,
		MessageID: item.MessageID,
		Quote:     item.Quote,
		Redacted:  item.Redacted,
		CreatedAt: item.CreatedAt.UTC(),
	}
	if item.Location != nil {
		result.Location = &locationDTO{
			Field: item.Location.Field,
			Start: item.Location.Start,
			End:   item.Location.End,
		}
	}
	if item.RedactedAt != nil {
		redactedAt := item.RedactedAt.UTC()
		result.RedactedAt = &redactedAt
	}
	return result
}

func evidenceFromDomainList(items []source.Evidence) []evidenceDTO {
	result := make([]evidenceDTO, 0, len(items))
	for _, item := range items {
		result = append(result, evidenceFromDomain(item))
	}
	return result
}

func revisionFromDomain(item memory.Revision) revisionDTO {
	return revisionDTO{
		MemoryID:  item.MemoryID,
		Version:   item.Version,
		Operation: string(item.Operation),
		Reason:    item.Reason,
		Snapshot:  memoryFromDomain(item.Snapshot),
		Evidence:  evidenceFromDomainList(item.Evidence),
		CreatedAt: item.CreatedAt.UTC(),
	}
}

func revisionsFromDomain(items []memory.Revision) []revisionDTO {
	result := make([]revisionDTO, 0, len(items))
	for _, item := range items {
		result = append(result, revisionFromDomain(item))
	}
	return result
}

func searchResultFromDomain(result retrieval.Result) searchResponse {
	items := make([]searchItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		matchedTerms := item.MatchedTerms
		if matchedTerms == nil {
			matchedTerms = []string{}
		}
		evidenceRefs := make([]evidenceRefDTO, 0, len(item.EvidenceRefs))
		for _, ref := range item.EvidenceRefs {
			evidenceRefs = append(evidenceRefs, evidenceRefDTO{ID: ref.ID, SourceID: ref.SourceID})
		}
		conflictRefs := make([]conflictRefDTO, 0, len(item.ConflictRefs))
		for _, ref := range item.ConflictRefs {
			conflictRefs = append(conflictRefs, conflictRefDTO{ID: ref.ID, ReasonCode: string(ref.ReasonCode)})
		}
		var explanation *explanationDTO
		if item.Explanation != nil {
			explanation = &explanationDTO{
				Ranking: item.Explanation.Ranking,
			}
			if item.Explanation.FieldWeights != [2]int{} {
				weights := item.Explanation.FieldWeights
				explanation.FieldWeights = &weights
			}
			if item.Explanation.Keyword != nil {
				explanation.Keyword = &keywordExplanationDTO{
					Rank: item.Explanation.Keyword.Rank,
					BM25: item.Explanation.Keyword.BM25,
				}
			}
			if item.Explanation.Semantic != nil {
				explanation.Semantic = &semanticExplanationDTO{
					Rank:   item.Explanation.Semantic.Rank,
					Cosine: item.Explanation.Semantic.Cosine,
				}
			}
		}
		items = append(items, searchItemDTO{
			Memory:       memoryFromDomain(item.Memory),
			Score:        item.Score,
			Snippet:      item.Snippet,
			MatchedTerms: matchedTerms,
			EvidenceRefs: evidenceRefs,
			Explanation:  explanation,
			ConflictRefs: conflictRefs,
		})
	}
	mode := string(result.Retrieval.Mode)
	if mode == "" {
		mode = string(retrieval.ModeKeyword)
	}
	warnings := result.Warnings
	if len(warnings) == 0 {
		warnings = nil
	}
	conflicts := make([]conflictSummaryDTO, 0, len(result.Conflicts))
	for _, conflict := range result.Conflicts {
		members := conflict.MemberIDs
		if members == nil {
			members = []string{}
		}
		conflicts = append(conflicts, conflictSummaryDTO{
			ID:          conflict.ID,
			ReasonCode:  string(conflict.ReasonCode),
			MemberIDs:   members,
			HasAccepted: conflict.HasAccepted,
		})
	}
	return searchResponse{
		Items:       items,
		Retrieval:   retrievalDTO{Mode: mode},
		Warnings:    warnings,
		Conflicts:   conflicts,
		Diagnostics: searchDiagnosticsFromDomain(result.Diagnostics),
	}
}

func searchDiagnosticsFromDomain(value *retrieval.SearchDiagnostics) *searchDiagnosticsDTO {
	if value == nil {
		return nil
	}
	return &searchDiagnosticsDTO{
		KeywordCandidates:      value.KeywordCandidates,
		ObservationCandidates:  value.ObservationCandidates,
		MemoryCandidates:       value.MemoryCandidates,
		DerivedCandidates:      value.DerivedCandidates,
		SemanticCandidates:     value.SemanticCandidates,
		SemanticExcluded:       value.SemanticExcluded,
		TermCandidates:         value.TermCandidates,
		KindFilterExcluded:     value.KindFilterExcluded,
		StateFilterExcluded:    value.StateFilterExcluded,
		RankingCandidates:      value.RankingCandidates,
		ReturnedItems:          value.ReturnedItems,
		ContextBudgetTruncated: value.ContextBudgetTruncated,
		SemanticDegradation:    value.SemanticDegradation,
	}
}

func observationFromDomain(item observation.Observation) observationDTO {
	return observationDTO{
		ID:                item.ID,
		SourceID:          item.SourceID,
		ScopeKey:          item.ScopeKey,
		Kind:              string(item.Kind),
		Content:           item.Content,
		ContentMode:       string(item.ContentMode),
		OriginRole:        item.OriginRole,
		Status:            string(item.Status),
		ExtractionVersion: item.ExtractionVersion,
		CreatedAt:         item.CreatedAt.UTC(),
		UpdatedAt:         item.UpdatedAt.UTC(),
	}
}

func recallObservationsFromDomain(items []retrieval.ObservationItem) []recallObservationItemDTO {
	result := make([]recallObservationItemDTO, 0, len(items))
	for _, item := range items {
		refs := make([]evidenceRefDTO, 0, len(item.EvidenceRefs))
		for _, ref := range item.EvidenceRefs {
			refs = append(refs, evidenceRefDTO{ID: ref.ID, SourceID: ref.SourceID})
		}
		result = append(result, recallObservationItemDTO{
			Observation:  observationFromDomain(item.Observation),
			Score:        item.Score,
			Snippet:      item.Snippet,
			MatchedTerms: nonNilStrings(item.MatchedTerms),
			EvidenceRefs: refs,
			Explanation:  explanationFromDomain(item.Explanation),
		})
	}
	return result
}

func recallDerivedFromDomain(items []retrieval.DerivedItem) []recallDerivedItemDTO {
	result := make([]recallDerivedItemDTO, 0, len(items))
	for _, item := range items {
		dependencies := make([]artifactDependencyDTO, 0, len(item.Dependencies))
		for _, dependency := range item.Dependencies {
			dependencies = append(dependencies, artifactDependencyDTO{
				Type:     string(dependency.Type),
				ID:       dependency.ID,
				Version:  dependency.Version,
				Required: dependency.Required,
			})
		}
		result = append(result, recallDerivedItemDTO{
			Artifact:     artifactMetaFromDomain(item.DerivedMemory),
			Revision:     artifactRevisionFromDomain(item.Revision),
			Score:        item.Score,
			Snippet:      item.Snippet,
			MatchedTerms: nonNilStrings(item.MatchedTerms),
			Dependencies: dependencies,
			Explanation:  explanationFromDomain(item.Explanation),
		})
	}
	return result
}

func recallRelationsFromDomain(items []governance.Relation) []recallRelationDTO {
	result := make([]recallRelationDTO, 0, len(items))
	for _, item := range items {
		result = append(result, recallRelationDTO{
			ID:          item.ID,
			Kind:        string(item.Kind),
			FromType:    string(item.FromType),
			FromID:      item.FromID,
			FromVersion: item.FromVersion,
			ToType:      string(item.ToType),
			ToID:        item.ToID,
			ToVersion:   item.ToVersion,
			CreatedAt:   item.CreatedAt.UTC(),
		})
	}
	return result
}

func explanationFromDomain(item *retrieval.Explanation) *explanationDTO {
	if item == nil {
		return nil
	}
	result := &explanationDTO{Ranking: item.Ranking}
	if item.FieldWeights != [2]int{} {
		weights := item.FieldWeights
		result.FieldWeights = &weights
	}
	if item.Keyword != nil {
		result.Keyword = &keywordExplanationDTO{
			Rank: item.Keyword.Rank,
			BM25: item.Keyword.BM25,
		}
	}
	if item.Semantic != nil {
		result.Semantic = &semanticExplanationDTO{
			Rank:   item.Semantic.Rank,
			Cosine: item.Semantic.Cosine,
		}
	}
	return result
}

func memoryRefsFromDomain(items []artifact.MemoryRef) []artifactMemoryRefDTO {
	result := make([]artifactMemoryRefDTO, 0, len(items))
	for _, item := range items {
		result = append(result, artifactMemoryRefDTO{MemoryID: item.MemoryID, Version: item.Version})
	}
	return result
}

func runRefsFromDomain(items []artifact.RunRef) []artifactRunRefDTO {
	result := make([]artifactRunRefDTO, 0, len(items))
	for _, item := range items {
		result = append(result, artifactRunRefDTO{ExperienceID: item.ExperienceID})
	}
	return result
}

func artifactMetaFromDomain(item artifact.DerivedMemory) artifactMetaDTO {
	return artifactMetaDTO{
		ID:             item.ID,
		Layer:          string(item.Layer),
		Kind:           string(item.Kind),
		TargetKey:      item.TargetKey,
		ScopeKey:       item.ScopeKey,
		Topic:          item.Topic,
		CurrentVersion: item.CurrentVersion,
		State:          string(item.State),
		ReasonCode:     item.ReasonCode,
		UpdatedAt:      item.UpdatedAt.UTC(),
	}
}

func artifactDependenciesFromDomain(items []artifact.DependencyRef) []artifactDependencyDTO {
	result := make([]artifactDependencyDTO, 0, len(items))
	for _, item := range items {
		result = append(result, artifactDependencyDTO{
			Type:     string(item.Type),
			ID:       item.ID,
			Version:  item.Version,
			Required: item.Required,
		})
	}
	return result
}

func meaningfulPayload(payload json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil
	}
	return payload
}

func artifactRevisionFromDomain(item artifact.Revision) artifactRevisionDTO {
	payload := meaningfulPayload(item.Payload)
	return artifactRevisionDTO{
		ArtifactID:      item.ArtifactID,
		Version:         item.Version,
		Title:           item.Title,
		Content:         item.Content,
		Payload:         payload,
		ContentHash:     item.ContentHash,
		Generator:       item.Generator,
		ProtocolVersion: item.ProtocolVersion,
		Truncated:       item.Truncated,
		CreatedAt:       item.CreatedAt.UTC(),
	}
}

func artifactDetailFromDomain(item artifact.Detail) artifactDetailResponse {
	result := artifactDetailResponse{
		Artifact:     artifactMetaFromDomain(item.DerivedMemory),
		Dependencies: artifactDependenciesFromDomain(item.Dependencies),
	}
	if item.Revision != nil {
		revision := artifactRevisionFromDomain(*item.Revision)
		result.Revision = &revision
	}
	return result
}

func artifactListFromDomain(page artifact.ListPage) artifactListResponse {
	items := make([]artifactMetaDTO, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, artifactMetaFromDomain(item))
	}
	return artifactListResponse{Items: items, NextCursor: page.NextCursor}
}

func artifactTargetListFromDomain(page artifact.TargetPage) artifactTargetListResponse {
	items := make([]artifactTargetDTO, 0, len(page.Items))
	for _, record := range page.Items {
		item := artifactTargetDTO{
			Kind:             string(record.Kind),
			TargetKey:        record.TargetKey,
			ScopeKey:         record.ScopeKey,
			Topic:            record.Topic,
			Status:           string(record.Status),
			ReasonCode:       record.ReasonCode,
			HasUsableVersion: record.HasUsableVersion(),
			UpdatedAt:        record.UpdatedAt.UTC(),
		}
		if record.DerivedMemory != nil {
			meta := artifactMetaFromDomain(*record.DerivedMemory)
			item.Artifact = &meta
		}
		if record.Job != nil {
			now := time.Now().UTC()
			retryReason := record.Job.LastErrorCode
			if retryReason == "" {
				retryReason = record.Job.WaitReason
			}
			job := artifactTargetJobDTO{
				Status:              string(record.Job.Status),
				WaitReason:          record.Job.WaitReason,
				LastErrorCode:       record.Job.LastErrorCode,
				RetryReason:         retryReason,
				Attempts:            record.Job.Attempts,
				ConsecutiveFailures: record.Job.ConsecutiveFailures,
				AgeSeconds:          secondsBetween(record.Job.CreatedAt, now),
			}
			if !record.Job.UpdatedAt.IsZero() {
				job.UpdatedAt = record.Job.UpdatedAt.UTC()
				if record.Job.Status == task.StatusQueued || record.Job.Status == task.StatusRunning ||
					record.Job.Status == task.StatusWaiting {
					job.StaleSeconds = secondsBetween(record.Job.UpdatedAt, now)
				}
			}
			job.StartedAt = utcPointer(record.Job.StartedAt)
			job.FinishedAt = utcPointer(record.Job.FinishedAt)
			item.Job = &job
		}
		if record.EvaluatedAt != nil {
			evaluatedAt := record.EvaluatedAt.UTC()
			item.EvaluatedAt = &evaluatedAt
		}
		items = append(items, item)
	}
	return artifactTargetListResponse{Items: items, NextCursor: page.NextCursor}
}

func recallFromDomain(result retrieval.RecallResult) recallResponse {
	atomic := searchResultFromDomain(retrieval.Result{
		Items:       result.Memories,
		Conflicts:   result.Conflicts,
		Warnings:    result.Warnings,
		Diagnostics: result.Diagnostics,
	})
	return recallResponse{
		QueryMode:    string(result.QueryMode),
		Observations: recallObservationsFromDomain(result.Observations),
		Memories:     atomic.Items,
		L1:           recallDerivedFromDomain(result.L1),
		L2:           recallDerivedFromDomain(result.L2),
		L3:           recallDerivedFromDomain(result.L3),
		L4:           recallDerivedFromDomain(result.L4),
		Relations:    recallRelationsFromDomain(result.Relations),
		Conflicts:    atomic.Conflicts,
		Warnings:     nonNilStrings(atomic.Warnings),
		Diagnostics:  atomic.Diagnostics,
	}
}

func sourceFromDomain(item source.Source) sourceDTO {
	result := sourceDTO{
		ID:         item.ID,
		AppID:      item.AppID,
		ExternalID: item.ExternalID,
		ScopeKey:   item.ScopeKey,
		Content:    item.Content,
		Segments:   sourceSegmentsFromDomain(item),
		Redacted:   item.Redacted,
		CreatedAt:  item.CreatedAt.UTC(),
	}
	if item.RedactedAt != nil {
		redactedAt := item.RedactedAt.UTC()
		result.RedactedAt = &redactedAt
	}
	return result
}

func sourceSegmentsFromDomain(item source.Source) []sourceSegmentDTO {
	view := source.BuildView(item.Content)
	result := make([]sourceSegmentDTO, 0, len(view.Segments))
	for _, segment := range view.Segments {
		result = append(result, sourceSegmentDTO{
			SegmentID:  segment.ID,
			MessageID:  segment.MessageID,
			Role:       segment.Role,
			OccurredAt: segment.OccurredAt,
			Field:      segment.Field,
			Start:      segment.Start,
			End:        segment.End,
			Text:       segment.Text,
		})
	}
	return result
}

func redactFromDomain(item source.RedactResult) redactResponse {
	invalidated := item.InvalidatedDerived
	if invalidated == nil {
		invalidated = []string{}
	}
	return redactResponse{
		ID:                    item.SourceID,
		Redacted:              item.Redacted,
		CancelledJobs:         item.CancelledJobs,
		RetractedObservations: item.RetractedObservations,
		InvalidatedDerived:    invalidated,
	}
}

func jobFromDomain(item jobs.ExtractionJob) jobDTO {
	observationIDs := item.ObservationIDs
	if observationIDs == nil {
		observationIDs = []string{}
	}
	diagnostics := make([]jobExtractionDiagnosticDTO, 0, len(item.Diagnostics))
	for _, diagnostic := range item.Diagnostics {
		diagnostics = append(diagnostics, jobExtractionDiagnosticDTO{
			Field:       diagnostic.Field,
			Code:        diagnostic.Code,
			Stage:       diagnostic.Stage,
			Disposition: diagnostic.Disposition,
		})
	}
	now := time.Now().UTC()
	queueAgeSeconds := int64(0)
	if !item.CreatedAt.IsZero() {
		reference := now
		if item.StartedAt != nil {
			reference = item.StartedAt.UTC()
		}
		queueAgeSeconds = secondsBetween(item.CreatedAt, reference)
	}
	staleSeconds := int64(0)
	if item.Status == ingest.JobQueued || item.Status == ingest.JobRunning || item.Status == ingest.JobWaiting {
		staleSeconds = secondsBetween(item.UpdatedAt, now)
	}
	retryReason := item.ReasonCode
	if retryReason == "" {
		retryReason = item.WaitReason
	}
	result := jobDTO{
		ID:               item.ID,
		SourceID:         item.SourceID,
		Status:           string(item.Status),
		Stage:            string(item.Stage),
		DiagnosticStage:  item.Job.Stage,
		Attempts:         item.Attempts,
		ProtocolFailures: item.ProtocolFailures,
		WaitReason:       item.WaitReason,
		ReasonCode:       item.ReasonCode,
		RetryReason:      retryReason,
		NextAttemptAt:    utcPointer(item.NextAttemptAt),
		Error:            item.Error,
		ObservationIDs:   observationIDs,
		Warnings:         nonNilStrings(item.Warnings),
		QualityOutcome:   string(item.QualityOutcome),
		ExtractionStats: jobExtractionStatsDTO{
			Items:                  item.ExtractionStats.Items,
			Observations:           item.ExtractionStats.Observations,
			NormalizedObservations: item.ExtractionStats.NormalizedObservations,
			PendingObservations:    item.ExtractionStats.PendingObservations,
			InvalidItems:           item.ExtractionStats.InvalidItems,
			DuplicateObservations:  item.ExtractionStats.DuplicateObservations,
			Chunks:                 item.ExtractionStats.Chunks,
			ChunkFailures:          item.ExtractionStats.ChunkFailures,
		},
		Timings: jobTimingsDTO{
			QueueAgeMillis:      item.Timings.QueueAgeMillis,
			ExtractionMillis:    item.Timings.ExtractionMillis,
			NormalizationMillis: item.Timings.NormalizationMillis,
			CommitMillis:        item.Timings.CommitMillis,
			TotalMillis:         item.Timings.TotalMillis,
		},
		ModelUsage: jobModelUsageDTO{
			Calls: item.ModelUsage.Calls, InputTokens: item.ModelUsage.InputTokens,
			OutputTokens: item.ModelUsage.OutputTokens, TotalTokens: item.ModelUsage.TotalTokens(),
		},
		Diagnostics:     diagnostics,
		QueueAgeSeconds: queueAgeSeconds,
		StaleSeconds:    staleSeconds,
		CreatedAt:       item.CreatedAt.UTC(),
		UpdatedAt:       item.UpdatedAt.UTC(),
		StartedAt:       utcPointer(item.StartedAt),
		FinishedAt:      utcPointer(item.FinishedAt),
	}
	return result
}

func jobListFromDomain(items []jobs.ExtractionJob) jobListResponse {
	result := make([]jobDTO, 0, len(items))
	for _, item := range items {
		result = append(result, jobFromDomain(item))
	}
	return jobListResponse{Items: result}
}

func governanceJobFromDomain(item jobs.TaskJob) governanceJobDTO {
	return governanceJobDTO{
		ID:                  item.ID,
		Category:            string(item.Category),
		Stage:               string(item.Stage),
		Kind:                string(item.Kind),
		TargetKey:           item.TargetKey,
		Revision:            item.Revision,
		Status:              string(item.Status),
		Attempts:            item.Attempts,
		ConsecutiveFailures: item.ConsecutiveFailures,
		NextAttemptAt:       utcPointer(item.NextAttemptAt),
		LastErrorCode:       item.LastErrorCode,
		WaitReason:          item.WaitReason,
		CreatedAt:           item.CreatedAt.UTC(),
		UpdatedAt:           item.UpdatedAt.UTC(),
		StartedAt:           utcPointer(item.StartedAt),
		FinishedAt:          utcPointer(item.FinishedAt),
	}
}

func governanceJobListFromDomain(items []jobs.TaskJob) governanceJobListResponse {
	result := make([]governanceJobDTO, 0, len(items))
	for _, item := range items {
		result = append(result, governanceJobFromDomain(item))
	}
	return governanceJobListResponse{Items: result}
}

func secondsBetween(from, to time.Time) int64 {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return 0
	}
	return int64(to.Sub(from) / time.Second)
}

func conflictFromDomain(item memory.Conflict) conflictDTO {
	members := make([]conflictMemberDTO, 0, len(item.Members))
	for _, member := range item.Members {
		members = append(members, conflictMemberDTO{
			MemoryID: member.MemoryID, Version: member.Version, CurrentVersion: member.CurrentVersion,
		})
	}
	events := make([]conflictEventDTO, 0, len(item.Events))
	for _, event := range item.Events {
		events = append(events, conflictEventDTO{
			ID:        event.ID,
			Action:    event.Action,
			MemoryID:  event.MemoryID,
			Version:   event.Version,
			CreatedAt: event.CreatedAt.UTC(),
		})
	}
	return conflictDTO{
		ID:         item.ID,
		Status:     string(item.Status),
		ReasonCode: string(item.ReasonCode),
		Resolution: item.Resolution,
		Revision:   item.Revision,
		CreatedAt:  item.CreatedAt.UTC(),
		UpdatedAt:  item.UpdatedAt.UTC(),
		Members:    members,
		Events:     events,
	}
}

func conflictsFromDomain(items []memory.Conflict) []conflictDTO {
	result := make([]conflictDTO, 0, len(items))
	for _, item := range items {
		result = append(result, conflictFromDomain(item))
	}
	return result
}

func experienceReceiptFromDomain(item governance.ExperienceReceipt) experienceReceiptResponse {
	return experienceReceiptResponse{
		ID:        item.ID,
		SourceID:  item.SourceID,
		RunID:     item.RunID,
		Outcome:   string(item.Outcome),
		State:     string(item.State),
		CreatedAt: item.CreatedAt.UTC(),
		UpdatedAt: item.UpdatedAt.UTC(),
	}
}

func skillContentFromDomain(item skill.Content) skillContentResponse {
	return skillContentResponse{
		ID:          item.ArtifactID,
		Version:     item.Version,
		State:       item.State,
		Name:        item.Name,
		Description: item.Description,
		Content:     item.Content,
		Truncated:   item.Truncated,
		MemoryRefs:  memoryRefsFromDomain(item.MemoryRefs),
		RunRefs:     runRefsFromDomain(item.RunRefs),
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
