package httptransport

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
	"github.com/N0rthHXD/dmemd/internal/governance/task"
	"github.com/N0rthHXD/dmemd/internal/ingest"
	"github.com/N0rthHXD/dmemd/internal/memory"
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

type temporalDetailDTO struct {
	Text          string `json:"text,omitempty"`
	Precision     string `json:"precision,omitempty"`
	TimezoneKnown bool   `json:"timezone_known"`
}

type temporalDetailsDTO struct {
	Observed  temporalDetailDTO `json:"observed"`
	Event     temporalDetailDTO `json:"event"`
	ValidFrom temporalDetailDTO `json:"valid_from"`
	ValidTo   temporalDetailDTO `json:"valid_to"`
}

type quantityDTO struct {
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

type qualifiersDTO struct {
	Entities    []string     `json:"entities,omitempty"`
	Quantity    *quantityDTO `json:"quantity,omitempty"`
	Conditions  []string     `json:"conditions,omitempty"`
	Polarity    string       `json:"polarity,omitempty"`
	Certainty   string       `json:"certainty,omitempty"`
	Attribution string       `json:"attribution,omitempty"`
}

type procedureDTO struct {
	Goal                 string   `json:"goal,omitempty"`
	Steps                []string `json:"steps,omitempty"`
	Preconditions        []string `json:"preconditions,omitempty"`
	CompletionConditions []string `json:"completion_conditions,omitempty"`
	FailureBranches      []string `json:"failure_branches,omitempty"`
}

type memoryDTO struct {
	ID                 string             `json:"id"`
	Kind               string             `json:"kind"`
	Phase              string             `json:"phase"`
	Durability         string             `json:"durability"`
	Title              string             `json:"title"`
	Content            string             `json:"content"`
	State              string             `json:"state"`
	AssertionStatus    string             `json:"assertion_status"`
	Version            int                `json:"version"`
	ObservedAt         time.Time          `json:"observed_at"`
	ObservedAtBasis    string             `json:"observed_at_basis"`
	EventTime          *time.Time         `json:"event_time"`
	EventTimeBasis     string             `json:"event_time_basis"`
	EventTimePrecision string             `json:"event_time_precision"`
	ValidFrom          *time.Time         `json:"valid_from"`
	ValidTo            *time.Time         `json:"valid_to"`
	ValidFromBasis     string             `json:"valid_from_basis"`
	ValidToBasis       string             `json:"valid_to_basis"`
	Temporal           temporalDetailsDTO `json:"temporal"`
	FactGroupID        string             `json:"fact_group_id,omitempty"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
	CreatedByApp       string             `json:"created_by_app,omitempty"`
	OriginSourceID     string             `json:"origin_source_id,omitempty"`
	ScopeKey           string             `json:"scope_key,omitempty"`
	Procedure          *procedureDTO      `json:"procedure,omitempty"`
	Qualifiers         *qualifiersDTO     `json:"qualifiers,omitempty"`
	Topics             []string           `json:"topics"`
	RawTopics          []string           `json:"raw_topics"`
	RetentionReason    string             `json:"retention_reason"`
}

type memoryListResponse struct {
	Items []memoryDTO `json:"items"`
}

type relationDTO struct {
	ID                 string    `json:"id"`
	FromMemoryID       string    `json:"from_memory_id"`
	FromVersion        int       `json:"from_version"`
	ToMemoryID         string    `json:"to_memory_id"`
	ToVersion          int       `json:"to_version"`
	Kind               string    `json:"kind"`
	CreatedAt          time.Time `json:"created_at"`
	FromVersionChanged bool      `json:"from_version_changed"`
	ToVersionChanged   bool      `json:"to_version_changed"`
}

type evolutionDTO struct {
	ID           string    `json:"id"`
	FactGroupID  string    `json:"fact_group_id,omitempty"`
	FromMemoryID string    `json:"from_memory_id"`
	FromVersion  int       `json:"from_version"`
	ToMemoryID   string    `json:"to_memory_id"`
	ToVersion    int       `json:"to_version"`
	Action       string    `json:"action"`
	ReasonCode   string    `json:"reason_code,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type memoryDetailDTO struct {
	memoryDTO
	Relations []relationDTO  `json:"relations"`
	Evolution []evolutionDTO `json:"evolution"`
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
	MessageID  string       `json:"message_id,omitempty"`
	Quote      string       `json:"quote,omitempty"`
	Location   *locationDTO `json:"location,omitempty"`
	ModelName  string       `json:"model_name,omitempty"`
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

type relatedDTO struct {
	ReasonCodes     []string `json:"reason_codes"`
	AnchorMemoryIDs []string `json:"anchor_memory_ids"`
}

type displayClusterDTO struct {
	FactGroupID   string   `json:"fact_group_id,omitempty"`
	MemberIDs     []string `json:"member_ids"`
	EvidenceCount int      `json:"evidence_count"`
}

type searchDiagnosticsDTO struct {
	KeywordCandidates      int    `json:"keyword_candidates"`
	SemanticCandidates     int    `json:"semantic_candidates"`
	SemanticExcluded       int    `json:"semantic_excluded"`
	TermCandidates         int    `json:"term_candidates"`
	KindFilterExcluded     int    `json:"kind_filter_excluded"`
	StateFilterExcluded    int    `json:"state_filter_excluded"`
	TimeFilterExcluded     int    `json:"time_filter_excluded"`
	RankingCandidates      int    `json:"ranking_candidates"`
	RelatedExpanded        int    `json:"related_expanded"`
	RelatedExcluded        int    `json:"related_excluded"`
	ReturnedItems          int    `json:"returned_items"`
	ContextBudgetTruncated int    `json:"context_budget_truncated"`
	SemanticDegradation    string `json:"semantic_degradation,omitempty"`
}

type searchItemDTO struct {
	Memory         memoryDTO          `json:"memory"`
	Score          float64            `json:"score"`
	Snippet        string             `json:"snippet"`
	MatchedTerms   []string           `json:"matched_terms"`
	EvidenceRefs   []evidenceRefDTO   `json:"evidence_refs"`
	Explanation    *explanationDTO    `json:"explanation,omitempty"`
	TemporalStatus string             `json:"temporal_status"`
	ConflictRefs   []conflictRefDTO   `json:"conflict_refs"`
	Related        *relatedDTO        `json:"related,omitempty"`
	Cluster        *displayClusterDTO `json:"cluster,omitempty"`
}

type conflictSummaryDTO struct {
	ID          string   `json:"id"`
	ReasonCode  string   `json:"reason_code"`
	MemberIDs   []string `json:"member_ids"`
	HasAccepted bool     `json:"has_accepted"`
	FactGroupID string   `json:"fact_group_id,omitempty"`
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

type artifactItemDTO struct {
	ID         string                 `json:"id"`
	Kind       string                 `json:"kind"`
	Version    int                    `json:"version"`
	Title      string                 `json:"title"`
	Content    string                 `json:"content"`
	Payload    json.RawMessage        `json:"payload,omitempty"`
	ScopeKey   string                 `json:"scope_key,omitempty"`
	Topic      string                 `json:"topic,omitempty"`
	State      string                 `json:"state"`
	Truncated  bool                   `json:"truncated"`
	MemoryRefs []artifactMemoryRefDTO `json:"memory_refs"`
	RunRefs    []artifactRunRefDTO    `json:"run_refs"`
	UpdatedAt  time.Time              `json:"updated_at"`
	Score      float64                `json:"score,omitempty"`
}

type artifactMetaDTO struct {
	ReasonCode     string    `json:"reason_code,omitempty"`
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	TargetKey      string    `json:"target_key"`
	ScopeKey       string    `json:"scope_key,omitempty"`
	Topic          string    `json:"topic,omitempty"`
	CurrentVersion int       `json:"current_version"`
	State          string    `json:"state"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type artifactRevisionDTO struct {
	TaxonomyVersion  string                 `json:"taxonomy_version"`
	GeneratorVersion string                 `json:"generator_version"`
	RunID            string                 `json:"run_id"`
	Provenance       json.RawMessage        `json:"provenance"`
	ArtifactID       string                 `json:"artifact_id"`
	Version          int                    `json:"version"`
	Title            string                 `json:"title"`
	Content          string                 `json:"content"`
	Payload          json.RawMessage        `json:"payload,omitempty"`
	ContentHash      string                 `json:"content_hash"`
	Generator        string                 `json:"generator"`
	ProtocolVersion  string                 `json:"protocol_version"`
	ReasonCode       string                 `json:"reason_code,omitempty"`
	Truncated        bool                   `json:"truncated"`
	MemoryRefs       []artifactMemoryRefDTO `json:"memory_refs"`
	RunRefs          []artifactRunRefDTO    `json:"run_refs"`
	CreatedAt        time.Time              `json:"created_at"`
}

type artifactDetailResponse struct {
	Artifact artifactMetaDTO      `json:"artifact"`
	Revision *artifactRevisionDTO `json:"revision,omitempty"`
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
	Memories    []searchItemDTO       `json:"memories"`
	Summaries   []artifactItemDTO     `json:"summaries"`
	Experiences []artifactItemDTO     `json:"experiences"`
	Skills      []artifactItemDTO     `json:"skills"`
	Conflicts   []conflictSummaryDTO  `json:"conflicts"`
	Warnings    []string              `json:"warnings"`
	Diagnostics *searchDiagnosticsDTO `json:"diagnostics,omitempty"`
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

type sourceDTO struct {
	ID         string          `json:"id"`
	AppID      string          `json:"app_id,omitempty"`
	ExternalID string          `json:"external_id,omitempty"`
	ScopeKey   string          `json:"scope_key,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Redacted   bool            `json:"redacted"`
	RedactedAt *time.Time      `json:"redacted_at,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type redactResponse struct {
	ID            string `json:"id"`
	Redacted      bool   `json:"redacted"`
	CancelledJobs int64  `json:"cancelled_jobs"`
}

type forgetResponse struct {
	MemoryIDs                []string `json:"memory_ids"`
	SourceIDs                []string `json:"source_ids"`
	ExperienceIDs            []string `json:"experience_ids"`
	Warnings                 []string `json:"warnings,omitempty"`
	RetainedRelatedMemoryIDs []string `json:"retained_related_memory_ids,omitempty"`
}

type jobDTO struct {
	ID               string                        `json:"id"`
	SourceID         string                        `json:"source_id"`
	Status           string                        `json:"status"`
	Attempts         int                           `json:"attempts"`
	ProtocolFailures int                           `json:"protocol_failures"`
	WaitReason       string                        `json:"wait_reason,omitempty"`
	Stage            string                        `json:"stage,omitempty"`
	ReasonCode       string                        `json:"reason_code,omitempty"`
	NextAttemptAt    *time.Time                    `json:"next_attempt_at,omitempty"`
	Error            string                        `json:"error,omitempty"`
	MemoryIDs        []string                      `json:"memory_ids"`
	ConflictIDs      []string                      `json:"conflict_ids"`
	PendingMemoryIDs []string                      `json:"pending_memory_ids"`
	Warnings         []string                      `json:"warnings"`
	QualityOutcome   string                        `json:"quality_outcome,omitempty"`
	ExtractionStats  jobExtractionStatsDTO         `json:"extraction_stats"`
	Retention        jobRetentionStatsDTO          `json:"retention"`
	Timings          jobTimingsDTO                 `json:"timings"`
	ModelUsage       jobModelUsageDTO              `json:"model_usage"`
	Efficiency       jobEfficiencyDTO              `json:"efficiency"`
	Diagnostics      []jobExtractionDiagnosticDTO  `json:"diagnostics"`
	Convergence      []jobConvergenceDiagnosticDTO `json:"convergence"`
	QueueAgeSeconds  int64                         `json:"queue_age_seconds"`
	StaleSeconds     int64                         `json:"stale_seconds"`
	RetryReason      string                        `json:"retry_reason,omitempty"`
	CreatedAt        time.Time                     `json:"created_at"`
	UpdatedAt        time.Time                     `json:"updated_at"`
	StartedAt        *time.Time                    `json:"started_at,omitempty"`
	FinishedAt       *time.Time                    `json:"finished_at,omitempty"`
}

type jobExtractionStatsDTO struct {
	RawCandidates           int `json:"raw_candidates"`
	ParsedCandidates        int `json:"parsed_candidates"`
	NormalizedCandidates    int `json:"normalized_candidates"`
	Normalizations          int `json:"normalizations"`
	RepairAttempts          int `json:"repair_attempts"`
	RepairedCandidates      int `json:"repaired_candidates"`
	RepairSucceeded         int `json:"repair_succeeded"`
	RepairFailed            int `json:"repair_failed"`
	RepairTimedOut          int `json:"repair_timed_out"`
	RepairInvalidOutput     int `json:"repair_invalid_output"`
	ExcludedCandidates      int `json:"excluded_candidates"`
	IsolatedCandidates      int `json:"isolated_candidates"`
	AcceptedCandidates      int `json:"accepted_candidates"`
	SkippedCandidates       int `json:"skipped_candidates"`
	MemoryWrites            int `json:"memory_writes"`
	ExtractionChunks        int `json:"extraction_chunks"`
	ExtractionChunkFailures int `json:"extraction_chunk_failures"`
	DroppedCandidates       int `json:"dropped_candidates"`
	CrossChunkDuplicates    int `json:"cross_chunk_duplicates"`
	ContextOnlyCandidates   int `json:"context_only_candidates"`
}

type jobRetentionStatsDTO struct {
	Retained            int            `json:"retained"`
	NotRetained         int            `json:"not_retained"`
	ByReason            map[string]int `json:"by_reason"`
	NotRetainedByReason map[string]int `json:"not_retained_by_reason"`
}

type jobTimingsDTO struct {
	QueueAgeMillis      int64 `json:"queue_age_ms"`
	ExtractionMillis    int64 `json:"extraction_ms"`
	NormalizationMillis int64 `json:"normalization_ms"`
	RepairMillis        int64 `json:"repair_ms"`
	ContextMillis       int64 `json:"context_ms"`
	DecisionMillis      int64 `json:"decision_ms"`
	CommitMillis        int64 `json:"commit_ms"`
	TotalMillis         int64 `json:"total_ms"`
}

type jobModelUsageDTO struct {
	Calls        int `json:"calls"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type jobEfficiencyDTO struct {
	Memories        int     `json:"memories"`
	ModelCalls      int     `json:"model_calls"`
	TotalTokens     int     `json:"total_tokens"`
	CallsPerMemory  float64 `json:"calls_per_memory"`
	TokensPerMemory float64 `json:"tokens_per_memory"`
}

type jobExtractionDiagnosticDTO struct {
	CandidateID string `json:"candidate_id,omitempty"`
	Field       string `json:"field,omitempty"`
	Code        string `json:"code"`
	Stage       string `json:"stage"`
	Disposition string `json:"disposition"`
}

type jobConvergenceDiagnosticDTO struct {
	CandidateID string `json:"candidate_id,omitempty"`
	GroupID     string `json:"group_id,omitempty"`
	Code        string `json:"code"`
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
	ID          string              `json:"id"`
	FactGroupID string              `json:"fact_group_id,omitempty"`
	Status      string              `json:"status"`
	ReasonCode  string              `json:"reason_code"`
	Resolution  string              `json:"resolution,omitempty"`
	Revision    int                 `json:"revision"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Members     []conflictMemberDTO `json:"members"`
	Events      []conflictEventDTO  `json:"events,omitempty"`
}

type conflictListResponse struct {
	Items []conflictDTO `json:"items"`
}

func memoryFromDomain(item memory.Memory) memoryDTO {
	return memoryDTO{
		ID:                 item.ID,
		Kind:               string(item.Kind),
		Phase:              string(item.Phase),
		Durability:         string(item.Durability),
		Title:              item.Title,
		Content:            item.Content,
		State:              string(item.State),
		AssertionStatus:    string(item.AssertionStatus),
		Version:            item.Version,
		ObservedAt:         item.ObservedAt.UTC(),
		ObservedAtBasis:    string(item.ObservedAtBasis),
		EventTime:          utcPointer(item.EventTime),
		EventTimeBasis:     string(item.EventTimeBasis),
		EventTimePrecision: string(item.EventTimePrecision),
		ValidFrom:          utcPointer(item.ValidFrom),
		ValidTo:            utcPointer(item.ValidTo),
		ValidFromBasis:     string(item.ValidFromBasis),
		ValidToBasis:       string(item.ValidToBasis),
		Temporal:           temporalDetailsFromDomain(item.Temporal),
		FactGroupID:        item.FactGroupID,
		CreatedAt:          item.CreatedAt.UTC(),
		UpdatedAt:          item.UpdatedAt.UTC(),
		CreatedByApp:       item.CreatedByApp,
		OriginSourceID:     item.OriginSourceID,
		ScopeKey:           item.ScopeKey,
		Procedure:          procedureFromDomain(item.Procedure),
		Qualifiers:         qualifiersFromDomain(item.Qualifiers),
		Topics:             nonNilStrings(item.Topics),
		RawTopics:          nonNilStrings(item.RawTopics),
		RetentionReason: string(memory.ClassifyRetention(item.Kind, item.Phase, item.Durability,
			item.EventTime, item.ValidTo, time.Now().UTC())),
	}
}

func temporalDetailsFromDomain(item memory.TemporalDetails) temporalDetailsDTO {
	return temporalDetailsDTO{
		Observed:  temporalDetailFromDomain(item.Observed),
		Event:     temporalDetailFromDomain(item.Event),
		ValidFrom: temporalDetailFromDomain(item.ValidFrom),
		ValidTo:   temporalDetailFromDomain(item.ValidTo),
	}
}

func temporalDetailFromDomain(item memory.TemporalDetail) temporalDetailDTO {
	return temporalDetailDTO{Text: item.Text, Precision: string(item.Precision), TimezoneKnown: item.TimezoneKnown}
}

func procedureFromDomain(item *memory.Procedure) *procedureDTO {
	if item == nil {
		return nil
	}
	return &procedureDTO{
		Goal:                 item.Goal,
		Steps:                nonNilStrings(item.Steps),
		Preconditions:        nonNilStrings(item.Preconditions),
		CompletionConditions: nonNilStrings(item.CompletionConditions),
		FailureBranches:      nonNilStrings(item.FailureBranches),
	}
}

func qualifiersFromDomain(item *memory.AssertionQualifiers) *qualifiersDTO {
	if item == nil {
		return nil
	}
	result := &qualifiersDTO{
		Entities:    nonNilStrings(item.Entities),
		Conditions:  nonNilStrings(item.Conditions),
		Polarity:    string(item.Polarity),
		Certainty:   string(item.Certainty),
		Attribution: item.Attribution,
	}
	if item.Quantity != nil {
		result.Quantity = &quantityDTO{Value: item.Quantity.Value, Unit: item.Quantity.Unit}
	}
	return result
}

func relationsFromDomain(items []memory.Relation) []relationDTO {
	result := make([]relationDTO, 0, len(items))
	for _, item := range items {
		result = append(result, relationDTO{
			ID:                 item.ID,
			FromMemoryID:       item.FromMemoryID,
			FromVersion:        item.FromVersion,
			ToMemoryID:         item.ToMemoryID,
			ToVersion:          item.ToVersion,
			Kind:               string(item.Kind),
			CreatedAt:          item.CreatedAt.UTC(),
			FromVersionChanged: item.FromVersionChanged,
			ToVersionChanged:   item.ToVersionChanged,
		})
	}
	return result
}

func evolutionFromDomain(items []memory.EvolutionEvent) []evolutionDTO {
	result := make([]evolutionDTO, 0, len(items))
	for _, item := range items {
		result = append(result, evolutionDTO{
			ID: item.ID, FactGroupID: item.FactGroupID,
			FromMemoryID: item.FromMemoryID, FromVersion: item.FromVersion,
			ToMemoryID: item.ToMemoryID, ToVersion: item.ToVersion,
			Action: item.Action, ReasonCode: item.ReasonCode,
			CreatedAt: item.CreatedAt.UTC(),
		})
	}
	return result
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
		MessageID: item.MessageID,
		Quote:     item.Quote,
		ModelName: item.ModelName,
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
		temporalStatus := item.Memory.TemporalStatus()
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
		var related *relatedDTO
		if item.Related != nil {
			related = &relatedDTO{
				ReasonCodes:     nonNilStrings(item.Related.ReasonCodes),
				AnchorMemoryIDs: nonNilStrings(item.Related.AnchorMemoryIDs),
			}
		}
		var cluster *displayClusterDTO
		if item.Cluster != nil {
			cluster = &displayClusterDTO{
				FactGroupID:   item.Cluster.FactGroupID,
				MemberIDs:     nonNilStrings(item.Cluster.MemberIDs),
				EvidenceCount: item.Cluster.EvidenceCount,
			}
		}
		items = append(items, searchItemDTO{
			Memory:         memoryFromDomain(item.Memory),
			Score:          item.Score,
			Snippet:        item.Snippet,
			MatchedTerms:   matchedTerms,
			EvidenceRefs:   evidenceRefs,
			Explanation:    explanation,
			TemporalStatus: temporalStatus,
			ConflictRefs:   conflictRefs,
			Related:        related,
			Cluster:        cluster,
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
			FactGroupID: conflict.FactGroupID,
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
		SemanticCandidates:     value.SemanticCandidates,
		SemanticExcluded:       value.SemanticExcluded,
		TermCandidates:         value.TermCandidates,
		KindFilterExcluded:     value.KindFilterExcluded,
		StateFilterExcluded:    value.StateFilterExcluded,
		TimeFilterExcluded:     value.TimeFilterExcluded,
		RankingCandidates:      value.RankingCandidates,
		RelatedExpanded:        value.RelatedExpanded,
		RelatedExcluded:        value.RelatedExcluded,
		ReturnedItems:          value.ReturnedItems,
		ContextBudgetTruncated: value.ContextBudgetTruncated,
		SemanticDegradation:    value.SemanticDegradation,
	}
}

func artifactItemsFromDomain(items []artifact.SearchHit) []artifactItemDTO {
	result := make([]artifactItemDTO, 0, len(items))
	for _, item := range items {
		result = append(result, artifactItemDTO{
			ID:         item.Artifact.ID,
			Kind:       string(item.Artifact.Kind),
			Version:    item.Revision.Version,
			Title:      item.Revision.Title,
			Content:    item.Revision.Content,
			Payload:    meaningfulPayload(item.Revision.Payload),
			ScopeKey:   item.Artifact.ScopeKey,
			Topic:      item.Artifact.Topic,
			State:      string(item.Artifact.State),
			Truncated:  item.Revision.Truncated,
			MemoryRefs: memoryRefsFromDomain(item.Revision.MemoryRefs),
			RunRefs:    runRefsFromDomain(item.Revision.RunRefs),
			UpdatedAt:  item.Artifact.UpdatedAt.UTC(),
			Score:      item.Score,
		})
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

func meaningfulPayload(payload json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil
	}
	return payload
}

func artifactMetaFromDomain(item artifact.Artifact) artifactMetaDTO {
	return artifactMetaDTO{
		ID:             item.ID,
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

func artifactRevisionFromDomain(item artifact.Revision) artifactRevisionDTO {
	payload := item.Payload
	if len(payload) == 0 {
		payload = nil
	}
	return artifactRevisionDTO{
		ArtifactID:       item.ArtifactID,
		Version:          item.Version,
		Title:            item.Title,
		Content:          item.Content,
		Payload:          payload,
		ContentHash:      item.ContentHash,
		Generator:        item.Generator,
		ProtocolVersion:  item.ProtocolVersion,
		TaxonomyVersion:  item.TaxonomyVersion,
		GeneratorVersion: item.GeneratorVersion,
		RunID:            item.RunID,
		Provenance:       item.Provenance,
		ReasonCode:       item.ReasonCode,
		Truncated:        item.Truncated,
		MemoryRefs:       memoryRefsFromDomain(item.MemoryRefs),
		RunRefs:          runRefsFromDomain(item.RunRefs),
		CreatedAt:        item.CreatedAt.UTC(),
	}
}

func artifactDetailFromDomain(item artifact.Detail) artifactDetailResponse {
	result := artifactDetailResponse{Artifact: artifactMetaFromDomain(item.Artifact)}
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
		if record.Artifact != nil {
			meta := artifactMetaFromDomain(*record.Artifact)
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
		Memories:    atomic.Items,
		Summaries:   artifactItemsFromDomain(result.Summaries),
		Experiences: artifactItemsFromDomain(result.Experiences),
		Skills:      artifactItemsFromDomain(result.Skills),
		Conflicts:   atomic.Conflicts,
		Warnings:    nonNilStrings(atomic.Warnings),
		Diagnostics: atomic.Diagnostics,
	}
}

func sourceFromDomain(item source.Source) sourceDTO {
	result := sourceDTO{
		ID:         item.ID,
		AppID:      item.AppID,
		ExternalID: item.ExternalID,
		ScopeKey:   item.ScopeKey,
		Content:    item.Content,
		Redacted:   item.Redacted,
		CreatedAt:  item.CreatedAt.UTC(),
	}
	if item.RedactedAt != nil {
		redactedAt := item.RedactedAt.UTC()
		result.RedactedAt = &redactedAt
	}
	return result
}

func redactFromDomain(item source.RedactResult) redactResponse {
	return redactResponse{
		ID:            item.SourceID,
		Redacted:      item.Redacted,
		CancelledJobs: item.CancelledJobs,
	}
}

func jobFromDomain(item ingest.Job) jobDTO {
	memoryIDs := item.MemoryIDs
	if memoryIDs == nil {
		memoryIDs = []string{}
	}
	diagnostics := make([]jobExtractionDiagnosticDTO, 0, len(item.Diagnostics))
	for _, diagnostic := range item.Diagnostics {
		diagnostics = append(diagnostics, jobExtractionDiagnosticDTO{
			CandidateID: diagnostic.CandidateID,
			Field:       diagnostic.Field,
			Code:        diagnostic.Code,
			Stage:       diagnostic.Stage,
			Disposition: diagnostic.Disposition,
		})
	}
	convergence := make([]jobConvergenceDiagnosticDTO, 0, len(item.Convergence))
	for _, diagnostic := range item.Convergence {
		convergence = append(convergence, jobConvergenceDiagnosticDTO{
			CandidateID: diagnostic.CandidateID,
			GroupID:     diagnostic.GroupID,
			Code:        diagnostic.Code,
		})
	}
	item.Retention.Normalize()
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
	memories := len(item.MemoryIDs)
	efficiency := jobEfficiencyDTO{Memories: memories, ModelCalls: item.ModelUsage.Calls,
		TotalTokens: item.ModelUsage.TotalTokens()}
	if memories > 0 {
		efficiency.CallsPerMemory = float64(item.ModelUsage.Calls) / float64(memories)
		efficiency.TokensPerMemory = float64(item.ModelUsage.TotalTokens()) / float64(memories)
	}
	result := jobDTO{
		ID:               item.ID,
		SourceID:         item.SourceID,
		Status:           string(item.Status),
		Attempts:         item.Attempts,
		ProtocolFailures: item.ProtocolFailures,
		WaitReason:       item.WaitReason,
		Stage:            item.Stage,
		ReasonCode:       item.ReasonCode,
		RetryReason:      retryReason,
		NextAttemptAt:    utcPointer(item.NextAttemptAt),
		Error:            item.Error,
		MemoryIDs:        memoryIDs,
		ConflictIDs:      nonNilStrings(item.ConflictIDs),
		PendingMemoryIDs: nonNilStrings(item.PendingMemoryIDs),
		Warnings:         nonNilStrings(item.Warnings),
		QualityOutcome:   string(item.QualityOutcome),
		ExtractionStats: jobExtractionStatsDTO{
			RawCandidates:           item.ExtractionStats.RawCandidates,
			ParsedCandidates:        item.ExtractionStats.ParsedCandidates,
			NormalizedCandidates:    item.ExtractionStats.NormalizedCandidates,
			Normalizations:          item.ExtractionStats.Normalizations,
			RepairAttempts:          item.ExtractionStats.RepairAttempts,
			RepairedCandidates:      item.ExtractionStats.RepairedCandidates,
			RepairSucceeded:         item.ExtractionStats.RepairSucceeded,
			RepairFailed:            item.ExtractionStats.RepairFailed,
			RepairTimedOut:          item.ExtractionStats.RepairTimedOut,
			RepairInvalidOutput:     item.ExtractionStats.RepairInvalidOutput,
			ExcludedCandidates:      item.ExtractionStats.ExcludedCandidates,
			IsolatedCandidates:      item.ExtractionStats.IsolatedCandidates,
			AcceptedCandidates:      item.ExtractionStats.AcceptedCandidates,
			SkippedCandidates:       item.ExtractionStats.SkippedCandidates,
			MemoryWrites:            item.ExtractionStats.MemoryWrites,
			ExtractionChunks:        item.ExtractionStats.ExtractionChunks,
			ExtractionChunkFailures: item.ExtractionStats.ExtractionChunkFailures,
			DroppedCandidates:       item.ExtractionStats.DroppedCandidates,
			CrossChunkDuplicates:    item.ExtractionStats.CrossChunkDuplicates,
			ContextOnlyCandidates:   item.ExtractionStats.ContextOnlyCandidates,
		},
		Retention: jobRetentionStatsDTO{
			Retained:            item.Retention.Retained,
			NotRetained:         item.Retention.NotRetained,
			ByReason:            nonNilCounts(item.Retention.ByReason),
			NotRetainedByReason: nonNilCounts(item.Retention.NotRetainedByReason),
		},
		Timings: jobTimingsDTO{
			QueueAgeMillis:      item.Timings.QueueAgeMillis,
			ExtractionMillis:    item.Timings.ExtractionMillis,
			NormalizationMillis: item.Timings.NormalizationMillis,
			RepairMillis:        item.Timings.RepairMillis,
			ContextMillis:       item.Timings.ContextMillis,
			DecisionMillis:      item.Timings.DecisionMillis,
			CommitMillis:        item.Timings.CommitMillis,
			TotalMillis:         item.Timings.TotalMillis,
		},
		ModelUsage: jobModelUsageDTO{
			Calls: item.ModelUsage.Calls, InputTokens: item.ModelUsage.InputTokens,
			OutputTokens: item.ModelUsage.OutputTokens, TotalTokens: item.ModelUsage.TotalTokens(),
		},
		Efficiency:      efficiency,
		Diagnostics:     diagnostics,
		Convergence:     convergence,
		QueueAgeSeconds: queueAgeSeconds,
		StaleSeconds:    staleSeconds,
		CreatedAt:       item.CreatedAt.UTC(),
		UpdatedAt:       item.UpdatedAt.UTC(),
		StartedAt:       utcPointer(item.StartedAt),
		FinishedAt:      utcPointer(item.FinishedAt),
	}
	return result
}

func nonNilCounts(values map[string]int) map[string]int {
	if values == nil {
		return map[string]int{}
	}
	return values
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
		ID:          item.ID,
		FactGroupID: item.FactGroupID,
		Status:      string(item.Status),
		ReasonCode:  string(item.ReasonCode),
		Resolution:  item.Resolution,
		Revision:    item.Revision,
		CreatedAt:   item.CreatedAt.UTC(),
		UpdatedAt:   item.UpdatedAt.UTC(),
		Members:     members,
		Events:      events,
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
