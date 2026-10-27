package httptransport

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/N0rthHXD/dmemd/internal/artifact"
	"github.com/N0rthHXD/dmemd/internal/governance"
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

type memoryDTO struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Phase           string     `json:"phase"`
	Durability      string     `json:"durability"`
	Title           string     `json:"title"`
	Content         string     `json:"content"`
	State           string     `json:"state"`
	AssertionStatus string     `json:"assertion_status"`
	Version         int        `json:"version"`
	ObservedAt      time.Time  `json:"observed_at"`
	ValidFrom       *time.Time `json:"valid_from"`
	ValidTo         *time.Time `json:"valid_to"`
	ValidFromBasis  string     `json:"valid_from_basis"`
	ValidToBasis    string     `json:"valid_to_basis"`
	FactGroupID     string     `json:"fact_group_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CreatedByApp    string     `json:"created_by_app,omitempty"`
	OriginSourceID  string     `json:"origin_source_id,omitempty"`
	ScopeKey        string     `json:"scope_key,omitempty"`
	Topics          []string   `json:"topics"`
	RawTopics       []string   `json:"raw_topics"`
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

type memoryDetailDTO struct {
	memoryDTO
	Relations []relationDTO `json:"relations"`
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

type searchItemDTO struct {
	Memory         memoryDTO        `json:"memory"`
	Score          float64          `json:"score"`
	Snippet        string           `json:"snippet"`
	MatchedTerms   []string         `json:"matched_terms"`
	EvidenceRefs   []evidenceRefDTO `json:"evidence_refs"`
	Explanation    *explanationDTO  `json:"explanation,omitempty"`
	TemporalStatus string           `json:"temporal_status"`
	ConflictRefs   []conflictRefDTO `json:"conflict_refs"`
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
	Items     []searchItemDTO      `json:"items"`
	Retrieval retrievalDTO         `json:"retrieval"`
	Warnings  []string             `json:"warnings,omitempty"`
	Conflicts []conflictSummaryDTO `json:"conflicts"`
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

type recallResponse struct {
	Memories    []searchItemDTO      `json:"memories"`
	Summaries   []artifactItemDTO    `json:"summaries"`
	Experiences []artifactItemDTO    `json:"experiences"`
	Skills      []artifactItemDTO    `json:"skills"`
	Conflicts   []conflictSummaryDTO `json:"conflicts"`
	Warnings    []string             `json:"warnings"`
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
	ID               string     `json:"id"`
	SourceID         string     `json:"source_id"`
	Status           string     `json:"status"`
	Attempts         int        `json:"attempts"`
	ProtocolFailures int        `json:"protocol_failures"`
	WaitReason       string     `json:"wait_reason,omitempty"`
	Stage            string     `json:"stage,omitempty"`
	ReasonCode       string     `json:"reason_code,omitempty"`
	NextAttemptAt    *time.Time `json:"next_attempt_at,omitempty"`
	Error            string     `json:"error,omitempty"`
	MemoryIDs        []string   `json:"memory_ids"`
	ConflictIDs      []string   `json:"conflict_ids"`
	PendingMemoryIDs []string   `json:"pending_memory_ids"`
	Warnings         []string   `json:"warnings"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
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
		ID:              item.ID,
		Kind:            string(item.Kind),
		Phase:           string(item.Phase),
		Durability:      string(item.Durability),
		Title:           item.Title,
		Content:         item.Content,
		State:           string(item.State),
		AssertionStatus: string(item.AssertionStatus),
		Version:         item.Version,
		ObservedAt:      item.ObservedAt.UTC(),
		ValidFrom:       utcPointer(item.ValidFrom),
		ValidTo:         utcPointer(item.ValidTo),
		ValidFromBasis:  string(item.ValidFromBasis),
		ValidToBasis:    string(item.ValidToBasis),
		FactGroupID:     item.FactGroupID,
		CreatedAt:       item.CreatedAt.UTC(),
		UpdatedAt:       item.UpdatedAt.UTC(),
		CreatedByApp:    item.CreatedByApp,
		OriginSourceID:  item.OriginSourceID,
		ScopeKey:        item.ScopeKey,
		Topics:          nonNilStrings(item.Topics),
		RawTopics:       nonNilStrings(item.RawTopics),
	}
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
		items = append(items, searchItemDTO{
			Memory:         memoryFromDomain(item.Memory),
			Score:          item.Score,
			Snippet:        item.Snippet,
			MatchedTerms:   matchedTerms,
			EvidenceRefs:   evidenceRefs,
			Explanation:    explanation,
			TemporalStatus: temporalStatus,
			ConflictRefs:   conflictRefs,
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
		Items:     items,
		Retrieval: retrievalDTO{Mode: mode},
		Warnings:  warnings,
		Conflicts: conflicts,
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

func recallFromDomain(result retrieval.RecallResult) recallResponse {
	atomic := searchResultFromDomain(retrieval.Result{
		Items:     result.Memories,
		Conflicts: result.Conflicts,
		Warnings:  result.Warnings,
	})
	return recallResponse{
		Memories:    atomic.Items,
		Summaries:   artifactItemsFromDomain(result.Summaries),
		Experiences: artifactItemsFromDomain(result.Experiences),
		Skills:      artifactItemsFromDomain(result.Skills),
		Conflicts:   atomic.Conflicts,
		Warnings:    nonNilStrings(atomic.Warnings),
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
	result := jobDTO{
		ID:               item.ID,
		SourceID:         item.SourceID,
		Status:           string(item.Status),
		Attempts:         item.Attempts,
		ProtocolFailures: item.ProtocolFailures,
		WaitReason:       item.WaitReason,
		Stage:            item.Stage,
		ReasonCode:       item.ReasonCode,
		NextAttemptAt:    utcPointer(item.NextAttemptAt),
		Error:            item.Error,
		MemoryIDs:        memoryIDs,
		ConflictIDs:      nonNilStrings(item.ConflictIDs),
		PendingMemoryIDs: nonNilStrings(item.PendingMemoryIDs),
		Warnings:         nonNilStrings(item.Warnings),
		CreatedAt:        item.CreatedAt.UTC(),
		UpdatedAt:        item.UpdatedAt.UTC(),
		StartedAt:        utcPointer(item.StartedAt),
		FinishedAt:       utcPointer(item.FinishedAt),
	}
	return result
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
